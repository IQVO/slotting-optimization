//go:build integration

package http_test

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// The replay-class rule (ADR 0016, the fleet rule
// .claude/rules/fleet/idempotency-and-outbox.md): a refusal whose problem type
// declares RETRYABLE is NOT stored -- its key is released and a retry under the
// same key executes the real work again. These tests drive the REAL router and
// Postgres through the two RETRYABLE refusals of the approve route.
//
// The races are made deterministic with the fleet's lock-and-bump shape: hold
// the plan's row lock (SELECT ... FOR UPDATE) until the request is blocked on
// its version-guarded UPDATE (a Lock waiter in pg_stat_activity), then commit a
// version bump the request cannot match. The UPDATE re-evaluates its WHERE on
// the committed row, matches nothing, and the repository answers
// concurrent-modification (the plan's own save) or approved-plan-conflict (the
// superseded previous plan's save, mapped by ApprovePlan.supersedePrevious).

// waitForLockWaiter blocks until some other backend of this database waits on a
// lock while running a statement that touches slot_plans.
func waitForLockWaiter(t *testing.T, f *pgFixture) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		n := f.query(`SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND pid <> pg_backend_pid()
			  AND wait_event_type = 'Lock' AND query LIKE '%slot_plans%'`)
		if n > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no request ever blocked on a slot_plans lock: the race never happened")
}

// holdRowLock opens a transaction holding the plan's row lock; the returned
// commit bumps the version so the blocked request's guard cannot match.
func holdRowLock(t *testing.T, f *pgFixture, planID string) func() {
	t.Helper()
	ctx := context.Background()
	conn, err := f.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT plan_id FROM slot_plans WHERE plan_id = $1 FOR UPDATE`, planID); err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		if _, err := tx.Exec(ctx, `UPDATE slot_plans SET version = version + 1 WHERE plan_id = $1`, planID); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		conn.Release()
	}
}

// generateDraft returns a fresh Draft plan id of the fixture's default site.
func generateDraft(t *testing.T, f *pgFixture, key string) string {
	t.Helper()
	r := f.post(t, "/slot-plans", key, `{}`)
	if r.status != http.StatusCreated {
		t.Fatalf("generate = %d %s", r.status, r.raw)
	}
	return r.body["planId"].(string)
}

// TestRetryable_ConcurrentModificationIsNotStoredAndItsKeyRetriesTheWork: the
// approval loses its own version-guarded UPDATE (409 concurrent-modification,
// RETRYABLE): no key row is left, the retry under the SAME key executes and
// wins, and its 200 is then stored and replayed.
func TestRetryable_ConcurrentModificationIsNotStoredAndItsKeyRetriesTheWork(t *testing.T) {
	f := newPgFixture(t)
	plan := generateDraft(t, f, "gen-cm")
	release := holdRowLock(t, f, plan)

	type result struct {
		resp response
		done chan struct{}
	}
	res := result{done: make(chan struct{})}
	go func() {
		defer close(res.done)
		res.resp = f.post(t, "/slot-plans/"+plan+"/approvals", "ap-cm", "")
	}()
	waitForLockWaiter(t, f)
	release()
	<-res.done

	expectProblem(t, res.resp, http.StatusConflict, "concurrent-modification")
	if n := f.query(`SELECT count(*) FROM idempotency_keys WHERE key = 'ap-cm'`); n != 0 {
		t.Fatalf("a RETRYABLE refusal left %d key rows: the key was not released", n)
	}

	// The retry under the same key executes again: the plan is still a Draft,
	// only its version moved, so the approval now wins.
	retry := f.post(t, "/slot-plans/"+plan+"/approvals", "ap-cm", "")
	if retry.status != http.StatusOK || retry.body["state"] != "Approved" {
		t.Fatalf("retry = %d %s, want the released key to execute the approval again", retry.status, retry.raw)
	}
	if n := f.query(`SELECT count(*) FROM idempotency_keys WHERE key = 'ap-cm' AND status_code = 200`); n != 1 {
		t.Fatalf("the retried approval left %d stored 200s, want 1", n)
	}

	replay := f.post(t, "/slot-plans/"+plan+"/approvals", "ap-cm", "")
	if replay.status != http.StatusOK || replay.raw != retry.raw || replay.replayed != "true" || retry.replayed != "" {
		t.Fatalf("replay = %d %q (replayed %q/%q), want the stored 200 byte-identically with the header",
			replay.status, replay.raw, retry.replayed, replay.replayed)
	}
}

// TestRetryable_ApprovedPlanConflictIsNotStoredAndADifferentBodyIsANewRequest:
// the approval loses the supersede of the site's Approved plan (409
// approved-plan-conflict, RETRYABLE): no key row is left, so even a DIFFERENT
// body under the same key is a new request (not 409 idempotency-key-reused),
// and it wins; a replay then answers the stored 200.
func TestRetryable_ApprovedPlanConflictIsNotStoredAndADifferentBodyIsANewRequest(t *testing.T) {
	f := newPgFixture(t)
	prev := generateDraft(t, f, "gen-prev")
	if r := f.post(t, "/slot-plans/"+prev+"/approvals", "ap-prev", ""); r.status != http.StatusOK {
		t.Fatalf("previous approval = %d %s", r.status, r.raw)
	}
	draft := generateDraft(t, f, "gen-next")
	release := holdRowLock(t, f, prev)

	type result struct {
		resp response
		done chan struct{}
	}
	res := result{done: make(chan struct{})}
	go func() {
		defer close(res.done)
		res.resp = f.post(t, "/slot-plans/"+draft+"/approvals", "ap-conflict", "")
	}()
	waitForLockWaiter(t, f)
	release()
	<-res.done

	expectProblem(t, res.resp, http.StatusConflict, "approved-plan-conflict")
	if n := f.query(`SELECT count(*) FROM idempotency_keys WHERE key = 'ap-conflict'`); n != 0 {
		t.Fatalf("a RETRYABLE refusal left %d key rows: the key was not released", n)
	}

	// The key was released: a different body under it is a NEW request, not a
	// reuse conflict, and the approval now supersedes the bumped winner.
	retry := f.post(t, "/slot-plans/"+draft+"/approvals", "ap-conflict", `{"note":"new request"}`)
	if retry.status != http.StatusOK || retry.body["state"] != "Approved" || retry.body["supersedesPlanId"] != prev {
		t.Fatalf("retry = %d %s, want the released key to execute a new request that wins", retry.status, retry.raw)
	}
	if n := f.query(`SELECT count(*) FROM slot_plans WHERE state = 'Approved'`); n != 1 {
		t.Fatalf("approved plans = %d, want exactly one", n)
	}

	replay := f.post(t, "/slot-plans/"+draft+"/approvals", "ap-conflict", `{"note":"new request"}`)
	if replay.status != http.StatusOK || replay.raw != retry.raw || replay.replayed != "true" {
		t.Fatalf("replay = %d %q, want the stored 200 with the replay header", replay.status, replay.raw)
	}
}

// TestRetryable_AConcurrentDuplicateOfAReleasedRequestExecutesItself: while
// the key owner is blocked, a duplicate blocks on the key's unique index; when
// the owner ends RETRYABLE and rolls back (key row included), the duplicate's
// INSERT succeeds and it executes the work itself. One 409 (the owner, not
// stored), one 200 (the duplicate, executed for real, then stored).
func TestRetryable_AConcurrentDuplicateOfAReleasedRequestExecutesItself(t *testing.T) {
	f := newPgFixture(t)
	plan := generateDraft(t, f, "gen-dup")
	release := holdRowLock(t, f, plan)

	responses := make(chan response, 2)
	for range 2 {
		go func() { responses <- f.post(t, "/slot-plans/"+plan+"/approvals", "ap-dup", "") }()
	}
	waitForLockWaiter(t, f)
	release()
	got := []response{<-responses, <-responses}

	conflicts, approved := 0, 0
	for _, r := range got {
		switch {
		case r.status == http.StatusConflict && r.replayed == "":
			expectProblem(t, r, http.StatusConflict, "concurrent-modification")
			conflicts++
		case r.status == http.StatusOK && r.replayed == "" && r.body["state"] == "Approved":
			approved++
		default:
			t.Fatalf("unexpected answer %d %q (replayed %q)", r.status, r.raw, r.replayed)
		}
	}
	if conflicts != 1 || approved != 1 {
		t.Fatalf("conflicts = %d, approved = %d, want 1 and 1 (the duplicate executes itself)", conflicts, approved)
	}
	if n := f.query(`SELECT count(*) FROM slot_plans WHERE plan_id = $1 AND state = 'Approved'`, plan); n != 1 {
		t.Fatalf("the plan is Approved %d times", n)
	}
	// The stored row is the duplicate's 200, never the released 409.
	if n := f.query(`SELECT count(*) FROM idempotency_keys WHERE key = 'ap-dup' AND status_code = 200`); n != 1 {
		t.Fatalf("stored rows for the key = %d, want exactly the duplicate's 200", n)
	}
	if n := f.query(`SELECT count(*) FROM idempotency_keys WHERE key = 'ap-dup' AND status_code = 409`); n != 0 {
		t.Fatalf("the RETRYABLE 409 was stored %d times", n)
	}
}
