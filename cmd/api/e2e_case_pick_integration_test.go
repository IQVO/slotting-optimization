//go:build integration

package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

// The case-pick end-to-end proof (ADR 0012): the real entry point on
// testcontainers Postgres and Kafka, fed ONLY through the input topics (a
// product-master ProductPackLevelDeclared + Measured for the CASE level, demand
// in whole cases, a layout with an each-pick zone and a case-pick zone) and
// driven through REST. A SKU demanded in whole cases lands on the case-pick
// face with a capacity in cases computed from the CASE cube; a SKU demanded in
// eaches lands on the each-pick face; the approved plan's event carries
// pick_unit/cases_capacity for the first and the byte-identical shape for the
// second.
func TestCasePickFacesOnRealPostgresAndKafka(t *testing.T) {
	pg, dbURL := startPostgres(t)
	kc, brokers := startKafka(t)
	_, _ = pg, kc
	for _, topic := range []string{topicDemand, topicProducts, topicFacility, topicInventory} {
		createTopic(t, brokers, topic)
	}
	db := newPGView(t, dbURL)
	e := env{dbURL: dbURL, brokers: brokers, addr: freeAddr(t), casePickZones: "CASEPICK"}
	s := e.start(t)

	const demand = "com.warehouse.wes.order-management.siteskudemand.SiteSkuDemandChanged"
	pm, fl := "/warehouse/product-master", "/warehouse/facility-layout"
	lt := "com.warehouse.wms.facility-layout."
	pt := "com.warehouse.wms.product-master.product."
	// SKU-C: two lines of 12 and 24 eaches = whole cases of 12. SKU-E: 3 eaches.
	produce(t, brokers, topicDemand,
		kafkago.Message{Key: []byte("ord-c1"), Value: inputEvent(t, "dem-c1", "/warehouse/order-management", demand, "ord-c1/line/1", demandData("ord-c1", "SKU-C", 12))},
		kafkago.Message{Key: []byte("ord-c2"), Value: inputEvent(t, "dem-c2", "/warehouse/order-management", demand, "ord-c2/line/1", demandData("ord-c2", "SKU-C", 24))},
		kafkago.Message{Key: []byte("ord-e1"), Value: inputEvent(t, "dem-e1", "/warehouse/order-management", demand, "ord-e1/line/1", demandData("ord-e1", "SKU-E", 3))})
	for i, sku := range []string{"SKU-C", "SKU-E"} {
		produce(t, brokers, topicProducts, kafkago.Message{Key: []byte(sku), Value: inputEvent(t, fmt.Sprintf("prod-%d", i), pm, pt+"ProductMeasured", sku, measuredData(sku))})
	}
	caseData := `{"sku":"SKU-C","level":"CASE","gtin":"24006381333935","child_gtin":"04006381333931","qty_of_child":12,"units_per_pack":12,` +
		`"dims":{"length_mm":400,"width_mm":300,"height_mm":200,"weight_g":6500},"is_orderable":true,"is_despatch_unit":true,` +
		`"effective_from":"2026-01-01T00:00:00Z","version":3}`
	measuredCase := `{"sku":"SKU-C","level":"CASE","gtin":"24006381333935","dims":{"length_mm":400,"width_mm":250,"height_mm":200,"weight_g":6900},` +
		`"measured_at":"2026-10-06T14:05:00Z","device_id":"CUBISCAN-03","version":4}`
	produce(t, brokers, topicProducts,
		kafkago.Message{Key: []byte("SKU-C"), Value: inputEvent(t, "pack-1", pm, pt+"ProductPackLevelDeclared", "SKU-C", caseData)},
		kafkago.Message{Key: []byte("SKU-C"), Value: inputEvent(t, "pack-2", pm, pt+"ProductPackLevelMeasured", "SKU-C", measuredCase)})
	produce(t, brokers, topicFacility,
		kafkago.Message{Key: []byte("WH1-FWD-AMB"), Value: inputEvent(t, "zone-1", fl, lt+"zone.ZoneRegistered", "WH1-FWD-AMB", `{"zoneId":"WH1-FWD-AMB","siteCode":"WH1","zoneCode":"FWD","temperatureClass":"Ambient"}`)},
		kafkago.Message{Key: []byte("WH1-CASE-AMB"), Value: inputEvent(t, "zone-2", fl, lt+"zone.ZoneRegistered", "WH1-CASE-AMB", `{"zoneId":"WH1-CASE-AMB","siteCode":"WH1","zoneCode":"CASEPICK","temperatureClass":"Ambient"}`)},
		kafkago.Message{Key: []byte("L-1"), Value: inputEvent(t, "slot-1", fl, lt+"locationslot.LocationSlotRegistered", "L-1", `{"locationCode":"L-1","zoneId":"WH1-FWD-AMB","maxWeightKg":40,"maxVolumeM3":0.5}`)},
		// 0.1 m3 / 100 kg: measured case = 400*250*200 = 20,000,000 mm3 -> 5 by volume, 14 by weight.
		kafkago.Message{Key: []byte("C-1"), Value: inputEvent(t, "slot-2", fl, lt+"locationslot.LocationSlotRegistered", "C-1", `{"locationCode":"C-1","zoneId":"WH1-CASE-AMB","maxWeightKg":100,"maxVolumeM3":0.1}`)})

	waitFor(t, 90*time.Second, "demand, product, pack-level and layout copies to be fed from Kafka", func() bool {
		return db.count(`SELECT count(*) FROM demand_lines`) == 3 && db.count(`SELECT count(*) FROM product_profiles`) == 2 &&
			db.count(`SELECT count(*) FROM pack_levels WHERE measured_length_mm IS NOT NULL`) == 1 &&
			db.count(`SELECT count(*) FROM zones`) == 2 && db.count(`SELECT count(*) FROM slots`) == 2
	})
	if n := db.count(`SELECT count(*) FROM processed_events WHERE consumer = 'slotting-pack-level'`); n != 2 {
		t.Fatalf("pack-level events claimed = %d, want 2", n)
	}

	body := s.call(http.MethodPost, "/slot-plans", `{}`, "gen-case", http.StatusCreated)
	var plan struct {
		PlanID      string `json:"planId"`
		Assignments []struct {
			SKU           string `json:"sku"`
			Slot          string `json:"slot"`
			PickUnit      string `json:"pickUnit"`
			CasesCapacity int64  `json:"casesCapacity"`
		} `json:"assignments"`
	}
	mustDecode(t, body, &plan)
	got := map[string][3]string{}
	for _, a := range plan.Assignments {
		got[a.SKU] = [3]string{a.Slot, a.PickUnit, fmt.Sprint(a.CasesCapacity)}
	}
	if len(plan.Assignments) != 2 || got["SKU-C"] != [3]string{"C-1", "CASE", "5"} || got["SKU-E"] != [3]string{"L-1", "EACH", "0"} {
		t.Fatalf("plan = %s\nwant SKU-C on C-1 as CASE with 5 cases (from the MEASURED case cube) and SKU-E on L-1 as EACH", body)
	}
	if strings.Contains(body, `"casesCapacity":0`) {
		t.Fatalf("casesCapacity must be absent for an EACH assignment: %s", body)
	}

	s.call(http.MethodPost, "/slot-plans/"+plan.PlanID+"/approvals", "", "ap-cp", http.StatusOK)
	fwd := s.call(http.MethodGet, "/forward-slots", "", "", http.StatusOK)
	if !strings.Contains(fwd, `"pickUnit":"CASE"`) || !strings.Contains(fwd, `"casesCapacity":5`) {
		t.Fatalf("GET /forward-slots = %s", fwd)
	}
	waitFor(t, 60*time.Second, "the relay to publish the outbox", func() bool {
		return db.count(`SELECT count(*) FROM outbox_events WHERE published_at IS NULL`) == 0
	})
	msgs := consume(t, brokers, topicEvents, 2)
	approved := decodeEnvelope(t, msgs[1])
	if !strings.HasSuffix(approved.Type, ".SlotPlanApproved") {
		t.Fatalf("second event = %s", approved.Type)
	}
	data := string(approved.Data)
	if !strings.Contains(data, `{"sku":"SKU-C","slot":"C-1","pick_unit":"CASE","cases_capacity":5}`) ||
		!strings.Contains(data, `{"sku":"SKU-E","slot":"L-1"}`) {
		t.Fatalf("SlotPlanApproved data = %s", data)
	}
}
