package http_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/claudioed/slotting-optimization/internal/application/repository"
)

func list(t *testing.T, v any) []any {
	t.Helper()
	l, ok := v.([]any)
	if !ok {
		t.Fatalf("%v is not a JSON array", v)
	}
	return l
}

// field fails the test unless m[key] == want.
func field(t *testing.T, m map[string]any, key string, want any) {
	t.Helper()
	if got := m[key]; got != want {
		t.Errorf("%s = %v, want %v", key, got, want)
	}
}

func item(t *testing.T, l []any, i int) map[string]any {
	t.Helper()
	m, ok := l[i].(map[string]any)
	if !ok {
		t.Fatalf("item %d of %v is not an object", i, l)
	}
	return m
}

func TestGenerateSlotPlan_201WithLocationAndFullPlan(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	r := f.mustDo(t, http.MethodPost, "/slot-plans", `{"siteId":"SITE-1","lookbackDays":7}`, http.StatusCreated)
	if r.contentType != "application/json" {
		t.Fatalf("content type = %s", r.contentType)
	}
	field(t, r.body, "state", "Draft")
	field(t, r.body, "policy", "abc-velocity-v1")
	field(t, r.body, "version", float64(1))
	field(t, r.body, "windowTo", "2026-10-08T12:00:00Z")
	field(t, r.body, "windowFrom", "2026-10-01T12:00:00Z")
	field(t, r.body, "generatedAt", "2026-10-08T12:00:00Z")
	id := r.body["planId"].(string)
	if !strings.HasPrefix(id, "plan-") || r.location != "/slot-plans/"+id {
		t.Fatalf("id %q location %q", id, r.location)
	}
	if f.outbox.Unpublished() != 1 {
		t.Fatalf("SlotPlanGenerated must be in the outbox: %d", f.outbox.Unpublished())
	}
	assertSeededPlanContent(t, r.body)
}

func assertSeededPlanContent(t *testing.T, body map[string]any) {
	t.Helper()
	assignments := list(t, body["assignments"])
	if len(assignments) != 2 {
		t.Fatalf("assignments = %v", assignments)
	}
	first := item(t, assignments, 0)
	field(t, first, "sku", "SKU-1")
	field(t, first, "slot", "A-01")
	field(t, first, "abcClass", "A")
	field(t, first, "picks", float64(2))
	field(t, first, "units", float64(15))

	moves := list(t, body["moves"])
	if len(moves) != 2 {
		t.Fatalf("moves = %v", moves)
	}
	field(t, item(t, moves, 0), "kind", "Assign")
	field(t, item(t, moves, 0), "toSlot", "A-01")
	field(t, item(t, moves, 0), "fromSlot", nil)

	unassigned := list(t, body["unassigned"])
	if len(unassigned) != 1 {
		t.Fatalf("unassigned = %v", unassigned)
	}
	field(t, item(t, unassigned, 0), "sku", "SKU-3")
	field(t, item(t, unassigned, 0), "reason", "NoPhysicalProfile")
}

func TestGenerateSlotPlan_NoBodyUsesTheDefaults(t *testing.T) {
	f := newFixture(t)
	r := f.mustDo(t, http.MethodPost, "/slot-plans", "", http.StatusCreated)
	if r.body["siteId"] != "SITE-1" || r.body["windowFrom"] != "2026-09-10T12:00:00Z" {
		t.Fatalf("body = %s", r.raw)
	}
	for _, key := range []string{"assignments", "moves", "unassigned"} {
		if l := list(t, r.body[key]); len(l) != 0 {
			t.Fatalf("%s = %v", key, l)
		}
	}
}

func TestGenerateSlotPlan_400(t *testing.T) {
	cases := []struct {
		name, body, slug string
	}{
		{"malformed json", `{"siteId":`, "malformed-request"},
		{"unknown field", `{"site":"SITE-1"}`, "malformed-request"},
		{"trailing data", `{} {}`, "malformed-request"},
		{"wrong type", `{"lookbackDays":"7"}`, "malformed-request"},
		{"lookback zero", `{"lookbackDays":0}`, "invalid-lookback"},
		{"lookback negative", `{"lookbackDays":-3}`, "invalid-lookback"},
		{"lookback too big", `{"lookbackDays":366}`, "invalid-lookback"},
		{"site empty", `{"siteId":""}`, "invalid-site-id"},
		{"site with space", `{"siteId":"a b"}`, "invalid-site-id"},
		{"site too long", `{"siteId":"` + strings.Repeat("x", 65) + `"}`, "invalid-site-id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			expectProblem(t, f.do(t, http.MethodPost, "/slot-plans", c.body), http.StatusBadRequest, c.slug)
			if f.outbox.Unpublished() != 0 {
				t.Fatal("a rejected request must enqueue nothing")
			}
		})
	}
}

func TestGenerateSlotPlan_500(t *testing.T) {
	f := newFixture(t)
	f.repo.saveErr = errDB
	r := f.do(t, http.MethodPost, "/slot-plans", `{}`)
	expectProblem(t, r, http.StatusInternalServerError, "internal-error")
	if strings.Contains(r.raw, "db down") {
		t.Fatalf("an internal error must not leak its message: %s", r.raw)
	}
}

func TestGetSlotPlan(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	id := f.generate(t)
	r := f.mustDo(t, http.MethodGet, "/slot-plans/"+id, "", http.StatusOK)
	if r.body["planId"] != id || r.body["state"] != "Draft" {
		t.Fatalf("body = %s", r.raw)
	}
	expectProblem(t, f.do(t, http.MethodGet, "/slot-plans/plan-unknown", ""), http.StatusNotFound, "plan-not-found")
	expectProblem(t, f.do(t, http.MethodGet, "/slot-plans/nope", ""), http.StatusBadRequest, "invalid-plan-id")
	expectProblem(t, f.do(t, http.MethodGet, "/slot-plans/plan-a%2Fb", ""), http.StatusBadRequest, "invalid-plan-id")
	f.repo.getErr = errDB
	expectProblem(t, f.do(t, http.MethodGet, "/slot-plans/"+id, ""), http.StatusInternalServerError, "internal-error")
}

func TestApproveSlotPlan(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	id := f.generate(t)
	r := f.mustDo(t, http.MethodPost, "/slot-plans/"+id+"/approve", "", http.StatusOK)
	if r.body["state"] != "Approved" || r.body["version"] != float64(2) || r.body["approvedAt"] != "2026-10-08T12:00:00Z" || r.body["supersedesPlanId"] != nil {
		t.Fatalf("body = %s", r.raw)
	}
	if f.outbox.Unpublished() != 2 {
		t.Fatalf("SlotPlanGenerated + SlotPlanApproved: %d", f.outbox.Unpublished())
	}
	expectProblem(t, f.do(t, http.MethodPost, "/slot-plans/"+id+"/approve", ""), http.StatusConflict, "plan-not-draft")
	expectProblem(t, f.do(t, http.MethodPost, "/slot-plans/plan-unknown/approve", ""), http.StatusNotFound, "plan-not-found")
	expectProblem(t, f.do(t, http.MethodPost, "/slot-plans/nope/approve", ""), http.StatusBadRequest, "invalid-plan-id")
}

func TestApproveSupersedesTheSitesPreviousPlan(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	first := f.generate(t)
	f.mustDo(t, http.MethodPost, "/slot-plans/"+first+"/approve", "", http.StatusOK)
	second := f.generate(t)
	r := f.mustDo(t, http.MethodPost, "/slot-plans/"+second+"/approve", "", http.StatusOK)
	if r.body["supersedesPlanId"] != first {
		t.Fatalf("body = %s", r.raw)
	}
	old := f.mustDo(t, http.MethodGet, "/slot-plans/"+first, "", http.StatusOK)
	if old.body["state"] != "Superseded" || old.body["supersededAt"] == nil {
		t.Fatalf("previous plan = %s", old.raw)
	}
	fwd := f.mustDo(t, http.MethodGet, "/forward-slots", "", http.StatusOK)
	if fwd.body["planId"] != second {
		t.Fatalf("forward slots = %s", fwd.raw)
	}
}

func TestApproveConflicts(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	id := f.generate(t)
	f.repo.saveErr = repository.ErrApprovedPlanConflict
	expectProblem(t, f.do(t, http.MethodPost, "/slot-plans/"+id+"/approve", ""), http.StatusConflict, "approved-plan-conflict")
	f.repo.saveErr = repository.ErrConcurrentModification
	expectProblem(t, f.do(t, http.MethodPost, "/slot-plans/"+id+"/approve", ""), http.StatusConflict, "concurrent-modification")
	f.repo.saveErr = errDB
	expectProblem(t, f.do(t, http.MethodPost, "/slot-plans/"+id+"/approve", ""), http.StatusInternalServerError, "internal-error")
}

func TestRejectSlotPlan(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	id := f.generate(t)
	r := f.mustDo(t, http.MethodPost, "/slot-plans/"+id+"/reject", `{"reason":"Too many relocations."}`, http.StatusOK)
	if r.body["state"] != "Rejected" || r.body["rejectReason"] != "Too many relocations." || r.body["rejectedAt"] != "2026-10-08T12:00:00Z" {
		t.Fatalf("body = %s", r.raw)
	}
	expectProblem(t, f.do(t, http.MethodPost, "/slot-plans/"+id+"/reject", ""), http.StatusConflict, "plan-not-draft")
	expectProblem(t, f.do(t, http.MethodPost, "/slot-plans/"+id+"/approve", ""), http.StatusConflict, "plan-not-draft")
}

func TestRejectWithoutABody(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	id := f.generate(t)
	r := f.mustDo(t, http.MethodPost, "/slot-plans/"+id+"/reject", "", http.StatusOK)
	if r.body["state"] != "Rejected" || r.body["rejectReason"] != nil {
		t.Fatalf("body = %s", r.raw)
	}
}

func TestRejectErrors(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	id := f.generate(t)
	expectProblem(t, f.do(t, http.MethodPost, "/slot-plans/"+id+"/reject", `{"why":"x"}`), http.StatusBadRequest, "malformed-request")
	expectProblem(t, f.do(t, http.MethodPost, "/slot-plans/"+id+"/reject", `{"reason":"`+strings.Repeat("x", 501)+`"}`), http.StatusBadRequest, "reject-reason-too-long")
	expectProblem(t, f.do(t, http.MethodPost, "/slot-plans/nope/reject", ""), http.StatusBadRequest, "invalid-plan-id")
	expectProblem(t, f.do(t, http.MethodPost, "/slot-plans/plan-unknown/reject", ""), http.StatusNotFound, "plan-not-found")
	f.repo.saveErr = errDB
	expectProblem(t, f.do(t, http.MethodPost, "/slot-plans/"+id+"/reject", ""), http.StatusInternalServerError, "internal-error")
}
