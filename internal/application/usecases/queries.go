package usecases

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// GetPlan returns one plan.
type GetPlan struct {
	Plans ports.SlotPlanRepository
}

// Handle validates the plan id and loads the plan
// (repository.ErrPlanNotFound when unknown).
func (uc *GetPlan) Handle(ctx context.Context, rawID string) (*slotplan.SlotPlan, error) {
	id, err := slotplan.NewPlanID(rawID)
	if err != nil {
		return nil, err
	}
	return uc.Plans.Get(ctx, id)
}

// ListPlansQuery is the input of ListPlans. Limit 0 means the default;
// Cursor is the opaque nextCursor of the previous page; SiteID and State ""
// mean "no filter".
type ListPlansQuery struct {
	Limit  int
	Cursor string
	SiteID string
	State  string
}

// PlanPage is one page of plans, newest first. NextCursor is empty on the
// last page.
type PlanPage struct {
	Items      []*slotplan.SlotPlan
	NextCursor string
}

// ListPlans lists plans a page at a time (cursor = base64url of the last
// plan id of the previous page).
type ListPlans struct {
	Plans ports.SlotPlanRepository
}

// Handle runs the use case.
func (uc *ListPlans) Handle(ctx context.Context, q ListPlansQuery) (PlanPage, error) {
	limit, err := listLimit(q.Limit)
	if err != nil {
		return PlanPage{}, err
	}
	after, err := DecodeCursor(q.Cursor)
	if err != nil {
		return PlanPage{}, err
	}
	filter, err := planFilter(q)
	if err != nil {
		return PlanPage{}, err
	}
	items, err := uc.Plans.List(ctx, filter, after, limit+1)
	if err != nil {
		return PlanPage{}, err
	}
	page := PlanPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextCursor = EncodeCursor(page.Items[limit-1].ID())
	}
	return page, nil
}

func planFilter(q ListPlansQuery) (repository.PlanFilter, error) {
	var f repository.PlanFilter
	if q.SiteID != "" {
		site, err := slotplan.NewSiteID(q.SiteID)
		if err != nil {
			return f, err
		}
		f.Site = site
	}
	if q.State != "" {
		st := slotplan.State(q.State)
		if !st.Valid() {
			return f, ErrInvalidState
		}
		f.State = st
	}
	return f, nil
}

func listLimit(limit int) (int, error) {
	if limit == 0 {
		return DefaultListLimit, nil
	}
	if limit < 1 || limit > MaxListLimit {
		return 0, ErrInvalidLimit
	}
	return limit, nil
}

// EncodeCursor makes the opaque cursor for "after this plan".
func EncodeCursor(id slotplan.PlanID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(id))
}

// DecodeCursor reverses EncodeCursor; "" is the first page.
func DecodeCursor(cursor string) (slotplan.PlanID, error) {
	if cursor == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", ErrInvalidCursor
	}
	id, err := slotplan.NewPlanID(string(raw))
	if err != nil {
		return "", ErrInvalidCursor
	}
	return id, nil
}

// ForwardSlotMap is the current approved assignment map of a site. PlanID
// and ApprovedAt are zero while the site has no Approved plan.
type ForwardSlotMap struct {
	Site        slotplan.SiteID
	PlanID      slotplan.PlanID
	ApprovedAt  time.Time
	Assignments []slotplan.Assignment
}

// ListForwardSlots returns the assignment map of the site's Approved plan.
type ListForwardSlots struct {
	Plans       ports.SlotPlanRepository
	DefaultSite string
}

// Handle runs the use case; a site without an Approved plan yields an empty
// map, not an error.
func (uc *ListForwardSlots) Handle(ctx context.Context, rawSite string) (ForwardSlotMap, error) {
	site, err := resolveSite(rawSite, uc.DefaultSite)
	if err != nil {
		return ForwardSlotMap{}, err
	}
	cur, err := uc.Plans.Current(ctx, site)
	if isNotFound(err) {
		return ForwardSlotMap{Site: site, Assignments: []slotplan.Assignment{}}, nil
	}
	if err != nil {
		return ForwardSlotMap{}, err
	}
	snap := cur.Snapshot()
	return ForwardSlotMap{Site: site, PlanID: snap.ID, ApprovedAt: snap.ApprovedAt, Assignments: snap.Assignments}, nil
}

// SkuVelocityQuery is the input of ListSkuVelocity: Limit 0 = 100,
// WindowDays 0 = 28.
type SkuVelocityQuery struct {
	SiteID     string
	Limit      int
	WindowDays int
}

// SkuVelocityResult is the velocity of a site over a window.
type SkuVelocityResult struct {
	Site  slotplan.SiteID
	From  time.Time
	To    time.Time
	Items []planning.SkuVelocity
}

// ListSkuVelocity reads the demand copy: per SKU the ACTIVE lines and units
// over the last WindowDays days, the demand the planner works from.
type ListSkuVelocity struct {
	Demand      ports.DemandLedger
	Clock       ports.Clock
	DefaultSite string
}

// Handle runs the use case.
func (uc *ListSkuVelocity) Handle(ctx context.Context, q SkuVelocityQuery) (SkuVelocityResult, error) {
	site, err := resolveSite(q.SiteID, uc.DefaultSite)
	if err != nil {
		return SkuVelocityResult{}, err
	}
	limit, err := listLimit(q.Limit)
	if err != nil {
		return SkuVelocityResult{}, err
	}
	days := q.WindowDays
	if days != 0 && (days < 1 || days > MaxLookbackDays) {
		return SkuVelocityResult{}, ErrInvalidLimit
	}
	if days == 0 {
		days = DefaultLookbackDays
	}
	to := uc.Clock.Now().UTC()
	from := to.Add(-time.Duration(days) * 24 * time.Hour)
	items, err := uc.Demand.Velocity(ctx, site, from, to)
	if err != nil {
		return SkuVelocityResult{}, fmt.Errorf("read demand: %w", err)
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return SkuVelocityResult{Site: site, From: from, To: to, Items: items}, nil
}
