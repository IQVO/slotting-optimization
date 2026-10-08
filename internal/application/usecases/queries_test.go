package usecases_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

func TestGetPlan(t *testing.T) {
	h := newHarness()
	h.draft("plan-1", "SITE-1", t0)
	uc := &usecases.GetPlan{Plans: h.repo}
	p, err := uc.Handle(context.Background(), "plan-1")
	if err != nil || p.ID() != "plan-1" {
		t.Fatalf("plan %v err %v", p, err)
	}
	if _, err := uc.Handle(context.Background(), "bad"); !errors.Is(err, slotplan.ErrInvalidPlanID) {
		t.Fatalf("err = %v", err)
	}
	if _, err := uc.Handle(context.Background(), "plan-2"); !errors.Is(err, repository.ErrPlanNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestListPlansPagesNewestFirst(t *testing.T) {
	h := newHarness()
	for i, id := range []string{"plan-1", "plan-2", "plan-3"} {
		h.draft(id, "SITE-1", t0.Add(time.Duration(i)*time.Hour))
	}
	uc := &usecases.ListPlans{Plans: h.repo}

	first, err := uc.Handle(context.Background(), usecases.ListPlansQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Items[0].ID() != "plan-3" || first.Items[1].ID() != "plan-2" || first.NextCursor == "" {
		t.Fatalf("first page = %+v", first)
	}
	if h.repo.listLimit != 3 {
		t.Fatalf("one extra row must be read to know there is a next page, limit = %d", h.repo.listLimit)
	}
	second, err := uc.Handle(context.Background(), usecases.ListPlansQuery{Limit: 2, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].ID() != "plan-1" || second.NextCursor != "" {
		t.Fatalf("second page = %+v", second)
	}
}

func TestListPlansFiltersAndValidation(t *testing.T) {
	h := newHarness()
	h.draft("plan-1", "SITE-1", t0)
	h.approved("plan-2", "SITE-2", t0)
	uc := &usecases.ListPlans{Plans: h.repo}

	page, err := uc.Handle(context.Background(), usecases.ListPlansQuery{SiteID: "SITE-2", State: "Approved"})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID() != "plan-2" {
		t.Fatalf("page %+v err %v", page, err)
	}
	if h.repo.listFilter.Site != "SITE-2" || h.repo.listFilter.State != slotplan.StateApproved || h.repo.listLimit != usecases.DefaultListLimit+1 {
		t.Fatalf("filter %+v limit %d", h.repo.listFilter, h.repo.listLimit)
	}
	cases := []struct {
		name string
		q    usecases.ListPlansQuery
		want error
	}{
		{"limit too small", usecases.ListPlansQuery{Limit: -1}, usecases.ErrInvalidLimit},
		{"limit too big", usecases.ListPlansQuery{Limit: 501}, usecases.ErrInvalidLimit},
		{"cursor not base64", usecases.ListPlansQuery{Cursor: "!!"}, usecases.ErrInvalidCursor},
		{"cursor not a plan id", usecases.ListPlansQuery{Cursor: "bm9wZQ"}, usecases.ErrInvalidCursor},
		{"state unknown", usecases.ListPlansQuery{State: "Done"}, usecases.ErrInvalidState},
		{"site invalid", usecases.ListPlansQuery{SiteID: "a/b"}, slotplan.ErrInvalidSiteID},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := uc.Handle(context.Background(), c.q); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
	h.repo.listErr = errBoom
	if _, err := uc.Handle(context.Background(), usecases.ListPlansQuery{}); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v", err)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	c := usecases.EncodeCursor("plan-abc")
	id, err := usecases.DecodeCursor(c)
	if err != nil || id != "plan-abc" {
		t.Fatalf("id %q err %v", id, err)
	}
	if id, err := usecases.DecodeCursor(""); err != nil || id != "" {
		t.Fatalf("empty cursor = %q, %v", id, err)
	}
}

func TestListForwardSlots(t *testing.T) {
	h := newHarness()
	h.approved("plan-1", "SITE-1", t0)
	uc := &usecases.ListForwardSlots{Plans: h.repo, DefaultSite: "SITE-1"}

	m, err := uc.Handle(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if m.PlanID != "plan-1" || m.Site != "SITE-1" || len(m.Assignments) != 1 || !m.ApprovedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("map = %+v", m)
	}
	empty, err := uc.Handle(context.Background(), "SITE-9")
	if err != nil || empty.PlanID != "" || empty.Assignments == nil || len(empty.Assignments) != 0 {
		t.Fatalf("a site without an approved plan has an empty map: %+v %v", empty, err)
	}
	if _, err := uc.Handle(context.Background(), "a b"); !errors.Is(err, slotplan.ErrInvalidSiteID) {
		t.Fatalf("err = %v", err)
	}
	h.repo.curErr = errBoom
	if _, err := uc.Handle(context.Background(), ""); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v", err)
	}
}

func newVelocity(h *harness) *usecases.ListSkuVelocity {
	h.demand.velocities = []planning.SkuVelocity{{SKU: "A", Picks: 3, Units: 9}, {SKU: "B", Picks: 2, Units: 4}, {SKU: "C", Picks: 1, Units: 1}}
	return &usecases.ListSkuVelocity{Demand: h.demand, Clock: fixedClock{t0}, DefaultSite: "SITE-1"}
}

func TestListSkuVelocityLimitsAndWindows(t *testing.T) {
	h := newHarness()
	uc := newVelocity(h)

	r, err := uc.Handle(context.Background(), usecases.SkuVelocityQuery{Limit: 2, WindowDays: 7})
	noErr(t, err)
	eq(t, "items", len(r.Items), 2)
	eq(t, "site", r.Site, "SITE-1")
	timeEq(t, "to", r.To, t0)
	timeEq(t, "from", r.From, t0.Add(-7*24*time.Hour))
	timeEq(t, "window from handed to the ledger", h.demand.from, r.From)
	timeEq(t, "window to handed to the ledger", h.demand.to, r.To)

	def, err := uc.Handle(context.Background(), usecases.SkuVelocityQuery{SiteID: "SITE-2"})
	noErr(t, err)
	eq(t, "default items", len(def.Items), 3)
	eq(t, "explicit site", def.Site, "SITE-2")
	timeEq(t, "default window", def.From, t0.Add(-28*24*time.Hour))
}

func TestListSkuVelocityValidation(t *testing.T) {
	uc := newVelocity(newHarness())
	for name, q := range map[string]usecases.SkuVelocityQuery{
		"window too small": {WindowDays: -1}, "window too big": {WindowDays: 366}, "limit too big": {Limit: 501},
	} {
		_, err := uc.Handle(context.Background(), q)
		if !errors.Is(err, usecases.ErrInvalidLimit) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	_, err := uc.Handle(context.Background(), usecases.SkuVelocityQuery{SiteID: "a b"})
	isErr(t, err, slotplan.ErrInvalidSiteID)
}

func TestListSkuVelocityReadFailure(t *testing.T) {
	h := newHarness()
	uc := newVelocity(h)
	h.demand.velErr = errBoom
	_, err := uc.Handle(context.Background(), usecases.SkuVelocityQuery{})
	isErr(t, err, errBoom)
}
