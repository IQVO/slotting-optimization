package repocontract

import (
	"context"
	"testing"

	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// ProfileDirectory runs the ProfileDirectory contract over an EMPTY directory.
func ProfileDirectory(t *testing.T, newDir func(t *testing.T) ports.ProfileDirectory) {
	t.Helper()
	cases := map[string]func(t *testing.T, p ports.ProfileDirectory){
		"unknown sku is not found":                    profileNotFound,
		"save then get round trips every field":       profileRoundTrip,
		"a newer version replaces, an older does not": profileVersionGuard,
		"many returns the planner view of known skus": profileMany,
		"a profile without dimensions has no unit":    profileNoUnit,
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) { fn(t, newDir(t)) })
	}
}

func unit(vol, w int64) *planning.UnitSize { return &planning.UnitSize{VolumeMM3: vol, WeightG: w} }

func profileNotFound(t *testing.T, p ports.ProfileDirectory) {
	_, err := p.Get(context.Background(), sku1)
	wantErr(t, err, repository.ErrProfileNotFound)
}

func profileRoundTrip(t *testing.T, p ports.ProfileDirectory) {
	ctx := context.Background()
	in := repository.ProductProfile{SKU: sku1, HandlingTags: []string{hazmat, "Fragile"}, TemperatureClass: planning.Frozen, Unit: unit(1_920_000, 1500), Version: 3}
	must(t, p.Save(ctx, in))
	got, err := p.Get(ctx, sku1)
	must(t, err)
	if got.SKU != sku1 || got.Version != 3 || got.TemperatureClass != planning.Frozen || !equalSlice(got.HandlingTags, []string{hazmat, "Fragile"}) ||
		got.Unit == nil || *got.Unit != *in.Unit {
		t.Fatalf("profile = %+v", got)
	}
}

func profileVersionGuard(t *testing.T, p ports.ProfileDirectory) {
	ctx := context.Background()
	must(t, p.Save(ctx, repository.ProductProfile{SKU: sku1, TemperatureClass: planning.Chilled, Version: 2}))
	must(t, p.Save(ctx, repository.ProductProfile{SKU: sku1, TemperatureClass: planning.Frozen, Version: 2}))
	must(t, p.Save(ctx, repository.ProductProfile{SKU: sku1, TemperatureClass: planning.Frozen, Version: 1}))
	got, err := p.Get(ctx, sku1)
	must(t, err)
	if got.TemperatureClass != planning.Chilled || got.Version != 2 {
		t.Fatalf("an equal or older version must not replace: %+v", got)
	}
	must(t, p.Save(ctx, repository.ProductProfile{SKU: sku1, TemperatureClass: planning.Frozen, Version: 3}))
	got, err = p.Get(ctx, sku1)
	must(t, err)
	if got.TemperatureClass != planning.Frozen || got.Version != 3 {
		t.Fatalf("a newer version replaces: %+v", got)
	}
}

func profileMany(t *testing.T, p ports.ProfileDirectory) {
	ctx := context.Background()
	must(t, p.Save(ctx, repository.ProductProfile{SKU: sku1, HandlingTags: []string{hazmat}, Unit: unit(10, 20), Version: 1}))
	must(t, p.Save(ctx, repository.ProductProfile{SKU: sku2, TemperatureClass: planning.Chilled, Version: 1}))
	got, err := p.Many(ctx, []slotplan.SKU{sku1, sku2, "SKU-404"})
	must(t, err)
	if len(got) != 2 {
		t.Fatalf("unknown skus are absent: %+v", got)
	}
	one := got[sku1]
	if !equalSlice(one.HandlingTags, []string{hazmat}) || one.Unit == nil || *one.Unit != *unit(10, 20) {
		t.Fatalf("SKU-1 = %+v", one)
	}
	if got[sku2].TemperatureClass != planning.Chilled || got[sku2].Unit != nil {
		t.Fatalf("SKU-2 = %+v", got[sku2])
	}
	none, err := p.Many(ctx, nil)
	must(t, err)
	if len(none) != 0 {
		t.Fatalf("many(nil) = %+v", none)
	}
}

func profileNoUnit(t *testing.T, p ports.ProfileDirectory) {
	ctx := context.Background()
	must(t, p.Save(ctx, repository.ProductProfile{SKU: sku1, Unit: unit(5, 5), Version: 1}))
	must(t, p.Save(ctx, repository.ProductProfile{SKU: sku1, Version: 2}))
	got, err := p.Get(ctx, sku1)
	must(t, err)
	if got.Unit != nil || got.Version != 2 {
		t.Fatalf("clearing the dimensions: %+v", got)
	}
}
