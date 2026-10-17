package kafka

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/slotting-optimization/internal/application/outbox"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// analyticsEnvelope is envelope() with the analytics dataschema.
func analyticsEnvelope(eventName, timeStr, data string) string {
	return `{"specversion":"1.0","id":"` + goldenID + `","source":"/warehouse/slotting-optimization",` +
		`"type":"com.warehouse.wms.slotting-optimization.slotplan.` + eventName + `","subject":"` + planID + `",` +
		`"datacontenttype":"application/json","dataschema":"urn:warehouse:slotting-optimization:analytics:` + eventName + `:v1",` +
		`"time":"` + timeStr + `","data":` + data + `}`
}

// TestAnalyticsEncoder_GoldenWireFormat pins the exact analytics bytes: the
// SAME payload as the integration stream, only the dataschema stream segment
// and the topic differ (ADR 0006 section 2).
func TestAnalyticsEncoder_GoldenWireFormat(t *testing.T) {
	cases := []struct {
		name  string
		event slotplan.Event
		want  string
	}{
		{
			"SlotPlanGenerated",
			slotplan.SlotPlanGenerated{
				Header: header(1, generated),
				Window: slotplan.Window{From: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC), To: generated},
				Policy: slotplan.PolicyABCVelocityV1, AssignmentCount: 2, MoveCount: 3, UnassignedCount: 1,
			},
			analyticsEnvelope("SlotPlanGenerated", "2026-10-08T12:00:00Z", `{"plan_id":"`+planID+`","site_id":"SITE-1",`+
				`"window_from":"2026-09-10T12:00:00Z","window_to":"2026-10-08T12:00:00Z","policy":"abc-velocity-v1",`+
				`"assignment_count":2,"move_count":3,"unassigned_count":1}`),
		},
		{
			"SlotPlanRejected",
			slotplan.SlotPlanRejected{Header: header(2, rejectedT), Reason: "Too many relocations before peak season."},
			analyticsEnvelope("SlotPlanRejected", "2026-10-08T12:15:00Z", `{"plan_id":"`+planID+`","site_id":"SITE-1",`+
				`"rejected_at":"2026-10-08T12:15:00Z","reason":"Too many relocations before peak season."}`),
		},
	}
	for _, tc := range cases {
		msgs, err := (&AnalyticsEncoder{NewID: fixedID}).Encode(tc.event)
		if err != nil || len(msgs) != 1 {
			t.Fatalf("%s: encode = %d msgs, %v", tc.name, len(msgs), err)
		}
		m := msgs[0]
		if m.Topic != AnalyticsTopic {
			t.Fatalf("%s: topic = %s, want %s", tc.name, m.Topic, AnalyticsTopic)
		}
		if m.DataSchema != "urn:warehouse:slotting-optimization:analytics:"+tc.name+":v1" {
			t.Fatalf("%s: dataschema = %s", tc.name, m.DataSchema)
		}
		if string(m.Value) != tc.want {
			t.Fatalf("%s: value =\n%s\nwant\n%s", tc.name, m.Value, tc.want)
		}
		if m.EventType != "com.warehouse.wms.slotting-optimization.slotplan."+tc.name {
			t.Fatalf("%s: type = %s", tc.name, m.EventType)
		}
	}
}

// checkPair asserts that in (integration) and an (analytics) are the two
// rows of ONE occurrence: same id, type, key and payload; only the topic
// and the dataschema stream differ.
func checkPair(t *testing.T, i int, in, an outbox.Message, eventName string) {
	t.Helper()
	if in.Topic != Topic || an.Topic != AnalyticsTopic {
		t.Fatalf("event %d topics = %s, %s", i, in.Topic, an.Topic)
	}
	if in.EventID != an.EventID {
		t.Fatalf("event %d ids = %s, %s; both must be the one minted id", i, in.EventID, an.EventID)
	}
	if in.EventType != an.EventType || in.EventType != "com.warehouse.wms.slotting-optimization.slotplan."+eventName {
		t.Fatalf("event %d types = %s, %s", i, in.EventType, an.EventType)
	}
	if string(in.Key) != string(an.Key) || string(in.Key) != planID {
		t.Fatalf("event %d keys = %s, %s", i, in.Key, an.Key)
	}
	if in.DataSchema == an.DataSchema || !strings.HasSuffix(an.DataSchema, ":analytics:"+eventName+":v1") {
		t.Fatalf("event %d dataschemas = %s, %s", i, in.DataSchema, an.DataSchema)
	}
	var inData, anData json.RawMessage
	if err := json.Unmarshal(in.Value, &struct {
		Data *json.RawMessage `json:"data"`
	}{&inData}); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(an.Value, &struct {
		Data *json.RawMessage `json:"data"`
	}{&anData}); err != nil {
		t.Fatal(err)
	}
	if string(inData) != string(anData) {
		t.Fatalf("event %d payloads differ:\n%s\n%s", i, inData, anData)
	}
}

// TestFanoutEncoder_TwoRowsOneIDPerEvent: every event becomes exactly the
// integration row then the analytics row, sharing one CloudEvents id, one
// type, one key and one payload; only topic and dataschema differ.
func TestFanoutEncoder_TwoRowsOneIDPerEvent(t *testing.T) {
	events := []slotplan.Event{
		slotplan.SlotPlanGenerated{Header: header(1, generated), Policy: slotplan.PolicyABCVelocityV1},
		slotplan.SlotPlanRejected{Header: header(2, rejectedT)},
	}
	msgs, err := (&FanoutEncoder{NewID: fixedID}).Encode(events...)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 4 {
		t.Fatalf("msgs = %d, want 4 (two per event)", len(msgs))
	}
	for i, event := range events {
		checkPair(t, i, msgs[2*i], msgs[2*i+1], event.EventName())
	}
}

// TestFanoutEncoder_FailsWholeCallOnUnknownEvent: an event the streams do
// not publish fails and returns no rows.
func TestFanoutEncoder_FailsWholeCallOnUnknownEvent(t *testing.T) {
	_, err := (&FanoutEncoder{}).Encode(unknownEvent{})
	if err == nil {
		t.Fatal("an unpublishable event must fail the call")
	}
	msgs, err := (&AnalyticsEncoder{}).Encode(unknownEvent{})
	if err == nil || msgs != nil {
		t.Fatal("the analytics encoder must fail the call too")
	}
}

// TestFanoutEncoder_TopicOverridesReachBothStreams: integration tests point
// each stream at its own unique topic through the encoders' Topic fields.
func TestFanoutEncoder_TopicOverridesReachBothStreams(t *testing.T) {
	f := &FanoutEncoder{
		Integration: &Encoder{Topic: "warehouse.slotting-optimization.events.itest-1"},
		Analytics:   &AnalyticsEncoder{Topic: "warehouse.slotting-optimization.analytics.itest-1"},
		NewID:       fixedID,
	}
	msgs, err := f.Encode(slotplan.SlotPlanGenerated{Header: header(1, generated), Policy: slotplan.PolicyABCVelocityV1})
	if err != nil || len(msgs) != 2 {
		t.Fatalf("encode = %d msgs, %v", len(msgs), err)
	}
	if msgs[0].Topic != "warehouse.slotting-optimization.events.itest-1" || msgs[1].Topic != "warehouse.slotting-optimization.analytics.itest-1" {
		t.Fatalf("topics = %s, %s", msgs[0].Topic, msgs[1].Topic)
	}
}

// TestFanoutEncoder_UniqueIDsAcrossOccurrences: two occurrences never share
// an id (random minting).
func TestFanoutEncoder_UniqueIDsAcrossOccurrences(t *testing.T) {
	f := NewFanoutEncoder()
	first, err := f.Encode(slotplan.SlotPlanGenerated{Header: header(1, generated)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.Encode(slotplan.SlotPlanGenerated{Header: header(1, generated)})
	if err != nil {
		t.Fatal(err)
	}
	if first[0].EventID == second[0].EventID {
		t.Fatal("two occurrences minted the same id")
	}
}
