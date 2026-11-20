package http

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// (a) Every problem type declares exactly one replay class on its catalogue
// entry. There is no runtime default: a new problem type is classified by the
// PR that adds it, or this test is red.
func TestEveryProblemTypeDeclaresAReplayClass(t *testing.T) {
	types := allProblemTypes()
	if len(types) == 0 {
		t.Fatal("the catalogue accessor returned no problem types: the test would pass on nothing")
	}
	classOf := map[string]replayClass{}
	for _, p := range types {
		t.Run(p.Slug, func(t *testing.T) {
			if !validReplayClass(p.Class) {
				t.Errorf("problem type %q (%d) declares no replay class (%q): set FINAL, OUTCOME or RETRYABLE on its catalogue entry (.claude/rules/fleet/idempotency-and-outbox.md)", p.Slug, p.Status, p.Class)
			}
			if p.Status >= http.StatusInternalServerError && p.Class != classRetryable {
				t.Errorf("problem type %q answers %d: a 5xx is never stored, so its class is RETRYABLE, got %q", p.Slug, p.Status, p.Class)
			}
			if prev, ok := classOf[p.Slug]; ok && prev != p.Class {
				t.Errorf("slug %q is declared %s and %s: one problem type has exactly one class", p.Slug, prev, p.Class)
			}
			classOf[p.Slug] = p.Class
		})
	}
}

// The check above must bite: an unclassified entry and a misspelt class are refused.
func TestTheUnclassifiedEntryCheckBites(t *testing.T) {
	for _, c := range []replayClass{"", "final", "STORED", "OUTCOME "} {
		if validReplayClass(c) {
			t.Errorf("class %q is accepted; only FINAL, OUTCOME and RETRYABLE are classes", c)
		}
	}
	for _, c := range []replayClass{classFinal, classOutcome, classRetryable} {
		if !validReplayClass(c) {
			t.Errorf("class %q is refused", c)
		}
	}
}

// The classification itself is pinned entry by entry (ADR 0016): flipping a
// class is a decision, not a refactor. A problem type that is new fails the
// first test above until it is classified here and in the ADR.
func TestEveryProblemTypeHasTheClassTheADRDecided(t *testing.T) {
	want := map[string]replayClass{
		"idempotency-key-required":   classFinal,     // before the store; the request alone
		"idempotency-key-invalid":    classFinal,     // before the store; the request alone
		"idempotency-key-reused":     classFinal,     // the store's own answer; nothing written
		"malformed-request":          classFinal,     // the request body alone
		"invalid-plan-id":            classFinal,     // the path id's format alone
		"invalid-site-id":            classRetryable, // mixed slug: format (request) and ErrNoSite (no default site configured)
		"invalid-lookback":           classFinal,     // the request body alone
		"unknown-query-parameter":    classFinal,     // the query alone (a GET: never stored)
		"invalid-limit":              classFinal,     // the query alone (a GET: never stored)
		"invalid-cursor":             classFinal,     // the query alone (a GET: never stored)
		"invalid-state":              classFinal,     // the query alone (a GET: never stored)
		"reject-reason-too-long":     classFinal,     // the request body alone
		"plan-not-found":             classFinal,     // ids are minted here (plan-<uuid>) and never deleted
		"plan-not-draft":             classFinal,     // Approved/Rejected/Superseded never return to Draft (ADR 0002)
		"approved-plan-conflict":     classRetryable, // lost supersede race; the retry supersedes the winner
		"concurrent-modification":    classRetryable, // lost optimistic-lock race, nothing written
		"internal-error":             classRetryable, // a 5xx is never stored
		"resource-not-found":         classFinal,     // the NUL path 404, before the middleware; the request alone
		"dependency-unavailable":     classRetryable, // GET /readyz 503; a 5xx is never stored
		"invalid-report-range":       classFinal,     // the reports query alone (a GET: never stored)
		"invalid-report-granularity": classFinal,     // the reports query alone (a GET: never stored)
		"report-store-error":         classRetryable, // a 5xx is never stored
	}
	got := map[string]replayClass{}
	for _, p := range allProblemTypes() {
		got[p.Slug] = p.Class
	}
	for slug, class := range want {
		if got[slug] != class {
			t.Errorf("%s: class %q, the ADR decided %q", slug, got[slug], class)
		}
	}
	for slug := range got {
		if _, ok := want[slug]; !ok {
			t.Errorf("problem type %q is not in the decided table", slug)
		}
	}
}

// problem literals are the catalogue: they may only be written in errors.go,
// where the class is declared, and each carries its class. A literal built at
// a call site would escape the unclassified-entry check above.
func TestEveryProblemLiteralIsInTheCatalogueAndClassified(t *testing.T) {
	literals := problemLiterals(t)
	if len(literals) == 0 {
		t.Fatal("no problem literal found: the scan would pass on nothing")
	}
	for _, l := range literals {
		if l.file != "errors.go" {
			t.Errorf("%s: a problem literal outside errors.go: declare it there so it carries a class", l.pos)
		}
		if !literalDeclaresClass(l.lit) {
			t.Errorf("%s: this problem literal declares no class", l.pos)
		}
	}
}

type problemLiteral struct {
	file string
	pos  token.Position
	lit  *ast.CompositeLit
}

// problemLiterals finds every problem composite literal of the package's non-test files.
func problemLiterals(t *testing.T) []problemLiteral {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var found []problemLiteral
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if lit, ok := n.(*ast.CompositeLit); ok && isProblem(lit) {
				found = append(found, problemLiteral{file: name, pos: fset.Position(lit.Pos()), lit: lit})
			}
			return true
		})
	}
	return found
}

func isProblem(lit *ast.CompositeLit) bool {
	id, ok := lit.Type.(*ast.Ident)
	return ok && id.Name == "problem"
}

// literalDeclaresClass: a keyed literal names `class`; a positional one has the fourth element.
func literalDeclaresClass(lit *ast.CompositeLit) bool {
	for i, e := range lit.Elts {
		if kv, ok := e.(*ast.KeyValueExpr); ok {
			if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "class" {
				return true
			}
		} else if i == 3 {
			return true
		}
	}
	return false
}

// The ADR's class table is the human copy of the catalogue: one row per slug,
// same class.
func TestTheADRClassTableMatchesTheCatalogue(t *testing.T) {
	raw, err := os.ReadFile("../../../../docs/adr/0016-every-problem-type-has-a-replay-class.md")
	if err != nil {
		t.Fatalf("the ADR with the class table: %v", err)
	}
	row := regexp.MustCompile("(?m)^\\| `([a-z0-9-]+)` \\| (\\d{3}) \\| (FINAL|OUTCOME|RETRYABLE) \\|")
	inADR := map[string]string{}
	for _, m := range row.FindAllStringSubmatch(string(raw), -1) {
		if _, dup := inADR[m[1]]; dup {
			t.Errorf("slug %q is listed twice in the ADR table", m[1])
		}
		inADR[m[1]] = m[3]
	}
	inCode := map[string]string{}
	for _, p := range allProblemTypes() {
		inCode[p.Slug] = string(p.Class)
	}
	for slug, class := range inCode {
		if got, ok := inADR[slug]; !ok {
			t.Errorf("problem type %q (%s) is missing from the ADR table", slug, class)
		} else if got != class {
			t.Errorf("problem type %q is %s in the catalogue and %s in the ADR", slug, class, got)
		}
	}
	for slug := range inADR {
		if _, ok := inCode[slug]; !ok {
			t.Errorf("the ADR lists %q, which is not a problem type of the catalogue", slug)
		}
	}
}

// The decision the middleware takes, for every status and class: 2xx stored,
// 5xx never, the rest by the declared class, and a refusal without a class (or
// with a misspelt one) is released.
func TestStorableDecisionByStatusAndClass(t *testing.T) {
	cases := []struct {
		code  int
		class replayClass
		want  bool
	}{
		{http.StatusCreated, "", true},
		{http.StatusOK, classRetryable, true},
		{http.StatusInternalServerError, classFinal, false},
		{http.StatusInternalServerError, classOutcome, false},
		{http.StatusServiceUnavailable, "", false},
		{http.StatusBadRequest, classFinal, true},
		{http.StatusConflict, classOutcome, true},
		{http.StatusBadRequest, classOutcome, true},
		{http.StatusNotFound, classFinal, true},
		{http.StatusConflict, classRetryable, false},
		{http.StatusNotFound, classRetryable, false},
		{http.StatusConflict, "", false},
		{http.StatusBadRequest, "final", false},
		{http.StatusMovedPermanently, "", false},
	}
	for _, c := range cases {
		rec := &capture{header: http.Header{}}
		rec.markReplayClass(c.class)
		rec.WriteHeader(c.code)
		if got := rec.storable(); got != c.want {
			t.Errorf("status %d class %q: storable = %v, want %v", c.code, c.class, got, c.want)
		}
	}
}

// writeProblem and writeError mark the capturing writer with the class of the
// problem answered.
func TestWriteProblemAndWriteErrorMarkTheCaptureWithTheCatalogueClass(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/x", nil)
	for _, e := range problemCatalogue {
		rec := &capture{header: http.Header{}}
		writeError(rec, r, e.err)
		if rec.class != e.p.class || !validReplayClass(rec.class) {
			t.Errorf("%s: writeError marked %q, the entry declares %q", e.p.slug, rec.class, e.p.class)
		}
	}
	for _, p := range allProblemTypes() {
		rec := &capture{header: http.Header{}}
		writeProblem(rec, r, problem{p.Status, p.Slug, "t", p.Class}, "d")
		if rec.class != p.Class {
			t.Errorf("%s: writeProblem marked %q, want %q", p.Slug, rec.class, p.Class)
		}
	}
	// An error nobody mapped is a 500: RETRYABLE, never stored.
	rec := &capture{header: http.Header{}}
	writeError(rec, r, errMissingOutcome)
	if rec.code != http.StatusInternalServerError || rec.class != classRetryable {
		t.Errorf("unmapped error = %d class %q, want 500 RETRYABLE", rec.code, rec.class)
	}
	// A plain writer (a route without the idempotency middleware, a recorder)
	// is not a capture.
	writeProblem(httptest.NewRecorder(), r, notFoundResource, "d")
}
