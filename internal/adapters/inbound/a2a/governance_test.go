package a2a

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The A2A surface mirrors the MCP charter (docs/adr/0005, .claude/rules/mcp.md):
// READ-ONLY. Generating, approving and rejecting a plan stay REST-only; an
// agent-initiated write path would be a second front door to the outbox and a
// way for software to approve its own proposal (ADR 0002). Widening this
// surface is a reviewed act: an ADR 0013 amendment and these constants.

// wantUseCases is the exact set of use cases Deps may hold: the read ones.
var wantUseCases = []string{"GetPlan", "ListForwardSlots", "ListPlans"}

// writeVerbs are the verbs of this context's write use cases (and the generic
// mutators); no skill id may carry one as a snake_case word.
var writeVerbs = []string{"generate", "approve", "reject", "create", "update", "delete", "set", "assign"}

// wantInternalImports is every internal package the adapter may import.
var wantInternalImports = []string{
	"internal/application/repository",
	"internal/application/usecases",
	"internal/domain/slotplan",
}

// depsFields returns the sorted field names of a Deps-shaped struct type.
func depsFields(typ reflect.Type) []string {
	var names []string
	for i := range typ.NumField() {
		names = append(names, typ.Field(i).Name)
	}
	sort.Strings(names)
	return names
}

func TestDepsHoldsOnlyReadUseCases(t *testing.T) {
	if got := depsFields(reflect.TypeFor[Deps]()); !reflect.DeepEqual(got, wantUseCases) {
		t.Fatalf("Deps fields = %v, want exactly the read use cases %v: the A2A surface is read-only (docs/adr/0013)", got, wantUseCases)
	}
}

// TestDepsSensorBites proves the field check can fail: a write use case
// field would not match the read-only set.
func TestDepsSensorBites(t *testing.T) {
	withWrite := reflect.TypeOf(struct {
		GetPlan          int
		ListPlans        int
		ListForwardSlots int
		ApprovePlan      int
	}{})
	if reflect.DeepEqual(depsFields(withWrite), wantUseCases) {
		t.Fatal("a Deps with a write use case passes the read-only check")
	}
}

// internalImports parses every non-test source file of the package and
// returns the module-internal import paths (tail from "internal/").
func internalImports(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if _, tail, ok := strings.Cut(path, "slotting-optimization/"); ok {
				seen[tail] = true
			}
		}
	}
	var out []string
	for path := range seen {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

// The adapter imports only the read use cases' package, the repository error
// sentinels and the plan value errors: no ports, no outbox, no encoder, no
// write-side domain package.
func TestAdapterImportsNoWriteSide(t *testing.T) {
	if got := internalImports(t); !reflect.DeepEqual(got, wantInternalImports) {
		t.Fatalf("internal imports = %v, want %v (a new one needs an ADR 0013 amendment)", got, wantInternalImports)
	}
}

func TestSkillIdsCarryNoWriteVerb(t *testing.T) {
	for _, s := range testCard().Skills {
		for _, word := range strings.Split(s.ID, "_") {
			for _, verb := range writeVerbs {
				if word == verb {
					t.Errorf("skill %q carries the write verb %q: the A2A surface is read-only", s.ID, verb)
				}
			}
		}
	}
}
