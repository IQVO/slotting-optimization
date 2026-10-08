//go:build integration

package outbox_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/slotting-optimization/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/clock"
	outboundkafka "github.com/claudioed/slotting-optimization/internal/adapters/outbound/kafka"
	relay "github.com/claudioed/slotting-optimization/internal/adapters/outbound/outbox"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/postgres"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
	"github.com/claudioed/slotting-optimization/internal/testing/kafkatest"
	"github.com/claudioed/slotting-optimization/internal/testing/pgtest"
	"github.com/claudioed/slotting-optimization/internal/testing/repocontract"
)

// The relay is proven end to end against real infrastructure, both started
// with testcontainers (never skip-gated, never a hardcoded broker address):
// use cases write plans + outbox rows in real Postgres, the relay drains them
// to a real broker on a unique topic, and the CloudEvents are read back.

func TestRelay_RealPostgresAndKafka_PublishesCloudEventsKeyedByPlanID(t *testing.T) {
	ctx := context.Background()
	brokers := kafkatest.Brokers(t)
	pool := pgtest.NewPool(t)
	topic := kafkatest.Topic(t, outboundkafka.Topic)

	plans := postgres.NewSlotPlanRepo(pool)
	w := usecases.Writer{
		Plans: plans, Outbox: postgres.NewOutboxRepo(pool),
		Encoder: &outboundkafka.Encoder{Topic: topic}, UoW: postgres.NewUnitOfWork(pool), Clock: clock.System{},
	}
	for _, p := range []*slotplan.SlotPlan{
		repocontract.Draft(t, "plan-a", "SITE-1", repocontract.T0),
		repocontract.Draft(t, "plan-b", "SITE-1", repocontract.T0.Add(time.Minute)),
	} {
		if err := plans.Save(ctx, p, 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (&usecases.ApprovePlan{Writer: w}).Handle(ctx, "plan-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := (&usecases.RejectPlan{Writer: w}).Handle(ctx, "plan-b", "not now"); err != nil {
		t.Fatal(err)
	}

	sink := outboundkafka.NewRelaySink(brokers)
	t.Cleanup(func() { _ = sink.Close() })
	r := relay.NewRelay(postgres.NewOutboxRepo(pool), sink, slog.New(slog.NewTextHandler(io.Discard, nil)), relay.WithInterval(100*time.Millisecond))
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = r.Run(runCtx) }()
	t.Cleanup(func() { stop(); <-done })

	reader := kafkago.NewReader(kafkago.ReaderConfig{Brokers: brokers, Topic: topic, Partition: 0, MinBytes: 1, MaxBytes: 1 << 20})
	t.Cleanup(func() { _ = reader.Close() })
	readCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	prefix := "com.warehouse.wms.slotting-optimization.slotplan."
	for i, want := range []struct{ typ, plan string }{{prefix + "SlotPlanApproved", "plan-a"}, {prefix + "SlotPlanRejected", "plan-b"}} {
		msg, err := reader.ReadMessage(readCtx)
		if err != nil {
			t.Fatalf("read message %d: %v", i, err)
		}
		assertCloudEvent(t, msg, want.typ, want.plan)
	}
	pgtest.WaitAllPublished(t, pool)
}

// assertCloudEvent checks the key, the content-type header and the decoded
// CloudEvents attributes of one consumed message.
func assertCloudEvent(t *testing.T, msg kafkago.Message, wantType, wantPlan string) {
	t.Helper()
	if string(msg.Key) != wantPlan {
		t.Errorf("key = %q, want %s", msg.Key, wantPlan)
	}
	if len(msg.Headers) != 1 || msg.Headers[0].Key != "content-type" || string(msg.Headers[0].Value) != cloudevents.MediaType {
		t.Errorf("headers = %+v", msg.Headers)
	}
	e, err := cloudevents.Decode(msg.Value)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if e.Type() != wantType || e.Subject() != wantPlan || e.Source() != "/warehouse/slotting-optimization" {
		t.Fatalf("event = type %q subject %q source %q, want type %q subject %q", e.Type(), e.Subject(), e.Source(), wantType, wantPlan)
	}
}
