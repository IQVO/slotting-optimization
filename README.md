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

The service is built: `cmd/api` serves the REST API of `apis/openapi.yaml`,
runs the three local-copy consumers (`DEMAND_MODE`, `PRODUCT_MODE`,
`LAYOUT_MODE`) and the transactional-outbox relay (`EVENT_PUBLISHER`), on
Postgres (`DATABASE_URL`) or in memory. It is not wired into the kind
cluster yet: there is no `Dockerfile`, no Helm chart and no Kong route
(see the [Runbook](docs/docs/operations/runbook.md)). No context consumes
its events yet, and executing the moves of an approved plan is a planned,
unbuilt edge.

## Working locally

```bash
make check        # fmt, vet, build, lint, tests
make check-all    # check + coverage gate + arch-test + bdd
make integration  # testcontainers Postgres and Kafka (Docker only)
make mutation     # gremlins on internal/domain/slotplan and internal/domain/planning
go run ./cmd/api  # in-memory mode on :8080 (set DEFAULT_SITE_ID to omit siteId)
spectral lint apis/openapi.yaml --ruleset .spectral.yaml
spectral lint apis/asyncapi.yaml --ruleset .spectral.asyncapi.yaml
```

Full walkthrough, including a local Postgres, seed data and the fleet
Kafka broker on `localhost:9092`:
[Quickstart](docs/docs/overview/quickstart.md).

## Study project

This repo, like the rest of the `warehouse-systems` fleet, is a personal
study project exploring Domain-Driven Design, hexagonal architecture, and
AI-agent harness engineering. It is not production software and carries
no support guarantee.
