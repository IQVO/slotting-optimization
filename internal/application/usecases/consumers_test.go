package usecases_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
)

var due = time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)

func activeLine() usecases.DemandChange {
	return usecases.DemandChange{SourceOrderID: "ord-1", LineNo: 1, SiteID: "SITE-1", SKU: "SKU-1", Units: 12, DueAt: due, State: usecases.DemandActive}
}

func TestApplyDemandChanged(t *testing.T) {
	h := newHarness()
	uc := &usecases.ApplyDemandChanged{Intake: h.intake(), Demand: h.demand}
	ctx := context.Background()

	out, err := uc.Handle(ctx, "e1", activeLine())
	noErr(t, err)
	eq(t, "outcome", out, usecases.OutcomeApplied)
	eq(t, "lines", len(h.demand.lines), 1)
	l := h.demand.lines[0]
	eq(t, "active", l.Active, true)
	eq(t, "units", l.Units, 12)
	eq(t, "site", l.Site, "SITE-1")
	eq(t, "sku", l.SKU, "SKU-1")
	eq(t, "line no", l.LineNo, 1)
	eq(t, "order", l.SourceOrderID, "ord-1")
	timeEq(t, "due", l.DueAt, due)

	out, err = uc.Handle(ctx, "e1", activeLine())
	noErr(t, err)
	eq(t, "the same CloudEvents id is applied once", out, usecases.OutcomeDuplicate)
	eq(t, "lines after the duplicate", len(h.demand.lines), 1)
}

func TestApplyDemandChangedRemovedDeactivatesTheLine(t *testing.T) {
	h := newHarness()
	uc := &usecases.ApplyDemandChanged{Intake: h.intake(), Demand: h.demand}
	removed := activeLine()
	removed.State, removed.Units = usecases.DemandRemoved, 0
	out, err := uc.Handle(context.Background(), "e2", removed)
	noErr(t, err)
	eq(t, "outcome", out, usecases.OutcomeApplied)
	eq(t, "active", h.demand.lines[0].Active, false)
	eq(t, "units", h.demand.lines[0].Units, 0)
}

func TestApplyDemandChangedSiteFilter(t *testing.T) {
	h := newHarness()
	uc := &usecases.ApplyDemandChanged{Intake: h.intake(), Demand: h.demand, OnlySite: "SITE-1"}
	other := activeLine()
	other.SiteID = "SITE-2"
	out, err := uc.Handle(context.Background(), "e1", other)
	if err != nil || out != usecases.OutcomeIgnored || len(h.demand.lines) != 0 || h.uow.runs != 0 {
		t.Fatalf("a line of another site is ignored without a claim: %q %v", out, err)
	}
	if out, err := uc.Handle(context.Background(), "e2", activeLine()); err != nil || out != usecases.OutcomeApplied {
		t.Fatalf("out %q err %v", out, err)
	}
}

func TestApplyDemandChangedRejectsInvalidPayloads(t *testing.T) {
	mutations := map[string]func(c *usecases.DemandChange){
		"bad site":        func(c *usecases.DemandChange) { c.SiteID = "a b" },
		"bad sku":         func(c *usecases.DemandChange) { c.SKU = "" },
		"no order":        func(c *usecases.DemandChange) { c.SourceOrderID = "" },
		"line zero":       func(c *usecases.DemandChange) { c.LineNo = 0 },
		"no due":          func(c *usecases.DemandChange) { c.DueAt = time.Time{} },
		"unknown state":   func(c *usecases.DemandChange) { c.State = "PAUSED" },
		"active no units": func(c *usecases.DemandChange) { c.Units = 0 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			h := newHarness()
			c := activeLine()
			mutate(&c)
			uc := &usecases.ApplyDemandChanged{Intake: h.intake(), Demand: h.demand}
			if _, err := uc.Handle(context.Background(), "e1", c); !errors.Is(err, usecases.ErrInvalidEvent) {
				t.Fatalf("err = %v", err)
			}
			if h.uow.runs != 0 || len(h.process.seen) != 0 {
				t.Fatal("an invalid event must claim nothing")
			}
		})
	}
}

func TestConsumersRollBackTheClaimOnFailure(t *testing.T) {
	h := newHarness()
	h.demand.applyErr = errBoom
	uc := &usecases.ApplyDemandChanged{Intake: h.intake(), Demand: h.demand}
	if _, err := uc.Handle(context.Background(), "e1", activeLine()); !errors.Is(err, errBoom) || h.uow.failures != 1 {
		t.Fatalf("err = %v failures %d", err, h.uow.failures)
	}
	h2 := newHarness()
	h2.process.err = errBoom
	uc2 := &usecases.ApplyDemandChanged{Intake: h2.intake(), Demand: h2.demand}
	if _, err := uc2.Handle(context.Background(), "e1", activeLine()); !errors.Is(err, errBoom) {
		t.Fatalf("claim error = %v", err)
	}
}

func TestApplyProductClassified(t *testing.T) {
	h := newHarness()
	uc := &usecases.ApplyProductClassified{Intake: h.intake(), Profiles: h.profile}
	ctx := context.Background()
	c := usecases.ProductClassification{SKU: "SKU-9", HandlingTags: []string{"Hazmat"}, TemperatureClass: "Frozen", Version: 3}

	if out, err := uc.Handle(ctx, "e1", c); err != nil || out != usecases.OutcomeApplied {
		t.Fatalf("out %q err %v", out, err)
	}
	got := h.profile.rows["SKU-9"]
	if got.Version != 3 || got.TemperatureClass != planning.Frozen || len(got.HandlingTags) != 1 || got.Unit != nil {
		t.Fatalf("profile = %+v", got)
	}
	if out, _ := uc.Handle(ctx, "e1", c); out != usecases.OutcomeDuplicate {
		t.Fatalf("out = %q", out)
	}
	older := c
	older.Version, older.TemperatureClass = 3, "Chilled"
	if out, _ := uc.Handle(ctx, "e2", older); out != usecases.OutcomeStale || h.profile.rows["SKU-9"].TemperatureClass != planning.Frozen {
		t.Fatalf("a message that is not newer must not apply: %q %+v", out, h.profile.rows["SKU-9"])
	}
	newer := c
	newer.Version, newer.HandlingTags, newer.TemperatureClass = 4, nil, ""
	if out, _ := uc.Handle(ctx, "e3", newer); out != usecases.OutcomeApplied {
		t.Fatalf("out = %q", out)
	}
	if got := h.profile.rows["SKU-9"]; got.Version != 4 || len(got.HandlingTags) != 0 || got.TemperatureClass != "" {
		t.Fatalf("a classification replaces the previous one: %+v", got)
	}
}

func TestApplyProductClassifiedValidationAndFailures(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]usecases.ProductClassification{
		"bad sku":     {SKU: "", Version: 1},
		"bad temp":    {SKU: "S", TemperatureClass: "Lukewarm", Version: 1},
		"bad version": {SKU: "S", Version: 0},
	} {
		h := newHarness()
		uc := &usecases.ApplyProductClassified{Intake: h.intake(), Profiles: h.profile}
		if _, err := uc.Handle(ctx, "e", c); !errors.Is(err, usecases.ErrInvalidEvent) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	h := newHarness()
	h.profile.getErr = errBoom
	uc := &usecases.ApplyProductClassified{Intake: h.intake(), Profiles: h.profile}
	if _, err := uc.Handle(ctx, "e", usecases.ProductClassification{SKU: "S", Version: 1}); !errors.Is(err, errBoom) {
		t.Fatalf("read err = %v", err)
	}
	h2 := newHarness()
	h2.profile.saveErr = errBoom
	uc2 := &usecases.ApplyProductClassified{Intake: h2.intake(), Profiles: h2.profile}
	if _, err := uc2.Handle(ctx, "e", usecases.ProductClassification{SKU: "S", Version: 1}); !errors.Is(err, errBoom) {
		t.Fatalf("save err = %v", err)
	}
}

func TestApplyPhysicalProfile(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	cls := &usecases.ApplyProductClassified{Intake: h.intake(), Profiles: h.profile}
	phys := &usecases.ApplyPhysicalProfile{Intake: h.intake(), Profiles: h.profile}
	if _, err := cls.Handle(ctx, "e0", usecases.ProductClassification{SKU: "SKU-1", HandlingTags: []string{"Fragile"}, Version: 2}); err != nil {
		t.Fatal(err)
	}

	out, err := phys.Handle(ctx, "e1", usecases.PhysicalChange{SKU: "SKU-1", HasEffective: true, VolumeMM3: 1_920_000, WeightG: 1500, Version: 4})
	if err != nil || out != usecases.OutcomeApplied {
		t.Fatalf("out %q err %v", out, err)
	}
	got := h.profile.rows["SKU-1"]
	if got.Unit == nil || got.Unit.VolumeMM3 != 1_920_000 || got.Unit.WeightG != 1500 || got.Version != 4 || got.HandlingTags[0] != "Fragile" {
		t.Fatalf("the classification must survive a physical update: %+v", got)
	}
	if out, _ := phys.Handle(ctx, "e2", usecases.PhysicalChange{SKU: "SKU-1", HasEffective: true, VolumeMM3: 1, WeightG: 1, Version: 4}); out != usecases.OutcomeStale {
		t.Fatalf("out = %q", out)
	}
	if out, _ := phys.Handle(ctx, "e3", usecases.PhysicalChange{SKU: "SKU-1", Version: 5}); out != usecases.OutcomeApplied || h.profile.rows["SKU-1"].Unit != nil {
		t.Fatalf("a message without effective dimensions clears the unit: %q %+v", out, h.profile.rows["SKU-1"])
	}
	if out, _ := phys.Handle(ctx, "e3", usecases.PhysicalChange{SKU: "SKU-1", Version: 5}); out != usecases.OutcomeDuplicate {
		t.Fatalf("out = %q", out)
	}
}

func TestApplyPhysicalProfileValidationAndFailures(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]usecases.PhysicalChange{
		"bad sku":     {SKU: "", Version: 1},
		"bad version": {SKU: "S", Version: 0},
		"zero volume": {SKU: "S", HasEffective: true, VolumeMM3: 0, WeightG: 5, Version: 1},
		"zero weight": {SKU: "S", HasEffective: true, VolumeMM3: 5, WeightG: 0, Version: 1},
	} {
		h := newHarness()
		uc := &usecases.ApplyPhysicalProfile{Intake: h.intake(), Profiles: h.profile}
		if _, err := uc.Handle(ctx, "e", c); !errors.Is(err, usecases.ErrInvalidEvent) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	h := newHarness()
	h.profile.getErr = errBoom
	uc := &usecases.ApplyPhysicalProfile{Intake: h.intake(), Profiles: h.profile}
	if _, err := uc.Handle(ctx, "e", usecases.PhysicalChange{SKU: "S", Version: 1}); !errors.Is(err, errBoom) {
		t.Fatalf("read err = %v", err)
	}
	h2 := newHarness()
	h2.profile.saveErr = errBoom
	uc2 := &usecases.ApplyPhysicalProfile{Intake: h2.intake(), Profiles: h2.profile}
	if _, err := uc2.Handle(ctx, "e", usecases.PhysicalChange{SKU: "S", Version: 1}); !errors.Is(err, errBoom) {
		t.Fatalf("save err = %v", err)
	}
}

func TestZoneRegistered(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	zone := &usecases.ApplyZoneRegistered{Intake: h.intake(), Catalogue: h.cat}
	z := usecases.ZoneChange{ZoneID: "WH1-FWD-AMB", SiteCode: "WH1", AreaCode: "FWD", ZoneCode: "FWD", TemperatureClass: "Ambient"}

	out, err := zone.Handle(ctx, "z1", z)
	noErr(t, err)
	eq(t, "outcome", out, usecases.OutcomeApplied)
	eq(t, "zones", len(h.cat.zones), 1)
	eq(t, "zone id", h.cat.zones[0].ID, "WH1-FWD-AMB")
	eq(t, "temperature", h.cat.zones[0].TemperatureClass, planning.Ambient)
	out, err = zone.Handle(ctx, "z1", z)
	noErr(t, err)
	eq(t, "duplicate", out, usecases.OutcomeDuplicate)
}

func TestLocationSlotRegistered(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	reg := &usecases.ApplyLocationSlotRegistered{Intake: h.intake(), Catalogue: h.cat}
	s := usecases.SlotChange{LocationCode: "WH1-FWD-A01-01-01-A", ZoneID: "WH1-FWD-AMB", MaxWeightKg: 40, MaxVolumeM3: 0.5}

	out, err := reg.Handle(ctx, "s1", s)
	noErr(t, err)
	eq(t, "outcome", out, usecases.OutcomeApplied)
	got := h.cat.savedSlots[0]
	eq(t, "an absent role means Storage", got.Role, repository.RoleStorage)
	eq(t, "volume", got.MaxVolumeM3, 0.5)
	eq(t, "zone", got.ZoneID, "WH1-FWD-AMB")

	s.Role = "Receiving"
	_, err = reg.Handle(ctx, "s2", s)
	noErr(t, err)
	eq(t, "explicit role kept", h.cat.savedSlots[1].Role, "Receiving")
}

func TestLocationSlotDecommissioned(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	dec := &usecases.ApplyLocationSlotDecommissioned{Intake: h.intake(), Catalogue: h.cat}
	out, err := dec.Handle(ctx, "d1", "WH1-FWD-A01-01-01-A")
	noErr(t, err)
	eq(t, "outcome", out, usecases.OutcomeApplied)
	eq(t, "retired", len(h.cat.retired), 1)
	out, err = dec.Handle(ctx, "d1", "WH1-FWD-A01-01-01-A")
	noErr(t, err)
	eq(t, "duplicate", out, usecases.OutcomeDuplicate)
}

func TestLayoutConsumersValidationAndFailures(t *testing.T) {
	ctx := context.Background()
	h := newHarness()
	zone := &usecases.ApplyZoneRegistered{Intake: h.intake(), Catalogue: h.cat}
	reg := &usecases.ApplyLocationSlotRegistered{Intake: h.intake(), Catalogue: h.cat}
	dec := &usecases.ApplyLocationSlotDecommissioned{Intake: h.intake(), Catalogue: h.cat}

	for name, z := range map[string]usecases.ZoneChange{
		"no zone id": {SiteCode: "WH1", ZoneCode: "FWD"}, "no site": {ZoneID: "z", ZoneCode: "FWD"},
		"no code": {ZoneID: "z", SiteCode: "WH1"}, "bad temp": {ZoneID: "z", SiteCode: "WH1", ZoneCode: "F", TemperatureClass: "Hot"},
	} {
		if _, err := zone.Handle(ctx, "e", z); !errors.Is(err, usecases.ErrInvalidEvent) {
			t.Fatalf("zone %s: err = %v", name, err)
		}
	}
	for name, s := range map[string]usecases.SlotChange{
		"bad code": {ZoneID: "z"}, "no zone": {LocationCode: "L"},
		"negative weight": {LocationCode: "L", ZoneID: "z", MaxWeightKg: -1}, "negative volume": {LocationCode: "L", ZoneID: "z", MaxVolumeM3: -1},
	} {
		if _, err := reg.Handle(ctx, "e", s); !errors.Is(err, usecases.ErrInvalidEvent) {
			t.Fatalf("slot %s: err = %v", name, err)
		}
	}
	if _, err := dec.Handle(ctx, "e", ""); !errors.Is(err, usecases.ErrInvalidEvent) {
		t.Fatalf("decommission: err = %v", err)
	}

	h.cat.err = errBoom
	if _, err := zone.Handle(ctx, "e1", usecases.ZoneChange{ZoneID: "z", SiteCode: "WH1", ZoneCode: "F"}); !errors.Is(err, errBoom) {
		t.Fatalf("zone save: %v", err)
	}
	if _, err := reg.Handle(ctx, "e2", usecases.SlotChange{LocationCode: "L", ZoneID: "z"}); !errors.Is(err, errBoom) {
		t.Fatalf("slot save: %v", err)
	}
	if _, err := dec.Handle(ctx, "e3", "L"); !errors.Is(err, errBoom) {
		t.Fatalf("decommission: %v", err)
	}
}
