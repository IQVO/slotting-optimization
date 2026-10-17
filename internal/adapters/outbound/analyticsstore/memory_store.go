package analyticsstore

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/claudioed/slotting-optimization/internal/analytics/report"
)

// Memory is the in-memory twin of the analytical store: the same Projection
// and the same Reader semantics (earliest milestone wins, dedupe on the
// CloudEvents id, dense day rows, percentile_cont percentiles), so the same
// contract test can run against it and against Postgres. It is NOT the
// production store: nothing in cmd/ ever wires it.
type Memory struct {
	mu      sync.Mutex
	applied map[string]time.Time // event id -> CloudEvents time
	plans   map[string]*memoryPlan
}

// memoryPlan mirrors one slot_plan_facts row.
type memoryPlan struct {
	site       string
	generated  *time.Time
	approved   *time.Time
	rejected   *time.Time
	superseded *time.Time
	supersedes string
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{applied: map[string]time.Time{}, plans: map[string]*memoryPlan{}}
}

var _ report.Projection = (*Memory)(nil)
var _ report.Reader = (*Memory)(nil)

// plan returns the row of id, creating it (like the SQL upsert) when absent.
func (m *Memory) plan(id, site string) *memoryPlan {
	if p, ok := m.plans[id]; ok {
		return p
	}
	p := &memoryPlan{site: site}
	m.plans[id] = p
	return p
}

func least(existing, candidate *time.Time) *time.Time {
	if candidate == nil {
		return existing // LEAST ignores NULLs: a milestone this event does not mark keeps the stored value
	}
	if existing == nil || candidate.Before(*existing) {
		return candidate
	}
	return existing
}

// Apply implements report.Projection with the same first-writer-wins
// semantics as the SQL store.
func (m *Memory) Apply(_ context.Context, e report.Event) (bool, error) {
	if err := e.Validate(); err != nil {
		return false, fmt.Errorf("%w: %w", report.ErrRejected, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, seen := m.applied[e.EventID]; seen {
		return false, nil
	}
	m.applied[e.EventID] = e.At.UTC()
	p := m.plan(e.PlanID, e.SiteID)
	ms := report.MilestonesOf(e)
	p.generated = least(p.generated, ms.Generated)
	p.approved = least(p.approved, ms.Approved)
	p.rejected = least(p.rejected, ms.Rejected)
	if e.Kind == report.KindSlotPlanApproved && e.SupersedesPlanID != "" {
		p.supersedes = e.SupersedesPlanID
		other := m.plan(e.SupersedesPlanID, e.SiteID)
		other.superseded = least(other.superseded, ms.Superseded)
	}
	return true, nil
}

// funnelFact is one plan's milestones, narrowed to the query's site.
type funnelFact struct {
	generated  *time.Time
	approved   *time.Time
	rejected   *time.Time
	superseded *time.Time
}

// funnelFacts copies the plans matching the site scope.
func (m *Memory) funnelFacts(siteID string) []funnelFact {
	facts := make([]funnelFact, 0, len(m.plans))
	for _, p := range m.plans {
		if siteID != "" && p.site != siteID {
			continue
		}
		facts = append(facts, funnelFact{generated: p.generated, approved: p.approved,
			rejected: p.rejected, superseded: p.superseded})
	}
	return facts
}

// countDay folds the facts into the single window w.
func countDay(facts []funnelFact, w report.DayWindow) report.FunnelDay {
	d := report.FunnelDay{Day: w.Day}
	var latencies []float64
	for _, f := range facts {
		if in(f.generated, w) {
			d.Generated++
		}
		if in(f.approved, w) {
			d.Approved++
			secs, ok := latencyOf(f)
			if ok {
				latencies = append(latencies, secs)
			}
		}
		if in(f.rejected, w) {
			d.Rejected++
		}
		if in(f.superseded, w) {
			d.Supersessions++
		}
	}
	sort.Float64s(latencies)
	d.ApprovalLatency = report.TimingOf(latencies)
	return d
}

// latencyOf is the generated-to-approved seconds of a plan the projection
// saw generated AND approved; a plan approved without a seen generation has
// none.
func latencyOf(f funnelFact) (float64, bool) {
	if f.generated == nil {
		return 0, false
	}
	secs := f.approved.Sub(*f.generated).Seconds()
	if secs < 0 {
		secs = 0
	}
	return secs, true
}

// FunnelDays implements report.Reader: one dense row per DayWindow, counted
// exactly like funnelDaysSQL.
func (m *Memory) FunnelDays(_ context.Context, q report.FunnelQuery) ([]report.FunnelDay, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	facts := m.funnelFacts(q.SiteID)
	var out []report.FunnelDay
	for _, w := range report.DayWindows(q.Range) {
		out = append(out, countDay(facts, w))
	}
	return out, nil
}

// in reports whether t falls in the half-open window [w.From, w.To); a nil
// time never does.
func in(t *time.Time, w report.DayWindow) bool {
	return t != nil && !t.Before(w.From) && t.Before(w.To)
}

// LastEventAt implements report.Reader: the newest applied CloudEvents time.
func (m *Memory) LastEventAt(_ context.Context) (*time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var newest *time.Time
	for _, at := range m.applied {
		at := at
		if newest == nil || at.After(*newest) {
			newest = &at
		}
	}
	if newest != nil {
		utc := newest.UTC()
		return &utc, nil
	}
	return nil, nil
}
