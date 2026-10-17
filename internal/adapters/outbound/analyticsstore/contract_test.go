package analyticsstore

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/claudioed/slotting-optimization/internal/analytics/report"
)

// store is what the contract exercises: the writer and the reader over one
// model. The SAME contract runs against Memory (unit) and Postgres
// (integration), so the two stores cannot drift.
type store interface {
	report.Projection
	report.Reader
}

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func genEv(id, plan, site, when string) report.Event {
	return report.Event{Kind: report.KindSlotPlanGenerated, EventID: id, At: at(when), PlanID: plan, SiteID: site}
}

func apprEv(id, plan, site, when, supersedes string) report.Event {
	return report.Event{Kind: report.KindSlotPlanApproved, EventID: id, At: at(when), PlanID: plan, SiteID: site, SupersedesPlanID: supersedes}
}

func rejEv(id, plan, site, when string) report.Event {
	return report.Event{Kind: report.KindSlotPlanRejected, EventID: id, At: at(when), PlanID: plan, SiteID: site}
}

func mustApply(t *testing.T, s store, events ...report.Event) {
	t.Helper()
	for _, e := range events {
		if _, err := s.Apply(context.Background(), e); err != nil {
			t.Fatalf("apply %s %s: %v", e.Kind, e.EventID, err)
		}
	}
}

func funnel(t *testing.T, s store, from, to, site string) []report.FunnelDay {
	t.Helper()
	got, err := s.FunnelDays(context.Background(), report.FunnelQuery{
		Range: report.Range{From: at(from), To: at(to)}, SiteID: site, Granularity: report.GranularityDay,
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func approx(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return math.Abs(*a-*b) < 1e-6
}

func f(v float64) *float64 { return &v }

// runContract is the shared behavioural contract of the analytical store,
// split into three phases so each stays simple.
func runContract(t *testing.T, newStore func(t *testing.T) store) {
	t.Helper()
	runWriterContract(t, newStore)
	runMilestonesContract(t, newStore)
	runSupersessionContract(t, newStore)
	runDayRowContract(t, newStore)
	runSiteScopeContract(t, newStore)
	runPercentileContract(t, newStore)
	runFreshnessContract(t, newStore)
}

// runWriterContract pins the writer semantics: deterministic rejection of
// invalid events, idempotent replay, order-independent milestone folding.
func runWriterContract(t *testing.T, newStore func(t *testing.T) store) {
	t.Helper()
	t.Run("invalid events are rejected deterministically", func(t *testing.T) {
		s := newStore(t)
		bad := genEv("", "plan-1", "SITE-1", "2026-10-05T10:00:00Z")
		if _, err := s.Apply(context.Background(), bad); !errors.Is(err, report.ErrRejected) {
			t.Fatalf("err = %v, want ErrRejected", err)
		}
	})

	t.Run("a replayed event id is a no-op", func(t *testing.T) {
		s := newStore(t)
		e := genEv("e1", "plan-1", "SITE-1", "2026-10-05T10:00:00Z")
		if applied, err := s.Apply(context.Background(), e); err != nil || !applied {
			t.Fatalf("first apply = %v, %v", applied, err)
		}
		if applied, err := s.Apply(context.Background(), e); err != nil || applied {
			t.Fatalf("replay = %v, %v; want false, nil", applied, err)
		}
		days := funnel(t, s, "2026-10-05T00:00:00Z", "2026-10-06T00:00:00Z", "")
		if len(days) != 1 || days[0].Generated != 1 {
			t.Fatalf("days = %+v, want one generation counted once", days)
		}
	})
}

// runMilestonesContract pins that milestones converge whatever order the
// events arrive in.
func runMilestonesContract(t *testing.T, newStore func(t *testing.T) store) {
	t.Helper()
	t.Run("milestones converge whatever order the events arrive in", func(t *testing.T) {
		s := newStore(t)
		mustApply(t, s,
			apprEv("e3", "plan-1", "SITE-1", "2026-10-05T12:00:00Z", "plan-0"),
			rejEv("e4", "plan-2", "SITE-1", "2026-10-06T09:00:00Z"),
			genEv("e1", "plan-1", "SITE-1", "2026-10-05T10:00:00Z"),
			genEv("e2", "plan-2", "SITE-1", "2026-10-06T08:00:00Z"),
		)
		days := funnel(t, s, "2026-10-05T00:00:00Z", "2026-10-07T00:00:00Z", "")
		if len(days) != 2 {
			t.Fatalf("days = %d, want 2", len(days))
		}
		if days[0].Generated != 1 || days[0].Approved != 1 || days[0].Rejected != 0 || days[0].Supersessions != 1 {
			t.Fatalf("day 5 = %+v; the approval superseded plan-0 the same day", days[0])
		}
		if days[1].Generated != 1 || days[1].Approved != 0 || days[1].Rejected != 1 {
			t.Fatalf("day 6 = %+v", days[1])
		}
		// plan-1 was generated and approved on day 5 with a 2h latency; the
		// events were applied out of order and still agree.
		want := report.Timing{Count: 1, P50: f(7200), P95: f(7200)}
		if days[0].ApprovalLatency.Count != want.Count || !approx(days[0].ApprovalLatency.P50, want.P50) || !approx(days[0].ApprovalLatency.P95, want.P95) {
			t.Fatalf("day 5 latency = %+v, want %+v", days[0].ApprovalLatency, want)
		}
	})
}

// runSupersessionContract pins how supersessions are stamped.
func runSupersessionContract(t *testing.T, newStore func(t *testing.T) store) {
	t.Helper()
	t.Run("the superseded plan is stamped by the later approval", func(t *testing.T) {
		s := newStore(t)
		mustApply(t, s,
			genEv("e1", "plan-1", "SITE-1", "2026-10-05T10:00:00Z"),
			apprEv("e2", "plan-1", "SITE-1", "2026-10-05T11:00:00Z", ""),
			genEv("e3", "plan-2", "SITE-1", "2026-10-06T10:00:00Z"),
			apprEv("e4", "plan-2", "SITE-1", "2026-10-06T11:00:00Z", "plan-1"),
		)
		days := funnel(t, s, "2026-10-05T00:00:00Z", "2026-10-07T00:00:00Z", "")
		if days[0].Supersessions != 0 || days[1].Supersessions != 1 {
			t.Fatalf("supersessions = %d, %d; want 0, 1 (the FIRST plan is superseded by the second approval)", days[0].Supersessions, days[1].Supersessions)
		}
		if days[1].Approved != 1 {
			t.Fatalf("day 6 approved = %d, want 1", days[1].Approved)
		}
	})

	t.Run("a superseded plan never seen before is still stamped", func(t *testing.T) {
		// The projection starts mid-history: the first event it sees is an
		// approval naming a plan whose own events predate the stream.
		s := newStore(t)
		mustApply(t, s, apprEv("e1", "plan-9", "SITE-1", "2026-10-06T11:00:00Z", "plan-old"))
		days := funnel(t, s, "2026-10-06T00:00:00Z", "2026-10-07T00:00:00Z", "")
		if days[0].Supersessions != 1 {
			t.Fatalf("supersessions = %d, want 1", days[0].Supersessions)
		}
	})

}

// runDayRowContract pins that days are dense and windowed.
func runDayRowContract(t *testing.T, newStore func(t *testing.T) store) {
	t.Helper()
	t.Run("days are dense and windowed, not per event", func(t *testing.T) {
		s := newStore(t)
		mustApply(t, s, genEv("e1", "plan-1", "SITE-1", "2026-10-05T23:30:00Z"))
		// A range that starts and ends mid-day: the counts cover only the
		// part of the day inside it, and empty days still appear.
		days := funnel(t, s, "2026-10-05T23:00:00Z", "2026-10-08T01:00:00Z", "")
		if len(days) != 4 {
			t.Fatalf("days = %d, want 4 (three whole days plus the partials)", len(days))
		}
		if days[0].Generated != 1 || days[1].Generated != 0 || days[3].Generated != 0 {
			t.Fatalf("dense days = %+v", days)
		}
	})

}

// runSiteScopeContract pins the optional site scope.
func runSiteScopeContract(t *testing.T, newStore func(t *testing.T) store) {
	t.Helper()
	t.Run("the site scope narrows every counter", func(t *testing.T) {
		s := newStore(t)
		mustApply(t, s,
			genEv("e1", "plan-1", "SITE-1", "2026-10-05T10:00:00Z"),
			genEv("e2", "plan-2", "SITE-2", "2026-10-05T10:30:00Z"),
			apprEv("e3", "plan-2", "SITE-2", "2026-10-05T11:00:00Z", ""),
			apprEv("e4", "plan-3", "SITE-3", "2026-10-05T11:30:00Z", ""),
		)
		all := funnel(t, s, "2026-10-05T00:00:00Z", "2026-10-06T00:00:00Z", "")
		if all[0].Generated != 2 || all[0].Approved != 2 {
			t.Fatalf("unscoped day = %+v", all[0])
		}
		s1 := funnel(t, s, "2026-10-05T00:00:00Z", "2026-10-06T00:00:00Z", "SITE-1")
		if s1[0].Generated != 1 || s1[0].Approved != 0 || s1[0].ApprovalLatency.Count != 0 {
			t.Fatalf("SITE-1 day = %+v", s1[0])
		}
		s2 := funnel(t, s, "2026-10-05T00:00:00Z", "2026-10-06T00:00:00Z", "SITE-2")
		if s2[0].Generated != 1 || s2[0].Approved != 1 || !approx(s2[0].ApprovalLatency.P50, f(1800)) {
			t.Fatalf("SITE-2 day = %+v; the 30-minute latency must be counted", s2[0])
		}
		s3 := funnel(t, s, "2026-10-05T00:00:00Z", "2026-10-06T00:00:00Z", "SITE-3")
		if s3[0].Generated != 0 || s3[0].Approved != 1 || s3[0].ApprovalLatency.Count != 0 {
			t.Fatalf("SITE-3 day = %+v; an approval without a seen generation has no latency", s3[0])
		}
	})

}

// runPercentileContract pins the latency percentile definition.
func runPercentileContract(t *testing.T, newStore func(t *testing.T) store) {
	t.Helper()
	t.Run("approval latency percentiles are linear-interpolated", func(t *testing.T) {
		s := newStore(t)
		// Four plans generated and approved on one day: 1h, 2h, 3h, 4h.
		for i := 0; i < 4; i++ {
			plan := "plan-" + string(rune('1'+i))
			mustApply(t, s,
				genEv("g"+string(rune('1'+i)), plan, "SITE-1", "2026-10-05T08:00:00Z"),
				apprEv("a"+string(rune('1'+i)), plan, "SITE-1", at("2026-10-05T08:00:00Z").Add(time.Duration(i+1)*time.Hour).Format(time.RFC3339), ""),
			)
		}
		days := funnel(t, s, "2026-10-05T00:00:00Z", "2026-10-06T00:00:00Z", "")
		lat := days[0].ApprovalLatency
		if lat.Count != 4 || !approx(lat.P50, f(9000)) || !approx(lat.P95, f(13860)) {
			t.Fatalf("latency = %+v, want count 4, p50 9000 (2h30), p95 13860 (3h51) by linear interpolation", lat)
		}
	})
}

// runFreshnessContract pins the freshness basis: the newest applied event.
func runFreshnessContract(t *testing.T, newStore func(t *testing.T) store) {
	t.Helper()
	t.Run("freshness is the newest applied event time", func(t *testing.T) {
		s := newStore(t)
		if got, err := s.LastEventAt(context.Background()); err != nil || got != nil {
			t.Fatalf("empty LastEventAt = %v, %v; want nil", got, err)
		}
		mustApply(t, s,
			genEv("e1", "plan-1", "SITE-1", "2026-10-05T10:00:00Z"),
			rejEv("e2", "plan-2", "SITE-1", "2026-10-07T09:00:00Z"),
			genEv("e3", "plan-3", "SITE-1", "2026-10-06T09:00:00Z"),
		)
		got, err := s.LastEventAt(context.Background())
		if err != nil || got == nil || !got.Equal(at("2026-10-07T09:00:00Z")) {
			t.Fatalf("LastEventAt = %v, %v; want 2026-10-07T09:00:00Z", got, err)
		}
	})
}

func TestMemory_Contract(t *testing.T) {
	runContract(t, func(*testing.T) store { return NewMemory() })
}
