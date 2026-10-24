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
	"math"
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

// TagHazmat is the handling tag that restricts a SKU to hazmat zones.
const TagHazmat = "Hazmat"

// TemperatureClass is a storage temperature requirement. The empty value
// means Ambient.
type TemperatureClass string

// Temperature classes.
const (
	Ambient TemperatureClass = "Ambient"
	Chilled TemperatureClass = "Chilled"
	Frozen  TemperatureClass = "Frozen"
)

func (t TemperatureClass) normalised() TemperatureClass {
	if t == "" {
		return Ambient
	}
	return t
}

// SkuVelocity is the demand of one SKU in the window: Picks counts ACTIVE
// demand lines, Units sums their units.
type SkuVelocity struct {
	SKU   slotplan.SKU
	Picks int64
	Units int64
}

// UnitSize is the effective size of ONE unit of a SKU.
type UnitSize struct {
	VolumeMM3 int64
	WeightG   int64
}

// CaseSize is the effective size of ONE CASE of a SKU: the cube and weight of
// the case as one handling unit, and how many eaches it holds.
type CaseSize struct {
	VolumeMM3    int64
	WeightG      int64
	UnitsPerPack int64
}

// Profile is what the planner needs to know about a SKU. Unit is nil while
// the SKU has no effective dimensions.
type Profile struct {
	HandlingTags     []string
	TemperatureClass TemperatureClass
	Unit             *UnitSize
	// Case is the CASE level in force with its effective size; nil when the
	// SKU has none (then it can only be EACH-picked).
	Case *CaseSize
}

func (p Profile) hazmat() bool {
	return slices.Contains(p.HandlingTags, TagHazmat)
}

func (p Profile) hasUnit() bool {
	return p.Unit != nil && p.Unit.VolumeMM3 > 0 && p.Unit.WeightG > 0
}

func (p Profile) hasCase() bool {
	c := p.Case
	return c != nil && c.VolumeMM3 > 0 && c.WeightG > 0 && c.UnitsPerPack > 0
}

// pickedInWholeCases reports whether the SKU is CASE-pick material: it has a
// usable CASE level and every positive demand line is a whole number of
// cases, i.e. unitsPerPack divides the greatest common divisor of the line
// quantities (ADR 0012). A SKU with no positive line (gcd 0) is not.
func (p Profile) pickedInWholeCases(lineUnitsGCD int64) bool {
	if !p.hasCase() || lineUnitsGCD < 1 {
		return false
	}
	return lineUnitsGCD%p.Case.UnitsPerPack == 0
}

// Slot is an active forward pick slot with the properties of its zone.
type Slot struct {
	Code             slotplan.SlotCode
	ZoneCode         string
	TemperatureClass TemperatureClass
	Hazmat           bool
	MaxWeightKg      float64
	MaxVolumeM3      float64
	// PickUnit is the unit the face is picked in: CASE for a case-pick face,
	// EACH (or the zero value) for an each-pick face.
	PickUnit slotplan.PickUnit
}

func (s Slot) caseFace() bool { return s.PickUnit == slotplan.PickCase }

// Parameters are the policy knobs: a SKU is class A while the picks of the
// SKUs ranked before it are below ClassAPercent of all picks, class B below
// ClassBPercent, else C.
type Parameters struct {
	ClassAPercent int
	ClassBPercent int
}

// DefaultParameters returns the v1 thresholds, 80 % and 95 %.
func DefaultParameters() Parameters {
	return Parameters{ClassAPercent: 80, ClassBPercent: 95}
}

// Input is everything a plan is computed from.
type Input struct {
	Velocities []SkuVelocity
	Profiles   map[slotplan.SKU]Profile
	Slots      []Slot
	Previous   map[slotplan.SKU]slotplan.SlotCode
	Params     Parameters
	// LineUnitsGCD is, per SKU, the greatest common divisor of its positive
	// demand-line quantities in the window (0 or absent: none). It decides
	// the pick unit together with the SKU's CASE level.
	LineUnitsGCD map[slotplan.SKU]int64
}

// Result is the proposal: assignments and unassigned SKUs in rank order,
// moves sorted by SKU.
type Result struct {
	Assignments []slotplan.Assignment
	Moves       []slotplan.Move
	Unassigned  []slotplan.Unassigned
}

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

// placer holds the mutable state of one planning run.
type placer struct {
	profiles map[slotplan.SKU]Profile
	previous map[slotplan.SKU]slotplan.SlotCode
	gcd      map[slotplan.SKU]int64
	ranked   []Slot
	// caseFaces: at least one ranked slot is a CASE-pick face. Without one
	// every SKU is EACH-picked and the plan is what it was before ADR 0012.
	caseFaces   bool
	taken       map[slotplan.SlotCode]bool
	assignments []slotplan.Assignment
	unassigned  []slotplan.Unassigned
}

func (p *placer) place(c SkuVelocity, class slotplan.ABCClass) {
	profile, known := p.profiles[c.SKU]
	unit := slotplan.PickEach
	if known && p.caseFaces && profile.pickedInWholeCases(p.gcd[c.SKU]) {
		unit = slotplan.PickCase
	}
	size, ok := handlingUnit(profile, unit)
	if !known || !ok {
		p.skip(c.SKU, slotplan.ReasonNoPhysicalProfile)
		return
	}
	compatible := filterSlots(p.ranked, func(s Slot) bool {
		return compatibleWith(profile, s) && s.caseFace() == (unit == slotplan.PickCase)
	})
	if len(compatible) == 0 {
		p.skip(c.SKU, slotplan.ReasonNoEligibleSlot)
		return
	}
	eligible := filterSlots(compatible, func(s Slot) bool { return holds(size, s) })
	if len(eligible) == 0 {
		p.skip(c.SKU, slotplan.ReasonNoCapacityFit)
		return
	}
	slot, ok := p.choose(c.SKU, eligible)
	if !ok {
		p.skip(c.SKU, slotplan.ReasonNoEligibleSlot)
		return
	}
	p.taken[slot.Code] = true
	a := slotplan.Assignment{
		SKU: c.SKU, Slot: slot.Code, Class: class, Picks: c.Picks, Units: c.Units, PickUnit: unit,
	}
	if unit == slotplan.PickCase {
		a.CasesCapacity = casesCapacity(size, slot)
	}
	p.assignments = append(p.assignments, a)
}

// handlingUnit is the size of what is picked: one each, or one whole case.
// false when the SKU has no usable size for that unit.
func handlingUnit(p Profile, unit slotplan.PickUnit) (*UnitSize, bool) {
	if unit == slotplan.PickCase {
		return &UnitSize{VolumeMM3: p.Case.VolumeMM3, WeightG: p.Case.WeightG}, true
	}
	return p.Unit, p.hasUnit()
}

// casesCapacity is how many whole units of size u the slot holds by volume
// and by weight, whichever is the tighter. The slot must hold at least one
// (holds), so the result is at least 1.
func casesCapacity(u *UnitSize, s Slot) int64 {
	byVolume := math.Floor(s.MaxVolumeM3 * 1e9 / float64(u.VolumeMM3))
	byWeight := math.Floor(s.MaxWeightKg * 1000 / float64(u.WeightG))
	return int64(math.Min(byVolume, byWeight))
}

// GCDOf is the greatest common divisor of the positive values; 0 when there
// is none.
func GCDOf(values []int64) int64 {
	var g int64
	for _, v := range values {
		if v < 1 {
			continue
		}
		g = gcd(g, v)
	}
	return g
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func (p *placer) skip(sku slotplan.SKU, reason slotplan.UnassignedReason) {
	p.unassigned = append(p.unassigned, slotplan.Unassigned{SKU: sku, Reason: reason})
}

// choose returns the previous slot if it is eligible and free, else the
// best free eligible slot.
func (p *placer) choose(sku slotplan.SKU, eligible []Slot) (Slot, bool) {
	prev := p.previous[sku]
	for _, s := range eligible {
		if s.Code == prev && !p.taken[s.Code] {
			return s, true
		}
	}
	for _, s := range eligible {
		if !p.taken[s.Code] {
			return s, true
		}
	}
	return Slot{}, false
}

func filterSlots(slots []Slot, keep func(Slot) bool) []Slot {
	var out []Slot
	for _, s := range slots {
		if keep(s) {
			out = append(out, s)
		}
	}
	return out
}

// compatibleWith: hazmat SKUs only in hazmat slots, others only in
// non-hazmat slots, and the temperature class must match (absent = Ambient).
func compatibleWith(p Profile, s Slot) bool {
	if p.hazmat() != s.Hazmat {
		return false
	}
	return p.TemperatureClass.normalised() == s.TemperatureClass.normalised()
}

// holds reports whether one unit fits the slot. A slot with no (or zero)
// capacity holds nothing because a unit's volume and weight are positive.
func holds(u *UnitSize, s Slot) bool {
	if float64(u.VolumeMM3) > s.MaxVolumeM3*1e9 {
		return false
	}
	return float64(u.WeightG) <= s.MaxWeightKg*1000
}

// buildMoves diffs the new assignments against the previous map.
func buildMoves(assignments []slotplan.Assignment, previous map[slotplan.SKU]slotplan.SlotCode) []slotplan.Move {
	var moves []slotplan.Move
	assigned := make(map[slotplan.SKU]struct{}, len(assignments))
	for _, a := range assignments {
		assigned[a.SKU] = struct{}{}
		prev := previous[a.SKU]
		if prev == "" {
			moves = append(moves, slotplan.Move{SKU: a.SKU, To: a.Slot, Kind: slotplan.MoveAssign})
			continue
		}
		if prev != a.Slot {
			moves = append(moves, slotplan.Move{SKU: a.SKU, From: prev, To: a.Slot, Kind: slotplan.MoveRelocate})
		}
	}
	for sku, prev := range previous {
		if _, ok := assigned[sku]; ok || prev == "" {
			continue
		}
		moves = append(moves, slotplan.Move{SKU: sku, From: prev, Kind: slotplan.MoveVacate})
	}
	slices.SortFunc(moves, func(a, b slotplan.Move) int { return cmp.Compare(a.SKU, b.SKU) })
	return moves
}
