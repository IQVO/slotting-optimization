//go:build integration

// Integration tests for the pgtx transaction-context package over a REAL
// testcontainers Postgres (internal/testing/pgtest: one container per test
// binary, one private migrated database per test — never an external
// DATABASE_URL, never t.Skip). These prove the all-or-nothing bracket the
// whole service relies on: a repository write (a SlotPlan save) plus its
// outbox rows (the publish) inside postgres.UnitOfWork commit TOGETHER, and
// an error anywhere inside the scope rolls EVERYTHING back — the plan, the
// outbox rows and any processed-event claim — leaving the database exactly
// as it was. pgtx.With/From is the transport that carries the open
// transaction to every repo invisibly; these tests exercise it through the
// real repos, the way cmd/api's use cases do.
package pgtx_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/postgres"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/postgres/pgtx"
	"github.com/claudioed/slotting-optimization/internal/application/outbox"
	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/testing/pgtest"
	"github.com/claudioed/slotting-optimization/internal/testing/repocontract"
)

// The tcpostgres reference keeps the testcontainers postgres module import
// reachable in this package's directory (required by internal/architecture's
// TestPostgresIntegrationTestsUseTestcontainers for any integration test
// package that talks to pgx).
var _ = tcpostgres.Run

func TestMain(m *testing.M) {
	code := m.Run()
	pgtest.Shutdown()
	os.Exit(code)
}

// stack is the real Postgres adapter set over one private database.
type stack struct {
	pool      *pgxpool.Pool
	plans     *postgres.SlotPlanRepo
	outbox    *postgres.OutboxRepo
	processed *postgres.ProcessedEventRepo
	uow       *postgres.UnitOfWork
}

// newStack wires the real repos and the real UnitOfWork.
func newStack(t *testing.T) *stack {
	t.Helper()
	pool := pgtest.NewPool(t)
	return &stack{
		pool:      pool,
		plans:     postgres.NewSlotPlanRepo(pool),
		outbox:    postgres.NewOutboxRepo(pool),
		processed: postgres.NewProcessedEventRepo(pool),
		uow:       postgres.NewUnitOfWork(pool),
	}
}

// counts reads the row counts the atomicity assertions need: plans,
// outbox rows and processed-event claims.
func (s *stack) counts(t *testing.T) (plans, rows, claims int) {
	t.Helper()
	err := s.pool.QueryRow(context.Background(), `
		SELECT (SELECT count(*) FROM slot_plans),
		       (SELECT count(*) FROM outbox_events),
		       (SELECT count(*) FROM processed_events)`).Scan(&plans, &rows, &claims)
	if err != nil {
		t.Fatal(err)
	}
	return plans, rows, claims
}

// wireMessage is one outbox row in its real wire shape (the CloudEvents
// bytes would come from the encoder; for the atomicity bracket the exact
// bytes are indifferent, the row's PRESENCE is what must be atomic).
func wireMessage(planID string) outbox.Message {
	return outbox.Message{
		EventID: "evt-" + planID, Topic: "warehouse.slotting-optimization.events",
		EventType: "com.warehouse.wms.slotting-optimization.slotplan.SlotPlanGenerated",
		Subject:   planID, Key: []byte(planID),
		DataSchema: "urn:warehouse:slotting-optimization:events:SlotPlanGenerated:v1",
		Value:      []byte(`{"specversion":"1.0"}`),
		Headers:    []outbox.Header{{Key: "content-type", Value: "application/cloudevents+json; charset=UTF-8"}},
	}
}

// TestPgtx_SaveAndPublishCommitAtomically proves the commit side: a plan
// saved inside the UnitOfWork and its outbox rows (the publish) are visible
// TOGETHER after Do returns nil — there is no window where the plan exists
// without its events, and none where the events exist without the plan.
func TestPgtx_SaveAndPublishCommitAtomically(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()

	err := s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.plans.Save(ctx, repocontract.Draft(t, "plan-tx-1", "SITE-1", repocontract.T0), 0); err != nil {
			return err
		}
		return s.outbox.Insert(ctx, wireMessage("plan-tx-1"))
	})
	if err != nil {
		t.Fatalf("unit of work: %v", err)
	}

	plans, rows, _ := s.counts(t)
	if plans != 1 || rows != 1 {
		t.Fatalf("after commit: %d plans, %d outbox rows; the save and the publish must land together", plans, rows)
	}
	// The committed plan reads back through the repo (outside any tx): the
	// aggregate really is durable, not just tentatively written.
	if _, err := s.plans.Get(ctx, "plan-tx-1"); err != nil {
		t.Fatalf("get the committed plan: %v", err)
	}
}

// TestPgtx_AnErrorInsideTheScopeRollsEverythingBack proves the rollback
// side: the plan save, the outbox rows AND the processed-event claim all
// ran, then the scope failed — nothing survives. This is the bracket that
// makes the transactional outbox trustworthy: a failed use case can never
// leave a half-published aggregate behind.
func TestPgtx_AnErrorInsideTheScopeRollsEverythingBack(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	boom := errors.New("boom inside the scope")

	err := s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.plans.Save(ctx, repocontract.Draft(t, "plan-tx-2", "SITE-1", repocontract.T0), 0); err != nil {
			return err
		}
		if err := s.outbox.Insert(ctx, wireMessage("plan-tx-2")); err != nil {
			return err
		}
		if _, err := s.processed.Claim(ctx, "slotting-demand", "evt-plan-tx-2"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the scope's own error", err)
	}

	if plans, rows, claims := s.counts(t); plans != 0 || rows != 0 || claims != 0 {
		t.Fatalf("after rollback: %d plans, %d outbox rows, %d claims survived", plans, rows, claims)
	}
	if _, err := s.plans.Get(ctx, "plan-tx-2"); !errors.Is(err, repository.ErrPlanNotFound) {
		t.Fatalf("the rolled-back plan must not exist: %v", err)
	}
	// The un-claimed id is claimable again (a redelivery is processed, not
	// skipped): the rollback really unwound the claim, not just hid it.
	claimed, err := s.processed.Claim(ctx, "slotting-demand", "evt-plan-tx-2")
	if err != nil || !claimed {
		t.Fatalf("claim after rollback = %v, %v; the id must be free again", claimed, err)
	}
}

// TestPgtx_ReposOutsideTheScopeSeeNothingUntilCommit proves the isolation
// side: writes inside the open transaction are invisible to a reader on the
// pool until Do commits — the transaction really is one pgx tx carried by
// pgtx, not a sequence of autocommit writes.
func TestPgtx_ReposOutsideTheScopeSeeNothingUntilCommit(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()

	committed := make(chan struct{})
	err := s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.plans.Save(ctx, repocontract.Draft(t, "plan-tx-3", "SITE-1", repocontract.T0), 0); err != nil {
			return err
		}
		// Inside the scope the plan is visible (same tx)...
		if _, err := s.plans.Get(ctx, "plan-tx-3"); err != nil {
			return err
		}
		// ...but a fresh reader on the pool — with a context that carries NO
		// transaction — must NOT see it yet.
		outside := postgres.NewSlotPlanRepo(s.pool)
		if _, err := outside.Get(context.Background(), "plan-tx-3"); !errors.Is(err, repository.ErrPlanNotFound) {
			return errors.New("an uncommitted plan leaked outside the unit of work")
		}
		close(committed)
		return nil
	})
	if err != nil {
		t.Fatalf("unit of work: %v", err)
	}
	select {
	case <-committed:
	default:
		t.Fatal("the scope never ran to completion")
	}
	if _, err := s.plans.Get(ctx, "plan-tx-3"); err != nil {
		t.Fatalf("get after commit: %v", err)
	}
}

// TestPgtx_NestedUnitOfWorkJoinsTheOuterTransaction proves pgtx.From's
// join semantics: a UnitOfWork.Do inside another Do does NOT open a second
// transaction — everything lands on the outer tx, so an inner failure still
// rolls back the outer writes (the property the consumer use cases' Intake
// relies on when it nests inside the handler's bracket).
func TestPgtx_NestedUnitOfWorkJoinsTheOuterTransaction(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	boom := errors.New("inner failure")

	err := s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.plans.Save(ctx, repocontract.Draft(t, "plan-tx-4", "SITE-1", repocontract.T0), 0); err != nil {
			return err
		}
		// A nested unit of work over the SAME pool must join, not begin.
		return s.uow.Do(ctx, func(ctx context.Context) error {
			if err := s.outbox.Insert(ctx, wireMessage("plan-tx-4")); err != nil {
				return err
			}
			return boom
		})
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the inner error", err)
	}
	if plans, rows, _ := s.counts(t); plans != 0 || rows != 0 {
		t.Fatalf("after the inner rollback: %d plans, %d outbox rows survived; the nested scope must not have committed on its own", plans, rows)
	}

	// And the commit path: a successful nested scope commits with the outer.
	err = s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.plans.Save(ctx, repocontract.Draft(t, "plan-tx-4", "SITE-1", repocontract.T0), 0); err != nil {
			return err
		}
		return s.uow.Do(ctx, func(ctx context.Context) error {
			return s.outbox.Insert(ctx, wireMessage("plan-tx-4"))
		})
	})
	if err != nil {
		t.Fatalf("nested commit: %v", err)
	}
	if plans, rows, _ := s.counts(t); plans != 1 || rows != 1 {
		t.Fatalf("after the nested commit: %d plans, %d outbox rows", plans, rows)
	}
}

// TestPgtx_ExplicitWithFromCarriesTheTransactionToAnyQuerier proves the
// primitive itself: pgtx.With stores the tx in the ctx and pgtx.From hands
// it back — and a write made through a bare pgx tx stored this way is what
// the repos' queryFor picks up.
func TestPgtx_ExplicitWithFromCarriesTheTransactionToAnyQuerier(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txCtx := pgtx.With(ctx, tx)
	if got, ok := pgtx.From(txCtx); !ok || got != tx {
		t.Fatalf("From(With(tx)) = %v, %v; the tx must come back unchanged", got, ok)
	}
	if _, ok := pgtx.From(ctx); ok {
		t.Fatal("From(plain ctx) reported a transaction")
	}

	// A repo called with the tx-carrying ctx joins it: the write is visible
	// inside the tx and gone after the rollback.
	if err := s.plans.Save(txCtx, repocontract.Draft(t, "plan-tx-5", "SITE-1", repocontract.T0), 0); err != nil {
		t.Fatalf("save on the carried tx: %v", err)
	}
	if _, err := s.plans.Get(txCtx, "plan-tx-5"); err != nil {
		t.Fatalf("get inside the carried tx: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if _, err := s.plans.Get(ctx, "plan-tx-5"); !errors.Is(err, repository.ErrPlanNotFound) {
		t.Fatalf("after the rollback the plan must be gone: %v", err)
	}
}
