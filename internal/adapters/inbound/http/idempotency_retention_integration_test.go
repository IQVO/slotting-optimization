//go:build integration

package http_test

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// The retention rule (ADR 0016, addendum 2026-11-22): a stored answer lives
// for a 24-hour TTL, then the key is forgotten -- an expired row must never
// answer a replay; the request under that key EXECUTES again. These tests
// drive the REAL router and Postgres of the pgFixture.

// ageKeyRow moves a key row's created_at into the past by age.
func ageKeyRow(t *testing.T, f *pgFixture, key string, age time.Duration) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE idempotency_keys SET created_at = now() - make_interval(secs => $2) WHERE key = $1`,
		key, age.Seconds()); err != nil {
		t.Fatalf("age %s: %v", key, err)
	}
}

// A live row replays (the control), and a row older than the 24h TTL executes:
// the same key and body runs the real work a second time and answers a NEW
// plan, with no Idempotency-Replayed header.
func TestAnExpiredKeyExecutesInsteadOfReplaying(t *testing.T) {
	f := newPgFixture(t)

	// Control: inside the window the same key and body replays the plan.
	first := f.post(t, "/slot-plans", "live-key", `{}`)
	if first.status != http.StatusCreated {
		t.Fatalf("first post = %d %s", first.status, first.raw)
	}
	replay := f.post(t, "/slot-plans", "live-key", `{}`)
	if replay.status != http.StatusCreated || replay.replayed != "true" {
		t.Fatalf("live replay = %d replayed=%q, want 201 and Idempotency-Replayed: true", replay.status, replay.replayed)
	}
	if replay.body["planId"] != first.body["planId"] {
		t.Fatalf("live replay planId = %v, want the stored %v", replay.body["planId"], first.body["planId"])
	}

	// The defect: a row past the TTL still answers today. It must not: the
	// key is claimed again and the work EXECUTES (a new plan, no replay
	// header), exactly like a key never seen before.
	expired := f.post(t, "/slot-plans", "age-key", `{}`)
	if expired.status != http.StatusCreated {
		t.Fatalf("expired first post = %d %s", expired.status, expired.raw)
	}
	ageKeyRow(t, f, "age-key", 25*time.Hour) // one hour past the 24h TTL
	again := f.post(t, "/slot-plans", "age-key", `{}`)
	if again.status != http.StatusCreated {
		t.Fatalf("expired replay = %d %s, want 201: the request executes again", again.status, again.raw)
	}
	if again.replayed == "true" {
		t.Fatal("an expired row answered the replay: the key must be forgotten at the TTL")
	}
	if again.body["planId"] == expired.body["planId"] {
		t.Fatalf("expired replay returned the stored plan %v: the work must run again", again.body["planId"])
	}
	// And the row the second execution stored is live again: a replay works.
	settled := f.post(t, "/slot-plans", "age-key", `{}`)
	if settled.status != http.StatusCreated || settled.replayed != "true" || settled.body["planId"] != again.body["planId"] {
		t.Fatalf("post-expiry replay = %d replayed=%q, want 201 replaying the second execution's answer", settled.status, settled.replayed)
	}
}
