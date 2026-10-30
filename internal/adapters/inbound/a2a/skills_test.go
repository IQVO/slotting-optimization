package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"

	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/ids"
	outboundkafka "github.com/claudioed/slotting-optimization/internal/adapters/outbound/kafka"
	"github.com/claudioed/slotting-optimization/internal/adapters/outbound/memory"
	"github.com/claudioed/slotting-optimization/internal/application/ports"
	"github.com/claudioed/slotting-optimization/internal/application/repository"
	"github.com/claudioed/slotting-optimization/internal/application/usecases"
	"github.com/claudioed/slotting-optimization/internal/domain/planning"
	"github.com/claudioed/slotting-optimization/internal/domain/slotplan"
)

var base = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

// tickClock is a settable clock: plans generated at different instants order
// "newest first" deterministically.
type tickClock struct{ now time.Time }

func (c *tickClock) Now() time.Time { return c.now }

const missingPlan = "plan-00000000-0000-4000-8000-0000000000ff"

// harness builds Deps over in-memory repos exactly as cmd/a2a wires them
// (minus Postgres). The write use cases exist ONLY here, to seed data the way
// the REST adapter would: the A2A surface itself has none.
type harness struct {
	deps     Deps
	clk      *tickClock
	demand   *memory.DemandLedger
	profiles *memory.ProfileDirectory
	slots    *memory.SlotCatalogue
	outbox   *memory.OutboxRepo

	generate *usecases.GeneratePlan
	approve  *usecases.ApprovePlan
	reject   *usecases.RejectPlan
}

func newHarness(t *testing.T, defaultSite string) *harness {
	t.Helper()
	plans, ob := memory.NewSlotPlanRepo(), memory.NewOutboxRepo()
	demand, profiles, slots := memory.NewDemandLedger(), memory.NewProfileDirectory(), memory.NewSlotCatalogue()
	planner, err := planning.NewPlanner(planning.LexicalRanking{})
	if err != nil {
		t.Fatal(err)
	}
	clk := &tickClock{now: base}
	w := usecases.Writer{
		Plans: plans, Outbox: ob, Encoder: outboundkafka.NewEncoder(),
		UoW: memory.NewUnitOfWork(plans, ob, demand, profiles, slots), Clock: clk,
	}
	return &harness{
		deps: Deps{
			GetPlan:          &usecases.GetPlan{Plans: plans, Executions: memory.NewMoveExecutionLedger()},
			ListPlans:        &usecases.ListPlans{Plans: plans},
			ListForwardSlots: &usecases.ListForwardSlots{Plans: plans, DefaultSite: defaultSite},
		},
		clk: clk, demand: demand, profiles: profiles, slots: slots, outbox: ob,
		generate: &usecases.GeneratePlan{
			Writer: w, Demand: demand, Profiles: profiles, Catalogue: slots, IDs: ids.UUID{}, Planner: planner,
			DefaultSite: "SITE-1", ForwardZoneCodes: []string{"FWD"},
		},
		approve: &usecases.ApprovePlan{Writer: w},
		reject:  &usecases.RejectPlan{Writer: w},
	}
}

// eq reports a mismatch of one named value (a flat call, so the test bodies
// that make many comparisons stay simple).
func eq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// seed gives SITE-1 three SKUs with demand (SKU-3 has no dimensions, so it is
// left unassigned), dimensions for SKU-1 and SKU-2 and two forward slots.
func (h *harness) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	due := base.Add(-time.Hour)
	for _, l := range []repository.DemandLine{
		{SourceOrderID: "o1", LineNo: 1, Site: "SITE-1", SKU: "SKU-1", Units: 10, DueAt: due, Active: true},
		{SourceOrderID: "o1", LineNo: 2, Site: "SITE-1", SKU: "SKU-1", Units: 5, DueAt: due, Active: true},
		{SourceOrderID: "o2", LineNo: 1, Site: "SITE-1", SKU: "SKU-2", Units: 4, DueAt: due, Active: true},
		{SourceOrderID: "o3", LineNo: 1, Site: "SITE-1", SKU: "SKU-3", Units: 1, DueAt: due, Active: true},
	} {
		must(t, h.demand.Apply(ctx, l))
	}
	for _, sku := range []slotplan.SKU{"SKU-1", "SKU-2"} {
		must(t, h.profiles.Save(ctx, repository.ProductProfile{SKU: sku, Unit: &planning.UnitSize{VolumeMM3: 1_000_000, WeightG: 1000}, Version: 1}))
	}
	must(t, h.slots.SaveZone(ctx, repository.Zone{ID: "Z1", SiteCode: "SITE-1", ZoneCode: "FWD", TemperatureClass: planning.Ambient}))
	for _, code := range []slotplan.SlotCode{"A-01", "A-02"} {
		must(t, h.slots.SaveSlot(ctx, repository.Slot{Code: code, ZoneID: "Z1", Role: repository.RoleStorage, MaxWeightKg: 40, MaxVolumeM3: 0.5}))
	}
}

// draft generates a Draft plan of SITE-1 at the harness clock and returns its id.
func (h *harness) draft(t *testing.T) string {
	t.Helper()
	p, err := h.generate.Handle(context.Background(), usecases.GenerateInput{})
	if err != nil {
		t.Fatalf("generate plan: %v", err)
	}
	return string(p.ID())
}

// ---- the wire: JSON-RPC over HTTP, driven by the SDK's own client ----------

// wireEnvelope is a TEST-LOCAL copy of the answer contract. If the adapter's
// envelope drifts, this suite fails instead of silently following it.
type wireEnvelope struct {
	Skill string `json:"skill"`
	Data  *struct {
		ForwardSlotMap *struct {
			SiteID      string `json:"siteId"`
			PlanID      string `json:"planId"`
			ApprovedAt  string `json:"approvedAt"`
			Assignments []struct {
				SKU      string `json:"sku"`
				Slot     string `json:"slot"`
				AbcClass string `json:"abcClass"`
				PickUnit string `json:"pickUnit"`
			} `json:"assignments"`
		} `json:"forwardSlotMap"`
		SlotPlan *struct {
			PlanID           string `json:"planId"`
			SiteID           string `json:"siteId"`
			State            string `json:"state"`
			Policy           string `json:"policy"`
			WindowFrom       string `json:"windowFrom"`
			WindowTo         string `json:"windowTo"`
			GeneratedAt      string `json:"generatedAt"`
			ApprovedAt       string `json:"approvedAt"`
			RejectedAt       string `json:"rejectedAt"`
			RejectReason     string `json:"rejectReason"`
			SupersedesPlanID string `json:"supersedesPlanId"`
			Version          int64  `json:"version"`
			Assignments      []struct {
				SKU      string `json:"sku"`
				Slot     string `json:"slot"`
				AbcClass string `json:"abcClass"`
				Picks    int64  `json:"picks"`
				Units    int64  `json:"units"`
			} `json:"assignments"`
			Moves []struct {
				SKU    string `json:"sku"`
				ToSlot string `json:"toSlot"`
				Kind   string `json:"kind"`
			} `json:"moves"`
			Unassigned []struct {
				SKU    string `json:"sku"`
				Reason string `json:"reason"`
			} `json:"unassigned"`
			Execution *struct {
				MovesTotal    int `json:"movesTotal"`
				MovesExecuted int `json:"movesExecuted"`
			} `json:"execution"`
		} `json:"slotPlan"`
	} `json:"data"`
	Problem *struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Detail string `json:"detail"`
		Status int    `json:"status"`
	} `json:"problem"`
}

type wire struct {
	client *a2aclient.Client
}

func newWire(t *testing.T, deps Deps) *wire {
	t.Helper()
	srv := httptest.NewServer(Handler(NewExecutor(deps)))
	t.Cleanup(srv.Close)
	card := Card(Config{AgentURL: srv.URL + "/", Version: "test"})
	client, err := a2aclient.NewFromCard(context.Background(), card)
	if err != nil {
		t.Fatalf("NewFromCard: %v", err)
	}
	return &wire{client: client}
}

// call sends one message part and returns the decoded envelope, the one-line
// text summary and the raw reply message.
func (w *wire) call(t *testing.T, part *a2a.Part) (wireEnvelope, string, *a2a.Message) {
	t.Helper()
	resp, err := w.client.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, part)})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	msg, ok := resp.(*a2a.Message)
	if !ok || len(msg.Parts) < 2 {
		t.Fatalf("reply = %T, want a message with a data and a text part", resp)
	}
	data, ok := msg.Parts[0].Content.(a2a.Data)
	if !ok {
		t.Fatalf("first reply part is %T, want data", msg.Parts[0].Content)
	}
	raw, _ := json.Marshal(data.Value)
	var env wireEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("reply is not the skill envelope: %v (%s)", err, raw)
	}
	text, _ := msg.Parts[1].Content.(a2a.Text)
	return env, string(text), msg
}

func (w *wire) skill(t *testing.T, skill string, args map[string]any) (wireEnvelope, string) {
	t.Helper()
	env, text, _ := w.call(t, a2a.NewDataPart(map[string]any{"skill": skill, "arguments": args}))
	return env, text
}

func wantProblem(t *testing.T, env wireEnvelope, status int, slug string) {
	t.Helper()
	if env.Problem == nil {
		t.Fatalf("expected a %d problem, got data: %+v", status, env.Data)
	}
	if env.Data != nil {
		t.Fatalf("a problem answer must carry no data: %+v", env.Data)
	}
	if env.Problem.Status != status || env.Problem.Type != problemTypeBase+slug {
		t.Fatalf("problem = %+v, want %d %s", env.Problem, status, slug)
	}
}

// ---- forward_slot_map -------------------------------------------------------

func TestWireForwardSlotMapAnswersTheApprovedPlansAssignments(t *testing.T) {
	h := newHarness(t, "SITE-1")
	h.seed(t)
	id := h.draft(t)
	h.clk.now = base.Add(time.Minute)
	if _, err := h.approve.Handle(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	w := newWire(t, h.deps)

	for name, args := range map[string]map[string]any{"explicit site": {"siteId": "SITE-1"}, "default site": {}} {
		t.Run(name, func(t *testing.T) {
			env, text := w.skill(t, skillForwardSlotMap, args)
			if env.Problem != nil || env.Skill != skillForwardSlotMap || env.Data == nil || env.Data.ForwardSlotMap == nil {
				t.Fatalf("envelope = %+v", env)
			}
			m := env.Data.ForwardSlotMap
			eq(t, "siteId", m.SiteID, "SITE-1")
			eq(t, "planId", m.PlanID, id)
			eq(t, "approvedAt", m.ApprovedAt, "2026-10-08T12:01:00Z")
			eq(t, "assignment count", len(m.Assignments), 2)
			for _, a := range m.Assignments {
				eq(t, "assignment pickUnit", a.PickUnit, "EACH")
				eq(t, "assignment "+a.SKU+" has a slot and a class", a.Slot != "" && a.AbcClass != "", true)
			}
			eq(t, "summary mentions site, count and plan", strings.Contains(text, "SITE-1") && strings.Contains(text, "2 SKUs") && strings.Contains(text, id), true)
		})
	}
}

func TestWireForwardSlotMapWithoutAnApprovedPlanIsEmptyNotAProblem(t *testing.T) {
	h := newHarness(t, "SITE-1")
	h.seed(t)
	h.draft(t) // a Draft is not the current map
	env, text := newWire(t, h.deps).skill(t, skillForwardSlotMap, map[string]any{"siteId": "SITE-1"})
	if env.Problem != nil || env.Data == nil || env.Data.ForwardSlotMap == nil {
		t.Fatalf("envelope = %+v", env)
	}
	if m := env.Data.ForwardSlotMap; m.PlanID != "" || m.ApprovedAt != "" || len(m.Assignments) != 0 || m.SiteID != "SITE-1" {
		t.Fatalf("map = %+v, want an empty map of SITE-1", m)
	}
	if !strings.Contains(text, "no Approved slot plan") {
		t.Errorf("summary = %q", text)
	}
}

func TestWireForwardSlotMapSiteProblems(t *testing.T) {
	for name, tc := range map[string]struct {
		defaultSite string
		args        map[string]any
	}{
		"malformed site":            {"SITE-1", map[string]any{"siteId": "bad site!"}},
		"no site and no default":    {"", map[string]any{}},
		"blank site, no default":    {"", map[string]any{"siteId": ""}},
		"malformed site no default": {"", map[string]any{"siteId": "a/b"}},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, tc.defaultSite)
			env, _ := newWire(t, h.deps).skill(t, skillForwardSlotMap, tc.args)
			wantProblem(t, env, http.StatusBadRequest, "invalid-site-id")
		})
	}
}

// ---- slot_plan_review -------------------------------------------------------

func TestWireSlotPlanReviewByIdReadsTheWholePlan(t *testing.T) {
	h := newHarness(t, "SITE-1")
	h.seed(t)
	id := h.draft(t)
	env, text := newWire(t, h.deps).skill(t, skillSlotPlanReview, map[string]any{"planId": id})
	if env.Problem != nil || env.Skill != skillSlotPlanReview || env.Data == nil || env.Data.SlotPlan == nil {
		t.Fatalf("envelope = %+v", env)
	}
	p := env.Data.SlotPlan
	eq(t, "planId", p.PlanID, id)
	eq(t, "siteId", p.SiteID, "SITE-1")
	eq(t, "state", p.State, "Draft")
	eq(t, "policy", p.Policy, "abc-velocity-v1")
	eq(t, "version", p.Version, 1)
	eq(t, "windowTo", p.WindowTo, "2026-10-08T12:00:00Z")
	eq(t, "windowFrom", p.WindowFrom, "2026-09-10T12:00:00Z")
	eq(t, "generatedAt", p.GeneratedAt, "2026-10-08T12:00:00Z")
	eq(t, "a fresh Draft has no decision", p.ApprovedAt+p.RejectedAt+p.RejectReason+p.SupersedesPlanID, "")
	eq(t, "assignments", len(p.Assignments), 2)
	eq(t, "moves", len(p.Moves), 2)
	eq(t, "unassigned", len(p.Unassigned), 1)
	if len(p.Unassigned) == 1 {
		eq(t, "the unassigned SKU", p.Unassigned[0].SKU, "SKU-3")
	}
	if p.Execution == nil {
		t.Fatal("the plan carries no execution progress (ADR 0010)")
	}
	eq(t, "movesTotal", p.Execution.MovesTotal, 2)
	eq(t, "movesExecuted", p.Execution.MovesExecuted, 0)
	for _, want := range []string{id, "is Draft", "2 assignments, 2 moves, 1 unassigned", "0 of 2 moves executed"} {
		eq(t, "summary contains "+want, strings.Contains(text, want), true)
	}
}

func TestWireSlotPlanReviewShowsTheDecision(t *testing.T) {
	h := newHarness(t, "SITE-1")
	h.seed(t)
	w := newWire(t, h.deps)

	approved := h.draft(t)
	h.clk.now = base.Add(time.Minute)
	if _, err := h.approve.Handle(context.Background(), approved); err != nil {
		t.Fatal(err)
	}
	env, _ := w.skill(t, skillSlotPlanReview, map[string]any{"planId": approved})
	if p := env.Data.SlotPlan; p.State != "Approved" || p.ApprovedAt != "2026-10-08T12:01:00Z" {
		t.Fatalf("approved plan = %+v", p)
	}

	rejected := h.draft(t)
	if _, err := h.reject.Handle(context.Background(), rejected, "too many relocations"); err != nil {
		t.Fatal(err)
	}
	env, _ = w.skill(t, skillSlotPlanReview, map[string]any{"planId": rejected})
	if p := env.Data.SlotPlan; p.State != "Rejected" || p.RejectReason != "too many relocations" || p.RejectedAt == "" {
		t.Fatalf("rejected plan = %+v", p)
	}
}

func TestWireSlotPlanReviewWithoutAPlanIdReadsTheNewestMatch(t *testing.T) {
	h := newHarness(t, "SITE-1")
	h.seed(t)
	w := newWire(t, h.deps)

	older := h.draft(t)
	h.clk.now = base.Add(time.Hour)
	newer := h.draft(t)
	h.clk.now = base.Add(2 * time.Hour)
	if _, err := h.approve.Handle(context.Background(), older); err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"newest of all":          {map[string]any{}, newer},
		"newest of the site":     {map[string]any{"siteId": "SITE-1"}, newer},
		"newest Draft":           {map[string]any{"state": "Draft"}, newer},
		"newest Approved":        {map[string]any{"siteId": "SITE-1", "state": "Approved"}, older},
		"no arguments at all":    {nil, newer},
		"explicit empty filters": {map[string]any{"siteId": "", "state": ""}, newer},
	} {
		t.Run(name, func(t *testing.T) {
			env, _ := w.skill(t, skillSlotPlanReview, tc.args)
			if env.Problem != nil || env.Data == nil || env.Data.SlotPlan == nil || env.Data.SlotPlan.PlanID != tc.want {
				t.Fatalf("envelope = %+v, want plan %s", env, tc.want)
			}
		})
	}
}

func TestWireSlotPlanReviewProblems(t *testing.T) {
	h := newHarness(t, "SITE-1")
	h.seed(t)
	h.draft(t)
	w := newWire(t, h.deps)

	for name, tc := range map[string]struct {
		args   map[string]any
		status int
		slug   string
	}{
		"unknown plan id":           {map[string]any{"planId": missingPlan}, http.StatusNotFound, "plan-not-found"},
		"malformed plan id":         {map[string]any{"planId": "not-a-plan"}, http.StatusBadRequest, "invalid-plan-id"},
		"blank plan id":             {map[string]any{"planId": "   "}, http.StatusBadRequest, "invalid-plan-id"},
		"no plan matches the site":  {map[string]any{"siteId": "SITE-9"}, http.StatusNotFound, "plan-not-found"},
		"no plan in that state":     {map[string]any{"state": "Rejected"}, http.StatusNotFound, "plan-not-found"},
		"unknown state":             {map[string]any{"state": "Pending"}, http.StatusBadRequest, "invalid-state"},
		"malformed site filter":     {map[string]any{"siteId": "bad site!"}, http.StatusBadRequest, "invalid-site-id"},
		"plan id with a site":       {map[string]any{"planId": missingPlan, "siteId": "SITE-1"}, http.StatusBadRequest, "invalid-argument"},
		"plan id with a state":      {map[string]any{"planId": missingPlan, "state": "Draft"}, http.StatusBadRequest, "invalid-argument"},
		"plan id wrong JSON type":   {map[string]any{"planId": 7}, http.StatusBadRequest, "invalid-argument"},
		"state wrong JSON type":     {map[string]any{"state": []string{"Draft"}}, http.StatusBadRequest, "invalid-argument"},
		"snake_case spelling":       {map[string]any{"plan_id": missingPlan}, http.StatusBadRequest, "invalid-argument"},
		"misspelled optional field": {map[string]any{"siteID2": "SITE-1"}, http.StatusBadRequest, "invalid-argument"},
	} {
		t.Run(name, func(t *testing.T) {
			env, text := w.skill(t, skillSlotPlanReview, tc.args)
			wantProblem(t, env, tc.status, tc.slug)
			if env.Skill != skillSlotPlanReview {
				t.Errorf("a problem answer still names its skill, got %q", env.Skill)
			}
			if !strings.Contains(text, "("+strconv.Itoa(tc.status)+")") {
				t.Errorf("summary = %q lacks the status", text)
			}
		})
	}
}

// ---- request shape ----------------------------------------------------------

// A request that is not a well-formed skill call is ANSWERED in-band (never a
// JSON-RPC error): strict decoding refuses unknown names and trailing data.
func TestWireRequestShapeValidation(t *testing.T) {
	h := newHarness(t, "SITE-1")
	w := newWire(t, h.deps)

	cases := []struct {
		name   string
		part   *a2a.Part
		status int
		slug   string
	}{
		{"unknown skill", a2a.NewDataPart(map[string]any{"skill": "approve_slot_plan", "arguments": map[string]any{}}), 400, "unknown-skill"},
		{"a skill of another context", a2a.NewDataPart(map[string]any{"skill": "staffing_status"}), 400, "unknown-skill"},
		{"missing skill", a2a.NewDataPart(map[string]any{"arguments": map[string]any{}}), 400, "malformed-skill-request"},
		{"unknown top-level field", a2a.NewDataPart(map[string]any{"skill": skillForwardSlotMap, "args": map[string]any{}}), 400, "malformed-skill-request"},
		{"skill of the wrong type", a2a.NewDataPart(map[string]any{"skill": 5}), 400, "malformed-skill-request"},
		{"text part that is not JSON", a2a.NewTextPart("where is SKU-1?"), 400, "malformed-skill-request"},
		{"text part with trailing data", a2a.NewTextPart(`{"skill":"forward_slot_map"} {"skill":"slot_plan_review"}`), 400, "malformed-skill-request"},
		{"text part with trailing garbage", a2a.NewTextPart(`{"skill":"forward_slot_map"} x`), 400, "malformed-skill-request"},
		{"raw part", a2a.NewRawPart([]byte("x")), 400, "malformed-skill-request"},
		{"arguments not an object", a2a.NewDataPart(map[string]any{"skill": skillForwardSlotMap, "arguments": 5}), 400, "invalid-argument"},
		{"arguments an array", a2a.NewDataPart(map[string]any{"skill": skillForwardSlotMap, "arguments": []any{}}), 400, "invalid-argument"},
		{"unknown argument", a2a.NewDataPart(map[string]any{"skill": skillForwardSlotMap, "arguments": map[string]any{"site": "SITE-1"}}), 400, "invalid-argument"},
		{"the deprecated-style snake_case key", a2a.NewDataPart(map[string]any{"skill": skillForwardSlotMap, "arguments": map[string]any{"site_id": "SITE-1"}}), 400, "invalid-argument"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, text, _ := w.call(t, tc.part)
			wantProblem(t, env, tc.status, tc.slug)
			if text == "" {
				t.Error("a problem answer carries a text summary too")
			}
		})
	}
}

// A text part holding the skill JSON is accepted, and the reply binds to the
// caller's context.
func TestWireTextPartAndContextBinding(t *testing.T) {
	h := newHarness(t, "SITE-1")
	h.seed(t)
	id := h.draft(t)
	w := newWire(t, h.deps)

	env, _, msg := w.call(t, a2a.NewTextPart(`{"skill":"slot_plan_review","arguments":{"planId":"`+id+`"}}`))
	if env.Problem != nil || env.Data == nil || env.Data.SlotPlan == nil || env.Data.SlotPlan.PlanID != id {
		t.Fatalf("envelope = %+v", env)
	}
	if msg.Role != a2a.MessageRoleAgent {
		t.Errorf("reply role = %v, want agent", msg.Role)
	}

	req := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewDataPart(map[string]any{"skill": skillForwardSlotMap}))
	req.ContextID = "ctx-77"
	resp, err := w.client.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: req})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.(*a2a.Message).ContextID; got != "ctx-77" {
		t.Errorf("reply contextId = %q, want the caller's ctx-77", got)
	}
}

// ---- reads never write, errors never leak ----------------------------------

// Every skill run adds no outbox row: the A2A surface cannot emit an event.
func TestWireReadsAddNoOutboxRow(t *testing.T) {
	h := newHarness(t, "SITE-1")
	h.seed(t)
	approved := h.draft(t)
	if _, err := h.approve.Handle(context.Background(), approved); err != nil {
		t.Fatal(err)
	}
	before := outboxCount(t, h)
	if before == 0 {
		t.Fatal("seeding wrote no outbox rows; the check below would be vacuous")
	}
	w := newWire(t, h.deps)
	w.skill(t, skillForwardSlotMap, map[string]any{})
	w.skill(t, skillSlotPlanReview, map[string]any{"planId": approved})
	w.skill(t, skillSlotPlanReview, map[string]any{})
	w.skill(t, skillSlotPlanReview, map[string]any{"planId": missingPlan})
	if after := outboxCount(t, h); after != before {
		t.Fatalf("outbox rows %d -> %d: a read skill wrote an event", before, after)
	}
}

func outboxCount(_ *testing.T, h *harness) int {
	return len(h.outbox.Messages())
}

// failingPlans is a plan repository whose every read fails with an
// infrastructure error carrying text that must never reach an agent.
type failingPlans struct{ ports.SlotPlanRepository }

var errInfra = errors.New(`pq: password authentication failed for user "slotting" at postgres://slotting:s3cret@db:5432`)

func (failingPlans) Get(context.Context, slotplan.PlanID) (*slotplan.SlotPlan, error) {
	return nil, errInfra
}

func (failingPlans) Current(context.Context, slotplan.SiteID) (*slotplan.SlotPlan, error) {
	return nil, errInfra
}

func (failingPlans) List(context.Context, repository.PlanFilter, slotplan.PlanID, int) ([]*slotplan.SlotPlan, error) {
	return nil, errInfra
}

func TestWireInfrastructureFailureIsAGeneric500(t *testing.T) {
	plans := failingPlans{}
	deps := Deps{
		GetPlan:          &usecases.GetPlan{Plans: plans},
		ListPlans:        &usecases.ListPlans{Plans: plans},
		ListForwardSlots: &usecases.ListForwardSlots{Plans: plans, DefaultSite: "SITE-1"},
	}
	w := newWire(t, deps)
	for name, call := range map[string]func() (wireEnvelope, string){
		"forward_slot_map": func() (wireEnvelope, string) { return w.skill(t, skillForwardSlotMap, map[string]any{}) },
		"slot_plan_review id": func() (wireEnvelope, string) {
			return w.skill(t, skillSlotPlanReview, map[string]any{"planId": missingPlan})
		},
		"slot_plan_review list": func() (wireEnvelope, string) { return w.skill(t, skillSlotPlanReview, map[string]any{}) },
	} {
		t.Run(name, func(t *testing.T) {
			env, text := call()
			wantProblem(t, env, http.StatusInternalServerError, "internal-error")
			raw, _ := json.Marshal(env)
			for _, leaked := range []string{"password", "s3cret", "postgres://", "pq:"} {
				if strings.Contains(string(raw), leaked) || strings.Contains(text, leaked) {
					t.Errorf("the answer leaks infrastructure text %q: %s / %s", leaked, raw, text)
				}
			}
		})
	}
}
