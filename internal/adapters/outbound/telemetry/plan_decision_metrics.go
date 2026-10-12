package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/claudioed/slotting-optimization/internal/application/ports"
)

// meterName scopes this service's own instruments, keeping them distinct
// from the ones otelchi and the runtime collector register.
const meterName = "github.com/claudioed/slotting-optimization"

// planDecidedCounterName is the Tier-2 business metric (ADR-0013): how many
// of the slotting recommendation engine's Draft plans a human operator
// actually decides on, and which way. A rejected rate climbing against
// approved is the signal that the planner's proposed assignments are
// drifting from what operations will accept onto the floor.
const planDecidedCounterName = "slotting_optimization.slot_plans.decided"

// outcomeKey distinguishes the two ways a Draft plan's life ends on the
// single counter, rather than splitting it into two instruments.
var outcomeKey = attribute.Key("outcome")

const (
	outcomeApproved = "approved"
	outcomeRejected = "rejected"
)

// PlanDecisionMetrics implements ports.PlanDecisionMetrics against the
// global MeterProvider. Until telemetry.Setup installs a real provider, the
// global one is a no-op, so recording is cheap and safe in tests and local
// runs.
type PlanDecisionMetrics struct {
	counter metric.Int64Counter
}

var _ ports.PlanDecisionMetrics = (*PlanDecisionMetrics)(nil)

// NewPlanDecisionMetrics registers the slot_plans.decided counter. It only
// fails if the instrument name is invalid, which is a programming error,
// not a runtime condition -- callers that would rather run
// un-instrumented than not at all can ignore the error and pass a nil
// ports.PlanDecisionMetrics instead.
func NewPlanDecisionMetrics() (*PlanDecisionMetrics, error) {
	counter, err := otel.Meter(meterName).Int64Counter(
		planDecidedCounterName,
		metric.WithDescription("Draft slot plans decided by a human operator, by outcome (approved puts the plan into effect on the floor, rejected discards it)."),
		metric.WithUnit("{plan}"),
	)
	if err != nil {
		return nil, err
	}
	return &PlanDecisionMetrics{counter: counter}, nil
}

// SlotPlanApproved implements ports.PlanDecisionMetrics.
func (m *PlanDecisionMetrics) SlotPlanApproved(ctx context.Context) {
	m.record(ctx, outcomeApproved)
}

// SlotPlanRejected implements ports.PlanDecisionMetrics.
func (m *PlanDecisionMetrics) SlotPlanRejected(ctx context.Context) {
	m.record(ctx, outcomeRejected)
}

func (m *PlanDecisionMetrics) record(ctx context.Context, outcome string) {
	m.counter.Add(ctx, 1, metric.WithAttributes(outcomeKey.String(outcome)))
}
