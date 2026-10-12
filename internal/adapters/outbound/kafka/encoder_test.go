package kafka

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

const (
	goldenID = "1b0c9a4e-2f7d-4a63-9d1e-5a7c3e2b9f10"
	planID   = "plan-3b0e8d2a-7c41-4f6a-9d15-2a8b6c4e1f07"
)

func fixedID() string { return goldenID }

func header(version int64, at time.Time) slotplan.Header {
	return slotplan.Header{Plan: planID, Site: "SITE-1", Version: version, At: at}
}

func envelope(eventName, timeStr, data string) string {
	return `{"specversion":"1.0","id":"` + goldenID + `","source":"/warehouse/slotting-optimization",` +
		`"type":"com.warehouse.wms.slotting-optimization.slotplan.` + eventName + `","subject":"` + planID + `",` +
		`"datacontenttype":"application/json","dataschema":"urn:warehouse:slotting-optimization:events:` + eventName + `:v1",` +
		`"time":"` + timeStr + `","data":` + data + `}`
}

var (
	generated = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	approvedT = time.Date(2026, 10, 8, 12, 30, 0, 0, time.UTC)
	rejectedT = time.Date(2026, 10, 8, 12, 15, 0, 0, time.UTC)
)

// TestEncoder_GoldenWireFormat pins the exact bytes of every published type
// (all CloudEvents attributes + payload, as apis/asyncapi.yaml's examples)
// and the outbox row around them (content-type header, key, topic, full
// type, dataschema, id).
func TestEncoder_GoldenWireFormat(t *testing.T) {
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
			envelope("SlotPlanGenerated", "2026-10-08T12:00:00Z", `{"plan_id":"`+planID+`","site_id":"SITE-1",`+
				`"window_from":"2026-09-10T12:00:00Z","window_to":"2026-10-08T12:00:00Z","policy":"abc-velocity-v1",`+
				`"assignment_count":2,"move_count":3,"unassigned_count":1}`),
		},
		{
			"SlotPlanApproved",
			slotplan.SlotPlanApproved{
				Header:           header(2, approvedT),
				SupersedesPlanID: "plan-8e7d6c5b-4a39-4281-b0c9-d8e7f6a5b4c3",
				Assignments: []slotplan.Assignment{
					{SKU: "SKU-1", Slot: "WH1-FWD-A01-01-01-A", Class: slotplan.ClassA, Picks: 80, Units: 160},
					{SKU: "SKU-2", Slot: "WH1-FWD-A01-01-02-A", Class: slotplan.ClassB, Picks: 20, Units: 40},
				},
				Moves: []slotplan.Move{
					{SKU: "SKU-1", To: "WH1-FWD-A01-01-01-A", Kind: slotplan.MoveAssign},
					{SKU: "SKU-2", From: "WH1-FWD-A02-03-01-A", To: "WH1-FWD-A01-01-02-A", Kind: slotplan.MoveRelocate},
					{SKU: "SKU-3", From: "WH1-FWD-A01-01-03-A", Kind: slotplan.MoveVacate},
				},
			},
			envelope("SlotPlanApproved", "2026-10-08T12:30:00Z", `{"plan_id":"`+planID+`","site_id":"SITE-1","approved_at":"2026-10-08T12:30:00Z",`+
				`"supersedes_plan_id":"plan-8e7d6c5b-4a39-4281-b0c9-d8e7f6a5b4c3",`+
				`"assignments":[{"sku":"SKU-1","slot":"WH1-FWD-A01-01-01-A"},{"sku":"SKU-2","slot":"WH1-FWD-A01-01-02-A"}],`+
				`"moves":[{"sku":"SKU-1","to_slot":"WH1-FWD-A01-01-01-A","kind":"Assign"},`+
				`{"sku":"SKU-2","from_slot":"WH1-FWD-A02-03-01-A","to_slot":"WH1-FWD-A01-01-02-A","kind":"Relocate"},`+
				`{"sku":"SKU-3","from_slot":"WH1-FWD-A01-01-03-A","kind":"Vacate"}]}`),
		},
		{
			"SlotPlanRejected",
			slotplan.SlotPlanRejected{Header: header(2, rejectedT), Reason: "Too many relocations before peak season."},
			envelope("SlotPlanRejected", "2026-10-08T12:15:00Z", `{"plan_id":"`+planID+`","site_id":"SITE-1","rejected_at":"2026-10-08T12:15:00Z",`+
				`"reason":"Too many relocations before peak season."}`),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msgs, err := (&Encoder{NewID: fixedID}).Encode(tc.event)
			if err != nil {
				t.Fatal(err)
			}
			if len(msgs) != 1 {
				t.Fatalf("got %d messages", len(msgs))
			}
			msg := msgs[0]
			if string(msg.Value) != tc.want {
				t.Fatalf("value:\n got %s\nwant %s", msg.Value, tc.want)
			}
			wantType := "com.warehouse.wms.slotting-optimization.slotplan." + tc.name
			if msg.EventType != wantType || msg.EventID != goldenID || msg.Topic != "warehouse.slotting-optimization.events" ||
				string(msg.Key) != planID || msg.Subject != planID ||
				msg.DataSchema != "urn:warehouse:slotting-optimization:events:"+tc.name+":v1" {
				t.Fatalf("row = %+v", msg)
			}
			if len(msg.Headers) != 1 || msg.Headers[0].Key != "content-type" || msg.Headers[0].Value != "application/cloudevents+json; charset=UTF-8" {
				t.Fatalf("headers = %+v", msg.Headers)
			}
		})
	}
}

func TestEncoder_OptionalFieldsAreOmittedAndListsNeverNull(t *testing.T) {
	msgs, err := (&Encoder{NewID: fixedID}).Encode(
		slotplan.SlotPlanApproved{Header: header(2, approvedT)},
		slotplan.SlotPlanRejected{Header: header(2, rejectedT)},
	)
	if err != nil {
		t.Fatal(err)
	}
	wantData := []string{
		`"data":{"plan_id":"` + planID + `","site_id":"SITE-1","approved_at":"2026-10-08T12:30:00Z","assignments":[],"moves":[]}}`,
		`"data":{"plan_id":"` + planID + `","site_id":"SITE-1","rejected_at":"2026-10-08T12:15:00Z"}}`,
	}
	for i, want := range wantData {
		if !strings.HasSuffix(string(msgs[i].Value), want) {
			t.Errorf("message %d = %s\nwant suffix %s", i, msgs[i].Value, want)
		}
	}
}

func TestEncoder_FractionalSecondsKeepTheirPrecisionInUTC(t *testing.T) {
	at := time.Date(2026, 10, 8, 9, 30, 0, 500000000, time.FixedZone("BRT", -3*3600))
	msgs, err := (&Encoder{NewID: fixedID}).Encode(slotplan.SlotPlanRejected{Header: header(2, at)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(msgs[0].Value), `"rejected_at":"2026-10-08T12:30:00.5Z"`) || !strings.Contains(string(msgs[0].Value), `"time":"2026-10-08T12:30:00.5Z"`) {
		t.Fatalf("value = %s", msgs[0].Value)
	}
}

type unknownEvent struct{ slotplan.Header }

func (unknownEvent) EventName() string { return "Unknown" }

func TestEncoder_RejectsAnUnpublishedEvent(t *testing.T) {
	msgs, err := NewEncoder().Encode(slotplan.SlotPlanRejected{Header: header(1, generated)}, unknownEvent{header(1, generated)})
	if err == nil || msgs != nil {
		t.Fatalf("got %v, %v; want an error and no messages", msgs, err)
	}
}

func TestEncoder_RejectsAnEventWithoutASubject(t *testing.T) {
	_, err := NewEncoder().Encode(slotplan.SlotPlanRejected{})
	if err == nil {
		t.Fatal("an event with an empty plan id has no CloudEvents subject and must not encode")
	}
}

func TestEncoder_DefaultsAndTopicOverride(t *testing.T) {
	msgs, err := (&Encoder{Topic: "itest-topic"}).Encode(slotplan.SlotPlanRejected{Header: header(1, generated)}, slotplan.SlotPlanRejected{Header: header(1, generated)})
	if err != nil {
		t.Fatal(err)
	}
	if msgs[0].Topic != "itest-topic" {
		t.Fatalf("topic = %q", msgs[0].Topic)
	}
	if _, err := uuid.Parse(msgs[0].EventID); err != nil || msgs[0].EventID == msgs[1].EventID {
		t.Fatalf("ids %q %q must be distinct UUIDs (%v)", msgs[0].EventID, msgs[1].EventID, err)
	}
	if !strings.Contains(string(msgs[0].Value), `"id":"`+msgs[0].EventID+`"`) {
		t.Fatal("the row id must be the CloudEvents id")
	}
}
