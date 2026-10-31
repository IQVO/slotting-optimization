package a2a

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

// problemTypeBase namespaces this context's problem types. It is the SAME
// base URI as the REST adapter's (internal/adapters/inbound/http/errors.go), so
// a delegating agent sees one error vocabulary over REST, MCP and A2A.
const problemTypeBase = "https://errors.slotting-optimization.warehouse-systems.dev/"

// problem is the RFC 7807-shaped rejection an agent client receives instead
// of a result: domain rejections and request-shape failures are business
// answers, not transport errors, so they travel in the message envelope.
type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Status int    `json:"status"`
}

// summary renders the one-line form used for the reply's text part.
func (p *problem) summary() string {
	return fmt.Sprintf("%s (%d): %s", p.Title, p.Status, p.Detail)
}

// Adapter-owned problems: request-shape failures the REST adapter has no
// counterpart for (a message is not an HTTP body).
func problemMalformedRequest(detail string) *problem {
	return &problem{Type: problemTypeBase + "malformed-skill-request", Title: "Malformed skill request", Detail: detail, Status: http.StatusBadRequest}
}

func problemUnknownSkill(skill string) *problem {
	return &problem{Type: problemTypeBase + "unknown-skill", Title: "Unknown skill", Detail: fmt.Sprintf("skill %q is not offered by this agent; the Agent Card lists the supported skills", skill), Status: http.StatusBadRequest}
}

func problemInvalidArgument(err error) *problem {
	return &problem{Type: problemTypeBase + "invalid-argument", Title: "Invalid skill argument", Detail: err.Error(), Status: http.StatusBadRequest}
}

// problemNoMatch is the 404 of slot_plan_review without a planId when no plan
// matches the filters (REST has no such route; the classification is the one
// of a missing plan).
func problemNoMatch() *problem {
	return &problem{Type: problemTypeBase + "plan-not-found", Title: "Slot plan not found", Detail: "no slot plan matches the given siteId and state", Status: http.StatusNotFound}
}

// catalogue maps every typed error a READ can raise to the (status, slug,
// title) the REST adapter gives it, in the same match order
// (internal/adapters/inbound/http/errors.go problemCatalogue). The write-only
// entries of that catalogue (reject-reason-too-long, plan-not-draft,
// approved-plan-conflict, concurrent-modification) are unreachable here. A
// shared domain error text that named a deprecated alias would be mapped to
// adapter-owned text; none of these does (the canonical key is siteId).
var catalogue = []struct {
	err    error
	status int
	slug   string
	title  string
}{
	{slotplan.ErrInvalidPlanID, http.StatusBadRequest, "invalid-plan-id", "Plan id is invalid"},
	{slotplan.ErrInvalidSiteID, http.StatusBadRequest, "invalid-site-id", "Site id is invalid"},
	{usecases.ErrNoSite, http.StatusBadRequest, "invalid-site-id", "Site id is invalid"},
	{usecases.ErrInvalidLimit, http.StatusBadRequest, "invalid-limit", "Limit is invalid"},
	{usecases.ErrInvalidCursor, http.StatusBadRequest, "invalid-cursor", "Cursor is invalid"},
	{usecases.ErrInvalidState, http.StatusBadRequest, "invalid-state", "State is invalid"},
	{repository.ErrPlanNotFound, http.StatusNotFound, "plan-not-found", "Slot plan not found"},
}

// problemFromUseCase maps a use case error to a problem answer, using the
// SAME classification as the REST adapter so a delegating agent sees the
// outcome a REST caller would. Domain rejections are EXPECTED business
// outcomes and are answered as problems; anything unrecognised is an internal
// failure, still answered as a problem (status 500) with a generic detail so
// infrastructure text (DSNs, SQL) never reaches the calling agent.
func problemFromUseCase(err error) *problem {
	for _, entry := range catalogue {
		if errors.Is(err, entry.err) {
			return &problem{Type: problemTypeBase + entry.slug, Title: entry.title, Detail: err.Error(), Status: entry.status}
		}
	}
	slog.Error("a2a skill failed", "error", err)
	return &problem{Type: problemTypeBase + "internal-error", Title: "Internal server error", Detail: "the skill failed; see server logs (trace id in the response metadata)", Status: http.StatusInternalServerError}
}

// envelope is the structured payload of every skill answer: exactly one of
// Data (a skill DTO) or Problem (why there is no result), plus the skill id
// the answer belongs to ("" when the request named none).
type envelope struct {
	Skill   string    `json:"skill"`
	Data    *skillDTO `json:"data,omitempty"`
	Problem *problem  `json:"problem,omitempty"`
}

// skillDTO is the union of the skill result DTOs: each skill fills its own
// field, keeping the envelope flat and versioned as one contract.
type skillDTO struct {
	ForwardSlotMap *forwardSlotMap `json:"forwardSlotMap,omitempty"`
	SlotPlan       *slotPlan       `json:"slotPlan,omitempty"`
}

// result is a successful skill run: the DTO and its one-line summary.
type result struct {
	dto     skillDTO
	summary string
}

// --- forward_slot_map ---------------------------------------------------------

type forwardSlot struct {
	SKU           string `json:"sku"`
	Slot          string `json:"slot"`
	AbcClass      string `json:"abcClass"`
	PickUnit      string `json:"pickUnit"`
	CasesCapacity int64  `json:"casesCapacity,omitempty"`
}

type forwardSlotMap struct {
	SiteID      string        `json:"siteId"`
	PlanID      string        `json:"planId,omitempty"`
	ApprovedAt  string        `json:"approvedAt,omitempty"`
	Assignments []forwardSlot `json:"assignments"`
}

func toForwardSlotMap(m usecases.ForwardSlotMap) (forwardSlotMap, string) {
	out := forwardSlotMap{
		SiteID: string(m.Site), PlanID: string(m.PlanID), ApprovedAt: instant(m.ApprovedAt),
		Assignments: make([]forwardSlot, len(m.Assignments)),
	}
	for i, a := range m.Assignments {
		out.Assignments[i] = forwardSlot{SKU: string(a.SKU), Slot: string(a.Slot), AbcClass: string(a.Class),
			PickUnit: string(a.PickUnit), CasesCapacity: a.CasesCapacity}
	}
	if out.PlanID == "" {
		return out, fmt.Sprintf("%s: no Approved slot plan, so no forward slot map yet", out.SiteID)
	}
	return out, fmt.Sprintf("%s: %d SKUs slotted by Approved plan %s", out.SiteID, len(out.Assignments), out.PlanID)
}

// --- slot_plan_review ---------------------------------------------------------

type assignment struct {
	SKU           string `json:"sku"`
	Slot          string `json:"slot"`
	AbcClass      string `json:"abcClass"`
	Picks         int64  `json:"picks"`
	Units         int64  `json:"units"`
	PickUnit      string `json:"pickUnit"`
	CasesCapacity int64  `json:"casesCapacity,omitempty"`
}

type move struct {
	SKU      string `json:"sku"`
	FromSlot string `json:"fromSlot,omitempty"`
	ToSlot   string `json:"toSlot,omitempty"`
	Kind     string `json:"kind"`
}

type unassigned struct {
	SKU    string `json:"sku"`
	Reason string `json:"reason"`
}

type moveExecution struct {
	MoveSeq          int    `json:"moveSeq"`
	SKU              string `json:"sku"`
	Kind             string `json:"kind"`
	Executed         bool   `json:"executed"`
	ExecutedQuantity int64  `json:"executedQuantity"`
	LastExecutedAt   string `json:"lastExecutedAt,omitempty"`
}

type execution struct {
	MovesTotal     int             `json:"movesTotal"`
	MovesExecuted  int             `json:"movesExecuted"`
	LastExecutedAt string          `json:"lastExecutedAt,omitempty"`
	Moves          []moveExecution `json:"moves"`
}

type slotPlan struct {
	PlanID           string       `json:"planId"`
	SiteID           string       `json:"siteId"`
	State            string       `json:"state"`
	Policy           string       `json:"policy"`
	WindowFrom       string       `json:"windowFrom"`
	WindowTo         string       `json:"windowTo"`
	GeneratedAt      string       `json:"generatedAt"`
	ApprovedAt       string       `json:"approvedAt,omitempty"`
	RejectedAt       string       `json:"rejectedAt,omitempty"`
	SupersededAt     string       `json:"supersededAt,omitempty"`
	RejectReason     string       `json:"rejectReason,omitempty"`
	SupersedesPlanID string       `json:"supersedesPlanId,omitempty"`
	Version          int64        `json:"version"`
	Assignments      []assignment `json:"assignments"`
	Moves            []move       `json:"moves"`
	Unassigned       []unassigned `json:"unassigned"`
	Execution        *execution   `json:"execution,omitempty"`
}

// instant formats an instant as RFC 3339 UTC; the zero time is "" (omitted).
func instant(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func toSlotPlan(v usecases.PlanView) (slotPlan, string) {
	s := v.Plan.Snapshot()
	out := slotPlan{
		PlanID: string(s.ID), SiteID: string(s.Site), State: string(s.State), Policy: string(s.Policy),
		WindowFrom: instant(s.Window.From), WindowTo: instant(s.Window.To), GeneratedAt: instant(s.GeneratedAt),
		ApprovedAt: instant(s.ApprovedAt), RejectedAt: instant(s.RejectedAt), SupersededAt: instant(s.SupersededAt),
		RejectReason: s.RejectReason, SupersedesPlanID: string(s.SupersedesPlanID), Version: s.Version,
		Assignments: make([]assignment, len(s.Assignments)),
		Moves:       make([]move, len(s.Moves)),
		Unassigned:  make([]unassigned, len(s.Unassigned)),
		Execution:   toExecution(v),
	}
	for i, a := range s.Assignments {
		out.Assignments[i] = assignment{SKU: string(a.SKU), Slot: string(a.Slot), AbcClass: string(a.Class), Picks: a.Picks, Units: a.Units,
			PickUnit: string(a.PickUnit), CasesCapacity: a.CasesCapacity}
	}
	for i, m := range s.Moves {
		out.Moves[i] = move{SKU: string(m.SKU), FromSlot: string(m.From), ToSlot: string(m.To), Kind: string(m.Kind)}
	}
	for i, u := range s.Unassigned {
		out.Unassigned[i] = unassigned{SKU: string(u.SKU), Reason: string(u.Reason)}
	}
	summary := fmt.Sprintf("plan %s (%s) is %s: %d assignments, %d moves, %d unassigned",
		out.PlanID, out.SiteID, out.State, len(out.Assignments), len(out.Moves), len(out.Unassigned))
	if out.Execution != nil {
		summary += fmt.Sprintf("; %d of %d moves executed", out.Execution.MovesExecuted, out.Execution.MovesTotal)
	}
	return out, summary
}

// toExecution maps the plan's execution progress (ADR 0010); nil when the use
// case has no execution ledger.
func toExecution(v usecases.PlanView) *execution {
	if v.Execution == nil {
		return nil
	}
	e := *v.Execution
	out := &execution{
		MovesTotal: e.MovesTotal, MovesExecuted: e.MovesExecuted, LastExecutedAt: instant(e.LastExecutedAt),
		Moves: make([]moveExecution, len(e.Moves)),
	}
	for i, m := range e.Moves {
		out.Moves[i] = moveExecution{
			MoveSeq: m.Seq, SKU: string(m.SKU), Kind: string(m.Kind), Executed: m.Executed,
			ExecutedQuantity: m.ExecutedQuantity, LastExecutedAt: instant(m.LastExecutedAt),
		}
	}
	return out
}
