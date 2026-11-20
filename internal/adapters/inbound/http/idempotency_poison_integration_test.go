//go:build integration

package http_test

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	inboundhttp "github.com/claudioed/slotting-optimization/internal/adapters/inbound/http"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/postgres/pgtx"
	"github.com/claudioed/slotting-optimization/internal/testing/pgtest"
)

// The tcpostgres reference keeps the testcontainers postgres module import
// reachable in this package's directory (required by internal/architecture's
// TestPostgresIntegrationTestsUseTestcontainers for any integration test
// package that talks to pgx).
var _ = tcpostgres.Run

// poisonTx simulates what a mapped domain conflict does to the middleware's
// transaction: a statement fails on the server (a unique violation, exactly
// like a racing approval hitting uq_slot_plans_one_approved_per_site), the
// repository translates it to a typed error, and the handler answers 4xx --
// but the transaction is ABORTED from then on. The middleware must still
// record and return that truthful 4xx, never a 500.
func poisonTx(t *testing.T, r *http.Request) {
	t.Helper()
	tx, ok := pgtx.From(r.Context())
	if !ok {
		t.Fatal("the middleware did not bind a transaction into the request context")
	}
	for i := 0; i < 2; i++ {
		if _, err := tx.Exec(r.Context(), `INSERT INTO idempotency_keys (key, method, path, request_hash) VALUES ('dup', 'm', 'p', 'h')`); err != nil {
			return // the second insert aborts the transaction (23505)
		}
	}
	t.Fatal("the duplicate insert did not abort the transaction")
}

// poisonedFinalRefusalHandler poisons the middleware's transaction (a failed
// statement ABORTS it, like a racing approval hitting the unique index) and
// then answers 409 plan-not-draft through the PRODUCTION error writer, so the
// answer carries the class its catalogue entry declares (FINAL).
func poisonedFinalRefusalHandler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		poisonTx(t, r)
		inboundhttp.RefuseForTest(w, r, "plan-not-draft", http.StatusConflict)
	})
}

// handWrittenConflictHandler answers a 409 problem+json BY HAND: no catalogue
// entry, no class. The rule's safe fallback: it is released like RETRYABLE.
func handWrittenConflictHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"type":"https://errors.slotting-optimization.warehouse-systems.dev/approved-plan-conflict","title":"Conflict","status":409}`))
	})
}

func keyedRequest(key string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/slot-plans/plan-x/approvals", nil)
	req.Header.Set(inboundhttp.IdempotencyKeyHeader, key)
	return req
}

// TestIdempotency_AFinal409FromAnAbortedTransactionCommitsTheOutcome: a mapped
// domain conflict that is FINAL for its request (409 plan-not-draft, ADR 0016)
// answers 409, not 500, even though the failed statement ABORTED the
// transaction; the savepoint healing still lets that 409 be recorded and
// replayed -- a retry with the same key must NOT re-run the handler.
func TestIdempotency_AFinal409FromAnAbortedTransactionCommitsTheOutcome(t *testing.T) {
	pool := pgtest.NewPool(t)
	mw := inboundhttp.RequireIdempotencyKey(pool, pgtx.With)(poisonedFinalRefusalHandler(t))

	first := httptest.NewRecorder()
	mw.ServeHTTP(first, keyedRequest("k-poison"))
	if first.Code != http.StatusConflict {
		t.Fatalf("first = %d %s, want the truthful 409", first.Code, first.Body.String())
	}

	var statusCode int
	if err := pool.QueryRow(t.Context(), `SELECT status_code FROM idempotency_keys WHERE key = 'k-poison'`).Scan(&statusCode); err != nil {
		t.Fatalf("the FINAL 409 outcome was not committed: %v", err)
	}
	if statusCode != http.StatusConflict {
		t.Fatalf("cached status = %d", statusCode)
	}

	replay := httptest.NewRecorder()
	mw.ServeHTTP(replay, keyedRequest("k-poison"))
	if replay.Code != http.StatusConflict || replay.Header().Get(inboundhttp.IdempotencyReplayedHeader) != "true" {
		t.Fatalf("replay = %d replayed=%q, want the cached 409", replay.Code, replay.Header().Get(inboundhttp.IdempotencyReplayedHeader))
	}
	if replay.Body.String() != first.Body.String() {
		t.Fatalf("replay body = %s, want %s", replay.Body.String(), first.Body.String())
	}
}

// TestIdempotency_AnUnclassified4xxFromAnAbortedTransactionIsReleased: a
// hand-written problem answer declares no class; the safe fallback (ADR 0016)
// releases it like RETRYABLE -- the key row is rolled back with the aborted
// transaction and a retry under the same key executes again.
func TestIdempotency_AnUnclassified4xxFromAnAbortedTransactionIsReleased(t *testing.T) {
	pool := pgtest.NewPool(t)
	var calls atomic.Int64
	handler := handWrittenConflictHandler()
	mw := inboundhttp.RequireIdempotencyKey(pool, pgtx.With)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if calls.Load() == 1 {
			poisonTx(t, r)
		}
		handler.ServeHTTP(w, r)
	}))

	first := httptest.NewRecorder()
	mw.ServeHTTP(first, keyedRequest("k-unc"))
	if first.Code != http.StatusConflict {
		t.Fatalf("first = %d %s, want the hand-written 409 answered", first.Code, first.Body.String())
	}
	if n := queryInt(t, pool, `SELECT count(*) FROM idempotency_keys WHERE key = 'k-unc'`); n != 0 {
		t.Fatalf("an unclassified refusal left %d key rows: the safe fallback releases it", n)
	}

	again := httptest.NewRecorder()
	mw.ServeHTTP(again, keyedRequest("k-unc"))
	if again.Code != http.StatusConflict || again.Header().Get(inboundhttp.IdempotencyReplayedHeader) != "" {
		t.Fatalf("retry = %d replayed=%q, want the released key to execute again", again.Code, again.Header().Get(inboundhttp.IdempotencyReplayedHeader))
	}
	if calls.Load() != 2 {
		t.Fatalf("handler ran %d times, want 2", calls.Load())
	}
}

// TestIdempotency_A2xxFromAnAbortedTransactionIsNeverCached: a success answer
// whose writes died with the transaction is a LIE if returned or cached.
// Roll everything back and fail the request, so the same key retries the
// real work.
func TestIdempotency_A2xxFromAnAbortedTransactionIsNeverCached(t *testing.T) {
	pool := pgtest.NewPool(t)
	var calls atomic.Int64
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			poisonTx(t, r) // only the first attempt dies inside the transaction
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"planId":"p1"}`))
	})
	mw := inboundhttp.RequireIdempotencyKey(pool, pgtx.With)(handler)

	first := httptest.NewRecorder()
	mw.ServeHTTP(first, keyedRequest("k-lie"))
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("first = %d %s, want 500 (the writes never happened)", first.Code, first.Body.String())
	}
	if n := queryInt(t, pool, `SELECT count(*) FROM idempotency_keys WHERE key = 'k-lie'`); n != 0 {
		t.Fatalf("a failed success left %d idempotency rows: the key could not retry", n)
	}

	second := httptest.NewRecorder()
	mw.ServeHTTP(second, keyedRequest("k-lie"))
	if second.Code != http.StatusCreated {
		t.Fatalf("retry = %d %s, want the real work to succeed", second.Code, second.Body.String())
	}
	if calls.Load() != 2 {
		t.Fatalf("handler ran %d times, want 2 (the first attempt was discarded)", calls.Load())
	}
}

func queryInt(t *testing.T, pool *pgxpool.Pool, sql string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), sql).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
