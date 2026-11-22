package kafka

import (
	"testing"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/slotting-optimization/internal/analytics/report"
)

// Pinned-contract tests for this repo's OWN spec (self-pin): the projector
// consumes this service's analytics topic, whose contract — including the
// .dlq poison policy — is documented in apis/asyncapi.yaml. The pinned copy
// is byte-equal to IQVO/slotting-optimization develop (contracts/pinned.json).
// Fixtures: this spec's own message examples (provenance: spec).
const (
	slottingSelfPin     = "testdata/slotting-optimization.asyncapi.yaml"
	analyticsTopic      = "warehouse.slotting-optimization.analytics" // mirrors outbound/kafka.AnalyticsTopic
	integrationTopicPin = "warehouse.slotting-optimization.events"
)

// E1: every type the projector dispatches on (analyticsKinds) stays
// documented on BOTH the analytics channel and the integration channel —
// the twin carries the same CloudEvents type under a different dataschema.
func TestSlotPlanAnalyticsPinCoversOurSubscription(t *testing.T) {
	doc := loadPinnedSpec(t, slottingSelfPin)
	requirePinnedDefaultContentType(t, doc)
	for _, channel := range []string{analyticsTopic, integrationTopicPin} {
		types := pinnedChannelTypes(t, doc, channel)
		for typ := range analyticsKinds {
			key, ok := types[typ]
			if !ok {
				t.Fatalf("pinned self spec no longer documents %s on %s", typ, channel)
			}
			requirePinnedMessageContentType(t, doc, key)
		}
	}
}

// newPinnedAnalytics stands the REAL projector consumer up on recording fakes.
func newPinnedAnalytics() (*AnalyticsConsumer, *pinnedProjection, *pinnedDLQRecorder) {
	projection, dlq := &pinnedProjection{}, &pinnedDLQRecorder{}
	return &AnalyticsConsumer{
		Projection: projection, DLQ: dlq, DLQTopic: analyticsTopic + DLQSuffix, Logger: quietLogger(),
	}, projection, dlq
}

// E2: the spec's own analytics examples decode through the REAL projector
// consumer and reach the projection with the right kind, id and plan.
func TestSlotPlanAnalyticsGoldenEventsDecode(t *testing.T) {
	doc := loadPinnedSpec(t, slottingSelfPin)
	c, projection, _ := newPinnedAnalytics()
	seen := 0
	for typ := range analyticsKinds {
		for _, ex := range pinnedExamples(t, doc, analyticsTopic, typ) {
			requirePinnedEnvelope(t, ex)
			if err := c.HandleMessage(t.Context(), pinnedKafkaMsg(pinnedEventBytes(t, ex))); err != nil {
				t.Fatalf("handle golden %s example %v: %v", typ, ex["id"], err)
			}
			seen++
		}
	}
	if got := len(projection.applied()); got != seen {
		t.Fatalf("projected %d events, want %d", got, seen)
	}
	for _, e := range projection.applied() {
		if e.PlanID == "" || e.SiteID == "" || e.EventID == "" {
			t.Errorf("projected event %+v lost an envelope field", e)
		}
	}
}

// E3: envelope invariants and the pin's own consts hold — including the
// analytics dataschema stream segment (urn:warehouse:slotting-optimization:
// analytics:<EventName>:v1), which REUSES the integration event name.
func TestSlotPlanAnalyticsEnvelopeInvariants(t *testing.T) {
	doc := loadPinnedSpec(t, slottingSelfPin)
	for typ := range analyticsKinds {
		pinnedEnvelopeConsts(t, doc, analyticsTopic, typ)
		for _, ex := range pinnedExamples(t, doc, analyticsTopic, typ) {
			requirePinnedEnvelope(t, ex)
			ds := strVal(ex["dataschema"])
			if want := "urn:warehouse:slotting-optimization:analytics:" + typeName(typ) + ":v1"; ds != want {
				t.Errorf("%s dataschema = %q, want the analytics twin %q", typ, ds, want)
			}
		}
	}
}

// E4, per the projector's OWN documented policy (ADR 0006): unknown data
// keys are tolerated; a known type with an unusable payload is
// dead-lettered to <topic>.dlq (raw bytes) and committed past; an unknown
// type is ignored; a non-CloudEvents body is skipped.
func TestSlotPlanAnalyticsTolerancePolicy(t *testing.T) {
	doc := loadPinnedSpec(t, slottingSelfPin)
	c, projection, dlq := newPinnedAnalytics()
	golden := pinnedFixtureFor(t, doc, analyticsTopic, pinnedKindType(report.KindSlotPlanGenerated))
	typ := strVal(golden["type"])

	before := len(projection.applied())
	if err := c.HandleMessage(t.Context(), pinnedKafkaMsg(pinnedEventBytes(t, pinnedWithUnknownDataKey(t, golden)))); err != nil {
		t.Fatalf("unknown data key must not fail projection: %v", err)
	}
	if got := len(projection.applied()); got != before+1 {
		t.Fatalf("unknown data key must still be projected: %d -> %d", before, got)
	}

	// payload plan_id != subject: deterministic, dead-lettered at once.
	poison := map[string]any{}
	for k, v := range golden {
		poison[k] = v
	}
	poison["id"] = "pin-self-poison-1"
	data := map[string]any{"plan_id": "a-different-plan", "site_id": "WH1"}
	poison["data"] = data
	if err := c.HandleMessage(t.Context(), pinnedKafkaMsg(pinnedEventBytes(t, poison))); err != nil {
		t.Fatalf("a deterministic poison must be dead-lettered, not retried: %v", err)
	}
	if got := dlq.published(); got != 1 {
		t.Fatalf("dead-lettered %d messages, want exactly 1", got)
	}
	if got := len(projection.applied()); got != before+1 {
		t.Fatalf("a dead-lettered event must not be projected: %d", got)
	}

	// an unknown type on the same topic is ignored, never dead-lettered.
	unknown := map[string]any{}
	for k, v := range golden {
		unknown[k] = v
	}
	unknown["id"] = "pin-self-unknown-1"
	unknown["type"] = typ + "Fictional"
	if err := c.HandleMessage(t.Context(), pinnedKafkaMsg(pinnedEventBytes(t, unknown))); err != nil {
		t.Fatalf("unknown type must be ignored, got %v", err)
	}
	if got := dlq.published(); got != 1 {
		t.Fatalf("an ignored type must not reach the DLQ: %d", got)
	}

	// a non-CloudEvents body is skipped (sampled WARN), never retried.
	if err := c.HandleMessage(t.Context(), pinnedKafkaMsg([]byte("not-a-cloudevent"))); err != nil {
		t.Fatalf("non-CloudEvent body must be skipped, got %v", err)
	}
	if got := dlq.published(); got != 1 {
		t.Fatalf("a skipped body must not reach the DLQ: %d", got)
	}
}

// pinnedKafkaMsg wraps wire bytes in the kafka message the loop hands the
// projector consumer.
func pinnedKafkaMsg(value []byte) kafkago.Message {
	return kafkago.Message{Topic: analyticsTopic, Partition: 0, Offset: 1, Value: value}
}

// pinnedKindType maps a report.Kind to the CloudEvents type the projector
// routes on (analyticsKinds is the routing table under test).
func pinnedKindType(kind report.Kind) string {
	for typ, k := range analyticsKinds {
		if k == kind {
			return typ
		}
	}
	return ""
}

// typeName strips the type's entity prefix: the last dot segment.
func typeName(full string) string {
	for i := len(full) - 1; i >= 0; i-- {
		if full[i] == '.' {
			return full[i+1:]
		}
	}
	return full
}
