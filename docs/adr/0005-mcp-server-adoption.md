# ADR 0005: MCP server adoption (read-only, unauthenticated, additive)

## Status

Accepted (2026-10-08).

## Context

The fleet exposes every bounded context to `warehouse-ops-agent` (and any
other MCP client) through a second, independent deployable binary that serves
the Model Context Protocol over Streamable HTTP, alongside the REST API and
never bundled into the same process (warehouse-planning ADR 0008,
product-master ADR 0005 and inbound-receiving ADR 0005 record the same
decision for those contexts). slotting-optimization owns the forward pick slot
plans (ADR 0002: Draft, then Approved, Rejected or Superseded), the current
forward slot map of a site and the demand velocity the planner works from;
agents ask about them constantly ("what is the current forward slot of
SKU-1?", "what did the pending plan change?", "which SKUs picked fastest this
month?"), while every change to a plan must leave as a versioned CloudEvent
through the transactional outbox (ADR 0004) and the approval is a human
decision (ADR 0002).

## Decision

1. `cmd/mcp` is a **second composition root**, independent of `cmd/api`: it
   wires the SAME read use cases (`usecases.GetPlan`, `ListPlans`,
   `ListForwardSlots`, `ListSkuVelocity`) to the SAME repositories (in-memory
   when `DATABASE_URL` is unset, Postgres otherwise) through a dedicated
   inbound adapter (`internal/adapters/inbound/mcp`) instead of the REST
   router. It is built on the official SDK
   (`github.com/modelcontextprotocol/go-sdk`), Streamable HTTP only (no stdio,
   no SSE), listens on `MCP_ADDR` (default `:8090`), serves the MCP endpoint
   at both `/` and `/mcp`, and answers `GET /healthz` for the probes.
2. The surface is **read-only: 4 tools**, each backed by an existing read use
   case and returning the REST body with snake_case names:
   `get_slot_plan` (`plan_id`), `list_slot_plans` (filters `site_id`, `state`;
   `limit`, `cursor`), `get_forward_slots` (optional `site_id`) and
   `get_sku_velocity` (optional `site_id`, `limit`, `window_days`). Lists page
   with the opaque cursor of the REST API (`limit` 1..500, default 100,
   `next_cursor` absent on the last page). Errors are tool results
   (`isError: true`) whose text starts with the REST problem slug
   (`plan-not-found`, `invalid-plan-id`, `invalid-site-id`, `invalid-limit`,
   `invalid-cursor`, `invalid-state`); anything untyped is logged and reported
   as a generic `internal-error`.
3. **No write tool.** Generating a plan and approving or rejecting it stay
   REST-only: those are the operations whose events other contexts consume,
   whose idempotency is keyed by the `Idempotency-Key` header, whose version
   guard is the `If-Match` header, and whose approval is the human decision of
   ADR 0002; an agent-initiated write path would be a second front door to the
   outbox, and a way for software to approve its own proposal. `cmd/mcp` wires
   no write use case at all, so it cannot insert into the outbox.
   `internal/adapters/inbound/mcp/governance_test.go` fails the build if any
   advertised tool name contains a write verb (`generate`, `approve`,
   `reject`, `create`, `update`, `delete`, `set`, `assign`), if a tool is not
   annotated read-only, or if the surface exceeds the 4-tool budget; the
   tool-registry golden (`testdata/tool_registry.golden.json`) pins names,
   descriptions, annotations and input/output schemas byte for byte.
4. **No authentication of any kind**, matching the fleet-wide decision
   (reverted 2026-09-11) that REST and MCP are both unauthenticated; access
   control is the in-cluster `ClusterIP` boundary.
   `TestNoAuthMiddlewareReintroduced` fails CI if auth middleware appears.
5. `cmd/mcp` **never starts the outbox relay, never runs the local-copy
   consumers and never dials Kafka**: it reads the same Postgres database
   `cmd/api` writes and has no `EVENT_PUBLISHER`, `KAFKA_BROKERS`, `*_MODE` or
   consumer group. The one planning variable it reads is the default site
   (`DEMAND_SITE_ID`, else `DEFAULT_SITE_ID`, exactly as `cmd/api` resolves
   it), so `get_forward_slots` and `get_sku_velocity` answer without a
   `site_id` the same way the REST endpoints do. It runs the idempotent
   embedded migrations on boot (through `MIGRATIONS_DATABASE_URL` when set,
   like `cmd/api`), so it can start against a fresh database; golang-migrate's
   advisory lock makes concurrent starts safe.
6. `TestMCPAdapterDependencyRule` keeps the adapter additive: it depends only
   on the application and domain layers, and nothing else imports it.
7. Packaging: the image (`Dockerfile`) builds every `cmd/*` binary, so
   `/app/mcp` ships next to `/app/api`; the chart
   (`charts/slotting-optimization`) renders an `mcp` Deployment + ClusterIP
   Service (component `mcp`) only when `mcp.enabled=true` (default false), and
   the chart selector test proves each Service selects exactly one
   Deployment. No HPA for mcp: the SDK keeps per-process session state.

## Consequences

- An agent can answer slotting questions without a REST client, and can never
  generate, approve or reject a plan, or emit an event, by doing so.
- Adding a tool (or any write tool) is a reviewed act: a new ADR or an
  amendment of this one, the golden updated with `-update`, and the budget in
  `governance_test.go` raised in the same change.
- Two binaries now read the slotting tables; the mcp one holds no write
  credentials beyond what the shared `DATABASE_URL` grants. A read-only
  database role for it is possible later without code changes.
- `get_sku_velocity` reads the demand copy the planner uses (ACTIVE order
  lines only), so it can lag order-management by the consumer's lag; it is not
  a source of truth for orders.

## Alternatives considered and rejected

- **Mirror every REST operation as a tool (read + write)**: rejected; see
  point 3. Writes stay where the outbox, the idempotency keys, the version
  guard, the human approval and the API contract are reviewed.
- **Bundle MCP into `cmd/api`'s HTTP server**: couples the two surfaces'
  scaling and failure domains; the fleet convention is two deployables.
- **Bearer-token auth on MCP only**: rejected fleet-wide in the 2026-09-11
  revert.
