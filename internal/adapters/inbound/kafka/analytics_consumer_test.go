package kafka_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	kafkaconsumer "github.com/claudioed/slotting-optimization/internal/adapters/inbound/kafka"
	outboundkafka "github.com/claudioed/slotting-optimization/internal/adapters/outbound/kafka"
	"github.com/claudioed/slotting-optimization/internal/analytics/report"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// fakeProjection is a scripted report.Projection for the analytics consumer.
type fakeProjection struct {
	mu      sync.Mutex
	applied []report.Event
	fail    int // transient failures before success
	reject  bool
}

func (p *fakeProjection) Apply(_ context.Context, e report.Event) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.reject {
		return false, errors.Join(report.ErrRejected, errors.New("integrity violation"))
	}
	if p.fail > 0 {
		p.fail--
		return false, errInjected
	}
	p.applied = append(p.applied, e)
	return true, nil
}

func (p *fakeProjection) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.applied)
}

func (p *fakeProjection) events() []report.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]report.Event(nil), p.applied...)
}

var analyticsWhen = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

// analyticsBytes encodes one domain event as its analytics-stream message
// value under a fixed id.
func analyticsBytes(t *testing.T, id string, ev slotplan.Event) []byte {
	t.Helper()
	msgs, err := (&outboundkafka.AnalyticsEncoder{NewID: func() string { return id }}).Encode(ev)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("encode analytics: %d msgs, %v", len(msgs), err)
	}
	return msgs[0].Value
}

func generatedEvent(plan, site string) slotplan.SlotPlanGenerated {
	return slotplan.SlotPlanGenerated{
		Header: slotplan.Header{Plan: slotplan.PlanID(plan), Site: slotplan.SiteID(site), Version: 1, At: analyticsWhen},
		Policy: slotplan.PolicyABCVelocityV1,
	}
}

func approvedEvent(plan, site, supersedes string) slotplan.SlotPlanApproved {
	return slotplan.SlotPlanApproved{
		Header:           slotplan.Header{Plan: slotplan.PlanID(plan), Site: slotplan.SiteID(site), Version: 2, At: analyticsWhen.Add(time.Hour)},
		SupersedesPlanID: slotplan.PlanID(supersedes),
	}
}

// newAnalyticsConsumer wires a consumer over the scripted reader and DLQ.
func newAnalyticsConsumer(p *fakeProjection, dlq kafkaconsumer.DeadLetterWriter, reader *fakeReader) *kafkaconsumer.AnalyticsConsumer {
	return &kafkaconsumer.AnalyticsConsumer{
		Reader: reader, Projection: p, DLQ: dlq, DLQTopic: "warehouse.slotting-optimization.analytics.dlq",
		Logger: testLogger(), Retry: fastRetry,
	}
}

func TestAnalyticsConsumer_AppliesValidEvents(t *testing.T) {
	ctx := context.Background()
	p := &fakeProjection{}
	c := newAnalyticsConsumer(p, &fakeDLQWriter{}, newFakeReader())
	if err := c.HandleMessage(ctx, kafkago.Message{Value: analyticsBytes(t, "e1", generatedEvent("plan-1", "SITE-1"))}); err != nil {
		t.Fatalf("err = %v", err)
	}
	events := p.events()
	if len(events) != 1 || events[0].Kind != report.KindSlotPlanGenerated ||
		events[0].PlanID != "plan-1" || events[0].SiteID != "SITE-1" || events[0].EventID != "e1" {
		t.Fatalf("applied = %+v", events)
	}

	// A supersedes plan id is carried on approvals only.
	p2 := &fakeProjection{}
	c2 := newAnalyticsConsumer(p2, &fakeDLQWriter{}, newFakeReader())
	if err := c2.HandleMessage(ctx, kafkago.Message{Value: analyticsBytes(t, "e2", approvedEvent("plan-2", "SITE-1", "plan-1"))}); err != nil {
		t.Fatal(err)
	}
	events = p2.events()
	if len(events) != 1 || events[0].Kind != report.KindSlotPlanApproved || events[0].SupersedesPlanID != "plan-1" {
		t.Fatalf("applied = %+v", events)
	}
}

func TestAnalyticsConsumer_SkipsWhatItCannotRoute(t *testing.T) {
	ctx := context.Background()

	// A non-CloudEvents message is skipped, not retried.
	p := &fakeProjection{}
	dlq := &fakeDLQWriter{}
	c := newAnalyticsConsumer(p, dlq, newFakeReader())
	if err := c.HandleMessage(ctx, kafkago.Message{Value: []byte(`{"event_type":"flat"}`)}); err != nil {
		t.Fatalf("err = %v, want nil (skipped)", err)
	}
	if p.count() != 0 || len(dlq.published()) != 0 {
		t.Fatalf("applied = %d, dead-lettered = %d; want none", p.count(), len(dlq.published()))
	}

	// An event of another type is ignored: a CONSUMED type (order-management's)
	// is a valid CloudEvent of an unknown kind on this topic.
	p2 := &fakeProjection{}
	c2 := newAnalyticsConsumer(p2, &fakeDLQWriter{}, newFakeReader())
	foreign := `{"specversion":"1.0","id":"x","source":"/warehouse/order-management",` +
		`"type":"com.warehouse.wes.order-management.siteskudemand.SiteSkuDemandChanged",` +
		`"subject":"o/line/1","time":"2026-10-08T12:00:00Z","datacontenttype":"application/json",` +
		`"dataschema":"urn:warehouse:order-management:events:SiteSkuDemandChanged:v1","data":{}}`
	if err := c2.HandleMessage(ctx, kafkago.Message{Value: []byte(foreign)}); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if p2.count() != 0 {
		t.Fatalf("applied = %d, want 0", p2.count())
	}
}

func TestAnalyticsConsumer_DeadLettersDeterministicFailures(t *testing.T) {
	ctx := context.Background()

	// A payload whose plan id contradicts the subject is dead-lettered.
	p := &fakeProjection{}
	dlq := &fakeDLQWriter{}
	c := newAnalyticsConsumer(p, dlq, newFakeReader())
	msg := kafkago.Message{Partition: 3, Offset: 7, Value: tamperSubject(t, "plan-1", "plan-other")}
	if err := c.HandleMessage(ctx, msg); err != nil {
		t.Fatalf("err = %v, want nil (dead-lettered)", err)
	}
	if p.count() != 0 {
		t.Fatalf("applied = %d, want 0", p.count())
	}
	published := dlq.published()
	if len(published) != 1 || published[0].Topic != c.DLQTopic || string(published[0].Value) != string(msg.Value) {
		t.Fatalf("dead-lettered = %+v", published)
	}
	if headerOf(published[0], "x-dlq-error") == "" || headerOf(published[0], "x-dlq-failed-at") == "" {
		t.Fatal("the dead-lettered message must carry x-dlq-error and x-dlq-failed-at")
	}

	// A store rejection is dead-lettered at once.
	p2 := &fakeProjection{reject: true}
	dlq2 := &fakeDLQWriter{}
	c2 := newAnalyticsConsumer(p2, dlq2, newFakeReader())
	if err := c2.HandleMessage(ctx, kafkago.Message{Value: analyticsBytes(t, "e1", generatedEvent("plan-1", "SITE-1"))}); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(dlq2.published()) != 1 {
		t.Fatalf("dead-lettered = %d, want 1", len(dlq2.published()))
	}
}

func TestAnalyticsConsumer_RetriesTransientFailures(t *testing.T) {
	ctx := context.Background()
	p := &fakeProjection{fail: 1}
	dlq := &fakeDLQWriter{}
	c := newAnalyticsConsumer(p, dlq, newFakeReader())
	err := c.HandleMessage(ctx, kafkago.Message{Value: analyticsBytes(t, "e1", generatedEvent("plan-1", "SITE-1"))})
	if !errors.Is(err, errInjected) {
		t.Fatalf("err = %v, want the injected transient error", err)
	}
	if len(dlq.published()) != 0 {
		t.Fatalf("dead-lettered = %d, want 0", len(dlq.published()))
	}

	// A failed DLQ write is a transient error too (the poison is retried, never lost).
	p2 := &fakeProjection{reject: true}
	c2 := newAnalyticsConsumer(p2, &failingDLQWriter{}, newFakeReader())
	err = c2.HandleMessage(ctx, kafkago.Message{Value: analyticsBytes(t, "e1", generatedEvent("plan-1", "SITE-1"))})
	if err == nil || errors.Is(err, report.ErrRejected) {
		t.Fatalf("err = %v, want the DLQ write failure", err)
	}
}

func headerOf(m kafkago.Message, key string) string {
	for _, h := range m.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}

// tamperSubject re-encodes a valid analytics message with the payload's
// plan_id pointing at a different plan than the envelope subject.
func tamperSubject(t *testing.T, payloadPlan, subjectPlan string) []byte {
	t.Helper()
	value := string(analyticsBytes(t, "e-tamper", generatedEvent(payloadPlan, "SITE-1")))
	return []byte(replaceOnce(value, `"subject":"`+payloadPlan+`"`, `"subject":"`+subjectPlan+`"`))
}

func replaceOnce(s, old, new string) string {
	for i := 0; i+len(old) <= len(s); i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + new + s[i+len(old):]
		}
	}
	return s
}

// failingDLQWriter always fails, like an unreachable DLQ topic.
type failingDLQWriter struct{}

func (failingDLQWriter) WriteMessages(context.Context, ...kafkago.Message) error {
	return errors.New("dlq unreachable")
}
func (failingDLQWriter) Close() error { return nil }

// TestAnalyticsConsumer_RunCommitsPastPoisonAndRetriesTransient: through the
// real run loop, a poison message is dead-lettered and its offset committed;
// a message whose projection fails transiently is retried until it succeeds
// and only then committed.
func TestAnalyticsConsumer_RunCommitsPastPoisonAndRetriesTransient(t *testing.T) {
	poison := kafkago.Message{Partition: 0, Offset: 0, Value: tamperSubject(t, "plan-1", "plan-other")}
	transient := kafkago.Message{Partition: 0, Offset: 1, Value: analyticsBytes(t, "e2", generatedEvent("plan-2", "SITE-1"))}
	reader := newFakeReader(poison.Value, transient.Value)
	// The first Apply of the SECOND message fails once; the poison never
	// reaches Apply (it fails validation).
	p := &fakeProjection{fail: 1}
	dlq := &fakeDLQWriter{}
	c := newAnalyticsConsumer(p, dlq, reader)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	deadline := time.After(10 * time.Second)
	for reader.commits() < 2 {
		select {
		case err := <-done:
			t.Fatalf("Run returned early: %v", err)
		case <-deadline:
			t.Fatalf("timed out; events = %v, applied = %d, dlq = %d", reader.log(), p.count(), len(dlq.published()))
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if p.count() != 1 {
		t.Fatalf("applied = %d, want only the healthy message", p.count())
	}
	if len(dlq.published()) != 1 || dlq.published()[0].Topic != c.DLQTopic {
		t.Fatalf("dead-lettered = %+v, want the poison message once", dlq.published())
	}
	if got := reader.log(); got[len(got)-1] != "commit:1" {
		t.Fatalf("events = %v; the last commit must be the retried message", got)
	}
}

// TestAnalyticsConsumer_SkipWarningIsSampled: every non-CloudEvents skip
// returns nil (committed past); the sampler's own contract (first logged,
// then one per interval with the suppressed count) is pinned by the
// skipSampler test in analytics_consumer_internal_test.go.
func TestAnalyticsConsumer_SkipsNeverFail(t *testing.T) {
	c := newAnalyticsConsumer(&fakeProjection{}, &fakeDLQWriter{}, newFakeReader())
	c.Now = func() time.Time { return analyticsWhen }
	ctx := context.Background()
	for range 3 {
		if err := c.HandleMessage(ctx, kafkago.Message{Value: []byte("nope")}); err != nil {
			t.Fatalf("a skipped message returned %v, want nil", err)
		}
	}
	if p := c.Projection.(*fakeProjection); p.count() != 0 {
		t.Fatalf("applied = %d, want 0", p.count())
	}
}
