package repocontract

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// ExecutionStores is what the MoveExecutionLedger contract runs on: the
// ledger and the plan repository its rows belong to (the Postgres table
// references slot_plans).
type ExecutionStores struct {
	Ledger ports.MoveExecutionLedger
	Plans  ports.SlotPlanRepository
}

// MoveExecutionLedger runs the MoveExecutionLedger contract (ADR 0010).
// newStores must return EMPTY stores on every call.
func MoveExecutionLedger(t *testing.T, newStores func(t *testing.T) ExecutionStores) {
	t.Helper()
	cases := map[string]func(t *testing.T, s ExecutionStores){
		"a recorded execution reads back":           executionRoundTrip,
		"a relocation id is recorded once":          executionOncePerRelocation,
		"executions are per plan":                   executionsPerPlan,
		"a plan without executions reads empty":     executionsEmpty,
		"several relocations of one move all count": executionsOfOneMove,
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) { fn(t, newStores(t)) })
	}
}

func savePlan(t *testing.T, s ExecutionStores, id string) slotplan.PlanID {
	t.Helper()
	must(t, s.Plans.Save(context.Background(), Draft(t, id, "SITE-1", T0), 0))
	return slotplan.PlanID(id)
}

func execution(relocation string, seq int, qty int64, at time.Time) slotplan.MoveExecution {
	return slotplan.MoveExecution{
		Seq: seq, RelocationID: relocation, SKU: "SKU-1", FromBin: "RES-1", ToBin: "FWD-1", Quantity: qty, At: at,
	}
}

func record(t *testing.T, s ExecutionStores, plan slotplan.PlanID, e slotplan.MoveExecution) bool {
	t.Helper()
	recorded, err := s.Ledger.Record(context.Background(), plan, e)
	must(t, err)
	return recorded
}

func forPlan(t *testing.T, s ExecutionStores, plan slotplan.PlanID) []slotplan.MoveExecution {
	t.Helper()
	got, err := s.Ledger.ForPlan(context.Background(), plan)
	must(t, err)
	slices.SortFunc(got, func(a, b slotplan.MoveExecution) int { return strings.Compare(a.RelocationID, b.RelocationID) })
	return got
}

func executionRoundTrip(t *testing.T, s ExecutionStores) {
	plan := savePlan(t, s, "plan-x")
	at := T0.Add(90 * time.Minute).In(time.FixedZone("BRT", -3*3600))
	if !record(t, s, plan, execution("rel-1", 0, 7, at)) {
		t.Fatal("the first record of a relocation must report recorded")
	}
	got := forPlan(t, s, plan)
	want := execution("rel-1", 0, 7, T0.Add(90*time.Minute))
	if len(got) != 1 || got[0].Seq != want.Seq || got[0].RelocationID != want.RelocationID || got[0].SKU != want.SKU ||
		got[0].FromBin != want.FromBin || got[0].ToBin != want.ToBin || got[0].Quantity != want.Quantity || !got[0].At.Equal(want.At) {
		t.Fatalf("ForPlan = %+v, want [%+v]", got, want)
	}
	if got[0].At.Location() != time.UTC {
		t.Fatalf("At must read back in UTC, got %v", got[0].At.Location())
	}
}

func executionOncePerRelocation(t *testing.T, s ExecutionStores) {
	plan := savePlan(t, s, "plan-x")
	record(t, s, plan, execution("rel-1", 0, 7, T0))
	if record(t, s, plan, execution("rel-1", 0, 9, T0.Add(time.Hour))) {
		t.Fatal("a second record of the same relocation id must report not recorded")
	}
	got := forPlan(t, s, plan)
	if len(got) != 1 || got[0].Quantity != 7 {
		t.Fatalf("ForPlan = %+v: the first record stands", got)
	}
}

func executionsPerPlan(t *testing.T, s ExecutionStores) {
	a, b := savePlan(t, s, "plan-a"), savePlan(t, s, "plan-b")
	record(t, s, a, execution("rel-1", 0, 1, T0))
	record(t, s, b, execution("rel-2", 0, 2, T0))
	if got := forPlan(t, s, a); len(got) != 1 || got[0].RelocationID != "rel-1" {
		t.Fatalf("plan-a = %+v", got)
	}
	if got := forPlan(t, s, b); len(got) != 1 || got[0].RelocationID != "rel-2" {
		t.Fatalf("plan-b = %+v", got)
	}
}

func executionsEmpty(t *testing.T, s ExecutionStores) {
	plan := savePlan(t, s, "plan-x")
	if got := forPlan(t, s, plan); len(got) != 0 {
		t.Fatalf("ForPlan = %+v, want none", got)
	}
}

func executionsOfOneMove(t *testing.T, s ExecutionStores) {
	plan := savePlan(t, s, "plan-x")
	record(t, s, plan, execution("rel-1", 0, 3, T0))
	record(t, s, plan, execution("rel-2", 0, 4, T0.Add(time.Minute)))
	got := forPlan(t, s, plan)
	if len(got) != 2 || got[0].Quantity+got[1].Quantity != 7 {
		t.Fatalf("ForPlan = %+v: both relocations of the move are kept", got)
	}
}
