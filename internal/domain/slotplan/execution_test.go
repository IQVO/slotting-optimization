package slotplan

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// approvedPlan is validNew approved: its moves, in move_seq order (sorted by
// SKU), are 0 Assign SKU-1 -> FWD-01, 1 Relocate SKU-2 FWD-09 -> FWD-02 and
// 2 Vacate SKU-3 FWD-03.
func approvedPlan(t *testing.T) *SlotPlan {
	t.Helper()
	p := mustGenerate(t)
	if _, err := p.Approve("", t1); err != nil {
		t.Fatal(err)
	}
	return p
}

func relocation(sku, from, to string) Relocation {
	return Relocation{ID: "rel-1", Site: "SITE-1", SKU: SKU(sku), FromBin: SlotCode(from), ToBin: SlotCode(to), Quantity: 7, At: t2}
}

func TestParseMoveRef(t *testing.T) {
	tests := []struct {
		in   string
		want MoveRef
		ok   bool
	}{
		{"plan-7:3", MoveRef{Plan: "plan-7", Seq: 3}, true},
		{"plan-7:0", MoveRef{Plan: "plan-7", Seq: 0}, true},
		{"plan-1b4e28ba-2fa1:12", MoveRef{Plan: "plan-1b4e28ba-2fa1", Seq: 12}, true},
		{"plan-a:b:4", MoveRef{Plan: "plan-a:b", Seq: 4}, true},
		{"plan-7:9", MoveRef{Plan: "plan-7", Seq: 9}, true},
		{"plan-7:90", MoveRef{Plan: "plan-7", Seq: 90}, true},
		{"", MoveRef{}, false},
		{"task-1", MoveRef{}, false},
		{"plan-9#3", MoveRef{}, false},
		{"plan-7:", MoveRef{}, false},
		{":3", MoveRef{}, false},
		{"plan-:3", MoveRef{}, false},
		{"order-7:3", MoveRef{}, false},
		{"plan-7:-1", MoveRef{}, false},
		{"plan-7:+1", MoveRef{}, false},
		{"plan-7:1a", MoveRef{}, false},
		{"plan-7: 1", MoveRef{}, false},
		{"plan-7:99999999999999999999", MoveRef{}, false},
	}
	for _, tc := range tests {
		got, ok := ParseMoveRef(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("ParseMoveRef(%q) = %+v, %v; want %+v, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestMoveRefString(t *testing.T) {
	if got := (MoveRef{Plan: "plan-7", Seq: 3}).String(); got != "plan-7:3" {
		t.Fatalf("String() = %q", got)
	}
}

func TestNewRelocationValid(t *testing.T) {
	at := t2.In(time.FixedZone("BRT", -3*3600))
	r, err := NewRelocation("rel-42", "WH1", "SKU-R1", "A-01", "A-02", 7, at)
	if err != nil {
		t.Fatal(err)
	}
	want := Relocation{ID: "rel-42", Site: "WH1", SKU: "SKU-R1", FromBin: "A-01", ToBin: "A-02", Quantity: 7, At: t2}
	if !reflect.DeepEqual(r, want) || r.At.Location() != time.UTC {
		t.Fatalf("NewRelocation = %+v, want %+v (UTC)", r, want)
	}
	if _, err := NewRelocation("rel-42", "WH1", "SKU-R1", "A-01", "A-02", 1, at); err != nil {
		t.Fatalf("quantity 1 must be accepted: %v", err)
	}
}

func TestNewRelocationRejectsMissingFields(t *testing.T) {
	tests := []struct {
		name                    string
		id, site, sku, from, to string
		qty                     int64
		at                      time.Time
	}{
		{"no relocation id", " ", "WH1", "S", "A", "B", 1, t2},
		{"no site", "r", "", "S", "A", "B", 1, t2},
		{"no sku", "r", "WH1", "", "A", "B", 1, t2},
		{"no from bin", "r", "WH1", "S", "", "B", 1, t2},
		{"no to bin", "r", "WH1", "S", "A", "", 1, t2},
		{"zero quantity", "r", "WH1", "S", "A", "B", 0, t2},
		{"negative quantity", "r", "WH1", "S", "A", "B", -1, t2},
		{"no occurred_at", "r", "WH1", "S", "A", "B", 1, time.Time{}},
	}
	for _, tc := range tests {
		if _, err := NewRelocation(tc.id, tc.site, tc.sku, tc.from, tc.to, tc.qty, tc.at); !errors.Is(err, ErrInvalidRelocation) {
			t.Errorf("%s: err = %v, want ErrInvalidRelocation", tc.name, err)
		}
	}
}

func TestMatchRefMatchesTheMoveAtItsPosition(t *testing.T) {
	p := approvedPlan(t)
	for seq, sku := range []string{"SKU-1", "SKU-2", "SKU-3"} {
		got, reason := p.MatchRef(MoveRef{Plan: p.ID(), Seq: seq}, relocation(sku, "X", "Y"))
		if reason != "" || got != seq {
			t.Errorf("seq %d: MatchRef = %d, %q; want %d, matched", seq, got, reason, seq)
		}
	}
}

func TestMatchRefUnmatched(t *testing.T) {
	p := approvedPlan(t)
	other := relocation("SKU-1", "X", "Y")
	other.Site = "SITE-2"
	tests := []struct {
		name string
		seq  int
		rel  Relocation
		want UnmatchedReason
	}{
		{"other site", 0, other, UnmatchedSiteMismatch},
		{"position past the last move", 3, relocation("SKU-1", "X", "Y"), UnmatchedUnknownMove},
		{"negative position", -1, relocation("SKU-1", "X", "Y"), UnmatchedUnknownMove},
		{"another sku at that position", 1, relocation("SKU-1", "X", "Y"), UnmatchedSKUMismatch},
	}
	for _, tc := range tests {
		got, reason := p.MatchRef(MoveRef{Plan: p.ID(), Seq: tc.seq}, tc.rel)
		if reason != tc.want || got != -1 {
			t.Errorf("%s: MatchRef = %d, %q; want -1, %q", tc.name, got, reason, tc.want)
		}
	}
}

func TestMatchRefOnlyOnExecutablePlans(t *testing.T) {
	draft := mustGenerate(t)
	if _, reason := draft.MatchRef(MoveRef{Plan: draft.ID(), Seq: 0}, relocation("SKU-1", "X", "FWD-01")); reason != UnmatchedPlanNotApproved {
		t.Fatalf("draft: reason %q, want %q", reason, UnmatchedPlanNotApproved)
	}
	rejected := mustGenerate(t)
	if _, err := rejected.Reject("", t1); err != nil {
		t.Fatal(err)
	}
	if _, reason := rejected.MatchRef(MoveRef{Plan: rejected.ID(), Seq: 0}, relocation("SKU-1", "X", "FWD-01")); reason != UnmatchedPlanNotApproved {
		t.Fatalf("rejected: reason %q, want %q", reason, UnmatchedPlanNotApproved)
	}
	superseded := approvedPlan(t)
	if err := superseded.Supersede(t2); err != nil {
		t.Fatal(err)
	}
	if seq, reason := superseded.MatchRef(MoveRef{Plan: superseded.ID(), Seq: 0}, relocation("SKU-1", "X", "FWD-01")); reason != "" || seq != 0 {
		t.Fatalf("superseded: %d, %q; want 0, matched (released tasks run to completion)", seq, reason)
	}
}

func TestMatchBinsByKind(t *testing.T) {
	p := approvedPlan(t)
	tests := []struct {
		name string
		rel  Relocation
		want int
	}{
		{"assign: any source, to the assigned slot", relocation("SKU-1", "RES-77", "FWD-01"), 0},
		{"relocate: from and to slots", relocation("SKU-2", "FWD-09", "FWD-02"), 1},
		{"vacate: from the slot, to any reserve bin", relocation("SKU-3", "FWD-03", "RES-12"), 2},
	}
	for _, tc := range tests {
		got, reason := p.MatchBins(tc.rel)
		if reason != "" || got != tc.want {
			t.Errorf("%s: MatchBins = %d, %q; want %d, matched", tc.name, got, reason, tc.want)
		}
	}
}

func TestMatchBinsUnmatched(t *testing.T) {
	p := approvedPlan(t)
	other := relocation("SKU-1", "RES-1", "FWD-01")
	other.Site = "SITE-2"
	tests := []struct {
		name string
		rel  Relocation
		want UnmatchedReason
	}{
		{"other site", other, UnmatchedSiteMismatch},
		{"assign to another slot", relocation("SKU-1", "RES-1", "FWD-02"), UnmatchedNoMatchingMove},
		{"relocate from another slot", relocation("SKU-2", "FWD-08", "FWD-02"), UnmatchedNoMatchingMove},
		{"relocate to another slot", relocation("SKU-2", "FWD-09", "FWD-05"), UnmatchedNoMatchingMove},
		{"vacate from another slot", relocation("SKU-3", "FWD-04", "RES-1"), UnmatchedNoMatchingMove},
		{"a sku with no move", relocation("SKU-9", "FWD-09", "FWD-02"), UnmatchedNoMatchingMove},
	}
	for _, tc := range tests {
		got, reason := p.MatchBins(tc.rel)
		if reason != tc.want || got != -1 {
			t.Errorf("%s: MatchBins = %d, %q; want -1, %q", tc.name, got, reason, tc.want)
		}
	}
	if _, reason := mustGenerate(t).MatchBins(relocation("SKU-1", "RES-1", "FWD-01")); reason != UnmatchedPlanNotApproved {
		t.Fatalf("draft: reason %q, want %q", reason, UnmatchedPlanNotApproved)
	}
}

func TestProgressWithoutExecutions(t *testing.T) {
	got := approvedPlan(t).Progress(nil)
	want := ExecutionProgress{
		MovesTotal: 3,
		Moves: []MoveProgress{
			{Seq: 0, SKU: "SKU-1", Kind: MoveAssign},
			{Seq: 1, SKU: "SKU-2", Kind: MoveRelocate},
			{Seq: 2, SKU: "SKU-3", Kind: MoveVacate},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Progress(nil) = %+v, want %+v", got, want)
	}
}

func TestProgressCountsExecutedMovesAndSumsQuantities(t *testing.T) {
	early, late := t1, t2.In(time.FixedZone("BRT", -3*3600))
	got := approvedPlan(t).Progress([]MoveExecution{
		{Seq: 2, RelocationID: "rel-3", Quantity: 4, At: late},
		{Seq: 0, RelocationID: "rel-1", Quantity: 5, At: early},
		{Seq: 0, RelocationID: "rel-2", Quantity: 2, At: early.Add(time.Minute)},
		{Seq: 3, RelocationID: "rel-x", Quantity: 9, At: late.Add(time.Hour)},
		{Seq: -1, RelocationID: "rel-y", Quantity: 9, At: late.Add(time.Hour)},
	})
	want := ExecutionProgress{
		MovesTotal:     3,
		MovesExecuted:  2,
		LastExecutedAt: t2,
		Moves: []MoveProgress{
			{Seq: 0, SKU: "SKU-1", Kind: MoveAssign, Executed: true, ExecutedQuantity: 7, LastExecutedAt: early.Add(time.Minute)},
			{Seq: 1, SKU: "SKU-2", Kind: MoveRelocate},
			{Seq: 2, SKU: "SKU-3", Kind: MoveVacate, Executed: true, ExecutedQuantity: 4, LastExecutedAt: t2},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Progress = %+v\nwant      %+v", got, want)
	}
	if got.LastExecutedAt.Location() != time.UTC || got.Moves[2].LastExecutedAt.Location() != time.UTC {
		t.Fatal("instants must be UTC")
	}
}

func TestProgressKeepsTheLatestInstantWhateverTheOrder(t *testing.T) {
	got := approvedPlan(t).Progress([]MoveExecution{
		{Seq: 1, RelocationID: "rel-2", Quantity: 1, At: t2},
		{Seq: 1, RelocationID: "rel-1", Quantity: 1, At: t1},
	})
	if !got.Moves[1].LastExecutedAt.Equal(t2) || !got.LastExecutedAt.Equal(t2) || got.MovesExecuted != 1 {
		t.Fatalf("Progress = %+v: the latest instant wins", got)
	}
}

func TestProgressOfAPlanWithoutMoves(t *testing.T) {
	in := validNew()
	in.Moves = nil
	p, _, err := Generate(in, t0)
	if err != nil {
		t.Fatal(err)
	}
	got := p.Progress([]MoveExecution{{Seq: 0, RelocationID: "r", Quantity: 1, At: t1}})
	if got.MovesTotal != 0 || got.MovesExecuted != 0 || !got.LastExecutedAt.IsZero() || len(got.Moves) != 0 || got.Moves == nil {
		t.Fatalf("Progress = %+v: nothing to execute", got)
	}
}
