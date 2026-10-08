package mcp_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/memory"
	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

const missingPlan = "plan-00000000-0000-4000-8000-0000000000ff"

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func TestGetSlotPlan(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	id := h.draft(t)

	got := h.ok(t, "get_slot_plan", map[string]any{"plan_id": id})
	if got["plan_id"] != id || got["site_id"] != "SITE-1" || got["state"] != "Draft" || got["policy"] != "abc-velocity-v1" ||
		got["version"] != 1.0 || got["window_to"] != "2026-10-08T12:00:00Z" || got["window_from"] != "2026-09-10T12:00:00Z" ||
		got["generated_at"] != "2026-10-08T12:00:00Z" {
		t.Fatalf("plan = %v", got)
	}
	for _, absent := range []string{"approved_at", "rejected_at", "superseded_at", "reject_reason", "supersedes_plan_id"} {
		if _, has := got[absent]; has {
			t.Errorf("%s must be omitted on a fresh Draft: %v", absent, got)
		}
	}
	assertDraftContent(t, got)
}

// assertDraftContent checks the assignments, moves and unassigned list of the
// plan generated from the seeded data.
func assertDraftContent(t *testing.T, got map[string]any) {
	t.Helper()
	if skus := sorted(strs(got, "assignments", "sku")); !reflect.DeepEqual(skus, []string{"SKU-1", "SKU-2"}) {
		t.Fatalf("assigned = %v, want SKU-1 and SKU-2", skus)
	}
	first := got["assignments"].([]any)[0].(map[string]any)
	for _, key := range []string{"sku", "slot", "abc_class", "picks", "units"} {
		if _, has := first[key]; !has {
			t.Errorf("assignment lacks %s: %v", key, first)
		}
	}
	if un := strs(got, "unassigned", "sku"); !reflect.DeepEqual(un, []string{"SKU-3"}) {
		t.Fatalf("unassigned = %v, want only the SKU without dimensions", un)
	}
	if moves := got["moves"].([]any); len(moves) != 2 {
		t.Fatalf("moves = %v, want an Assign per assigned SKU", moves)
	}
}

func TestGetSlotPlan_ShowsTheDecision(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	approved := h.draft(t)
	if _, err := h.approve.Handle(context.Background(), approved); err != nil {
		t.Fatal(err)
	}
	if got := h.ok(t, "get_slot_plan", map[string]any{"plan_id": approved}); got["state"] != "Approved" || got["approved_at"] != "2026-10-08T12:00:00Z" {
		t.Fatalf("approved plan = %v", got)
	}

	rejected := h.draft(t)
	if _, err := h.reject.Handle(context.Background(), rejected, "too many relocations"); err != nil {
		t.Fatal(err)
	}
	if got := h.ok(t, "get_slot_plan", map[string]any{"plan_id": rejected}); got["state"] != "Rejected" || got["reject_reason"] != "too many relocations" || got["rejected_at"] == nil {
		t.Fatalf("rejected plan = %v", got)
	}
}

func TestGetSlotPlan_Errors(t *testing.T) {
	h := newHarness(t)
	failWith(t, h.session, "get_slot_plan", map[string]any{"plan_id": missingPlan}, "plan-not-found")
	failWith(t, h.session, "get_slot_plan", map[string]any{"plan_id": ""}, "invalid-plan-id")
	failWith(t, h.session, "get_slot_plan", map[string]any{"plan_id": "not a plan id"}, "invalid-plan-id")
	failWith(t, h.session, "get_slot_plan", map[string]any{"plan_id": "SKU-1"}, "invalid-plan-id")
}

// seedThreePlans leaves one Approved, one Rejected and one Draft plan of
// SITE-1 and returns their ids.
func seedThreePlans(t *testing.T, h *harness) (approved, rejected, draft string) {
	t.Helper()
	h.seed(t)
	approved = h.draft(t)
	if _, err := h.approve.Handle(context.Background(), approved); err != nil {
		t.Fatal(err)
	}
	rejected = h.draft(t)
	if _, err := h.reject.Handle(context.Background(), rejected, "no"); err != nil {
		t.Fatal(err)
	}
	draft = h.draft(t)
	return approved, rejected, draft
}

func TestListSlotPlans_FiltersAndPages(t *testing.T) {
	h := newHarness(t)
	approved, rejected, draft := seedThreePlans(t, h)

	all := h.ok(t, "list_slot_plans", map[string]any{})
	want := sorted([]string{approved, rejected, draft})
	if got := sorted(strs(all, "items", "plan_id")); !reflect.DeepEqual(got, want) {
		t.Fatalf("all = %v, want %v", got, want)
	}
	if _, has := all["next_cursor"]; has {
		t.Fatalf("a last page must omit next_cursor: %v", all)
	}
	summary := all["items"].([]any)[0].(map[string]any)
	for _, key := range []string{"plan_id", "site_id", "state", "policy", "window_from", "window_to", "generated_at", "assignment_count", "move_count", "unassigned_count", "version"} {
		if _, has := summary[key]; !has {
			t.Errorf("summary lacks %s: %v", key, summary)
		}
	}
	if _, has := summary["assignments"]; has {
		t.Errorf("a summary must not carry the full assignment list: %v", summary)
	}

	assertPagesCoverEveryPlan(t, h, want)

	for state, id := range map[string]string{"Approved": approved, "Rejected": rejected, "Draft": draft} {
		if got := strs(h.ok(t, "list_slot_plans", map[string]any{"state": state}), "items", "plan_id"); !reflect.DeepEqual(got, []string{id}) {
			t.Fatalf("state=%s = %v, want [%s]", state, got, id)
		}
	}
	if got := strs(h.ok(t, "list_slot_plans", map[string]any{"state": "Superseded"}), "items", "plan_id"); len(got) != 0 {
		t.Fatalf("state=Superseded = %v, want none", got)
	}
	if got := strs(h.ok(t, "list_slot_plans", map[string]any{"site_id": "SITE-2"}), "items", "plan_id"); len(got) != 0 {
		t.Fatalf("site_id=SITE-2 = %v, want none", got)
	}
	if got := h.ok(t, "list_slot_plans", map[string]any{"site_id": "SITE-1", "state": "Approved"}); !reflect.DeepEqual(strs(got, "items", "plan_id"), []string{approved}) {
		t.Fatalf("site_id+state = %v", got)
	}
}

// assertPagesCoverEveryPlan pages two at a time and requires every plan
// exactly once.
func assertPagesCoverEveryPlan(t *testing.T, h *harness, want []string) {
	t.Helper()
	first := h.ok(t, "list_slot_plans", map[string]any{"limit": 2})
	if len(strs(first, "items", "plan_id")) != 2 || first["next_cursor"] == nil {
		t.Fatalf("first page = %v", first)
	}
	second := h.ok(t, "list_slot_plans", map[string]any{"limit": 2, "cursor": first["next_cursor"]})
	if len(strs(second, "items", "plan_id")) != 1 {
		t.Fatalf("second page = %v", second)
	}
	if _, has := second["next_cursor"]; has {
		t.Fatalf("the last page must omit next_cursor: %v", second)
	}
	paged := sorted(append(strs(first, "items", "plan_id"), strs(second, "items", "plan_id")...))
	if !reflect.DeepEqual(paged, want) {
		t.Fatalf("paged = %v, want %v", paged, want)
	}

}

func TestListSlotPlans_EmptyIsAnEmptyArray(t *testing.T) {
	got := newHarness(t).ok(t, "list_slot_plans", map[string]any{"state": "Draft"})
	if items, isSlice := got["items"].([]any); !isSlice || len(items) != 0 {
		t.Fatalf("items = %#v, want []", got["items"])
	}
}

func TestListSlotPlans_MalformedQueries(t *testing.T) {
	h := newHarness(t)
	for name, tc := range map[string]struct {
		args map[string]any
		slug string
	}{
		"unknown state":  {map[string]any{"state": "Bogus"}, "invalid-state"},
		"limit too big":  {map[string]any{"limit": 501}, "invalid-limit"},
		"negative limit": {map[string]any{"limit": -1}, "invalid-limit"},
		"bad cursor":     {map[string]any{"cursor": "!!not-base64!!"}, "invalid-cursor"},
		"bad site":       {map[string]any{"site_id": "no spaces allowed"}, "invalid-site-id"},
	} {
		t.Run(name, func(t *testing.T) { failWith(t, h.session, "list_slot_plans", tc.args, tc.slug) })
	}
}

func TestGetForwardSlots(t *testing.T) {
	h := newHarness(t)
	h.seed(t)

	none := h.ok(t, "get_forward_slots", map[string]any{})
	if none["site_id"] != "SITE-1" || none["plan_id"] != nil || none["approved_at"] != nil {
		t.Fatalf("a site without an Approved plan = %v", none)
	}
	if items, isSlice := none["assignments"].([]any); !isSlice || len(items) != 0 {
		t.Fatalf("assignments = %#v, want []", none["assignments"])
	}

	approved := h.draft(t)
	if _, err := h.approve.Handle(context.Background(), approved); err != nil {
		t.Fatal(err)
	}
	// A Draft does not change the current map.
	h.draft(t)

	assertCurrentMap(t, h, approved)

	if other := h.ok(t, "get_forward_slots", map[string]any{"site_id": "SITE-2"}); other["site_id"] != "SITE-2" || other["plan_id"] != nil {
		t.Fatalf("another site = %v", other)
	}
}

// assertCurrentMap requires the default and the explicit site to answer with
// the Approved plan's map.
func assertCurrentMap(t *testing.T, h *harness, approved string) {
	t.Helper()
	for name, args := range map[string]map[string]any{"default site": {}, "explicit site": {"site_id": "SITE-1"}} {
		got := h.ok(t, "get_forward_slots", args)
		if got["site_id"] != "SITE-1" || got["plan_id"] != approved || got["approved_at"] != "2026-10-08T12:00:00Z" {
			t.Fatalf("%s: forward slots = %v", name, got)
		}
		if skus := sorted(strs(got, "assignments", "sku")); !reflect.DeepEqual(skus, []string{"SKU-1", "SKU-2"}) {
			t.Fatalf("%s: assigned = %v", name, skus)
		}
		a := got["assignments"].([]any)[0].(map[string]any)
		if a["slot"] == "" || a["abc_class"] == "" || len(a) != 3 {
			t.Fatalf("%s: assignment = %v, want sku, slot and abc_class only", name, a)
		}
	}
}

func TestGetForwardSlots_Errors(t *testing.T) {
	h := newHarness(t)
	failWith(t, h.session, "get_forward_slots", map[string]any{"site_id": "no spaces allowed"}, "invalid-site-id")

	noDefault := newHarnessWithSite(t, "")
	failWith(t, noDefault.session, "get_forward_slots", map[string]any{}, "invalid-site-id")
	if got := noDefault.ok(t, "get_forward_slots", map[string]any{"site_id": "SITE-1"}); got["site_id"] != "SITE-1" {
		t.Fatalf("an explicit site works without a default: %v", got)
	}
}

func TestGetSkuVelocity(t *testing.T) {
	h := newHarness(t)
	h.seed(t)

	got := h.ok(t, "get_sku_velocity", map[string]any{})
	if got["site_id"] != "SITE-1" || got["window_to"] != "2026-10-08T12:00:00Z" || got["window_from"] != "2026-09-10T12:00:00Z" {
		t.Fatalf("velocity = %v", got)
	}
	// Fastest first; the stale line (60 days old), the inactive line and the
	// other site's demand are not counted.
	if skus := strs(got, "items", "sku"); !reflect.DeepEqual(skus, []string{"SKU-1", "SKU-2", "SKU-3"}) {
		t.Fatalf("skus = %v", skus)
	}
	top := got["items"].([]any)[0].(map[string]any)
	if top["picks"] != 2.0 || top["units"] != 15.0 {
		t.Fatalf("SKU-1 = %v, want 2 picks / 15 units", top)
	}

	if skus := strs(h.ok(t, "get_sku_velocity", map[string]any{"limit": 1}), "items", "sku"); !reflect.DeepEqual(skus, []string{"SKU-1"}) {
		t.Fatalf("limit=1 = %v", skus)
	}
	wide := h.ok(t, "get_sku_velocity", map[string]any{"window_days": 90})
	if skus := strs(wide, "items", "sku"); !reflect.DeepEqual(skus, []string{"SKU-1", "SKU-2", "SKU-OLD", "SKU-3"}) {
		t.Fatalf("window_days=90 = %v", skus)
	}
	if wide["window_from"] != "2026-07-10T12:00:00Z" {
		t.Fatalf("window_from = %v", wide["window_from"])
	}
	if skus := strs(h.ok(t, "get_sku_velocity", map[string]any{"site_id": "SITE-2"}), "items", "sku"); !reflect.DeepEqual(skus, []string{"SKU-9"}) {
		t.Fatalf("site_id=SITE-2 = %v", skus)
	}
	empty := h.ok(t, "get_sku_velocity", map[string]any{"site_id": "SITE-3"})
	if items, isSlice := empty["items"].([]any); !isSlice || len(items) != 0 {
		t.Fatalf("items = %#v, want []", empty["items"])
	}
}

func TestGetSkuVelocity_Errors(t *testing.T) {
	h := newHarness(t)
	for name, args := range map[string]map[string]any{
		"limit too big":   {"limit": 501},
		"negative limit":  {"limit": -1},
		"window too wide": {"window_days": 366},
		"negative window": {"window_days": -1},
	} {
		t.Run(name, func(t *testing.T) { failWith(t, h.session, "get_sku_velocity", args, "invalid-limit") })
	}
	failWith(t, h.session, "get_sku_velocity", map[string]any{"site_id": "no spaces allowed"}, "invalid-site-id")
	failWith(t, newHarnessWithSite(t, "").session, "get_sku_velocity", map[string]any{}, "invalid-site-id")
}

// No tool call ever enqueues an event: reads add no outbox row.
func TestReadsNeverWrite(t *testing.T) {
	h := newHarness(t)
	approved, _, _ := seedThreePlans(t, h)
	before := len(h.outbox.Messages())
	if before == 0 {
		t.Fatal("seeding should have enqueued the plan events")
	}
	h.ok(t, "get_slot_plan", map[string]any{"plan_id": approved})
	h.ok(t, "list_slot_plans", map[string]any{})
	h.ok(t, "get_forward_slots", map[string]any{})
	h.ok(t, "get_sku_velocity", map[string]any{})
	if after := len(h.outbox.Messages()); after != before {
		t.Fatalf("outbox rows %d -> %d: an MCP read wrote an event", before, after)
	}
}

// brokenPlans fails every read with an infrastructure error carrying a DSN.
type brokenPlans struct{ *memory.SlotPlanRepo }

var errInfra = errors.New("dial tcp 10.0.0.9:5432: password authentication failed for postgres://app:hunter2@db")

func (brokenPlans) Get(context.Context, slotplan.PlanID) (*slotplan.SlotPlan, error) {
	return nil, errInfra
}

func (brokenPlans) Current(context.Context, slotplan.SiteID) (*slotplan.SlotPlan, error) {
	return nil, errInfra
}

func (brokenPlans) List(context.Context, repository.PlanFilter, slotplan.PlanID, int) ([]*slotplan.SlotPlan, error) {
	return nil, errInfra
}

// An untyped (infrastructure) failure is reported as a generic internal-error:
// no DSN, host or SQL text reaches the model.
func TestInfrastructureErrorsDoNotLeak(t *testing.T) {
	session := connectSession(t, depsFor(brokenPlans{memory.NewSlotPlanRepo()}, memory.NewDemandLedger(), "SITE-1"))
	for name, args := range map[string]map[string]any{
		"get_slot_plan":     {"plan_id": missingPlan},
		"list_slot_plans":   {},
		"get_forward_slots": {},
	} {
		failWith(t, session, name, args, "internal-error")
		if got := text(call(t, session, name, args)); strings.Contains(got, "hunter2") || strings.Contains(got, "10.0.0.9") {
			t.Errorf("%s leaked infrastructure detail: %q", name, got)
		}
	}
}
