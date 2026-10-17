package http

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/riandyrn/otelchi"
	otelchimetric "github.com/riandyrn/otelchi/metric"

	"github.com/claudioed/slotting-optimization/internal/analytics/report"
)

// ReportsServiceName labels the reports binary for logs/spans/metrics,
// distinct from the api's DefaultServiceName.
const ReportsServiceName = "slotting-optimization-reports"

// ReportsServer is the inbound HTTP adapter of cmd/slotting-reports: the
// read-only slot-plan funnel report of ADR 0006. It depends only on the
// report.Reader port over the analytical database; it never touches the
// OLTP use cases, and it writes nothing.
type ReportsServer struct {
	Reader report.Reader
	// Now is the clock the default range ends at and the freshness lag is
	// measured against (time.Now when nil).
	Now func() time.Time
}

const dayLayout = "2006-01-02"

// The wire shapes (snake_case like the rest of this API); days are UTC
// calendar dates and the report structs never leak onto the wire.
type timingDTO struct {
	Count      int      `json:"count"`
	P50Seconds *float64 `json:"p50_seconds"`
	P95Seconds *float64 `json:"p95_seconds"`
}

type funnelDayDTO struct {
	Day             string    `json:"day"`
	PlansGenerated  int       `json:"plans_generated"`
	PlansApproved   int       `json:"plans_approved"`
	PlansRejected   int       `json:"plans_rejected"`
	ApprovalLatency timingDTO `json:"approval_latency"`
	Supersessions   int       `json:"supersessions"`
}

type funnelReportDTO struct {
	From        time.Time      `json:"from"`
	To          time.Time      `json:"to"`
	SiteID      string         `json:"site_id,omitempty"`
	Granularity string         `json:"granularity"`
	Days        []funnelDayDTO `json:"days"`
}

// NewReportsRouter builds the router of cmd/slotting-reports: /healthz,
// GET /reports/slot-plan-funnel and GET /reports/freshness. No auth
// middleware (fleet rule), no CORS: the service is cluster-internal.
func NewReportsRouter(s *ReportsServer) http.Handler {
	r := chi.NewRouter()
	r.Use(otelchi.Middleware(ReportsServiceName, otelchi.WithChiRoutes(r)))
	r.Use(otelchimetric.NewServerRequestDuration(otelchimetric.NewBaseConfig(ReportsServiceName)))
	r.Get("/healthz", handleHealthz)
	r.Get("/reports/slot-plan-funnel", s.handleSlotPlanFunnel)
	r.Get("/reports/freshness", s.handleFreshness)
	return r
}

func (s *ReportsServer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func timingToDTO(t report.Timing) timingDTO {
	return timingDTO{Count: t.Count, P50Seconds: t.P50, P95Seconds: t.P95}
}

// handleSlotPlanFunnel serves the per-day funnel over ?from=&to=&siteId=
// and ?granularity=day (the only granularity; default day).
func (s *ReportsServer) handleSlotPlanFunnel(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	now := s.now()
	rg, err := report.ParseRange(q.Get("from"), q.Get("to"), now)
	if err != nil {
		writeProblem(w, r, problem{http.StatusBadRequest, "invalid-report-range",
			"from and to must form a valid RFC 3339 range of at most 366 days"}, err.Error())
		return
	}
	granularity, err := report.ParseGranularity(q.Get("granularity"))
	if err != nil {
		writeProblem(w, r, problem{http.StatusBadRequest, "invalid-report-granularity",
			"granularity must be \"day\""}, err.Error())
		return
	}
	days, err := s.Reader.FunnelDays(r.Context(), report.FunnelQuery{Range: rg, SiteID: q.Get("siteId"), Granularity: granularity})
	if err != nil {
		writeReportStoreError(w, r, err)
		return
	}
	out := funnelReportDTO{
		From: rg.From, To: rg.To, SiteID: q.Get("siteId"),
		Granularity: string(granularity), Days: make([]funnelDayDTO, 0, len(days)),
	}
	for _, d := range days {
		out.Days = append(out.Days, funnelDayDTO{
			Day: d.Day.UTC().Format(dayLayout), PlansGenerated: d.Generated, PlansApproved: d.Approved,
			PlansRejected: d.Rejected, ApprovalLatency: timingToDTO(d.ApprovalLatency), Supersessions: d.Supersessions,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleFreshness serves how far the projection is behind (now - the newest
// applied event time). Both fields are null until the first event is applied.
func (s *ReportsServer) handleFreshness(w http.ResponseWriter, r *http.Request) {
	asOf, err := s.Reader.LastEventAt(r.Context())
	if err != nil {
		writeReportStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, report.ComputeFreshness(asOf, s.now()))
}

// writeReportStoreError is the 500 for a failed analytical query. The cause
// is logged, never echoed: it can carry SQL and connection details.
func writeReportStoreError(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "report query failed", "error", err, "path", r.URL.Path)
	writeProblem(w, r, problem{http.StatusInternalServerError, "report-store-error",
		"The report could not be served"}, "the analytical database query failed")
}
