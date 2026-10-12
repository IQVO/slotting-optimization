package planning

import (
	"errors"
	"math/rand"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

var unit = &UnitSize{VolumeMM3: 1_000_000, WeightG: 500}

func prof() Profile { return Profile{Unit: unit} }

func slot(code string) Slot {
	return Slot{Code: slotplan.SlotCode(code), ZoneCode: "FWD", TemperatureClass: Ambient, MaxWeightKg: 50, MaxVolumeM3: 1}
}

func vel(sku string, picks, units int64) SkuVelocity {
	return SkuVelocity{SKU: slotplan.SKU(sku), Picks: picks, Units: units}
}

func planner(t *testing.T) *Planner {
	t.Helper()
	p, err := NewPlanner(LexicalRanking{})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func mustPlan(t *testing.T, in Input) Result {
	t.Helper()
	if in.Params == (Parameters{}) {
		in.Params = DefaultParameters()
	}
	r, err := planner(t).Plan(in)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return r
}

func profiles(skus ...string) map[slotplan.SKU]Profile {
	m := make(map[slotplan.SKU]Profile, len(skus))
	for _, s := range skus {
		m[slotplan.SKU(s)] = prof()
	}
	return m
}

func slotOf(r Result, sku string) slotplan.SlotCode {
	for _, a := range r.Assignments {
		if a.SKU == slotplan.SKU(sku) {
			return a.Slot
		}
	}
	return ""
}

func reasonOf(r Result, sku string) slotplan.UnassignedReason {
	for _, u := range r.Unassigned {
		if u.SKU == slotplan.SKU(sku) {
			return u.Reason
		}
	}
	return ""
}

func moveOf(r Result, sku string) (slotplan.Move, bool) {
	for _, m := range r.Moves {
		if m.SKU == slotplan.SKU(sku) {
			return m, true
		}
	}
	return slotplan.Move{}, false
}

func TestNewPlannerRequiresAPolicy(t *testing.T) {
	if p, err := NewPlanner(nil); !errors.Is(err, ErrNoRankingPolicy) || p != nil {
		t.Fatalf("got %v %v", p, err)
	}
	if got := planner(t).Policy(); got != "abc-velocity-v1" {
		t.Fatalf("policy = %q", got)
	}
}

func TestDefaultParameters(t *testing.T) {
	if p := DefaultParameters(); p.ClassAPercent != 80 || p.ClassBPercent != 95 {
		t.Fatalf("%+v", p)
	}
}

func TestCandidatesOrderAndFilter(t *testing.T) {
	got := candidates([]SkuVelocity{
		vel("D", 5, 10), vel("Z", 0, 99), vel("B", 5, 20), vel("A", 5, 20), vel("C", 9, 1),
	})
	var order []slotplan.SKU
	for _, c := range got {
		order = append(order, c.SKU)
	}
	want := []slotplan.SKU{"C", "A", "B", "D"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v (picks desc, units desc, sku asc, zero picks dropped)", order, want)
	}
}

func TestClassFor(t *testing.T) {
	p := DefaultParameters()
	tests := []struct {
		before int64
		want   slotplan.ABCClass
	}{
		{0, slotplan.ClassA},
		{79, slotplan.ClassA},
		{80, slotplan.ClassB},
		{94, slotplan.ClassB},
		{95, slotplan.ClassC},
		{100, slotplan.ClassC},
	}
	for _, tt := range tests {
		if got := classFor(tt.before, 100, p); got != tt.want {
			t.Errorf("classFor(%d, 100) = %s, want %s", tt.before, got, tt.want)
		}
	}
	if got := classFor(40, 100, Parameters{ClassAPercent: 40, ClassBPercent: 60}); got != slotplan.ClassB {
		t.Errorf("custom threshold: got %s", got)
	}
	if got := classFor(39, 100, Parameters{ClassAPercent: 40, ClassBPercent: 60}); got != slotplan.ClassA {
		t.Errorf("custom threshold: got %s", got)
	}
}

func TestClassifyUsesSharesOfHigherRankedSkus(t *testing.T) {
	cands := []SkuVelocity{vel("S1", 50, 0), vel("S2", 30, 0), vel("S3", 10, 0), vel("S4", 5, 0), vel("S5", 5, 0)}
	got := classify(cands, DefaultParameters())
	want := []slotplan.ABCClass{slotplan.ClassA, slotplan.ClassA, slotplan.ClassB, slotplan.ClassB, slotplan.ClassC}
	if !slices.Equal(got, want) {
		t.Fatalf("classes = %v, want %v", got, want)
	}
	if got := classify([]SkuVelocity{vel("only", 7, 0)}, DefaultParameters()); got[0] != slotplan.ClassA {
		t.Fatalf("a single SKU is class A, got %v", got)
	}
	if got := classify(nil, DefaultParameters()); len(got) != 0 {
		t.Fatalf("no candidates, no classes: %v", got)
	}
}

func TestPlanAssignsByRankToBestSlots(t *testing.T) {
	r := mustPlan(t, Input{
		Velocities: []SkuVelocity{vel("LOW", 1, 1), vel("TOP", 60, 120), vel("MID", 30, 30), vel("IDLE", 0, 0)},
		Profiles:   profiles("LOW", "TOP", "MID", "IDLE"),
		Slots:      []Slot{slot("FWD-03"), slot("FWD-01"), slot("FWD-02")},
	})
	if slotOf(r, "TOP") != "FWD-01" || slotOf(r, "MID") != "FWD-02" || slotOf(r, "LOW") != "FWD-03" {
		t.Fatalf("assignments: %+v", r.Assignments)
	}
	if slotOf(r, "IDLE") != "" || reasonOf(r, "IDLE") != "" {
		t.Fatal("a SKU without picks is not a candidate: neither assigned nor unassigned")
	}
	want := []slotplan.Assignment{
		{SKU: "TOP", Slot: "FWD-01", Class: slotplan.ClassA, Picks: 60, Units: 120},
		{SKU: "MID", Slot: "FWD-02", Class: slotplan.ClassA, Picks: 30, Units: 30},
		{SKU: "LOW", Slot: "FWD-03", Class: slotplan.ClassC, Picks: 1, Units: 1},
	}
	if !reflect.DeepEqual(r.Assignments, want) {
		t.Fatalf("assignments = %+v\nwant %+v", r.Assignments, want)
	}
	if len(r.Unassigned) != 0 || len(r.Moves) != 3 {
		t.Fatalf("unassigned %v moves %v", r.Unassigned, r.Moves)
	}
	for _, m := range r.Moves {
		if m.Kind != slotplan.MoveAssign || m.From != "" || m.To == "" {
			t.Fatalf("first plan is all Assign moves: %+v", m)
		}
	}
}

func TestPlanMoreCandidatesThanSlots(t *testing.T) {
	r := mustPlan(t, Input{
		Velocities: []SkuVelocity{vel("A", 30, 0), vel("B", 20, 0), vel("C", 10, 0)},
		Profiles:   profiles("A", "B", "C"),
		Slots:      []Slot{slot("FWD-01"), slot("FWD-02")},
	})
	if slotOf(r, "A") == "" || slotOf(r, "B") == "" || reasonOf(r, "C") != slotplan.ReasonNoEligibleSlot {
		t.Fatalf("the lowest ranked SKU is left out: %+v %+v", r.Assignments, r.Unassigned)
	}
}

func TestPlanNoSlotsAtAll(t *testing.T) {
	r := mustPlan(t, Input{Velocities: []SkuVelocity{vel("A", 3, 3)}, Profiles: profiles("A")})
	if len(r.Assignments) != 0 || reasonOf(r, "A") != slotplan.ReasonNoEligibleSlot {
		t.Fatalf("%+v", r)
	}
}

func TestPlanPhysicalProfileRules(t *testing.T) {
	tests := []struct {
		name string
		prof map[slotplan.SKU]Profile
	}{
		{"no entry", nil},
		{"unit absent", map[slotplan.SKU]Profile{"A": {}}},
		{"zero volume", map[slotplan.SKU]Profile{"A": {Unit: &UnitSize{VolumeMM3: 0, WeightG: 10}}}},
		{"zero weight", map[slotplan.SKU]Profile{"A": {Unit: &UnitSize{VolumeMM3: 10, WeightG: 0}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := mustPlan(t, Input{Velocities: []SkuVelocity{vel("A", 3, 3)}, Profiles: tt.prof, Slots: []Slot{slot("FWD-01")}})
			if reasonOf(r, "A") != slotplan.ReasonNoPhysicalProfile || len(r.Assignments) != 0 {
				t.Fatalf("%+v", r)
			}
		})
	}
	smallest := map[slotplan.SKU]Profile{"A": {Unit: &UnitSize{VolumeMM3: 1, WeightG: 1}}}
	r := mustPlan(t, Input{Velocities: []SkuVelocity{vel("A", 3, 3)}, Profiles: smallest, Slots: []Slot{slot("FWD-01")}})
	if slotOf(r, "A") != "FWD-01" {
		t.Fatalf("a 1 mm3 / 1 g unit is a valid profile: %+v", r)
	}
}

func TestPlanHazmatAndTemperatureSegregation(t *testing.T) {
	hazSlot := slot("FWD-01")
	hazSlot.Hazmat = true
	chilled := slot("FWD-02")
	chilled.TemperatureClass = Chilled
	plain := slot("FWD-03")
	emptyTemp := slot("FWD-04") // a zone that did not say: Ambient
	emptyTemp.TemperatureClass = ""

	tests := []struct {
		name    string
		profile Profile
		slots   []Slot
		want    slotplan.SlotCode
		reason  slotplan.UnassignedReason
	}{
		{"hazmat sku goes to the hazmat slot only", Profile{Unit: unit, HandlingTags: []string{"Fragile", TagHazmat}}, []Slot{plain, hazSlot}, "FWD-01", ""},
		{"hazmat sku with no hazmat slot", Profile{Unit: unit, HandlingTags: []string{TagHazmat}}, []Slot{plain}, "", slotplan.ReasonNoEligibleSlot},
		{"plain sku never goes to a hazmat slot", Profile{Unit: unit, HandlingTags: []string{"Fragile"}}, []Slot{hazSlot, plain}, "FWD-03", ""},
		{"plain sku with only hazmat slots", prof(), []Slot{hazSlot}, "", slotplan.ReasonNoEligibleSlot},
		{"chilled sku goes to the chilled slot", Profile{Unit: unit, TemperatureClass: Chilled}, []Slot{plain, chilled}, "FWD-02", ""},
		{"chilled sku with only ambient slots", Profile{Unit: unit, TemperatureClass: Chilled}, []Slot{plain}, "", slotplan.ReasonNoEligibleSlot},
		{"ambient sku never goes to a chilled slot", prof(), []Slot{chilled, plain}, "FWD-03", ""},
		{"absent temperature on the sku is ambient", prof(), []Slot{chilled, plain}, "FWD-03", ""},
		{"explicit ambient sku in an unspecified zone", Profile{Unit: unit, TemperatureClass: Ambient}, []Slot{emptyTemp}, "FWD-04", ""},
		{"absent sku temperature in an unspecified zone", prof(), []Slot{emptyTemp}, "FWD-04", ""},
		{"frozen sku in an unspecified zone", Profile{Unit: unit, TemperatureClass: Frozen}, []Slot{emptyTemp}, "", slotplan.ReasonNoEligibleSlot},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := mustPlan(t, Input{
				Velocities: []SkuVelocity{vel("A", 3, 3)},
				Profiles:   map[slotplan.SKU]Profile{"A": tt.profile},
				Slots:      tt.slots,
			})
			if slotOf(r, "A") != tt.want || reasonOf(r, "A") != tt.reason {
				t.Fatalf("slot %q reason %q, want %q %q", slotOf(r, "A"), reasonOf(r, "A"), tt.want, tt.reason)
			}
		})
	}
}

func TestPlanCapacityFit(t *testing.T) {
	exact := slot("FWD-01")
	exact.MaxVolumeM3, exact.MaxWeightKg = 0.001, 0.5 // exactly 1_000_000 mm3 and 500 g
	smallVolume := slot("FWD-02")
	smallVolume.MaxVolumeM3 = 0.000999
	smallWeight := slot("FWD-03")
	smallWeight.MaxWeightKg = 0.499
	noCapacity := slot("FWD-04")
	noCapacity.MaxVolumeM3, noCapacity.MaxWeightKg = 0, 0
	noVolume := slot("FWD-05")
	noVolume.MaxVolumeM3 = 0
	noWeight := slot("FWD-06")
	noWeight.MaxWeightKg = 0

	r := mustPlan(t, Input{Velocities: []SkuVelocity{vel("A", 3, 3)}, Profiles: profiles("A"), Slots: []Slot{exact}})
	if slotOf(r, "A") != "FWD-01" {
		t.Fatalf("a unit exactly as big as the slot fits: %+v", r)
	}
	for _, s := range []Slot{smallVolume, smallWeight, noCapacity, noVolume, noWeight} {
		r := mustPlan(t, Input{Velocities: []SkuVelocity{vel("A", 3, 3)}, Profiles: profiles("A"), Slots: []Slot{s}})
		if reasonOf(r, "A") != slotplan.ReasonNoCapacityFit || len(r.Assignments) != 0 {
			t.Errorf("slot %s must not hold the unit: %+v", s.Code, r)
		}
	}
	// A big slot elsewhere is not enough when it is incompatible: still NoCapacityFit
	// only when a compatible slot exists, NoEligibleSlot otherwise.
	haz := slot("FWD-07")
	haz.Hazmat = true
	r = mustPlan(t, Input{Velocities: []SkuVelocity{vel("A", 3, 3)}, Profiles: profiles("A"), Slots: []Slot{smallVolume, haz}})
	if reasonOf(r, "A") != slotplan.ReasonNoCapacityFit {
		t.Fatalf("compatible slot exists but is too small: %+v", r)
	}
	// Both dimensions are checked independently on a large unit.
	big := map[slotplan.SKU]Profile{"A": {Unit: &UnitSize{VolumeMM3: 2_000_000_000, WeightG: 12_500}}}
	roomy := slot("FWD-08")
	roomy.MaxVolumeM3, roomy.MaxWeightKg = 2, 12.5
	r = mustPlan(t, Input{Velocities: []SkuVelocity{vel("A", 3, 3)}, Profiles: big, Slots: []Slot{roomy}})
	if slotOf(r, "A") != "FWD-08" {
		t.Fatalf("2 m3 / 12.5 kg unit fits a 2 m3 / 12.5 kg slot: %+v", r)
	}
	roomy.MaxVolumeM3 = 1.999999999
	r = mustPlan(t, Input{Velocities: []SkuVelocity{vel("A", 3, 3)}, Profiles: big, Slots: []Slot{roomy}})
	if reasonOf(r, "A") != slotplan.ReasonNoCapacityFit {
		t.Fatalf("one mm3 too big: %+v", r)
	}
}

func TestPlanSkipsSlotsTooSmallForTheSku(t *testing.T) {
	tiny := slot("FWD-01")
	tiny.MaxVolumeM3 = 0.0000001
	r := mustPlan(t, Input{
		Velocities: []SkuVelocity{vel("A", 3, 3)},
		Profiles:   profiles("A"),
		Slots:      []Slot{tiny, slot("FWD-02")},
	})
	if slotOf(r, "A") != "FWD-02" {
		t.Fatalf("the first slot that fits wins, not the first slot: %+v", r)
	}
}

func TestStickinessKeepsTheSlotEvenWhenNotBest(t *testing.T) {
	r := mustPlan(t, Input{
		Velocities: []SkuVelocity{vel("A", 10, 10)},
		Profiles:   profiles("A"),
		Slots:      []Slot{slot("FWD-01"), slot("FWD-02"), slot("FWD-03")},
		Previous:   map[slotplan.SKU]slotplan.SlotCode{"A": "FWD-03"},
	})
	if slotOf(r, "A") != "FWD-03" || len(r.Moves) != 0 {
		t.Fatalf("a SKU keeps its eligible previous slot and no move is made: %+v", r)
	}
}

func TestStickinessGivesWayWhenThePreviousSlotIsNoLongerEligible(t *testing.T) {
	tiny := slot("FWD-03")
	tiny.MaxVolumeM3 = 0.0000001
	haz := slot("FWD-04")
	haz.Hazmat = true
	for name, prevSlots := range map[string][]Slot{
		"too small":     {slot("FWD-01"), slot("FWD-02"), tiny},
		"now hazmat":    {slot("FWD-01"), slot("FWD-02"), haz},
		"decommissined": {slot("FWD-01"), slot("FWD-02")},
	} {
		prev := slotplan.SlotCode("FWD-03")
		if name == "now hazmat" {
			prev = "FWD-04"
		}
		r := mustPlan(t, Input{
			Velocities: []SkuVelocity{vel("A", 10, 10)},
			Profiles:   profiles("A"),
			Slots:      prevSlots,
			Previous:   map[slotplan.SKU]slotplan.SlotCode{"A": prev},
		})
		m, ok := moveOf(r, "A")
		if slotOf(r, "A") != "FWD-01" || !ok || m.Kind != slotplan.MoveRelocate || m.From != prev || m.To != "FWD-01" {
			t.Errorf("%s: want Relocate %s -> FWD-01, got %+v / %+v", name, prev, r.Assignments, r.Moves)
		}
	}
}

func TestStickinessLosesToARankedSkuThatAlreadyTookTheSlot(t *testing.T) {
	// TOP has no previous slot and takes the best one, FWD-01, which LOW held.
	r := mustPlan(t, Input{
		Velocities: []SkuVelocity{vel("TOP", 50, 50), vel("LOW", 5, 5)},
		Profiles:   profiles("TOP", "LOW"),
		Slots:      []Slot{slot("FWD-01"), slot("FWD-02")},
		Previous:   map[slotplan.SKU]slotplan.SlotCode{"LOW": "FWD-01"},
	})
	if slotOf(r, "TOP") != "FWD-01" || slotOf(r, "LOW") != "FWD-02" {
		t.Fatalf("%+v", r.Assignments)
	}
	m, _ := moveOf(r, "LOW")
	if m.Kind != slotplan.MoveRelocate || m.From != "FWD-01" || m.To != "FWD-02" {
		t.Fatalf("LOW is displaced: %+v", m)
	}
	a, _ := moveOf(r, "TOP")
	if a.Kind != slotplan.MoveAssign || a.To != "FWD-01" {
		t.Fatalf("TOP is newly assigned: %+v", a)
	}
}

func TestTwoSkusClaimingTheSamePreviousSlot(t *testing.T) {
	r := mustPlan(t, Input{
		Velocities: []SkuVelocity{vel("HI", 9, 9), vel("LO", 1, 1)},
		Profiles:   profiles("HI", "LO"),
		Slots:      []Slot{slot("FWD-01"), slot("FWD-02")},
		Previous:   map[slotplan.SKU]slotplan.SlotCode{"HI": "FWD-02", "LO": "FWD-02"},
	})
	if slotOf(r, "HI") != "FWD-02" || slotOf(r, "LO") != "FWD-01" {
		t.Fatalf("the higher ranked SKU keeps the contested slot: %+v", r.Assignments)
	}
}

func movesScenario(t *testing.T) Result {
	t.Helper()
	return mustPlan(t, Input{
		Velocities: []SkuVelocity{vel("KEEP", 40, 1), vel("NEW", 30, 1), vel("GOTO", 20, 1), vel("NOPROFILE", 10, 1), vel("QUIET", 0, 0)},
		Profiles:   profiles("KEEP", "NEW", "GOTO", "QUIET"),
		Slots:      []Slot{slot("FWD-01"), slot("FWD-02"), slot("FWD-03")},
		Previous: map[slotplan.SKU]slotplan.SlotCode{
			"KEEP": "FWD-01", "GOTO": "FWD-09", "QUIET": "FWD-02", "GONE": "FWD-07", "NOPROFILE": "FWD-05", "BLANK": "",
		},
	})
}

func TestMovesAssignAndRelocate(t *testing.T) {
	r := movesScenario(t)
	if _, moved := moveOf(r, "KEEP"); moved {
		t.Error("KEEP stays: no move")
	}
	if m, _ := moveOf(r, "NEW"); m.Kind != slotplan.MoveAssign || m.From != "" || m.To != "FWD-02" {
		t.Errorf("NEW: %+v", m)
	}
	if m, _ := moveOf(r, "GOTO"); m.Kind != slotplan.MoveRelocate || m.From != "FWD-09" || m.To != "FWD-03" {
		t.Errorf("GOTO: %+v", m)
	}
	if reasonOf(r, "NOPROFILE") != slotplan.ReasonNoPhysicalProfile {
		t.Errorf("unassigned: %+v", r.Unassigned)
	}
}

func TestMovesVacateAndOrdering(t *testing.T) {
	r := movesScenario(t)
	for sku, from := range map[string]slotplan.SlotCode{"QUIET": "FWD-02", "GONE": "FWD-07", "NOPROFILE": "FWD-05"} {
		if m, _ := moveOf(r, sku); m.Kind != slotplan.MoveVacate || m.From != from || m.To != "" {
			t.Errorf("%s: want Vacate from %s, got %+v", sku, from, m)
		}
	}
	if _, moved := moveOf(r, "BLANK"); moved {
		t.Error("a blank previous slot means no previous assignment")
	}
	skus := make([]slotplan.SKU, 0, len(r.Moves))
	for _, m := range r.Moves {
		skus = append(skus, m.SKU)
	}
	if !slices.IsSorted(skus) {
		t.Errorf("moves must be sorted by SKU: %v", skus)
	}
	if len(r.Moves) != 5 {
		t.Errorf("want 5 moves, got %d: %+v", len(r.Moves), r.Moves)
	}
}

func TestResultFeedsSlotPlanGenerate(t *testing.T) {
	in := Input{
		Velocities: []SkuVelocity{vel("A", 40, 1), vel("B", 30, 1), vel("C", 20, 1), vel("D", 10, 1)},
		Profiles:   profiles("A", "B", "C"),
		Slots:      []Slot{slot("FWD-01"), slot("FWD-02")},
		Previous:   map[slotplan.SKU]slotplan.SlotCode{"B": "FWD-01", "Z": "FWD-02"},
	}
	r := mustPlan(t, in)
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	_, _, err := slotplan.Generate(slotplan.NewPlan{
		ID: "plan-1", Site: "SITE-1", Window: slotplan.Window{From: now.AddDate(0, 0, -28), To: now},
		Policy: planner(t).Policy(), Assignments: r.Assignments, Moves: r.Moves, Unassigned: r.Unassigned,
	}, now)
	if err != nil {
		t.Fatalf("the planner's result must always be a valid plan: %v", err)
	}
}

func TestPlanIsDeterministicUnderShuffledInput(t *testing.T) {
	haz := slot("FWD-05")
	haz.Hazmat = true
	slots := []Slot{slot("FWD-01"), slot("FWD-02"), slot("FWD-03"), slot("FWD-04"), haz}
	vels := []SkuVelocity{
		vel("S1", 50, 10), vel("S2", 50, 10), vel("S3", 50, 12), vel("S4", 20, 1), vel("S5", 20, 1),
		vel("S6", 3, 3), vel("S7", 1, 1), vel("S8", 0, 0), vel("S9", 8, 8),
	}
	profs := profiles("S1", "S2", "S3", "S4", "S5", "S6", "S7", "S9")
	profs["S9"] = Profile{Unit: unit, HandlingTags: []string{TagHazmat}}
	prev := map[slotplan.SKU]slotplan.SlotCode{"S4": "FWD-04", "S7": "FWD-01", "S8": "FWD-02"}
	base := mustPlan(t, Input{Velocities: vels, Profiles: profs, Slots: slots, Previous: prev})
	if len(base.Assignments) == 0 || len(base.Moves) == 0 || len(base.Unassigned) == 0 {
		t.Fatalf("the scenario must exercise assignments, moves and unassigned: %+v", base)
	}
	rng := rand.New(rand.NewSource(7))
	for i := range 50 {
		v := slices.Clone(vels)
		s := slices.Clone(slots)
		rng.Shuffle(len(v), func(a, b int) { v[a], v[b] = v[b], v[a] })
		rng.Shuffle(len(s), func(a, b int) { s[a], s[b] = s[b], s[a] })
		got := mustPlan(t, Input{Velocities: v, Profiles: profs, Slots: s, Previous: prev})
		if !reflect.DeepEqual(got, base) {
			t.Fatalf("run %d differs:\n%+v\n%+v", i, got, base)
		}
	}
}

// reverseRanking ranks by code, descending: proves the planner honours the port.
type reverseRanking struct{}

func (reverseRanking) Rank(slots []Slot) []Slot {
	out := LexicalRanking{}.Rank(slots)
	slices.Reverse(out)
	return out
}

func TestPlannerHonoursTheRankingPort(t *testing.T) {
	p, err := NewPlanner(reverseRanking{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.Plan(Input{
		Velocities: []SkuVelocity{vel("A", 9, 9)}, Profiles: profiles("A"),
		Slots: []Slot{slot("FWD-01"), slot("FWD-02")}, Params: DefaultParameters(),
	})
	if err != nil || slotOf(r, "A") != "FWD-02" {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestLexicalRanking(t *testing.T) {
	in := []Slot{slot("B"), slot("A"), slot("C")}
	got := LexicalRanking{}.Rank(in)
	if got[0].Code != "A" || got[1].Code != "B" || got[2].Code != "C" {
		t.Fatalf("%v", got)
	}
	if in[0].Code != "B" {
		t.Fatal("Rank must not reorder its input")
	}
}

type badRanking struct{ fn func([]Slot) []Slot }

func (b badRanking) Rank(s []Slot) []Slot { return b.fn(s) }

func TestRankingContractViolations(t *testing.T) {
	slots := []Slot{slot("FWD-01"), slot("FWD-02")}
	cases := map[string]func([]Slot) []Slot{
		"too few":      func(s []Slot) []Slot { return s[:1] },
		"too many":     func(s []Slot) []Slot { return append(slices.Clone(s), slot("FWD-03")) },
		"unknown code": func(s []Slot) []Slot { return []Slot{s[0], slot("FWD-99")} },
		"duplicate":    func(s []Slot) []Slot { return []Slot{s[0], s[0]} },
	}
	for name, fn := range cases {
		p, _ := NewPlanner(badRanking{fn})
		_, err := p.Plan(Input{Velocities: []SkuVelocity{vel("A", 1, 1)}, Profiles: profiles("A"), Slots: slots, Params: DefaultParameters()})
		if !errors.Is(err, ErrRankingContract) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestRankingCannotChangeSlotProperties(t *testing.T) {
	cheat := badRanking{func(s []Slot) []Slot {
		out := slices.Clone(s)
		out[0].Hazmat = true // a policy must only order
		return out
	}}
	p, _ := NewPlanner(cheat)
	r, err := p.Plan(Input{Velocities: []SkuVelocity{vel("A", 1, 1)}, Profiles: profiles("A"), Slots: []Slot{slot("FWD-01")}, Params: DefaultParameters()})
	if err != nil || slotOf(r, "A") != "FWD-01" {
		t.Fatalf("the planner uses its own slot values: %+v %v", r, err)
	}
}

func TestPlanValidatesInput(t *testing.T) {
	ok := func() Input {
		return Input{Velocities: []SkuVelocity{vel("A", 1, 1)}, Profiles: profiles("A"), Slots: []Slot{slot("FWD-01")}, Params: DefaultParameters()}
	}
	tests := []struct {
		name string
		mut  func(*Input)
		want error
	}{
		{"zero params", func(i *Input) { i.Params = Parameters{} }, ErrInvalidParameters},
		{"A below 1", func(i *Input) { i.Params = Parameters{ClassAPercent: 0, ClassBPercent: 50} }, ErrInvalidParameters},
		{"B below A", func(i *Input) { i.Params = Parameters{ClassAPercent: 60, ClassBPercent: 59} }, ErrInvalidParameters},
		{"B above 100", func(i *Input) { i.Params = Parameters{ClassAPercent: 80, ClassBPercent: 101} }, ErrInvalidParameters},
		{"empty velocity sku", func(i *Input) { i.Velocities[0].SKU = "" }, ErrInvalidVelocity},
		{"negative picks", func(i *Input) { i.Velocities[0].Picks = -1 }, ErrInvalidVelocity},
		{"negative units", func(i *Input) { i.Velocities[0].Units = -1 }, ErrInvalidVelocity},
		{"duplicate velocity", func(i *Input) { i.Velocities = append(i.Velocities, vel("A", 2, 2)) }, ErrDuplicateVelocity},
		{"empty slot code", func(i *Input) { i.Slots[0].Code = "" }, ErrInvalidSlot},
		{"duplicate slot", func(i *Input) { i.Slots = append(i.Slots, slot("FWD-01")) }, ErrDuplicateSlot},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := ok()
			tt.mut(&in)
			r, err := planner(t).Plan(in)
			if !errors.Is(err, tt.want) || !reflect.DeepEqual(r, Result{}) {
				t.Fatalf("err = %v result = %+v, want %v", err, r, tt.want)
			}
		})
	}
	// The legal edges: A = B = 100 and A = 1.
	for _, p := range []Parameters{{ClassAPercent: 100, ClassBPercent: 100}, {ClassAPercent: 1, ClassBPercent: 1}, {ClassAPercent: 80, ClassBPercent: 80}} {
		in := ok()
		in.Params = p
		if _, err := planner(t).Plan(in); err != nil {
			t.Errorf("%+v must be valid: %v", p, err)
		}
	}
	// Zero picks and zero units are legal values.
	in := ok()
	in.Velocities = []SkuVelocity{vel("A", 0, 0)}
	if _, err := planner(t).Plan(in); err != nil {
		t.Errorf("zero velocity must be valid: %v", err)
	}
}
