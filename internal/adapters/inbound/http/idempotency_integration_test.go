//go:build integration

package http_test

import (
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestGenerate_WithoutAnIdempotencyKeyIs400(t *testing.T) {
	f := newPgFixture(t)
	expectProblem(t, f.post(t, "/slot-plans", "", `{}`), http.StatusBadRequest, "idempotency-key-required")
	if n := f.query(`SELECT count(*) FROM slot_plans`); n != 0 {
		t.Fatalf("%d plans were created without a key", n)
	}
}

func TestGenerate_ReplayWithTheSameKeyAndBodyReturnsTheOriginalPlan(t *testing.T) {
	f := newPgFixture(t)
	first := f.post(t, "/slot-plans", "key-1", `{"lookbackDays":7}`)
	if first.status != http.StatusCreated {
		t.Fatalf("first = %d %s", first.status, first.raw)
	}
	again := f.post(t, "/slot-plans", "key-1", `{"lookbackDays":7}`)
	if again.status != http.StatusCreated || again.body["planId"] != first.body["planId"] || again.location != first.location || again.raw != first.raw {
		t.Fatalf("replay = %d %s (location %q), want the original %s", again.status, again.raw, again.location, first.raw)
	}
	if first.replayed != "" || again.replayed != "true" {
		t.Fatalf("Idempotency-Replayed = %q on the first answer, %q on the replay; want \"\" and \"true\"", first.replayed, again.replayed)
	}
	if plans, events := f.query(`SELECT count(*) FROM slot_plans`), f.query(`SELECT count(*) FROM outbox_events`); plans != 1 || events != 1 {
		t.Fatalf("plans %d, outbox rows %d: a replay must write nothing", plans, events)
	}
}

func TestGenerate_TheSameKeyWithADifferentBodyIs409(t *testing.T) {
	f := newPgFixture(t)
	f.post(t, "/slot-plans", "key-1", `{"lookbackDays":7}`)
	expectProblem(t, f.post(t, "/slot-plans", "key-1", `{"lookbackDays":14}`), http.StatusConflict, "idempotency-key-reused")
	if n := f.query(`SELECT count(*) FROM slot_plans`); n != 1 {
		t.Fatalf("plans = %d", n)
	}
}

func TestGenerate_DifferentKeysCreateDifferentPlans(t *testing.T) {
	f := newPgFixture(t)
	a, b := f.post(t, "/slot-plans", "key-a", `{}`), f.post(t, "/slot-plans", "key-b", `{}`)
	if a.status != http.StatusCreated || b.status != http.StatusCreated || a.body["planId"] == b.body["planId"] {
		t.Fatalf("a = %s, b = %s", a.raw, b.raw)
	}
}

func TestGenerate_ARejectedRequestIsCachedToo(t *testing.T) {
	f := newPgFixture(t)
	first := f.post(t, "/slot-plans", "key-bad", `{"lookbackDays":999}`)
	again := f.post(t, "/slot-plans", "key-bad", `{"lookbackDays":999}`)
	expectProblem(t, first, http.StatusBadRequest, "invalid-lookback")
	if again.status != first.status || again.raw != first.raw {
		t.Fatalf("replay = %d %s", again.status, again.raw)
	}
}

func TestGenerate_ConcurrentRequestsWithOneKeyCreateOnePlan(t *testing.T) {
	f := newPgFixture(t)
	const n = 8
	var wg sync.WaitGroup
	results := make([]response, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = f.post(t, "/slot-plans", "same-key", `{}`)
		}()
	}
	wg.Wait()
	for _, r := range results {
		if r.status != http.StatusCreated || r.body["planId"] != results[0].body["planId"] {
			t.Fatalf("results = %v", results)
		}
	}
	if plans := f.query(`SELECT count(*) FROM slot_plans`); plans != 1 {
		t.Fatalf("plans = %d", plans)
	}
}

// TestApprove_EndToEndOnPostgres drives the whole flow through HTTP: generate
// twice, approve one, approve the other (supersedes), and check what the
// database holds, including the outbox rows.
func TestApprove_EndToEndOnPostgres(t *testing.T) {
	f := newPgFixture(t)
	first := f.post(t, "/slot-plans", "k1", `{}`).body["planId"].(string)
	second := f.post(t, "/slot-plans", "k2", `{}`).body["planId"].(string)

	if r := f.post(t, "/slot-plans/"+first+"/approvals", "ap-first", ""); r.status != http.StatusOK || r.body["state"] != "Approved" {
		t.Fatalf("approve first = %d %s", r.status, r.raw)
	}
	r := f.post(t, "/slot-plans/"+second+"/approvals", "ap-second", "")
	if r.status != http.StatusOK || r.body["supersedesPlanId"] != first {
		t.Fatalf("approve second = %d %s", r.status, r.raw)
	}
	if n := f.query(`SELECT count(*) FROM slot_plans WHERE state = 'Approved'`); n != 1 {
		t.Fatalf("approved plans = %d", n)
	}
	if n := f.query(`SELECT count(*) FROM slot_plans WHERE state = 'Superseded' AND plan_id = $1`, first); n != 1 {
		t.Fatalf("the first plan must be Superseded")
	}
	// Generated x2 + Approved x2; superseding raises no event.
	if n := f.query(`SELECT count(*) FROM outbox_events`); n != 4 {
		t.Fatalf("outbox rows = %d", n)
	}
	expectProblem(t, f.post(t, "/slot-plans/"+second+"/approvals", "ap-second-again", ""), http.StatusConflict, "plan-not-draft")
}

// TestApprove_RacingApprovalsOfOneSiteLeaveOneApprovedPlan: with and without a
// previously Approved plan, concurrent approvals of different drafts leave
// exactly one Approved plan; an approval that loses a real overlap is a 409
// approved-plan-conflict.
func TestApprove_RacingApprovalsOfOneSiteLeaveOneApprovedPlan(t *testing.T) {
	t.Run("no previous plan", func(t *testing.T) { raceApprovals(t, false) })
	t.Run("with a previous approved plan", func(t *testing.T) { raceApprovals(t, true) })
}

func raceApprovals(t *testing.T, withPrevious bool) {
	f := newPgFixture(t)
	if withPrevious {
		prev := f.post(t, "/slot-plans", "prev", `{}`).body["planId"].(string)
		f.post(t, "/slot-plans/"+prev+"/approvals", "ap-prev", "")
	}
	const n = 5
	drafts := make([]string, n)
	for i := range n {
		drafts[i] = f.post(t, "/slot-plans", "draft-"+string(rune('a'+i)), `{}`).body["planId"].(string)
	}
	var wg sync.WaitGroup
	results := make([]response, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = f.post(t, "/slot-plans/"+drafts[i]+"/approvals", "ap-"+drafts[i], "")
		}()
	}
	wg.Wait()
	won := 0
	for _, r := range results {
		if r.status == http.StatusOK {
			won++
			continue
		}
		expectProblem(t, r, http.StatusConflict, "approved-plan-conflict")
	}
	assertOneApproved(t, f, won, withPrevious)
}

// assertOneApproved: requests that do not overlap in time each win by
// superseding the one before; only a real overlap loses. Either way exactly
// one plan is Approved and every other winner was superseded.
func assertOneApproved(t *testing.T, f *pgFixture, won int, withPrevious bool) {
	t.Helper()
	if won < 1 {
		t.Fatal("at least one approval must win")
	}
	if approved := f.query(`SELECT count(*) FROM slot_plans WHERE state = 'Approved'`); approved != 1 {
		t.Fatalf("approved plans = %d", approved)
	}
	wantSuperseded := won - 1
	if withPrevious {
		wantSuperseded = won
	}
	if superseded := f.query(`SELECT count(*) FROM slot_plans WHERE state = 'Superseded'`); superseded != wantSuperseded {
		t.Fatalf("superseded plans = %d, want %d (won %d)", superseded, wantSuperseded, won)
	}
}

func TestIdempotencyKey_MalformedKeyIs400Invalid(t *testing.T) {
	f := newPgFixture(t)
	for _, key := range []string{strings.Repeat("k", 129), "tab\tkey", "caf\u00e9"} {
		expectProblem(t, f.post(t, "/slot-plans", key, `{}`), http.StatusBadRequest, "idempotency-key-invalid")
	}
	if n := f.query(`SELECT count(*) FROM slot_plans`); n != 0 {
		t.Fatalf("%d plans were created with an invalid key", n)
	}
}

func TestIdempotencyKey_IsGlobalAcrossRoutes(t *testing.T) {
	f := newPgFixture(t)
	id := f.post(t, "/slot-plans", "k-plan", `{}`).body["planId"].(string)
	// The key of POST /slot-plans on another route is a reuse, never a new key.
	expectProblem(t, f.post(t, "/slot-plans/"+id+"/approvals", "k-plan", ""), http.StatusConflict, "idempotency-key-reused")
	if n := f.query(`SELECT count(*) FROM slot_plans WHERE state = 'Approved'`); n != 0 {
		t.Fatalf("approved plans = %d: a reused key must not run the handler", n)
	}
}

func TestApprovalAndRejection_ReplayAndMissingKey(t *testing.T) {
	f := newPgFixture(t)
	a := f.post(t, "/slot-plans", "g1", `{}`).body["planId"].(string)
	b := f.post(t, "/slot-plans", "g2", `{}`).body["planId"].(string)
	expectProblem(t, f.post(t, "/slot-plans/"+a+"/approvals", "", ""), http.StatusBadRequest, "idempotency-key-required")
	expectProblem(t, f.post(t, "/slot-plans/"+b+"/rejections", "", ""), http.StatusBadRequest, "idempotency-key-required")

	first := f.post(t, "/slot-plans/"+a+"/approvals", "ap", "")
	again := f.post(t, "/slot-plans/"+a+"/approvals", "ap", "")
	if first.status != http.StatusOK || again.raw != first.raw || again.replayed != "true" || first.replayed != "" {
		t.Fatalf("approval replay = %d %q %s, first %q %s", again.status, again.replayed, again.raw, first.replayed, first.raw)
	}
	rej := f.post(t, "/slot-plans/"+b+"/rejections", "rj", `{"reason":"no"}`)
	rejAgain := f.post(t, "/slot-plans/"+b+"/rejections", "rj", `{"reason":"no"}`)
	if rej.status != http.StatusOK || rejAgain.raw != rej.raw || rejAgain.replayed != "true" {
		t.Fatalf("rejection replay = %d %q %s", rejAgain.status, rejAgain.replayed, rejAgain.raw)
	}
	expectProblem(t, f.post(t, "/slot-plans/"+b+"/rejections", "rj", `{"reason":"other"}`), http.StatusConflict, "idempotency-key-reused")
	if n := f.query(`SELECT count(*) FROM outbox_events`); n != 4 {
		t.Fatalf("outbox rows = %d, want 4 (2 generated, 1 approved, 1 rejected)", n)
	}
}
