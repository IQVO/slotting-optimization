package kafka

import (
	"github.com/google/uuid"

	"github.com/claudioed/slotting-optimization/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/slotting-optimization/internal/application/outbox"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// AnalyticsTopic is this service's analytics topic (ADR 0006): the data
// product's own stream, consumed only by cmd/slotting-projector. Its DLQ is
// AnalyticsTopic + ".dlq".
const AnalyticsTopic = "warehouse.slotting-optimization.analytics"

// AnalyticsEncoder encodes the same three events as Encoder onto
// AnalyticsTopic, with dataschema
// urn:warehouse:slotting-optimization:analytics:<EventName>:v1. The payloads
// equal the integration payloads byte for byte (ADR 0006): the row builders
// are shared, so an event published on one stream can never be missing from
// the other. Alone it mints its own ids (tests); production uses
// FanoutEncoder so both messages of one occurrence share one id.
type AnalyticsEncoder struct {
	// NewID mints a CloudEvents id (uuid.NewString when nil).
	NewID func() string
	// Topic overrides AnalyticsTopic (integration tests use a unique one).
	Topic string
}

// Encode encodes events in order onto the analytics stream. An event type
// the streams do not publish is a programming error and fails the call.
func (e *AnalyticsEncoder) Encode(events ...slotplan.Event) ([]outbox.Message, error) {
	newID := e.NewID
	if newID == nil {
		newID = uuid.NewString
	}
	topic := e.Topic
	if topic == "" {
		topic = AnalyticsTopic
	}
	out := make([]outbox.Message, 0, len(events))
	for _, ev := range events {
		data, err := payloadFor(ev)
		if err != nil {
			return nil, err
		}
		msg, err := buildMessage(ev, newID(), topic, cloudevents.StreamAnalytics, data)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, nil
}

// FanoutEncoder is the encoder cmd/api hands the use cases (ADR 0006):
// every domain event becomes TWO outbox messages, the integration one first
// and the analytics one second, carrying the SAME CloudEvents id (minted
// once here, persisted with both rows) and the same `type`. Both rows are
// inserted by the use case's single UnitOfWork, so they commit or roll back
// together with the aggregate; a relay retry republishes each row's
// persisted bytes. outbox_events' identity is (event_id, topic), so the
// shared id is legal.
type FanoutEncoder struct {
	// Integration is the events-stream encoder (a zero &Encoder{} when nil).
	Integration *Encoder
	// Analytics is the analytics-stream encoder (a zero &AnalyticsEncoder{}
	// when nil).
	Analytics *AnalyticsEncoder
	// NewID mints the shared CloudEvents id (uuid.NewString when nil).
	NewID func() string
}

// NewFanoutEncoder returns a FanoutEncoder over the production topics.
func NewFanoutEncoder() *FanoutEncoder {
	return &FanoutEncoder{NewID: uuid.NewString}
}

// Encode encodes events, two messages per event (integration then
// analytics). It fails, and returns no messages, on an event type the
// streams do not publish.
func (f *FanoutEncoder) Encode(events ...slotplan.Event) ([]outbox.Message, error) {
	integration, analytics := f.Integration, f.Analytics
	if integration == nil {
		integration = &Encoder{}
	}
	if analytics == nil {
		analytics = &AnalyticsEncoder{}
	}
	newID := f.NewID
	if newID == nil {
		newID = uuid.NewString
	}
	integrationTopic, analyticsTopic := integration.Topic, analytics.Topic
	if integrationTopic == "" {
		integrationTopic = Topic
	}
	if analyticsTopic == "" {
		analyticsTopic = AnalyticsTopic
	}
	out := make([]outbox.Message, 0, 2*len(events))
	for _, ev := range events {
		data, err := payloadFor(ev)
		if err != nil {
			return nil, err
		}
		id := newID()
		in, err := buildMessage(ev, id, integrationTopic, cloudevents.StreamEvents, data)
		if err != nil {
			return nil, err
		}
		an, err := buildMessage(ev, id, analyticsTopic, cloudevents.StreamAnalytics, data)
		if err != nil {
			return nil, err
		}
		out = append(out, in, an)
	}
	return out, nil
}
