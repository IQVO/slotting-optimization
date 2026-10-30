# ADR 0013: A2A inbound adapter (read-only, unauthenticated, additive)

## Status

Accepted (2026-10-29). Follows [ADR 0005](./0005-mcp-server-adoption.md) (the
read-only MCP adapter this one is modelled on and whose charter it inherits),
[ADR 0004](./0004-cloudevents-envelope-and-type-catalogue.md) (CloudEvents on
every Kafka message) and
[ADR 0011](./0011-postgres-and-kafka-are-the-only-runtime.md) (Postgres and
Kafka are the only runtime). Mirrors the fleet's reference rollouts
(workforce-management ADR 0039, wes-work-planning ADR 0046), minus the write
skill: this context's agent surface is read-only.

## Context

The fleet is adopting the Agent2Agent protocol so that a bounded context can
**delegate** a goal to another context's agent. slotting-optimization already
exposes its use cases over REST and, read-only, over MCP (ADR 0005). Three
transports now carry three different things and must not overlap:

| Transport | Carries |
|---|---|
| Kafka + CloudEvents | **facts** between contexts (ADR 0004) |
| MCP | an agent's **tools** (here: four reads) |
| A2A | **delegation** of a goal to another context's agent; never data sync |

The two goals a delegating agent has for this context are the two things the
ADR 0005 reads exist for: *where is each SKU slotted right now?* and *what does
this plan change, and how far has it been executed?*

## Decision

1. Add `internal/adapters/inbound/a2a`, a third driving adapter over the
   **same** read use cases (`GetPlan`, `ListPlans`, `ListForwardSlots`) as the
   REST and MCP adapters, built on the official `a2aproject/a2a-go/v2` SDK
   (pinned v2.7.0) and served by a new `cmd/a2a` binary: JSON-RPC 2.0 over
   HTTP, port **8093**, the Agent Card at `/.well-known/agent-card.json`, the
   endpoint mounted at `/` and `/a2a`, `GET /healthz`. `A2A_AGENT_URL` (the
   absolute http(s) URL the card advertises) is mandatory: a missing or
   relative value is refused at boot before telemetry, any connection and the
   listener.
2. **The agent is READ-ONLY, because the MCP charter is.** ADR 0005 point 3
   keeps generating, approving and rejecting a plan REST-only: those are the
   operations whose events other contexts consume, whose idempotency is keyed by
   `Idempotency-Key`, whose version guard is `If-Match` and whose approval is
   the human decision of ADR 0002. An agent-initiated write would be a second
   front door to the outbox and a way for software to approve its own
   proposal. The A2A surface therefore offers no write skill, and
   `internal/adapters/inbound/a2a/governance_test.go` fails the build if
   `Deps` gains a use case beyond the three reads, if the adapter imports any
   package beyond the read use cases' package, the repository error sentinels
   and the plan value errors, or if a skill id carries a write verb
   (`generate`, `approve`, `reject`, `create`, `update`, `delete`, `set`,
   `assign`). Widening it is a reviewed act: an amendment of this ADR and of
   ADR 0005.
3. **Two curated, goal-shaped skills**, derived from the MCP tools (not one
   skill per endpoint):

   | Skill | Kind | Composed from | What it does |
   |---|---|---|---|
   | `forward_slot_map` | read | `get_forward_slots` | the assignments (`sku`, `slot`, `abcClass`, `pickUnit`, `casesCapacity`) of the site's Approved plan, with its `planId` and `approvedAt`; a site without an Approved plan yields an empty map and no `planId`, not a problem. `siteId` defaults to the service's default site. |
   | `slot_plan_review` | read | `get_slot_plan`, `list_slot_plans` | one plan in full: state, policy, window, decision instants, assignments, moves, unassigned SKUs and execution progress (ADR 0010). With `planId` that plan; without it the NEWEST plan matching the optional `siteId` and `state` (the two MCP reads composed, so the caller need not page). `planId` cannot be combined with the filters. |

   `get_sku_velocity` (demand the planner works from, not a goal a peer
   delegates) is deliberately not offered.
4. **Message-only, answered in-band.** Both skills are transactional
   request/response reads, so the agent has no Task lifecycle. Domain
   rejections and malformed requests are **answered** as RFC 7807-shaped
   `problem`s inside the message envelope (`{skill, data | problem}` in a data
   part plus a one-line text part), never raised as JSON-RPC errors, with the
   **same classification and slugs as the REST adapter** (`problemCatalogue`,
   shared problem base URI): `400` `invalid-plan-id`, `invalid-site-id` (also
   when no site is given and no default is configured), `invalid-state`,
   `404` `plan-not-found` (an unknown plan id, or no plan matching the filters
   of `slot_plan_review`) and `500` `internal-error` with a generic detail so
   no infrastructure text (DSNs, SQL) reaches the caller. REST's `409` entries
   (`plan-not-draft`, `approved-plan-conflict`, `concurrent-modification`) are
   write-only and unreachable here, and there is no `503` because no skill
   calls a dependency. Adapter-owned `400`s cover the request shape:
   `unknown-skill`, `malformed-skill-request`, `invalid-argument`.
5. **Strict input.** Requests are decoded with `DisallowUnknownFields` and
   trailing data is refused, so a misspelled optional argument (`stat` for
   `state`) can never silently widen the question and answer for a different
   plan. Go's `encoding/json` matches names case-insensitively, so only
   genuinely unknown names are refused. Arguments are camelCase like the REST
   contract and carry only the canonical keys (`siteId`, `planId`, `state`):
   no shared domain error text names a deprecated alias, so no
   answer-rewriting was needed.
6. **`cmd/a2a` is a read-only composition root** cloned from `cmd/mcp` by a
   script of asserted exact replacements. Like `cmd/mcp` it wires no write use
   case, so it can never insert into the outbox; it therefore starts no outbox
   relay, runs no local-copy consumer and never dials Kafka (no
   `EVENT_PUBLISHER`, no `KAFKA_BROKERS`, no consumer group). This differs from
   the reference rollouts whose A2A binary shares an outbox publisher for its
   write skill: here a delegated *read* leaves no fact, and nothing else
   needs to leave. Postgres is the only runtime (ADR 0011): `DATABASE_URL` is
   mandatory, there is no in-memory path (`TestRuntimeBinaryHasNoInMemoryAdapters`
   scans `cmd/a2a` too), and the idempotent embedded migrations run on boot
   (through `MIGRATIONS_DATABASE_URL` when set) exactly as in `cmd/mcp`.
7. **Unauthenticated, like REST and MCP** (fleet-wide revert 2026-09-11). The
   card declares no security schemes and `TestNoAuthMiddlewareReintroduced`
   scans the adapter (`internal/adapters/inbound/**`); `cmd/a2a` ships no
   middleware beyond tracing and metrics. Access control is the in-cluster
   `ClusterIP` boundary.
8. **Layering.** `TestA2AAdapterDependencyRule` keeps the adapter additive: it
   depends only on the application and domain layers and nothing else imports
   it. Each skill run opens an OTel span `a2a.skill <id>` (attribute
   `a2a.skill`), mirroring `mcp.tool <name>`.
9. **Packaging.** The image already builds every `cmd/*` binary, so `/app/a2a`
   ships next to `/app/mcp`. The chart renders an `a2a` Deployment + ClusterIP
   Service (component `a2a`, `<release>-a2a`, command `/app/a2a`) and an
   optional Kong `HTTPRoute` only when `a2a.enabled=true` (default **false**).
   `A2A_AGENT_URL` defaults to `http://<release>-a2a.<ns>.svc.cluster.local:8093/`
   and is overridable with `a2a.agentUrl`; the route `/api/a2a/slotting-optimization`
   is rewritten to `/`, so the card stays reachable through the gateway. Port
   8093 is the fleet's A2A port (8090 is mcp, 8091 the projector admin port,
   8092 the reports API). Like mcp there is no HPA: the agent is a single
   replica until a load reason appears.

## Consequences

- An agent can delegate "where is SKU-1 slotted?" and "what did the pending
  plan change?" to this context without a REST or MCP client, and can never
  generate, approve or reject a plan, or emit an event, by doing so.
- The same two questions are answerable over MCP; A2A adds goal-shaped
  composition (the latest-plan form) and an in-band problem vocabulary, not new
  data. Keeping the contracts apart avoids a third place where a read model can
  drift: all three adapters call the same use cases.
- A write skill (for example delegating plan generation) would need its own
  ADR: the `Idempotency-Key` and `If-Match` guards would have to be carried in
  the message, the human approval of ADR 0002 stays outside any agent
  surface, and `cmd/a2a` would then need the outbox publisher and `KAFKA_BROKERS`
  that the reference rollouts wire.
- `slot_plan_review` without a `planId` reads the newest plan by
  `generated_at`; two plans generated in the same instant order by id, as the
  list endpoint does.
- Three binaries now read the slotting tables; the a2a one holds no write
  credentials beyond what the shared `DATABASE_URL` grants (a read-only
  database role is possible later without code changes, as for mcp).

## Alternatives considered and rejected

- **A write skill (`generate_slot_plan`, `approve_slot_plan`)**: rejected; see
  point 2. It would widen what the context exposes beyond ADR 0005.
- **One skill per MCP tool (four skills)**: rejected; the fleet curates at most
  two goal-shaped skills per agent, and plan listing is only ever asked to find
  the plan that `slot_plan_review` then reads.
- **Serve A2A from `cmd/mcp` or `cmd/api`**: couples scaling and failure
  domains of surfaces the fleet deploys independently.
- **Wire the outbox publisher "just in case"**: a binary without a write use
  case needs no Kafka; omitting it keeps the pod's blast radius and its env
  (and the chart test that proves it) minimal.
