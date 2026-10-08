package kafka_test

import (
	"context"
	"reflect"
	"testing"
)

// End to end through Consumer.Run with a scripted reader: the offset is
// committed only after the effect, in order, poison messages included.
func TestRun_CommitsEveryMessageInOrderAfterHandlingIt(t *testing.T) {
	c := newCopies()
	consumer := c.demandConsumer("")
	reader := newFakeReader(
		demandMsg("e1", "ord-1", 1, "SITE-1", "SKU-1", 12, "ACTIVE"),
		[]byte(legacyFlat),
		demandMsg("e3", "ord-2", 1, "SITE-1", "SKU-2", 3, "ACTIVE"),
	)
	if err := runUntilCommits(t, consumer, reader, 3); err != context.Canceled {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	want := []string{"fetch:0", "commit:0", "fetch:1", "commit:1", "fetch:2", "commit:2"}
	if got := reader.log(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events\n got  %v\n want %v", got, want)
	}
	if len(c.velocity(t, "SITE-1")) != 2 {
		t.Fatal("both valid lines must be stored")
	}
}

func TestRun_ATransientFailureRetriesTheSameMessageThenCommits(t *testing.T) {
	c := newCopies()
	c.demand.fail = 2
	consumer := c.demandConsumer("")
	reader := newFakeReader(demandMsg("e1", "ord-1", 1, "SITE-1", "SKU-1", 12, "ACTIVE"))
	if err := runUntilCommits(t, consumer, reader, 1); err != context.Canceled {
		t.Fatalf("Run = %v", err)
	}
	if got, want := reader.log(), []string{"fetch:0", "commit:0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want one fetch and one commit AFTER the third attempt", got)
	}
	if c.demand.callCount() != 3 {
		t.Fatalf("attempts = %d, want 3", c.demand.callCount())
	}
	if got := c.velocity(t, "SITE-1"); len(got) != 1 || got[0].Picks != 1 {
		t.Fatalf("a retried message must be applied exactly once: %+v", got)
	}
}

func TestRun_AMessageThatNeverSucceedsIsDeadLetteredAndTheLoopMovesOn(t *testing.T) {
	c := newCopies()
	c.demand.fail = 1 << 30
	consumer := c.demandConsumer("")
	dlq := &fakeDLQWriter{}
	consumer.DLQ = dlq
	reader := newFakeReader(demandMsg("e1", "ord-1", 1, "SITE-1", "SKU-1", 12, "ACTIVE"))
	if err := runUntilCommits(t, consumer, reader, 1); err != context.Canceled {
		t.Fatalf("Run = %v", err)
	}
	published := dlq.published()
	if len(published) != 1 || published[0].Topic != "warehouse.order-management.events.dlq" {
		t.Fatalf("DLQ = %+v", published)
	}
	headers := map[string]bool{}
	for _, h := range published[0].Headers {
		headers[h.Key] = true
	}
	for _, k := range []string{"x-dlq-source-topic", "x-dlq-source-partition", "x-dlq-source-offset", "x-dlq-error", "x-dlq-failed-at"} {
		if !headers[k] {
			t.Errorf("DLQ message lacks header %s", k)
		}
	}
	if calls := c.demand.callCount(); calls != 5 {
		t.Fatalf("attempts = %d, want the bounded 5", calls)
	}
	if c.processed.Has("slotting-demand", "e1") {
		t.Fatal("a dead-lettered message must not be marked processed")
	}
}

func TestClose_ReleasesReaderAndDeadLetterWriter(t *testing.T) {
	c := newCopies()
	consumer := c.demandConsumer("")
	consumer.Reader = newFakeReader()
	consumer.DLQ = &fakeDLQWriter{}
	if err := consumer.Close(); err != nil {
		t.Fatal(err)
	}
	consumer.DLQ = nil
	if err := consumer.Close(); err != nil {
		t.Fatal(err)
	}
}
