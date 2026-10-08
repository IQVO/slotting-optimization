// Package repocontract holds the behavioural contract of the outbound
// repository ports as plain functions over the ports, so the in-memory
// adapters (unit tests) and the Postgres adapters (testcontainers
// integration tests) are proven to behave identically. It lives outside
// internal/adapters because adapters never import each other.
package repocontract

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// T0 is the reference instant of the contract fixtures.
var T0 = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

// Draft builds a Draft plan of site generated at generatedAt with one
// assignment (SKU-1 in slot "slot-<id>"), one Assign move and one
// unassigned SKU.
func Draft(t *testing.T, id, site string, generatedAt time.Time) *slotplan.SlotPlan {
	t.Helper()
	w, err := slotplan.NewWindow(generatedAt.Add(-28*24*time.Hour), generatedAt)
	if err != nil {
		t.Fatal(err)
	}
	slot := slotplan.SlotCode("slot-" + id)
	p, _, err := slotplan.Generate(slotplan.NewPlan{
		ID: slotplan.PlanID(id), Site: slotplan.SiteID(site), Window: w, Policy: slotplan.PolicyABCVelocityV1,
		Assignments: []slotplan.Assignment{{SKU: "SKU-1", Slot: slot, Class: slotplan.ClassA, Picks: 3, Units: 9}},
		Moves:       []slotplan.Move{{SKU: "SKU-1", To: slot, Kind: slotplan.MoveAssign}},
		Unassigned:  []slotplan.Unassigned{{SKU: "SKU-2", Reason: slotplan.ReasonNoPhysicalProfile}},
	}, generatedAt)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func wantErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

// SlotPlanRepository runs the SlotPlanRepository contract. newRepo must
// return an EMPTY repository on every call.
func SlotPlanRepository(t *testing.T, newRepo func(t *testing.T) ports.SlotPlanRepository) {
	t.Helper()
	cases := map[string]func(t *testing.T, r ports.SlotPlanRepository){
		"round trip keeps the whole plan":             roundTrip,
		"unknown plan is not found":                   unknownPlan,
		"insert of an existing id is a conflict":      duplicateInsert,
		"update is guarded by the loaded version":     versionGuard,
		"one approved plan per site":                  oneApprovedPerSite,
		"supersede first lets the next plan approve":  supersedeThenApprove,
		"current is the approved plan of that site":   currentPerSite,
		"list is newest first with keyset paging":     listPaging,
		"list filters by site and state":              listFilters,
		"list after an unknown plan is an empty page": listUnknownCursor,
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) { fn(t, newRepo(t)) })
	}
}

func roundTrip(t *testing.T, r ports.SlotPlanRepository) {
	ctx := context.Background()
	p := Draft(t, "plan-a", "SITE-1", T0)
	must(t, r.Save(ctx, p, 0))
	got, err := r.Get(ctx, "plan-a")
	must(t, err)
	want, have := p.Snapshot(), got.Snapshot()
	if !sameSnapshot(want, have) {
		t.Fatalf("round trip lost data:\n want %+v\n have %+v", want, have)
	}
	if _, err := got.Approve("plan-z", T0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	must(t, r.Save(ctx, got, 1))
	again, err := r.Get(ctx, "plan-a")
	must(t, err)
	s := again.Snapshot()
	if s.State != slotplan.StateApproved || s.Version != 2 || s.SupersedesPlanID != "plan-z" || !s.ApprovedAt.Equal(T0.Add(time.Minute)) {
		t.Fatalf("approved plan = %+v", s)
	}
}

func sameSnapshot(a, b slotplan.Snapshot) bool {
	if a.ID != b.ID || a.Site != b.Site || a.Policy != b.Policy || a.State != b.State || a.Version != b.Version {
		return false
	}
	if !a.Window.From.Equal(b.Window.From) || !a.Window.To.Equal(b.Window.To) || !a.GeneratedAt.Equal(b.GeneratedAt) {
		return false
	}
	return equalSlice(a.Assignments, b.Assignments) && equalSlice(a.Moves, b.Moves) && equalSlice(a.Unassigned, b.Unassigned)
}

func equalSlice[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func unknownPlan(t *testing.T, r ports.SlotPlanRepository) {
	_, err := r.Get(context.Background(), "plan-nope")
	wantErr(t, err, repository.ErrPlanNotFound)
	_, err = r.Current(context.Background(), "SITE-1")
	wantErr(t, err, repository.ErrPlanNotFound)
}

func duplicateInsert(t *testing.T, r ports.SlotPlanRepository) {
	ctx := context.Background()
	must(t, r.Save(ctx, Draft(t, "plan-a", "SITE-1", T0), 0))
	wantErr(t, r.Save(ctx, Draft(t, "plan-a", "SITE-1", T0), 0), repository.ErrConcurrentModification)
}

func versionGuard(t *testing.T, r ports.SlotPlanRepository) {
	ctx := context.Background()
	must(t, r.Save(ctx, Draft(t, "plan-a", "SITE-1", T0), 0))
	first, err := r.Get(ctx, "plan-a")
	must(t, err)
	second, err := r.Get(ctx, "plan-a")
	must(t, err)

	_, err = first.Reject("no", T0.Add(time.Minute))
	must(t, err)
	must(t, r.Save(ctx, first, 1))
	_, err = second.Reject("late", T0.Add(2*time.Minute))
	must(t, err)
	wantErr(t, r.Save(ctx, second, 1), repository.ErrConcurrentModification)

	stored, err := r.Get(ctx, "plan-a")
	must(t, err)
	if s := stored.Snapshot(); s.RejectReason != "no" || s.Version != 2 {
		t.Fatalf("the losing writer must change nothing: %+v", s)
	}
	ghost := Draft(t, "plan-ghost", "SITE-1", T0)
	wantErr(t, r.Save(ctx, ghost, 5), repository.ErrConcurrentModification)
}

func approve(t *testing.T, r ports.SlotPlanRepository, id string) error {
	t.Helper()
	p, err := r.Get(context.Background(), slotplan.PlanID(id))
	must(t, err)
	loaded := p.Version()
	if _, err := p.Approve("", T0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	return r.Save(context.Background(), p, loaded)
}

func oneApprovedPerSite(t *testing.T, r ports.SlotPlanRepository) {
	ctx := context.Background()
	must(t, r.Save(ctx, Draft(t, "plan-a", "SITE-1", T0), 0))
	must(t, r.Save(ctx, Draft(t, "plan-b", "SITE-1", T0.Add(time.Minute)), 0))
	must(t, r.Save(ctx, Draft(t, "plan-c", "SITE-2", T0), 0))

	must(t, approve(t, r, "plan-a"))
	wantErr(t, approve(t, r, "plan-b"), repository.ErrApprovedPlanConflict)
	must(t, approve(t, r, "plan-c")) // another site is independent

	b, err := r.Get(ctx, "plan-b")
	must(t, err)
	if b.State() != slotplan.StateDraft || b.Version() != 1 {
		t.Fatalf("the conflicting approval must leave the draft alone: %s v%d", b.State(), b.Version())
	}
}

func supersedeThenApprove(t *testing.T, r ports.SlotPlanRepository) {
	ctx := context.Background()
	must(t, r.Save(ctx, Draft(t, "plan-a", "SITE-1", T0), 0))
	must(t, r.Save(ctx, Draft(t, "plan-b", "SITE-1", T0.Add(time.Minute)), 0))
	must(t, approve(t, r, "plan-a"))

	a, err := r.Get(ctx, "plan-a")
	must(t, err)
	loaded := a.Version()
	must(t, a.Supersede(T0.Add(2*time.Hour)))
	must(t, r.Save(ctx, a, loaded))
	must(t, approve(t, r, "plan-b"))

	cur, err := r.Current(ctx, "SITE-1")
	must(t, err)
	if cur.ID() != "plan-b" {
		t.Fatalf("current = %s", cur.ID())
	}
	old, err := r.Get(ctx, "plan-a")
	must(t, err)
	if old.State() != slotplan.StateSuperseded {
		t.Fatalf("plan-a = %s", old.State())
	}
}

func currentPerSite(t *testing.T, r ports.SlotPlanRepository) {
	ctx := context.Background()
	must(t, r.Save(ctx, Draft(t, "plan-a", "SITE-1", T0), 0))
	must(t, r.Save(ctx, Draft(t, "plan-c", "SITE-2", T0), 0))
	must(t, approve(t, r, "plan-c"))
	_, err := r.Current(ctx, "SITE-1")
	wantErr(t, err, repository.ErrPlanNotFound)
	cur, err := r.Current(ctx, "SITE-2")
	must(t, err)
	if cur.ID() != "plan-c" || cur.State() != slotplan.StateApproved {
		t.Fatalf("current = %s %s", cur.ID(), cur.State())
	}
}

func ids(ps []*slotplan.SlotPlan) []slotplan.PlanID {
	out := make([]slotplan.PlanID, len(ps))
	for i, p := range ps {
		out[i] = p.ID()
	}
	return out
}

func listPaging(t *testing.T, r ports.SlotPlanRepository) {
	ctx := context.Background()
	must(t, r.Save(ctx, Draft(t, "plan-1", "SITE-1", T0), 0))
	must(t, r.Save(ctx, Draft(t, "plan-2", "SITE-1", T0.Add(time.Hour)), 0))
	must(t, r.Save(ctx, Draft(t, "plan-3", "SITE-1", T0.Add(2*time.Hour)), 0))
	// same instant as plan-3: the id breaks the tie, descending
	must(t, r.Save(ctx, Draft(t, "plan-4", "SITE-1", T0.Add(2*time.Hour)), 0))

	page1, err := r.List(ctx, repository.PlanFilter{}, "", 2)
	must(t, err)
	if got := ids(page1); !equalSlice(got, []slotplan.PlanID{"plan-4", "plan-3"}) {
		t.Fatalf("page 1 = %v", got)
	}
	page2, err := r.List(ctx, repository.PlanFilter{}, "plan-3", 2)
	must(t, err)
	if got := ids(page2); !equalSlice(got, []slotplan.PlanID{"plan-2", "plan-1"}) {
		t.Fatalf("page 2 = %v", got)
	}
	page3, err := r.List(ctx, repository.PlanFilter{}, "plan-1", 2)
	must(t, err)
	if len(page3) != 0 {
		t.Fatalf("page 3 = %v", ids(page3))
	}
}

func listFilters(t *testing.T, r ports.SlotPlanRepository) {
	ctx := context.Background()
	must(t, r.Save(ctx, Draft(t, "plan-1", "SITE-1", T0), 0))
	must(t, r.Save(ctx, Draft(t, "plan-2", "SITE-2", T0.Add(time.Hour)), 0))
	must(t, approve(t, r, "plan-2"))

	bySite, err := r.List(ctx, repository.PlanFilter{Site: "SITE-1"}, "", 10)
	must(t, err)
	if got := ids(bySite); !equalSlice(got, []slotplan.PlanID{"plan-1"}) {
		t.Fatalf("by site = %v", got)
	}
	byState, err := r.List(ctx, repository.PlanFilter{State: slotplan.StateApproved}, "", 10)
	must(t, err)
	if got := ids(byState); !equalSlice(got, []slotplan.PlanID{"plan-2"}) {
		t.Fatalf("by state = %v", got)
	}
	none, err := r.List(ctx, repository.PlanFilter{Site: "SITE-1", State: slotplan.StateApproved}, "", 10)
	must(t, err)
	if len(none) != 0 {
		t.Fatalf("both filters = %v", ids(none))
	}
}

func listUnknownCursor(t *testing.T, r ports.SlotPlanRepository) {
	ctx := context.Background()
	must(t, r.Save(ctx, Draft(t, "plan-1", "SITE-1", T0), 0))
	got, err := r.List(ctx, repository.PlanFilter{}, "plan-unknown", 10)
	must(t, err)
	if len(got) != 0 {
		t.Fatalf("got %v", ids(got))
	}
}
