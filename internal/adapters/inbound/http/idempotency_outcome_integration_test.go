//go:build integration

package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	inboundhttp "github.com/claudioed/slotting-optimization/internal/adapters/inbound/http"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/postgres/pgtx"
	"github.com/claudioed/slotting-optimization/internal/testing/pgtest"
)

// No problem type of this service is OUTCOME (ADR 0016: no use case writes and
// then refuses), but the middleware still honours the class, so a future
// OUTCOME entry is safe: this probe commits one write and refuses 409 with
// class OUTCOME. The write commits WITH the stored answer, a replay records
// nothing new, and an OUTCOME whose writes were lost (the savepoint cannot be
// released) is a 500 that stores nothing.

func outcomeRequest(key string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/slot-plans", strings.NewReader(`{}`))
	req.Header.Set(inboundhttp.IdempotencyKeyHeader, key)
	return req
}

func TestOutcomeBranchCommitsTheWritesWithTheStoredAnswerAndAReplayAddsNothing(t *testing.T) {
	pool := pgtest.NewPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE outcome_probe (tag text NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	mw := inboundhttp.RequireIdempotencyKey(pool, pgtx.With)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		tx, _ := pgtx.From(r.Context())
		if _, err := tx.Exec(r.Context(), `INSERT INTO outcome_probe (tag) VALUES ('committed')`); err != nil {
			t.Error(err)
		}
		inboundhttp.ProbeRefuseForTest(w, r, http.StatusConflict, "OUTCOME")
	}))
	count := func(sql string) int {
		var n int
		if err := pool.QueryRow(ctx, sql).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	first := httptest.NewRecorder()
	mw.ServeHTTP(first, outcomeRequest("k-outcome"))
	if first.Code != http.StatusConflict {
		t.Fatalf("first = %d %s, want 409", first.Code, first.Body.String())
	}
	if n := count(`SELECT count(*) FROM outcome_probe`); n != 1 {
		t.Fatalf("committed writes after the refusal = %d, want 1: an OUTCOME refusal commits what the command did", n)
	}
	if n := count(`SELECT count(*) FROM idempotency_keys WHERE key = 'k-outcome' AND status_code = 409`); n != 1 {
		t.Fatalf("stored answers = %d, want 1", n)
	}
	for i := 0; i < 2; i++ {
		rep := httptest.NewRecorder()
		mw.ServeHTTP(rep, outcomeRequest("k-outcome"))
		if rep.Code != http.StatusConflict || rep.Header().Get(inboundhttp.IdempotencyReplayedHeader) != "true" || rep.Body.String() != first.Body.String() {
			t.Fatalf("replay %d = %d %q, want the stored answer with Idempotency-Replayed", i, rep.Code, rep.Header().Get(inboundhttp.IdempotencyReplayedHeader))
		}
	}
	if calls.Load() != 1 || count(`SELECT count(*) FROM outcome_probe`) != 1 {
		t.Fatalf("handler ran %d times, probe rows %d: a replay must record nothing new", calls.Load(), count(`SELECT count(*) FROM outcome_probe`))
	}
}

func TestOutcomeBranchWhoseWritesWereLostIsA500AndStoresNothing(t *testing.T) {
	pool := pgtest.NewPool(t)
	ctx := context.Background()
	mw := inboundhttp.RequireIdempotencyKey(pool, pgtx.With)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tx, _ := pgtx.From(r.Context())
		// A statement that fails on the server aborts the transaction: the savepoint cannot be released.
		_, _ = tx.Exec(r.Context(), `INSERT INTO table_that_does_not_exist VALUES (1)`)
		inboundhttp.ProbeRefuseForTest(w, r, http.StatusConflict, "OUTCOME")
	}))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, outcomeRequest("k-lost"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("an OUTCOME whose writes are lost = %d %s, want 500", rec.Code, rec.Body.String())
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM idempotency_keys WHERE key = 'k-lost'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("key rows = %d (%v), want 0: nothing is stored", n, err)
	}
}
