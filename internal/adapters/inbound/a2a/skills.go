package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/claudioed/slotting-optimization/internal/application/usecases"
)

// tracerName is the OTel instrumentation scope for A2A skill spans, mirroring
// the MCP adapter's mcp.tool spans.
const tracerName = "github.com/claudioed/slotting-optimization/internal/adapters/inbound/a2a"

// Skill ids advertised on the Agent Card (see Card).
const (
	skillForwardSlotMap = "forward_slot_map"
	skillSlotPlanReview = "slot_plan_review"
)

// skillRequest is the wire contract of every skill call: the first message
// part carries (as a Data part, or a Text part holding the same JSON) the
// skill id and its arguments. Strict on purpose -- an agent client is
// machine-to-machine, so unknown fields are rejected, never ignored: a
// misspelled name like "stat" must not silently drop the state filter and
// answer for another plan. (Go's encoding/json matches field names
// case-insensitively, so "SITEID" still reaches siteId; only genuinely
// unknown names are refused.)
type skillRequest struct {
	Skill     string          `json:"skill"`
	Arguments json.RawMessage `json:"arguments"`
}

// forwardSlotMapArgs and slotPlanReviewArgs are the per-skill argument shapes.
type forwardSlotMapArgs struct {
	SiteID string `json:"siteId"`
}

type slotPlanReviewArgs struct {
	PlanID string `json:"planId"`
	SiteID string `json:"siteId"`
	State  string `json:"state"`
}

// answer maps one incoming message to a skill and returns the agent's reply
// Message. It is the whole executor body: this is a message-only agent, so a
// skill run is exactly one request mapped to one answer (ADR 0013).
func answer(ctx context.Context, deps Deps, ec *a2asrv.ExecutorContext) a2a.Event {
	req, prob := parseSkillRequest(ec)
	if prob != nil {
		return reply(ec, envelope{Problem: prob}, prob.summary())
	}

	ctx, span := otel.Tracer(tracerName).Start(ctx, "a2a.skill "+req.Skill)
	defer span.End()
	span.SetAttributes(attribute.String("a2a.skill", req.Skill))

	res, prob := run(ctx, deps, req)
	if prob != nil {
		span.SetStatus(codes.Error, prob.Detail)
		return reply(ec, envelope{Skill: req.Skill, Problem: prob}, prob.summary())
	}
	return reply(ec, envelope{Skill: req.Skill, Data: &res.dto}, res.summary)
}

// run dispatches one validated request to its skill.
func run(ctx context.Context, deps Deps, req skillRequest) (result, *problem) {
	switch req.Skill {
	case skillForwardSlotMap:
		return deps.forwardSlotMap(ctx, req.Arguments)
	case skillSlotPlanReview:
		return deps.slotPlanReview(ctx, req.Arguments)
	default:
		return result{}, problemUnknownSkill(req.Skill)
	}
}

// reply builds the agent Message for one skill run: a Data part carrying the
// structured envelope and a Text part carrying the one-line summary, bound to
// the caller's context (and task, when the client referenced one).
func reply(ec *a2asrv.ExecutorContext, env envelope, summary string) a2a.Event {
	return a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewDataPart(env), a2a.NewTextPart(summary))
}

// parseSkillRequest extracts the skillRequest from the message's first part.
// A Data part (or a Text part holding the same JSON object) is accepted;
// anything else is a request-shape problem, answered -- not raised -- because
// a malformed delegation is a business answer for the calling agent, not a
// transport failure.
func parseSkillRequest(ec *a2asrv.ExecutorContext) (skillRequest, *problem) {
	msg := ec.Message
	if msg == nil || len(msg.Parts) == 0 {
		return skillRequest{}, problemMalformedRequest("the message carries no parts")
	}
	raw, err := partJSON(msg.Parts[0])
	if err != nil {
		return skillRequest{}, problemMalformedRequest(fmt.Sprintf("the first message part is not a skill request: %v", err))
	}
	var req skillRequest
	if err := decodeStrict(raw, &req); err != nil {
		return skillRequest{}, problemMalformedRequest(fmt.Sprintf("the skill request is not valid: %v", err))
	}
	if req.Skill == "" {
		return skillRequest{}, problemMalformedRequest(`the skill request carries no "skill" field`)
	}
	return req, nil
}

// partJSON renders one part's content as the JSON bytes of a skill request.
func partJSON(part *a2a.Part) ([]byte, error) {
	switch content := part.Content.(type) {
	case a2a.Data:
		return json.Marshal(content.Value)
	case a2a.Text:
		return []byte(string(content)), nil
	default:
		return nil, errors.New("expected a data part (or a text part holding the skill JSON)")
	}
}

// decodeStrict decodes one JSON document into dst, rejecting unknown fields
// and trailing data. Absent arguments decode as the empty object so each
// skill's own checks produce the specific problem.
func decodeStrict(raw []byte, dst any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("unexpected data after the JSON document")
	}
	return nil
}

// forwardSlotMap is the current-slots read: the assignment map of the site's
// Approved plan, exactly as GET /forward-slots and get_forward_slots answer.
func (d Deps) forwardSlotMap(ctx context.Context, rawArgs json.RawMessage) (result, *problem) {
	var args forwardSlotMapArgs
	if err := decodeStrict(rawArgs, &args); err != nil {
		return result{}, problemInvalidArgument(fmt.Errorf("arguments: %w", err))
	}
	m, err := d.ListForwardSlots.Handle(ctx, args.SiteID)
	if err != nil {
		return result{}, problemFromUseCase(err)
	}
	dto, summary := toForwardSlotMap(m)
	return result{dto: skillDTO{ForwardSlotMap: &dto}, summary: summary}, nil
}

// slotPlanReview is the plan read: one plan in full (with its execution
// progress), by id or as the newest plan of a site and/or state. The latest
// form composes the two reads the MCP surface offers (list_slot_plans, newest
// first, then get_slot_plan) so a delegating agent need not page.
func (d Deps) slotPlanReview(ctx context.Context, rawArgs json.RawMessage) (result, *problem) {
	var args slotPlanReviewArgs
	if err := decodeStrict(rawArgs, &args); err != nil {
		return result{}, problemInvalidArgument(fmt.Errorf("arguments: %w", err))
	}
	if args.PlanID != "" && (args.SiteID != "" || args.State != "") {
		return result{}, problemInvalidArgument(errors.New("planId cannot be combined with siteId or state: give a planId, or filters for the newest matching plan"))
	}
	planID := args.PlanID
	if planID == "" {
		latest, prob := d.latestPlanID(ctx, args)
		if prob != nil {
			return result{}, prob
		}
		planID = latest
	}
	view, err := d.GetPlan.View(ctx, planID)
	if err != nil {
		return result{}, problemFromUseCase(err)
	}
	dto, summary := toSlotPlan(view)
	return result{dto: skillDTO{SlotPlan: &dto}, summary: summary}, nil
}

// latestPlanID resolves the newest plan matching the optional site and state
// filters; a 404 problem when none matches.
func (d Deps) latestPlanID(ctx context.Context, args slotPlanReviewArgs) (string, *problem) {
	page, err := d.ListPlans.Handle(ctx, usecases.ListPlansQuery{Limit: 1, SiteID: args.SiteID, State: args.State})
	if err != nil {
		return "", problemFromUseCase(err)
	}
	if len(page.Items) == 0 {
		return "", problemNoMatch()
	}
	return string(page.Items[0].ID()), nil
}
