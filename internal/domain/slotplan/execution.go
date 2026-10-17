package slotplan

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// Execution of an Approved plan (ADR 0010): wes-work-planning turns each move
// into RELOCATE work, fulfillment-execution runs it and inventory-storage
// records it as a relocation (StockRelocated). This file holds what the plan
// needs to recognise those relocations as its moves and to report how far
// its execution has got. It never changes the plan itself.

// ErrInvalidRelocation marks a relocation fact that misses a required field.
var ErrInvalidRelocation = errors.New("slotplan: relocation is missing a required field")

// MoveRef identifies one move of a plan: the plan id and the 0-based
// position of the move in the plan's moves, which is the order
// SlotPlanApproved publishes them in (sorted by SKU). On the wire it is
// "<plan_id>:<move_seq>" (fulfillment-execution's relocation_ref).
type MoveRef struct {
	Plan PlanID
	Seq  int
}

// String renders the wire form "<plan_id>:<move_seq>".
func (r MoveRef) String() string { return string(r.Plan) + ":" + strconv.Itoa(r.Seq) }

// ParseMoveRef reads "<plan_id>:<move_seq>" (split at the last ':'). ok is
// false when s is anything else (empty, a task id, a ticket number): the
// caller then falls back to matching on the bins.
func ParseMoveRef(s string) (MoveRef, bool) {
	// Without a ':' i is -1 and the plan part is empty, which NewPlanID refuses.
	i := strings.LastIndexByte(s, ':')
	plan, err := NewPlanID(s[:max(i, 0)])
	if err != nil {
		return MoveRef{}, false
	}
	seq, ok := parseSeq(s[i+1:])
	if !ok {
		return MoveRef{}, false
	}
	return MoveRef{Plan: plan, Seq: seq}, true
}

// parseSeq accepts decimal digits only (no sign, no space) that fit an int.
func parseSeq(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// Relocation is a stock move the ledger recorded (inventory-storage's
// StockRelocated), reduced to what a plan matches on.
type Relocation struct {
	ID       string
	Site     SiteID
	SKU      SKU
	FromBin  SlotCode
	ToBin    SlotCode
	Quantity int64
	At       time.Time
}

// NewRelocation validates the required fields of a relocation fact: every
// identifier present, a quantity of at least 1 and the instant it happened.
// Bins are not held to the slot-code rules: a bin that is no forward slot
// simply never matches one.
func NewRelocation(id, site, sku, fromBin, toBin string, quantity int64, at time.Time) (Relocation, error) {
	for _, f := range []struct{ name, value string }{
		{"relocation_id", id}, {"site_code", site}, {"sku", sku}, {"from_bin", fromBin}, {"to_bin", toBin},
	} {
		if strings.TrimSpace(f.value) == "" {
			return Relocation{}, errors.Join(ErrInvalidRelocation, errors.New(f.name+" is required"))
		}
	}
	if quantity < 1 {
		return Relocation{}, errors.Join(ErrInvalidRelocation, errors.New("quantity must be at least 1"))
	}
	if at.IsZero() {
		return Relocation{}, errors.Join(ErrInvalidRelocation, errors.New("occurred_at is required"))
	}
	return Relocation{
		ID: id, Site: SiteID(site), SKU: SKU(sku), FromBin: SlotCode(fromBin), ToBin: SlotCode(toBin),
		Quantity: quantity, At: at.UTC(),
	}, nil
}

// UnmatchedReason says why a relocation is not one of a plan's moves. The
// empty reason means it matched.
type UnmatchedReason string

// Unmatched reasons.
const (
	// UnmatchedUnknownPlan: the move reference names a plan this context
	// does not know (decided by the caller, which looks the plan up).
	UnmatchedUnknownPlan UnmatchedReason = "unknown-plan"
	// UnmatchedNoCurrentPlan: no move reference and the site has no
	// Approved plan to match the bins against (decided by the caller).
	UnmatchedNoCurrentPlan UnmatchedReason = "no-current-plan"
	// UnmatchedPlanNotApproved: the plan was never approved (Draft or
	// Rejected), so none of its moves was ever released.
	UnmatchedPlanNotApproved UnmatchedReason = "plan-not-approved"
	// UnmatchedSiteMismatch: the relocation happened in another site.
	UnmatchedSiteMismatch UnmatchedReason = "site-mismatch"
	// UnmatchedUnknownMove: the plan has no move at that position.
	UnmatchedUnknownMove UnmatchedReason = "unknown-move"
	// UnmatchedSKUMismatch: the move at that position is for another SKU.
	UnmatchedSKUMismatch UnmatchedReason = "sku-mismatch"
	// UnmatchedNoMatchingMove: no move of the plan has that SKU and bins.
	UnmatchedNoMatchingMove UnmatchedReason = "no-matching-move"
	// UnmatchedNotSlotting: the relocation's reason is not SLOTTING (a
	// manual, consolidation or replenishment move), so it executes no slot
	// plan move even when its SKU and bins happen to match one (decided by
	// the caller, before any plan is read).
	UnmatchedNotSlotting UnmatchedReason = "reason-not-slotting"
)

// executionGuard checks what every match needs: a plan whose moves were
// released (Approved, or Superseded: released tasks run to completion) and
// the relocation's site.
func (p *SlotPlan) executionGuard(r Relocation) UnmatchedReason {
	if p.state != StateApproved && p.state != StateSuperseded {
		return UnmatchedPlanNotApproved
	}
	if p.site != r.Site {
		return UnmatchedSiteMismatch
	}
	return ""
}

// MatchRef returns the position of the move ref names when r executes it,
// or -1 and why not.
func (p *SlotPlan) MatchRef(ref MoveRef, r Relocation) (int, UnmatchedReason) {
	if reason := p.executionGuard(r); reason != "" {
		return -1, reason
	}
	if ref.Seq < 0 || ref.Seq >= len(p.moves) {
		return -1, UnmatchedUnknownMove
	}
	if p.moves[ref.Seq].SKU != r.SKU {
		return -1, UnmatchedSKUMismatch
	}
	return ref.Seq, ""
}

// MatchBins returns the position of the move r executes, judged by the SKU
// and the bins its kind fixes (Relocate: both slots; Assign: the slot it
// fills, from any bin; Vacate: the slot it empties, to any bin), or -1 and
// why not. It is the fallback for relocations without a move reference.
func (p *SlotPlan) MatchBins(r Relocation) (int, UnmatchedReason) {
	if reason := p.executionGuard(r); reason != "" {
		return -1, reason
	}
	for i, m := range p.moves {
		if m.SKU == r.SKU && binsAgree(m, r) {
			return i, ""
		}
	}
	return -1, UnmatchedNoMatchingMove
}

func binsAgree(m Move, r Relocation) bool {
	if m.Kind == MoveAssign {
		return m.To == r.ToBin
	}
	if m.Kind == MoveRelocate {
		return m.From == r.FromBin && m.To == r.ToBin
	}
	return m.From == r.FromBin
}

// MoveExecution is one relocation recorded against a move of a plan.
type MoveExecution struct {
	Seq          int
	RelocationID string
	SKU          SKU
	FromBin      SlotCode
	ToBin        SlotCode
	Quantity     int64
	At           time.Time
}

// MoveProgress is the execution state of one move.
type MoveProgress struct {
	Seq              int
	SKU              SKU
	Kind             MoveKind
	Executed         bool
	ExecutedQuantity int64
	LastExecutedAt   time.Time
}

// ExecutionProgress is how far the execution of a plan has got: the moves
// executed (at least one relocation recorded) out of the plan's moves, the
// latest relocation instant, and the state of every move in move_seq order.
type ExecutionProgress struct {
	MovesTotal     int
	MovesExecuted  int
	LastExecutedAt time.Time
	Moves          []MoveProgress
}

// Progress derives the execution progress of the plan from the relocations
// recorded against it. An execution for a position the plan does not have
// is ignored.
func (p *SlotPlan) Progress(execs []MoveExecution) ExecutionProgress {
	out := ExecutionProgress{MovesTotal: len(p.moves), Moves: make([]MoveProgress, len(p.moves))}
	for i, m := range p.moves {
		out.Moves[i] = MoveProgress{Seq: i, SKU: m.SKU, Kind: m.Kind}
	}
	for _, e := range execs {
		if e.Seq < 0 || e.Seq >= len(p.moves) {
			continue
		}
		mp := &out.Moves[e.Seq]
		if !mp.Executed {
			out.MovesExecuted++
		}
		mp.Executed = true
		mp.ExecutedQuantity += e.Quantity
		at := e.At.UTC()
		if at.After(mp.LastExecutedAt) {
			mp.LastExecutedAt = at
		}
		if at.After(out.LastExecutedAt) {
			out.LastExecutedAt = at
		}
	}
	return out
}
