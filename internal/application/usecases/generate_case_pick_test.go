package usecases_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

func casePickHarness(t *testing.T) (*harness, *usecases.GeneratePlan, *fakePacks) {
	t.Helper()
	h, packs := newHarness(), newFakePacks()
	uc := newGenerate(t, h)
	uc.CasePickZoneCodes, uc.PackLevels = []string{"CASEPICK"}, packs
	h.demand.velocities = []planning.SkuVelocity{{SKU: "SKU-1", Picks: 80, Units: 960}, {SKU: "SKU-2", Picks: 20, Units: 30}}
	h.demand.gcd = map[slotplan.SKU]int64{"SKU-1": 12, "SKU-2": 1}
	h.profile.rows["SKU-1"] = repository.ProductProfile{SKU: "SKU-1", Unit: unit(1_000_000, 500)}
	h.profile.rows["SKU-2"] = repository.ProductProfile{SKU: "SKU-2", Unit: unit(1_000_000, 500)}
	packs.cases = map[slotplan.SKU]planning.CaseSize{
		"SKU-1": {VolumeMM3: 24_000_000, WeightG: 6500, UnitsPerPack: 12},
		"SKU-2": {VolumeMM3: 24_000_000, WeightG: 6500, UnitsPerPack: 12},
	}
	h.cat.slots = []planning.Slot{
		slot("A-01"),
		{Code: "C-01", ZoneCode: "CASEPICK", TemperatureClass: planning.Ambient, MaxWeightKg: 100, MaxVolumeM3: 0.1},
	}
	return h, uc, packs
}

func TestGeneratePlanSplitsCasePickFromEachPickFaces(t *testing.T) {
	h, uc, packs := casePickHarness(t)
	p, err := uc.Handle(context.Background(), usecases.GenerateInput{})
	noErr(t, err)
	as := p.Snapshot().Assignments
	eq(t, "assignments", len(as), 2)
	eq(t, "SKU-1 face", as[0].Slot, "C-01")
	eq(t, "SKU-1 unit", as[0].PickUnit, slotplan.PickCase)
	eq(t, "SKU-1 cases capacity (4.16 cases by volume)", as[0].CasesCapacity, 4)
	eq(t, "SKU-2 face", as[1].Slot, "A-01")
	eq(t, "SKU-2 unit", as[1].PickUnit, slotplan.PickEach)
	eq(t, "SKU-2 cases capacity", as[1].CasesCapacity, 0)

	timeEq(t, "case levels read at the plan instant", packs.askedAt, t0)
	if !slices.Equal(packs.askedFor, []slotplan.SKU{"SKU-1", "SKU-2"}) {
		t.Fatalf("asked for %v", packs.askedFor)
	}
	if want := []string{"FWD", "CASEPICK"}; !slices.Equal(h.cat.zoneCodes, want) {
		t.Fatalf("zones read = %v, want %v", h.cat.zoneCodes, want)
	}
}

func TestGeneratePlanWithoutCasePickZonesNeverTouchesPackData(t *testing.T) {
	h, uc, packs := casePickHarness(t)
	uc.CasePickZoneCodes, uc.PackLevels = nil, nil
	h.cat.slots = []planning.Slot{slot("A-01"), slot("A-02")}
	p, err := uc.Handle(context.Background(), usecases.GenerateInput{})
	noErr(t, err)
	for _, a := range p.Snapshot().Assignments {
		eq(t, "pick unit "+string(a.SKU), a.PickUnit, slotplan.PickEach)
	}
	eq(t, "line quantities not read", h.demand.gcdCalls, 0)
	eq(t, "case levels not read", len(packs.askedFor), 0)
	if want := []string{"FWD"}; !slices.Equal(h.cat.zoneCodes, want) {
		t.Fatalf("zones read = %v", h.cat.zoneCodes)
	}
}

func TestGeneratePlanCaseSizeOnlyLandsOnKnownProfiles(t *testing.T) {
	h, uc, _ := casePickHarness(t)
	delete(h.profile.rows, "SKU-1") // no product profile: handling class unknown
	p, err := uc.Handle(context.Background(), usecases.GenerateInput{})
	noErr(t, err)
	snap := p.Snapshot()
	eq(t, "assignments", len(snap.Assignments), 1)
	eq(t, "unassigned", len(snap.Unassigned), 1)
	eq(t, "reason", snap.Unassigned[0].Reason, slotplan.ReasonNoPhysicalProfile)
}

func TestGeneratePlanCasePickFailures(t *testing.T) {
	h, uc, packs := casePickHarness(t)
	packs.activeErr = errBoom
	if _, err := uc.Handle(context.Background(), usecases.GenerateInput{}); !errors.Is(err, errBoom) {
		t.Fatalf("case levels: %v", err)
	}
	packs.activeErr, h.demand.gcdErr = nil, errBoom
	if _, err := uc.Handle(context.Background(), usecases.GenerateInput{}); !errors.Is(err, errBoom) {
		t.Fatalf("line quantities: %v", err)
	}
	h.demand.gcdErr, uc.PackLevels = nil, nil
	if _, err := uc.Handle(context.Background(), usecases.GenerateInput{}); err == nil {
		t.Fatal("case-pick zones without a pack-level view is a wiring error, not a silent EACH plan")
	}
}

func TestGeneratePlanForwardZoneAlsoListedAsCasePickIsReadOnce(t *testing.T) {
	h, uc, _ := casePickHarness(t)
	uc.CasePickZoneCodes = []string{"FWD", "CASEPICK"}
	_, err := uc.Handle(context.Background(), usecases.GenerateInput{})
	noErr(t, err)
	if want := []string{"FWD", "CASEPICK"}; !slices.Equal(h.cat.zoneCodes, want) {
		t.Fatalf("zones read = %v, want %v", h.cat.zoneCodes, want)
	}
}
