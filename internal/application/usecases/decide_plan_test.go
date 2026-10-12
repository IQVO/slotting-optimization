package usecases_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

func TestApproveSupersedesThePreviousPlanInOneUnitOfWork(t *testing.T) {
	h := newHarness()
	h.approved("plan-1", "SITE-1", t0.Add(-2*time.Hour))
	h.draft("plan-2", "SITE-1", t0.Add(-time.Hour))
	h.draft("plan-3", "OTHER", t0.Add(-time.Hour))

	p, err := (&usecases.ApprovePlan{Writer: h.writer}).Handle(context.Background(), "plan-2")
	noErr(t, err)
	snap := p.Snapshot()
	eq(t, "state", snap.State, slotplan.StateApproved)
	eq(t, "supersedes", snap.SupersedesPlanID, "plan-1")
	eq(t, "version", snap.Version, 2)
	timeEq(t, "approved at", snap.ApprovedAt, t0)
	eq(t, "previous plan state", h.repo.plans["plan-1"].State, slotplan.StateSuperseded)
	eq(t, "other site's plan state", h.repo.plans["plan-3"].State, slotplan.StateDraft)
	assertSupersedeOrder(t, h)
}

func assertSupersedeOrder(t *testing.T, h *harness) {
	t.Helper()
	eq(t, "units of work", h.uow.runs, 1)
	eq(t, "saves", len(h.repo.saves), 2)
	eq(t, "first save (the previous plan goes first)", h.repo.saves[0].id, "plan-1")
	eq(t, "second save", h.repo.saves[1].id, "plan-2")
	eq(t, "outbox (superseding raises no event)", strings.Join(h.outbox.types(), ","), "SlotPlanApproved")
	ev := h.enc.events[0].(slotplan.SlotPlanApproved)
	eq(t, "event supersedes", ev.SupersedesPlanID, "plan-1")
	eq(t, "event assignments", len(ev.Assignments), 1)
}

func TestApproveTheFirstPlanOfASite(t *testing.T) {
	h := newHarness()
	h.draft("plan-1", "SITE-1", t0)
	p, err := (&usecases.ApprovePlan{Writer: h.writer}).Handle(context.Background(), "plan-1")
	noErr(t, err)
	eq(t, "supersedes", p.Snapshot().SupersedesPlanID, "")
	eq(t, "saves", len(h.repo.saves), 1)
}

func TestApproveNotADraftChangesNothing(t *testing.T) {
	h := newHarness()
	h.approved("plan-1", "SITE-1", t0)
	h.draft("plan-2", "SITE-1", t0)
	uc := &usecases.ApprovePlan{Writer: h.writer}
	_, err := uc.Handle(context.Background(), "plan-2")
	noErr(t, err)
	_, err = uc.Handle(context.Background(), "plan-2")
	isErr(t, err, slotplan.ErrNotDraft)
	_, err = uc.Handle(context.Background(), "plan-1") // superseded by now
	isErr(t, err, slotplan.ErrNotDraft)
	eq(t, "saves (failed approvals save nothing)", len(h.repo.saves), 2)
}

// TestApproveErrors drives every failure of ApprovePlan: setup arranges the
// fakes after plan-1 (Approved) and plan-2 (Draft) of SITE-1 are stored.
func TestApproveErrors(t *testing.T) {
	cases := []struct {
		name   string
		id     string
		setup  func(h *harness)
		wantIs error
	}{
		{"bad id", "nope", func(*harness) {}, slotplan.ErrInvalidPlanID},
		{"unknown plan", "plan-7", func(*harness) {}, repository.ErrPlanNotFound},
		{"lost the supersede race", "plan-2", func(h *harness) { h.repo.saveErr["plan-1"] = repository.ErrConcurrentModification }, repository.ErrApprovedPlanConflict},
		{"lost the insert race", "plan-2", func(h *harness) {
			h.repo.saveErr["plan-1"] = nil
			h.repo.saveErr["plan-2"] = repository.ErrApprovedPlanConflict
		}, repository.ErrApprovedPlanConflict},
		{"other save error", "plan-2", func(h *harness) { h.repo.saveErr["plan-1"] = errBoom }, errBoom},
		{"reading the approved plan fails", "plan-2", func(h *harness) { h.repo.curErr = errBoom }, errBoom},
		{"enqueue fails", "plan-2", func(h *harness) { h.outbox.err = errBoom }, errBoom},
		{"encode fails", "plan-2", func(h *harness) { h.enc.err = errBoom }, errBoom},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness()
			h.approved("plan-1", "SITE-1", t0)
			h.draft("plan-2", "SITE-1", t0)
			c.setup(h)
			_, err := (&usecases.ApprovePlan{Writer: h.writer}).Handle(context.Background(), c.id)
			isErr(t, err, c.wantIs)
		})
	}
}

func TestReject(t *testing.T) {
	uc := func(h *harness) *usecases.RejectPlan { return &usecases.RejectPlan{Writer: h.writer} }
	t.Run("rejects a draft", func(t *testing.T) {
		h := newHarness()
		h.draft("plan-1", "SITE-1", t0)
		p, err := uc(h).Handle(context.Background(), "plan-1", "  too many moves ")
		if err != nil {
			t.Fatal(err)
		}
		snap := p.Snapshot()
		if snap.State != slotplan.StateRejected || snap.RejectReason != "too many moves" || snap.Version != 2 {
			t.Fatalf("snapshot = %+v", snap)
		}
		if got := h.outbox.types(); len(got) != 1 || got[0] != "SlotPlanRejected" {
			t.Fatalf("outbox = %v", got)
		}
	})
	t.Run("bad id", func(t *testing.T) {
		if _, err := uc(newHarness()).Handle(context.Background(), "x", ""); !errors.Is(err, slotplan.ErrInvalidPlanID) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		if _, err := uc(newHarness()).Handle(context.Background(), "plan-1", ""); !errors.Is(err, repository.ErrPlanNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("not a draft", func(t *testing.T) {
		h := newHarness()
		h.approved("plan-1", "SITE-1", t0)
		if _, err := uc(h).Handle(context.Background(), "plan-1", ""); !errors.Is(err, slotplan.ErrNotDraft) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("reason too long", func(t *testing.T) {
		h := newHarness()
		h.draft("plan-1", "SITE-1", t0)
		if _, err := uc(h).Handle(context.Background(), "plan-1", strings.Repeat("x", 501)); !errors.Is(err, slotplan.ErrRejectReasonTooLong) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("save error", func(t *testing.T) {
		h := newHarness()
		h.draft("plan-1", "SITE-1", t0)
		h.repo.saveErr["plan-1"] = repository.ErrConcurrentModification
		if _, err := uc(h).Handle(context.Background(), "plan-1", ""); !errors.Is(err, repository.ErrConcurrentModification) {
			t.Fatalf("err = %v", err)
		}
	})
}
