package repocontract

import (
	"context"
	"testing"
	"time"

	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/domain/packlevel"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// PackLevelView runs the PackLevelView contract over an EMPTY view.
func PackLevelView(t *testing.T, newView func(t *testing.T) ports.PackLevelView) {
	t.Helper()
	cases := map[string]func(t *testing.T, v ports.PackLevelView){
		"an unseen level is not found":                       packLevelNotFound,
		"save then get round trips every field":              packLevelRoundTrip,
		"save upserts per sku and level":                     packLevelUpsert,
		"active cases use measured dims over declared":       activeCasesMeasured,
		"active cases are bounded by effective from and to":  activeCasesWindow,
		"active cases skip levels without dims and non-case": activeCasesSkips,
		"active cases only return the asked skus":            activeCasesAsked,
		"a cleared measurement falls back to declared dims":  activeCasesCleared,
		"timestamps round trip to the microsecond":           packLevelMicros,
		"an empty sku list returns nothing":                  activeCasesEmpty,
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) { fn(t, newView(t)) })
	}
}

func caseLevel(t *testing.T, sku, gtin string, dims *packlevel.Dims, from time.Time, version int64) packlevel.PackLevel {
	t.Helper()
	l, err := packlevel.New(sku, "CASE", gtin, 12, dims, from, version)
	must(t, err)
	return l
}

func caseDims() *packlevel.Dims {
	return &packlevel.Dims{LengthMM: 400, WidthMM: 300, HeightMM: 200, WeightG: 6500}
}

func saveLevel(t *testing.T, v ports.PackLevelView, l packlevel.PackLevel) {
	t.Helper()
	must(t, v.Save(context.Background(), l))
}

func active(t *testing.T, v ports.PackLevelView, at time.Time, skus ...string) map[slotplan.SKU]planning.CaseSize {
	t.Helper()
	keys := make([]slotplan.SKU, len(skus))
	for i, s := range skus {
		keys[i] = slotplan.SKU(s)
	}
	got, err := v.ActiveCases(context.Background(), keys, at)
	must(t, err)
	return got
}

func packLevelNotFound(t *testing.T, v ports.PackLevelView) {
	_, found, err := v.Get(context.Background(), "SKU-1", packlevel.Case)
	must(t, err)
	if found {
		t.Fatal("an unseen level must not be found")
	}
}

func packLevelRoundTrip(t *testing.T, v ports.PackLevelView) {
	end := T0.Add(48 * time.Hour)
	l := caseLevel(t, "SKU-1", "24006381333935", caseDims(), T0, 3)
	l.Measured = &packlevel.Dims{LengthMM: 410, WidthMM: 310, HeightMM: 210, WeightG: 6900}
	l.EffectiveTo = &end
	saveLevel(t, v, l)
	got, found, err := v.Get(context.Background(), "SKU-1", packlevel.Case)
	must(t, err)
	if !found || got.SKU != "SKU-1" || got.Level != packlevel.Case || got.GTIN != "24006381333935" ||
		got.UnitsPerPack != 12 || got.Version != 3 || !got.EffectiveFrom.Equal(T0) ||
		got.EffectiveTo == nil || !got.EffectiveTo.Equal(end) {
		t.Fatalf("got %+v", got)
	}
	if got.Declared == nil || *got.Declared != *caseDims() || got.Measured == nil || *got.Measured != *l.Measured {
		t.Fatalf("dims = %+v / %+v", got.Declared, got.Measured)
	}
	packLevelRoundTripOpen(t, v)
}

// packLevelRoundTripOpen: a level with no dims and no end date reads back bare.
func packLevelRoundTripOpen(t *testing.T, v ports.PackLevelView) {
	open := caseLevel(t, "SKU-2", "g2", nil, T0, 1)
	saveLevel(t, v, open)
	got, _, err := v.Get(context.Background(), "SKU-2", packlevel.Case)
	must(t, err)
	if got.Declared != nil || got.Measured != nil || got.EffectiveTo != nil {
		t.Fatalf("a level with no dims and no end: %+v", got)
	}
}

func packLevelUpsert(t *testing.T, v ports.PackLevelView) {
	saveLevel(t, v, caseLevel(t, "SKU-1", "g1", caseDims(), T0, 1))
	saveLevel(t, v, caseLevel(t, "SKU-1", "g2", caseDims(), T0, 2))
	each, err := packlevel.New("SKU-1", "EACH", "g0", 1, nil, T0, 1)
	must(t, err)
	saveLevel(t, v, each)
	got, _, err := v.Get(context.Background(), "SKU-1", packlevel.Case)
	must(t, err)
	if got.GTIN != "g2" || got.Version != 2 {
		t.Fatalf("the second save replaces the first: %+v", got)
	}
	if e, found, _ := v.Get(context.Background(), "SKU-1", packlevel.Each); !found || e.GTIN != "g0" {
		t.Fatalf("levels of one SKU are separate rows: %+v %v", e, found)
	}
}

func activeCasesMeasured(t *testing.T, v ports.PackLevelView) {
	l := caseLevel(t, "SKU-1", "g1", caseDims(), T0, 1)
	saveLevel(t, v, l)
	got := active(t, v, T0, "SKU-1")
	want := planning.CaseSize{VolumeMM3: 24_000_000, WeightG: 6500, UnitsPerPack: 12}
	if got["SKU-1"] != want {
		t.Fatalf("declared: %+v, want %+v", got, want)
	}
	l.Measured = &packlevel.Dims{LengthMM: 410, WidthMM: 310, HeightMM: 210, WeightG: 6900}
	l.Version = 2
	saveLevel(t, v, l)
	want = planning.CaseSize{VolumeMM3: 410 * 310 * 210, WeightG: 6900, UnitsPerPack: 12}
	if got := active(t, v, T0, "SKU-1"); got["SKU-1"] != want {
		t.Fatalf("measured: %+v, want %+v", got, want)
	}
}

func activeCasesWindow(t *testing.T, v ports.PackLevelView) {
	end := T0.Add(time.Hour)
	l := caseLevel(t, "SKU-1", "g1", caseDims(), T0, 1)
	l.EffectiveTo = &end
	saveLevel(t, v, l)
	for _, tc := range []struct {
		at   time.Time
		want bool
	}{
		{T0.Add(-time.Microsecond), false}, {T0, true}, {end.Add(-time.Microsecond), true}, {end, false},
	} {
		if _, ok := active(t, v, tc.at, "SKU-1")["SKU-1"]; ok != tc.want {
			t.Errorf("active at %v = %v, want %v", tc.at, ok, tc.want)
		}
	}
}

func activeCasesSkips(t *testing.T, v ports.PackLevelView) {
	saveLevel(t, v, caseLevel(t, "NO-DIMS", "g1", nil, T0, 1))
	pallet, err := packlevel.New("PALLET-ONLY", "PALLET", "g2", 480, caseDims(), T0, 1)
	must(t, err)
	saveLevel(t, v, pallet)
	if got := active(t, v, T0, "NO-DIMS", "PALLET-ONLY"); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func activeCasesAsked(t *testing.T, v ports.PackLevelView) {
	saveLevel(t, v, caseLevel(t, "SKU-1", "g1", caseDims(), T0, 1))
	saveLevel(t, v, caseLevel(t, "SKU-2", "g2", caseDims(), T0, 1))
	got := active(t, v, T0, "SKU-2", "SKU-UNKNOWN")
	if len(got) != 1 || got["SKU-2"].UnitsPerPack != 12 {
		t.Fatalf("got %+v", got)
	}
}

func activeCasesCleared(t *testing.T, v ports.PackLevelView) {
	l := caseLevel(t, "SKU-1", "g1", caseDims(), T0, 1)
	l.Measured = &packlevel.Dims{LengthMM: 1, WidthMM: 1, HeightMM: 1, WeightG: 1}
	saveLevel(t, v, l)
	l.Measured, l.GTIN, l.Version = nil, "g2", 2
	saveLevel(t, v, l)
	if got := active(t, v, T0, "SKU-1"); got["SKU-1"].VolumeMM3 != 24_000_000 {
		t.Fatalf("got %+v", got)
	}
}

func packLevelMicros(t *testing.T, v ports.PackLevelView) {
	from := time.Date(2026, 10, 8, 12, 0, 0, 123456000, time.UTC)
	saveLevel(t, v, caseLevel(t, "SKU-1", "g1", nil, from, 1))
	got, _, err := v.Get(context.Background(), "SKU-1", packlevel.Case)
	must(t, err)
	if !got.EffectiveFrom.Equal(from) {
		t.Fatalf("effective_from = %v, want %v", got.EffectiveFrom, from)
	}
}

func activeCasesEmpty(t *testing.T, v ports.PackLevelView) {
	saveLevel(t, v, caseLevel(t, "SKU-1", "g1", caseDims(), T0, 1))
	if got := active(t, v, T0); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}
