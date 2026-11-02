// Command api is slotting-optimization's composition root: it wires env
// config into adapters, adapters into use cases, and use cases into the HTTP
// router, the four local-copy Kafka consumers (ADR 0003, ADR 0010) and the
// outbox relay, all on real Postgres and real Kafka (ADR 0011): there is no
// in-memory mode, no log sink and no consumer that is off.
//
// Environment:
//
//	HTTP_ADDR                   listen address (default :8080)
//	DATABASE_URL                Postgres DSN (REQUIRED: there is no in-memory mode, ADR 0011)
//	MIGRATIONS_DATABASE_URL     direct DSN for the boot migrations (default DATABASE_URL)
//	KAFKA_BROKERS               comma-separated brokers (REQUIRED: relay, consumers, /readyz)
//	EVENT_PUBLISHER             kafka (default kafka); log is a boot error
//	OUTBOX_RELAY_INTERVAL       relay poll interval (Go duration or seconds, default 1s)
//	OUTBOX_MAX_ATTEMPTS         failures before the relay excludes a row (default 20, 0 = unlimited; ADR 0008)
//	DEMAND_SITE_ID              the site plans default to; when set, demand for other sites is ignored
//	LOOKBACK_DAYS               default demand window of a new plan (default 28)
//	FORWARD_ZONE_CODES          comma-separated zone codes of the forward pick slots (default FWD)
//	CASE_PICK_ZONE_CODES        comma-separated zone codes whose faces are CASE-pick faces (default empty:
//	                            every face is an EACH-pick face and plans are what they were; ADR 0012)
//	DEMAND_MODE, PRODUCT_MODE, LAYOUT_MODE, RELOCATION_MODE, PACK_LEVEL_MODE
//	                            kafka (default kafka); permissive and off are boot errors
//	DEMAND_CONSUMER_GROUP, PRODUCT_CONSUMER_GROUP, LAYOUT_CONSUMER_GROUP, RELOCATION_CONSUMER_GROUP, PACK_LEVEL_CONSUMER_GROUP
//	                            stable group ids (ALL REQUIRED, boot error naming the variable)
//	                            (RELOCATION_*: inventory-storage's StockRelocated -> plan execution progress, ADR 0010)
//	                            (PACK_LEVEL_*: product-master's ProductPackLevel* -> the local PackLevelView, ADR 0012)
//	OTEL_EXPORTER_OTLP_ENDPOINT OTLP/gRPC collector (default localhost:4317)
//	SHUTDOWN_DRAIN_DELAY        wait after flipping /readyz before closing (default 5s)
//	LOG_LEVEL, SERVICE_VERSION, ENVIRONMENT, CORS_ALLOWED_ORIGINS
//
// GET /readyz is 200 only while Postgres answers a ping AND a Kafka broker
// answers a metadata request (verdict cached 3s, bounded 2s); /healthz is
// liveness only. The process exits non-zero when the relay or a consumer
// returns on its own, so the orchestrator restarts it.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	inboundhttp "github.com/claudioed/slotting-optimization/internal/adapters/inbound/http"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/telemetry"
	"github.com/claudioed/slotting-optimization/internal/config"
)

// shutdownTimeout bounds the HTTP server's graceful drain.
const shutdownTimeout = 10 * time.Second

// workerDrainTimeout bounds how long shutdown waits for the consumers and the
// relay to return after their context is cancelled.
const workerDrainTimeout = 10 * time.Second

func main() {
	if err := run(context.Background()); err != nil {
		slog.Error("service exited with error", "error", err)
		os.Exit(1)
	}
}

// run boots the service and serves until SIGINT/SIGTERM or until ctx is
// cancelled (the integration test stops the real entry point that way).
func run(ctx context.Context) error {
	// The boot logger stays at info until the configured level is known.
	srv := config.ServerFromEnv(newLogger("info"))
	logger := newLogger(srv.LogLevel)
	slog.SetDefault(logger)

	// Misconfiguration is refused before anything is opened or bound (ADR 0011).
	rt, err := config.LoadRuntime(os.Getenv, consumerEnvs())
	if err != nil {
		return err
	}

	otelShutdown, err := setupTelemetry(logger, srv)
	if err != nil {
		return err
	}
	defer otelShutdown()

	// closeAdapters (pool.Close) is deferred FIRST so it runs LAST, after the
	// HTTP drain, the consumers and the relay have all stopped.
	ad, closeAdapters, err := buildAdapters(ctx, os.Getenv("DATABASE_URL"), logger)
	if err != nil {
		return err
	}
	defer closeAdapters()

	metrics, err := telemetry.NewMetricsHandler()
	if err != nil {
		return fmt.Errorf("metrics handler: %w", err)
	}
	planDecisionMetrics, err := telemetry.NewPlanDecisionMetrics()
	if err != nil {
		return fmt.Errorf("plan decision metrics: %w", err)
	}
	readiness := newReadiness(ad.pool.Ping, rt.Brokers)
	server, err := buildServer(ad, config.FromEnv(logger), readiness, metrics, planDecisionMetrics)
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Addr:              srv.HTTPAddr,
		Handler:           inboundhttp.NewRouter(server),
		ReadHeaderTimeout: 5 * time.Second,
	}

	consumers, closeConsumers := startConsumers(ad, rt, logger)
	relay, closeRelay, err := startOutboxRelay(ad.outboxStore, rt.Brokers, logger)
	if err != nil {
		closeConsumers()
		return err
	}
	return serveUntilSignal(ctx, logger, httpServer, readiness, consumers, closeConsumers, relay, closeRelay, srv.ShutdownDrainDelay)
}

// serveUntilSignal runs httpServer until SIGINT/SIGTERM (or a listen error),
// then shuts down in the fleet order: (1) /readyz flips to 503, (2) wait the
// drain delay, (3) drain HTTP, (4) stop and await the consumers, (5) stop and
// await the relay LAST so nothing committed above is stranded; the caller's
// deferred pool.Close runs after all of it.
func serveUntilSignal(ctx context.Context, logger *slog.Logger, httpServer *http.Server, readiness *inboundhttp.Readiness,
	consumers []*worker, closeConsumers func(), relay *worker, closeRelay func(), drainDelay time.Duration,
) error {
	errCh := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	died := watchWorkers(append([]*worker{relay}, consumers...)...)
	select {
	case err := <-errCh:
		closeConsumers()
		closeRelay()
		return err
	case name := <-died:
		// A consumer or the relay returned on its own (a lost broker, a
		// failed reader): the service cannot run without it and must not sit
		// "alive" with a dead worker, so exit non-zero and let the
		// orchestrator restart it.
		closeConsumers()
		closeRelay()
		return fmt.Errorf("%s stopped unexpectedly: exiting so the orchestrator restarts the service", name)
	case <-sigCtx.Done():
	}

	readiness.SetNotReady()
	if drainDelay > 0 {
		logger.Info("shutdown: readiness flipped to not-ready; waiting for traffic to drain", "drain_delay", drainDelay.String())
		time.Sleep(drainDelay)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	err := httpServer.Shutdown(shutdownCtx)

	closeConsumers()
	for _, w := range consumers {
		await(logger, w)
	}
	closeRelay()
	await(logger, relay)
	return err
}

// watchWorkers returns a channel that receives the name of every worker that
// returns. Workers only return when stopped or on an unrecoverable failure, so
// a value read while the service is serving means a worker died.
func watchWorkers(workers ...*worker) <-chan string {
	died := make(chan string, len(workers))
	for _, w := range workers {
		if w == nil {
			continue
		}
		go func() {
			<-w.done
			died <- w.name
		}()
	}
	return died
}

// await waits (bounded) for w to return; nil w is a no-op.
func await(logger *slog.Logger, w *worker) {
	if w == nil {
		return
	}
	select {
	case <-w.done:
	case <-time.After(workerDrainTimeout):
		logger.Warn("worker did not stop before the shutdown drain deadline", "worker", w.name)
	}
}

// setupTelemetry installs the OTLP trace and metric providers (never blocks
// on a missing Collector) and returns a bounded flush-on-exit closer.
func setupTelemetry(logger *slog.Logger, srv config.Server) (func(), error) {
	otelCtx, otelCancel := context.WithTimeout(context.Background(), 10*time.Second)
	endpoint := srv.OTLPEndpoint
	if endpoint == "" {
		endpoint = telemetry.DefaultOTLPEndpoint
	}
	otelShutdown, err := telemetry.Setup(otelCtx, inboundhttp.DefaultServiceName, srv.ServiceVersion, endpoint)
	otelCancel()
	if err != nil {
		return nil, fmt.Errorf("telemetry setup: %w", err)
	}
	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := otelShutdown(shutdownCtx); err != nil {
			logger.Error("telemetry shutdown failed", "error", err)
		}
	}, nil
}

// newLogger builds the JSON logger; LOG_LEVEL debug|info|warn|warning|error
// (case-insensitive; "warning" is accepted like "warn" fleet-wide).
func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
