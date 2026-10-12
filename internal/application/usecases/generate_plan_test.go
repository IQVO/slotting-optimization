package usecases_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

func newGenerate(t *testing.T, h *harness) *usecases.GeneratePlan {
	t.Helper()
	planner, err := planning.NewPlanner(planning.LexicalRanking{})
	if err != nil {
		t.Fatal(err)
	}
	return &usecases.GeneratePlan{
		Writer: h.writer, Demand: h.demand, Profiles: h.profile, Catalogue: h.cat, IDs: h.ids,
		Planner: planner, DefaultSite: "SITE-1", ForwardZoneCodes: []string{"FWD"},
	}
}

func unit(vol, weight int64) *planning.UnitSize {
	return &planning.UnitSize{VolumeMM3: vol, WeightG: weight}
}

func slot(code string) planning.Slot {
	return planning.Slot{Code: slotplan.SlotCode(code), ZoneCode: "FWD", TemperatureClass: planning.Ambient, MaxWeightKg: 40, MaxVolumeM3: 0.5}
}

func TestGeneratePlanHappyPath(t *testing.T) {
	h := newHarness()
	h.demand.velocities = []planning.SkuVelocity{{SKU: "SKU-1", Picks: 80, Units: 160}, {SKU: "SKU-2", Picks: 20, Units: 40}, {SKU: "SKU-3", Picks: 1, Units: 1}}
	h.profile.rows["SKU-1"] = repository.ProductProfile{SKU: "SKU-1", Unit: unit(1_000_000, 1000)}
	h.profile.rows["SKU-2"] = repository.ProductProfile{SKU: "SKU-2", Unit: unit(1_000_000, 1000)}
	h.cat.slots = []planning.Slot{slot("A-01"), slot("A-02")}

	p, err := newGenerate(t, h).Handle(context.Background(), usecases.GenerateInput{})
	noErr(t, err)
	snap := p.Snapshot()
	eq(t, "state", snap.State, slotplan.StateDraft)
	eq(t, "version", snap.Version, 1)
	eq(t, "site", snap.Site, "SITE-1")
	eq(t, "id", snap.ID, "plan-1")
	eq(t, "policy", snap.Policy, slotplan.PolicyABCVelocityV1)
	eq(t, "assignments", len(snap.Assignments), 2)
	eq(t, "SKU-1 slot", snap.Assignments[0].Slot, "A-01")
	eq(t, "SKU-2 slot", snap.Assignments[1].Slot, "A-02")
	eq(t, "unassigned", len(snap.Unassigned), 1)
	eq(t, "unassigned reason", snap.Unassigned[0].Reason, slotplan.ReasonNoPhysicalProfile)
	timeEq(t, "window to", snap.Window.To, t0)
	timeEq(t, "window from", snap.Window.From, t0.Add(-28*24*time.Hour))
	assertGeneratedAndSavedOnce(t, h)
}

func assertGeneratedAndSavedOnce(t *testing.T, h *harness) {
	t.Helper()
	eq(t, "outbox", strings.Join(h.outbox.types(), ","), "SlotPlanGenerated")
	eq(t, "units of work", h.uow.runs, 1)
	eq(t, "saves", len(h.repo.saves), 1)
	eq(t, "loaded version of the insert", h.repo.saves[0].loadedVersion, 0)
	eq(t, "demand site", h.demand.site, "SITE-1")
	eq(t, "catalogue site", h.cat.site, "SITE-1")
	eq(t, "forward zone", strings.Join(h.cat.zoneCodes, ","), "FWD")
	eq(t, "profiles requested", len(h.profile.requestedSKUs), 3)
}

func TestGeneratePlanIsStickyAgainstTheApprovedPlan(t *testing.T) {
	h := newHarness()
	prev := h.approved("plan-9", "SITE-1", t0.Add(-time.Hour)) // SKU-1 -> S-plan-9
	h.demand.velocities = []planning.SkuVelocity{{SKU: "SKU-1", Picks: 5, Units: 5}}
	h.profile.rows["SKU-1"] = repository.ProductProfile{SKU: "SKU-1", Unit: unit(1000, 100)}
	h.cat.slots = []planning.Slot{slot("A-01"), slot("S-plan-9")}

	p, err := newGenerate(t, h).Handle(context.Background(), usecases.GenerateInput{SiteID: "SITE-1", LookbackDays: 7})
	if err != nil {
		t.Fatal(err)
	}
	snap := p.Snapshot()
	if snap.Assignments[0].Slot != "S-plan-9" || len(snap.Moves) != 0 {
		t.Fatalf("a SKU that still fits keeps its slot: %+v moves %+v", snap.Assignments, snap.Moves)
	}
	if !snap.Window.From.Equal(t0.Add(-7 * 24 * time.Hour)) {
		t.Fatalf("window from = %v", snap.Window.From)
	}
	if prev.State() != slotplan.StateApproved {
		t.Fatal("generating must not touch the approved plan")
	}
}

func TestGeneratePlanUsesTheConfiguredDefaultLookback(t *testing.T) {
	for _, c := range []struct{ configured, want int }{{7, 7}, {0, 28}, {-3, 28}, {400, 28}} {
		h := newHarness()
		uc := newGenerate(t, h)
		uc.DefaultLookback = c.configured
		p, err := uc.Handle(context.Background(), usecases.GenerateInput{})
		noErr(t, err)
		timeEq(t, "window from", p.Snapshot().Window.From, t0.Add(-time.Duration(c.want)*24*time.Hour))
	}
	h := newHarness()
	uc := newGenerate(t, h)
	uc.DefaultLookback = 7
	p, err := uc.Handle(context.Background(), usecases.GenerateInput{LookbackDays: 14})
	noErr(t, err)
	timeEq(t, "a request overrides the configured default", p.Snapshot().Window.From, t0.Add(-14*24*time.Hour))
}

func TestGeneratePlanValidatesTheRequest(t *testing.T) {
	cases := []struct {
		name string
		in   usecases.GenerateInput
		site string
		want error
	}{
		{"bad site", usecases.GenerateInput{SiteID: "a b"}, "SITE-1", slotplan.ErrInvalidSiteID},
		{"no site at all", usecases.GenerateInput{}, "", usecases.ErrNoSite},
		{"lookback too small", usecases.GenerateInput{LookbackDays: -1}, "SITE-1", usecases.ErrInvalidLookback},
		{"lookback too big", usecases.GenerateInput{LookbackDays: 366}, "SITE-1", usecases.ErrInvalidLookback},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness()
			uc := newGenerate(t, h)
			uc.DefaultSite = c.site
			if _, err := uc.Handle(context.Background(), c.in); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if h.uow.runs != 0 {
				t.Fatal("nothing may run for an invalid request")
			}
		})
	}
}

func TestGeneratePlanPropagatesFailures(t *testing.T) {
	setups := map[string]func(h *harness){
		"demand":    func(h *harness) { h.demand.velErr = errBoom },
		"profiles":  func(h *harness) { h.profile.manyErr = errBoom },
		"catalogue": func(h *harness) { h.cat.slotsErr = errBoom },
		"approved":  func(h *harness) { h.repo.curErr = errBoom },
		"save":      func(h *harness) { h.repo.saveErr["plan-1"] = errBoom },
		"encode":    func(h *harness) { h.enc.err = errBoom },
		"outbox":    func(h *harness) { h.outbox.err = errBoom },
	}
	for name, setup := range setups {
		t.Run(name, func(t *testing.T) {
			h := newHarness()
			setup(h)
			if _, err := newGenerate(t, h).Handle(context.Background(), usecases.GenerateInput{}); !errors.Is(err, errBoom) {
				t.Fatalf("err = %v", err)
			}
			if h.uow.failures != 1 {
				t.Fatalf("the unit of work must fail so everything rolls back, failures = %d", h.uow.failures)
			}
		})
	}
}

func TestGeneratePlanRejectsAnInvalidPlannerResult(t *testing.T) {
	h := newHarness()
	h.demand.velocities = []planning.SkuVelocity{{SKU: "SKU-1", Picks: 1}, {SKU: "SKU-1", Picks: 2}}
	if _, err := newGenerate(t, h).Handle(context.Background(), usecases.GenerateInput{}); !errors.Is(err, planning.ErrDuplicateVelocity) {
		t.Fatalf("err = %v", err)
	}
}
