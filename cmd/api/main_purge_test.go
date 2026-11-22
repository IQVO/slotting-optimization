package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	inboundhttp "github.com/claudioed/slotting-optimization/internal/adapters/inbound/http"
	"github.com/jackc/pgx/v5/pgconn"
)

// discardLogger writes nothing: the loop's logs are not under test here.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// scriptedPurge answers each call with the next scripted result.
type scriptedPurge struct {
	results []scripted
	calls   int
}

type scripted struct {
	n   int
	err error
}

func (s *scriptedPurge) purge(context.Context, int) (int, error) {
	if s.calls >= len(s.results) {
		s.calls++
		return 0, nil // nothing left: a short batch ends the pass
	}
	r := s.results[s.calls]
	s.calls++
	return r.n, r.err
}

// A full batch repeats, a short batch ends the pass: two statements, both counted.
func TestPurgeOnceStopsOnAShortBatch(t *testing.T) {
	s := &scriptedPurge{results: []scripted{{n: 500}, {n: 499}}}
	total := purgeOnce(context.Background(), s.purge, discardLogger())
	if total != 999 || s.calls != 2 {
		t.Fatalf("total = %d after %d calls, want 999 after 2", total, s.calls)
	}
}

// A failed pass keeps what it deleted so far and stops: the next tick retries.
func TestPurgeOnceStopsOnAnErrorAndKeepsTheCount(t *testing.T) {
	s := &scriptedPurge{results: []scripted{{err: errors.New("db gone")}}}
	if total := purgeOnce(context.Background(), s.purge, discardLogger()); total != 0 || s.calls != 1 {
		t.Fatalf("total = %d after %d calls, want 0 after 1", total, s.calls)
	}
	s2 := &scriptedPurge{results: []scripted{{n: 500}, {err: errors.New("db gone mid-pass")}}}
	if total := purgeOnce(context.Background(), s2.purge, discardLogger()); total != 500 || s2.calls != 2 {
		t.Fatalf("total = %d after %d calls, want 500 after 2", total, s2.calls)
	}
}

// A cancelled context ends the pass even while full batches keep coming.
func TestPurgeOnceStopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &scriptedPurge{results: []scripted{{n: 500}, {n: 500}, {n: 500}}}
	cancel()
	if total := purgeOnce(ctx, s.purge, discardLogger()); total != 0 || s.calls != 0 {
		t.Fatalf("total = %d after %d calls on a cancelled context, want 0 and 0", total, s.calls)
	}
}

// countingExec counts Exec calls (each answering an empty batch); the count
// is atomic: the purge goroutine writes it, the test reads it.
type countingExec struct{ calls atomic.Int32 }

func (c *countingExec) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	c.calls.Add(1)
	return pgconn.NewCommandTag("DELETE 0"), nil
}

// The worker runs one pass at once, then only on the tick, and stops with stop().
func TestStartIdempotencyPurgeRunsAtOnceThenStops(t *testing.T) {
	db := &countingExec{}
	w, stop := startIdempotencyPurge(db, discardLogger())
	deadline := time.Now().Add(5 * time.Second)
	for db.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if db.calls.Load() == 0 {
		t.Fatal("the purge never ran its at-start pass")
	}
	stop()
	select {
	case <-w.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the purge did not stop after stop()")
	}
	after := db.calls.Load()
	time.Sleep(50 * time.Millisecond)
	if n := db.calls.Load(); n != after {
		t.Fatalf("the purge ran %d more passes after stop", n-after)
	}
}

// Guard: the interval and the batch stay the fleet's fixed values (hourly,
// 500), housekeeping not configuration — expiry is enforced at read.
func TestThePurgeUsesTheFleetIntervalAndBatch(t *testing.T) {
	if idempotencyPurgeInterval != time.Hour {
		t.Fatalf("interval = %s, want 1h", idempotencyPurgeInterval)
	}
	if inboundhttp.IdempotencyPurgeBatch != 500 {
		t.Fatalf("batch = %d, want 500", inboundhttp.IdempotencyPurgeBatch)
	}
}
