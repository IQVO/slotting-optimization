package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	inboundhttp "github.com/claudioed/slotting-optimization/internal/adapters/inbound/http"
	"github.com/claudioed/slotting-optimization/internal/analytics/report"
)

// fakeReader is a scripted report.Reader.
type fakeReader struct {
	days    []report.FunnelDay
	query   report.FunnelQuery
	calls   int
	asOf    *time.Time
	readErr error
}

func (f *fakeReader) FunnelDays(_ context.Context, q report.FunnelQuery) ([]report.FunnelDay, error) {
	f.calls++
	f.query = q
	if f.readErr != nil {
		return nil, f.readErr
	}
	if f.days == nil {
		return []report.FunnelDay{}, nil
	}
	return f.days, nil
}

func (f *fakeReader) LastEventAt(context.Context) (*time.Time, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	return f.asOf, nil
}

var reportNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func newReportsServer(r *fakeReader) http.Handler {
	return inboundhttp.NewReportsRouter(&inboundhttp.ReportsServer{Reader: r, Now: func() time.Time { return reportNow }})
}

func getJSON(t *testing.T, h http.Handler, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	body := map[string]any{}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("body is not JSON: %v: %s", err, rec.Body.String())
		}
	}
	return rec, body
}

func TestSlotPlanFunnelReport(t *testing.T) {
	p50, p95 := 7200.0, 9000.0
	reader := &fakeReader{days: []report.FunnelDay{{
		Day:       time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC),
		Generated: 3, Approved: 2, Rejected: 1, Supersessions: 1,
		ApprovalLatency: report.Timing{Count: 2, P50: &p50, P95: &p95},
	}}}
	h := newReportsServer(reader)

	rec, body := getJSON(t, h, "/reports/slot-plan-funnel?from=2026-10-05T00:00:00Z&to=2026-10-06T00:00:00Z&siteId=SITE-1&granularity=day")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if reader.query.SiteID != "SITE-1" || reader.query.Granularity != report.GranularityDay {
		t.Fatalf("query = %+v", reader.query)
	}
	if !reader.query.Range.From.Equal(time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("range = %+v", reader.query.Range)
	}
	if body["site_id"] != "SITE-1" || body["granularity"] != "day" {
		t.Fatalf("body head = %v", body)
	}
	days := body["days"].([]any)
	if len(days) != 1 {
		t.Fatalf("days = %v", days)
	}
	day := days[0].(map[string]any)
	for name, want := range map[string]float64{
		"day": 0, "plans_generated": 3, "plans_approved": 2, "plans_rejected": 1, "supersessions": 1,
	} {
		if name == "day" {
			if day["day"] != "2026-10-05" {
				t.Fatalf("day = %v", day["day"])
			}
			continue
		}
		if day[name] != want {
			t.Fatalf("%s = %v, want %v", name, day[name], want)
		}
	}
	latency := day["approval_latency"].(map[string]any)
	if latency["count"] != float64(2) || latency["p50_seconds"] != p50 || latency["p95_seconds"] != p95 {
		t.Fatalf("latency = %v", latency)
	}
}

func TestSlotPlanFunnelDefaultsAndValidation(t *testing.T) {
	h := newReportsServer(&fakeReader{})

	// No params: the 30 days ending now, day granularity, every site.
	rec, body := getJSON(t, h, "/reports/slot-plan-funnel")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if body["from"] != "2026-09-08T12:00:00Z" || body["to"] != "2026-10-08T12:00:00Z" {
		t.Fatalf("default range = %v..%v", body["from"], body["to"])
	}
	if body["granularity"] != "day" {
		t.Fatalf("default granularity = %v", body["granularity"])
	}
	if _, present := body["site_id"]; present {
		t.Fatalf("site_id must be omitted when no site was given: %v", body)
	}

	for name, path := range map[string]string{
		"bad from":        "/reports/slot-plan-funnel?from=nope",
		"bad to":          "/reports/slot-plan-funnel?to=nope",
		"inverted range":  "/reports/slot-plan-funnel?from=2026-10-08T00:00:00Z&to=2026-10-07T00:00:00Z",
		"range too large": "/reports/slot-plan-funnel?from=2025-01-01T00:00:00Z&to=2026-10-08T00:00:00Z",
		"bad granularity": "/reports/slot-plan-funnel?granularity=hour",
	} {
		rec, body := getJSON(t, h, path)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d", name, rec.Code)
		}
		if _, ok := body["type"]; !ok {
			t.Fatalf("%s: not an RFC 7807 problem: %v", name, body)
		}
	}
}

func TestReportsFreshness(t *testing.T) {
	asOf := time.Date(2026, 10, 8, 11, 59, 55, 0, time.UTC)
	rec, body := getJSON(t, newReportsServer(&fakeReader{asOf: &asOf}), "/reports/freshness")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if body["as_of"] != "2026-10-08T11:59:55Z" || body["lag_seconds"] != float64(5) {
		t.Fatalf("freshness = %v", body)
	}

	// Nothing applied yet: both fields null.
	rec, body = getJSON(t, newReportsServer(&fakeReader{}), "/reports/freshness")
	if rec.Code != http.StatusOK || body["as_of"] != nil || body["lag_seconds"] != nil {
		t.Fatalf("empty freshness = %d %v", rec.Code, body)
	}
}

func TestReportsStoreErrorIs500(t *testing.T) {
	h := newReportsServer(&fakeReader{readErr: errors.New("connection refused")})
	for _, path := range []string{"/reports/slot-plan-funnel", "/reports/freshness"} {
		rec, body := getJSON(t, h, path)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("%s: status = %d", path, rec.Code)
		}
		if body["type"] != "https://errors.slotting-optimization.warehouse-systems.dev/report-store-error" {
			t.Fatalf("%s: problem type = %v", path, body["type"])
		}
		if _, leaked := body["connection refused"]; leaked {
			t.Fatalf("%s: the cause leaked into the response", path)
		}
	}
}

func TestReportsHealthz(t *testing.T) {
	rec, _ := getJSON(t, newReportsServer(&fakeReader{}), "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}
