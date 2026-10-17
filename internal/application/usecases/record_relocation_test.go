package usecases_test

import (
	"context"
	"testing"
	"time"

	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// fakeExecutions is an in-test ports.MoveExecutionLedger keyed on the
// relocation id, like the table's primary key.
type fakeExecutions struct {
	rows      map[string]slotplan.MoveExecution
	plans     map[string]slotplan.PlanID
	recordErr error
	readErr   error
}

func newFakeExecutions() *fakeExecutions {
	return &fakeExecutions{rows: map[string]slotplan.MoveExecution{}, plans: map[string]slotplan.PlanID{}}
}

func (f *fakeExecutions) Record(_ context.Context, plan slotplan.PlanID, e slotplan.MoveExecution) (bool, error) {
	if f.recordErr != nil {
		return false, f.recordErr
	}
	if _, dup := f.rows[e.RelocationID]; dup {
		return false, nil
	}
	f.rows[e.RelocationID] = e
	f.plans[e.RelocationID] = plan
	return true, nil
}

func (f *fakeExecutions) ForPlan(_ context.Context, plan slotplan.PlanID) ([]slotplan.MoveExecution, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	var out []slotplan.MoveExecution
	for id, e := range f.rows {
		if f.plans[id] == plan {
			out = append(out, e)
		}
	}
	return out, nil
}

type observed struct{ outcome, reason string }

type fakeRelocationMetrics struct{ seen []observed }

func (m *fakeRelocationMetrics) RelocationObserved(_ context.Context, outcome, reason string) {
	m.seen = append(m.seen, observed{outcome, reason})
}

// executionHarness stores plan-1 of SITE-1 Approved with three moves in
// move_seq order: 0 Assign SKU-1 -> FWD-01, 1 Relocate SKU-2 FWD-09 ->
// FWD-02, 2 Vacate SKU-3 from FWD-03.
type executionHarness struct {
	*harness
	execs   *fakeExecutions
	metrics *fakeRelocationMetrics
	uc      *usecases.RecordRelocation
}

func newExecutionHarness(t *testing.T) *executionHarness {
	t.Helper()
	h := &executionHarness{harness: newHarness(), execs: newFakeExecutions(), metrics: &fakeRelocationMetrics{}}
	h.storePlan(t, "plan-1", "SITE-1", true)
	h.uc = &usecases.RecordRelocation{Intake: h.intake(), Plans: h.repo, Executions: h.execs, Metrics: h.metrics}
	return h
}

func (h *executionHarness) storePlan(t *testing.T, id, site string, approve bool) *slotplan.SlotPlan {
	t.Helper()
	w, err := slotplan.NewWindow(t0.Add(-28*24*time.Hour), t0)
	noErr(t, err)
	p, _, err := slotplan.Generate(slotplan.NewPlan{
		ID: slotplan.PlanID(id), Site: slotplan.SiteID(site), Window: w, Policy: slotplan.PolicyABCVelocityV1,
		Assignments: []slotplan.Assignment{
			{SKU: "SKU-1", Slot: "FWD-01", Class: slotplan.ClassA, Picks: 9, Units: 9},
			{SKU: "SKU-2", Slot: "FWD-02", Class: slotplan.ClassB, Picks: 3, Units: 3},
		},
		Moves: []slotplan.Move{
			{SKU: "SKU-3", From: "FWD-03", Kind: slotplan.MoveVacate},
			{SKU: "SKU-1", To: "FWD-01", Kind: slotplan.MoveAssign},
			{SKU: "SKU-2", From: "FWD-09", To: "FWD-02", Kind: slotplan.MoveRelocate},
		},
	}, t0)
	noErr(t, err)
	if approve {
		_, err = p.Approve("", t0.Add(time.Minute))
		noErr(t, err)
	}
	h.repo.put(p)
	return p
}

func fact(id, sku, from, to, sourceRef string) usecases.RelocationFact {
	return usecases.RelocationFact{
		RelocationID: id, SiteCode: "SITE-1", SKU: sku, FromBin: from, ToBin: to, Quantity: 7,
		Reason: "SLOTTING", SourceRef: sourceRef, OccurredAt: t0.Add(time.Hour),
	}
}

func TestRecordRelocation_MatchesTheMoveReference(t *testing.T) {
	h := newExecutionHarness(t)
	res, err := h.uc.Handle(context.Background(), "ce-1", fact("rel-1", "SKU-2", "FWD-09", "FWD-02", "plan-1:1"))
	noErr(t, err)
	eq(t, "outcome", res.Outcome, usecases.OutcomeApplied)
	eq(t, "plan", res.Plan, slotplan.PlanID("plan-1"))
	eq(t, "move_seq", res.MoveSeq, 1)
	eq(t, "unmatched", res.Unmatched, slotplan.UnmatchedReason(""))
	got := h.execs.rows["rel-1"]
	eq(t, "recorded seq", got.Seq, 1)
	eq(t, "recorded quantity", got.Quantity, int64(7))
	eq(t, "recorded sku", got.SKU, slotplan.SKU("SKU-2"))
	eq(t, "recorded from", got.FromBin, slotplan.SlotCode("FWD-09"))
	eq(t, "recorded to", got.ToBin, slotplan.SlotCode("FWD-02"))
	timeEq(t, "recorded at", got.At, t0.Add(time.Hour))
	eq(t, "recorded plan", h.execs.plans["rel-1"], slotplan.PlanID("plan-1"))
	eq(t, "metrics", len(h.metrics.seen), 1)
	eq(t, "metric", h.metrics.seen[0], observed{"applied", ""})
}

func TestRecordRelocation_ReferenceWinsOverBins(t *testing.T) {
	h := newExecutionHarness(t)
	// The reference names move 0 of plan-1 (SKU-1); the bins alone would match
	// no move (the Assign fills FWD-01): the reference decides.
	res, err := h.uc.Handle(context.Background(), "ce-1", fact("rel-1", "SKU-1", "RES-4", "FWD-77", " plan-1:0 "))
	noErr(t, err)
	eq(t, "outcome", res.Outcome, usecases.OutcomeApplied)
	eq(t, "move_seq", res.MoveSeq, 0)
}

func TestRecordRelocation_FallsBackToTheBinsWithoutAReference(t *testing.T) {
	for _, ref := range []string{"", "task-1", "plan-9#3"} {
		h := newExecutionHarness(t)
		res, err := h.uc.Handle(context.Background(), "ce-1", fact("rel-1", "SKU-3", "FWD-03", "RES-12", ref))
		noErr(t, err)
		eq(t, "outcome for "+ref, res.Outcome, usecases.OutcomeApplied)
		eq(t, "move_seq for "+ref, res.MoveSeq, 2)
		eq(t, "plan for "+ref, res.Plan, slotplan.PlanID("plan-1"))
	}
}

// A relocation whose reason is not SLOTTING executes no plan move, even when
// its SKU and bins (or its source_ref) match one: a replenishment from a
// reserve bin must not inflate a plan's progress.
func TestRecordRelocation_OnlySlottingReasonExecutesAMove(t *testing.T) {
	for _, reason := range []string{"REPLENISHMENT", "MANUAL", "CONSOLIDATION", "", "slotting"} {
		for _, ref := range []string{"", "plan-1:2", "repl-7"} {
			h := newExecutionHarness(t)
			f := fact("rel-1", "SKU-3", "FWD-03", "RES-12", ref) // matches Vacate move 2 by bins and by reference
			f.Reason = reason
			res, err := h.uc.Handle(context.Background(), "ce-1", f)
			noErr(t, err)
			name := "reason=" + reason + " ref=" + ref
			eq(t, name+": outcome", res.Outcome, usecases.OutcomeUnmatched)
			eq(t, name+": reason", res.Unmatched, slotplan.UnmatchedNotSlotting)
			eq(t, name+": move_seq", res.MoveSeq, -1)
			eq(t, name+": recorded", len(h.execs.rows), 0)
			eq(t, name+": claimed", h.process.seen[usecases.RelocationConsumer+"/ce-1"], true)
			eq(t, name+": metric", h.metrics.seen[0], observed{"unmatched", string(slotplan.UnmatchedNotSlotting)})
		}
	}
}

func TestRecordRelocation_UnmatchedIsClaimedNotRecorded(t *testing.T) {
	tests := []struct {
		name string
		fact usecases.RelocationFact
		want slotplan.UnmatchedReason
	}{
		{"unknown plan", fact("rel-1", "SKU-1", "RES-1", "FWD-01", "plan-404:0"), slotplan.UnmatchedUnknownPlan},
		{"move past the end", fact("rel-1", "SKU-1", "RES-1", "FWD-01", "plan-1:3"), slotplan.UnmatchedUnknownMove},
		{"another sku", fact("rel-1", "SKU-9", "RES-1", "FWD-01", "plan-1:0"), slotplan.UnmatchedSKUMismatch},
		{"bins of no move", fact("rel-1", "SKU-2", "FWD-08", "FWD-02", ""), slotplan.UnmatchedNoMatchingMove},
	}
	for _, tc := range tests {
		h := newExecutionHarness(t)
		res, err := h.uc.Handle(context.Background(), "ce-1", tc.fact)
		noErr(t, err)
		eq(t, tc.name+": outcome", res.Outcome, usecases.OutcomeUnmatched)
		eq(t, tc.name+": reason", res.Unmatched, tc.want)
		eq(t, tc.name+": move_seq", res.MoveSeq, -1)
		eq(t, tc.name+": recorded", len(h.execs.rows), 0)
		eq(t, tc.name+": claimed", h.process.seen[usecases.RelocationConsumer+"/ce-1"], true)
		eq(t, tc.name+": metric", h.metrics.seen[0], observed{"unmatched", string(tc.want)})
	}
}

func TestRecordRelocation_NoCurrentPlanAndNotApproved(t *testing.T) {
	h := newExecutionHarness(t)
	other := fact("rel-1", "SKU-1", "RES-1", "FWD-01", "")
	other.SiteCode = "SITE-2"
	res, err := h.uc.Handle(context.Background(), "ce-1", other)
	noErr(t, err)
	eq(t, "no current plan", res.Unmatched, slotplan.UnmatchedNoCurrentPlan)

	h.storePlan(t, "plan-2", "SITE-1", false)
	res, err = h.uc.Handle(context.Background(), "ce-2", fact("rel-2", "SKU-1", "RES-1", "FWD-01", "plan-2:0"))
	noErr(t, err)
	eq(t, "draft plan", res.Unmatched, slotplan.UnmatchedPlanNotApproved)
}

func TestRecordRelocation_RedeliveryAndReplayAreDuplicates(t *testing.T) {
	h := newExecutionHarness(t)
	f := fact("rel-1", "SKU-2", "FWD-09", "FWD-02", "plan-1:1")
	_, err := h.uc.Handle(context.Background(), "ce-1", f)
	noErr(t, err)
	res, err := h.uc.Handle(context.Background(), "ce-1", f)
	noErr(t, err)
	eq(t, "same CloudEvents id", res.Outcome, usecases.OutcomeDuplicate)
	eq(t, "same CloudEvents id: move_seq", res.MoveSeq, -1)
	res, err = h.uc.Handle(context.Background(), "ce-2", f)
	noErr(t, err)
	eq(t, "same relocation, new id", res.Outcome, usecases.OutcomeDuplicate)
	eq(t, "recorded once", len(h.execs.rows), 1)
	eq(t, "metric", h.metrics.seen[2], observed{"duplicate", ""})
}

func TestRecordRelocation_InvalidFactTouchesNothing(t *testing.T) {
	h := newExecutionHarness(t)
	bad := fact("rel-1", "SKU-1", "RES-1", "FWD-01", "plan-1:0")
	bad.Quantity = 0
	_, err := h.uc.Handle(context.Background(), "ce-1", bad)
	isErr(t, err, usecases.ErrInvalidEvent)
	isErr(t, err, slotplan.ErrInvalidRelocation)
	eq(t, "units of work", h.uow.runs, 0)
	eq(t, "metrics", len(h.metrics.seen), 0)
}

func TestRecordRelocation_TransientFailuresRollBack(t *testing.T) {
	h := newExecutionHarness(t)
	h.execs.recordErr = errBoom
	_, err := h.uc.Handle(context.Background(), "ce-1", fact("rel-1", "SKU-2", "FWD-09", "FWD-02", "plan-1:1"))
	isErr(t, err, errBoom)
	eq(t, "failed units", h.uow.failures, 1)
	eq(t, "metrics", len(h.metrics.seen), 0)

	h = newExecutionHarness(t)
	h.repo.getErr = errBoom
	_, err = h.uc.Handle(context.Background(), "ce-1", fact("rel-1", "SKU-2", "FWD-09", "FWD-02", "plan-1:1"))
	isErr(t, err, errBoom)

	h = newExecutionHarness(t)
	h.repo.curErr = errBoom
	_, err = h.uc.Handle(context.Background(), "ce-1", fact("rel-1", "SKU-2", "FWD-09", "FWD-02", ""))
	isErr(t, err, errBoom)

	h = newExecutionHarness(t)
	h.process.err = errBoom
	_, err = h.uc.Handle(context.Background(), "ce-1", fact("rel-1", "SKU-2", "FWD-09", "FWD-02", ""))
	isErr(t, err, errBoom)
}

func TestRecordRelocation_NilMetricsIsFine(t *testing.T) {
	h := newExecutionHarness(t)
	h.uc.Metrics = nil
	res, err := h.uc.Handle(context.Background(), "ce-1", fact("rel-1", "SKU-2", "FWD-09", "FWD-02", "plan-1:1"))
	noErr(t, err)
	eq(t, "outcome", res.Outcome, usecases.OutcomeApplied)
}

func TestGetPlanView_CarriesExecutionProgress(t *testing.T) {
	h := newExecutionHarness(t)
	_, err := h.uc.Handle(context.Background(), "ce-1", fact("rel-1", "SKU-2", "FWD-09", "FWD-02", "plan-1:1"))
	noErr(t, err)
	v, err := (&usecases.GetPlan{Plans: h.repo, Executions: h.execs}).View(context.Background(), "plan-1")
	noErr(t, err)
	eq(t, "plan", v.Plan.ID(), slotplan.PlanID("plan-1"))
	if v.Execution == nil {
		t.Fatal("execution progress missing")
	}
	eq(t, "moves total", v.Execution.MovesTotal, 3)
	eq(t, "moves executed", v.Execution.MovesExecuted, 1)
	eq(t, "move 1 executed", v.Execution.Moves[1].Executed, true)
	timeEq(t, "last executed at", v.Execution.LastExecutedAt, t0.Add(time.Hour))
}

func TestGetPlanView_WithoutLedgerAndErrors(t *testing.T) {
	h := newExecutionHarness(t)
	v, err := (&usecases.GetPlan{Plans: h.repo}).View(context.Background(), "plan-1")
	noErr(t, err)
	if v.Execution != nil || v.Plan == nil {
		t.Fatalf("view = %+v: no ledger, no progress", v)
	}
	_, err = (&usecases.GetPlan{Plans: h.repo, Executions: h.execs}).View(context.Background(), "nope")
	isErr(t, err, slotplan.ErrInvalidPlanID)
	h.execs.readErr = errBoom
	_, err = (&usecases.GetPlan{Plans: h.repo, Executions: h.execs}).View(context.Background(), "plan-1")
	isErr(t, err, errBoom)
}
