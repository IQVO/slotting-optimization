# slotting-optimization

The WMS-tier planner of forward pick slots in the
[warehouse-systems](https://github.com/IQVO) fleet: it proposes which SKUs get
a forward pick slot and which slot (policy `abc-velocity-v1`: rank by order-line
picks, ABC classes, eligibility by hazmat, temperature and capacity, stickiness
to limit churn), and records the human decision on the proposal (Draft, then
Approved or Rejected). Approved plans are published as CloudEvents with the full
assignment map and the moves. Execution of the moves on the floor is a planned,
not yet built, edge.

Built from `warehouse-harness-template` v2 (`HARNESS.md` documents the sensors).
CloudEvents subdomain `wms`; module `github.com/claudioed/slotting-optimization`.

## Where to read

| What | Where |
|---|---|
| Bounded context, classification, context map, exclusions | [`docs/adr/0001`](docs/adr/0001-slotting-optimization-bounded-context.md) |
| The SlotPlan aggregate, invariants, the `abc-velocity-v1` policy, human approval | [`docs/adr/0002`](docs/adr/0002-slotplan-aggregate-and-abc-velocity-policy.md) |
| Event-fed local copies, consumed contracts, modes and consumer groups | [`docs/adr/0003`](docs/adr/0003-local-copies-and-consumed-contracts.md) |
| CloudEvents envelope and the type catalogue | [`docs/adr/0004`](docs/adr/0004-cloudevents-envelope-and-type-catalogue.md) |
| REST contract | [`apis/openapi.yaml`](apis/openapi.yaml) |
| Event contract | [`apis/asyncapi.yaml`](apis/asyncapi.yaml) |
| MCP server (read-only tools, `cmd/mcp`) | [`docs/adr/0005`](docs/adr/0005-mcp-server-adoption.md), `.claude/rules/mcp.md` |
| Image and Helm chart | `Dockerfile`, `charts/slotting-optimization`, `.claude/rules/chart.md` |
| Domain code | `internal/domain/slotplan` (aggregate), `internal/domain/planning` (planner) |
| Agent rules | `.claude/rules/` |

## Status

Contracts, ADRs, the domain packages and the service (use cases, Postgres
persistence, the outbox, the three local-copy Kafka consumers and the REST
API in `cmd/api`) are in place, packaged as one image with two binaries
(`/app/api` and `/app/mcp`) and a Helm chart. Execution of the approved moves
on the floor is a planned, not yet built, edge.

## Binaries

| Binary | Port | What it is |
|---|---|---|
| `cmd/api` | 8080 | REST API, outbox relay and local-copy consumers (the writer) |
| `cmd/mcp` | 8090 | MCP server over Streamable HTTP, read-only, no Kafka (ADR 0005) |

`cmd/mcp` serves four tools at `/` and `/mcp` (health at `/healthz`), no auth:

| Tool | Reads |
|---|---|
| `get_slot_plan` | one plan by id: state, policy, window, assignments, moves, unassigned |
| `list_slot_plans` | plans newest first, filter `site_id` and `state` (Draft, Approved, Rejected, Superseded), paged |
| `get_forward_slots` | the CURRENT forward slot map of a site (its Approved plan) |
| `get_sku_velocity` | order-line picks and units per SKU over `window_days`, fastest first, `limit` |

Generating, approving and rejecting a plan stay on REST: the MCP surface has no
write tool and the build fails if one appears.

## Working locally

```bash
make check        # fmt, vet, build, lint, tests
make check-all    # check + coverage gate, arch tests, BDD
make integration  # testcontainers Postgres/Kafka tests (needs Docker)
make mutation     # gremlins on internal/domain/slotplan and internal/domain/planning
spectral lint apis/openapi.yaml --ruleset .spectral.yaml
spectral lint apis/asyncapi.yaml --ruleset .spectral.asyncapi.yaml
```

## Running it

```bash
docker build -t slotting-optimization:local .
docker run --rm -p 8080:8080 slotting-optimization:local        # api, in-memory without DATABASE_URL
docker run --rm -p 8090:8090 --entrypoint /app/mcp slotting-optimization:local   # MCP server
helm lint charts/slotting-optimization -f charts/slotting-optimization/ci/default-values.yaml
python3 charts/slotting-optimization/tests/test_service_selectors.py
```

The chart refuses to render without `database.url` or `database.existingSecret`
and exposes a dedicated value for every environment variable `cmd/api` reads
(`DEMAND_SITE_ID`, `LOOKBACK_DAYS`, `FORWARD_ZONE_CODES`, the `*_MODE` and
`*_CONSUMER_GROUP` pairs, ...); the MCP Deployment is off unless
`mcp.enabled=true`. Images and chart publish to `ghcr.io/iqvo/slotting-optimization`.

## Study project

This repo, like the rest of the `warehouse-systems` fleet, is a personal
study project exploring Domain-Driven Design, hexagonal architecture, and
AI-agent harness engineering. It is not production software and carries
no support guarantee.
