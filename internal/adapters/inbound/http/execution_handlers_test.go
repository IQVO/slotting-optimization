package http_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// ADR 0010: GET /slot-plans/{planId} carries the execution progress derived
// from the relocations recorded against the plan; the write responses do
// not.
func TestGetSlotPlan_CarriesExecutionProgress(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	created := f.mustDo(t, http.MethodPost, "/slot-plans", `{"siteId":"SITE-1"}`, http.StatusCreated)
	if _, has := created.body["execution"]; has {
		t.Fatalf("POST /slot-plans must not carry execution: %s", created.raw)
	}
	id := created.body["planId"].(string)
	f.mustDo(t, http.MethodPost, "/slot-plans/"+id+"/approve", "", http.StatusOK)
	at := now.Add(2 * time.Hour)
	for _, e := range []slotplan.MoveExecution{
		{Seq: 0, RelocationID: "rel-1", SKU: "SKU-1", FromBin: "RES-1", ToBin: "A-01", Quantity: 4, At: at.Add(-time.Hour)},
		{Seq: 0, RelocationID: "rel-2", SKU: "SKU-1", FromBin: "RES-2", ToBin: "A-01", Quantity: 3, At: at},
	} {
		if _, err := f.execs.Record(context.Background(), slotplan.PlanID(id), e); err != nil {
			t.Fatal(err)
		}
	}

	r := f.mustDo(t, http.MethodGet, "/slot-plans/"+id, "", http.StatusOK)
	exec, ok := r.body["execution"].(map[string]any)
	if !ok {
		t.Fatalf("execution missing: %s", r.raw)
	}
	field(t, exec, "movesTotal", float64(2))
	field(t, exec, "movesExecuted", float64(1))
	field(t, exec, "lastExecutedAt", "2026-10-08T14:00:00Z")
	moves := list(t, exec["moves"])
	if len(moves) != 2 {
		t.Fatalf("moves = %v", moves)
	}
	first, second := item(t, moves, 0), item(t, moves, 1)
	field(t, first, "moveSeq", float64(0))
	field(t, first, "sku", "SKU-1")
	field(t, first, "kind", "Assign")
	field(t, first, "executed", true)
	field(t, first, "executedQuantity", float64(7))
	field(t, first, "lastExecutedAt", "2026-10-08T14:00:00Z")
	field(t, second, "moveSeq", float64(1))
	field(t, second, "sku", "SKU-2")
	field(t, second, "executed", false)
	field(t, second, "executedQuantity", float64(0))
	field(t, second, "lastExecutedAt", nil)
}

func TestGetSlotPlan_ExecutionOfAPlanNotStarted(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	id := f.mustDo(t, http.MethodPost, "/slot-plans", "", http.StatusCreated).body["planId"].(string)
	r := f.mustDo(t, http.MethodGet, "/slot-plans/"+id, "", http.StatusOK)
	exec, ok := r.body["execution"].(map[string]any)
	if !ok {
		t.Fatalf("execution missing: %s", r.raw)
	}
	field(t, exec, "movesExecuted", float64(0))
	field(t, exec, "lastExecutedAt", nil)
}
