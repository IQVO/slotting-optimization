package kafka

import (
	"context"
	"errors"
	"testing"

	segmentio "github.com/segmentio/kafka-go"

	"github.com/claudioed/slotting-optimization/internal/application/outbox"
)

type fakeWriter struct {
	got []segmentio.Message
	err error
}

func (w *fakeWriter) WriteMessages(_ context.Context, msgs ...segmentio.Message) error {
	w.got = append(w.got, msgs...)
	return w.err
}

func TestRelaySink_WritesKeyValueHeadersAndTopic(t *testing.T) {
	w := &fakeWriter{}
	sink := NewRelaySinkWithWriter(w)
	err := sink.Send(context.Background(), outbox.Message{
		Topic: "t", Key: []byte("SKU-1"), Value: []byte("v"), EventType: "x",
		Headers: []outbox.Header{{Key: "content-type", Value: "application/cloudevents+json; charset=UTF-8"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(w.got) != 1 {
		t.Fatalf("wrote %d", len(w.got))
	}
	m := w.got[0]
	if m.Topic != "t" || string(m.Key) != "SKU-1" || string(m.Value) != "v" || len(m.Headers) != 1 ||
		m.Headers[0].Key != "content-type" || string(m.Headers[0].Value) != "application/cloudevents+json; charset=UTF-8" {
		t.Fatalf("message = %+v", m)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("close of a fake writer: %v", err)
	}
}

func TestRelaySink_Errors(t *testing.T) {
	w := &fakeWriter{}
	sink := NewRelaySinkWithWriter(w)
	if err := sink.Send(context.Background()); err != nil || len(w.got) != 0 {
		t.Fatalf("empty send: %v, wrote %d", err, len(w.got))
	}
	if err := sink.Send(context.Background(), outbox.Message{EventType: "x"}); err == nil {
		t.Fatal("a message without a topic must be rejected")
	}
	boom := errors.New("boom")
	w.err = boom
	if err := sink.Send(context.Background(), outbox.Message{Topic: "t"}); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestNewRelaySink_UsesTheFleetSyncWriterSettings(t *testing.T) {
	sink := NewRelaySink([]string{"127.0.0.1:1"})
	w, ok := sink.writer.(*segmentio.Writer)
	if !ok {
		t.Fatalf("writer = %T", sink.writer)
	}
	if w.Topic != "" || w.RequiredAcks != segmentio.RequireAll || w.BatchTimeout != syncWriterBatchTimeout || !w.AllowAutoTopicCreation {
		t.Fatalf("writer settings = %+v", w)
	}
	if _, ok := w.Balancer.(*segmentio.Hash); !ok {
		t.Fatalf("balancer = %T, want Hash (key = plan id)", w.Balancer)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
}
