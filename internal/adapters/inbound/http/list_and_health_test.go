package http_test

import (
	"net/http"
	"testing"
)

func TestListSlotPlans_PagesNewestFirstWithACursor(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	ids := []string{f.generate(t), f.generate(t), f.generate(t)}

	first := f.mustDo(t, http.MethodGet, "/slot-plans?limit=2", "", http.StatusOK)
	items := list(t, first.body["items"])
	cursor, _ := first.body["nextCursor"].(string)
	if len(items) != 2 || cursor == "" {
		t.Fatalf("first page = %s", first.raw)
	}
	second := f.mustDo(t, http.MethodGet, "/slot-plans?limit=2&cursor="+cursor, "", http.StatusOK)
	rest := list(t, second.body["items"])
	if len(rest) != 1 || second.body["nextCursor"] != nil {
		t.Fatalf("second page = %s", second.raw)
	}
	seen := map[string]bool{}
	for _, it := range append(items, rest...) {
		seen[it.(map[string]any)["planId"].(string)] = true
	}
	for _, id := range ids {
		if !seen[id] {
			t.Fatalf("plan %s missing from the pages", id)
		}
	}
	summary := items[0].(map[string]any)
	if summary["assignmentCount"] != float64(2) || summary["moveCount"] != float64(2) || summary["unassignedCount"] != float64(1) || summary["state"] != "Draft" {
		t.Fatalf("summary = %v", summary)
	}
	if _, has := summary["assignments"]; has {
		t.Fatal("a summary carries counts, not the content")
	}
}

func TestListSlotPlans_FiltersAndEmpty(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	id := f.generate(t)
	f.generate(t)
	f.mustDo(t, http.MethodPost, "/slot-plans/"+id+"/approve", "", http.StatusOK)

	approved := f.mustDo(t, http.MethodGet, "/slot-plans?state=Approved&siteId=SITE-1", "", http.StatusOK)
	if items := list(t, approved.body["items"]); len(items) != 1 || items[0].(map[string]any)["planId"] != id || items[0].(map[string]any)["approvedAt"] == nil {
		t.Fatalf("approved = %s", approved.raw)
	}
	none := f.mustDo(t, http.MethodGet, "/slot-plans?siteId=OTHER", "", http.StatusOK)
	if items := list(t, none.body["items"]); len(items) != 0 {
		t.Fatalf("other site = %s", none.raw)
	}
}

func TestListSlotPlans_400And500(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct{ query, slug string }{
		{"limit=0", "invalid-limit"}, {"limit=501", "invalid-limit"}, {"limit=abc", "invalid-limit"}, {"limit=-1", "invalid-limit"},
		{"cursor=!!", "invalid-cursor"}, {"cursor=bm9wZQ", "invalid-cursor"},
		{"state=Done", "invalid-state"}, {"siteId=a%20b", "invalid-site-id"},
	} {
		expectProblem(t, f.do(t, http.MethodGet, "/slot-plans?"+c.query, ""), http.StatusBadRequest, c.slug)
	}
	f.repo.listErr = errDB
	expectProblem(t, f.do(t, http.MethodGet, "/slot-plans", ""), http.StatusInternalServerError, "internal-error")
}

func TestForwardSlots(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	empty := f.mustDo(t, http.MethodGet, "/forward-slots", "", http.StatusOK)
	if empty.body["siteId"] != "SITE-1" || empty.body["planId"] != nil || len(list(t, empty.body["assignments"])) != 0 {
		t.Fatalf("a site without an approved plan = %s", empty.raw)
	}
	id := f.generate(t)
	f.mustDo(t, http.MethodPost, "/slot-plans/"+id+"/approve", "", http.StatusOK)
	r := f.mustDo(t, http.MethodGet, "/forward-slots?siteId=SITE-1", "", http.StatusOK)
	as := list(t, r.body["assignments"])
	if r.body["planId"] != id || r.body["approvedAt"] != "2026-10-08T12:00:00Z" || len(as) != 2 {
		t.Fatalf("map = %s", r.raw)
	}
	if a := as[0].(map[string]any); a["sku"] != "SKU-1" || a["slot"] != "A-01" || a["abcClass"] != "A" {
		t.Fatalf("first assignment = %v", a)
	}
	expectProblem(t, f.do(t, http.MethodGet, "/forward-slots?siteId=a%20b", ""), http.StatusBadRequest, "invalid-site-id")
	f.repo.currentErr = errDB
	expectProblem(t, f.do(t, http.MethodGet, "/forward-slots", ""), http.StatusInternalServerError, "internal-error")
}

func TestSkuVelocity(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	r := f.mustDo(t, http.MethodGet, "/sku-velocity?limit=2&windowDays=7", "", http.StatusOK)
	items := list(t, r.body["items"])
	if r.body["siteId"] != "SITE-1" || r.body["windowTo"] != "2026-10-08T12:00:00Z" || r.body["windowFrom"] != "2026-10-01T12:00:00Z" || len(items) != 2 {
		t.Fatalf("body = %s", r.raw)
	}
	if top := items[0].(map[string]any); top["sku"] != "SKU-1" || top["picks"] != float64(2) || top["units"] != float64(15) {
		t.Fatalf("top = %v", top)
	}
	all := f.mustDo(t, http.MethodGet, "/sku-velocity", "", http.StatusOK)
	if len(list(t, all.body["items"])) != 3 {
		t.Fatalf("defaults = %s", all.raw)
	}
	for _, q := range []string{"limit=0", "limit=501", "limit=x", "windowDays=0", "windowDays=366", "windowDays=x"} {
		expectProblem(t, f.do(t, http.MethodGet, "/sku-velocity?"+q, ""), http.StatusBadRequest, "invalid-limit")
	}
	expectProblem(t, f.do(t, http.MethodGet, "/sku-velocity?siteId=a%20b", ""), http.StatusBadRequest, "invalid-site-id")
}

func TestHealthReadinessAndMetrics(t *testing.T) {
	f := newFixture(t)
	if r := f.mustDo(t, http.MethodGet, "/healthz", "", http.StatusOK); r.body["status"] != "ok" {
		t.Fatalf("healthz = %s", r.raw)
	}
	if r := f.mustDo(t, http.MethodGet, "/readyz", "", http.StatusOK); r.body["status"] != "ready" {
		t.Fatalf("readyz = %s", r.raw)
	}
	if r := f.mustDo(t, http.MethodGet, "/metrics", "", http.StatusOK); r.raw != "# metrics\n" {
		t.Fatalf("metrics = %q", r.raw)
	}
	f.readiness.SetNotReady()
	if r := f.mustDo(t, http.MethodGet, "/readyz", "", http.StatusServiceUnavailable); r.body["status"] != "not_ready" {
		t.Fatalf("draining readyz = %s", r.raw)
	}
	f.mustDo(t, http.MethodGet, "/healthz", "", http.StatusOK) // liveness is never flipped
}

func TestAWrongMethodIs405AndUnknownRoutesAre404(t *testing.T) {
	f := newFixture(t)
	f.mustDo(t, http.MethodDelete, "/slot-plans", "", http.StatusMethodNotAllowed)
	f.mustDo(t, http.MethodGet, "/nope", "", http.StatusNotFound)
}
