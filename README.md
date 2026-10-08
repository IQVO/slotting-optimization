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

## Status

Contracts, ADRs and the domain packages are in place. The service (use cases,
persistence, Kafka adapters, HTTP) is built in the next phases.

## Working locally

```bash
make check        # fmt, vet, build, lint, tests
make mutation     # gremlins on internal/domain/slotplan and internal/domain/planning
spectral lint apis/openapi.yaml --ruleset .spectral.yaml
spectral lint apis/asyncapi.yaml --ruleset .spectral.asyncapi.yaml
```

## Study project

This repo, like the rest of the `warehouse-systems` fleet, is a personal
study project exploring Domain-Driven Design, hexagonal architecture, and
AI-agent harness engineering. It is not production software and carries
no support guarantee.
