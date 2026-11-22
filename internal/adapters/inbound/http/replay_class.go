package http

import "net/http"

// replayClass says what the Idempotency-Key middleware does with a problem answer when the same
// request is retried under the same key (docs/adr/0016-every-problem-type-has-a-replay-class.md,
// the fleet rule .claude/rules/fleet/idempotency-and-outbox.md). It is declared ON the catalogue
// entry of each problem type, next to its slug and title; there is no default and no list of
// slugs in the middleware.
type replayClass string

const (
	// classFinal: the answer is a function of the request alone. Stored and replayed.
	classFinal replayClass = "FINAL"
	// classOutcome: the command COMMITTED state and then answered an error that must be
	// remembered. Stored, and its writes commit with the stored answer (one transaction).
	classOutcome replayClass = "OUTCOME"
	// classRetryable: nothing committed and the answer depends on state that can change. Not
	// stored: the transaction rolls back, key row included, and a retry executes again.
	classRetryable replayClass = "RETRYABLE"
)

// validReplayClass reports whether c is one of the three classes; "" is "unclassified".
func validReplayClass(c replayClass) bool {
	return c == classFinal || c == classOutcome || c == classRetryable
}

// problemType is one problem type of the catalogue as the tests and the ADR table see it.
type problemType struct {
	Slug   string
	Status int
	Class  replayClass
}

// standaloneProblems are the problem types written outside problemCatalogue: the middlewares'
// own answers, the 5xx fallback, the NUL-path 404, the readiness 503 and the reports reader's
// own answers (a read-only binary, never behind the middleware).
var standaloneProblems = []problem{
	idempotencyKeyRequired, idempotencyKeyInvalid, idempotencyKeyReused,
	internalProblem, notFoundResource, dependencyUnavailable,
	invalidReportRange, invalidReportGranularity, reportStoreError,
}

// allProblemTypes is the whole catalogue: every typed error's problem, then the standalone ones,
// one entry per (slug, status). Several errors map to invalid-site-id, invalid-limit and
// invalid-cursor.
func allProblemTypes() []problemType {
	var out []problemType
	seen := map[problemType]bool{}
	add := func(p problem) {
		pt := problemType{Slug: p.slug, Status: p.status, Class: p.class}
		if !seen[pt] {
			seen[pt] = true
			out = append(out, pt)
		}
	}
	for _, e := range problemCatalogue {
		add(e.p)
	}
	for _, p := range standaloneProblems {
		add(p)
	}
	return out
}

// replayMarker is implemented by the capturing writer the idempotency middleware hands the
// handler. The production error writer marks it with the class of the problem it answers.
type replayMarker interface {
	markReplayClass(replayClass)
}

// markReplayClass marks w when it is the middleware's capture; any other writer (a route without
// the middleware, a test recorder) has nothing to mark.
func markReplayClass(w http.ResponseWriter, class replayClass) {
	if m, ok := w.(replayMarker); ok {
		m.markReplayClass(class)
	}
}

func (c *capture) markReplayClass(class replayClass) { c.class = class }

// storable reports whether the captured answer is final for the request: a 2xx is stored, a 5xx
// never is, and every other answer is stored only when its problem type declares FINAL or
// OUTCOME. A refusal that declares no class (or a misspelt one) is released like RETRYABLE: a
// released refusal costs one more execution, a wrongly stored one pins a stale refusal forever
// (this store keeps key rows indefinitely; there is no TTL).
func (c *capture) storable() bool {
	switch {
	case c.code >= http.StatusOK && c.code < http.StatusMultipleChoices:
		return true
	case c.code >= http.StatusInternalServerError:
		return false
	default:
		return c.class == classFinal || c.class == classOutcome
	}
}
