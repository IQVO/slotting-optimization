package kafka

import (
	"testing"
	"time"
)

// TestSkipSamplerFirstThenOnePerInterval pins the skip sampler: the first
// call is emitted, calls inside the interval are suppressed and counted, and
// the first call after the interval carries the suppressed count.
func TestSkipSamplerFirstThenOnePerInterval(t *testing.T) {
	start := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	now := start
	s := &skipSampler{every: time.Minute, now: func() time.Time { return now }}

	if emit, suppressed := s.allow(); !emit || suppressed != 0 {
		t.Fatalf("first = %v, %v; want emitted with no suppressed count", emit, suppressed)
	}
	now = start.Add(10 * time.Second)
	if emit, suppressed := s.allow(); emit || suppressed != 0 {
		t.Fatalf("inside the interval = %v, %v; want suppressed", emit, suppressed)
	}
	now = start.Add(20 * time.Second)
	if emit, _ := s.allow(); emit {
		t.Fatal("a second call inside the interval must be suppressed")
	}
	now = start.Add(90 * time.Second)
	if emit, suppressed := s.allow(); !emit || suppressed != 2 {
		t.Fatalf("after the interval = %v, %v; want emitted with 2 suppressed", emit, suppressed)
	}
	now = start.Add(95 * time.Second)
	if emit, suppressed := s.allow(); emit || suppressed != 0 {
		t.Fatalf("the counter must reset after an emitted warning: %v, %v", emit, suppressed)
	}
}

// TestAnalyticsKindsCoverExactlyTheThreePublishedTypes: the kinds table keys
// are the full CloudEvents types of the three published events.
func TestAnalyticsKindsCoverExactlyTheThreePublishedTypes(t *testing.T) {
	want := map[string]bool{
		"com.warehouse.wms.slotting-optimization.slotplan.SlotPlanGenerated": true,
		"com.warehouse.wms.slotting-optimization.slotplan.SlotPlanApproved":  true,
		"com.warehouse.wms.slotting-optimization.slotplan.SlotPlanRejected":  true,
	}
	if len(analyticsKinds) != len(want) {
		t.Fatalf("kinds = %v, want exactly %v", analyticsKinds, want)
	}
	for full := range want {
		if _, ok := analyticsKinds[full]; !ok {
			t.Fatalf("%s missing from the kinds table", full)
		}
	}
}
