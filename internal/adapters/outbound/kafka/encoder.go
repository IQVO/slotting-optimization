// Package kafka is the outbound Kafka adapter: it encodes SlotPlan domain
// events into CloudEvents 1.0 outbox messages (Encoder) and writes drained
// outbox rows to the broker (RelaySink). Envelopes are built ONLY through
// internal/adapters/kafka/cloudevents. Payloads are exactly the ones pinned
// in apis/asyncapi.yaml.
package kafka

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/claudioed/slotting-optimization/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/slotting-optimization/internal/application/outbox"
	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// Topic is this service's integration-event topic.
const Topic = "warehouse.slotting-optimization.events"

// Entity is the `<entity>` segment of every published type:
// com.warehouse.wms.slotting-optimization.slotplan.<EventName>.
const Entity = "slotplan"

// schemaVersion is the dataschema version of every payload below
// (urn:warehouse:slotting-optimization:events:<EventName>:v1).
const schemaVersion = 1

// Wire payloads (CloudEvents `data`, snake_case JSON). Optional fields are
// omitted when unset; timestamps are RFC 3339 UTC.

type generatedData struct {
	PlanID          string `json:"plan_id"`
	SiteID          string `json:"site_id"`
	WindowFrom      string `json:"window_from"`
	WindowTo        string `json:"window_to"`
	Policy          string `json:"policy"`
	AssignmentCount int    `json:"assignment_count"`
	MoveCount       int    `json:"move_count"`
	UnassignedCount int    `json:"unassigned_count"`
}

type assignmentData struct {
	SKU  string `json:"sku"`
	Slot string `json:"slot"`
}

type moveData struct {
	SKU      string `json:"sku"`
	FromSlot string `json:"from_slot,omitempty"`
	ToSlot   string `json:"to_slot,omitempty"`
	Kind     string `json:"kind"`
}

type approvedData struct {
	PlanID           string           `json:"plan_id"`
	SiteID           string           `json:"site_id"`
	ApprovedAt       string           `json:"approved_at"`
	SupersedesPlanID string           `json:"supersedes_plan_id,omitempty"`
	Assignments      []assignmentData `json:"assignments"`
	Moves            []moveData       `json:"moves"`
}

type rejectedData struct {
	PlanID     string `json:"plan_id"`
	SiteID     string `json:"site_id"`
	RejectedAt string `json:"rejected_at"`
	Reason     string `json:"reason,omitempty"`
}

// Encoder implements ports.EventEncoder: each domain event becomes one
// outbox.Message holding the CloudEvents bytes, the Kafka key (the plan id)
// and the content-type header. The CloudEvents `id` is minted HERE, once,
// and persisted with the outbox row, so a relay retry republishes the same id.
type Encoder struct {
	// NewID mints a CloudEvents id; uuid.NewString when nil.
	NewID func() string
	// Topic overrides the destination topic (Topic when empty).
	Topic string
}

// NewEncoder returns an Encoder minting random UUID v4 ids.
func NewEncoder() *Encoder { return &Encoder{NewID: uuid.NewString} }

var _ ports.EventEncoder = (*Encoder)(nil)

// Encode encodes events in order. An event type it does not publish is a
// programming error and fails the whole call.
func (e *Encoder) Encode(events ...slotplan.Event) ([]outbox.Message, error) {
	newID := e.NewID
	if newID == nil {
		newID = uuid.NewString
	}
	topic := e.Topic
	if topic == "" {
		topic = Topic
	}
	out := make([]outbox.Message, 0, len(events))
	for _, ev := range events {
		data, err := payloadFor(ev)
		if err != nil {
			return nil, err
		}
		msg, err := buildMessage(ev, newID(), topic, data)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, nil
}

// buildMessage wraps data in the CloudEvents envelope of ev and returns the
// outbox row for topic.
func buildMessage(ev slotplan.Event, id, topic string, data any) (outbox.Message, error) {
	plan := string(ev.PlanID())
	value, err := cloudevents.New(cloudevents.Spec{
		ID:        id,
		Entity:    Entity,
		EventName: ev.EventName(),
		Subject:   plan,
		Time:      ev.OccurredAt(),
		Stream:    cloudevents.StreamEvents,
		Version:   schemaVersion,
		Data:      data,
	})
	if err != nil {
		return outbox.Message{}, fmt.Errorf("encode %s: %w", ev.EventName(), err)
	}
	ct := cloudevents.ContentTypeHeader()
	return outbox.Message{
		EventID:    id,
		Topic:      topic,
		EventType:  cloudevents.Type(Entity, ev.EventName()),
		Subject:    plan,
		Key:        []byte(plan),
		DataSchema: cloudevents.DataSchema(cloudevents.StreamEvents, ev.EventName(), schemaVersion),
		Value:      value,
		Headers:    []outbox.Header{{Key: ct.Key, Value: string(ct.Value)}},
	}, nil
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func payloadFor(ev slotplan.Event) (any, error) {
	switch e := ev.(type) {
	case slotplan.SlotPlanGenerated:
		return generatedData{
			PlanID: string(e.Plan), SiteID: string(e.Site),
			WindowFrom: ts(e.Window.From), WindowTo: ts(e.Window.To), Policy: string(e.Policy),
			AssignmentCount: e.AssignmentCount, MoveCount: e.MoveCount, UnassignedCount: e.UnassignedCount,
		}, nil
	case slotplan.SlotPlanApproved:
		return approved(e), nil
	case slotplan.SlotPlanRejected:
		return rejectedData{PlanID: string(e.Plan), SiteID: string(e.Site), RejectedAt: ts(e.At), Reason: e.Reason}, nil
	default:
		return nil, fmt.Errorf("kafka encoder: %s is not a published event", ev.EventName())
	}
}

// approved maps the event to its wire form. Both lists are always present
// (never null) even when empty.
func approved(e slotplan.SlotPlanApproved) approvedData {
	out := approvedData{
		PlanID: string(e.Plan), SiteID: string(e.Site), ApprovedAt: ts(e.At),
		SupersedesPlanID: string(e.SupersedesPlanID),
		Assignments:      make([]assignmentData, len(e.Assignments)),
		Moves:            make([]moveData, len(e.Moves)),
	}
	for i, a := range e.Assignments {
		out.Assignments[i] = assignmentData{SKU: string(a.SKU), Slot: string(a.Slot)}
	}
	for i, m := range e.Moves {
		out.Moves[i] = moveData{SKU: string(m.SKU), FromSlot: string(m.From), ToSlot: string(m.To), Kind: string(m.Kind)}
	}
	return out
}
