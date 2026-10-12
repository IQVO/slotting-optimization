package http

import (
	"time"

	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// Requests. Pointers tell "absent" from an explicit zero value.

type generateSlotPlanRequest struct {
	SiteID       *string `json:"siteId"`
	LookbackDays *int    `json:"lookbackDays"`
}

type rejectSlotPlanRequest struct {
	Reason string `json:"reason"`
}

// Responses (apis/openapi.yaml components). Lists are never null.

type assignmentBody struct {
	SKU      string `json:"sku"`
	Slot     string `json:"slot"`
	AbcClass string `json:"abcClass"`
	Picks    int64  `json:"picks"`
	Units    int64  `json:"units"`
}

type moveBody struct {
	SKU      string `json:"sku"`
	FromSlot string `json:"fromSlot,omitempty"`
	ToSlot   string `json:"toSlot,omitempty"`
	Kind     string `json:"kind"`
}

type unassignedBody struct {
	SKU    string `json:"sku"`
	Reason string `json:"reason"`
}

type slotPlanBody struct {
	PlanID           string           `json:"planId"`
	SiteID           string           `json:"siteId"`
	State            string           `json:"state"`
	Policy           string           `json:"policy"`
	WindowFrom       string           `json:"windowFrom"`
	WindowTo         string           `json:"windowTo"`
	GeneratedAt      string           `json:"generatedAt"`
	ApprovedAt       string           `json:"approvedAt,omitempty"`
	RejectedAt       string           `json:"rejectedAt,omitempty"`
	SupersededAt     string           `json:"supersededAt,omitempty"`
	RejectReason     string           `json:"rejectReason,omitempty"`
	SupersedesPlanID string           `json:"supersedesPlanId,omitempty"`
	Version          int64            `json:"version"`
	Assignments      []assignmentBody `json:"assignments"`
	Moves            []moveBody       `json:"moves"`
	Unassigned       []unassignedBody `json:"unassigned"`
}

type slotPlanSummaryBody struct {
	PlanID          string `json:"planId"`
	SiteID          string `json:"siteId"`
	State           string `json:"state"`
	Policy          string `json:"policy"`
	WindowFrom      string `json:"windowFrom"`
	WindowTo        string `json:"windowTo"`
	GeneratedAt     string `json:"generatedAt"`
	ApprovedAt      string `json:"approvedAt,omitempty"`
	AssignmentCount int    `json:"assignmentCount"`
	MoveCount       int    `json:"moveCount"`
	UnassignedCount int    `json:"unassignedCount"`
	Version         int64  `json:"version"`
}

type slotPlanPageBody struct {
	Items      []slotPlanSummaryBody `json:"items"`
	NextCursor string                `json:"nextCursor,omitempty"`
}

type forwardSlotBody struct {
	SKU      string `json:"sku"`
	Slot     string `json:"slot"`
	AbcClass string `json:"abcClass"`
}

type forwardSlotsBody struct {
	SiteID      string            `json:"siteId"`
	PlanID      string            `json:"planId,omitempty"`
	ApprovedAt  string            `json:"approvedAt,omitempty"`
	Assignments []forwardSlotBody `json:"assignments"`
}

type skuVelocityBody struct {
	SKU   string `json:"sku"`
	Picks int64  `json:"picks"`
	Units int64  `json:"units"`
}

type skuVelocityListBody struct {
	SiteID     string            `json:"siteId"`
	WindowFrom string            `json:"windowFrom"`
	WindowTo   string            `json:"windowTo"`
	Items      []skuVelocityBody `json:"items"`
}

// ts formats an instant as RFC 3339 UTC; the zero time is "" (omitted).
func ts(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func toSlotPlan(p *slotplan.SlotPlan) slotPlanBody {
	s := p.Snapshot()
	out := slotPlanBody{
		PlanID: string(s.ID), SiteID: string(s.Site), State: string(s.State), Policy: string(s.Policy),
		WindowFrom: ts(s.Window.From), WindowTo: ts(s.Window.To), GeneratedAt: ts(s.GeneratedAt),
		ApprovedAt: ts(s.ApprovedAt), RejectedAt: ts(s.RejectedAt), SupersededAt: ts(s.SupersededAt),
		RejectReason: s.RejectReason, SupersedesPlanID: string(s.SupersedesPlanID), Version: s.Version,
		Assignments: make([]assignmentBody, len(s.Assignments)),
		Moves:       make([]moveBody, len(s.Moves)),
		Unassigned:  make([]unassignedBody, len(s.Unassigned)),
	}
	for i, a := range s.Assignments {
		out.Assignments[i] = assignmentBody{SKU: string(a.SKU), Slot: string(a.Slot), AbcClass: string(a.Class), Picks: a.Picks, Units: a.Units}
	}
	for i, m := range s.Moves {
		out.Moves[i] = moveBody{SKU: string(m.SKU), FromSlot: string(m.From), ToSlot: string(m.To), Kind: string(m.Kind)}
	}
	for i, u := range s.Unassigned {
		out.Unassigned[i] = unassignedBody{SKU: string(u.SKU), Reason: string(u.Reason)}
	}
	return out
}

func toSlotPlanPage(page usecases.PlanPage) slotPlanPageBody {
	out := slotPlanPageBody{Items: make([]slotPlanSummaryBody, len(page.Items)), NextCursor: page.NextCursor}
	for i, p := range page.Items {
		s := p.Snapshot()
		out.Items[i] = slotPlanSummaryBody{
			PlanID: string(s.ID), SiteID: string(s.Site), State: string(s.State), Policy: string(s.Policy),
			WindowFrom: ts(s.Window.From), WindowTo: ts(s.Window.To), GeneratedAt: ts(s.GeneratedAt), ApprovedAt: ts(s.ApprovedAt),
			AssignmentCount: len(s.Assignments), MoveCount: len(s.Moves), UnassignedCount: len(s.Unassigned), Version: s.Version,
		}
	}
	return out
}

func toForwardSlots(m usecases.ForwardSlotMap) forwardSlotsBody {
	out := forwardSlotsBody{
		SiteID: string(m.Site), PlanID: string(m.PlanID), ApprovedAt: ts(m.ApprovedAt),
		Assignments: make([]forwardSlotBody, len(m.Assignments)),
	}
	for i, a := range m.Assignments {
		out.Assignments[i] = forwardSlotBody{SKU: string(a.SKU), Slot: string(a.Slot), AbcClass: string(a.Class)}
	}
	return out
}

func toSkuVelocity(r usecases.SkuVelocityResult) skuVelocityListBody {
	out := skuVelocityListBody{SiteID: string(r.Site), WindowFrom: ts(r.From), WindowTo: ts(r.To), Items: make([]skuVelocityBody, len(r.Items))}
	for i, v := range r.Items {
		out.Items[i] = velocityBody(v)
	}
	return out
}

func velocityBody(v planning.SkuVelocity) skuVelocityBody {
	return skuVelocityBody{SKU: string(v.SKU), Picks: v.Picks, Units: v.Units}
}
