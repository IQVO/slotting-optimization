package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	inboundhttp "github.com/claudioed/slotting-optimization/internal/adapters/inbound/http"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/clock"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/ids"
	outboundkafka "github.com/claudioed/slotting-optimization/internal/adapters/outbound/kafka"
	outboxrelay "github.com/claudioed/slotting-optimization/internal/adapters/outbound/outbox"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/postgres"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/postgres/pgtx"
	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/bootretry"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
)

// newReadiness builds the /readyz gate: the drain gate plus a Postgres ping
// and a Kafka broker answering a topic-less metadata request (never topics:
// the broker auto-creates them on first write), cached 3s, bounded 2s. The
// ping is a parameter so the gate is testable without a pool.
func newReadiness(ping func(context.Context) error, brokers []string) *inboundhttp.Readiness {
	return inboundhttp.NewReadiness(inboundhttp.DefaultReadinessTTL, inboundhttp.DefaultReadinessTimeout,
		inboundhttp.Dependency{Name: "postgres", Check: ping},
		inboundhttp.Dependency{Name: "kafka", Check: outboundkafka.NewBrokerCheck(brokers).Check},
	)
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
	executions  ports.MoveExecutionLedger
	packLevels  ports.PackLevelView
	uow         ports.UnitOfWork
	// idempotency is the Idempotency-Key middleware of POST /slot-plans.
	idempotency func(http.Handler) http.Handler
	// pool is the Postgres pool (the readiness check pings it).
	pool *pgxpool.Pool
}

// writer bundles the ports every write use case needs. The encoder is the
// FanoutEncoder (ADR 0006): every domain event becomes TWO outbox rows — the
// integration row on warehouse.slotting-optimization.events first, the
// analytics row on warehouse.slotting-optimization.analytics second — under
// ONE CloudEvents id minted per occurrence, so both streams commit together
// and a relay retry republishes identical bytes.
func (a adapters) writer() usecases.Writer {
	return usecases.Writer{Plans: a.plans, Outbox: a.outbox, Encoder: outboundkafka.NewFanoutEncoder(), UoW: a.uow, Clock: clock.System{}}
}

// intake bundles the ports every consumer use case needs.
func (a adapters) intake() usecases.Intake {
	return usecases.Intake{UoW: a.uow, Processed: a.processed}
}

func buildServer(ad adapters, cfg config, readiness *inboundhttp.Readiness, metrics http.Handler, planMetrics ports.PlanDecisionMetrics) (*inboundhttp.Server, error) {
	planner, err := planning.NewPlanner(planning.LexicalRanking{})
	if err != nil {
		return nil, fmt.Errorf("planner: %w", err)
	}
	w := ad.writer()
	return &inboundhttp.Server{
		GeneratePlan: &usecases.GeneratePlan{
			Writer: w, Demand: ad.demand, Profiles: ad.profiles, Catalogue: ad.catalogue, IDs: ids.UUID{}, Planner: planner,
			DefaultSite: cfg.defaultSite, ForwardZoneCodes: cfg.forwardZoneCodes, DefaultLookback: cfg.lookbackDays,
			CasePickZoneCodes: cfg.casePickZoneCodes, PackLevels: ad.packLevels,
		},
		ApprovePlan:      &usecases.ApprovePlan{Writer: w, Metrics: planMetrics},
		RejectPlan:       &usecases.RejectPlan{Writer: w, Metrics: planMetrics},
		GetPlan:          &usecases.GetPlan{Plans: ad.plans, Executions: ad.executions},
		ListPlans:        &usecases.ListPlans{Plans: ad.plans},
		ListForwardSlots: &usecases.ListForwardSlots{Plans: ad.plans, DefaultSite: cfg.defaultSite},
		ListSkuVelocity:  &usecases.ListSkuVelocity{Demand: ad.demand, Clock: clock.System{}, DefaultSite: cfg.defaultSite},
		Idempotency:      ad.idempotency,
		Readiness:        readiness,
		Metrics:          metrics,
	}, nil
}

// buildAdapters wires the Postgres adapters, running the embedded migrations
// first. An empty databaseURL is a boot error: there is no in-memory runtime.
func buildAdapters(ctx context.Context, databaseURL string, logger *slog.Logger) (adapters, func(), error) {
	noop := func() {}
	if databaseURL == "" {
		return adapters{}, noop, errMissingDatabaseURL
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
		executions:  postgres.NewMoveExecutionLedger(pool),
		packLevels:  postgres.NewPackLevelView(pool),
		uow:         postgres.NewUnitOfWork(pool),
		idempotency: inboundhttp.RequireIdempotencyKey(pool, pgtx.With),
		pool:        pool,
	}
}

// migrationsDatabaseURL returns MIGRATIONS_DATABASE_URL when set (a direct
// DSN: golang-migrate's advisory lock does not survive PgBouncer transaction
// pooling), else databaseURL.
func migrationsDatabaseURL(databaseURL string) string {
	return getenv("MIGRATIONS_DATABASE_URL", databaseURL)
}
