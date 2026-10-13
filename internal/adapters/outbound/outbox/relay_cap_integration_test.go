//go:build integration

package outbox_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	relay "github.com/claudioed/slotting-optimization/internal/adapters/outbound/outbox"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/postgres"
	"github.com/claudioed/slotting-optimization/internal/testing/pgtest"
)

func failingPasses(t *testing.T, r *relay.Relay, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		published, err := r.RelayOnce(context.Background())
		if err == nil || published != 0 {
			t.Fatalf("pass %d: want the poison row to fail with nothing published, got published=%d err=%v", i, published, err)
		}
	}
}

// TestOutboxRelay_MaxAttempts_PoisonRowExcludedThenGoodRowPublishes is the
// gap-fix D12 reference test (ADR 0008) over real Postgres: a poison row
// blocks the relay only until it has failed maxAttempts times. The pass that
// records the last attempt logs it at ERROR with id, topic, type and key;
// from then on the row is excluded from claims (still in outbox_events with
// attempts and last_error) and the good row behind it, same key, publishes.
func TestOutboxRelay_MaxAttempts_PoisonRowExcludedThenGoodRowPublishes(t *testing.T) {
	pool := pgtest.NewPool(t)
	ctx := context.Background()
	poisonID := pgtest.InsertOutboxRow(t, pool, "Poison", "plan-cap-1")
	goodID := pgtest.InsertOutboxRow(t, pool, "Good", "plan-cap-1")

	var logs bytes.Buffer
	sink := &poisonSink{failOn: "Poison"}
	r := relay.NewRelay(postgres.NewOutboxRepo(pool), sink, slog.New(slog.NewJSONHandler(&logs, nil)), relay.WithMaxAttempts(3))

	failingPasses(t, r, 2)
	if recs := capRecords(t, &logs); len(recs) != 0 {
		t.Fatalf("no attempt-cap ERROR expected below the cap, got %v", recs)
	}
	failingPasses(t, r, 1)
	recs := capRecords(t, &logs)
	if len(recs) != 1 {
		t.Fatalf("want exactly 1 attempt-cap ERROR record, got %d", len(recs))
	}
	if got, _ := recs[0]["outbox_id"].(float64); int64(got) != poisonID {
		t.Errorf("outbox_id = %v, want %d", recs[0]["outbox_id"], poisonID)
	}
	for field, want := range map[string]string{"topic": "test.integration", "event_type": "Poison", "key": "plan-cap-1"} {
		if recs[0][field] != want {
			t.Errorf("%s = %v, want %q", field, recs[0][field], want)
		}
	}

	if published, err := r.RelayOnce(ctx); err != nil || published != 1 {
		t.Fatalf("pass after the cap: want the good row published, got published=%d err=%v", published, err)
	}
	if got := sink.sentTypes(); len(got) != 1 || got[0] != "Good" {
		t.Fatalf("sink got %v, want only the Good row", got)
	}
	if _, _, ok := pgtest.OutboxRowState(t, pool, goodID); !ok {
		t.Fatal("the good row is not marked published")
	}
	if published, err := r.RelayOnce(ctx); err != nil || published != 0 {
		t.Fatalf("pass after drain: published=%d err=%v, want a no-op", published, err)
	}
	attempts, lastErr, ok := pgtest.OutboxRowState(t, pool, poisonID)
	if ok || attempts != 3 || lastErr != "message too large" {
		t.Fatalf("poison row: attempts=%d last_error=%q published=%v, want 3, the send error, unpublished", attempts, lastErr, ok)
	}
}

// TestOutboxRelay_MaxAttempts_ReArmedRowIsClaimedAgain proves the runbook's
// re-arm SQL: resetting attempts to 0 makes an excluded row claimable again.
func TestOutboxRelay_MaxAttempts_ReArmedRowIsClaimedAgain(t *testing.T) {
	pool := pgtest.NewPool(t)
	ctx := context.Background()
	poisonID := pgtest.InsertOutboxRow(t, pool, "Poison", "plan-cap-2")

	sink := &poisonSink{failOn: "Poison"}
	r := relay.NewRelay(postgres.NewOutboxRepo(pool), sink, quiet(), relay.WithMaxAttempts(1))
	failingPasses(t, r, 1)
	if published, err := r.RelayOnce(ctx); err != nil || published != 0 {
		t.Fatalf("capped row was claimed again: published=%d err=%v", published, err)
	}

	if _, err := pool.Exec(ctx, `UPDATE outbox_events SET attempts = 0 WHERE id = $1`, poisonID); err != nil {
		t.Fatalf("re-arm: %v", err)
	}
	sink.mu.Lock()
	sink.failOn = ""
	sink.mu.Unlock()
	if published, err := r.RelayOnce(ctx); err != nil || published != 1 {
		t.Fatalf("re-armed row: published=%d err=%v, want it published", published, err)
	}
}

// TestOutboxRelay_NoCap_PoisonRowBlocksLaterRows pins WithMaxAttempts(0)
// (OUTBOX_MAX_ATTEMPTS=0, unlimited): the poison row is retried forever and
// the row behind it never publishes. This is why every cmd/* relay must
// pass a cap (architecture fitness test TestOutboxRelaysPassAttemptCap).
func TestOutboxRelay_NoCap_PoisonRowBlocksLaterRows(t *testing.T) {
	pool := pgtest.NewPool(t)
	pgtest.InsertOutboxRow(t, pool, "Poison", "plan-cap-3")
	goodID := pgtest.InsertOutboxRow(t, pool, "Good", "plan-cap-3")

	sink := &poisonSink{failOn: "Poison"}
	r := relay.NewRelay(postgres.NewOutboxRepo(pool), sink, quiet(), relay.WithMaxAttempts(0))
	failingPasses(t, r, 5)
	if _, _, ok := pgtest.OutboxRowState(t, pool, goodID); ok || len(sink.sentTypes()) != 0 {
		t.Fatalf("with no cap the good row must stay blocked, published=%v sent=%d", ok, len(sink.sentTypes()))
	}
}
