package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/claudioed/slotting-optimization/internal/application/ports"
)

// relocationsObservedCounterName counts the relocations the StockRelocated
// consumer handled (ADR 0010), by outcome: applied (a slot-plan move was
// executed), duplicate (that relocation was already recorded) or unmatched
// (no move of a known plan; reason says why). A climbing unmatched rate with
// reason unknown-plan or no-matching-move says the floor and the plans
// disagree.
const relocationsObservedCounterName = "slotting_optimization.relocations.observed"

var reasonKey = attribute.Key("reason")

// RelocationMetrics implements ports.RelocationMetrics against the global
// MeterProvider (a no-op until telemetry.Setup installs a real one).
type RelocationMetrics struct {
	counter metric.Int64Counter
}

var _ ports.RelocationMetrics = (*RelocationMetrics)(nil)

// NewRelocationMetrics registers the relocations.observed counter. It only
// fails on an invalid instrument name, a programming error.
func NewRelocationMetrics() (*RelocationMetrics, error) {
	counter, err := otel.Meter(meterName).Int64Counter(
		relocationsObservedCounterName,
		metric.WithDescription("Relocations (inventory-storage StockRelocated) handled by the slot-plan execution consumer, by outcome (applied, duplicate, unmatched) and, when unmatched, reason."),
		metric.WithUnit("{relocation}"),
	)
	if err != nil {
		return nil, err
	}
	return &RelocationMetrics{counter: counter}, nil
}

// RelocationObserved implements ports.RelocationMetrics. reason is added
// only when non-empty.
func (m *RelocationMetrics) RelocationObserved(ctx context.Context, outcome, reason string) {
	attrs := []attribute.KeyValue{outcomeKey.String(outcome)}
	if reason != "" {
		attrs = append(attrs, reasonKey.String(reason))
	}
	m.counter.Add(ctx, 1, metric.WithAttributes(attrs...))
}
