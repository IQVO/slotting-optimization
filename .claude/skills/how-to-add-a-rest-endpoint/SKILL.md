---
name: how-to-add-a-rest-endpoint
description: Add or change a REST endpoint in this service in the fleet's hexagonal order (domain invariant, use case, port, HTTP adapter, apis/openapi.yaml, godog scenario). Use when touching internal/adapters/inbound/http, apis/openapi.yaml, or exposing a use case over HTTP.
---

# How to add a REST endpoint

Use when asked to add a new REST use case/endpoint to this service. Follow
this order — domain first, adapter last — never the reverse; writing the
HTTP handler before the domain invariant it enforces produces handlers
that validate nothing and use cases that get bypassed.

This walks the path `POST /slot-plans/{planId}/approve` takes
(`internal/domain/slotplan/plan.go` `Approve` →
`internal/application/usecases/decide_plan.go` `ApprovePlan` →
`internal/adapters/inbound/http/handlers.go` `handleApproveSlotPlan`) as the
concrete worked example — read those files alongside this guide. The read
side is `GET /forward-slots` (`queries.go` `ListForwardSlots` →
`handleListForwardSlots`).

## 1. Domain first: does an invariant already exist, or do you need one?

Check `internal/domain/slotplan/` for the rule this endpoint enforces. A REST
endpoint should almost never contain business logic itself — it decodes a
request, calls a use case, encodes the result. If the operation needs a new
domain rule (e.g. "only a Draft can be approved", `slotplan.ErrNotDraft`),
add it to the aggregate in `internal/domain/slotplan/`, with its own
table-driven unit test, BEFORE touching the application or adapter layers.
Ranking and placement rules belong to `internal/domain/planning`.

## 2. Application: define the use case

Add a file in `internal/application/usecases/` (one concern per file, like
`generate_plan.go`, `decide_plan.go`, `queries.go`). Shape, from
`decide_plan.go`:

- a struct embedding `Writer` (plans repository, outbox, `EventEncoder`,
  `UnitOfWork`, `Clock`) for a write, or holding only the read port
  (`Plans ports.SlotPlanRepository`) for a query — driven ports only, never a
  concrete adapter;
- `Handle(ctx, ...)` that loads the aggregate, calls its own method to apply
  the rule (never inline the invariant here), and for a write saves the plan
  and enqueues its events through `Writer` inside ONE `ports.UnitOfWork`, so
  the row and the outbox messages commit together;
- time only from `ports.Clock`, ids only from `ports.IDGenerator`.

Ports are interfaces ONLY (`internal/application/ports/`;
`internal/architecture/` fails CI on a struct or function there).

Unit-test the use case against the fakes in
`internal/application/usecases/fakes_test.go` (success path AND the
domain-rule failure path); keep assertions flat with the `eq`/`noErr`/`isErr`
helpers there, because the linters bound test complexity.

## 3. Adapter: wire the HTTP handler

In `internal/adapters/inbound/http/`:

1. `dto.go` — the request/response structs (JSON tags live ONLY here; domain
   types never carry them).
2. `server.go` — add the use case field to `Server` and the route in
   `NewRouter`; `handlers.go` — the handler: decode and validate (path ids via
   `planIDParam`, bodies via `decodeOptionalJSON`, which rejects unknown
   fields), call `Handle`, then `writeJSON`.
3. `errors.go` — map each new use-case/domain error to an RFC 7807 problem in
   `problemFor` (status, slug, title). Reuse an existing slug before adding
   one; an unmapped error becomes the generic 500 `internal-error` and never
   leaks its message. The type URI is
   `https://errors.slotting-optimization.warehouse-systems.dev/<slug>`.
4. Wire the new use case in `cmd/api/main.go` `buildServer`.

A resource-creating POST (`POST /slot-plans`) additionally goes through the
fleet `Idempotency-Key` middleware (`idempotency.go`, wired as
`Server.Idempotency` over Postgres). Do not add another POST that creates a
resource without it; a state transition like approve answers 409 when
repeated instead.

Write the httptest in `internal/adapters/inbound/http/` against the in-memory
fixture in `helpers_test.go` (`newFixture`, `expectProblem`): one success
path and one test per error slug the endpoint can produce. A behaviour that
needs the real `Idempotency-Key` store goes in
`idempotency_integration_test.go` (build tag `integration`, testcontainers).

## 4. Contract: update OpenAPI

Add the path to `apis/openapi.yaml` (request/response schemas and the RFC 7807
problem response for each error case — copy the shape of the
`/slot-plans/{planId}/approve` entry), then run
`spectral lint apis/openapi.yaml --ruleset .spectral.yaml --fail-severity=warn`
(the `api-lint` CI job). The contract is PINNED: change it only with the
matching code in the same PR. Update `.claude/rules/rest-api.md` if the
endpoint list changes.

## 5. Behaviour: add a godog scenario

If this endpoint is user-facing behaviour, add a scenario to the matching file
in `features/` (`generate_plan.feature`, `approve_and_reject.feature`,
`velocity_and_listing.feature`). The steps are in `features_test.go`; the suite
drives the real chi router over in-memory adapters and feeds the local copies
through the real consumers with raw CloudEvents.

## 6. Verify before opening the PR

```bash
make check       # fmt-check vet build lint test
make check-all   # + coverage (90% gate) + arch-test + bdd
make integration # real Postgres/Kafka via testcontainers (needs Docker)
```

`make coverage` gates `./internal/domain/...,./internal/application/...` at
90% — a new use case with no test on its failure path is the most common way
to miss this gate.
