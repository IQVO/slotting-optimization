package telemetry_test

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/telemetry"
)

// ADR 0010: every relocation the StockRelocated consumer handles is counted
// on ONE counter by outcome; unmatched ones also carry why.
func TestRelocationMetrics_CountsByOutcomeAndReason(t *testing.T) {
	reader := metric.NewManualReader()
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(metric.NewMeterProvider(metric.WithReader(reader)))
	t.Cleanup(func() { otel.SetMeterProvider(previous) })

	m, err := telemetry.NewRelocationMetrics()
	if err != nil {
		t.Fatalf("NewRelocationMetrics: %v", err)
	}
	ctx := context.Background()
	m.RelocationObserved(ctx, "applied", "")
	m.RelocationObserved(ctx, "applied", "")
	m.RelocationObserved(ctx, "unmatched", "unknown-plan")
	m.RelocationObserved(ctx, "duplicate", "")

	counts := relocationCounts(t, reader)
	want := map[string]int64{"applied": 2, "unmatched/unknown-plan": 1, "duplicate": 1}
	for k, v := range want {
		if counts[k] != v {
			t.Errorf("%s = %d, want %d (all: %v)", k, counts[k], v, counts)
		}
	}
	if len(counts) != len(want) {
		t.Errorf("series = %v, want exactly %v", counts, want)
	}
}

// relocationCounts reads the relocations.observed series keyed
// "<outcome>" or "<outcome>/<reason>".
func relocationCounts(t *testing.T, reader *metric.ManualReader) map[string]int64 {
	t.Helper()
	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	counts := map[string]int64{}
	for _, scope := range collected.ScopeMetrics {
		for _, mt := range scope.Metrics {
			if mt.Name == "slotting_optimization.relocations.observed" {
				addRelocationPoints(t, counts, mt.Data)
			}
		}
	}
	return counts
}

func addRelocationPoints(t *testing.T, counts map[string]int64, data metricdata.Aggregation) {
	t.Helper()
	sum, ok := data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("relocations.observed is a %T, want an int64 Sum", data)
	}
	for _, p := range sum.DataPoints {
		outcome, _ := p.Attributes.Value("outcome")
		key := outcome.AsString()
		if reason, ok := p.Attributes.Value("reason"); ok {
			key += "/" + reason.AsString()
		}
		counts[key] = p.Value
	}
}
