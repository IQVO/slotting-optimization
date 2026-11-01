package planning

import (
	"cmp"
	"math"
	"slices"

	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

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
