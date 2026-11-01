// Package planning is the pure domain service that computes a slot plan:
// which SKUs deserve a forward pick slot and which slot each one gets. It
// does no I/O and never reads a clock; the result is a function of its
// Input alone (same input in any order, same output). The slot-ranking
// policy is a port so a travel-distance policy can replace the lexical one
// later without touching the planner.
package planning

import (
	"cmp"
	"errors"
	"slices"

	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// Errors returned by Plan for input that cannot be planned.
var (
	ErrNoRankingPolicy   = errors.New("planning: a slot ranking policy is required")
	ErrInvalidParameters = errors.New("planning: class thresholds must satisfy 0 < A <= B <= 100")
	ErrInvalidVelocity   = errors.New("planning: picks and units must not be negative and the sku must not be empty")
	ErrDuplicateVelocity = errors.New("planning: a sku appears twice in the velocities")
	ErrInvalidSlot       = errors.New("planning: a slot needs a code")
	ErrDuplicateSlot     = errors.New("planning: a slot code appears twice")
	ErrRankingContract   = errors.New("planning: the ranking policy must return each given slot exactly once")
)

// SlotRankingPolicy orders slots best first. Rank must return each given
// slot exactly once and be deterministic.
type SlotRankingPolicy interface {
	Rank(slots []Slot) []Slot
}

// LexicalRanking ranks slots by location code, ascending. Facility-layout
// events carry no travel distance, so this is the v1 stand-in (ADR 0002).
type LexicalRanking struct{}

// Rank returns a sorted copy of slots.
func (LexicalRanking) Rank(slots []Slot) []Slot {
	out := slices.Clone(slots)
	slices.SortFunc(out, func(a, b Slot) int { return cmp.Compare(a.Code, b.Code) })
	return out
}

// Planner implements policy abc-velocity-v1.
type Planner struct {
	ranking SlotRankingPolicy
}

// NewPlanner builds a planner around a slot ranking policy.
func NewPlanner(ranking SlotRankingPolicy) (*Planner, error) {
	if ranking == nil {
		return nil, ErrNoRankingPolicy
	}
	return &Planner{ranking: ranking}, nil
}

// Policy returns the policy name recorded on every plan this planner makes.
func (*Planner) Policy() slotplan.PolicyName { return slotplan.PolicyABCVelocityV1 }

// Plan computes the proposal.
//
// Candidates are the SKUs with picks > 0, ordered by picks desc, units desc,
// SKU asc. They are walked in that order; each takes the slot it held before
// if that slot is still eligible and free (stickiness, the churn limit),
// else the best free eligible slot. Rank order outranks stickiness: a better
// ranked SKU can take a slot a lower ranked one used to hold.
func (pl *Planner) Plan(in Input) (Result, error) {
	if err := validate(in); err != nil {
		return Result{}, err
	}
	ranked, err := pl.rank(in.Slots)
	if err != nil {
		return Result{}, err
	}
	cands := candidates(in.Velocities)
	classes := classify(cands, in.Params)
	p := &placer{
		profiles:  in.Profiles,
		previous:  in.Previous,
		gcd:       in.LineUnitsGCD,
		ranked:    ranked,
		caseFaces: slices.ContainsFunc(ranked, Slot.caseFace),
		taken:     make(map[slotplan.SlotCode]bool, len(ranked)),
	}
	for i, c := range cands {
		p.place(c, classes[i])
	}
	return Result{
		Assignments: p.assignments,
		Moves:       buildMoves(p.assignments, in.Previous),
		Unassigned:  p.unassigned,
	}, nil
}

func validate(in Input) error {
	pa, pb := in.Params.ClassAPercent, in.Params.ClassBPercent
	if pa < 1 || pb < pa || pb > 100 {
		return ErrInvalidParameters
	}
	seenSKU := make(map[slotplan.SKU]struct{}, len(in.Velocities))
	for _, v := range in.Velocities {
		if v.SKU == "" || v.Picks < 0 || v.Units < 0 {
			return ErrInvalidVelocity
		}
		if _, dup := seenSKU[v.SKU]; dup {
			return ErrDuplicateVelocity
		}
		seenSKU[v.SKU] = struct{}{}
	}
	seenSlot := make(map[slotplan.SlotCode]struct{}, len(in.Slots))
	for _, s := range in.Slots {
		if s.Code == "" {
			return ErrInvalidSlot
		}
		if _, dup := seenSlot[s.Code]; dup {
			return ErrDuplicateSlot
		}
		seenSlot[s.Code] = struct{}{}
	}
	return nil
}

// rank asks the policy for an order and verifies it is a permutation of the
// input; the returned slots are always the caller's own values.
func (pl *Planner) rank(slots []Slot) ([]Slot, error) {
	byCode := make(map[slotplan.SlotCode]Slot, len(slots))
	for _, s := range slots {
		byCode[s.Code] = s
	}
	got := pl.ranking.Rank(slices.Clone(slots))
	if len(got) != len(slots) {
		return nil, ErrRankingContract
	}
	out := make([]Slot, 0, len(got))
	for _, g := range got {
		s, ok := byCode[g.Code]
		if !ok {
			return nil, ErrRankingContract
		}
		delete(byCode, g.Code)
		out = append(out, s)
	}
	return out, nil
}

// candidates keeps SKUs with picks and orders them picks desc, units desc,
// SKU asc.
func candidates(vs []SkuVelocity) []SkuVelocity {
	out := make([]SkuVelocity, 0, len(vs))
	for _, v := range vs {
		if v.Picks > 0 {
			out = append(out, v)
		}
	}
	slices.SortFunc(out, func(a, b SkuVelocity) int {
		return cmp.Or(cmp.Compare(b.Picks, a.Picks), cmp.Compare(b.Units, a.Units), cmp.Compare(a.SKU, b.SKU))
	})
	return out
}

// classify gives each candidate (already in rank order) its ABC class from
// the share of picks held by the candidates ranked before it.
func classify(cands []SkuVelocity, p Parameters) []slotplan.ABCClass {
	var total int64
	for _, c := range cands {
		total += c.Picks
	}
	classes := make([]slotplan.ABCClass, len(cands))
	var before int64
	for i, c := range cands {
		classes[i] = classFor(before, total, p)
		before += c.Picks
	}
	return classes
}

func classFor(before, total int64, p Parameters) slotplan.ABCClass {
	if before*100 < int64(p.ClassAPercent)*total {
		return slotplan.ClassA
	}
	if before*100 < int64(p.ClassBPercent)*total {
		return slotplan.ClassB
	}
	return slotplan.ClassC
}
