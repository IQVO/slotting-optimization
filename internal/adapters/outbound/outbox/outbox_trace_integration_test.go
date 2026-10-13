//go:build integration

// Gap fix D27 (ADR 0009): the W3C trace context of the request that wrote an
// outbox row crosses the outbox to Kafka. Real Postgres and a real Kafka
// broker, both through testcontainers (internal/testing/pgtest and
// kafkatest; never an env-provided database or broker address).
package outbox_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/claudioed/slotting-optimization/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/clock"
	outboundkafka "github.com/claudioed/slotting-optimization/internal/adapters/outbound/kafka"
	relay "github.com/claudioed/slotting-optimization/internal/adapters/outbound/outbox"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/postgres"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/testing/kafkatest"
	"github.com/claudioed/slotting-optimization/internal/testing/pgtest"
	"github.com/claudioed/slotting-optimization/internal/testing/repocontract"
)

// installW3CTracing installs a real SDK tracer provider and the W3C
// trace-context propagator globally (what telemetry.Setup does in
// production), restoring the previous globals when the test ends.
func installW3CTracing(t *testing.T) trace.Tracer {
	t.Helper()
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	tp := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})
	return tp.Tracer("outbox-trace-itest")
}

// TestOutboxTrace_TraceparentCrossesTheOutboxToKafka: a use case runs inside
// a request span; the outbox row stores that span's traceparent in
// outbox_events.headers; the relay (a later pass, no span of its own, a
// background context) sends it as a Kafka header; and a consumer extracting
// it with the global propagator lands on the request's trace and span.
func TestOutboxTrace_TraceparentCrossesTheOutboxToKafka(t *testing.T) {
	tracer := installW3CTracing(t)
	ctx := context.Background()
	pool := pgtest.NewPool(t)
	brokers := kafkatest.Brokers(t)
	topic := kafkatest.Topic(t, outboundkafka.Topic)

	plans := postgres.NewSlotPlanRepo(pool)
	if err := plans.Save(ctx, repocontract.Draft(t, "plan-trace-1", "SITE-1", repocontract.T0), 0); err != nil {
		t.Fatal(err)
	}
	w := usecases.Writer{
		Plans: plans, Outbox: postgres.NewOutboxRepo(pool),
		Encoder: &outboundkafka.Encoder{Topic: topic}, UoW: postgres.NewUnitOfWork(pool), Clock: clock.System{},
	}
	reqCtx, reqSpan := tracer.Start(ctx, "POST /slot-plans/{planId}/approve", trace.WithSpanKind(trace.SpanKindServer))
	_, err := (&usecases.ApprovePlan{Writer: w}).Handle(reqCtx, "plan-trace-1")
	reqSpan.End()
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	want := reqSpan.SpanContext()
	wantTraceparent := fmt.Sprintf("00-%s-%s-01", want.TraceID(), want.SpanID())

	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT headers FROM outbox_events WHERE topic = $1`, topic).Scan(&raw); err != nil {
		t.Fatalf("read stored headers: %v", err)
	}
	var stored []map[string]string
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("decode stored headers %s: %v", raw, err)
	}
	if len(stored) != 2 || stored[0]["key"] != "content-type" || stored[1]["key"] != "traceparent" || stored[1]["value"] != wantTraceparent {
		t.Fatalf("outbox row headers = %s, want content-type first, then the request span's traceparent %q", raw, wantTraceparent)
	}

	sink := outboundkafka.NewRelaySink(brokers)
	t.Cleanup(func() { _ = sink.Close() })
	r := relay.NewRelay(postgres.NewOutboxRepo(pool), sink, quiet(), relay.WithMaxAttempts(relay.DefaultMaxAttempts))
	// The relay runs without the request's context: continuity must come
	// from the stored row, not from the caller.
	if n, err := r.RelayOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("relay: n=%d err=%v", n, err)
	}

	msg := kafkatest.ReadN(t, topic, 1, 60*time.Second)[0]
	if got := msg.Headers["traceparent"]; got != wantTraceparent {
		t.Fatalf("Kafka traceparent = %q, want %q (headers %v)", got, wantTraceparent, msg.Headers)
	}
	if msg.Headers["content-type"] != cloudevents.MediaType {
		t.Fatalf("content-type header lost on the outbox path: %v", msg.Headers)
	}
	consumerCtx := otel.GetTextMapPropagator().Extract(context.Background(), propagation.MapCarrier(msg.Headers))
	parent := trace.SpanContextFromContext(consumerCtx)
	if parent.TraceID() != want.TraceID() || parent.SpanID() != want.SpanID() || !parent.IsRemote() {
		t.Fatalf("consumer would parent on %s/%s (remote=%v), want the request span %s/%s",
			parent.TraceID(), parent.SpanID(), parent.IsRemote(), want.TraceID(), want.SpanID())
	}
}

// TestOutboxTrace_RowWithoutTraceHeadersRelaysUnchanged pins the other half
// of D27: a row written with no trace context (no active span, or a row that
// predates trace capture) relays with exactly its stored headers, key and
// value, and carries no traceparent a consumer could mis-parent onto.
func TestOutboxTrace_RowWithoutTraceHeadersRelaysUnchanged(t *testing.T) {
	installW3CTracing(t)
	ctx := context.Background()
	pool := pgtest.NewPool(t)
	brokers := kafkatest.Brokers(t)
	topic := kafkatest.Topic(t, outboundkafka.Topic)

	if _, err := pool.Exec(ctx, `
		INSERT INTO outbox_events (event_id, topic, event_type, subject, key, dataschema, value)
		VALUES ('evt-untraced', $1, 'com.example.Untraced', 'plan-untraced', 'plan-untraced', 'urn:test', '{}')
	`, topic); err != nil {
		t.Fatalf("insert outbox row: %v", err)
	}

	sink := outboundkafka.NewRelaySink(brokers)
	t.Cleanup(func() { _ = sink.Close() })
	r := relay.NewRelay(postgres.NewOutboxRepo(pool), sink, quiet(), relay.WithMaxAttempts(relay.DefaultMaxAttempts))
	if n, err := r.RelayOnce(ctx); err != nil || n != 1 {
		t.Fatalf("relay: n=%d err=%v", n, err)
	}

	msg := kafkatest.ReadN(t, topic, 1, 60*time.Second)[0]
	if msg.Key != "plan-untraced" || string(msg.Value) != `{}` {
		t.Fatalf("row relayed changed: key=%q value=%q", msg.Key, msg.Value)
	}
	if len(msg.Headers) != 0 {
		t.Fatalf("a row without stored headers must relay with none, got %v", msg.Headers)
	}
}
