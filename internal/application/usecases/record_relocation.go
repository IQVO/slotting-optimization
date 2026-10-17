package usecases

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// RelocationConsumer namespaces the claims of the StockRelocated consumer
// (ADR 0010) in ports.ProcessedEvents.
const RelocationConsumer = "slotting-relocation"

// OutcomeUnmatched: the relocation is no move of any plan this context
// knows (a manual, consolidation or replenishment move, an unknown plan or
// move). It is claimed and counted, never dead-lettered.
const OutcomeUnmatched Outcome = "unmatched"

// SlottingReason is the only StockRelocated reason that can execute a slot
// plan move: slot-plan RELOCATE tasks complete with it (ADR 0010). Every
// other reason (MANUAL, CONSOLIDATION, REPLENISHMENT, ...) is unmatched.
const SlottingReason = "SLOTTING"

// RelocationFact is a decoded inventory-storage StockRelocated payload.
// SourceRef is the caller's reference: fulfillment-execution's
// relocation_ref "<plan_id>:<move_seq>" on slot-plan moves, a task id, a
// ticket, or "".
type RelocationFact struct {
	RelocationID string
	SiteCode     string
	SKU          string
	FromBin      string
	ToBin        string
	Quantity     int64
	Reason       string
	SourceRef    string
	OccurredAt   time.Time
}

// RelocationResult reports what RecordRelocation did. MoveSeq is -1 unless
// a move was recorded; Plan is set whenever a plan was identified;
// Unmatched says why an unmatched relocation matched no move.
type RelocationResult struct {
	Outcome   Outcome
	Plan      slotplan.PlanID
	MoveSeq   int
	Unmatched slotplan.UnmatchedReason
}

// RecordRelocation records a relocation inventory-storage made against the
// move of a plan it executes (ADR 0010): matched on source_ref first (the
// move reference), else on the site's current Approved plan by SKU and
// bins. It never changes a plan and never gates anything.
type RecordRelocation struct {
	Intake
	Plans      ports.SlotPlanRepository
	Executions ports.MoveExecutionLedger
	// Metrics, when set, counts every handled relocation by outcome.
	Metrics ports.RelocationMetrics
}

// Handle runs the use case. A fact missing a required field is wrapped in
// ErrInvalidEvent before anything is claimed; a non-nil error otherwise is
// transient and the unit of work rolled back.
func (uc *RecordRelocation) Handle(ctx context.Context, eventID string, f RelocationFact) (RelocationResult, error) {
	rel, err := slotplan.NewRelocation(f.RelocationID, f.SiteCode, f.SKU, f.FromBin, f.ToBin, f.Quantity, f.OccurredAt)
	if err != nil {
		return RelocationResult{}, invalid(err)
	}
	res := RelocationResult{MoveSeq: -1}
	outcome, err := uc.apply(ctx, RelocationConsumer, eventID, func(ctx context.Context) (Outcome, error) {
		if strings.TrimSpace(f.Reason) != SlottingReason {
			res = RelocationResult{Outcome: OutcomeUnmatched, MoveSeq: -1, Unmatched: slotplan.UnmatchedNotSlotting}
			return res.Outcome, nil
		}
		r, err := uc.record(ctx, rel, strings.TrimSpace(f.SourceRef))
		res = r
		return r.Outcome, err
	})
	if err != nil {
		return RelocationResult{}, err
	}
	res.Outcome = outcome
	if uc.Metrics != nil {
		uc.Metrics.RelocationObserved(ctx, string(res.Outcome), string(res.Unmatched))
	}
	return res, nil
}

func (uc *RecordRelocation) record(ctx context.Context, rel slotplan.Relocation, sourceRef string) (RelocationResult, error) {
	plan, seq, reason, err := uc.match(ctx, rel, sourceRef)
	if err != nil {
		return RelocationResult{MoveSeq: -1}, err
	}
	res := RelocationResult{Outcome: OutcomeUnmatched, MoveSeq: -1, Unmatched: reason}
	if plan != nil {
		res.Plan = plan.ID()
	}
	if reason != "" {
		return res, nil
	}
	recorded, err := uc.Executions.Record(ctx, plan.ID(), slotplan.MoveExecution{
		Seq: seq, RelocationID: rel.ID, SKU: rel.SKU, FromBin: rel.FromBin, ToBin: rel.ToBin, Quantity: rel.Quantity, At: rel.At,
	})
	if err != nil {
		return RelocationResult{MoveSeq: -1}, fmt.Errorf("record move execution: %w", err)
	}
	if !recorded {
		res.Outcome = OutcomeDuplicate
		return res, nil
	}
	res.Outcome, res.MoveSeq = OutcomeApplied, seq
	return res, nil
}

// match finds the plan and the move rel executes: by the move reference
// when sourceRef is one, else by SKU and bins on the site's current
// Approved plan.
func (uc *RecordRelocation) match(ctx context.Context, rel slotplan.Relocation, sourceRef string) (*slotplan.SlotPlan, int, slotplan.UnmatchedReason, error) {
	if ref, ok := slotplan.ParseMoveRef(sourceRef); ok {
		plan, err := uc.Plans.Get(ctx, ref.Plan)
		if isNotFound(err) {
			return nil, -1, slotplan.UnmatchedUnknownPlan, nil
		}
		if err != nil {
			return nil, -1, "", fmt.Errorf("read slot plan: %w", err)
		}
		seq, reason := plan.MatchRef(ref, rel)
		return plan, seq, reason, nil
	}
	plan, err := uc.Plans.Current(ctx, rel.Site)
	if isNotFound(err) {
		return nil, -1, slotplan.UnmatchedNoCurrentPlan, nil
	}
	if err != nil {
		return nil, -1, "", fmt.Errorf("read approved slot plan: %w", err)
	}
	seq, reason := plan.MatchBins(rel)
	return plan, seq, reason, nil
}
