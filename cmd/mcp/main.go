// Command mcp is the composition root for the slotting-optimization MCP
// server (docs/adr/0005-mcp-server-adoption.md): it wires env config to the
// outbound repositories, the repositories to the SAME read use cases the REST
// adapter uses (GetPlan, ListPlans, ListForwardSlots, ListSkuVelocity), and
// those to the inbound MCP adapter, then serves MCP over Streamable HTTP (and
// nothing else: no stdio, no SSE). It is a second, independent deployable
// alongside cmd/api.
//
// The surface is READ-ONLY: no write use case is wired here at all, so this
// binary can never insert into the outbox. It never starts the outbox relay,
// never runs the local-copy consumers and never dials Kafka (no
// EVENT_PUBLISHER, no KAFKA_BROKERS, no consumer groups): those run in
// cmd/api only. It reads the SAME Postgres as cmd/api and has no in-memory
// mode (ADR 0011): DATABASE_URL is required.
//
// There is NO auth of any kind (fleet-wide revert 2026-09-11): no keys, no
// bearer checks. Access control is the in-cluster ClusterIP boundary.
//
// Environment:
//
//	MCP_ADDR                     listen address (default :8090)
//	DATABASE_URL                 Postgres DSN (REQUIRED: there is no in-memory mode, ADR 0011)
//	MIGRATIONS_DATABASE_URL      direct DSN for the boot migrations (default DATABASE_URL)
//	DEMAND_SITE_ID               the site get_forward_slots and get_sku_velocity default to
//	                             (falls back to DEFAULT_SITE_ID), matching cmd/api
//	DEFAULT_SITE_ID              fallback of DEMAND_SITE_ID
//	OTEL_EXPORTER_OTLP_ENDPOINT  OTLP/gRPC collector (default localhost:4317)
//	LOG_LEVEL, SERVICE_VERSION, ENVIRONMENT
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	inboundmcp "github.com/claudioed/slotting-optimization/internal/adapters/inbound/mcp"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/clock"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/postgres"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/telemetry"
	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/bootretry"
)

// shutdownTimeout bounds the HTTP server's graceful drain.
const shutdownTimeout = 10 * time.Second

func main() {
	if err := run(); err != nil {
		slog.Error("mcp server exited with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	logger := newLogger(getenv("LOG_LEVEL", "info"))
	slog.SetDefault(logger)

	// Misconfiguration is refused before anything is opened.
	databaseURL, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}

	otelCtx, otelCancel := context.WithTimeout(context.Background(), 10*time.Second)
	otelShutdown, err := telemetry.Setup(otelCtx, mcpServiceName, getenv("SERVICE_VERSION", "dev"),
		getenv("OTEL_EXPORTER_OTLP_ENDPOINT", telemetry.DefaultOTLPEndpoint))
	otelCancel()
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := otelShutdown(shutdownCtx); err != nil {
			logger.Error("telemetry shutdown failed", "error", err)
		}
	}()

	deps, closeAdapters, err := buildDeps(context.Background(), logger, databaseURL,
		getenv("MIGRATIONS_DATABASE_URL", databaseURL), defaultSite())
	if err != nil {
		return err
	}
	defer closeAdapters()

	srv := &http.Server{
		Addr:              getenv("MCP_ADDR", ":8090"),
		Handler:           newRouter(inboundmcp.Handler(inboundmcp.NewServer(deps))),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return serveMCP(logger, srv)
}

// defaultSite is the site the site-scoped tools fall back to, resolved the
// way cmd/api resolves it (DEMAND_SITE_ID, else DEFAULT_SITE_ID).
func defaultSite() string {
	return getenv("DEMAND_SITE_ID", os.Getenv("DEFAULT_SITE_ID"))
}

// errMissingDatabaseURL is the boot error of an unset DATABASE_URL: the MCP
// binary reads the same Postgres as cmd/api and has no in-memory mode.
var errMissingDatabaseURL = errors.New("DATABASE_URL is required: this context runs on real Postgres, there is no in-memory mode")

// loadConfig validates the environment before anything is opened and returns
// DATABASE_URL.
func loadConfig(env func(string) string) (string, error) {
	databaseURL := env("DATABASE_URL")
	if databaseURL == "" {
		return "", errMissingDatabaseURL
	}
	return databaseURL, nil
}

// buildDeps migrates then connects a pgx pool and wires the read use cases
// over the Postgres repositories. An empty databaseURL is a boot error. The
// returned close func releases the pool.
//
// Like every sibling's cmd/mcp this binary runs the idempotent embedded
// migrations on start, so it can boot against a fresh database before cmd/api
// has; golang-migrate's advisory lock makes concurrent starts safe.
// migrationsDatabaseURL is used ONLY for that step (a DIRECT Postgres DSN where
// DATABASE_URL points at PgBouncer, which cannot honour the session-scoped
// advisory lock); the runtime pool always uses databaseURL.
func buildDeps(ctx context.Context, logger *slog.Logger, databaseURL, migrationsDatabaseURL, site string) (inboundmcp.Deps, func(), error) {
	noop := func() {}
	if databaseURL == "" {
		return inboundmcp.Deps{}, noop, errMissingDatabaseURL
	}
	// The first outbound dial of an injected pod can be reset (Istio native
	// sidecars), so migrations and the first ping retry with backoff; on
	// exhaustion the process refuses to boot.
	if err := bootretry.Retry(ctx, logger, "run migrations", func() error {
		return postgres.RunMigrations(migrationsDatabaseURL)
	}); err != nil {
		return inboundmcp.Deps{}, noop, err
	}
	pool, err := postgres.NewPool(ctx, databaseURL)
	if err != nil {
		return inboundmcp.Deps{}, noop, err
	}
	if err := bootretry.Retry(ctx, logger, "ping postgres", func() error { return pool.Ping(ctx) }); err != nil {
		pool.Close()
		return inboundmcp.Deps{}, noop, err
	}
	logger.Info("postgres repositories configured")
	return depsOver(postgres.NewSlotPlanRepo(pool), postgres.NewDemandLedger(pool), postgres.NewMoveExecutionLedger(pool), site), pool.Close, nil
}

// depsOver wires the four read-only use cases over the given read ports.
func depsOver(plans ports.SlotPlanRepository, demand ports.DemandLedger, executions ports.MoveExecutionLedger, site string) inboundmcp.Deps {
	return inboundmcp.Deps{
		GetPlan:          &usecases.GetPlan{Plans: plans, Executions: executions},
		ListPlans:        &usecases.ListPlans{Plans: plans},
		ListForwardSlots: &usecases.ListForwardSlots{Plans: plans, DefaultSite: site},
		ListSkuVelocity:  &usecases.ListSkuVelocity{Demand: demand, Clock: clock.System{}, DefaultSite: site},
	}
}

// serveMCP runs srv until it fails or SIGINT/SIGTERM arrives, then drains it
// gracefully.
func serveMCP(logger *slog.Logger, srv *http.Server) error {
	serverErr := make(chan error, 1)
	go func() {
		logger.Info("mcp server listening (Streamable HTTP)", "addr", srv.Addr)
		serverErr <- srv.ListenAndServe()
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// newLogger builds the process-wide JSON logger. LOG_LEVEL maps
// debug|info|warn|warning|error (case-insensitive; "warning" is accepted
// like "warn" fleet-wide), defaulting to Info.
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

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
