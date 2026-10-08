package memory_test

import (
	"context"
	"errors"
	"testing"

	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/memory"
	"github.com/claudioed/slotting-optimization/internal/application/outbox"
	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
	"github.com/claudioed/slotting-optimization/internal/testing/repocontract"
)

func TestSlotPlanRepositoryContract(t *testing.T) {
	repocontract.SlotPlanRepository(t, func(*testing.T) ports.SlotPlanRepository { return memory.NewSlotPlanRepo() })
}

func TestDemandLedgerContract(t *testing.T) {
	repocontract.DemandLedger(t, func(*testing.T) ports.DemandLedger { return memory.NewDemandLedger() })
}

func TestProfileDirectoryContract(t *testing.T) {
	repocontract.ProfileDirectory(t, func(*testing.T) ports.ProfileDirectory { return memory.NewProfileDirectory() })
}

func TestSlotCatalogueContract(t *testing.T) {
	repocontract.SlotCatalogue(t, func(*testing.T) ports.SlotCatalogue { return memory.NewSlotCatalogue() })
}

var errBoom = errors.New("boom")

// TestUnitOfWorkRollsEverythingBack proves the all-or-nothing behaviour the
// Postgres transaction gives: a failing unit restores every participant.
func TestUnitOfWorkRollsEverythingBack(t *testing.T) {
	plans, ob, processed := memory.NewSlotPlanRepo(), memory.NewOutboxRepo(), memory.NewProcessedEventRepo()
	demand, profiles, cat := memory.NewDemandLedger(), memory.NewProfileDirectory(), memory.NewSlotCatalogue()
	uow := memory.NewUnitOfWork(plans, ob, processed, demand, profiles, cat)
	ctx := context.Background()

	err := uow.Do(ctx, func(ctx context.Context) error {
		if err := plans.Save(ctx, repocontract.Draft(t, "plan-a", "SITE-1", repocontract.T0), 0); err != nil {
			return err
		}
		if err := ob.Insert(ctx, outbox.Message{EventType: "x"}); err != nil {
			return err
		}
		if _, err := processed.Claim(ctx, "c", "e1"); err != nil {
			return err
		}
		if err := demand.Apply(ctx, repository.DemandLine{SourceOrderID: "o", LineNo: 1, Site: "S", SKU: "A", Units: 1, DueAt: repocontract.T0, Active: true}); err != nil {
			return err
		}
		if err := profiles.Save(ctx, repository.ProductProfile{SKU: "A", Version: 1}); err != nil {
			return err
		}
		if err := cat.SaveZone(ctx, repository.Zone{ID: "Z"}); err != nil {
			return err
		}
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v", err)
	}
	if _, err := plans.Get(ctx, "plan-a"); !errors.Is(err, repository.ErrPlanNotFound) {
		t.Fatalf("plan survived the rollback: %v", err)
	}
	if ob.Unpublished() != 0 || processed.Has("c", "e1") {
		t.Fatal("outbox row or claim survived the rollback")
	}
	if _, err := profiles.Get(ctx, "A"); !errors.Is(err, repository.ErrProfileNotFound) {
		t.Fatalf("profile survived the rollback: %v", err)
	}

	if err := uow.Do(ctx, func(ctx context.Context) error {
		return plans.Save(ctx, repocontract.Draft(t, "plan-b", "SITE-1", repocontract.T0), 0)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := plans.Get(ctx, slotplan.PlanID("plan-b")); err != nil {
		t.Fatalf("a committed unit keeps its writes: %v", err)
	}
}

func TestUnitOfWorkIsReentrant(t *testing.T) {
	plans := memory.NewSlotPlanRepo()
	uow := memory.NewUnitOfWork(plans)
	ctx := context.Background()
	err := uow.Do(ctx, func(ctx context.Context) error {
		if err := plans.Save(ctx, repocontract.Draft(t, "plan-a", "SITE-1", repocontract.T0), 0); err != nil {
			return err
		}
		// the inner unit joins the outer one; its success must not commit early
		_ = uow.Do(ctx, func(context.Context) error { return nil })
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v", err)
	}
	if _, err := plans.Get(ctx, "plan-a"); !errors.Is(err, repository.ErrPlanNotFound) {
		t.Fatal("the outermost unit owns commit/rollback")
	}
}

func TestOutboxDrainPublishesInOrderAndStopsOnError(t *testing.T) {
	ob := memory.NewOutboxRepo()
	ctx := context.Background()
	if err := ob.Insert(ctx, outbox.Message{EventType: "a"}, outbox.Message{EventType: "b"}, outbox.Message{EventType: "c"}); err != nil {
		t.Fatal(err)
	}
	var sent []string
	n, err := ob.Drain(ctx, 2, func(_ context.Context, m outbox.Message) error { sent = append(sent, m.EventType); return nil })
	if err != nil || n != 2 || len(sent) != 2 || sent[0] != "a" || sent[1] != "b" || ob.Unpublished() != 1 {
		t.Fatalf("drain: n=%d err=%v sent=%v unpublished=%d", n, err, sent, ob.Unpublished())
	}
	n, err = ob.Drain(ctx, 10, func(context.Context, outbox.Message) error { return errBoom })
	if !errors.Is(err, errBoom) || n != 0 || ob.Unpublished() != 1 || ob.LastError(2) == "" {
		t.Fatalf("drain failure: n=%d err=%v unpublished=%d last=%q", n, err, ob.Unpublished(), ob.LastError(2))
	}
	if len(ob.Messages()) != 3 {
		t.Fatalf("messages = %d", len(ob.Messages()))
	}
}

func TestProcessedEventsClaimOnce(t *testing.T) {
	p := memory.NewProcessedEventRepo()
	ctx := context.Background()
	first, _ := p.Claim(ctx, "demand", "e1")
	again, _ := p.Claim(ctx, "demand", "e1")
	other, _ := p.Claim(ctx, "product", "e1")
	if !first || again || !other {
		t.Fatalf("first=%v again=%v otherConsumer=%v", first, again, other)
	}
}
