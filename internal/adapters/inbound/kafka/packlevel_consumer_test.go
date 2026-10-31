package kafka_test

import (
	"context"
	"testing"
	"time"

	"github.com/claudioed/slotting-optimization/internal/domain/packlevel"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

var packAt = time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)

func (c *copies) activeCase(t *testing.T) (size int64, found bool) {
	t.Helper()
	got, err := c.packs.ActiveCases(context.Background(), []slotplan.SKU{"SKU-1"}, packAt)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := got["SKU-1"]
	return s.VolumeMM3, ok
}

func TestPackLevel_DeclaredFillsTheViewFromProductMastersPayload(t *testing.T) {
	c := newCopies()
	handle(t, c.packLevelConsumer(), packLevelMsg("e1", "ProductPackLevelDeclared", "SKU-1", caseDeclaredData))
	l, found, err := c.packs.Get(context.Background(), "SKU-1", packlevel.Case)
	if err != nil || !found {
		t.Fatalf("found %v err %v", found, err)
	}
	if l.GTIN != "24006381333935" || l.UnitsPerPack != 12 || l.Version != 3 || l.Declared == nil ||
		*l.Declared != (packlevel.Dims{LengthMM: 400, WidthMM: 300, HeightMM: 200, WeightG: 6500}) {
		t.Fatalf("level = %+v", l)
	}
	if vol, ok := c.activeCase(t); !ok || vol != 24_000_000 {
		t.Fatalf("active case volume = %d %v", vol, ok)
	}
}

func TestPackLevel_DeclaredWithoutDimsIsStoredButHasNoCaseSize(t *testing.T) {
	c := newCopies()
	data := `{"sku":"SKU-1","level":"CASE","gtin":"24006381333935","units_per_pack":12,"is_orderable":true,"is_despatch_unit":true,"effective_from":"2026-10-06T20:00:00Z","version":1}`
	handle(t, c.packLevelConsumer(), packLevelMsg("e1", "ProductPackLevelDeclared", "SKU-1", data))
	if _, found, _ := c.packs.Get(context.Background(), "SKU-1", packlevel.Case); !found {
		t.Fatal("the level is stored")
	}
	if _, ok := c.activeCase(t); ok {
		t.Fatal("no dims, no case size")
	}
}

func TestPackLevel_MeasuredReplacesTheDeclaredSizeAndRetiredEndsIt(t *testing.T) {
	c := newCopies()
	consumer := c.packLevelConsumer()
	handle(t, consumer, packLevelMsg("e1", "ProductPackLevelDeclared", "SKU-1", caseDeclaredData))
	handle(t, consumer, packLevelMsg("e2", "ProductPackLevelMeasured", "SKU-1",
		`{"sku":"SKU-1","level":"CASE","gtin":"24006381333935","dims":{"length_mm":410,"width_mm":310,"height_mm":210,"weight_g":6900},"measured_at":"2026-10-06T14:05:00Z","device_id":"CUBISCAN-03","version":5}`))
	if vol, ok := c.activeCase(t); !ok || vol != 410*310*210 {
		t.Fatalf("measured volume = %d %v", vol, ok)
	}
	handle(t, consumer, packLevelMsg("e3", "ProductPackLevelRetired", "SKU-1",
		`{"sku":"SKU-1","level":"CASE","gtin":"24006381333935","effective_to":"2026-10-06T21:01:00Z","reason":"superseded","version":6}`))
	if _, ok := c.activeCase(t); ok {
		t.Fatal("a retired level has no case size")
	}
}

func TestPackLevel_SupersededGTINIsReplacedAndALateRetirementOfTheOldOneIsIgnored(t *testing.T) {
	c := newCopies()
	consumer := c.packLevelConsumer()
	handle(t, consumer, packLevelMsg("e1", "ProductPackLevelDeclared", "SKU-1", caseDeclaredData))
	next := `{"sku":"SKU-1","level":"CASE","gtin":"34006381333932","units_per_pack":24,"dims":{"length_mm":500,"width_mm":300,"height_mm":200,"weight_g":12000},"is_orderable":true,"is_despatch_unit":true,"effective_from":"2026-10-06T21:01:00Z","version":5}`
	handle(t, consumer, packLevelMsg("e2", "ProductPackLevelDeclared", "SKU-1", next))
	handle(t, consumer, packLevelMsg("e3", "ProductPackLevelRetired", "SKU-1",
		`{"sku":"SKU-1","level":"CASE","gtin":"24006381333935","effective_to":"2026-10-06T21:01:00Z","reason":"superseded","version":4}`))
	l, _, _ := c.packs.Get(context.Background(), "SKU-1", packlevel.Case)
	if l.GTIN != "34006381333932" || l.UnitsPerPack != 24 || l.EffectiveTo != nil {
		t.Fatalf("the new GTIN stays in force: %+v", l)
	}
}

func TestPackLevel_AnEventIdIsAppliedOnceAndOlderVersionsAreStale(t *testing.T) {
	c := newCopies()
	consumer := c.packLevelConsumer()
	m := packLevelMsg("e1", "ProductPackLevelDeclared", "SKU-1", caseDeclaredData)
	handle(t, consumer, m)
	handle(t, consumer, m)
	older := `{"sku":"SKU-1","level":"CASE","gtin":"99999999999999","units_per_pack":6,"dims":{"length_mm":1,"width_mm":1,"height_mm":1,"weight_g":1},"effective_from":"2026-10-06T20:00:00Z","version":2}`
	handle(t, consumer, packLevelMsg("e2", "ProductPackLevelDeclared", "SKU-1", older))
	if l, _, _ := c.packs.Get(context.Background(), "SKU-1", packlevel.Case); l.GTIN != "24006381333935" || l.Version != 3 {
		t.Fatalf("an older version must not replace the level: %+v", l)
	}
}

func TestPackLevel_IgnoresOtherTypesAndSkipsPayloadsThatCanNeverBeApplied(t *testing.T) {
	c := newCopies()
	consumer := c.packLevelConsumer()
	for name, msg := range map[string][]byte{
		"another product-master type": classifiedMsg("e1", "SKU-1", `{"sku":"SKU-1","handling_tags":[],"version":1}`),
		"payload of the wrong type":   packLevelMsg("e2", "ProductPackLevelDeclared", "SKU-1", `{"sku":7}`),
		"unknown level":               packLevelMsg("e3", "ProductPackLevelDeclared", "SKU-1", `{"sku":"SKU-1","level":"BOX","gtin":"g","units_per_pack":2,"effective_from":"2026-10-06T20:00:00Z","version":1}`),
		"zero units":                  packLevelMsg("e4", "ProductPackLevelDeclared", "SKU-1", `{"sku":"SKU-1","level":"CASE","gtin":"g","units_per_pack":0,"effective_from":"2026-10-06T20:00:00Z","version":1}`),
		"retired without gtin":        packLevelMsg("e5", "ProductPackLevelRetired", "SKU-1", `{"sku":"SKU-1","level":"CASE","effective_to":"2026-10-06T20:00:00Z","version":2}`),
		"measured with zero dims":     packLevelMsg("e6", "ProductPackLevelMeasured", "SKU-1", `{"sku":"SKU-1","level":"CASE","gtin":"g","dims":{},"version":2}`),
		"measured of an unseen level": packLevelMsg("e7", "ProductPackLevelMeasured", "SKU-1", `{"sku":"SKU-1","level":"CASE","gtin":"g","dims":{"length_mm":1,"width_mm":1,"height_mm":1,"weight_g":1},"version":2}`),
		"retired of an unseen level":  packLevelMsg("e8", "ProductPackLevelRetired", "SKU-1", `{"sku":"SKU-1","level":"CASE","gtin":"g","effective_to":"2026-10-06T20:00:00Z","reason":"x","version":2}`),
	} {
		handle(t, consumer, msg)
		if _, found, _ := c.packs.Get(context.Background(), "SKU-1", packlevel.Case); found {
			t.Fatalf("%s must not create a level", name)
		}
	}
}
