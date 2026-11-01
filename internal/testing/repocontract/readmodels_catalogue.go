package repocontract

import (
	"context"
	"testing"

	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

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
