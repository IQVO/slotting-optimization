# slotting-optimization

[![CI](https://github.com/IQVO/slotting-optimization/actions/workflows/ci.yml/badge.svg?branch=develop)](https://github.com/IQVO/slotting-optimization/actions/workflows/ci.yml?query=branch%3Adevelop) [![coverage gate](https://img.shields.io/badge/coverage%20gate-90%25-brightgreen)](https://github.com/IQVO/slotting-optimization/blob/develop/Makefile) [![Go](https://img.shields.io/badge/go-1.27.3-00ADD8?logo=go&logoColor=white)](https://github.com/IQVO/slotting-optimization/blob/develop/go.mod) [![release](https://img.shields.io/github/v/release/IQVO/slotting-optimization?sort=semver)](https://github.com/IQVO/slotting-optimization/releases) [![docs](https://img.shields.io/badge/docs-iqvo.github.io-blue)](https://iqvo.github.io/slotting-optimization/) [![license](https://img.shields.io/badge/license-MIT-blue)](https://github.com/IQVO/slotting-optimization/blob/develop/LICENSE)

The WMS-tier planner of forward pick slots in the
[warehouse-systems](https://github.com/IQVO) fleet: it proposes which SKUs get
a forward pick slot and which slot (policy `abc-velocity-v1`: rank by
order-line picks, ABC classes, eligibility by hazmat, temperature and
capacity, stickiness to limit churn), and records the human decision on the
proposal (Draft, then Approved or Rejected). It does **not** execute moves:
approved plans are published as CloudEvents with the full assignment map and
the moves, and the moves of an Approved plan are executed on the floor as WES
relocation work (ADR 0010) — wes-work-planning plans one RELOCATE work unit
per move, fulfillment-execution executes it, inventory-storage records the
`StockRelocated` that comes back here as the plan's execution progress.
Built from `warehouse-harness-template` v2 (`HARNESS.md` documents the
sensors). CloudEvents subdomain `wms`; module
`github.com/claudioed/slotting-optimization`. Contracts, ADRs, the domain
packages and the service, the read-only MCP server, the read-only A2A agent,
the analytics read side and the web remote are all in place (see
[Status](#status)); agent rules live in `.claude/rules/`.

## Disclaimer

This project is a domain-driven design study. It is not associated with,
endorsed by or affiliated with any company, and it uses no company's code,
data, logos or trademarks. All names, figures and examples are illustrative
or synthetic.

---

## Documentation

The documentation site is published at
**[https://iqvo.github.io/slotting-optimization/](https://iqvo.github.io/slotting-optimization/)**
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

Where to read what:

| What | Where |
|---|---|
| Bounded context, classification, context map, exclusions | [`docs/adr/0001`](docs/adr/0001-slotting-optimization-bounded-context.md) |
| The SlotPlan aggregate, invariants, the `abc-velocity-v1` policy, human approval | [`docs/adr/0002`](docs/adr/0002-slotplan-aggregate-and-abc-velocity-policy.md) |
| Event-fed local copies, consumed contracts, modes and consumer groups | [`docs/adr/0003`](docs/adr/0003-local-copies-and-consumed-contracts.md) |
| CloudEvents envelope and the type catalogue | [`docs/adr/0004`](docs/adr/0004-cloudevents-envelope-and-type-catalogue.md) |
| MCP server (read-only tools, `cmd/mcp`) | [`docs/adr/0005`](docs/adr/0005-mcp-server-adoption.md), `.claude/rules/mcp.md` |
| A2A agent (read-only skills, `cmd/a2a`) | [`docs/adr/0013`](docs/adr/0013-a2a-inbound-adapter.md) |
| Analytics read side (stream, `cmd/slotting-projector`, `cmd/slotting-reports`) | [`docs/adr/0006`](docs/adr/0006-analytics-read-side.md) |
| Execution of approved plans as WES relocation work | [`docs/adr/0010`](docs/adr/0010-approved-slot-plans-are-executed-as-wes-relocation-work.md) |
| Postgres and Kafka are the only runtime | [`docs/adr/0011`](docs/adr/0011-postgres-and-kafka-are-the-only-runtime.md) |
| Image and Helm chart | `Dockerfile`, `charts/slotting-optimization`, `.claude/rules/chart.md` |
| Domain code | `internal/domain/slotplan` (aggregate), `internal/domain/planning` (planner) |

---

## Architecture

Hexagonal / ports & adapters; dependencies point inward only.

```text
apis/                       openapi.yaml (REST), asyncapi.yaml (events)
cmd/api/                    the composition root: REST API, outbox relay, the local-copy consumers (the writer)
cmd/mcp/                    read-only MCP server (ADR 0005)
cmd/a2a/                    read-only A2A agent (ADR 0013)
cmd/slotting-projector/     analytics projector — the only analytical-DB writer (ADR 0006)
cmd/slotting-reports/       analytics reader (:8092)
internal/domain/            slotplan (the SlotPlan aggregate), planning (the planner)
internal/application/       usecases, ports, repository, outbox, idempotency
internal/adapters/
  inbound/http/             chi handlers, RFC 7807 errors, Idempotency-Key middleware, /metrics
  inbound/kafka/            the five local-copy consumers and the projector's consumer
  inbound/mcp/, inbound/a2a/  the MCP tools and A2A skills (governance-tested: no writes)
  outbound/postgres/        pgxpool repos, UnitOfWork, outbox + relay
  outbound/kafka/           CloudEvents publisher
  outbound/outbox/          the transactional outbox relay (attempt cap, ADR 0008)
  outbound/analyticsstore/  analytical-DB projection and report queries (read-only)
  outbound/telemetry/       OpenTelemetry wiring
internal/architecture/      hexagonal and fleet fitness tests
migrations/                 golang-migrate SQL (analytics migrations embedded)
features/                   godog scenarios
web/                        the slotting_mfe remote
charts/slotting-optimization/  Helm chart (values, templates, tests/)
docs/                       Docusaurus site (docs/docs) and the ADRs (docs/adr)
```

The layering is enforced by the executable fitness tests in
`internal/architecture` (`make arch-test`, CI job `arch-test`).

## Status

Everything is packaged as one image with five binaries (`/app/api`,
`/app/mcp`, `/app/a2a`, `/app/slotting-projector`, `/app/slotting-reports`)
and a Helm chart. warehouse-infra (`origin/develop`,
`helm-values/slotting-optimization.yaml`) deploys the api, the MCP server
(Kong `/api/mcp/slotting-optimization`) and the analytics pair; it does not
deploy the web remote (no `frontend_remote` in its `terraform/apps.yaml`
entry, no `frontend.enabled`).

| Binary | Port | What it is |
|---|---|---|
| `cmd/api` | 8080 | REST API, outbox relay and local-copy consumers (the writer) |
| `cmd/mcp` | 8090 | MCP server over Streamable HTTP, read-only, no Kafka (ADR 0005) |
| `cmd/a2a` | 8093 | A2A agent (Agent2Agent JSON-RPC over HTTP), read-only, no Kafka (ADR 0013) |
| `cmd/slotting-projector` | 8091 (admin: `/healthz`, `/readyz`) | consumes `warehouse.slotting-optimization.analytics` (group `ANALYTICS_CONSUMER_GROUP`) and writes the analytical database (`ANALYTICS_DATABASE_URL`); its only writer (ADR 0006) |
| `cmd/slotting-reports` | 8092 | read-only `GET /reports/slot-plan-funnel` and `GET /reports/freshness` over the analytical database (ADR 0006) |

---

## Domain events

All messages are **CloudEvents 1.0**; the type convention is
`com.warehouse.wms.slotting-optimization.slotplan.<EventName>` (the `wms`
subdomain segment).

**Publishes** (through the transactional outbox) — 3 events on
`warehouse.slotting-optimization.events`, each with an `…Analytics` twin on
`warehouse.slotting-optimization.analytics`:

| Entity | Events |
|---|---|
| `slotplan` | `SlotPlanGenerated`, `SlotPlanApproved`, `SlotPlanRejected` |

Real types: `com.warehouse.wms.slotting-optimization.slotplan.SlotPlanGenerated`,
`…SlotPlanApproved`, `…SlotPlanRejected`.

**Consumes** — five always-on local-copy consumers (ADR 0003; every other
type ignored, `permissive`/`off` are boot errors) over four upstream topics:

| Topic | Messages | Consumer |
|---|---|---|
| `warehouse.order-management.events` | `SiteSkuDemandChanged` (the velocity input) | `DEMAND_*` |
| `warehouse.product-master.events` | `ProductClassified`, `ProductDimensionsDeclared`, `ProductMeasured` (eligibility) | `PRODUCT_*` |
| `warehouse.product-master.events` | `ProductPackLevelDeclared`, `ProductPackLevelMeasured`, `ProductPackLevelRetired` → the local PackLevelView (ADR 0012) | `PACK_LEVEL_*` |
| `warehouse.facility.events` | `ZoneRegistered`, `LocationSlotRegistered`, `LocationSlotDecommissioned` (the forward slots themselves) | `LAYOUT_*` |
| `warehouse.inventory.events` | `StockRelocated` → a plan's execution progress (ADR 0010) | `RELOCATION_*` |

Contracts are pinned in `apis/asyncapi.yaml`.

---

## Running it

Postgres and Kafka are the only runtime (ADR 0011): there is no in-memory
mode, no log sink and no consumer that is off. `cmd/api` exits at boot unless
`DATABASE_URL`, `KAFKA_BROKERS` and all five `*_CONSUMER_GROUP` variables are
set (see the `docker-compose.yml` header).

```bash
docker compose up -d --wait   # Postgres :55432 + Kafka :9092
export DATABASE_URL='postgres://slotting:***@localhost:55432/slotting?sslmode=disable'
export KAFKA_BROKERS='localhost:9092'
export DEMAND_CONSUMER_GROUP=so-demand-local PRODUCT_CONSUMER_GROUP=so-product-local \
       LAYOUT_CONSUMER_GROUP=so-layout-local RELOCATION_CONSUMER_GROUP=so-relocation-local \
       PACK_LEVEL_CONSUMER_GROUP=so-pack-level-local
go run ./cmd/api              # :8080 (set DEFAULT_SITE_ID to omit siteId)
```

`GET /readyz` is 200 only while Postgres answers a ping AND a Kafka broker
answers a metadata request (verdict cached 3 s, bounded 2 s); `/healthz` is
liveness only. The process exits non-zero when the relay or a consumer
returns on its own, so the orchestrator restarts it.

### Configuration

`cmd/api`:

| Env var | Default | Meaning |
|---|---|---|
| `HTTP_ADDR` | `:8080` | REST listen address |
| `DATABASE_URL` | **required** | Postgres DSN; no in-memory mode (ADR 0011) |
| `MIGRATIONS_DATABASE_URL` | *(unset = `DATABASE_URL`)* | Direct DSN for the boot migrations |
| `KAFKA_BROKERS` | **required** | Comma-separated brokers (relay, consumers, `/readyz`) |
| `EVENT_PUBLISHER` | `kafka` | Only `kafka`; `log` is a boot error |
| `OUTBOX_RELAY_INTERVAL` | `1s` | Relay poll interval (Go duration or seconds) |
| `OUTBOX_MAX_ATTEMPTS` | `20` | Failures before the relay excludes a row (`0` = unlimited, ADR 0008) |
| `DEMAND_SITE_ID` | *(unset; falls back to `DEFAULT_SITE_ID`)* | The site plans default to; when set, demand for other sites is ignored |
| `DEFAULT_SITE_ID` | *(unset)* | Default site when a request omits `siteId` (read by `api`, `mcp` and `a2a`) |
| `LOOKBACK_DAYS` | `28` | Default demand window of a new plan |
| `FORWARD_ZONE_CODES` | `FWD` | Comma-separated zone codes of the forward pick slots |
| `CASE_PICK_ZONE_CODES` | *(empty)* | Zone codes whose faces are CASE-pick faces; empty = every face is an EACH-pick face (ADR 0012) |
| `DEMAND_MODE`, `PRODUCT_MODE`, `LAYOUT_MODE`, `RELOCATION_MODE`, `PACK_LEVEL_MODE` | *(unset = `kafka`)* | Only `kafka`; `permissive`/`off` are boot errors |
| `DEMAND_` / `PRODUCT_` / `LAYOUT_` / `RELOCATION_` / `PACK_LEVEL_CONSUMER_GROUP` | **all required** | Stable group ids; boot error naming the variable |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | OTLP/gRPC Collector endpoint |
| `SHUTDOWN_DRAIN_DELAY` | `5s` | Wait after flipping `/readyz` before closing |
| `LOG_LEVEL` / `SERVICE_VERSION` / `ENVIRONMENT` / `CORS_ALLOWED_ORIGINS` | `info` / `dev` / `local` / *(unset)* | Logging, resource attributes, browser origins |

The other binaries:

| Binary | Env vars |
|---|---|
| `cmd/mcp` | `MCP_ADDR` (`:8090`), `DATABASE_URL` (**required**), `DEFAULT_SITE_ID`, `LOG_LEVEL`, `SERVICE_VERSION` |
| `cmd/a2a` | `A2A_ADDR` (`:8093`), `A2A_AGENT_URL` (**required**, absolute URL the Agent Card advertises), `DATABASE_URL` (**required**), `DEFAULT_SITE_ID`, `LOG_LEVEL`, `SERVICE_VERSION` |
| `cmd/slotting-projector` | `ANALYTICS_DATABASE_URL` (**required**), `KAFKA_BROKERS` (**required**), `ANALYTICS_CONSUMER_GROUP`, `ADMIN_ADDR` (`:8091`), `LOG_LEVEL`, `SERVICE_VERSION` |
| `cmd/slotting-reports` | `ANALYTICS_DATABASE_URL` (**required**), `HTTP_ADDR` (`:8092`), `LOG_LEVEL`, `SERVICE_VERSION` |

Every variable is tabulated in
[Configuration](docs/docs/operations/configuration.md).

---

## Observability

OpenTelemetry is wired in `internal/adapters/outbound/telemetry` — an
outbound adapter; the domain and application layers never import it.

- **Traces**: a `TracerProvider` exporting over OTLP/gRPC; HTTP handlers run
  inside spans; trace context crosses the outbox onto the Kafka message.
- **Metrics**: a `MeterProvider` over OTLP/gRPC plus a Prometheus scrape
  handler at **`GET /metrics`**: HTTP server metrics, Go runtime metrics,
  and the business counters **`slotting_optimization.slot_plans.decided`**
  (per plan decision, with its outcome) and
  **`slotting_optimization.relocations.observed`** (plan execution progress
  via `StockRelocated`).
- **Logs**: structured `log/slog` JSON on stdout; level from `LOG_LEVEL`.
- Export is non-blocking: with no Collector at
  `OTEL_EXPORTER_OTLP_ENDPOINT`, telemetry is dropped silently and the
  service runs normally.

Details: [Observability](docs/docs/operations/observability.md).

---

## API

REST on `HTTP_ADDR` (default `:8080`), specified in `apis/openapi.yaml`.
Errors are **RFC 7807** `application/problem+json`. Eleven operations over
ten paths:

| Method | Path | Purpose |
|---|---|---|
| POST | `/slot-plans` | Generate a plan (policy `abc-velocity-v1`). **Requires an `Idempotency-Key` header**: the same key + body replays the original response; the same key with a different body is rejected (`idempotency-key-reused`) |
| GET | `/slot-plans` | List plans (filter `site_id`, `state`) |
| GET | `/slot-plans/{planId}` | One plan in full: assignments, moves, unassigned, execution progress |
| POST | `/slot-plans/{planId}/approve` | Record the human approval (Draft → Approved) |
| POST | `/slot-plans/{planId}/reject` | Record the rejection (Draft → Rejected) |
| GET | `/forward-slots` | The CURRENT forward slot map of a site (its Approved plan) |
| GET | `/sku-velocity` | Order-line picks and units per SKU over a window |
| GET | `/healthz`, `/readyz` | liveness; readiness (drain gate + Postgres + Kafka) |
| GET | `/metrics` | Prometheus scrape |
| GET | `/reports/slot-plan-funnel`, `/reports/freshness` | analytics — served by `cmd/slotting-reports`, not `cmd/api` |

Lint the contracts with `spectral lint apis/openapi.yaml --ruleset
.spectral.yaml` and `spectral lint apis/asyncapi.yaml --ruleset
.spectral.asyncapi.yaml`.

### Analytics data product (Slot Plan Funnel)

ADR 0006. `cmd/slotting-projector` consumes
`warehouse.slotting-optimization.analytics` (group
`ANALYTICS_CONSUMER_GROUP`) and is the **only writer** of the separate
analytical database (`ANALYTICS_DATABASE_URL`); `cmd/slotting-reports`
serves `GET /reports/slot-plan-funnel` and `GET /reports/freshness`
read-only on `:8092`.

---

## Helm chart

```bash
docker build -t slotting-optimization:local .
docker run --rm -p 8080:8080 slotting-optimization:local        # api: exits at boot unless DATABASE_URL, KAFKA_BROKERS and the five *_CONSUMER_GROUP are set
docker run --rm -p 8090:8090 --entrypoint /app/mcp slotting-optimization:local   # MCP server: needs DATABASE_URL
docker run --rm -p 8093:8093 --entrypoint /app/a2a slotting-optimization:local   # A2A agent: needs DATABASE_URL and A2A_AGENT_URL
docker run --rm -p 8091:8091 --entrypoint /app/slotting-projector slotting-optimization:local   # needs ANALYTICS_DATABASE_URL and KAFKA_BROKERS
docker run --rm -p 8092:8092 --entrypoint /app/slotting-reports slotting-optimization:local     # needs ANALYTICS_DATABASE_URL
helm lint charts/slotting-optimization -f charts/slotting-optimization/ci/default-values.yaml
bash charts/slotting-optimization/tests/render_test.sh
python3 charts/slotting-optimization/tests/test_service_selectors.py
```

The chart refuses to render without `database.url` or
`database.existingSecret`, `kafka.brokers` and the five
`config.*ConsumerGroup` values, and exposes a dedicated value for every
environment variable `cmd/api` reads (`DEMAND_SITE_ID`, `LOOKBACK_DAYS`,
`FORWARD_ZONE_CODES`, the `*_MODE` (only `kafka`) and `*_CONSUMER_GROUP`
pairs, ...). Four components are off by default: the MCP Deployment
(`mcp.enabled`), the A2A Deployment (`a2a.enabled`; Kong
`/api/a2a/slotting-optimization` rewritten to `/`; `A2A_AGENT_URL` defaults
to the in-cluster Service DNS, overridable with `a2a.agentUrl`), the
analytics projector and reports Deployments (`analytics.enabled`, which also
needs `analytics.url` or `analytics.existingSecret`) and the web remote
(`frontend.enabled`). The chart renders no Kong route for the reports
Service (it has no `reports-route.yaml`). Images and chart publish to
`ghcr.io/iqvo/slotting-optimization`; CI runs the render and selector tests
in the `chart-tests` job and lints the chart in `helm-lint`.

### Running the MCP server in Kubernetes

`mcp.enabled=true` renders the MCP Deployment + Service on port `8090`
(ADR 0005). Four read-only tools served at `/` and `/mcp` (health at
`/healthz`), no auth:

| Tool | Reads |
|---|---|
| `get_slot_plan` | one plan by id: state, policy, window, assignments, moves, unassigned |
| `list_slot_plans` | plans newest first, filter `site_id` and `state` (Draft, Approved, Rejected, Superseded), paged |
| `get_forward_slots` | the CURRENT forward slot map of a site (its Approved plan) |
| `get_sku_velocity` | order-line picks and units per SKU over `window_days`, fastest first, `limit` |

Generating, approving and rejecting a plan stay on REST: the MCP surface has
no write tool and the build fails if one appears.

### Running the A2A agent in Kubernetes

`a2a.enabled=true` renders the A2A Deployment + Service on port `8093`
(ADR 0013). The same read surface as an Agent2Agent agent: JSON-RPC at `/`
and `/a2a`, the Agent Card at `/.well-known/agent-card.json`, health at
`/healthz`, no auth. `A2A_AGENT_URL` (the absolute URL the card advertises)
and `DATABASE_URL` are mandatory. A request is
`{"skill": "<id>", "arguments": {...}}` in a data part; a domain rejection
is answered in-band as an RFC 7807-style `problem` (same statuses and slugs
as REST), never a JSON-RPC error.

| Skill | Reads |
|---|---|
| `forward_slot_map` | the CURRENT forward slot map of a site (`siteId`, default site if omitted) |
| `slot_plan_review` | one plan in full with its execution progress, by `planId`, or the newest plan matching `siteId` / `state` |

Like MCP it has no write skill, wires no write use case and never dials
Kafka; A2A carries delegation only, facts still leave as CloudEvents from
`cmd/api`.

---

## Operator micro-frontend (`web/`)

`slotting_mfe`, a Vite + React Module Federation remote that exposes
`./App` (screens: plan list and plan detail with the approve / reject
workbench, forward slots, SKU velocity). The `warehouse-console` shell
(`origin/develop`) mounts it under `/slotting/*`; the remote's own tests and
comments assume `/slotting-optimization/*` (it uses relative links, so it
works under either). Production asset base `/mfes/slotting-optimization/`;
standalone dev server on `:5194` — the next free port on the fleet's 5181+
ladder after inbound-receiving's `:5193`.

```bash
cd web
npm ci            # needs a sibling warehouse-ui-kit checkout (file:../../warehouse-ui-kit)
npm run dev       # standalone on http://localhost:5194
npm test          # vitest: api-client mapping, idempotency headers, screens
npm run build     # tsc -b && vite build (dist/remoteEntry.js exposes slotting_mfe)
npm run lint      # oxlint
```

Standalone dev calls the API directly at `http://localhost:8080`, so start
`cmd/api` with `CORS_ALLOWED_ORIGINS=http://localhost:5194`. Against the
kind cluster's Kong instead, the remote reads the API origin at runtime from
the console's `/config.json` (`window.__WAREHOUSE_CONFIG__.apiOrigin`), so
it calls `http://localhost:8000/api/slotting-optimization` when served by
the console shell — nothing is baked in at build time. In the cluster the
chart's optional `frontend` component (off by default) serves the built
bundle behind warehouse-infra's Nginx web gateway at
`/mfes/slotting-optimization/`. CI job `web` lints, tests and builds it.

---

## Local development / quality gate

```bash
make check        # fmt, vet, build, lint, tests
make check-all    # check + coverage gate, arch tests, BDD
make integration  # testcontainers Postgres/Kafka tests (needs Docker)
make mutation     # gremlins on internal/domain/slotplan and internal/domain/planning
make vuln         # govulncheck ./...
```

lefthook runs `fmt-check`, `vet` and `lint` on pre-commit and `make check`
on pre-push (`lefthook install` once per clone). `make help` lists every
target.

## Tests

- **Unit, handler and BDD** (`make test`, `make bdd`): no database; godog
  features in `features/`.
- **Integration** (`make integration`, CI job `integration`):
  testcontainers Postgres/Kafka — 23 `*_integration_test.go` suites covering
  the repository contract, use-case wiring, the outbox relay, the CloudEvents
  publisher, every local-copy consumer, the projector's consumer, the
  analytical store, the REST router (RFC 7807, Idempotency-Key replay and
  reuse), MCP and A2A wiring, and the end-to-end analytics path (ADR 0006).
- **Architecture fitness**: `internal/architecture` (`make arch-test`, CI
  `arch-test`).
- **Mutation**: gremlins over the two domain packages — `mutation-fast` on
  every push/PR, the exhaustive `mutation` run on schedule.
- **Web**: the remote has its own vitest suite (CI job `web`).

CI (`.github/workflows/ci.yml`) runs on every push and PR: `lint`,
`guide-lint`, `complexity`, `test` (with the coverage gate), `bdd`,
`integration`, `mutation-fast`, `api-lint`, `vuln` (govulncheck),
`helm-lint`, `chart-tests`, `web`, `arch-test`, plus the scheduled
`mutation` and advisory `drift`; `trivy-scan` builds the image and blocks on
CRITICAL/HIGH CVEs with a fix; `docker-publish` pushes the image and
`release` tags the next version and publishes the chart.

## Architecture Decision Records

The ADR index lives at [docs/adr/index.md](docs/adr/index.md) — 13 decisions
(0001–0013), from the bounded context to the A2A inbound adapter.

## License

MIT — see [LICENSE](LICENSE).
