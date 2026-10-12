package kafka_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	kafkaconsumer "github.com/claudioed/slotting-optimization/internal/adapters/inbound/kafka"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/memory"
	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fastRetry keeps Run-loop tests quick while still exercising backoff.
var fastRetry = kafkaconsumer.RetryPolicy{Initial: time.Millisecond, Max: 4 * time.Millisecond}

var errInjected = errors.New("injected transient database failure")

// flakyDemand wraps the in-memory demand ledger and fails the first `fail`
// Apply calls AFTER applying the change, so the unit of work must roll the
// half-done effect (and the processed-event claim) back.
type flakyDemand struct {
	*memory.DemandLedger
	mu    sync.Mutex
	fail  int
	calls int
}

func (f *flakyDemand) Apply(ctx context.Context, l repository.DemandLine) error {
	if err := f.DemandLedger.Apply(ctx, l); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail > 0 {
		f.fail--
		return errInjected
	}
	return nil
}

func (f *flakyDemand) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// countingUoW counts how often a unit of work was opened, proving a
// deterministic bad message never even touches the database.
type countingUoW struct {
	inner interface {
		Do(ctx context.Context, fn func(context.Context) error) error
	}
	mu    sync.Mutex
	calls int
}

func (u *countingUoW) Do(ctx context.Context, fn func(context.Context) error) error {
	u.mu.Lock()
	u.calls++
	u.mu.Unlock()
	return u.inner.Do(ctx, fn)
}

func (u *countingUoW) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls
}

// copies is the whole read side the three consumers write to.
type copies struct {
	demand    *flakyDemand
	profiles  *memory.ProfileDirectory
	catalogue *memory.SlotCatalogue
	processed *memory.ProcessedEventRepo
	uow       *countingUoW
}

func newCopies() *copies {
	demand := &flakyDemand{DemandLedger: memory.NewDemandLedger()}
	profiles, cat, processed := memory.NewProfileDirectory(), memory.NewSlotCatalogue(), memory.NewProcessedEventRepo()
	return &copies{
		demand: demand, profiles: profiles, catalogue: cat, processed: processed,
		uow: &countingUoW{inner: memory.NewUnitOfWork(demand.DemandLedger, profiles, cat, processed)},
	}
}

func (c *copies) intake() usecases.Intake {
	return usecases.Intake{UoW: c.uow, Processed: c.processed}
}

func (c *copies) demandConsumer(onlySite string) *kafkaconsumer.Consumer {
	uc := &usecases.ApplyDemandChanged{Intake: c.intake(), Demand: c.demand, OnlySite: onlySite}
	return &kafkaconsumer.Consumer{Name: "demand", Topic: kafkaconsumer.DemandTopic, Handlers: kafkaconsumer.DemandHandlers(uc, testLogger()), Logger: testLogger(), Retry: fastRetry}
}

func (c *copies) productConsumer() *kafkaconsumer.Consumer {
	cls := &usecases.ApplyProductClassified{Intake: c.intake(), Profiles: c.profiles}
	phys := &usecases.ApplyPhysicalProfile{Intake: c.intake(), Profiles: c.profiles}
	return &kafkaconsumer.Consumer{Name: "product", Topic: kafkaconsumer.ProductTopic, Handlers: kafkaconsumer.ProductHandlers(cls, phys, testLogger()), Logger: testLogger(), Retry: fastRetry}
}

func (c *copies) layoutConsumer() *kafkaconsumer.Consumer {
	zone := &usecases.ApplyZoneRegistered{Intake: c.intake(), Catalogue: c.catalogue}
	reg := &usecases.ApplyLocationSlotRegistered{Intake: c.intake(), Catalogue: c.catalogue}
	dec := &usecases.ApplyLocationSlotDecommissioned{Intake: c.intake(), Catalogue: c.catalogue}
	return &kafkaconsumer.Consumer{Name: "layout", Topic: kafkaconsumer.LayoutTopic, Handlers: kafkaconsumer.LayoutHandlers(zone, reg, dec, testLogger()), Logger: testLogger(), Retry: fastRetry}
}

var window = [2]time.Time{time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)}

func (c *copies) velocity(t *testing.T, site string) []planning.SkuVelocity {
	t.Helper()
	v, err := c.demand.Velocity(context.Background(), slotplan.SiteID(site), window[0], window[1])
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// fakeReader is a scripted kafkaconsumer.Reader. It records every
// FetchMessage/CommitMessages in order so a test can assert that the offset
// was committed only after the handler succeeded, and exactly once. After the
// scripted messages it blocks until ctx is cancelled, like a real reader on an
// idle topic.
type fakeReader struct {
	mu       sync.Mutex
	msgs     []kafkago.Message
	next     int
	events   []string
	onCommit func()
}

func newFakeReader(values ...[]byte) *fakeReader {
	r := &fakeReader{}
	for i, v := range values {
		r.msgs = append(r.msgs, kafkago.Message{Partition: 0, Offset: int64(i), Value: v, Topic: "t"})
	}
	return r
}

func (r *fakeReader) FetchMessage(ctx context.Context) (kafkago.Message, error) {
	r.mu.Lock()
	if r.next < len(r.msgs) {
		m := r.msgs[r.next]
		r.next++
		r.events = append(r.events, fmt.Sprintf("fetch:%d", m.Offset))
		r.mu.Unlock()
		return m, nil
	}
	r.mu.Unlock()
	<-ctx.Done()
	return kafkago.Message{}, ctx.Err()
}

func (r *fakeReader) CommitMessages(_ context.Context, msgs ...kafkago.Message) error {
	r.mu.Lock()
	for _, m := range msgs {
		r.events = append(r.events, fmt.Sprintf("commit:%d", m.Offset))
	}
	fn := r.onCommit
	r.mu.Unlock()
	if fn != nil {
		fn()
	}
	return nil
}

func (r *fakeReader) Close() error { return nil }

func (r *fakeReader) log() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func (r *fakeReader) commits() int {
	n := 0
	for _, e := range r.log() {
		if len(e) > 6 && e[:6] == "commit" {
			n++
		}
	}
	return n
}

// fakeDLQWriter records every message WriteMessages publishes.
type fakeDLQWriter struct {
	mu   sync.Mutex
	msgs []kafkago.Message
}

func (w *fakeDLQWriter) WriteMessages(_ context.Context, msgs ...kafkago.Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.msgs = append(w.msgs, msgs...)
	return nil
}

func (w *fakeDLQWriter) Close() error { return nil }

func (w *fakeDLQWriter) published() []kafkago.Message {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]kafkago.Message(nil), w.msgs...)
}

// runUntilCommits runs c.Run until the reader saw `want` commits (or fails
// after 10s), then cancels and returns Run's error.
func runUntilCommits(t *testing.T, c *kafkaconsumer.Consumer, reader *fakeReader, want int) error {
	t.Helper()
	c.Reader = reader
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	deadline := time.After(10 * time.Second)
	for reader.commits() < want {
		select {
		case err := <-done:
			return err
		case <-deadline:
			t.Fatalf("timed out waiting for %d commits; events=%v", want, reader.log())
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
		return nil
	}
}
