// Package a2a is the inbound Agent2Agent adapter: it exposes this bounded
// context to the fleet's agent ecosystem as a third driving adapter over the
// same application-layer READ use cases the REST and MCP adapters use. It is
// built on the official A2A Go SDK (a2aproject/a2a-go) and speaks JSON-RPC 2.0
// over HTTP, with the Agent Card served at the well-known path.
//
// Per docs/adr/0013-a2a-inbound-adapter.md, A2A is the DELEGATION channel
// between bounded contexts: an agent client sends a goal-shaped skill request
// as a structured message part and this agent answers from its own state
// through its own use cases. It is not a data-sync channel -- facts between
// contexts still travel exclusively as CloudEvents on Kafka (ADR 0004).
//
// Like the MCP adapter (ADR 0005) the surface is READ-ONLY: generating,
// approving and rejecting a plan stay REST-only (their events feed other
// contexts and the approval is the human decision of ADR 0002), so Deps holds
// no write use case and the binary can never insert into the outbox.
// governance_test.go fails the build if a write use case, a write-verb skill
// or a third skill appears. This package depends inward on the application
// layer and the domain only -- never on an outbound adapter, the REST adapter
// or the MCP adapter -- and nothing else may depend on it
// (internal/architecture's TestA2AAdapterDependencyRule). The composition
// root (cmd/a2a) wires concrete repositories into the use cases. There is no
// auth of any kind (fleet-wide revert 2026-09-11).
package a2a

import (
	"context"
	"iter"
	"net/http"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/claudioed/slotting-optimization/internal/application/usecases"
)

// Config carries the deployment-specific identity of this agent: the absolute
// URL clients use to reach its JSON-RPC endpoint (advertised on the Agent
// Card) and the card version. It is supplied by the composition root from
// A2A_AGENT_URL; the card MUST advertise a URL the fleet's agent clients can
// actually dial (in-cluster: http://<release>-a2a.<ns>.svc.cluster.local:8093/).
type Config struct {
	AgentURL string
	Version  string
}

// Deps is everything the A2A skills need, injected by the composition root.
// It carries the SAME read use cases the REST and MCP adapters use (GET
// /slot-plans/{planId}, /slot-plans, /forward-slots); the adapter never
// constructs an outbound adapter itself, and it holds no write use case at
// all (docs/adr/0013, docs/adr/0005).
type Deps struct {
	GetPlan          *usecases.GetPlan
	ListPlans        *usecases.ListPlans
	ListForwardSlots *usecases.ListForwardSlots
}

// Card builds this agent's Agent Card (ADR 0013). The surface is two curated,
// goal-shaped READ skills -- not one skill per endpoint, the same curation
// discipline ADR 0005 applied to MCP tools. forward_slot_map answers "where is
// each SKU slotted right now"; slot_plan_review answers "what does this plan
// (or the latest one of a site) change, and how far has it been executed".
// The card declares no security schemes on purpose: this surface is
// unauthenticated by the same fleet-wide decision as REST and MCP; schemes are
// added here, not in a redesign, if that decision is ever reversed.
func Card(cfg Config) *a2a.AgentCard {
	return &a2a.AgentCard{
		Name:        "slotting-optimization",
		Description: "Slotting Optimization agent for forward pick slotting (read-only): answers where every SKU is slotted right now at a site (the assignments of its Approved slot plan) and reviews a slot plan (its state and policy, the SKU-to-slot assignments with their ABC class, the moves against the previous plan, the SKUs left unassigned and the execution progress of its relocations), either by plan id or as the newest plan of a site and/or state. It changes nothing: generating a plan and approving or rejecting it (the human decision) are REST operations of slotting-optimization and are not offered here.",
		Version:     cfg.Version,
		Provider: &a2a.AgentProvider{
			Org: "IQVO",
			URL: "https://github.com/IQVO/slotting-optimization",
		},
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(cfg.AgentURL, a2a.TransportProtocolJSONRPC),
		},
		DefaultInputModes:  []string{"application/json", "text/plain"},
		DefaultOutputModes: []string{"application/json", "text/plain"},
		Capabilities:       a2a.AgentCapabilities{Streaming: false},
		Skills: []a2a.AgentSkill{
			{
				ID:          skillForwardSlotMap,
				Name:        "Current forward slot map of a site",
				Description: "Read the CURRENT forward slot map of a site: the assignments (sku, slot, abcClass, pickUnit, casesCapacity for CASE faces) of its Approved slot plan, with that planId and approvedAt. A site without an Approved plan yields an empty assignment list and no planId, not a problem. siteId defaults to the service's default site; a malformed site (or none given while no default is configured) is answered as a 400 problem. Read-only.",
				Tags:        []string{"slotting", "forward-slots", "read"},
				InputModes:  []string{"application/json"},
				Examples: []string{
					`{"skill":"forward_slot_map","arguments":{"siteId":"SITE-1"}}`,
					`{"skill":"forward_slot_map","arguments":{}}`,
				},
			},
			{
				ID:          skillSlotPlanReview,
				Name:        "Review a slot plan",
				Description: "Read one slot plan in full: siteId, state (Draft, Approved, Rejected or Superseded), policy, the demand window, generatedAt and the decision instants (approvedAt, rejectedAt, supersededAt, each omitted until it happens), rejectReason and supersedesPlanId (omitted when none), the assignments (sku, slot, abcClass, picks, units), the moves against the previous plan (sku, fromSlot, toSlot, kind), the unassigned SKUs with their reason, the version and the execution progress of its relocations. Give planId to read that plan (404 problem when unknown, 400 when malformed); without planId the NEWEST plan matching the optional siteId and/or state (Draft, Approved, Rejected, Superseded) is read, and a 404 problem says when none matches. planId cannot be combined with siteId or state. Read-only.",
				Tags:        []string{"slotting", "slot-plan", "read"},
				InputModes:  []string{"application/json"},
				Examples: []string{
					`{"skill":"slot_plan_review","arguments":{"planId":"plan-1b4e28ba-2fa1-4d3c-8f6e-0a1b2c3d4e5f"}}`,
					`{"skill":"slot_plan_review","arguments":{"siteId":"SITE-1","state":"Draft"}}`,
				},
			},
		},
	}
}

// NewExecutor returns the AgentExecutor that maps an incoming message to a
// skill and answers with a single agent Message (a message-only agent in A2A
// terms): both curated skills are transactional request/response reads, so
// there is no long-running Task lifecycle to track in this first A2A
// surface. The response carries the caller's contextId (and taskId when the
// client referenced one) so a delegating saga can group its interactions.
func NewExecutor(deps Deps) a2asrv.AgentExecutor {
	return a2asrv.AgentExecutorFunc(func(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			yield(answer(ctx, deps, ec), nil)
		}
	})
}

// Handler wires the executor into the transport-agnostic A2A request handler
// and wraps it in the JSON-RPC 2.0 HTTP handler -- the only transport this
// agent ships (ADR 0013), mirroring ADR 0005's "Streamable HTTP only"
// minimalism for MCP.
func Handler(executor a2asrv.AgentExecutor) http.Handler {
	return a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(executor))
}
