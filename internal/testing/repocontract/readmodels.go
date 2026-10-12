package repocontract

import (
	"context"
	"testing"
	"time"

	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

func line(order string, n int, site, sku string, units int64, due time.Time, active bool) repository.DemandLine {
	return repository.DemandLine{
		SourceOrderID: order, LineNo: n, Site: slotplan.SiteID(site), SKU: slotplan.SKU(sku),
		Units: units, DueAt: due, Active: active,
	}
}

// DemandLedger runs the DemandLedger contract. newLedger must return an
// EMPTY ledger on every call.
func DemandLedger(t *testing.T, newLedger func(t *testing.T) ports.DemandLedger) {
	t.Helper()
	cases := map[string]func(t *testing.T, d ports.DemandLedger){
		"velocity counts active lines and sums units per sku": velocityCounts,
		"the window is half open on due_at":                   velocityWindow,
		"last writer wins per source line":                    lastWriterWins,
		"a removed line stops counting but stays":             removedLines,
		"velocity is per site":                                velocityPerSite,
		"velocity is ordered picks, units, sku":               velocityOrder,
		"an empty ledger has no velocity":                     velocityEmpty,
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) { fn(t, newLedger(t)) })
	}
}

func apply(t *testing.T, d ports.DemandLedger, ls ...repository.DemandLine) {
	t.Helper()
	for _, l := range ls {
		must(t, d.Apply(context.Background(), l))
	}
}

func velocity(t *testing.T, d ports.DemandLedger, site string, from, to time.Time) []planning.SkuVelocity {
	t.Helper()
	got, err := d.Velocity(context.Background(), slotplan.SiteID(site), from, to)
	must(t, err)
	return got
}

func sv(sku string, picks, units int64) planning.SkuVelocity {
	return planning.SkuVelocity{SKU: slotplan.SKU(sku), Picks: picks, Units: units}
}

func velocityCounts(t *testing.T, d ports.DemandLedger) {
	due := T0.Add(-time.Hour)
	apply(t, d,
		line("o1", 1, "S", "A", 5, due, true), line("o1", 2, "S", "A", 7, due, true), line("o2", 1, "S", "B", 2, due, true))
	got := velocity(t, d, "S", T0.Add(-24*time.Hour), T0)
	if !equalSlice(got, []planning.SkuVelocity{sv("A", 2, 12), sv("B", 1, 2)}) {
		t.Fatalf("velocity = %+v", got)
	}
}

func velocityWindow(t *testing.T, d ports.DemandLedger) {
	from, to := T0.Add(-24*time.Hour), T0
	apply(t, d,
		line("o1", 1, "S", "AT-FROM", 1, from, true),
		line("o2", 1, "S", "AT-TO", 1, to, true),
		line("o3", 1, "S", "BEFORE", 1, from.Add(-time.Nanosecond), true),
		line("o4", 1, "S", "INSIDE", 1, to.Add(-time.Nanosecond), true))
	got := velocity(t, d, "S", from, to)
	if !equalSlice(got, []planning.SkuVelocity{sv("AT-FROM", 1, 1), sv("INSIDE", 1, 1)}) {
		t.Fatalf("[from, to) must include from and exclude to: %+v", got)
	}
}

func lastWriterWins(t *testing.T, d ports.DemandLedger) {
	due := T0.Add(-time.Hour)
	apply(t, d, line("o1", 1, "S", "A", 5, due, true), line("o1", 1, "S", "B", 9, due, true))
	got := velocity(t, d, "S", T0.Add(-24*time.Hour), T0)
	if !equalSlice(got, []planning.SkuVelocity{sv("B", 1, 9)}) {
		t.Fatalf("the second message for the same line replaces the first: %+v", got)
	}
}

func removedLines(t *testing.T, d ports.DemandLedger) {
	due := T0.Add(-time.Hour)
	apply(t, d, line("o1", 1, "S", "A", 5, due, true), line("o1", 1, "S", "A", 0, due, false))
	if got := velocity(t, d, "S", T0.Add(-24*time.Hour), T0); len(got) != 0 {
		t.Fatalf("a REMOVED line must not count: %+v", got)
	}
	apply(t, d, line("o1", 1, "S", "A", 5, due, true)) // an active message later reactivates it
	if got := velocity(t, d, "S", T0.Add(-24*time.Hour), T0); !equalSlice(got, []planning.SkuVelocity{sv("A", 1, 5)}) {
		t.Fatalf("last writer wins also for reactivation: %+v", got)
	}
}

func velocityPerSite(t *testing.T, d ports.DemandLedger) {
	due := T0.Add(-time.Hour)
	apply(t, d, line("o1", 1, "S1", "A", 5, due, true), line("o2", 1, "S2", "B", 5, due, true))
	got := velocity(t, d, "S2", T0.Add(-24*time.Hour), T0)
	if !equalSlice(got, []planning.SkuVelocity{sv("B", 1, 5)}) {
		t.Fatalf("velocity = %+v", got)
	}
}

func velocityOrder(t *testing.T, d ports.DemandLedger) {
	due := T0.Add(-time.Hour)
	apply(t, d,
		line("o1", 1, "S", "C", 1, due, true),
		line("o2", 1, "S", "B", 10, due, true), line("o2", 2, "S", "B", 10, due, true),
		line("o3", 1, "S", "A", 10, due, true), line("o3", 2, "S", "A", 10, due, true),
		line("o4", 1, "S", "D", 50, due, true), line("o4", 2, "S", "D", 50, due, true))
	got := velocity(t, d, "S", T0.Add(-24*time.Hour), T0)
	want := []planning.SkuVelocity{sv("D", 2, 100), sv("A", 2, 20), sv("B", 2, 20), sv("C", 1, 1)}
	if !equalSlice(got, want) {
		t.Fatalf("picks desc, units desc, sku asc:\n got %+v\nwant %+v", got, want)
	}
}

func velocityEmpty(t *testing.T, d ports.DemandLedger) {
	if got := velocity(t, d, "S", T0.Add(-24*time.Hour), T0); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

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
	_, err := p.Get(context.Background(), "SKU-1")
	wantErr(t, err, repository.ErrProfileNotFound)
}

func profileRoundTrip(t *testing.T, p ports.ProfileDirectory) {
	ctx := context.Background()
	in := repository.ProductProfile{SKU: "SKU-1", HandlingTags: []string{"Hazmat", "Fragile"}, TemperatureClass: planning.Frozen, Unit: unit(1_920_000, 1500), Version: 3}
	must(t, p.Save(ctx, in))
	got, err := p.Get(ctx, "SKU-1")
	must(t, err)
	if got.SKU != "SKU-1" || got.Version != 3 || got.TemperatureClass != planning.Frozen || !equalSlice(got.HandlingTags, []string{"Hazmat", "Fragile"}) ||
		got.Unit == nil || *got.Unit != *in.Unit {
		t.Fatalf("profile = %+v", got)
	}
}

func profileVersionGuard(t *testing.T, p ports.ProfileDirectory) {
	ctx := context.Background()
	must(t, p.Save(ctx, repository.ProductProfile{SKU: "SKU-1", TemperatureClass: planning.Chilled, Version: 2}))
	must(t, p.Save(ctx, repository.ProductProfile{SKU: "SKU-1", TemperatureClass: planning.Frozen, Version: 2}))
	must(t, p.Save(ctx, repository.ProductProfile{SKU: "SKU-1", TemperatureClass: planning.Frozen, Version: 1}))
	got, err := p.Get(ctx, "SKU-1")
	must(t, err)
	if got.TemperatureClass != planning.Chilled || got.Version != 2 {
		t.Fatalf("an equal or older version must not replace: %+v", got)
	}
	must(t, p.Save(ctx, repository.ProductProfile{SKU: "SKU-1", TemperatureClass: planning.Frozen, Version: 3}))
	got, err = p.Get(ctx, "SKU-1")
	must(t, err)
	if got.TemperatureClass != planning.Frozen || got.Version != 3 {
		t.Fatalf("a newer version replaces: %+v", got)
	}
}

func profileMany(t *testing.T, p ports.ProfileDirectory) {
	ctx := context.Background()
	must(t, p.Save(ctx, repository.ProductProfile{SKU: "SKU-1", HandlingTags: []string{"Hazmat"}, Unit: unit(10, 20), Version: 1}))
	must(t, p.Save(ctx, repository.ProductProfile{SKU: "SKU-2", TemperatureClass: planning.Chilled, Version: 1}))
	got, err := p.Many(ctx, []slotplan.SKU{"SKU-1", "SKU-2", "SKU-404"})
	must(t, err)
	if len(got) != 2 {
		t.Fatalf("unknown skus are absent: %+v", got)
	}
	one := got["SKU-1"]
	if !equalSlice(one.HandlingTags, []string{"Hazmat"}) || one.Unit == nil || *one.Unit != *unit(10, 20) {
		t.Fatalf("SKU-1 = %+v", one)
	}
	if got["SKU-2"].TemperatureClass != planning.Chilled || got["SKU-2"].Unit != nil {
		t.Fatalf("SKU-2 = %+v", got["SKU-2"])
	}
	none, err := p.Many(ctx, nil)
	must(t, err)
	if len(none) != 0 {
		t.Fatalf("many(nil) = %+v", none)
	}
}

func profileNoUnit(t *testing.T, p ports.ProfileDirectory) {
	ctx := context.Background()
	must(t, p.Save(ctx, repository.ProductProfile{SKU: "SKU-1", Unit: unit(5, 5), Version: 1}))
	must(t, p.Save(ctx, repository.ProductProfile{SKU: "SKU-1", Version: 2}))
	got, err := p.Get(ctx, "SKU-1")
	must(t, err)
	if got.Unit != nil || got.Version != 2 {
		t.Fatalf("clearing the dimensions: %+v", got)
	}
}

// SlotCatalogue runs the SlotCatalogue contract over an EMPTY catalogue.
func SlotCatalogue(t *testing.T, newCat func(t *testing.T) ports.SlotCatalogue) {
	t.Helper()
	cases := map[string]func(t *testing.T, c ports.SlotCatalogue){
		"forward slots join the zone attributes":                 catalogueJoin,
		"only active storage slots of forward zones":             catalogueFilters,
		"slots are per site":                                     cataloguePerSite,
		"a slot may arrive before its zone":                      catalogueSlotBeforeZone,
		"decommissioning is irreversible":                        catalogueDecommission,
		"decommissioning an unseen slot blocks its registration": catalogueTombstone,
		"re-registering updates capacity and zone":               catalogueUpsert,
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) { fn(t, newCat(t)) })
	}
}

func zone(id, site, code string, temp planning.TemperatureClass, hazmat bool) repository.Zone {
	return repository.Zone{ID: id, SiteCode: site, AreaCode: "A", ZoneCode: code, TemperatureClass: temp, Hazmat: hazmat}
}

func slot(code, zoneID, role string, kg, m3 float64) repository.Slot {
	return repository.Slot{Code: slotplan.SlotCode(code), ZoneID: zoneID, Role: role, MaxWeightKg: kg, MaxVolumeM3: m3}
}

func forward(t *testing.T, c ports.SlotCatalogue, site string, codes ...string) []planning.Slot {
	t.Helper()
	got, err := c.ForwardSlots(context.Background(), slotplan.SiteID(site), codes)
	must(t, err)
	return got
}

func codesOf(ss []planning.Slot) []slotplan.SlotCode {
	out := make([]slotplan.SlotCode, len(ss))
	for i, s := range ss {
		out[i] = s.Code
	}
	return out
}

func catalogueJoin(t *testing.T, c ports.SlotCatalogue) {
	ctx := context.Background()
	must(t, c.SaveZone(ctx, zone("Z1", "WH1", "FWD", planning.Frozen, true)))
	must(t, c.SaveSlot(ctx, slot("L-1", "Z1", repository.RoleStorage, 40, 0.5)))
	got := forward(t, c, "WH1", "FWD")
	want := planning.Slot{Code: "L-1", ZoneCode: "FWD", TemperatureClass: planning.Frozen, Hazmat: true, MaxWeightKg: 40, MaxVolumeM3: 0.5}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func catalogueFilters(t *testing.T, c ports.SlotCatalogue) {
	ctx := context.Background()
	must(t, c.SaveZone(ctx, zone("Z-FWD", "WH1", "FWD", planning.Ambient, false)))
	must(t, c.SaveZone(ctx, zone("Z-RSV", "WH1", "RSV", planning.Ambient, false)))
	must(t, c.SaveSlot(ctx, slot("L-3", "Z-FWD", repository.RoleStorage, 1, 1)))
	must(t, c.SaveSlot(ctx, slot("L-1", "Z-FWD", repository.RoleStorage, 1, 1)))
	must(t, c.SaveSlot(ctx, slot("L-2", "Z-FWD", "Receiving", 1, 1)))
	must(t, c.SaveSlot(ctx, slot("L-4", "Z-RSV", repository.RoleStorage, 1, 1)))
	must(t, c.SaveSlot(ctx, slot("L-5", "Z-FWD", repository.RoleStorage, 1, 1)))
	must(t, c.Decommission(ctx, "L-5"))

	if got := codesOf(forward(t, c, "WH1", "FWD")); !equalSlice(got, []slotplan.SlotCode{"L-1", "L-3"}) {
		t.Fatalf("FWD storage slots, ordered by code = %v", got)
	}
	if got := codesOf(forward(t, c, "WH1", "FWD", "RSV")); !equalSlice(got, []slotplan.SlotCode{"L-1", "L-3", "L-4"}) {
		t.Fatalf("both zone codes = %v", got)
	}
	if got := forward(t, c, "WH1"); len(got) != 0 {
		t.Fatalf("no zone codes, no forward slots: %v", codesOf(got))
	}
}

func cataloguePerSite(t *testing.T, c ports.SlotCatalogue) {
	ctx := context.Background()
	must(t, c.SaveZone(ctx, zone("Z1", "WH1", "FWD", planning.Ambient, false)))
	must(t, c.SaveZone(ctx, zone("Z2", "WH2", "FWD", planning.Ambient, false)))
	must(t, c.SaveSlot(ctx, slot("L-1", "Z1", repository.RoleStorage, 1, 1)))
	must(t, c.SaveSlot(ctx, slot("L-2", "Z2", repository.RoleStorage, 1, 1)))
	if got := codesOf(forward(t, c, "WH2", "FWD")); !equalSlice(got, []slotplan.SlotCode{"L-2"}) {
		t.Fatalf("got %v", got)
	}
}

func catalogueSlotBeforeZone(t *testing.T, c ports.SlotCatalogue) {
	ctx := context.Background()
	must(t, c.SaveSlot(ctx, slot("L-1", "Z1", repository.RoleStorage, 1, 1)))
	if got := forward(t, c, "WH1", "FWD"); len(got) != 0 {
		t.Fatalf("a slot whose zone is unknown is not plannable yet: %v", codesOf(got))
	}
	must(t, c.SaveZone(ctx, zone("Z1", "WH1", "FWD", planning.Ambient, false)))
	if got := codesOf(forward(t, c, "WH1", "FWD")); !equalSlice(got, []slotplan.SlotCode{"L-1"}) {
		t.Fatalf("convergent once the zone arrives: %v", got)
	}
}

func catalogueDecommission(t *testing.T, c ports.SlotCatalogue) {
	ctx := context.Background()
	must(t, c.SaveZone(ctx, zone("Z1", "WH1", "FWD", planning.Ambient, false)))
	must(t, c.SaveSlot(ctx, slot("L-1", "Z1", repository.RoleStorage, 1, 1)))
	must(t, c.Decommission(ctx, "L-1"))
	must(t, c.SaveSlot(ctx, slot("L-1", "Z1", repository.RoleStorage, 9, 9)))
	if got := forward(t, c, "WH1", "FWD"); len(got) != 0 {
		t.Fatalf("a decommissioned slot must stay retired: %v", codesOf(got))
	}
}

func catalogueTombstone(t *testing.T, c ports.SlotCatalogue) {
	ctx := context.Background()
	must(t, c.SaveZone(ctx, zone("Z1", "WH1", "FWD", planning.Ambient, false)))
	must(t, c.Decommission(ctx, "L-1"))
	must(t, c.SaveSlot(ctx, slot("L-1", "Z1", repository.RoleStorage, 1, 1)))
	if got := forward(t, c, "WH1", "FWD"); len(got) != 0 {
		t.Fatalf("got %v", codesOf(got))
	}
}

func catalogueUpsert(t *testing.T, c ports.SlotCatalogue) {
	ctx := context.Background()
	must(t, c.SaveZone(ctx, zone("Z1", "WH1", "FWD", planning.Ambient, false)))
	must(t, c.SaveZone(ctx, zone("Z2", "WH1", "FWD", planning.Chilled, false)))
	must(t, c.SaveSlot(ctx, slot("L-1", "Z1", repository.RoleStorage, 1, 1)))
	must(t, c.SaveSlot(ctx, slot("L-1", "Z2", repository.RoleStorage, 8, 0.8)))
	got := forward(t, c, "WH1", "FWD")
	if len(got) != 1 || got[0].MaxWeightKg != 8 || got[0].MaxVolumeM3 != 0.8 || got[0].TemperatureClass != planning.Chilled {
		t.Fatalf("got %+v", got)
	}
	must(t, c.SaveZone(ctx, zone("Z2", "WH1", "FWD", planning.Frozen, true)))
	got = forward(t, c, "WH1", "FWD")
	if got[0].TemperatureClass != planning.Frozen || !got[0].Hazmat {
		t.Fatalf("a re-registered zone replaces its attributes: %+v", got[0])
	}
}
