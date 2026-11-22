# CLAUDE.md — slotting-optimization

`slotting-optimization` is the forward-pick slotting planner (`wms` tier) of
the `warehouse-systems` fleet (GitHub org `IQVO`, Go module
`github.com/claudioed/slotting-optimization`). It proposes which SKUs get a
forward pick slot and which slot (policy `abc-velocity-v1`: rank by order-line
picks, ABC classes, eligibility by hazmat, temperature and capacity,
stickiness to limit churn) and records the human decision on the proposal
(Draft, then Approved or Rejected). It owns the `SlotPlan` aggregate and the
pure `planning.Planner` domain service. It does NOT execute moves: an Approved
plan is published with its full assignment map and moves, and the moves run on
the floor as WES relocation work (ADR 0010). It ships five binaries:
`cmd/api` (REST + outbox relay + the five local-copy consumers), `cmd/mcp`
(read-only MCP), `cmd/a2a` (read-only A2A agent), `cmd/slotting-projector`
and `cmd/slotting-reports` (analytics read side), plus the `web/` remote.
Agent authority: every MCP tool and A2A skill declares a level (L1 read today;
ADR 0015).

## Hard rules (each one cost an incident or is fitness-tested)

1. **Hexagonal dependency rule.** `internal/domain` depends on nothing internal
   but domain; `internal/application` only on domain and application; inbound
   and outbound adapters never depend on each other; only `cmd/` wires layers.
   `make arch-test` enforces it (`internal/architecture`).
2. **CloudEvents 1.0 is MANDATORY** on every Kafka message produced or
   consumed (structured mode, no flat envelope, no dual-write, no envelope
   toggle; `TestNoEventEnvelopeToggleOrFlatEnvelope`, `TestCloudEventsOnly`).
   Build/decode ONLY through `internal/adapters/kafka/cloudevents/`; `type` is
   `com.warehouse.wms.slotting-optimization.slotplan.<EventName>`; a breaking
   payload change is a new `.v2` type. Catalogue:
   `docs/adr/0004-cloudevents-envelope-and-type-catalogue.md`; payloads:
   `apis/asyncapi.yaml`; rules: `.claude/rules/integration-events.md`.
3. **Events leave through the transactional outbox only**: the plan save and
   the encoded events commit in one unit of work; the relay in `cmd/api`
   publishes, with an attempt cap (ADR 0008, `TestOutboxRelaysPassAttemptCap`)
   and the W3C trace context carried across (ADR 0009).
4. **No calls to sibling contexts, ever.** Demand, product, pack-level, layout
   and relocation facts are event-fed LOCAL COPIES (ADR 0003, ADR 0012) from
   `warehouse.order-management.events`, `warehouse.product-master.events`,
   `warehouse.facility.events` and `warehouse.inventory.events`. All five
   consumers are always on (`permissive`/`off` modes are boot errors) and each
   group id comes from its env var (`TestKafkaConsumerGroupNeverHardcodedInline`).
5. **SlotPlan invariants** (ADR 0002): a slot holds at most one SKU and a SKU
   is in at most one slot; `Approve`/`Reject` only from `Draft`, `Supersede`
   only from `Approved`; Approved, Rejected and Superseded plans never change.
   At most ONE Approved plan per site: approving supersedes the previous one
   in the same unit of work, backed by a partial unique index.
6. **The planner is pure**: no I/O, no clock reads, same input in any order
   gives the same output. `internal/domain/slotplan` and
   `internal/domain/planning` are under the blocking mutation gate; write
   decision logic as `if` chains with early returns (gremlins under-reports
   tagless `switch`).
7. **Every POST is behind the `Idempotency-Key` middleware** (ADR 0014,
   `internal/adapters/inbound/http/idempotency.go`); same key + different
   request is 409 `idempotency-key-reused`.
8. **No auth layer on REST, MCP or A2A** (fleet-wide revert 2026-09-11;
   `TestNoAuthMiddlewareReintroduced`). MCP and A2A are read-only and
   additive (ADR 0005, ADR 0013).
9. **Postgres and Kafka are the only runtime** (ADR 0011): no in-memory mode,
   no log sink (`EVENT_PUBLISHER=log` is a boot error;
   `TestRuntimeBinaryHasNoInMemoryAdapters`). Integration tests use
   testcontainers.
10. **Analytics is a separate read side** (ADR 0006): own topic
    `warehouse.slotting-optimization.analytics`, own database,
    `cmd/slotting-projector` is its only writer.
11. **An ADR comes first** for a new cross-context contract, a new consumed
    topic, a new planning policy, a write surface for agents or any auth.
    ADRs live in `docs/adr/`.

## Commands

```bash
make check-fast    # fmt-check + vet + arch-test + tests of changed packages: run before saying "done"
make check         # fmt-check vet build lint test
make check-all     # check + coverage (90% gate on domain+application+analytics) + arch-test + bdd
make integration   # real Postgres/Kafka via testcontainers (needs Docker)
make mutation      # gremlins on slotplan and planning (CI job mutation-fast, blocking)
make mutation-full # gremlins on internal/domain (CI job mutation, weekly)
make vuln          # govulncheck
make guide-lint    # these guides: skills load, references resolve, context budget
make harness-test  # unit tests of the agent hooks
```

Run locally: `docker compose up -d --wait` (Postgres `:55432`, Kafka
`:9092`), then `DATABASE_URL=... KAFKA_BROKERS=...` plus the five
`DEMAND_`/`PRODUCT_`/`LAYOUT_`/`RELOCATION_`/`PACK_LEVEL_CONSUMER_GROUP`
vars, `go run ./cmd/api` (REST `:8080`). `cmd/mcp` (`:8090`) and `cmd/a2a`
(`:8093`, also `A2A_AGENT_URL`) need `DATABASE_URL`;
`cmd/slotting-projector` (`:8091`) and `cmd/slotting-reports` (`:8092`) need
`ANALYTICS_DATABASE_URL`. Every variable: `docs/docs/operations/configuration.md`.

## Where things are written down (read before editing the matching area)

- `.claude/rules/domain-model.md`: ubiquitous language, the aggregate, the
  planner, events, use cases. `.claude/rules/rest-api.md`: routes and error
  slugs. `.claude/rules/integration-events.md`: published/consumed events,
  outbox. `.claude/rules/mcp.md`: the MCP adapter. `.claude/rules/chart.md`:
  image and Helm chart.
- `.claude/rules/fleet/*.md`: fleet-wide rules copied from the harness
  template; never edit them here.
- `docs/adr/`: decisions (index: `docs/adr/index.md`). `apis/openapi.yaml`,
  `apis/asyncapi.yaml`: contracts. Operating docs: `docs/docs/operations/`
  (configuration, runbook, observability, troubleshooting),
  `docs/docs/development/testing.md`. The REST pages under
  `docs/docs/api-reference/rest/` are generated (`npm run gen-api-docs` in
  `docs/`, checked by the `docs-api-drift` CI job): never hand-edit them.
  `HARNESS.md`: what each sensor runs and why. `INCIDENTS.md`: the fleet
  incident log.
- REST guideline adoption: ADR 0014; the Spectral `rest-guideline` ruleset
  (`.spectral.yaml`) runs in the `api-lint` CI job.

harness-template: v4
(hooks, guide-lint).

<!-- harness:scoped-rules:start (generated by tools/migrate_v3.py in warehouse-harness-template; do not hand-edit) -->
## Scoped rules and harness

Claude Code loads each rule below automatically when you touch the matching paths. OpenCode and Codex do NOT: read the rule BEFORE editing matching files.

| When touching | Read |
|---|---|
| `Dockerfile`, `.dockerignore`, `charts/slotting-optimization/**` | `.claude/rules/chart.md` |
| `internal/domain/**`, `internal/application/**`, `features/**` | `.claude/rules/domain-model.md` |
| `internal/adapters/**/kafka/**`, `internal/adapters/outbound/events/**`, `apis/asyncapi*` | `.claude/rules/integration-events.md` |
| `internal/adapters/inbound/mcp/**`, `cmd/mcp/**`, `charts/slotting-optimization/templates/mcp-*.yaml` | `.claude/rules/mcp.md` |
| `internal/adapters/inbound/http/**`, `apis/openapi*.yaml`, `apis/openapi/**` | `.claude/rules/rest-api.md` |

Hooks (`scripts/harness/hook.py`, wired for Claude Code, Codex and OpenCode) block pushes to develop/main, `--no-verify`, bare `rm -rf`, and edits to generated files, and feed gofmt/vet findings back after each edit. Before saying "done" run `make check-fast`; the full gate is `make check-all`. `HARNESS_OFF=1` disables the hooks when debugging the harness itself.
<!-- harness:scoped-rules:end -->
