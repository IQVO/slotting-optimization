package mcp_test

import (
	"context"
	"testing"
	"time"

	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// ADR 0010: get_slot_plan carries the plan's execution progress (snake_case,
// omitted when the server has no execution ledger).
func TestGetSlotPlan_CarriesExecutionProgress(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	id := h.draft(t)
	if _, err := h.approve.Handle(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	at := now.Add(90 * time.Minute)
	if _, err := h.execs.Record(context.Background(), slotplan.PlanID(id), slotplan.MoveExecution{
		Seq: 1, RelocationID: "rel-1", SKU: "SKU-2", FromBin: "RES-1", ToBin: "A-02", Quantity: 5, At: at,
	}); err != nil {
		t.Fatal(err)
	}

	got := h.ok(t, "get_slot_plan", map[string]any{"plan_id": id})
	exec, ok := got["execution"].(map[string]any)
	if !ok {
		t.Fatalf("execution missing: %v", got)
	}
	if exec["moves_total"] != 2.0 || exec["moves_executed"] != 1.0 || exec["last_executed_at"] != "2026-10-08T13:30:00Z" {
		t.Fatalf("execution = %v", exec)
	}
	moves := exec["moves"].([]any)
	assertMoveNotExecuted(t, moves[0].(map[string]any))
	assertMoveExecuted(t, moves[1].(map[string]any))
}

func assertMoveNotExecuted(t *testing.T, m map[string]any) {
	t.Helper()
	if m["move_seq"] != 0.0 || m["executed"] != false || m["executed_quantity"] != 0.0 {
		t.Fatalf("move 0 = %v", m)
	}
	if _, has := m["last_executed_at"]; has {
		t.Fatalf("move 0 was never executed, last_executed_at must be omitted: %v", m)
	}
}

func assertMoveExecuted(t *testing.T, m map[string]any) {
	t.Helper()
	if m["move_seq"] != 1.0 || m["sku"] != "SKU-2" || m["kind"] != "Assign" || m["executed"] != true ||
		m["executed_quantity"] != 5.0 || m["last_executed_at"] != "2026-10-08T13:30:00Z" {
		t.Fatalf("move 1 = %v", m)
	}
}

func TestGetSlotPlan_WithoutLedgerOmitsExecution(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	id := h.draft(t)
	session := connectSession(t, depsFor(h.generate.Plans, h.demand, "SITE-1"))
	res := call(t, session, "get_slot_plan", map[string]any{"plan_id": id})
	sc, isMap := res.StructuredContent.(map[string]any)
	if res.IsError || !isMap {
		t.Fatalf("get_slot_plan = %#v", res)
	}
	if _, has := sc["execution"]; has {
		t.Fatalf("execution must be omitted without a ledger: %v", sc)
	}
}
