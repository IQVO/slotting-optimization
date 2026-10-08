// Command api is slotting-optimization's composition root: it wires env
// config into adapters, adapters into use cases, and use cases into the HTTP
// router, the three local-copy Kafka consumers (ADR 0003) and the outbox
// relay.
//
// Environment:
//
//	HTTP_ADDR                   listen address (default :8080)
//	DATABASE_URL                Postgres DSN; unset = in-memory adapters
//	MIGRATIONS_DATABASE_URL     direct DSN for the boot migrations (default DATABASE_URL)
//	EVENT_PUBLISHER             kafka | log (default log)
//	KAFKA_BROKERS               comma-separated brokers (EVENT_PUBLISHER=kafka, any *_MODE=kafka)
//	OUTBOX_RELAY_INTERVAL       relay poll interval (Go duration or seconds, default 1s)
//	DEMAND_SITE_ID              the site plans default to; when set, demand for other sites is ignored
//	LOOKBACK_DAYS               default demand window of a new plan (default 28)
//	FORWARD_ZONE_CODES          comma-separated zone codes of the forward pick slots (default FWD)
//	DEMAND_MODE, PRODUCT_MODE, LAYOUT_MODE
//	                            kafka | permissive (default permissive = consumer not started)
//	DEMAND_CONSUMER_GROUP, PRODUCT_CONSUMER_GROUP, LAYOUT_CONSUMER_GROUP
//	                            stable group ids; required when the mode is kafka
//	OTEL_EXPORTER_OTLP_ENDPOINT OTLP/gRPC collector (default localhost:4317)
//	SHUTDOWN_DRAIN_DELAY        wait after flipping /readyz before closing (default 5s)
//	LOG_LEVEL, SERVICE_VERSION, ENVIRONMENT, CORS_ALLOWED_ORIGINS
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	inboundhttp "github.com/claudioed/slotting-optimization/internal/adapters/inbound/http"
	inboundkafka "github.com/claudioed/slotting-optimization/internal/adapters/inbound/kafka"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/clock"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/ids"
	outboundkafka "github.com/claudioed/slotting-optimization/internal/adapters/outbound/kafka"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/memory"
	outboxrelay "github.com/claudioed/slotting-optimization/internal/adapters/outbound/outbox"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/postgres"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/postgres/pgtx"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/telemetry"
	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/bootretry"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
)

// shutdownTimeout bounds the HTTP server's graceful drain.
const shutdownTimeout = 10 * time.Second

// workerDrainTimeout bounds how long shutdown waits for the consumers and the
// relay to return after their context is cancelled.
const workerDrainTimeout = 10 * time.Second

// DefaultShutdownDrainDelay is how long shutdown waits, after flipping
// /readyz to not-ready, before closing the listener (SHUTDOWN_DRAIN_DELAY
// overrides it; "0" disables it).
const DefaultShutdownDrainDelay = 5 * time.Second

// defaultForwardZoneCode is the zone code of the forward pick slots when
// FORWARD_ZONE_CODES is unset.
const defaultForwardZoneCode = "FWD"

func main() {
	if err := run(); err != nil {
		slog.Error("service exited with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	logger := newLogger(getenv("LOG_LEVEL", "info"))
	slog.SetDefault(logger)

	otelShutdown, err := setupTelemetry(logger)
	if err != nil {
		return err
	}
	defer otelShutdown()

	// closeAdapters (pool.Close) is deferred FIRST so it runs LAST, after the
	// HTTP drain, the consumers and the relay have all stopped.
	ad, closeAdapters, err := buildAdapters(context.Background(), os.Getenv("DATABASE_URL"), logger)
	if err != nil {
		return err
	}
	defer closeAdapters()

	metrics, err := telemetry.NewMetricsHandler()
	if err != nil {
		return fmt.Errorf("metrics handler: %w", err)
	}
	readiness := &inboundhttp.Readiness{}
	server, err := buildServer(ad, configFromEnv(logger), readiness, metrics)
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Addr:              getenv("HTTP_ADDR", ":8080"),
		Handler:           inboundhttp.NewRouter(server),
		ReadHeaderTimeout: 5 * time.Second,
	}

	consumers, closeConsumers, err := startConsumers(ad, logger)
	if err != nil {
		return err
	}
	relay, closeRelay, err := startOutboxRelay(ad.outboxStore, logger)
	if err != nil {
		closeConsumers()
		return err
	}
	return serveUntilSignal(logger, httpServer, readiness, consumers, closeConsumers, relay, closeRelay)
}

// adapters is the set of outbound adapters the composition root wires.
type adapters struct {
	plans       ports.SlotPlanRepository
	demand      ports.DemandLedger
	profiles    ports.ProfileDirectory
	catalogue   ports.SlotCatalogue
	outbox      ports.OutboxRepository
	outboxStore outboxrelay.Store
	processed   ports.ProcessedEvents
	uow         ports.UnitOfWork
	// idempotency is the Idempotency-Key middleware of POST /slot-plans; nil
	// in the in-memory development mode.
	idempotency func(http.Handler) http.Handler
}

// writer bundles the ports every write use case needs.
func (a adapters) writer() usecases.Writer {
	return usecases.Writer{Plans: a.plans, Outbox: a.outbox, Encoder: outboundkafka.NewEncoder(), UoW: a.uow, Clock: clock.System{}}
}

// intake bundles the ports every consumer use case needs.
func (a adapters) intake() usecases.Intake {
	return usecases.Intake{UoW: a.uow, Processed: a.processed}
}

// config is the use-case configuration read from the environment.
type config struct {
	defaultSite      string
	onlySite         string
	lookbackDays     int
	forwardZoneCodes []string
}

func configFromEnv(logger *slog.Logger) config {
	site := os.Getenv("DEMAND_SITE_ID")
	return config{
		defaultSite:      getenv("DEMAND_SITE_ID", os.Getenv("DEFAULT_SITE_ID")),
		onlySite:         site,
		lookbackDays:     parseLookbackDays(os.Getenv("LOOKBACK_DAYS"), logger),
		forwardZoneCodes: parseZoneCodes(os.Getenv("FORWARD_ZONE_CODES")),
	}
}

// parseLookbackDays reads LOOKBACK_DAYS; unset, malformed or out of range
// falls back to the use case default (0 = DefaultLookbackDays).
func parseLookbackDays(raw string, logger *slog.Logger) int {
	if raw == "" {
		return 0
	}
	days, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || days < 1 || days > usecases.MaxLookbackDays {
		logger.Warn("invalid LOOKBACK_DAYS; using the default", "value", raw, "default", usecases.DefaultLookbackDays)
		return 0
	}
	return days
}

// parseZoneCodes reads FORWARD_ZONE_CODES (comma separated), defaulting to
// FWD.
func parseZoneCodes(raw string) []string {
	var codes []string
	for _, c := range strings.Split(raw, ",") {
		if c = strings.TrimSpace(c); c != "" {
			codes = append(codes, c)
		}
	}
	if len(codes) == 0 {
		return []string{defaultForwardZoneCode}
	}
	return codes
}

func buildServer(ad adapters, cfg config, readiness *inboundhttp.Readiness, metrics http.Handler) (*inboundhttp.Server, error) {
	planner, err := planning.NewPlanner(planning.LexicalRanking{})
	if err != nil {
		return nil, fmt.Errorf("planner: %w", err)
	}
	w := ad.writer()
	return &inboundhttp.Server{
		GeneratePlan: &usecases.GeneratePlan{
			Writer: w, Demand: ad.demand, Profiles: ad.profiles, Catalogue: ad.catalogue, IDs: ids.UUID{}, Planner: planner,
			DefaultSite: cfg.defaultSite, ForwardZoneCodes: cfg.forwardZoneCodes, DefaultLookback: cfg.lookbackDays,
		},
		ApprovePlan:      &usecases.ApprovePlan{Writer: w},
		RejectPlan:       &usecases.RejectPlan{Writer: w},
		GetPlan:          &usecases.GetPlan{Plans: ad.plans},
		ListPlans:        &usecases.ListPlans{Plans: ad.plans},
		ListForwardSlots: &usecases.ListForwardSlots{Plans: ad.plans, DefaultSite: cfg.defaultSite},
		ListSkuVelocity:  &usecases.ListSkuVelocity{Demand: ad.demand, Clock: clock.System{}, DefaultSite: cfg.defaultSite},
		Idempotency:      ad.idempotency,
		Readiness:        readiness,
		Metrics:          metrics,
	}, nil
}

// buildAdapters wires Postgres when databaseURL is set (running the embedded
// migrations first) or in-memory adapters otherwise.
func buildAdapters(ctx context.Context, databaseURL string, logger *slog.Logger) (adapters, func(), error) {
	noop := func() {}
	if databaseURL == "" {
		logger.Info("DATABASE_URL not configured; using in-memory adapters")
		plans, ob, processed := memory.NewSlotPlanRepo(), memory.NewOutboxRepo(), memory.NewProcessedEventRepo()
		demand, profiles, catalogue := memory.NewDemandLedger(), memory.NewProfileDirectory(), memory.NewSlotCatalogue()
		return adapters{
			plans: plans, demand: demand, profiles: profiles, catalogue: catalogue,
			outbox: ob, outboxStore: ob, processed: processed,
			uow: memory.NewUnitOfWork(plans, ob, demand, profiles, catalogue, processed),
		}, noop, nil
	}

	// The first outbound dial of an injected pod can be reset (Istio native
	// sidecars), so migrations and the first ping retry with backoff; on
	// exhaustion the LAST error is returned and the process refuses to boot.
	migrationsURL := migrationsDatabaseURL(databaseURL)
	if err := bootretry.Retry(ctx, logger, "run migrations", func() error {
		return postgres.RunMigrations(migrationsURL)
	}); err != nil {
		return adapters{}, noop, err
	}
	pool, err := postgres.NewPool(ctx, databaseURL)
	if err != nil {
		return adapters{}, noop, err
	}
	if err := bootretry.Retry(ctx, logger, "ping postgres", func() error { return pool.Ping(ctx) }); err != nil {
		pool.Close()
		return adapters{}, noop, err
	}
	logger.Info("postgres adapters configured")
	return postgresAdapters(pool), pool.Close, nil
}

func postgresAdapters(pool *pgxpool.Pool) adapters {
	ob := postgres.NewOutboxRepo(pool)
	return adapters{
		plans: postgres.NewSlotPlanRepo(pool), demand: postgres.NewDemandLedger(pool),
		profiles: postgres.NewProfileDirectory(pool), catalogue: postgres.NewSlotCatalogue(pool),
		outbox: ob, outboxStore: ob, processed: postgres.NewProcessedEventRepo(pool),
		uow:         postgres.NewUnitOfWork(pool),
		idempotency: inboundhttp.RequireIdempotencyKey(pool, pgtx.With),
	}
}

// migrationsDatabaseURL returns MIGRATIONS_DATABASE_URL when set (a direct
// DSN: golang-migrate's advisory lock does not survive PgBouncer transaction
// pooling), else databaseURL.
func migrationsDatabaseURL(databaseURL string) string {
	return getenv("MIGRATIONS_DATABASE_URL", databaseURL)
}

// worker is a started background loop and the channel closed once it
// returned.
type worker struct {
	name string
	done chan struct{}
}

// Consumer modes (DEMAND_MODE, PRODUCT_MODE, LAYOUT_MODE).
const (
	modePermissive = "permissive"
	modeKafka      = "kafka"
)

// parseConsumerMode validates a *_MODE value ("" and "permissive" = no
// consumer).
func parseConsumerMode(name, raw string) (string, error) {
	switch raw {
	case "", modePermissive:
		return modePermissive, nil
	case modeKafka:
		return modeKafka, nil
	default:
		return "", fmt.Errorf("unknown %s %q (want kafka or permissive)", name, raw)
	}
}

// consumerSpec is one local-copy consumer: its mode and group variables and
// how to build it once both are resolved.
type consumerSpec struct {
	modeEnv, groupEnv string
	build             func(brokers []string, group string, logger *slog.Logger) *inboundkafka.Consumer
}

func consumerSpecs(ad adapters, cfg config) []consumerSpec {
	intake := ad.intake()
	return []consumerSpec{
		{"DEMAND_MODE", "DEMAND_CONSUMER_GROUP", func(brokers []string, group string, logger *slog.Logger) *inboundkafka.Consumer {
			uc := &usecases.ApplyDemandChanged{Intake: intake, Demand: ad.demand, OnlySite: cfg.onlySite}
			return inboundkafka.NewDemandConsumer(brokers, group, uc, logger)
		}},
		{"PRODUCT_MODE", "PRODUCT_CONSUMER_GROUP", func(brokers []string, group string, logger *slog.Logger) *inboundkafka.Consumer {
			classified := &usecases.ApplyProductClassified{Intake: intake, Profiles: ad.profiles}
			physical := &usecases.ApplyPhysicalProfile{Intake: intake, Profiles: ad.profiles}
			return inboundkafka.NewProductConsumer(brokers, group, classified, physical, logger)
		}},
		{"LAYOUT_MODE", "LAYOUT_CONSUMER_GROUP", func(brokers []string, group string, logger *slog.Logger) *inboundkafka.Consumer {
			return inboundkafka.NewLayoutConsumer(brokers, group,
				&usecases.ApplyZoneRegistered{Intake: intake, Catalogue: ad.catalogue},
				&usecases.ApplyLocationSlotRegistered{Intake: intake, Catalogue: ad.catalogue},
				&usecases.ApplyLocationSlotDecommissioned{Intake: intake, Catalogue: ad.catalogue}, logger)
		}},
	}
}

// plannedConsumer is a consumer whose mode is kafka, with its resolved group.
type plannedConsumer struct {
	spec  consumerSpec
	group string
}

// planConsumers resolves every consumer's mode and group from the
// environment and refuses to boot on a misconfiguration: an unknown mode, or
// mode kafka without its group variable or without KAFKA_BROKERS.
func planConsumers(specs []consumerSpec) ([]plannedConsumer, []string, error) {
	var planned []plannedConsumer
	for _, spec := range specs {
		mode, err := parseConsumerMode(spec.modeEnv, os.Getenv(spec.modeEnv))
		if err != nil {
			return nil, nil, err
		}
		if mode != modeKafka {
			continue
		}
		group := os.Getenv(spec.groupEnv)
		if group == "" {
			return nil, nil, fmt.Errorf("%s=kafka requires %s", spec.modeEnv, spec.groupEnv)
		}
		planned = append(planned, plannedConsumer{spec: spec, group: group})
	}
	if len(planned) == 0 {
		return nil, nil, nil
	}
	raw := os.Getenv("KAFKA_BROKERS")
	if raw == "" {
		return nil, nil, errors.New("a consumer in kafka mode requires KAFKA_BROKERS")
	}
	return planned, strings.Split(raw, ","), nil
}

// startConsumers starts every consumer whose mode is kafka. The readers dial
// lazily inside Run, so a broker outage never blocks boot.
func startConsumers(ad adapters, logger *slog.Logger) ([]*worker, func(), error) {
	planned, brokers, err := planConsumers(consumerSpecs(ad, configFromEnv(logger)))
	if err != nil {
		return nil, nil, err
	}
	if len(planned) == 0 {
		logger.Info("no consumer is in kafka mode; the local copies are whatever the database holds")
		return nil, func() {}, nil
	}
	ctx, stop := context.WithCancel(context.Background())
	var (
		workers []*worker
		closers []func() error
	)
	for _, p := range planned {
		consumer := p.spec.build(brokers, p.group, logger)
		w := &worker{name: p.spec.modeEnv, done: make(chan struct{})}
		workers = append(workers, w)
		closers = append(closers, consumer.Close)
		go func() {
			defer close(w.done)
			logger.Info("consumer running", "consumer", consumer.Name, "topic", consumer.Topic, "group_id", p.group, "brokers", brokers)
			if err := consumer.Run(ctx); !errors.Is(err, context.Canceled) {
				logger.Error("consumer stopped", "consumer", consumer.Name, "error", err)
			}
		}()
	}
	return workers, func() {
		stop()
		for _, closeFn := range closers {
			_ = closeFn()
		}
	}, nil
}

// Event publisher modes (EVENT_PUBLISHER).
const (
	publisherLog   = "log"
	publisherKafka = "kafka"
)

// parsePublisherMode validates EVENT_PUBLISHER ("" and "log" = log sink).
func parsePublisherMode(raw string) (string, error) {
	switch raw {
	case "", publisherLog:
		return publisherLog, nil
	case publisherKafka:
		return publisherKafka, nil
	default:
		return "", fmt.Errorf("unknown EVENT_PUBLISHER %q (want kafka or log)", raw)
	}
}

// parseRelayInterval reads OUTBOX_RELAY_INTERVAL (Go duration or plain
// seconds), defaulting when unset, malformed or non-positive.
func parseRelayInterval(raw string, logger *slog.Logger) time.Duration {
	if raw == "" {
		return outboxrelay.DefaultInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		if secs, serr := strconv.ParseFloat(raw, 64); serr == nil {
			d, err = time.Duration(secs*float64(time.Second)), nil
		}
	}
	if err != nil || d <= 0 {
		logger.Warn("invalid OUTBOX_RELAY_INTERVAL; using the default", "value", raw, "default", outboxrelay.DefaultInterval)
		return outboxrelay.DefaultInterval
	}
	return d
}

// startOutboxRelay starts the relay goroutine with the EVENT_PUBLISHER sink.
// Kafka is dialled lazily on the first send, never here.
func startOutboxRelay(store outboxrelay.Store, logger *slog.Logger) (*worker, func(), error) {
	mode, err := parsePublisherMode(os.Getenv("EVENT_PUBLISHER"))
	if err != nil {
		return nil, nil, err
	}
	var (
		sink      outboxrelay.Sink = outboxrelay.LogSink{Logger: logger}
		closeSink                  = func() {}
	)
	if mode == publisherKafka {
		raw := os.Getenv("KAFKA_BROKERS")
		if raw == "" {
			return nil, nil, errors.New("EVENT_PUBLISHER=kafka requires KAFKA_BROKERS")
		}
		kafkaSink := outboundkafka.NewRelaySink(strings.Split(raw, ","))
		sink = kafkaSink
		closeSink = func() {
			if err := kafkaSink.Close(); err != nil {
				logger.Error("kafka relay sink close failed", "error", err)
			}
		}
	}
	interval := parseRelayInterval(os.Getenv("OUTBOX_RELAY_INTERVAL"), logger)
	relay := outboxrelay.NewRelay(store, sink, logger, outboxrelay.WithInterval(interval))

	ctx, stop := context.WithCancel(context.Background())
	w := &worker{name: "outbox-relay", done: make(chan struct{})}
	go func() {
		defer close(w.done)
		defer closeSink()
		logger.Info("outbox relay running", "publisher", mode, "interval", interval, "topic", outboundkafka.Topic)
		if err := relay.Run(ctx); !errors.Is(err, context.Canceled) {
			logger.Error("outbox relay stopped", "error", err)
		}
	}()
	return w, stop, nil
}

// serveUntilSignal runs httpServer until SIGINT/SIGTERM (or a listen error),
// then shuts down in the fleet order: (1) /readyz flips to 503, (2) wait the
// drain delay, (3) drain HTTP, (4) stop and await the consumers, (5) stop and
// await the relay LAST so nothing committed above is stranded; the caller's
// deferred pool.Close runs after all of it.
func serveUntilSignal(logger *slog.Logger, httpServer *http.Server, readiness *inboundhttp.Readiness,
	consumers []*worker, closeConsumers func(), relay *worker, closeRelay func(),
) error {
	errCh := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-errCh:
		closeConsumers()
		closeRelay()
		return err
	case <-ctx.Done():
	}

	readiness.SetNotReady()
	if delay := shutdownDrainDelay(logger); delay > 0 {
		logger.Info("shutdown: readiness flipped to not-ready; waiting for traffic to drain", "drain_delay", delay.String())
		time.Sleep(delay)
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

// shutdownDrainDelay reads SHUTDOWN_DRAIN_DELAY ("0" disables; unset,
// negative or unparsable = the default).
func shutdownDrainDelay(logger *slog.Logger) time.Duration {
	raw := os.Getenv("SHUTDOWN_DRAIN_DELAY")
	if raw == "" {
		return DefaultShutdownDrainDelay
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		logger.Warn("ignoring invalid SHUTDOWN_DRAIN_DELAY", "value", raw, "default", DefaultShutdownDrainDelay.String())
		return DefaultShutdownDrainDelay
	}
	return d
}

// setupTelemetry installs the OTLP trace and metric providers (never blocks
// on a missing Collector) and returns a bounded flush-on-exit closer.
func setupTelemetry(logger *slog.Logger) (func(), error) {
	otelCtx, otelCancel := context.WithTimeout(context.Background(), 10*time.Second)
	otelShutdown, err := telemetry.Setup(otelCtx, inboundhttp.DefaultServiceName, getenv("SERVICE_VERSION", "dev"),
		getenv("OTEL_EXPORTER_OTLP_ENDPOINT", telemetry.DefaultOTLPEndpoint))
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

// newLogger builds the JSON logger; LOG_LEVEL debug|info|warn|error.
func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
