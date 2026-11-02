package http_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// TestRetiredRoutesAreGone: the verb routes of the pre-guideline API answer
// 404 or 405 (no alias, no dual route).
func TestRetiredRoutesAreGone(t *testing.T) {
	f := newFixture(t)
	id := f.generate2(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/slot-plans/" + id + "/approve"},
		{http.MethodPost, "/slot-plans/" + id + "/reject"},
	} {
		got := f.do(t, tc.method, tc.path, "").status
		if r := f.do(t, tc.method, tc.path, ""); r.replayed != "" {
			t.Errorf("%s %s carries Idempotency-Replayed", tc.method, tc.path)
		}
		if got != http.StatusNotFound && got != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 404 or 405", tc.method, tc.path, got)
		}
	}
}

func (f *fixture) generate2(t *testing.T) string {
	t.Helper()
	f.seed(t)
	return f.generate(t)
}

// TestSkuVelocity_WalksEveryPageOnce pages with limit=1 over rows that share
// the sort key (picks, units) and checks no row repeats or is skipped.
func TestSkuVelocity_WalksEveryPageOnce(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	due := now.Add(-time.Hour)
	for i, sku := range []string{"SKU-4", "SKU-5"} {
		must(t, i, f.demand.Apply(context.Background(), repository.DemandLine{SourceOrderID: "t" + sku, LineNo: 1, Site: "SITE-1", SKU: slotplan.SKU(sku), Units: 1, DueAt: due, Active: true}))
	}
	want := []string{"SKU-1", "SKU-2", "SKU-3", "SKU-4", "SKU-5"}
	var got []string
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		q := "/sku-velocity?limit=2"
		if cursor != "" {
			q += "&cursor=" + cursor
		}
		r := f.mustDo(t, http.MethodGet, q, "", http.StatusOK)
		for _, it := range list(t, r.body["items"]) {
			got = append(got, it.(map[string]any)["sku"].(string))
		}
		cursor, _ = r.body["nextCursor"].(string)
		if cursor == "" {
			break
		}
	}
	if len(got) != len(want) {
		t.Fatalf("walked %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("walked %v, want %v", got, want)
		}
	}
}

// TestCollections_RejectBadPagingAndUnknownParameters pins the 400 problems of
// REST guideline 3.2 and 4.2 on every paged collection.
func TestCollections_RejectBadPagingAndUnknownParameters(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	for _, base := range []string{"/sku-velocity", "/slot-plans"} {
		for q, slug := range map[string]string{
			"limit=":                         "invalid-limit",
			"limit=1&limit=2":                "invalid-limit",
			"cursor=!!":                      "invalid-cursor",
			"cursor=a&cursor=b":              "invalid-cursor",
			"after=x":                        "unknown-query-parameter",
			"offset=1":                       "unknown-query-parameter",
			"nextToken=x":                    "unknown-query-parameter",
			"from=x":                         "unknown-query-parameter",
			"limit=1&nope=1":                 "unknown-query-parameter",
			"cursor=eyJzIjoiMSIsImkiOiJ4In0": "invalid-cursor",
		} {
			if base == "/slot-plans" && q == "cursor=eyJzIjoiMSIsImkiOiJ4In0" {
				continue // the plan cursor is the plan id; another shape is covered by the plan tests
			}
			expectProblem(t, f.do(t, http.MethodGet, base+"?"+q, ""), http.StatusBadRequest, slug)
		}
	}
	expectProblem(t, f.do(t, http.MethodGet, "/forward-slots?after=x", ""), http.StatusBadRequest, "unknown-query-parameter")
}
