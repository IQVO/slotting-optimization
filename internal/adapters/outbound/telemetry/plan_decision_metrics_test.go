package telemetry_test

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/telemetry"
)

// ADR-0013: the Tier-2 slot_plans.decided counter is recorded by the
// outbound telemetry adapter behind ports.PlanDecisionMetrics, with a
// single counter carrying the outcome attribute rather than two separate
// instruments for approved/rejected.
func TestPlanDecisionMetrics_CountsByOutcome(t *testing.T) {
	reader := metric.NewManualReader()
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(metric.NewMeterProvider(metric.WithReader(reader)))
	t.Cleanup(func() { otel.SetMeterProvider(previous) })

	metrics, err := telemetry.NewPlanDecisionMetrics()
	if err != nil {
		t.Fatalf("NewPlanDecisionMetrics: %v", err)
	}

	ctx := context.Background()
	metrics.SlotPlanApproved(ctx)
	metrics.SlotPlanApproved(ctx)
	metrics.SlotPlanRejected(ctx)

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &collected); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	counts := map[string]int64{}
	for _, scope := range collected.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "slotting_optimization.slot_plans.decided" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("slotting_optimization.slot_plans.decided is a %T, want an int64 Sum", m.Data)
			}
			for _, point := range sum.DataPoints {
				outcome, ok := point.Attributes.Value("outcome")
				if !ok {
					t.Fatal("data point has no outcome attribute")
				}
				counts[outcome.AsString()] = point.Value
			}
		}
	}

	if counts["approved"] != 2 {
		t.Errorf("outcome=approved count = %d, want 2", counts["approved"])
	}
	if counts["rejected"] != 1 {
		t.Errorf("outcome=rejected count = %d, want 1", counts["rejected"])
	}
}

func TestPlanDecisionMetrics_RecordingNeverPanics(t *testing.T) {
	// Whatever the global provider happens to be -- the no-op one before
	// Setup runs, a real one after -- recording is fire-and-forget and
	// must never take the use case down with it.
	metrics, err := telemetry.NewPlanDecisionMetrics()
	if err != nil {
		t.Fatalf("NewPlanDecisionMetrics: %v", err)
	}

	metrics.SlotPlanApproved(context.Background())
	metrics.SlotPlanRejected(context.Background())
}
