package kafka_test

import (
	"context"
	"errors"
	"testing"

	kafkaconsumer "github.com/claudioed/slotting-optimization/internal/adapters/inbound/kafka"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

func handle(t *testing.T, c *kafkaconsumer.Consumer, msg []byte) {
	t.Helper()
	if err := c.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("HandleMessage = %v, want nil", err)
	}
}

func TestDemand_ActiveLineCountsAndLastWriterWins(t *testing.T) {
	c := newCopies()
	consumer := c.demandConsumer("")
	handle(t, consumer, demandMsg("e1", "ord-1", 1, "SITE-1", "SKU-1", 12, "ACTIVE"))
	handle(t, consumer, demandMsg("e2", "ord-1", 2, "SITE-1", "SKU-1", 3, "ACTIVE"))
	got := c.velocity(t, "SITE-1")
	if len(got) != 1 || got[0] != (planning.SkuVelocity{SKU: "SKU-1", Picks: 2, Units: 15}) {
		t.Fatalf("velocity = %+v", got)
	}
	handle(t, consumer, demandMsg("e3", "ord-1", 1, "SITE-1", "SKU-2", 5, "ACTIVE")) // the line changed SKU
	got = c.velocity(t, "SITE-1")
	if len(got) != 2 || got[0].SKU != "SKU-2" || got[1].SKU != "SKU-1" || got[1].Picks != 1 {
		t.Fatalf("last writer wins per line: %+v", got)
	}
}

func TestDemand_RemovedDeactivatesTheLine(t *testing.T) {
	c := newCopies()
	consumer := c.demandConsumer("")
	handle(t, consumer, demandMsg("e1", "ord-1", 1, "SITE-1", "SKU-1", 12, "ACTIVE"))
	handle(t, consumer, demandMsg("e2", "ord-1", 1, "SITE-1", "SKU-1", 12, "REMOVED"))
	if got := c.velocity(t, "SITE-1"); len(got) != 0 {
		t.Fatalf("a REMOVED line must not count: %+v", got)
	}
}

func TestDemand_AnEventIdIsAppliedOnce(t *testing.T) {
	c := newCopies()
	consumer := c.demandConsumer("")
	m := demandMsg("e1", "ord-1", 1, "SITE-1", "SKU-1", 12, "ACTIVE")
	handle(t, consumer, m)
	handle(t, consumer, m)
	if c.demand.callCount() != 1 {
		t.Fatalf("the redelivered event was applied %d times", c.demand.callCount())
	}
}

func TestDemand_OnlyTheConfiguredSiteIsKept(t *testing.T) {
	c := newCopies()
	consumer := c.demandConsumer("SITE-1")
	handle(t, consumer, demandMsg("e1", "ord-1", 1, "SITE-2", "SKU-1", 12, "ACTIVE"))
	handle(t, consumer, demandMsg("e2", "ord-2", 1, "SITE-1", "SKU-2", 1, "ACTIVE"))
	if len(c.velocity(t, "SITE-2")) != 0 || len(c.velocity(t, "SITE-1")) != 1 {
		t.Fatal("only SITE-1 may be stored when it is configured")
	}
}

func TestConsumers_SkipEverythingDeterministicWithoutTouchingTheDatabase(t *testing.T) {
	c := newCopies()
	consumer := c.demandConsumer("")
	other := ce("e9", "/warehouse/order-management", "com.warehouse.wes.order-management.order.OrderPlaced", "ord-1", "urn:x", `{}`)
	cases := map[string][]byte{
		"legacy flat envelope":      []byte(legacyFlat),
		"not json":                  []byte("not json"),
		"empty value":               nil,
		"unknown type":              other,
		"wrong specversion":         []byte(`{"specversion":"0.3","id":"e1","source":"/s","type":"t","data":{}}`),
		"payload of the wrong type": ce("e1", "/warehouse/order-management", kafkaconsumer.TypeSiteSkuDemandChanged, "o/line/1", "urn:x", `{"source_order_id":7}`),
		"zero units on ACTIVE":      demandMsg("e1", "ord-1", 1, "SITE-1", "SKU-1", 0, "ACTIVE"),
		"unknown state":             demandMsg("e1", "ord-1", 1, "SITE-1", "SKU-1", 1, "PAUSED"),
		"invalid sku":               demandMsg("e1", "ord-1", 1, "SITE-1", "a b", 1, "ACTIVE"),
		"line zero":                 demandMsg("e1", "ord-1", 0, "SITE-1", "SKU-1", 1, "ACTIVE"),
	}
	for name, msg := range cases {
		t.Run(name, func(t *testing.T) {
			handle(t, consumer, msg)
			if c.uow.count() != 0 {
				t.Fatal("a deterministic bad message must never open a unit of work")
			}
		})
	}
	if len(c.velocity(t, "SITE-1")) != 0 {
		t.Fatal("nothing may be stored")
	}
}

func TestDemand_ATransientFailureRollsBackAndTheRetryApplies(t *testing.T) {
	c := newCopies()
	c.demand.fail = 1
	consumer := c.demandConsumer("")
	m := demandMsg("e1", "ord-1", 1, "SITE-1", "SKU-1", 12, "ACTIVE")
	if err := consumer.HandleMessage(context.Background(), m); !errors.Is(err, errInjected) {
		t.Fatalf("HandleMessage = %v, want the transient error", err)
	}
	if len(c.velocity(t, "SITE-1")) != 0 {
		t.Fatal("the half-done effect must be rolled back")
	}
	if c.processed.Has("slotting-demand", "e1") {
		t.Fatal("the claim must be rolled back with the effect, or the retry would be skipped")
	}
	handle(t, consumer, m)
	if got := c.velocity(t, "SITE-1"); len(got) != 1 || got[0].Picks != 1 {
		t.Fatalf("the retry must apply: %+v", got)
	}
}

const (
	classifiedHazmat = `{"sku":"SKU-9","handling_tags":["Hazmat","TemperatureSensitive"],"temperature_class":"Frozen","dot_hazard_class":3,"classification_source":"native","version":3}`
	declared         = `{"sku":"SKU-9","declared":{"length_mm":200,"width_mm":120,"height_mm":80,"weight_g":1500,"volume_mm3":1920000},"effective":{"length_mm":200,"width_mm":120,"height_mm":80,"weight_g":1500,"volume_mm3":1920000},"effective_source":"declared","discrepancy":false,"version":4}`
	measured         = `{"sku":"SKU-9","declared":{"length_mm":200,"width_mm":120,"height_mm":80,"weight_g":1500,"volume_mm3":1920000},"measured":{"length_mm":205,"width_mm":121,"height_mm":82,"weight_g":1720,"volume_mm3":2034010,"measured_at":"2026-10-06T14:05:00Z","device_id":"CUBISCAN-03"},"effective":{"length_mm":205,"width_mm":121,"height_mm":82,"weight_g":1720,"volume_mm3":2034010},"effective_source":"measured","discrepancy":true,"version":5}`
	typeDeclared     = kafkaconsumer.TypeProductDimensionsDeclared
	typeMeasured     = kafkaconsumer.TypeProductMeasured
)

func TestProduct_ClassificationAndEffectivePhysicalProfile(t *testing.T) {
	c := newCopies()
	consumer := c.productConsumer()
	handle(t, consumer, classifiedMsg("e1", "SKU-9", classifiedHazmat))
	handle(t, consumer, physicalMsg("e2", typeDeclared, "SKU-9", declared))
	p, err := c.profiles.Get(context.Background(), "SKU-9")
	if err != nil {
		t.Fatal(err)
	}
	if p.Version != 4 || p.TemperatureClass != planning.Frozen || len(p.HandlingTags) != 2 || p.HandlingTags[0] != "Hazmat" || p.Unit == nil || p.Unit.VolumeMM3 != 1920000 || p.Unit.WeightG != 1500 {
		t.Fatalf("profile = %+v", p)
	}
	handle(t, consumer, physicalMsg("e3", typeMeasured, "SKU-9", measured))
	p, _ = c.profiles.Get(context.Background(), "SKU-9")
	if p.Version != 5 || p.Unit.VolumeMM3 != 2034010 || p.Unit.WeightG != 1720 || p.TemperatureClass != planning.Frozen {
		t.Fatalf("a measurement replaces the physical concern only: %+v", p)
	}
}

func TestProduct_StaleAndNoneAndRedelivery(t *testing.T) {
	c := newCopies()
	consumer := c.productConsumer()
	handle(t, consumer, physicalMsg("e1", typeMeasured, "SKU-9", measured)) // version 5
	handle(t, consumer, physicalMsg("e2", typeDeclared, "SKU-9", declared)) // version 4: stale
	p, _ := c.profiles.Get(context.Background(), "SKU-9")
	if p.Version != 5 || p.Unit.WeightG != 1720 {
		t.Fatalf("an older version must not replace a newer one: %+v", p)
	}
	none := `{"sku":"SKU-9","effective_source":"none","discrepancy":false,"version":6}`
	handle(t, consumer, physicalMsg("e3", typeDeclared, "SKU-9", none))
	p, _ = c.profiles.Get(context.Background(), "SKU-9")
	if p.Version != 6 || p.Unit != nil {
		t.Fatalf("a profile with no effective dimensions clears the unit: %+v", p)
	}
	before := c.uow.count()
	handle(t, consumer, physicalMsg("e3", typeDeclared, "SKU-9", none))
	if c.uow.count() != before+1 {
		t.Fatal("a redelivery still runs one unit of work (to find the claim)")
	}
}

func TestProduct_DeterministicProblemsAreSkipped(t *testing.T) {
	c := newCopies()
	consumer := c.productConsumer()
	for name, msg := range map[string][]byte{
		"bad temperature class":  classifiedMsg("e1", "SKU-9", `{"sku":"SKU-9","handling_tags":[],"temperature_class":"Lukewarm","version":1}`),
		"version zero":           classifiedMsg("e2", "SKU-9", `{"sku":"SKU-9","handling_tags":[],"version":0}`),
		"tags of the wrong type": classifiedMsg("e3", "SKU-9", `{"sku":"SKU-9","handling_tags":"Hazmat","version":2}`),
		"zero effective volume":  physicalMsg("e4", typeMeasured, "SKU-9", `{"sku":"SKU-9","effective":{"volume_mm3":0,"weight_g":5},"effective_source":"measured","version":2}`),
		"legacy flat":            []byte(legacyFlat),
	} {
		t.Run(name, func(t *testing.T) { handle(t, consumer, msg) })
	}
	if _, err := c.profiles.Get(context.Background(), "SKU-9"); err == nil {
		t.Fatal("nothing may be stored")
	}
}

func TestLayout_ZonesSlotsAndDecommissioning(t *testing.T) {
	c := newCopies()
	consumer := c.layoutConsumer()
	handle(t, consumer, zoneMsg("z1", "WH1-FWD-AMB", `{"eventName":"ZoneRegistered","eventType":"x","occurredAt":"2026-09-01T08:00:00Z","zoneId":"WH1-FWD-AMB","siteCode":"WH1","areaCode":"FWD","zoneCode":"FWD","temperatureClass":"Ambient","hazmat":false}`))
	const reg = "com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered"
	const dec = "com.warehouse.wms.facility-layout.locationslot.LocationSlotDecommissioned"
	slot := func(code string) string {
		return `{"eventName":"LocationSlotRegistered","locationCode":"` + code + `","aisleId":"A1","zoneId":"WH1-FWD-AMB","locationType":"ShelfBin","maxWeightKg":40,"maxVolumeM3":0.5}`
	}
	handle(t, consumer, slotMsg("s1", reg, "L-1", slot("L-1")))
	handle(t, consumer, slotMsg("s2", reg, "L-2", slot("L-2")))
	handle(t, consumer, slotMsg("s3", reg, "L-3", `{"locationCode":"L-3","zoneId":"WH1-FWD-AMB","role":"Receiving","maxWeightKg":40,"maxVolumeM3":0.5}`))
	slots, err := c.catalogue.ForwardSlots(context.Background(), "WH1", []string{"FWD"})
	if err != nil || len(slots) != 2 || slots[0].Code != "L-1" || slots[0].MaxVolumeM3 != 0.5 || slots[0].TemperatureClass != planning.Ambient {
		t.Fatalf("slots = %+v, %v (an absent role is Storage; Receiving is not forward)", slots, err)
	}
	handle(t, consumer, slotMsg("d1", dec, "L-1", `{"locationCode":"L-1"}`))
	slots, _ = c.catalogue.ForwardSlots(context.Background(), "WH1", []string{"FWD"})
	if len(slots) != 1 || slots[0].Code != slotplan.SlotCode("L-2") {
		t.Fatalf("slots after decommission = %+v", slots)
	}
}

func TestLayout_DeterministicProblemsAreSkipped(t *testing.T) {
	c := newCopies()
	consumer := c.layoutConsumer()
	const reg = "com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered"
	for name, msg := range map[string][]byte{
		"zone without a code":       zoneMsg("z1", "Z", `{"zoneId":"Z","siteCode":"WH1","temperatureClass":"Ambient"}`),
		"zone with a bad temp":      zoneMsg("z2", "Z", `{"zoneId":"Z","siteCode":"WH1","zoneCode":"FWD","temperatureClass":"Hot"}`),
		"slot without a zone":       slotMsg("s1", reg, "L", `{"locationCode":"L"}`),
		"slot with negative cap":    slotMsg("s2", reg, "L", `{"locationCode":"L","zoneId":"Z","maxWeightKg":-1}`),
		"slot payload wrong type":   slotMsg("s3", reg, "L", `{"locationCode":5}`),
		"decommission without code": slotMsg("d1", "com.warehouse.wms.facility-layout.locationslot.LocationSlotDecommissioned", "L", `{}`),
	} {
		t.Run(name, func(t *testing.T) { handle(t, consumer, msg) })
	}
	if c.uow.count() != 0 {
		t.Fatal("a deterministic bad message must never open a unit of work")
	}
}
