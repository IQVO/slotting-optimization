# slotting-optimization

The WMS-tier planner of forward pick slots in the
[warehouse-systems](https://github.com/IQVO) fleet: it proposes which SKUs get
a forward pick slot and which slot (policy `abc-velocity-v1`: rank by order-line
picks, ABC classes, eligibility by hazmat, temperature and capacity, stickiness
to limit churn), and records the human decision on the proposal (Draft, then
Approved or Rejected). Approved plans are published as CloudEvents with the full
assignment map and the moves. The moves of an Approved plan are executed on
the floor as WES relocation work (ADR 0010): wes-work-planning plans one
RELOCATE work unit per move, fulfillment-execution executes it,
inventory-storage records the `StockRelocated` that comes back here as the
plan's execution progress.

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
| Analytics read side (stream, `cmd/slotting-projector`, `cmd/slotting-reports`) | [`docs/adr/0006`](docs/adr/0006-analytics-read-side.md) |
| Execution of approved plans as WES relocation work | [`docs/adr/0010`](docs/adr/0010-approved-slot-plans-are-executed-as-wes-relocation-work.md) |
| Postgres and Kafka are the only runtime | [`docs/adr/0011`](docs/adr/0011-postgres-and-kafka-are-the-only-runtime.md) |
| Image and Helm chart | `Dockerfile`, `charts/slotting-optimization`, `.claude/rules/chart.md` |
| Domain code | `internal/domain/slotplan` (aggregate), `internal/domain/planning` (planner) |
| Agent rules | `.claude/rules/` |

## Documentation

The documentation site is published at
[https://iqvo.github.io/slotting-optimization/](https://iqvo.github.io/slotting-optimization/)
(sources in `docs/`, built by `.github/workflows/docs.yml`).

| Area | Pages |
| --- | --- |
| Overview | [Introduction](docs/docs/overview/introduction.md), [Architecture](docs/docs/overview/architecture.md), [Quickstart](docs/docs/overview/quickstart.md) |
| Operations | [Configuration](docs/docs/operations/configuration.md) (every env var), [Runbook](docs/docs/operations/runbook.md), [Observability](docs/docs/operations/observability.md), [Troubleshooting](docs/docs/operations/troubleshooting.md) |
| Development | [Testing](docs/docs/development/testing.md) |
| Ecosystem | [Integration](docs/docs/ecosystem/integration.md) |
| Domain-Driven Design | [Use cases](docs/docs/ddd/use-cases.md), [Subdomain classification](docs/docs/ddd/subdomain-classification.md), [Ubiquitous language](docs/docs/ddd/ubiquitous-language.md), [DDD artifacts index](docs/docs/ddd/ddd-artifacts.md): [core domain chart](docs/docs/ddd/core-domain-chart.md), [bounded context canvas](docs/docs/ddd/bounded-context-canvas.md), [context map](docs/docs/ddd/context-map.md), [aggregate design canvas](docs/docs/ddd/aggregate-design-canvas.md), [domain message flow](docs/docs/ddd/domain-message-flow.md), [EventStorming](docs/docs/ddd/eventstorming.md), [class diagrams](docs/docs/ddd/class-diagram.md), [entity-relationship](docs/docs/ddd/entity-relationship.md), [sequence diagrams](docs/docs/ddd/sequence-diagrams.md), [domain events](docs/docs/ddd/domain-events.md) |
| API | [API overview](docs/docs/api-reference/overview.md), [event catalogue](docs/docs/api-reference/events.md), generated REST reference (`docs/docs/api-reference/rest`, `npm run gen-api-docs`) |
| Decisions | [ADR index](docs/adr/index.md) |

## Status

Contracts, ADRs, the domain packages and the service (use cases, Postgres
persistence, the outbox, the four Kafka consumers and the REST API in
`cmd/api`), the read-only MCP server (`cmd/mcp`), the analytics read side
(`cmd/slotting-projector`, `cmd/slotting-reports`) and the web remote (`web/`)
are in place, packaged as one image with four binaries (`/app/api`,
`/app/mcp`, `/app/slotting-projector`, `/app/slotting-reports`) and a Helm
chart. warehouse-infra (`origin/develop`, `helm-values/slotting-optimization.yaml`)
deploys the api, the MCP server (Kong `/api/mcp/slotting-optimization`) and
the analytics pair; it does not deploy the web remote (no `frontend_remote` in
its `terraform/apps.yaml` entry, no `frontend.enabled`).

## Binaries

| Binary | Port | What it is |
|---|---|---|
| `cmd/api` | 8080 | REST API, outbox relay and local-copy consumers (the writer) |
| `cmd/mcp` | 8090 | MCP server over Streamable HTTP, read-only, no Kafka (ADR 0005) |
| `cmd/slotting-projector` | 8091 (admin: `/healthz`, `/readyz`) | consumes `warehouse.slotting-optimization.analytics` (group `ANALYTICS_CONSUMER_GROUP`) and writes the analytical database (`ANALYTICS_DATABASE_URL`); its only writer (ADR 0006) |
| `cmd/slotting-reports` | 8092 | read-only `GET /reports/slot-plan-funnel` and `GET /reports/freshness` over the analytical database (ADR 0006) |

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
docker compose up -d --wait   # Postgres :55432 + Kafka :9092 (the only runtime, no in-memory mode)
# then, with DATABASE_URL, KAFKA_BROKERS and the four *_CONSUMER_GROUP set (docker-compose.yml header):
go run ./cmd/api  # on :8080 (set DEFAULT_SITE_ID to omit siteId)
spectral lint apis/openapi.yaml --ruleset .spectral.yaml
spectral lint apis/asyncapi.yaml --ruleset .spectral.asyncapi.yaml
```

## Web remote (`web/`)

`slotting_mfe`, a Vite + React Module Federation remote that exposes `./App`
(screens: plan list and plan detail with the approve / reject workbench,
forward slots, SKU velocity). The `warehouse-console` shell (`origin/develop`)
mounts it under `/slotting/*`; the remote's own tests and comments assume
`/slotting-optimization/*` (it uses relative links, so it works under either).
Production asset base `/mfes/slotting-optimization/`; standalone dev server on
`:5194` — the next free port on the fleet's 5181+ ladder after
inbound-receiving's `:5193`.

```bash
cd web
npm ci            # needs a sibling warehouse-ui-kit checkout (file:../../warehouse-ui-kit)
npm run dev       # standalone on http://localhost:5194
npm test          # vitest: api-client mapping, idempotency headers, screens
npm run build     # tsc -b && vite build (dist/remoteEntry.js exposes slotting_mfe)
npm run lint      # oxlint
```

Standalone dev calls the API directly at `http://localhost:8080`, so start
`cmd/api` with `CORS_ALLOWED_ORIGINS=http://localhost:5194`. Against the kind
cluster's Kong instead, the remote reads the API origin at runtime from the
console's `/config.json` (`window.__WAREHOUSE_CONFIG__.apiOrigin`), so it
calls `http://localhost:8000/api/slotting-optimization` when served by the
console shell — nothing is baked in at build time. In the cluster the chart's
optional `frontend` component (off by default) serves the built bundle behind
warehouse-infra's Nginx web gateway at `/mfes/slotting-optimization/`.

## Running it

```bash
docker build -t slotting-optimization:local .
docker run --rm -p 8080:8080 slotting-optimization:local        # api: exits at boot unless DATABASE_URL, KAFKA_BROKERS and the four *_CONSUMER_GROUP are set (see docker-compose.yml)
docker run --rm -p 8090:8090 --entrypoint /app/mcp slotting-optimization:local   # MCP server: needs DATABASE_URL
docker run --rm -p 8091:8091 --entrypoint /app/slotting-projector slotting-optimization:local   # needs ANALYTICS_DATABASE_URL and KAFKA_BROKERS
docker run --rm -p 8092:8092 --entrypoint /app/slotting-reports slotting-optimization:local     # needs ANALYTICS_DATABASE_URL
helm lint charts/slotting-optimization -f charts/slotting-optimization/ci/default-values.yaml
bash charts/slotting-optimization/tests/render_test.sh
python3 charts/slotting-optimization/tests/test_service_selectors.py
```

Postgres and Kafka are the only runtime (docs/adr/0011): there is no in-memory
mode, and the chart refuses to render without `database.url` or
`database.existingSecret`, `kafka.brokers` and the four `config.*ConsumerGroup`
values. It exposes a dedicated value for every environment variable `cmd/api`
reads (`DEMAND_SITE_ID`, `LOOKBACK_DAYS`, `FORWARD_ZONE_CODES`, the `*_MODE` (only
`kafka`) and `*_CONSUMER_GROUP` pairs, ...). Three components are off by
default: the MCP Deployment (`mcp.enabled`), the analytics projector and
reports Deployments (`analytics.enabled`, which also needs `analytics.url` or
`analytics.existingSecret`) and the web remote (`frontend.enabled`). The chart
renders no Kong route for the reports Service (it has no `reports-route.yaml`).
Images and chart publish to `ghcr.io/iqvo/slotting-optimization`.

## Study project

This repo, like the rest of the `warehouse-systems` fleet, is a personal
study project exploring Domain-Driven Design, hexagonal architecture, and
AI-agent harness engineering. It is not production software and carries
no support guarantee.
