package usecases_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/domain/packlevel"
)

var from = time.Date(2026, 10, 6, 20, 0, 0, 0, time.UTC)

func declared(version int64) usecases.PackLevelDeclaredChange {
	return usecases.PackLevelDeclaredChange{
		SKU: "SKU-1", Level: "CASE", GTIN: "24006381333935", UnitsPerPack: 12,
		Dims:          &packlevel.Dims{LengthMM: 400, WidthMM: 300, HeightMM: 200, WeightG: 6500},
		EffectiveFrom: from, Version: version,
	}
}

func packHarness() (*harness, *fakePacks) {
	h, p := newHarness(), newFakePacks()
	return h, p
}

func TestApplyPackLevelDeclared(t *testing.T) {
	h, packs := packHarness()
	uc := &usecases.ApplyPackLevelDeclared{Intake: h.intake(), PackLevels: packs}
	ctx := context.Background()

	out, err := uc.Handle(ctx, "e1", declared(3))
	noErr(t, err)
	eq(t, "outcome", out, usecases.OutcomeApplied)
	l := packs.rows["SKU-1|CASE"]
	eq(t, "gtin", l.GTIN, "24006381333935")
	eq(t, "units", l.UnitsPerPack, 12)
	eq(t, "version", l.Version, 3)
	eq(t, "length", l.Declared.LengthMM, 400)
	timeEq(t, "effective from", l.EffectiveFrom, from)

	out, err = uc.Handle(ctx, "e1", declared(4))
	noErr(t, err)
	eq(t, "the same CloudEvents id is applied once", out, usecases.OutcomeDuplicate)

	for name, v := range map[string]int64{"replay": 3, "older": 2} {
		out, err = uc.Handle(ctx, "e-"+name, declared(v))
		noErr(t, err)
		eq(t, name, out, usecases.OutcomeStale)
	}
	eq(t, "saves", packs.saves, 1)

	out, err = uc.Handle(ctx, "e5", declared(4))
	noErr(t, err)
	eq(t, "newer version", out, usecases.OutcomeApplied)
}

func TestApplyPackLevelDeclaredKeepsTheMeasurementOfTheSameGTINOnly(t *testing.T) {
	h, packs := packHarness()
	declare := &usecases.ApplyPackLevelDeclared{Intake: h.intake(), PackLevels: packs}
	measure := &usecases.ApplyPackLevelMeasured{Intake: h.intake(), PackLevels: packs}
	ctx := context.Background()
	_, err := declare.Handle(ctx, "d1", declared(1))
	noErr(t, err)
	out, err := measure.Handle(ctx, "m1", usecases.PackLevelMeasuredChange{
		SKU: "SKU-1", Level: "CASE", GTIN: "24006381333935", Version: 2,
		Dims: packlevel.Dims{LengthMM: 410, WidthMM: 310, HeightMM: 210, WeightG: 6900},
	})
	noErr(t, err)
	eq(t, "measured", out, usecases.OutcomeApplied)

	_, err = declare.Handle(ctx, "d2", declared(3)) // same GTIN re-sent in full
	noErr(t, err)
	if packs.rows["SKU-1|CASE"].Measured == nil {
		t.Fatal("a re-declaration of the same GTIN keeps the measurement")
	}
	next := declared(4)
	next.GTIN = "34006381333932"
	_, err = declare.Handle(ctx, "d3", next)
	noErr(t, err)
	if packs.rows["SKU-1|CASE"].Measured != nil {
		t.Fatal("a new GTIN starts unmeasured")
	}
}

func TestApplyPackLevelDeclaredRejectsInvalidPayloads(t *testing.T) {
	h, packs := packHarness()
	uc := &usecases.ApplyPackLevelDeclared{Intake: h.intake(), PackLevels: packs}
	mut := func(f func(*usecases.PackLevelDeclaredChange)) usecases.PackLevelDeclaredChange {
		c := declared(1)
		f(&c)
		return c
	}
	for name, c := range map[string]usecases.PackLevelDeclaredChange{
		"blank sku":    mut(func(c *usecases.PackLevelDeclaredChange) { c.SKU = "" }),
		"sku with /":   mut(func(c *usecases.PackLevelDeclaredChange) { c.SKU = "A/B" }),
		"bad level":    mut(func(c *usecases.PackLevelDeclaredChange) { c.Level = "BOX" }),
		"blank gtin":   mut(func(c *usecases.PackLevelDeclaredChange) { c.GTIN = "" }),
		"zero units":   mut(func(c *usecases.PackLevelDeclaredChange) { c.UnitsPerPack = 0 }),
		"zero version": mut(func(c *usecases.PackLevelDeclaredChange) { c.Version = 0 }),
		"zero weight":  mut(func(c *usecases.PackLevelDeclaredChange) { c.Dims.WeightG = 0 }),
	} {
		_, err := uc.Handle(context.Background(), "e-"+name, c)
		if !errors.Is(err, usecases.ErrInvalidEvent) {
			t.Errorf("%s: err = %v, want ErrInvalidEvent", name, err)
		}
	}
	eq(t, "nothing claimed", h.uow.runs, 0)
}

func TestApplyPackLevelDeclaredPropagatesStorageErrors(t *testing.T) {
	h, packs := packHarness()
	uc := &usecases.ApplyPackLevelDeclared{Intake: h.intake(), PackLevels: packs}
	packs.getErr = errBoom
	if _, err := uc.Handle(context.Background(), "e1", declared(1)); !errors.Is(err, errBoom) {
		t.Fatalf("get: %v", err)
	}
	packs.getErr, packs.saveErr = nil, errBoom
	if _, err := uc.Handle(context.Background(), "e2", declared(1)); !errors.Is(err, errBoom) {
		t.Fatalf("save: %v", err)
	}
}

func retired(gtin string, version int64) usecases.PackLevelRetiredChange {
	return usecases.PackLevelRetiredChange{SKU: "SKU-1", Level: "CASE", GTIN: gtin, EffectiveTo: from.Add(time.Hour), Version: version}
}

func TestApplyPackLevelRetired(t *testing.T) {
	h, packs := packHarness()
	declare := &usecases.ApplyPackLevelDeclared{Intake: h.intake(), PackLevels: packs}
	uc := &usecases.ApplyPackLevelRetired{Intake: h.intake(), PackLevels: packs}
	ctx := context.Background()

	out, err := uc.Handle(ctx, "r0", retired("24006381333935", 2))
	noErr(t, err)
	eq(t, "a level never seen", out, usecases.OutcomeIgnored)

	_, err = declare.Handle(ctx, "d1", declared(1))
	noErr(t, err)
	out, err = uc.Handle(ctx, "r1", retired("OTHER-GTIN", 2))
	noErr(t, err)
	eq(t, "another gtin", out, usecases.OutcomeIgnored)
	out, err = uc.Handle(ctx, "r2", retired("24006381333935", 1))
	noErr(t, err)
	eq(t, "an equal version for the stored gtin", out, usecases.OutcomeStale)

	out, err = uc.Handle(ctx, "r3", retired("24006381333935", 2))
	noErr(t, err)
	eq(t, "retired", out, usecases.OutcomeApplied)
	l := packs.rows["SKU-1|CASE"]
	if l.EffectiveTo == nil || !l.EffectiveTo.Equal(from.Add(time.Hour)) || l.Version != 2 {
		t.Fatalf("level = %+v", l)
	}
	out, err = uc.Handle(ctx, "r3", retired("24006381333935", 3))
	noErr(t, err)
	eq(t, "duplicate id", out, usecases.OutcomeDuplicate)
}

func TestApplyPackLevelRetiredRejectsInvalidPayloads(t *testing.T) {
	h, packs := packHarness()
	uc := &usecases.ApplyPackLevelRetired{Intake: h.intake(), PackLevels: packs}
	for name, c := range map[string]usecases.PackLevelRetiredChange{
		"blank gtin": retired("", 2), "zero version": retired("g", 0),
		"bad level": {SKU: "S", Level: "BOX", GTIN: "g", Version: 1}, "bad sku": {SKU: "", Level: "CASE", GTIN: "g", Version: 1},
	} {
		if _, err := uc.Handle(context.Background(), "e-"+name, c); !errors.Is(err, usecases.ErrInvalidEvent) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	packs.getErr = errBoom
	if _, err := uc.Handle(context.Background(), "e", retired("g", 2)); !errors.Is(err, errBoom) {
		t.Errorf("get: %v", err)
	}
	packs.getErr, packs.saveErr = nil, errBoom
	packs.rows["SKU-1|CASE"] = packlevel.PackLevel{SKU: "SKU-1", Level: packlevel.Case, GTIN: "g", UnitsPerPack: 2, EffectiveFrom: from, Version: 1}
	if _, err := uc.Handle(context.Background(), "e2", retired("g", 2)); !errors.Is(err, errBoom) {
		t.Errorf("save: %v", err)
	}
}

func measured(gtin string, version int64) usecases.PackLevelMeasuredChange {
	return usecases.PackLevelMeasuredChange{
		SKU: "SKU-1", Level: "CASE", GTIN: gtin, Version: version,
		Dims: packlevel.Dims{LengthMM: 410, WidthMM: 310, HeightMM: 210, WeightG: 6900},
	}
}

func TestApplyPackLevelMeasured(t *testing.T) {
	h, packs := packHarness()
	declare := &usecases.ApplyPackLevelDeclared{Intake: h.intake(), PackLevels: packs}
	uc := &usecases.ApplyPackLevelMeasured{Intake: h.intake(), PackLevels: packs}
	ctx := context.Background()

	out, err := uc.Handle(ctx, "m0", measured("24006381333935", 2))
	noErr(t, err)
	eq(t, "a level never seen", out, usecases.OutcomeIgnored)

	_, err = declare.Handle(ctx, "d1", declared(1))
	noErr(t, err)
	out, err = uc.Handle(ctx, "m1", measured("OTHER", 2))
	noErr(t, err)
	eq(t, "another gtin", out, usecases.OutcomeIgnored)
	out, err = uc.Handle(ctx, "m2", measured("24006381333935", 1))
	noErr(t, err)
	eq(t, "equal version", out, usecases.OutcomeStale)
	out, err = uc.Handle(ctx, "m3", measured("24006381333935", 2))
	noErr(t, err)
	eq(t, "applied", out, usecases.OutcomeApplied)
	l := packs.rows["SKU-1|CASE"]
	eq(t, "version", l.Version, 2)
	eq(t, "measured weight", l.Measured.WeightG, 6900)
	eq(t, "declared weight kept", l.Declared.WeightG, 6500)
}

func TestApplyPackLevelMeasuredRejectsInvalidPayloads(t *testing.T) {
	h, packs := packHarness()
	uc := &usecases.ApplyPackLevelMeasured{Intake: h.intake(), PackLevels: packs}
	bad := measured("g", 2)
	bad.Dims.HeightMM = 0
	for name, c := range map[string]usecases.PackLevelMeasuredChange{
		"zero height": bad, "blank gtin": measured("", 2), "zero version": measured("g", 0),
	} {
		if _, err := uc.Handle(context.Background(), "e-"+name, c); !errors.Is(err, usecases.ErrInvalidEvent) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	packs.getErr = errBoom
	if _, err := uc.Handle(context.Background(), "e", measured("g", 2)); !errors.Is(err, errBoom) {
		t.Errorf("get: %v", err)
	}
	packs.getErr, packs.saveErr = nil, errBoom
	packs.rows["SKU-1|CASE"] = packlevel.PackLevel{SKU: "SKU-1", Level: packlevel.Case, GTIN: "g", UnitsPerPack: 2, EffectiveFrom: from, Version: 1}
	if _, err := uc.Handle(context.Background(), "e2", measured("g", 2)); !errors.Is(err, errBoom) {
		t.Errorf("save: %v", err)
	}
}
