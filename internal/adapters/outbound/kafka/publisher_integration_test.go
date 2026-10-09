//go:build integration

// Integration tests for the outbound Kafka publisher over a REAL broker
// started by testcontainers (internal/testing/kafkatest: one shared broker
// per test binary, a unique timestamp-suffixed topic per test — never an
// external KAFKA_BROKERS, never localhost, never t.Skip). Real domain events
// raised by the SlotPlan aggregate are encoded by the production Encoder,
// written through the production RelaySink (the exact segmentio.Writer
// settings cmd/api builds: RequireAll acks, Hash balancer on the plan id,
// short BatchTimeout) and consumed back from the broker. What is asserted is
// the fleet-mandatory CloudEvents 1.0 contract on the wire: the
// com.warehouse.wms.slotting-optimization.slotplan.<Event> type, specversion
// 1.0, a non-empty id and the service source, the plan-id message key that
// keeps one plan's events ordered, the content-type header and the payloads
// pinned in apis/asyncapi.yaml.
package kafka_test

import (
	"context"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"

	"github.com/claudioed/slotting-optimization/internal/adapters/kafka/cloudevents"
	outboundkafka "github.com/claudioed/slotting-optimization/internal/adapters/outbound/kafka"
	"github.com/claudioed/slotting-optimization/internal/application/outbox"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
	"github.com/claudioed/slotting-optimization/internal/testing/kafkatest"
	"github.com/claudioed/slotting-optimization/internal/testing/repocontract"
)

// outboxMessage is one encoded event in the outbox row's wire shape.
type outboxMessage = outbox.Message

// The broker itself comes from internal/testing/kafkatest, which starts it
// through testcontainers-go/modules/kafka (referenced here so the module
// import stays reachable from this Kafka-touching file).
var _ = tckafka.Run

// generate returns a Draft plan (the repocontract fixture shape: one
// assignment, one Assign move, one unassigned SKU) together with the
// SlotPlanGenerated event it raised.
func generate(t *testing.T, id, site string, at time.Time) (*slotplan.SlotPlan, []slotplan.Event) {
	t.Helper()
	w, err := slotplan.NewWindow(at.Add(-28*24*time.Hour), at)
	if err != nil {
		t.Fatal(err)
	}
	p, events, err := slotplan.Generate(slotplan.NewPlan{
		ID: slotplan.PlanID(id), Site: slotplan.SiteID(site), Window: w, Policy: slotplan.PolicyABCVelocityV1,
		Assignments: []slotplan.Assignment{{SKU: "SKU-1", Slot: slotplan.SlotCode("slot-" + id), Class: slotplan.ClassA, Picks: 3, Units: 9}},
		Moves:       []slotplan.Move{{SKU: "SKU-1", To: slotplan.SlotCode("slot-" + id), Kind: slotplan.MoveAssign}},
		Unassigned:  []slotplan.Unassigned{{SKU: "SKU-2", Reason: slotplan.ReasonNoPhysicalProfile}},
	}, at)
	if err != nil {
		t.Fatal(err)
	}
	return p, events
}

// publish drives the real publisher path: encode with the production
// Encoder (CloudEvents ids minted here, once, exactly like the use cases do
// inside their unit of work) and send the outbox rows through the
// production RelaySink to topic on the real broker.
func publish(t *testing.T, topic string, events ...slotplan.Event) []outboxMessage {
	t.Helper()
	msgs, err := (&outboundkafka.Encoder{Topic: topic}).Encode(events...)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	publishRows(t, topic, msgs...)
	return msgs
}

// publishRows sends already-encoded outbox rows through the production
// RelaySink — the exact operation the outbox relay performs on every pass,
// including a retry of a row that failed mid-pass.
func publishRows(t *testing.T, topic string, msgs ...outboxMessage) {
	t.Helper()
	sink := outboundkafka.NewRelaySink(kafkatest.Brokers(t))
	t.Cleanup(func() { _ = sink.Close() })
	if err := sink.Send(context.Background(), msgs...); err != nil {
		t.Fatalf("send: %v", err)
	}
}

// wantEnvelope is the CloudEvents contract one consumed message must carry.
type wantEnvelope struct {
	typ     string
	subject string
	schema  string
	data    func(t *testing.T, e ce.Event)
}

// assertConsumed decodes one consumed message and asserts the mandatory
// CloudEvents 1.0 envelope and this service's identity segments.
func assertConsumed(t *testing.T, msg kafkatest.Message, want wantEnvelope) {
	t.Helper()
	if msg.Key != want.subject {
		t.Errorf("key = %q, want the plan id %q (one plan's events stay ordered on one partition)", msg.Key, want.subject)
	}
	if len(msg.Headers) != 1 || msg.Headers["content-type"] != cloudevents.MediaType {
		t.Errorf("headers = %v, want exactly the content-type %q", msg.Headers, cloudevents.MediaType)
	}
	e, err := cloudevents.Decode(msg.Value)
	if err != nil {
		t.Fatalf("decode %s: %v", want.typ, err)
	}
	if e.SpecVersion() != cloudevents.SpecVersion {
		t.Errorf("specversion = %q, want %q", e.SpecVersion(), cloudevents.SpecVersion)
	}
	if e.Type() != want.typ {
		t.Errorf("type = %q, want %q", e.Type(), want.typ)
	}
	if e.Source() != cloudevents.Source {
		t.Errorf("source = %q, want %q", e.Source(), cloudevents.Source)
	}
	if e.ID() == "" {
		t.Errorf("id is empty")
	}
	if e.Subject() != want.subject {
		t.Errorf("subject = %q, want %q", e.Subject(), want.subject)
	}
	if e.DataSchema() != want.schema {
		t.Errorf("dataschema = %q, want %q", e.DataSchema(), want.schema)
	}
	if want.data != nil {
		want.data(t, e)
	}
}

// TestKafkaPublisher_RealBrokerPublishesTheCloudEventsContract publishes the
// aggregate's three event kinds through the production encoder and sink and
// consumes them back from the real broker, asserting the envelope, the key
// and the payloads. The fourth message re-publishes the SAME CloudEvents id
// (an outbox retry republishes identical bytes): consumers dedupe on the id.
func TestKafkaPublisher_RealBrokerPublishesTheCloudEventsContract(t *testing.T) {
	topic := kafkatest.Topic(t, outboundkafka.Topic)
	at := repocontract.T0
	prefix := "com.warehouse.wms.slotting-optimization.slotplan."

	_, generated := generate(t, "plan-pub-1", "SITE-1", at)

	approvedPlan, _ := generate(t, "plan-pub-2", "SITE-2", at.Add(time.Minute))
	approved, err := approvedPlan.Approve("", at.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("approve: %v", err)
	}

	rejectedPlan, _ := generate(t, "plan-pub-3", "SITE-3", at.Add(2*time.Minute))
	rejected, err := rejectedPlan.Reject("hazmat mismatch", at.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("reject: %v", err)
	}

	publish(t, topic, generated...)
	publish(t, topic, approved...)
	rejectedRows := publish(t, topic, rejected...)
	// The outbox retry: the SAME persisted rows (same CloudEvents ids)
	// republished to the broker, exactly as the relay's next pass does.
	publishRows(t, topic, rejectedRows...)

	got := kafkatest.ReadN(t, topic, 4, 60*time.Second)
	for i, want := range []wantEnvelope{
		{
			typ: prefix + "SlotPlanGenerated", subject: "plan-pub-1",
			schema: "urn:warehouse:slotting-optimization:events:SlotPlanGenerated:v1",
			data: func(t *testing.T, e ce.Event) {
				var p struct {
					PlanID          string `json:"plan_id"`
					SiteID          string `json:"site_id"`
					WindowFrom      string `json:"window_from"`
					WindowTo        string `json:"window_to"`
					Policy          string `json:"policy"`
					AssignmentCount int    `json:"assignment_count"`
					MoveCount       int    `json:"move_count"`
					UnassignedCount int    `json:"unassigned_count"`
				}
				if err := e.DataAs(&p); err != nil {
					t.Fatalf("generated data: %v", err)
				}
				if p.PlanID != "plan-pub-1" || p.SiteID != "SITE-1" || p.Policy != string(slotplan.PolicyABCVelocityV1) {
					t.Fatalf("generated payload identity = %+v", p)
				}
				if p.AssignmentCount != 1 || p.MoveCount != 1 || p.UnassignedCount != 1 {
					t.Fatalf("generated payload counts = %+v", p)
				}
				if p.WindowFrom == "" || p.WindowTo == "" {
					t.Fatalf("generated payload window = %+v", p)
				}
			},
		},
		{
			typ: prefix + "SlotPlanApproved", subject: "plan-pub-2",
			schema: "urn:warehouse:slotting-optimization:events:SlotPlanApproved:v1",
			data: func(t *testing.T, e ce.Event) {
				var p struct {
					PlanID      string `json:"plan_id"`
					SiteID      string `json:"site_id"`
					ApprovedAt  string `json:"approved_at"`
					Assignments []struct {
						SKU  string `json:"sku"`
						Slot string `json:"slot"`
					} `json:"assignments"`
					Moves []struct {
						SKU      string `json:"sku"`
						Kind     string `json:"kind"`
						FromSlot string `json:"from_slot"`
						ToSlot   string `json:"to_slot"`
					} `json:"moves"`
				}
				if err := e.DataAs(&p); err != nil {
					t.Fatalf("approved data: %v", err)
				}
				if p.PlanID != "plan-pub-2" || p.SiteID != "SITE-2" || p.ApprovedAt == "" {
					t.Fatalf("approved payload identity = %+v", p)
				}
				// The full map and the moves ride along: a consumer needs no
				// lookup. Lists are never null.
				if len(p.Assignments) != 1 || p.Assignments[0].SKU != "SKU-1" || p.Assignments[0].Slot != "slot-plan-pub-2" {
					t.Fatalf("approved assignments = %+v", p.Assignments)
				}
				if len(p.Moves) != 1 || p.Moves[0].Kind != "Assign" || p.Moves[0].ToSlot != "slot-plan-pub-2" {
					t.Fatalf("approved moves = %+v", p.Moves)
				}
			},
		},
		{
			typ: prefix + "SlotPlanRejected", subject: "plan-pub-3",
			schema: "urn:warehouse:slotting-optimization:events:SlotPlanRejected:v1",
			data: func(t *testing.T, e ce.Event) {
				var p struct {
					PlanID     string `json:"plan_id"`
					SiteID     string `json:"site_id"`
					RejectedAt string `json:"rejected_at"`
					Reason     string `json:"reason"`
				}
				if err := e.DataAs(&p); err != nil {
					t.Fatalf("rejected data: %v", err)
				}
				if p.PlanID != "plan-pub-3" || p.SiteID != "SITE-3" || p.Reason != "hazmat mismatch" {
					t.Fatalf("rejected payload = %+v", p)
				}
			},
		},
		{
			// The retry's envelope: same everything as the first send.
			typ: prefix + "SlotPlanRejected", subject: "plan-pub-3",
			schema: "urn:warehouse:slotting-optimization:events:SlotPlanRejected:v1",
		},
	} {
		assertConsumed(t, got[i], want)
	}

	// The relay retry published the same id: the two rejected envelopes are
	// byte-identical (id included), which is what lets consumers dedupe.
	if string(got[2].Value) != string(got[3].Value) {
		t.Fatal("an outbox retry must republish identical bytes (same CloudEvents id)")
	}
}

// TestKafkaPublisher_OnePlanSticksToOnePartitionKeysOrdering publishes
// several events of ONE plan and asserts they all carried the plan id as the
// Kafka key — the ordering guarantee the Hash balancer gives the aggregate.
func TestKafkaPublisher_OnePlanSticksToOnePartitionKeysOrdering(t *testing.T) {
	topic := kafkatest.Topic(t, outboundkafka.Topic)
	at := repocontract.T0

	plan, events := generate(t, "plan-ord-1", "SITE-9", at)
	approved, err := plan.Approve("", at.Add(time.Minute))
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	events = append(events, approved...)
	publish(t, topic, events...)

	got := kafkatest.ReadN(t, topic, 2, 60*time.Second)
	types := map[string]bool{}
	for i, msg := range got {
		if msg.Key != "plan-ord-1" {
			t.Errorf("message %d key = %q, want plan-ord-1", i, msg.Key)
		}
		e, err := cloudevents.Decode(msg.Value)
		if err != nil {
			t.Fatalf("decode message %d: %v", i, err)
		}
		types[e.Type()] = true
	}
	if !types["com.warehouse.wms.slotting-optimization.slotplan.SlotPlanGenerated"] ||
		!types["com.warehouse.wms.slotting-optimization.slotplan.SlotPlanApproved"] {
		t.Fatalf("expected one Generated and one Approved event, got %v", types)
	}
}
