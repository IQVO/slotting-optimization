package planning

import (
	"testing"

	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// A case of 12: 24 L, 6.5 kg.
var caseOf12 = &CaseSize{VolumeMM3: 24_000_000, WeightG: 6500, UnitsPerPack: 12}

func caseProf() Profile { return Profile{Unit: unit, Case: caseOf12} }

func caseFace(code string, volM3, weightKg float64) Slot {
	return Slot{Code: slotplan.SlotCode(code), ZoneCode: "CASEPICK", TemperatureClass: Ambient,
		MaxWeightKg: weightKg, MaxVolumeM3: volM3, PickUnit: slotplan.PickCase}
}

func assignmentOf(t *testing.T, r Result, sku string) slotplan.Assignment {
	t.Helper()
	for _, a := range r.Assignments {
		if a.SKU == slotplan.SKU(sku) {
			return a
		}
	}
	t.Fatalf("%s has no assignment: %+v / %+v", sku, r.Assignments, r.Unassigned)
	return slotplan.Assignment{}
}

func casePlan(t *testing.T, gcd int64, profile Profile, slots ...Slot) Result {
	t.Helper()
	return mustPlan(t, Input{
		Velocities:   []SkuVelocity{vel("S", 10, 120)},
		Profiles:     map[slotplan.SKU]Profile{"S": profile},
		Slots:        slots,
		LineUnitsGCD: map[slotplan.SKU]int64{"S": gcd},
	})
}

func TestWholeCaseDemandGoesToACaseFaceWithCasesCapacity(t *testing.T) {
	// 0.1 m3 / 100 kg face: 4 cases by volume (4.16), 15 by weight.
	r := casePlan(t, 12, caseProf(), slot("FWD-01"), caseFace("CASE-01", 0.1, 100))
	a := assignmentOf(t, r, "S")
	if a.Slot != "CASE-01" || a.PickUnit != slotplan.PickCase || a.CasesCapacity != 4 {
		t.Fatalf("assignment = %+v", a)
	}
}

func TestCasesCapacityIsTheTighterOfVolumeAndWeight(t *testing.T) {
	byWeight := assignmentOf(t, casePlan(t, 24, caseProf(), caseFace("C", 1, 14)), "S") // 41 by volume, 2 by weight
	if byWeight.CasesCapacity != 2 {
		t.Fatalf("by weight: %+v", byWeight)
	}
	byVolume := assignmentOf(t, casePlan(t, 24, caseProf(), caseFace("C", 0.05, 1000)), "S") // 2 by volume, 153 by weight
	if byVolume.CasesCapacity != 2 {
		t.Fatalf("by volume: %+v", byVolume)
	}
}

func TestExactlyOneCaseFitsAFace(t *testing.T) {
	r := casePlan(t, 12, caseProf(), caseFace("C", 0.024, 6.5))
	if a := assignmentOf(t, r, "S"); a.CasesCapacity != 1 {
		t.Fatalf("a face exactly the size of a case holds one: %+v", a)
	}
	// a hair under: weight, then volume
	if reasonOf(casePlan(t, 12, caseProf(), caseFace("C", 1, 6.499)), "S") != slotplan.ReasonNoCapacityFit {
		t.Fatal("a case heavier than the face does not fit")
	}
	if reasonOf(casePlan(t, 12, caseProf(), caseFace("C", 0.0239, 100)), "S") != slotplan.ReasonNoCapacityFit {
		t.Fatal("a case bigger than the face does not fit")
	}
}

func TestCaseCapacityIgnoresTheEachCube(t *testing.T) {
	// The each cube (1 L) would fit 100 eaches; the case (24 L) fits 4.
	r := casePlan(t, 12, caseProf(), caseFace("C", 0.1, 1000))
	if a := assignmentOf(t, r, "S"); a.CasesCapacity != 4 {
		t.Fatalf("capacity must come from the case: %+v", a)
	}
}

func TestDemandThatIsNotWholeCasesStaysEachPick(t *testing.T) {
	r := casePlan(t, 6, caseProf(), slot("FWD-01"), caseFace("CASE-01", 0.1, 100))
	a := assignmentOf(t, r, "S")
	if a.Slot != "FWD-01" || a.PickUnit != slotplan.PickEach || a.CasesCapacity != 0 {
		t.Fatalf("assignment = %+v", a)
	}
}

func TestPickUnitBoundaryAtUnitsPerPack(t *testing.T) {
	for _, tc := range []struct {
		gcd  int64
		want slotplan.PickUnit
	}{
		{0, slotplan.PickEach}, {11, slotplan.PickEach}, {12, slotplan.PickCase},
		{13, slotplan.PickEach}, {24, slotplan.PickCase}, {36, slotplan.PickCase},
	} {
		r := casePlan(t, tc.gcd, caseProf(), slot("FWD-01"), caseFace("CASE-01", 0.1, 100))
		if got := assignmentOf(t, r, "S").PickUnit; got != tc.want {
			t.Errorf("gcd %d: pick unit %s, want %s", tc.gcd, got, tc.want)
		}
	}
}

func TestNoCaseFacesMeansEveryoneIsEachPickedAsBefore(t *testing.T) {
	r := casePlan(t, 12, caseProf(), slot("FWD-01"))
	a := assignmentOf(t, r, "S")
	if a.Slot != "FWD-01" || a.PickUnit != slotplan.PickEach || a.CasesCapacity != 0 {
		t.Fatalf("assignment = %+v", a)
	}
}

func TestEachSkuNeverTakesACaseFace(t *testing.T) {
	r := mustPlan(t, Input{
		Velocities: []SkuVelocity{vel("S", 10, 120)},
		Profiles:   map[slotplan.SKU]Profile{"S": prof()},
		Slots:      []Slot{caseFace("CASE-01", 1, 100)},
	})
	if reasonOf(r, "S") != slotplan.ReasonNoEligibleSlot {
		t.Fatalf("an each-picked SKU with only case faces left is unassigned: %+v", r)
	}
}

func TestCaseSkuNeverTakesAnEachFace(t *testing.T) {
	r := casePlan(t, 12, caseProf(), slot("FWD-01"), caseFace("CASE-01", 0.001, 1))
	if reasonOf(r, "S") != slotplan.ReasonNoCapacityFit {
		t.Fatalf("a case-picked SKU whose case fits no case face is unassigned: %+v", r)
	}
	r = mustPlan(t, Input{
		Velocities:   []SkuVelocity{vel("S", 10, 120), vel("T", 5, 60)},
		Profiles:     map[slotplan.SKU]Profile{"S": caseProf(), "T": caseProf()},
		Slots:        []Slot{slot("FWD-01"), caseFace("CASE-01", 1, 100)},
		LineUnitsGCD: map[slotplan.SKU]int64{"S": 12, "T": 12},
	})
	if slotOf(r, "S") != "CASE-01" || reasonOf(r, "T") != slotplan.ReasonNoEligibleSlot {
		t.Fatalf("one case face, two case SKUs: %+v / %+v", r.Assignments, r.Unassigned)
	}
}

func TestCasePickDoesNotNeedTheEachDimensions(t *testing.T) {
	r := casePlan(t, 12, Profile{Case: caseOf12}, caseFace("CASE-01", 1, 100))
	if a := assignmentOf(t, r, "S"); a.PickUnit != slotplan.PickCase {
		t.Fatalf("assignment = %+v", a)
	}
}

func TestSkuWithoutAnyUsableSizeHasNoPhysicalProfile(t *testing.T) {
	r := casePlan(t, 12, Profile{}, slot("FWD-01"), caseFace("CASE-01", 1, 100))
	if reasonOf(r, "S") != slotplan.ReasonNoPhysicalProfile {
		t.Fatalf("%+v", r)
	}
	r = mustPlan(t, Input{Velocities: []SkuVelocity{vel("S", 1, 1)}, Slots: []Slot{slot("FWD-01")}})
	if reasonOf(r, "S") != slotplan.ReasonNoPhysicalProfile {
		t.Fatalf("an unknown sku: %+v", r)
	}
}

func TestUnusableCaseSizeFallsBackToEachPick(t *testing.T) {
	for name, c := range map[string]*CaseSize{
		"nil":          nil,
		"zero volume":  {VolumeMM3: 0, WeightG: 1, UnitsPerPack: 12},
		"zero weight":  {VolumeMM3: 1, WeightG: 0, UnitsPerPack: 12},
		"zero units":   {VolumeMM3: 1, WeightG: 1, UnitsPerPack: 0},
		"no pack size": {},
	} {
		r := casePlan(t, 12, Profile{Unit: unit, Case: c}, slot("FWD-01"), caseFace("CASE-01", 1, 100))
		if a := assignmentOf(t, r, "S"); a.PickUnit != slotplan.PickEach || a.Slot != "FWD-01" {
			t.Errorf("%s: %+v", name, a)
		}
	}
}

func TestCaseFaceKeepsItsSkuAcrossPlans(t *testing.T) {
	r := mustPlan(t, Input{
		Velocities:   []SkuVelocity{vel("S", 10, 120)},
		Profiles:     map[slotplan.SKU]Profile{"S": caseProf()},
		Slots:        []Slot{caseFace("CASE-01", 1, 100), caseFace("CASE-02", 1, 100)},
		Previous:     map[slotplan.SKU]slotplan.SlotCode{"S": "CASE-02"},
		LineUnitsGCD: map[slotplan.SKU]int64{"S": 12},
	})
	if slotOf(r, "S") != "CASE-02" {
		t.Fatalf("stickiness holds on case faces: %+v", r.Assignments)
	}
	if _, moved := moveOf(r, "S"); moved {
		t.Fatal("a SKU that keeps its face has no move")
	}
}

func TestCaseFaceStillHonoursTemperatureAndHazmat(t *testing.T) {
	f := caseFace("CASE-01", 1, 100)
	f.TemperatureClass = Frozen
	if reasonOf(casePlan(t, 12, caseProf(), f), "S") != slotplan.ReasonNoEligibleSlot {
		t.Fatal("an ambient case SKU does not go to a frozen case face")
	}
	f.TemperatureClass, f.Hazmat = Ambient, true
	if reasonOf(casePlan(t, 12, caseProf(), f), "S") != slotplan.ReasonNoEligibleSlot {
		t.Fatal("a non-hazmat case SKU does not go to a hazmat case face")
	}
}

func TestGCDOf(t *testing.T) {
	for _, tc := range []struct {
		in   []int64
		want int64
	}{
		{nil, 0}, {[]int64{0}, 0}, {[]int64{-4, 0}, 0}, {[]int64{12}, 12}, {[]int64{12, 24, 36}, 12},
		{[]int64{12, 18}, 6}, {[]int64{7, 13}, 1}, {[]int64{0, 9, -3, 6}, 3},
	} {
		if got := GCDOf(tc.in); got != tc.want {
			t.Errorf("GCDOf(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
