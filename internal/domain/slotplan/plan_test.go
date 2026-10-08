package slotplan

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var (
	t0 = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	t1 = t0.Add(time.Hour)
	t2 = t0.Add(2 * time.Hour)
)

func window() Window {
	return Window{From: t0.AddDate(0, 0, -28), To: t0}
}

// validNew has two assignments (given out of SKU order), an Assign, a
// Relocate and a Vacate move, and one unassigned SKU.
func validNew() NewPlan {
	return NewPlan{
		ID:     "plan-1",
		Site:   "SITE-1",
		Window: window(),
		Policy: PolicyABCVelocityV1,
		Assignments: []Assignment{
			{SKU: "SKU-2", Slot: "FWD-02", Class: ClassB, Picks: 20, Units: 40},
			{SKU: "SKU-1", Slot: "FWD-01", Class: ClassA, Picks: 80, Units: 160},
		},
		Moves: []Move{
			{SKU: "SKU-3", From: "FWD-03", Kind: MoveVacate},
			{SKU: "SKU-2", From: "FWD-09", To: "FWD-02", Kind: MoveRelocate},
			{SKU: "SKU-1", To: "FWD-01", Kind: MoveAssign},
		},
		Unassigned: []Unassigned{
			{SKU: "SKU-5", Reason: ReasonNoPhysicalProfile},
			{SKU: "SKU-4", Reason: ReasonNoCapacityFit},
		},
	}
}

func mustGenerate(t *testing.T) *SlotPlan {
	t.Helper()
	p, _, err := Generate(validNew(), t0)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return p
}

func TestGenerateHappyPath(t *testing.T) {
	p, events, err := Generate(validNew(), t0.In(time.FixedZone("BRT", -3*3600)))
	if err != nil {
		t.Fatal(err)
	}
	if p.ID() != "plan-1" || p.Site() != "SITE-1" || p.State() != StateDraft || p.Version() != 1 {
		t.Fatalf("unexpected plan: %v %v %v %v", p.ID(), p.Site(), p.State(), p.Version())
	}
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	ev, ok := events[0].(SlotPlanGenerated)
	if !ok {
		t.Fatalf("want SlotPlanGenerated, got %T", events[0])
	}
	want := SlotPlanGenerated{
		Header:          Header{Plan: "plan-1", Site: "SITE-1", Version: 1, At: t0},
		Window:          window(),
		Policy:          PolicyABCVelocityV1,
		AssignmentCount: 2,
		MoveCount:       3,
		UnassignedCount: 2,
	}
	if !reflect.DeepEqual(ev, want) {
		t.Fatalf("event = %+v, want %+v", ev, want)
	}
	if ev.EventName() != "SlotPlanGenerated" || ev.PlanID() != "plan-1" || ev.SiteID() != "SITE-1" ||
		ev.PlanVersion() != 1 || !ev.OccurredAt().Equal(t0) || ev.OccurredAt().Location() != time.UTC {
		t.Fatalf("event accessors wrong: %+v", ev)
	}
}

func TestGenerateStoresSortedBySKUAndCopiesInput(t *testing.T) {
	in := validNew()
	p, _, err := Generate(in, t0)
	if err != nil {
		t.Fatal(err)
	}
	s := p.Snapshot()
	if s.Assignments[0].SKU != "SKU-1" || s.Assignments[1].SKU != "SKU-2" {
		t.Errorf("assignments not sorted by SKU: %+v", s.Assignments)
	}
	if s.Moves[0].SKU != "SKU-1" || s.Moves[1].SKU != "SKU-2" || s.Moves[2].SKU != "SKU-3" {
		t.Errorf("moves not sorted by SKU: %+v", s.Moves)
	}
	if s.Unassigned[0].SKU != "SKU-4" || s.Unassigned[1].SKU != "SKU-5" {
		t.Errorf("unassigned not sorted by SKU: %+v", s.Unassigned)
	}
	if in.Assignments[0].SKU != "SKU-2" {
		t.Error("Generate must not reorder the caller's slice")
	}
	in.Assignments[0].Picks = 999
	if p.Snapshot().Assignments[1].Picks != 20 {
		t.Error("the plan must hold its own copy of the assignments")
	}
	s.Assignments[0].Picks = 555
	s.Moves[0].Kind = MoveVacate
	s.Unassigned[0].Reason = ReasonNoEligibleSlot
	again := p.Snapshot()
	if again.Assignments[0].Picks != 80 || again.Moves[0].Kind != MoveAssign || again.Unassigned[0].Reason != ReasonNoCapacityFit {
		t.Error("Snapshot must hand out copies")
	}
}

func TestGenerateAllowsEmptyPlan(t *testing.T) {
	in := validNew()
	in.Assignments, in.Moves, in.Unassigned = nil, nil, nil
	p, events, err := Generate(in, t0)
	if err != nil {
		t.Fatal(err)
	}
	ev := events[0].(SlotPlanGenerated)
	if ev.AssignmentCount != 0 || ev.MoveCount != 0 || ev.UnassignedCount != 0 || p.State() != StateDraft {
		t.Fatalf("empty plan: %+v", ev)
	}
}

func TestGenerateRejectsInvalidInput(t *testing.T) {
	mutate := func(f func(*NewPlan)) NewPlan {
		in := validNew()
		f(&in)
		return in
	}
	tests := []struct {
		name string
		in   NewPlan
		want error
	}{
		{"bad plan id", mutate(func(n *NewPlan) { n.ID = "x" }), ErrInvalidPlanID},
		{"bad site", mutate(func(n *NewPlan) { n.Site = "" }), ErrInvalidSiteID},
		{"bad policy", mutate(func(n *NewPlan) { n.Policy = "a b" }), ErrInvalidPolicy},
		{"bad window", mutate(func(n *NewPlan) { n.Window = Window{From: t0, To: t0} }), ErrInvalidWindow},
		{"bad assignment sku", mutate(func(n *NewPlan) { n.Assignments[0].SKU = "" }), ErrInvalidSKU},
		{"bad assignment slot", mutate(func(n *NewPlan) { n.Assignments[0].Slot = "" }), ErrInvalidSlotCode},
		{"bad class", mutate(func(n *NewPlan) { n.Assignments[0].Class = "D" }), ErrInvalidABCClass},
		{"zero picks", mutate(func(n *NewPlan) { n.Assignments[0].Picks = 0 }), ErrInvalidMetric},
		{"negative units", mutate(func(n *NewPlan) { n.Assignments[0].Units = -1 }), ErrInvalidMetric},
		{"duplicate sku", mutate(func(n *NewPlan) { n.Assignments[0].SKU = "SKU-1" }), ErrDuplicateSKU},
		{"two skus in one slot", mutate(func(n *NewPlan) { n.Assignments[0].Slot = "FWD-01" }), ErrDuplicateSlot},
		{"bad move sku", mutate(func(n *NewPlan) { n.Moves[0].SKU = "" }), ErrInvalidSKU},
		{"bad move kind", mutate(func(n *NewPlan) { n.Moves[0].Kind = "Move" }), ErrInvalidMoveKind},
		{"two moves for one sku", mutate(func(n *NewPlan) { n.Moves[0].SKU = "SKU-1" }), ErrInvalidMove},
		{"assign with from", mutate(func(n *NewPlan) { n.Moves[2].From = "FWD-09" }), ErrInvalidMove},
		{"assign without to", mutate(func(n *NewPlan) { n.Moves[2].To = "" }), ErrInvalidMove},
		{"assign to the wrong slot", mutate(func(n *NewPlan) { n.Moves[2].To = "FWD-77" }), ErrInvalidMove},
		{"assign of an unassigned sku", mutate(func(n *NewPlan) { n.Moves[2].SKU = "SKU-8" }), ErrInvalidMove},
		{"relocate without from", mutate(func(n *NewPlan) { n.Moves[1].From = "" }), ErrInvalidMove},
		{"relocate without to", mutate(func(n *NewPlan) { n.Moves[1].To = "" }), ErrInvalidMove},
		{"relocate to the same slot", mutate(func(n *NewPlan) { n.Moves[1].From = "FWD-02" }), ErrInvalidMove},
		{"relocate to the wrong slot", mutate(func(n *NewPlan) { n.Moves[1].To = "FWD-77" }), ErrInvalidMove},
		{"relocate of an unassigned sku", mutate(func(n *NewPlan) { n.Moves[1].SKU = "SKU-8" }), ErrInvalidMove},
		{"vacate without from", mutate(func(n *NewPlan) { n.Moves[0].From = "" }), ErrInvalidMove},
		{"vacate with to", mutate(func(n *NewPlan) { n.Moves[0].To = "FWD-03" }), ErrInvalidMove},
		{"vacate of an assigned sku", mutate(func(n *NewPlan) { n.Moves[0].SKU = "SKU-1" }), ErrInvalidMove},
		{"bad unassigned sku", mutate(func(n *NewPlan) { n.Unassigned[0].SKU = "" }), ErrInvalidSKU},
		{"bad reason", mutate(func(n *NewPlan) { n.Unassigned[0].Reason = "x" }), ErrInvalidReason},
		{"duplicate unassigned", mutate(func(n *NewPlan) { n.Unassigned[1].SKU = "SKU-5" }), ErrInvalidUnassigned},
		{"unassigned and assigned", mutate(func(n *NewPlan) { n.Unassigned[0].SKU = "SKU-1" }), ErrInvalidUnassigned},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, events, err := Generate(tt.in, t0)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if p != nil || events != nil {
				t.Fatal("a failed Generate must return nothing")
			}
		})
	}
}

func TestGenerateAcceptsSmallestMetrics(t *testing.T) {
	in := validNew()
	in.Assignments[0].Picks, in.Assignments[0].Units = 1, 0
	if _, _, err := Generate(in, t0); err != nil {
		t.Fatalf("1 pick and 0 units is the smallest legal assignment: %v", err)
	}
}

func TestGenerateRejectsZeroNow(t *testing.T) {
	if _, _, err := Generate(validNew(), time.Time{}); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("want ErrInvalidSnapshot, got %v", err)
	}
}

func TestApprove(t *testing.T) {
	p := mustGenerate(t)
	events, err := p.Approve("plan-0", t1)
	if err != nil {
		t.Fatal(err)
	}
	if p.State() != StateApproved || p.Version() != 2 {
		t.Fatalf("state %v version %d", p.State(), p.Version())
	}
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	ev, ok := events[0].(SlotPlanApproved)
	if !ok {
		t.Fatalf("want SlotPlanApproved, got %T", events[0])
	}
	s := p.Snapshot()
	want := SlotPlanApproved{
		Header:           Header{Plan: "plan-1", Site: "SITE-1", Version: 2, At: t1},
		SupersedesPlanID: "plan-0",
		Assignments:      s.Assignments,
		Moves:            s.Moves,
	}
	if !reflect.DeepEqual(ev, want) {
		t.Fatalf("event = %+v, want %+v", ev, want)
	}
	if ev.EventName() != "SlotPlanApproved" || len(ev.Assignments) != 2 || len(ev.Moves) != 3 {
		t.Fatalf("approved event must carry the full map and the moves: %+v", ev)
	}
	if !s.ApprovedAt.Equal(t1) || s.SupersedesPlanID != "plan-0" || !s.RejectedAt.IsZero() || !s.SupersededAt.IsZero() {
		t.Fatalf("snapshot after approve: %+v", s)
	}
	ev.Assignments[0].Picks = 12345
	if p.Snapshot().Assignments[0].Picks == 12345 {
		t.Fatal("the event must carry copies")
	}
}

func TestApproveWithoutPredecessor(t *testing.T) {
	p := mustGenerate(t)
	events, err := p.Approve("", t1)
	if err != nil {
		t.Fatal(err)
	}
	if events[0].(SlotPlanApproved).SupersedesPlanID != "" || p.Snapshot().SupersedesPlanID != "" {
		t.Fatal("no predecessor means an empty supersedes id")
	}
}

func TestApproveValidatesSupersedes(t *testing.T) {
	for name, id := range map[string]PlanID{"malformed": "nope", "self": "plan-1"} {
		p := mustGenerate(t)
		events, err := p.Approve(id, t1)
		if !errors.Is(err, ErrInvalidSupersedes) || events != nil {
			t.Errorf("%s: err = %v events = %v", name, err, events)
		}
		if p.State() != StateDraft || p.Version() != 1 {
			t.Errorf("%s: a failed approval must leave the plan untouched", name)
		}
	}
}

func TestDecisionsOnlyFromDraft(t *testing.T) {
	approved := func() *SlotPlan {
		p := mustGenerate(t)
		if _, err := p.Approve("", t1); err != nil {
			t.Fatal(err)
		}
		return p
	}
	rejected := func() *SlotPlan {
		p := mustGenerate(t)
		if _, err := p.Reject("no", t1); err != nil {
			t.Fatal(err)
		}
		return p
	}
	superseded := func() *SlotPlan {
		p := approved()
		if err := p.Supersede(t2); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for name, build := range map[string]func() *SlotPlan{"approved": approved, "rejected": rejected, "superseded": superseded} {
		p := build()
		before := p.Snapshot()
		if events, err := p.Approve("", t2); !errors.Is(err, ErrNotDraft) || events != nil {
			t.Errorf("%s Approve: err = %v events = %v", name, err, events)
		}
		if events, err := p.Reject("again", t2); !errors.Is(err, ErrNotDraft) || events != nil {
			t.Errorf("%s Reject: err = %v events = %v", name, err, events)
		}
		if !reflect.DeepEqual(before, p.Snapshot()) {
			t.Errorf("%s: immutable plan changed", name)
		}
	}
}

func TestReject(t *testing.T) {
	p := mustGenerate(t)
	events, err := p.Reject("  too much churn \n", t1)
	if err != nil {
		t.Fatal(err)
	}
	ev, ok := events[0].(SlotPlanRejected)
	if !ok || len(events) != 1 {
		t.Fatalf("want one SlotPlanRejected, got %v", events)
	}
	want := SlotPlanRejected{Header: Header{Plan: "plan-1", Site: "SITE-1", Version: 2, At: t1}, Reason: "too much churn"}
	if !reflect.DeepEqual(ev, want) || ev.EventName() != "SlotPlanRejected" {
		t.Fatalf("event = %+v, want %+v", ev, want)
	}
	s := p.Snapshot()
	if p.State() != StateRejected || p.Version() != 2 || s.RejectReason != "too much churn" ||
		!s.RejectedAt.Equal(t1) || !s.ApprovedAt.IsZero() {
		t.Fatalf("snapshot after reject: %+v", s)
	}
}

func TestRejectReasonLimits(t *testing.T) {
	p := mustGenerate(t)
	if _, err := p.Reject("", t1); err != nil {
		t.Fatalf("an empty reason is allowed: %v", err)
	}
	max := strings.Repeat("é", 500)
	p = mustGenerate(t)
	if _, err := p.Reject(max, t1); err != nil {
		t.Fatalf("500 characters are allowed: %v", err)
	}
	p = mustGenerate(t)
	events, err := p.Reject(max+"é", t1)
	if !errors.Is(err, ErrRejectReasonTooLong) || events != nil {
		t.Fatalf("501 characters: err = %v events = %v", err, events)
	}
	if p.State() != StateDraft || p.Version() != 1 {
		t.Fatal("a failed rejection must leave the plan untouched")
	}
	p = mustGenerate(t)
	if _, err := p.Reject(" "+max+" ", t1); err != nil {
		t.Fatalf("the limit applies after trimming: %v", err)
	}
}

func TestSupersede(t *testing.T) {
	p := mustGenerate(t)
	if err := p.Supersede(t1); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("Draft: want ErrNotApproved, got %v", err)
	}
	if _, err := p.Approve("", t1); err != nil {
		t.Fatal(err)
	}
	if err := p.Supersede(t2); err != nil {
		t.Fatal(err)
	}
	s := p.Snapshot()
	if p.State() != StateSuperseded || p.Version() != 3 || !s.SupersededAt.Equal(t2) || !s.ApprovedAt.Equal(t1) {
		t.Fatalf("snapshot after supersede: %+v", s)
	}
	if err := p.Supersede(t2); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("Superseded: want ErrNotApproved, got %v", err)
	}
	r := mustGenerate(t)
	if _, err := r.Reject("", t1); err != nil {
		t.Fatal(err)
	}
	if err := r.Supersede(t2); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("Rejected: want ErrNotApproved, got %v", err)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	p := mustGenerate(t)
	if _, err := p.Approve("plan-0", t1); err != nil {
		t.Fatal(err)
	}
	got, err := Restore(p.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Snapshot(), p.Snapshot()) {
		t.Fatalf("round trip changed the plan:\n%+v\n%+v", got.Snapshot(), p.Snapshot())
	}
}

func TestRestoreNormalisesToUTC(t *testing.T) {
	brt := time.FixedZone("BRT", -3*3600)
	s := mustGenerate(t).Snapshot()
	s.State = StateSuperseded
	s.ApprovedAt, s.SupersededAt = t1.In(brt), t2.In(brt)
	s.GeneratedAt = t0.In(brt)
	s.Window = Window{From: window().From.In(brt), To: window().To.In(brt)}
	p, err := Restore(s)
	if err != nil {
		t.Fatal(err)
	}
	got := p.Snapshot()
	for name, ts := range map[string]time.Time{
		"generated": got.GeneratedAt, "approved": got.ApprovedAt, "superseded": got.SupersededAt,
		"from": got.Window.From, "to": got.Window.To,
	} {
		if ts.Location() != time.UTC {
			t.Errorf("%s is not UTC", name)
		}
	}
	if !got.RejectedAt.IsZero() || got.RejectedAt.Location() != time.UTC {
		t.Error("an unset timestamp stays the zero time")
	}
}

func TestRestoreValidatesState(t *testing.T) {
	base := func(st State, f func(*Snapshot)) Snapshot {
		s := mustGenerate(t).Snapshot()
		s.State = st
		switch st {
		case StateApproved:
			s.ApprovedAt = t1
		case StateRejected:
			s.RejectedAt = t1
		case StateSuperseded:
			s.ApprovedAt, s.SupersededAt = t1, t2
		}
		f(&s)
		return s
	}
	noop := func(*Snapshot) {}
	for _, st := range []State{StateDraft, StateApproved, StateRejected, StateSuperseded} {
		if _, err := Restore(base(st, noop)); err != nil {
			t.Errorf("%s must restore: %v", st, err)
		}
	}
	if _, err := Restore(base(StateRejected, func(s *Snapshot) { s.RejectReason = "no" })); err != nil {
		t.Errorf("a reject reason on a Rejected plan is legal: %v", err)
	}
	for _, st := range []State{StateApproved, StateSuperseded} {
		if _, err := Restore(base(st, func(s *Snapshot) { s.SupersedesPlanID = "plan-0" })); err != nil {
			t.Errorf("%s with a predecessor is legal: %v", st, err)
		}
	}
	bad := map[string]struct {
		s    Snapshot
		want error
	}{
		"unknown state":                   {base("Pending", noop), ErrInvalidState},
		"empty state":                     {base("", noop), ErrInvalidState},
		"draft with approvedAt":           {base(StateDraft, func(s *Snapshot) { s.ApprovedAt = t1 }), ErrInvalidSnapshot},
		"draft with rejectedAt":           {base(StateDraft, func(s *Snapshot) { s.RejectedAt = t1 }), ErrInvalidSnapshot},
		"draft with supersededAt":         {base(StateDraft, func(s *Snapshot) { s.SupersededAt = t1 }), ErrInvalidSnapshot},
		"approved without approvedAt":     {base(StateApproved, func(s *Snapshot) { s.ApprovedAt = time.Time{} }), ErrInvalidSnapshot},
		"approved with rejectedAt":        {base(StateApproved, func(s *Snapshot) { s.RejectedAt = t1 }), ErrInvalidSnapshot},
		"approved with supersededAt":      {base(StateApproved, func(s *Snapshot) { s.SupersededAt = t2 }), ErrInvalidSnapshot},
		"rejected without rejectedAt":     {base(StateRejected, func(s *Snapshot) { s.RejectedAt = time.Time{} }), ErrInvalidSnapshot},
		"rejected with approvedAt":        {base(StateRejected, func(s *Snapshot) { s.ApprovedAt = t1 }), ErrInvalidSnapshot},
		"rejected with supersededAt":      {base(StateRejected, func(s *Snapshot) { s.SupersededAt = t1 }), ErrInvalidSnapshot},
		"superseded without supersededAt": {base(StateSuperseded, func(s *Snapshot) { s.SupersededAt = time.Time{} }), ErrInvalidSnapshot},
		"superseded without approvedAt":   {base(StateSuperseded, func(s *Snapshot) { s.ApprovedAt = time.Time{} }), ErrInvalidSnapshot},
		"superseded with rejectedAt":      {base(StateSuperseded, func(s *Snapshot) { s.RejectedAt = t1 }), ErrInvalidSnapshot},
		"reason on a draft":               {base(StateDraft, func(s *Snapshot) { s.RejectReason = "x" }), ErrInvalidSnapshot},
		"reason on an approved plan":      {base(StateApproved, func(s *Snapshot) { s.RejectReason = "x" }), ErrInvalidSnapshot},
		"predecessor on a draft":          {base(StateDraft, func(s *Snapshot) { s.SupersedesPlanID = "plan-0" }), ErrInvalidSnapshot},
		"predecessor on a rejected":       {base(StateRejected, func(s *Snapshot) { s.SupersedesPlanID = "plan-0" }), ErrInvalidSnapshot},
		"malformed predecessor":           {base(StateApproved, func(s *Snapshot) { s.SupersedesPlanID = "zz" }), ErrInvalidSupersedes},
		"self predecessor":                {base(StateApproved, func(s *Snapshot) { s.SupersedesPlanID = s.ID }), ErrInvalidSupersedes},
		"version zero":                    {base(StateDraft, func(s *Snapshot) { s.Version = 0 }), ErrInvalidSnapshot},
		"no generatedAt":                  {base(StateDraft, func(s *Snapshot) { s.GeneratedAt = time.Time{} }), ErrInvalidSnapshot},
	}
	for name, c := range bad {
		if p, err := Restore(c.s); !errors.Is(err, c.want) || p != nil {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
	}
	s := base(StateDraft, func(s *Snapshot) { s.Version = 1 })
	if _, err := Restore(s); err != nil {
		t.Errorf("version 1 is the smallest legal version: %v", err)
	}
}

func TestHeaderAccessors(t *testing.T) {
	h := Header{Plan: "plan-7", Site: "S", Version: 4, At: t2}
	var ev Event = SlotPlanRejected{Header: h}
	if ev.PlanID() != "plan-7" || ev.SiteID() != "S" || ev.PlanVersion() != 4 || !ev.OccurredAt().Equal(t2) {
		t.Fatalf("header accessors wrong: %+v", h)
	}
}
