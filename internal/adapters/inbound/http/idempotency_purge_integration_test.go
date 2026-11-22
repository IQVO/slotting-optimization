//go:build integration

package http_test

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	inboundhttp "github.com/claudioed/slotting-optimization/internal/adapters/inbound/http"
)

// The purge rule (ADR 0016, addendum 2026-11-22; order-sourcing #84's
// design): the background purge deletes expired key rows in bounded batches,
// never a row a request holds, never blocking one. These tests run against a
// real Postgres, next to the real middleware of the pgFixture.

// seedExpiredKey stores a completed key row that was created age ago (the
// shape the middleware leaves behind, aged past the TTL).
func seedExpiredKey(t *testing.T, f *pgFixture, key string, age time.Duration) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO idempotency_keys
		(key, method, path, request_hash, status_code, response_body, response_headers, created_at, completed_at)
		VALUES ($1, 'POST', '/slot-plans', 'fp', 201, '\x7b7d', '{}', now() - make_interval(secs => $2), now())`,
		key, age.Seconds()); err != nil {
		t.Fatalf("seed %s: %v", key, err)
	}
}

// keyRows returns the keys currently stored, sorted.
func keyRows(t *testing.T, f *pgFixture) []string {
	t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT key FROM idempotency_keys ORDER BY key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return keys
}

// The purge deletes what the read path already ignores (the expired rows)
// and keeps everything live; an empty second pass proves nothing live went.
func TestPurgeDeletesExpiredKeysAndKeepsLiveOnes(t *testing.T) {
	f := newPgFixture(t)
	seedExpiredKey(t, f, "expired-a", inboundhttp.IdempotencyTTL+time.Hour)
	seedExpiredKey(t, f, "expired-b", 48*time.Hour)
	seedExpiredKey(t, f, "live-a", time.Hour)
	seedExpiredKey(t, f, "live-b", inboundhttp.IdempotencyTTL-time.Hour)

	n, err := inboundhttp.PurgeExpiredIdempotencyKeys(context.Background(), f.pool, 100)
	if err != nil || n != 2 {
		t.Fatalf("purge = %d, %v, want the 2 expired rows deleted", n, err)
	}
	if got := keyRows(t, f); !slices.Equal(got, []string{"live-a", "live-b"}) {
		t.Fatalf("rows left = %v, want only the live ones", got)
	}
	if n, err := inboundhttp.PurgeExpiredIdempotencyKeys(context.Background(), f.pool, 100); err != nil || n != 0 {
		t.Fatalf("second purge = %d, %v, want 0: nothing live is ever deleted", n, err)
	}
}

// One call deletes at most one batch; repeated calls drain the backlog to empty.
func TestPurgeDeletesAtMostOneBatchPerCall(t *testing.T) {
	f := newPgFixture(t)
	for i := range 5 {
		seedExpiredKey(t, f, fmt.Sprintf("old-%d", i), 30*time.Hour)
	}
	seedExpiredKey(t, f, "live", time.Minute)

	if n, err := inboundhttp.PurgeExpiredIdempotencyKeys(context.Background(), f.pool, 2); err != nil || n != 2 {
		t.Fatalf("first batch = %d, %v, want 2", n, err)
	}
	if left := len(keyRows(t, f)); left != 4 {
		t.Fatalf("rows after one batch of 2 = %d, want 4 (3 expired + 1 live)", left)
	}
	total := 2
	for {
		n, err := inboundhttp.PurgeExpiredIdempotencyKeys(context.Background(), f.pool, 2)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
		total += n
	}
	if got := keyRows(t, f); total != 5 || !slices.Equal(got, []string{"live"}) {
		t.Fatalf("purged %d rows, left %v, want 5 and only the live key", total, got)
	}
}

// A request claiming an expired key holds the row (its DELETE + INSERT run
// in the request's own transaction). The purge must neither delete that row
// nor wait for the request: it skips a locked row.
func TestPurgeNeverDeletesARowARequestHoldsAndNeverWaitsForIt(t *testing.T) {
	f := newPgFixture(t)
	ctx := context.Background()
	seedExpiredKey(t, f, "held", 30*time.Hour)

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// What the middleware does: claim the expired row, then insert its own
	// (the request's row lock lives until the commit).
	if _, err := tx.Exec(ctx, `DELETE FROM idempotency_keys
		WHERE key = $1 AND created_at <= now() - make_interval(secs => $2)`, "held", inboundhttp.IdempotencyTTL.Seconds()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO idempotency_keys (key, method, path, request_hash)
		VALUES ($1, 'POST', '/slot-plans', 'fp') ON CONFLICT (key) DO NOTHING`, "held"); err != nil {
		t.Fatal(err)
	}

	purgeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if n, err := inboundhttp.PurgeExpiredIdempotencyKeys(purgeCtx, f.pool, 100); err != nil || n != 0 {
		t.Fatalf("purge while the request holds the row = %d, %v, want 0 and no wait", n, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := inboundhttp.PurgeExpiredIdempotencyKeys(ctx, f.pool, 100); err != nil || n != 0 {
		t.Fatalf("purge after the request committed = %d, %v: the row is live now", n, err)
	}
	if got := keyRows(t, f); !slices.Equal(got, []string{"held"}) {
		t.Fatalf("rows = %v, want the claimed key's fresh row", got)
	}
}

// The real middleware on keys whose rows have expired, while the purge runs
// at the same time: every round answers 201 with ONE plan (a duplicate
// replays the claimer's answer), each key executes exactly once, and a
// repeat of a stored key replays.
func TestPurgeRacingARequestOnAnExpiredKeyKeepsTheRequestCorrect(t *testing.T) {
	f := newPgFixture(t)
	const rounds = 15
	plans := map[string]bool{}
	for i := range rounds {
		key := fmt.Sprintf("race-%d", i)
		seedExpiredKey(t, f, key, 30*time.Hour)
		var wg sync.WaitGroup
		results := make([]response, 3)
		for j := range results {
			wg.Add(1)
			go func() { defer wg.Done(); results[j] = f.post(t, "/slot-plans", key, `{}`) }()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = inboundhttp.PurgeExpiredIdempotencyKeys(context.Background(), f.pool, 100)
		}()
		wg.Wait()
		for j, r := range results {
			if r.status != http.StatusCreated {
				t.Fatalf("round %d request %d: answered %d %s, want 201", i, j, r.status, r.raw)
			}
			plans[r.body["planId"].(string)] = true
		}
		if len(plans) != i+1 {
			t.Fatalf("round %d produced %d plans, want one per key: a duplicate must replay the claimer's answer", i, len(plans))
		}
	}
	if left := len(keyRows(t, f)); left != rounds {
		t.Fatalf("%d key rows left, want one live row per key (%d)", left, rounds)
	}
	if again := f.post(t, "/slot-plans", "race-0", `{}`); again.replayed != "true" || again.status != http.StatusCreated {
		t.Fatalf("a repeat of a stored key = %d replayed=%q, want a replay", again.status, again.replayed)
	}
}
