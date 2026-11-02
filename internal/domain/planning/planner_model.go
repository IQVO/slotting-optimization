package planning

import (
	"slices"

	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
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
