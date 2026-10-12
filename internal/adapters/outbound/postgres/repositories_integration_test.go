//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/postgres"
	"github.com/claudioed/slotting-optimization/internal/application/outbox"
	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
	"github.com/claudioed/slotting-optimization/internal/testing/pgtest"
	"github.com/claudioed/slotting-optimization/internal/testing/repocontract"
)

// The same behavioural contract the in-memory adapters pass, run against a
// real Postgres: every contract case gets its own database.

func TestSlotPlanRepoContract(t *testing.T) {
	repocontract.SlotPlanRepository(t, func(t *testing.T) ports.SlotPlanRepository {
		return postgres.NewSlotPlanRepo(pgtest.NewPool(t))
	})
}

func TestDemandLedgerContract(t *testing.T) {
	repocontract.DemandLedger(t, func(t *testing.T) ports.DemandLedger {
		return postgres.NewDemandLedger(pgtest.NewPool(t))
	})
}

func TestProfileDirectoryContract(t *testing.T) {
	repocontract.ProfileDirectory(t, func(t *testing.T) ports.ProfileDirectory {
		return postgres.NewProfileDirectory(pgtest.NewPool(t))
	})
}

func TestSlotCatalogueContract(t *testing.T) {
	repocontract.SlotCatalogue(t, func(t *testing.T) ports.SlotCatalogue {
		return postgres.NewSlotCatalogue(pgtest.NewPool(t))
	})
}

// TestOneApprovedPlanPerSiteIsEnforcedByTheDatabase proves the partial unique
// index, not just the use case: raw approvals racing for one site leave
// exactly one Approved plan and every loser sees ErrApprovedPlanConflict.
func TestOneApprovedPlanPerSiteIsEnforcedByTheDatabase(t *testing.T) {
	ctx := context.Background()
	repo := postgres.NewSlotPlanRepo(pgtest.NewPool(t))
	const n = 6
	for i := range n {
		id := "plan-" + string(rune('a'+i))
		if err := repo.Save(ctx, repocontract.Draft(t, id, "SITE-1", repocontract.T0.Add(time.Duration(i)*time.Minute)), 0); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := repo.Get(ctx, slotplan.PlanID("plan-"+string(rune('a'+i))))
			if err != nil {
				results[i] = err
				return
			}
			if _, err := p.Approve("", repocontract.T0.Add(time.Hour)); err != nil {
				results[i] = err
				return
			}
			results[i] = repo.Save(ctx, p, 1)
		}()
	}
	wg.Wait()
	won := 0
	for _, err := range results {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, repository.ErrApprovedPlanConflict):
			t.Fatalf("a loser must get ErrApprovedPlanConflict, got %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("%d approvals won, want exactly 1", won)
	}
	approved, err := repo.List(ctx, repository.PlanFilter{State: slotplan.StateApproved}, "", 100)
	if err != nil || len(approved) != 1 {
		t.Fatalf("approved plans = %d, %v", len(approved), err)
	}
}

// TestUnitOfWorkCommitsOrRollsBackThePlanTheOutboxAndTheClaimTogether proves
// the transactional outbox on real Postgres.
func TestUnitOfWorkCommitsOrRollsBackThePlanTheOutboxAndTheClaimTogether(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.NewPool(t)
	plans, ob, processed, uow := postgres.NewSlotPlanRepo(pool), postgres.NewOutboxRepo(pool), postgres.NewProcessedEventRepo(pool), postgres.NewUnitOfWork(pool)
	boom := errors.New("boom")

	err := uow.Do(ctx, func(ctx context.Context) error {
		if err := plans.Save(ctx, repocontract.Draft(t, "plan-a", "SITE-1", repocontract.T0), 0); err != nil {
			return err
		}
		if err := ob.Insert(ctx, outbox.Message{EventID: "e1", Topic: "t", EventType: "x", Subject: "plan-a", DataSchema: "d", Value: []byte("{}")}); err != nil {
			return err
		}
		if _, err := processed.Claim(ctx, "c", "e1"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if _, err := plans.Get(ctx, "plan-a"); !errors.Is(err, repository.ErrPlanNotFound) {
		t.Fatalf("plan survived a rollback: %v", err)
	}
	var rows, claims int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM outbox_events), (SELECT count(*) FROM processed_events)`).Scan(&rows, &claims); err != nil {
		t.Fatal(err)
	}
	if rows != 0 || claims != 0 {
		t.Fatalf("outbox rows %d, claims %d survived a rollback", rows, claims)
	}

	if err := uow.Do(ctx, func(ctx context.Context) error {
		return plans.Save(ctx, repocontract.Draft(t, "plan-a", "SITE-1", repocontract.T0), 0)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := plans.Get(ctx, "plan-a"); err != nil {
		t.Fatalf("a committed unit keeps the plan: %v", err)
	}
}

func TestProcessedEventsClaimOnce(t *testing.T) {
	ctx := context.Background()
	p := postgres.NewProcessedEventRepo(pgtest.NewPool(t))
	first, err := p.Claim(ctx, "demand", "e1")
	again, _ := p.Claim(ctx, "demand", "e1")
	other, _ := p.Claim(ctx, "product", "e1")
	if err != nil || !first || again || !other {
		t.Fatalf("first=%v again=%v otherConsumer=%v err=%v", first, again, other, err)
	}
}

func TestOutboxDrainOrderAndFailureHandling(t *testing.T) {
	ctx := context.Background()
	ob := postgres.NewOutboxRepo(pgtest.NewPool(t))
	msg := func(id string) outbox.Message {
		return outbox.Message{EventID: id, Topic: "t", EventType: "type-" + id, Subject: "s", Key: []byte("k"), DataSchema: "d", Value: []byte("{}"),
			Headers: []outbox.Header{{Key: "content-type", Value: "application/cloudevents+json; charset=UTF-8"}}}
	}
	if err := ob.Insert(ctx, msg("a"), msg("b"), msg("c")); err != nil {
		t.Fatal(err)
	}
	var sent []string
	n, err := ob.Drain(ctx, 2, func(_ context.Context, m outbox.Message) error {
		sent = append(sent, m.EventType)
		if len(m.Headers) != 1 || m.Headers[0].Key != "content-type" || string(m.Key) != "k" {
			t.Errorf("message lost data: %+v", m)
		}
		return nil
	})
	if err != nil || n != 2 || len(sent) != 2 || sent[0] != "type-a" || sent[1] != "type-b" {
		t.Fatalf("drain: n=%d err=%v sent=%v", n, err, sent)
	}
	boom := errors.New("broker down")
	n, err = ob.Drain(ctx, 10, func(context.Context, outbox.Message) error { return boom })
	if !errors.Is(err, boom) || n != 0 {
		t.Fatalf("failure: n=%d err=%v", n, err)
	}
	n, err = ob.Drain(ctx, 10, func(context.Context, outbox.Message) error { return nil })
	if err != nil || n != 1 {
		t.Fatalf("the failed row is retried: n=%d err=%v", n, err)
	}
	// the same (event_id, topic) is stored once
	if err := ob.Insert(ctx, msg("a")); err == nil {
		t.Fatal("a second row with the same event id and topic must violate the unique constraint")
	}
}
