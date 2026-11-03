package mcp

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// reviewSlotPlanSOP is the operator standard-operating-procedure the
// review_slot_plan prompt hands to the model (Agentic Standard v2, AS-3).
// Per the charter, prompts encode how to interpret results, when to
// escalate, and what "done" means; this one names tools but NEVER calls
// them, and it cannot write: every tool it names is read-only (ADR 0005) —
// generating, approving and rejecting plans are REST operations and the
// approval is the human decision of ADR 0002.
const reviewSlotPlanSOP = `You are reviewing a forward pick slot plan of a warehouse site before its approval decision. Use only the MCP tools of slotting-optimization; never assume state — read it.

Procedure:
1. Call get_slot_plan(plan_id = PLAN). Read the state (Draft, Approved, Rejected or Superseded), the policy, the demand window (window_from, window_to) and version; generated_at is when the planner produced it. If you do not know the plan id, find it with list_slot_plans (optionally filtered by site_id and state; newest first — take the first page).
2. Read the assignments (sku, slot, abc_class, picks, units): each SKU the plan places in a forward pick face. A SKU in the unassigned list carries the reason it could not be placed (for example no dimensions on file) — that is a data gap to fix upstream, not a planner bug.
3. Read the moves against the previous plan (sku, from_slot, to_slot, kind): the relocations executing this plan will cause. Cross-check them against the CURRENT map with get_forward_slots(site_id): a move whose from_slot no longer matches reality means the previous plan changed since this one was generated — say so.
4. If you need to judge whether the ranking is still sound, read the demand it was built from with get_sku_velocity(site_id, window_days): the plan's picks and units come from the same ACTIVE order lines over its window. A big shift since window_to means the plan is stale.
5. If the plan is Approved, execution progress is included (execution: moves_total, moves_executed and per move its executed quantity and last_executed_at, recorded from inventory-storage's relocations).

Interpretation:
- abc_class follows the policy's velocity ranking (abc-velocity-v1): A SKUs should sit in the most accessible faces. A dense cluster of A SKUs in deep slots is a ranking problem worth flagging.
- A Rejected or Superseded plan is history: reject_reason and supersedes_plan_id say why and by what. Only an Approved plan feeds get_forward_slots.
- unassigned SKUs do not block approval by themselves; a missing dimension profile does block that SKU ever being placed.

Escalate to a human when: the moves would relocate a disproportionately large share of the assortment, a staleness or mismatch from steps 3-4 appears, or unassigned SKUs cover fast-moving A-class items. The approve/reject decision itself is a human call on REST (ADR 0002) — this context never makes it and offers no tool for it.

Done means: for PLAN you have reported its state, policy, window and version, summarised the assignments (count, A-class placement), the moves (count, biggest relocations) and the unassigned SKUs with reasons, flagged any staleness or cross-check mismatch, and given a one-line recommendation the human approver can act on.`

// registerPrompts adds the workflow prompt (the operator SOP of this
// context). Read-only by construction: a prompt returns text.
func (d Deps) registerPrompts(server *mcp.Server) {
	server.AddPrompt(&mcp.Prompt{
		Name:        "review_slot_plan",
		Description: "Standard operating procedure for reviewing a forward pick slot plan before its approval decision: read the plan, judge the assignments and moves against the current map and the demand, and know when to escalate.",
		Arguments: []*mcp.PromptArgument{{
			Name:        "planId",
			Description: "the slot plan id to review, e.g. plan-1b4e28ba-2fa1-4d3c-8f6e-0a1b2c3d4e5f (omit to have the SOP explain how to find the newest plan with list_slot_plans)",
			Required:    false,
		}},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		planID := strings.TrimSpace(req.Params.Arguments["planId"])
		if planID == "" {
			planID = "<plan_id>"
		}
		return &mcp.GetPromptResult{
			Description: "How to review a slot plan: read it, cross-check the moves and demand, escalate the borderline cases, recommend.",
			Messages: []*mcp.PromptMessage{{
				Role:    "user",
				Content: &mcp.TextContent{Text: strings.ReplaceAll(reviewSlotPlanSOP, "PLAN", planID)},
			}},
		}, nil
	})
}
