// The MCP surface beyond tools (Agentic Standard v2, AS-3): ONE resource —
// the scoped current-forward-slots read model — and ONE prompt — this
// context's operator SOP. Pinned through the same in-memory client the tool
// tests use.
package mcp_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// approve drafts a plan of SITE-1 from the seeded data and approves it.
func approve(t *testing.T, h *harness) string {
	t.Helper()
	planID := h.draft(t)
	if _, err := h.approve.Handle(context.Background(), planID); err != nil {
		t.Fatal(err)
	}
	return planID
}

// The resource template is advertised with the fleet URI shape and reads the
// same read model as the get_forward_slots tool.
func TestSurface_ResourceTemplateListedAndRead(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	planID := approve(t, h)
	ctx := t.Context()

	listed, err := h.session.ListResourceTemplates(ctx, nil)
	if err != nil {
		t.Fatalf("list resource templates: %v", err)
	}
	found := false
	for _, rt := range listed.ResourceTemplates {
		if rt.URITemplate == "warehouse://slotting-optimization/forward-slots/{siteId}" {
			found = true
			if rt.MIMEType != "application/json" {
				t.Errorf("template MIMEType = %q, want application/json", rt.MIMEType)
			}
		}
	}
	if !found {
		t.Fatalf("forward-slots resource template not advertised: %+v", listed.ResourceTemplates)
	}

	res, err := h.session.ReadResource(ctx, &sdk.ReadResourceParams{
		URI: "warehouse://slotting-optimization/forward-slots/SITE-1",
	})
	if err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if len(res.Contents) != 1 {
		t.Fatalf("contents = %d entries, want 1", len(res.Contents))
	}
	c := res.Contents[0]
	if c.URI != "warehouse://slotting-optimization/forward-slots/SITE-1" {
		t.Errorf("content URI = %q", c.URI)
	}
	if c.MIMEType != "application/json" {
		t.Errorf("content MIMEType = %q, want application/json", c.MIMEType)
	}
	assertForwardSlotMap(t, c.Text, "SITE-1", planID)
}

// assertForwardSlotMap decodes a resource's JSON text as the forward slot
// map of one site's Approved plan and checks its shape.
func assertForwardSlotMap(t *testing.T, body, siteID, planID string) {
	t.Helper()
	var m struct {
		SiteID      string `json:"site_id"`
		PlanID      string `json:"plan_id"`
		ApprovedAt  string `json:"approved_at"`
		Assignments []struct {
			SKU      string `json:"sku"`
			Slot     string `json:"slot"`
			AbcClass string `json:"abc_class"`
		} `json:"assignments"`
	}
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("resource text is not the forward-slots JSON: %v (%s)", err, body)
	}
	if m.SiteID != siteID || m.PlanID != planID {
		t.Errorf("forward slot map = site %s plan %s, want %s / %s", m.SiteID, m.PlanID, siteID, planID)
	}
	if planID == "" {
		if m.ApprovedAt != "" {
			t.Errorf("approved_at = %q, want none without an Approved plan", m.ApprovedAt)
		}
		if len(m.Assignments) != 0 {
			t.Errorf("assignments = %d, want none without an Approved plan", len(m.Assignments))
		}
		return
	}
	if m.ApprovedAt == "" {
		t.Errorf("approved_at is empty for the Approved plan %s", planID)
	}
	if len(m.Assignments) == 0 {
		t.Errorf("assignments = %v, want the seeded SKUs of the Approved plan", m.Assignments)
	}
	for _, a := range m.Assignments {
		if a.SKU == "" || a.Slot == "" || a.AbcClass == "" {
			t.Errorf("assignment %+v is missing sku, slot or abc_class", a)
		}
	}
}

// A site without an Approved plan is an empty map (the use case's contract),
// not an error; malformed URIs are errors, never silent defaults.
func TestSurface_ResourceEmptyMapAndErrors(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	ctx := t.Context()

	// No Approved plan yet: an empty assignment list, no plan_id.
	res, err := h.session.ReadResource(ctx, &sdk.ReadResourceParams{
		URI: "warehouse://slotting-optimization/forward-slots/SITE-1",
	})
	if err != nil {
		t.Fatalf("read resource without an Approved plan: %v", err)
	}
	assertForwardSlotMap(t, res.Contents[0].Text, "SITE-1", "")

	for _, uri := range []string{
		"staffing://slotting-optimization/forward-slots/SITE-1", // wrong scheme
		"warehouse://node-topology/forward-slots/SITE-1",        // wrong context
		"warehouse://slotting-optimization/plans/SITE-1",        // wrong model
		"warehouse://slotting-optimization/forward-slots/",      // empty siteId
		"warehouse://slotting-optimization/forward-slots/S/1",   // trailing segment
	} {
		if _, err := h.session.ReadResource(ctx, &sdk.ReadResourceParams{URI: uri}); err == nil {
			t.Errorf("read resource %q: expected an error", uri)
		}
	}

	// A well-formed site with no Approved plan of its own is an empty map,
	// not an error (the use case's contract).
	other, err := h.session.ReadResource(ctx, &sdk.ReadResourceParams{
		URI: "warehouse://slotting-optimization/forward-slots/SITE-2",
	})
	if err != nil {
		t.Fatalf("read resource of SITE-2: %v", err)
	}
	assertForwardSlotMap(t, other.Contents[0].Text, "SITE-2", "")
}

// The prompt is advertised with its typed argument and returns the SOP text.
func TestSurface_PromptListedAndGet(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	planID := approve(t, h)
	ctx := t.Context()

	listed, err := h.session.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatalf("list prompts: %v", err)
	}
	if len(listed.Prompts) != 1 || listed.Prompts[0].Name != "review_slot_plan" {
		t.Fatalf("prompts = %+v, want exactly review_slot_plan", listed.Prompts)
	}
	if len(listed.Prompts[0].Arguments) != 1 || listed.Prompts[0].Arguments[0].Name != "planId" {
		t.Fatalf("prompt arguments = %+v, want one planId", listed.Prompts[0].Arguments)
	}

	res, err := h.session.GetPrompt(ctx, &sdk.GetPromptParams{
		Name:      "review_slot_plan",
		Arguments: map[string]string{"planId": planID},
	})
	if err != nil {
		t.Fatalf("get prompt: %v", err)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(res.Messages))
	}
	text := res.Messages[0].Content.(*sdk.TextContent).Text
	for _, needle := range []string{"get_slot_plan", "list_slot_plans", "get_forward_slots", "get_sku_velocity", planID, "REST"} {
		if !strings.Contains(text, needle) {
			t.Errorf("SOP text does not mention %q", needle)
		}
	}

	// A missing argument degrades to the placeholder, never to a hidden call.
	noArg, err := h.session.GetPrompt(ctx, &sdk.GetPromptParams{Name: "review_slot_plan"})
	if err != nil {
		t.Fatalf("get prompt without arguments: %v", err)
	}
	if plain := noArg.Messages[0].Content.(*sdk.TextContent).Text; !strings.Contains(plain, "<plan_id>") {
		t.Errorf("SOP without an argument should carry the <plan_id> placeholder")
	}
}
