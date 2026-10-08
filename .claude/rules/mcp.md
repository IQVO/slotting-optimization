---
paths:
  - "internal/adapters/inbound/mcp/**"
  - "cmd/mcp/**"
  - "charts/slotting-optimization/templates/mcp-*.yaml"
---

# MCP server (inbound adapter, read-only)

One MCP server for this bounded context, an additive inbound adapter over the
SAME read use cases the REST adapter calls. Decision record:
`docs/adr/0005-mcp-server-adoption.md`; the short fleet rule is
`.claude/rules/fleet/no-auth-and-mcp.md`.

- Code: `internal/adapters/inbound/mcp/` (tools, error mapping) and the
  composition root `cmd/mcp/` (env, repositories, router, graceful shutdown).
  Built on the official SDK `github.com/modelcontextprotocol/go-sdk` (v1.8.0).
- Transport: **Streamable HTTP only** (no stdio, no SSE). Listens on
  `MCP_ADDR` (default `:8090`), served at **`/` and `/mcp`**; `GET /healthz`
  -> `200 {"status":"ok"}`.
- **Read-only.** `Deps` holds only the four read use cases (`GetPlan`,
  `ListPlans`, `ListForwardSlots`, `ListSkuVelocity`); never add a write use
  case, a repository `Save`, the outbox or an encoder to it. Generating a plan
  and approving or rejecting it are REST-only because their events feed other
  contexts, their idempotency and version guards live on the REST contract
  and the approval is the human decision of ADR 0002.
- **No auth of any kind** (fleet-wide revert 2026-09-11).
  `TestNoAuthMiddlewareReintroduced` fails CI if it is reintroduced.
- Architecture: the adapter depends ONLY on `internal/application` and
  `internal/domain`; nothing depends on it (`TestMCPAdapterDependencyRule`).
- Env (`cmd/mcp`): `MCP_ADDR`, `DATABASE_URL` (unset -> in-memory
  repositories), `MIGRATIONS_DATABASE_URL` (direct DSN for the migration step
  only), `DEMAND_SITE_ID` / `DEFAULT_SITE_ID` (the default site of the
  site-scoped tools, resolved like `cmd/api`), `OTEL_EXPORTER_OTLP_ENDPOINT`,
  `LOG_LEVEL`, `SERVICE_VERSION`, `ENVIRONMENT`. It runs the idempotent
  embedded migrations on start and uses `internal/bootretry` for the Postgres
  dial. It does NOT start the outbox relay, does NOT run the local-copy
  consumers and does NOT dial Kafka (no `EVENT_PUBLISHER`, `KAFKA_BROKERS`,
  `*_MODE` or consumer group).
- Chart: `mcp.enabled` (default false) renders
  `charts/slotting-optimization/templates/mcp-deployment.yaml` and
  `charts/slotting-optimization/templates/mcp-service.yaml` (component `mcp`,
  command `/app/mcp`, probes on `/healthz`). Keep the env there in sync with
  `cmd/mcp/main.go`'s header;
  `charts/slotting-optimization/tests/test_service_selectors.py` asserts the
  mcp pod gets no Kafka/relay/consumer/planning env.

## Tools (4; budget is 4)

Arguments are snake_case. Results are the REST bodies with snake_case names.
Failures are tool errors (`isError: true`) whose text is `<slug>: <message>`
with the REST problem slugs; unexpected infrastructure errors are logged and
reported as a generic `internal-error`. Lists page with the REST cursor
(`limit` 1..500, default 100; `next_cursor` is absent on the last page).

| Tool | Backed by | Arguments | Result | Errors |
|---|---|---|---|---|
| `get_slot_plan` | `GetPlan` | `plan_id` | `plan_id`, `site_id`, `state`, `policy`, `window_from`, `window_to`, `generated_at`, `approved_at` / `rejected_at` / `superseded_at` / `reject_reason` / `supersedes_plan_id` (omitted when unset), `assignments[]` (`sku`, `slot`, `abc_class`, `picks`, `units`), `moves[]` (`sku`, `from_slot`, `to_slot`, `kind`), `unassigned[]` (`sku`, `reason`), `version` | `invalid-plan-id`, `plan-not-found` |
| `list_slot_plans` | `ListPlans` | optional `limit`, `cursor`, `site_id`, `state` (Draft, Approved, Rejected, Superseded) | `items[]` (summaries with `assignment_count`, `move_count`, `unassigned_count`; never null), `next_cursor` | `invalid-limit`, `invalid-cursor`, `invalid-state`, `invalid-site-id` |
| `get_forward_slots` | `ListForwardSlots` | optional `site_id` (default site) | `site_id`, `plan_id` / `approved_at` (omitted without an Approved plan), `assignments[]` (`sku`, `slot`, `abc_class`) | `invalid-site-id` |
| `get_sku_velocity` | `ListSkuVelocity` | optional `site_id` (default site), `limit`, `window_days` (1..365, default 28) | `site_id`, `window_from`, `window_to`, `items[]` (`sku`, `picks`, `units`; fastest first) | `invalid-site-id`, `invalid-limit` (also a bad `window_days`, as REST) |

## Tests

- `internal/adapters/inbound/mcp/governance_test.go`: `TestToolSurface` pins
  the exact set, the budget, read-only annotations, descriptions and
  documented snake_case arguments, and fails on any write-verb tool name
  (`generate`, `approve`, `reject`, `create`, `update`, `delete`, `set`,
  `assign`); `TestWriteVerbSensorFailsOnWriteNames` proves that check can
  fail; `TestToolRegistryGolden` pins the advertised registry against
  `testdata/tool_registry.golden.json`.
- `tools_test.go` drives every tool through the SDK in-memory transport over
  in-memory repos (seeded with the write use cases, as REST would) and proves
  reads add no outbox row and leak no infrastructure error text.
- `cmd/mcp/main_test.go`: `/healthz`, both mount paths over real Streamable
  HTTP, no auth required. `cmd/mcp/main_integration_test.go`
  (`-tags=integration`, testcontainers Postgres): boots on a migrated DB,
  reads what the api's use cases wrote, outbox unchanged by reads.

## Adding a tool

Only a read tool, only with an ADR 0005 amendment: a typed input struct
(snake_case `json` tags + `jsonschema:"..."` on every field), a `Deps` method
calling an existing read use case, registration in `registerTools` with the
read-only annotations, errors through `mapError`, `wantTools` and `maxTools`
in `governance_test.go` updated, and the golden regenerated with
`go test ./internal/adapters/inbound/mcp -run TestToolRegistryGolden -update`.
