package mcp

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// Deps is everything the MCP tools need, injected by the composition root.
// It carries the SAME read use cases the REST adapter uses (GET
// /slot-plans/{planId}, /slot-plans, /forward-slots, /sku-velocity); the
// adapter never constructs an outbound adapter itself, and it holds no write
// use case at all (docs/adr/0005).
type Deps struct {
	GetPlan          *usecases.GetPlan
	ListPlans        *usecases.ListPlans
	ListForwardSlots *usecases.ListForwardSlots
	ListSkuVelocity  *usecases.ListSkuVelocity
}

// --- inputs -------------------------------------------------------------------

type planIDInput struct {
	PlanID string `json:"plan_id" jsonschema:"the slot plan id returned when it was generated, e.g. plan-1b4e28ba-2fa1-4d3c-8f6e-0a1b2c3d4e5f"`
}

type listPlansInput struct {
	Limit  int    `json:"limit,omitempty" jsonschema:"page size, 1..500; omitted or 0 means 100"`
	Cursor string `json:"cursor,omitempty" jsonschema:"the opaque next_cursor returned by the previous page; omitted means the first page"`
	SiteID string `json:"site_id,omitempty" jsonschema:"keep only plans of this site; omitted means every site"`
	State  string `json:"state,omitempty" jsonschema:"keep only plans in this state: Draft, Approved, Rejected or Superseded"`
}

type forwardSlotsInput struct {
	SiteID string `json:"site_id,omitempty" jsonschema:"the site to read the forward slot map of; omitted means the service's default site"`
}

type skuVelocityInput struct {
	SiteID     string `json:"site_id,omitempty" jsonschema:"the site to read demand of; omitted means the service's default site"`
	Limit      int    `json:"limit,omitempty" jsonschema:"keep only the N fastest SKUs, 1..500; omitted or 0 means 100"`
	WindowDays int    `json:"window_days,omitempty" jsonschema:"demand window in days ending now, 1..365; omitted or 0 means 28"`
}

// --- outputs (the REST bodies with snake_case names) --------------------------

type assignmentView struct {
	SKU      string `json:"sku"`
	Slot     string `json:"slot"`
	AbcClass string `json:"abc_class"`
	Picks    int64  `json:"picks"`
	Units    int64  `json:"units"`
}

type moveView struct {
	SKU      string `json:"sku"`
	FromSlot string `json:"from_slot,omitempty"`
	ToSlot   string `json:"to_slot,omitempty"`
	Kind     string `json:"kind"`
}

type unassignedView struct {
	SKU    string `json:"sku"`
	Reason string `json:"reason"`
}

type slotPlanView struct {
	PlanID           string           `json:"plan_id"`
	SiteID           string           `json:"site_id"`
	State            string           `json:"state"`
	Policy           string           `json:"policy"`
	WindowFrom       string           `json:"window_from"`
	WindowTo         string           `json:"window_to"`
	GeneratedAt      string           `json:"generated_at"`
	ApprovedAt       string           `json:"approved_at,omitempty"`
	RejectedAt       string           `json:"rejected_at,omitempty"`
	SupersededAt     string           `json:"superseded_at,omitempty"`
	RejectReason     string           `json:"reject_reason,omitempty"`
	SupersedesPlanID string           `json:"supersedes_plan_id,omitempty"`
	Version          int64            `json:"version"`
	Assignments      []assignmentView `json:"assignments"`
	Moves            []moveView       `json:"moves"`
	Unassigned       []unassignedView `json:"unassigned"`
}

type slotPlanSummaryView struct {
	PlanID          string `json:"plan_id"`
	SiteID          string `json:"site_id"`
	State           string `json:"state"`
	Policy          string `json:"policy"`
	WindowFrom      string `json:"window_from"`
	WindowTo        string `json:"window_to"`
	GeneratedAt     string `json:"generated_at"`
	ApprovedAt      string `json:"approved_at,omitempty"`
	AssignmentCount int    `json:"assignment_count"`
	MoveCount       int    `json:"move_count"`
	UnassignedCount int    `json:"unassigned_count"`
	Version         int64  `json:"version"`
}

type slotPlanPageView struct {
	Items      []slotPlanSummaryView `json:"items"`
	NextCursor string                `json:"next_cursor,omitempty"`
}

type forwardSlotView struct {
	SKU      string `json:"sku"`
	Slot     string `json:"slot"`
	AbcClass string `json:"abc_class"`
}

type forwardSlotsView struct {
	SiteID      string            `json:"site_id"`
	PlanID      string            `json:"plan_id,omitempty"`
	ApprovedAt  string            `json:"approved_at,omitempty"`
	Assignments []forwardSlotView `json:"assignments"`
}

type skuVelocityItemView struct {
	SKU   string `json:"sku"`
	Picks int64  `json:"picks"`
	Units int64  `json:"units"`
}

type skuVelocityView struct {
	SiteID     string                `json:"site_id"`
	WindowFrom string                `json:"window_from"`
	WindowTo   string                `json:"window_to"`
	Items      []skuVelocityItemView `json:"items"`
}

// --- mapping ------------------------------------------------------------------

// instant formats an instant as RFC 3339 UTC; the zero time is "" (omitted).
func instant(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func toSlotPlan(p *slotplan.SlotPlan) slotPlanView {
	s := p.Snapshot()
	out := slotPlanView{
		PlanID: string(s.ID), SiteID: string(s.Site), State: string(s.State), Policy: string(s.Policy),
		WindowFrom: instant(s.Window.From), WindowTo: instant(s.Window.To), GeneratedAt: instant(s.GeneratedAt),
		ApprovedAt: instant(s.ApprovedAt), RejectedAt: instant(s.RejectedAt), SupersededAt: instant(s.SupersededAt),
		RejectReason: s.RejectReason, SupersedesPlanID: string(s.SupersedesPlanID), Version: s.Version,
		Assignments: make([]assignmentView, len(s.Assignments)),
		Moves:       make([]moveView, len(s.Moves)),
		Unassigned:  make([]unassignedView, len(s.Unassigned)),
	}
	for i, a := range s.Assignments {
		out.Assignments[i] = assignmentView{SKU: string(a.SKU), Slot: string(a.Slot), AbcClass: string(a.Class), Picks: a.Picks, Units: a.Units}
	}
	for i, m := range s.Moves {
		out.Moves[i] = moveView{SKU: string(m.SKU), FromSlot: string(m.From), ToSlot: string(m.To), Kind: string(m.Kind)}
	}
	for i, u := range s.Unassigned {
		out.Unassigned[i] = unassignedView{SKU: string(u.SKU), Reason: string(u.Reason)}
	}
	return out
}

func toSlotPlanPage(page usecases.PlanPage) slotPlanPageView {
	out := slotPlanPageView{Items: make([]slotPlanSummaryView, len(page.Items)), NextCursor: page.NextCursor}
	for i, p := range page.Items {
		s := p.Snapshot()
		out.Items[i] = slotPlanSummaryView{
			PlanID: string(s.ID), SiteID: string(s.Site), State: string(s.State), Policy: string(s.Policy),
			WindowFrom: instant(s.Window.From), WindowTo: instant(s.Window.To), GeneratedAt: instant(s.GeneratedAt),
			ApprovedAt: instant(s.ApprovedAt), AssignmentCount: len(s.Assignments), MoveCount: len(s.Moves),
			UnassignedCount: len(s.Unassigned), Version: s.Version,
		}
	}
	return out
}

func toForwardSlots(m usecases.ForwardSlotMap) forwardSlotsView {
	out := forwardSlotsView{
		SiteID: string(m.Site), PlanID: string(m.PlanID), ApprovedAt: instant(m.ApprovedAt),
		Assignments: make([]forwardSlotView, len(m.Assignments)),
	}
	for i, a := range m.Assignments {
		out.Assignments[i] = forwardSlotView{SKU: string(a.SKU), Slot: string(a.Slot), AbcClass: string(a.Class)}
	}
	return out
}

func toSkuVelocity(r usecases.SkuVelocityResult) skuVelocityView {
	out := skuVelocityView{
		SiteID: string(r.Site), WindowFrom: instant(r.From), WindowTo: instant(r.To),
		Items: make([]skuVelocityItemView, len(r.Items)),
	}
	for i, v := range r.Items {
		out.Items[i] = velocityItem(v)
	}
	return out
}

func velocityItem(v planning.SkuVelocity) skuVelocityItemView {
	return skuVelocityItemView{SKU: string(v.SKU), Picks: v.Picks, Units: v.Units}
}

// --- handlers -----------------------------------------------------------------

func (d Deps) getSlotPlan(ctx context.Context, in planIDInput) (slotPlanView, error) {
	p, err := d.GetPlan.Handle(ctx, in.PlanID)
	if err != nil {
		return slotPlanView{}, mapError(err)
	}
	return toSlotPlan(p), nil
}

func (d Deps) listSlotPlans(ctx context.Context, in listPlansInput) (slotPlanPageView, error) {
	page, err := d.ListPlans.Handle(ctx, usecases.ListPlansQuery{Limit: in.Limit, Cursor: in.Cursor, SiteID: in.SiteID, State: in.State})
	if err != nil {
		return slotPlanPageView{}, mapError(err)
	}
	return toSlotPlanPage(page), nil
}

func (d Deps) getForwardSlots(ctx context.Context, in forwardSlotsInput) (forwardSlotsView, error) {
	m, err := d.ListForwardSlots.Handle(ctx, in.SiteID)
	if err != nil {
		return forwardSlotsView{}, mapError(err)
	}
	return toForwardSlots(m), nil
}

func (d Deps) getSkuVelocity(ctx context.Context, in skuVelocityInput) (skuVelocityView, error) {
	r, err := d.ListSkuVelocity.Handle(ctx, usecases.SkuVelocityQuery{SiteID: in.SiteID, Limit: in.Limit, WindowDays: in.WindowDays})
	if err != nil {
		return skuVelocityView{}, mapError(err)
	}
	return toSkuVelocity(r), nil
}

// --- registry -----------------------------------------------------------------

// registerTools adds the four read-only tools. Every tool is annotated
// read-only, idempotent and closed-world; TestToolSurface pins the set and
// fails the build on any write-verb name (docs/adr/0005).
func (d Deps) registerTools(server *mcp.Server) {
	closedWorld := false
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closedWorld}

	addTool(server, &mcp.Tool{
		Name: "get_slot_plan",
		Description: "Read one forward pick slot plan by id: site_id, state (Draft, Approved, Rejected or Superseded), policy, " +
			"the demand window (window_from, window_to), generated_at and the decision instants (approved_at, rejected_at, " +
			"superseded_at, each omitted until it happens), reject_reason and supersedes_plan_id (omitted when none), " +
			"the assignments (sku, slot, abc_class, picks, units), the moves against the previous plan (sku, from_slot, " +
			"to_slot, kind) and the unassigned SKUs with their reason, plus version. Read-only; fails with plan-not-found " +
			"for an unknown id and invalid-plan-id for a malformed one.",
		Annotations: readOnly,
	}, d.getSlotPlan)

	addTool(server, &mcp.Tool{
		Name: "list_slot_plans",
		Description: "List slot plans newest first, a page at a time (default 100, max 500), as summaries (ids, site, state, " +
			"policy, window and assignment/move/unassigned counts), optionally only those of one site and/or in one state " +
			"(Draft, Approved, Rejected or Superseded). Pass the returned next_cursor to get the next page; it is absent on " +
			"the last page. Read-only; a bad limit fails with invalid-limit, a bad cursor with invalid-cursor, an unknown " +
			"state with invalid-state and a malformed site with invalid-site-id.",
		Annotations: readOnly,
	}, d.listSlotPlans)

	addTool(server, &mcp.Tool{
		Name: "get_forward_slots",
		Description: "Read the CURRENT forward slot map of a site: the assignments (sku, slot, abc_class) of its Approved plan, " +
			"with that plan_id and approved_at. A site without an Approved plan yields an empty assignment list and no " +
			"plan_id, not an error. site_id defaults to the service's default site. Read-only; fails with invalid-site-id " +
			"for a malformed site or when no site is given and no default is configured.",
		Annotations: readOnly,
	}, d.getForwardSlots)

	addTool(server, &mcp.Tool{
		Name: "get_sku_velocity",
		Description: "Read the demand the planner works from: per SKU the ACTIVE order lines (picks) and units over the last " +
			"window_days days (default 28, max 365) of a site, fastest first, cut to limit SKUs (default 100, max 500). " +
			"site_id defaults to the service's default site. Returns window_from and window_to (UTC). Read-only; a bad " +
			"limit or window fails with invalid-limit and a malformed or missing site with invalid-site-id.",
		Annotations: readOnly,
	}, d.getSkuVelocity)
}

// addTool registers one tool. A handler error is returned to the SDK as the
// handler's error, which the SDK turns into an isError tool result (never a
// transport/protocol failure), carrying the error text.
func addTool[In, Out any](
	server *mcp.Server,
	tool *mcp.Tool,
	handle func(context.Context, In) (Out, error),
) {
	mcp.AddTool(server, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		out, err := handle(ctx, in)
		if err != nil {
			var zero Out
			return nil, zero, err
		}
		return nil, out, nil
	})
}
