---
id: runbook
title: Runbook
sidebar_label: Runbook
sidebar_position: 2
---

# Runbook

How `slotting-optimization` is deployed, started, stopped and looked after.
Every statement is taken from the code on `develop`; where the deployment
tooling does not exist yet the page says so.

## Deployment

| Item | State on `develop` |
| --- | --- |
| Binaries | two in one image: `cmd/api` (`/app/api`: REST, the three local-copy consumers and the outbox relay in one process, [Architecture](/docs/overview/architecture)) and `cmd/mcp` (`/app/mcp`: the read-only MCP server, ADR 0005) |
| Web remote | `web/` is the Module Federation remote `slotting_mfe` (exposes `./App`, mounted by warehouse-console on `/slotting/*`). It has its own image, built from `web/Dockerfile` (nginx-unprivileged on `8080`), and the chart's `frontend` component serves it; see [Frontend image](#frontend-image-web-remote) |
| Container image | built from the repository's `Dockerfile`; the `docker-publish` job of `.github/workflows/ci.yml` pushes `ghcr.io/iqvo/slotting-optimization:{latest,<short-sha>}` on a push to `main` |
| Helm chart | `charts/slotting-optimization`: the api Deployment (always), the mcp Deployment and Service (`mcp.enabled`, off by default), an optional HPA, ingress or Gateway API route. The api Deployment renders every variable `cmd/api` reads as explicit `env` and also mounts the chart ConfigMap with `envFrom` (explicit `env` wins); `tests/render_test.sh` (CI job `chart-tests`) fails when a variable a binary reads is not rendered, when the mount disappears, or when the ConfigMap carries a key no binary reads. The `release` job packages and pushes the chart. `warehouse-infra` (`origin/develop`) has no values file, Kong route or ArgoCD application for this context yet (gap fix D10, infra part) |
| Edge route | planned: Kong `:8000` under `/api/slotting-optimization` (ADR 0001). Not wired. |
| Auth | none, by fleet decision (REST and MCP are unauthenticated; `TestNoAuthMiddlewareReintroduced`) |

Until `warehouse-infra` wires it the service runs from source, a locally
built binary ([Quickstart](/docs/overview/quickstart)) or `helm install` of the
chart. The rest of this page describes the process behaviour any deployment
must respect.

### One Deployment, one port

`cmd/api` runs as exactly one Deployment (the binary has no role switch)
with one Service port, `HTTP_ADDR` (default `:8080`), serving:

| Path | Purpose | Answer |
| --- | --- | --- |
| `GET /healthz` | liveness | always `200 {"status":"ok"}` while the process runs |
| `GET /readyz` | readiness | `200 {"status":"ready"}`; `503 {"status":"not_ready"}` once shutdown began; `503` problem `dependency-unavailable` while Postgres or the Kafka broker is unreachable |
| `GET /metrics` | Prometheus scrape | Go runtime and process collectors ([Observability](/docs/operations/observability)) |
| everything else | the REST API of `apis/openapi.yaml` | see [API overview](/docs/api-reference) |

### What readiness waits for

`/readyz` (`internal/adapters/inbound/http/readiness.go`, wired in
`cmd/api/main.go` `newReadiness`) is the shutdown gate **and** a real
dependency check: it is ready only while a Postgres ping succeeds and a Kafka
broker answers a metadata request. The metadata request carries no topic, and
an empty topic list is healthy: the service never requires topics (the relay
writer auto-creates them). A failure is a 503 `dependency-unavailable` whose
`detail` names `postgres` and/or `kafka`. The verdict is cached for 3 s and one
round of checks is bounded at 2 s, so probes cannot hammer either dependency.
Readiness also still implies a working database at boot: the listener only
opens after `buildAdapters` has run the migrations and pinged the pool, so a
process that cannot reach Postgres exits before it serves anything. Kafka is
not a boot dependency (readers and writers dial lazily), but a broker outage
makes the pod unready, and when the relay or a consumer returns on its own the
process exits non-zero (`... stopped unexpectedly: exiting so the orchestrator
restarts the service`) so the pod restarts.

### Boot sequence

1. Logger (`LOG_LEVEL`), then the environment is validated before anything is
   opened or bound (`loadRuntime`): `DATABASE_URL`, `KAFKA_BROKERS` and the
   four `*_CONSUMER_GROUP` are mandatory, `*_MODE` accepts only `kafka` and
   `EVENT_PUBLISHER` only `kafka`. A violation exits 1 with an error naming
   the variable (`service exited with error`).
2. OpenTelemetry (`OTEL_EXPORTER_OTLP_ENDPOINT`, non-blocking).
3. Run the embedded migrations through `MIGRATIONS_DATABASE_URL`, open the
   pool, ping it (`postgres adapters configured`). Both steps retry 5 times
   with 1 s, 2 s, 4 s, 8 s waits (15 s of backoff in total; `internal/bootretry`) because a pod's first
   outbound dial can be reset by its Istio sidecar. The last error is returned
   and the process exits 1 (`service exited with error`).
4. Build the router, then start the four consumers
   (`consumer running` with `consumer`, `topic`, `group_id`, `brokers`), then
   the relay (`outbox relay running` with `publisher`, `interval`,
   `max_attempts`, `topic`).
5. Listen (`http server listening`).

There is no in-memory fallback and no optional consumer ([ADR 0011](/docs/adr/0011-postgres-and-kafka-are-the-only-runtime)).

### Graceful shutdown

On `SIGTERM`/`SIGINT` (`serveUntilSignal` in `cmd/api/main.go`):

1. `/readyz` flips to 503.
2. Sleep `SHUTDOWN_DRAIN_DELAY` (default 5 s) so the endpoints controller stops routing.
3. `http.Server.Shutdown` with a 10 s budget.
4. Cancel the consumers and wait up to 10 s each (an in-flight message
   finishes or rolls back; its offset is not committed, so it is redelivered).
5. Cancel the relay and wait up to 10 s; a pass interrupted mid-way still
   commits the rows it already sent.
6. Close the pool, flush telemetry (5 s).

Give the pod a `terminationGracePeriodSeconds` of at least
`SHUTDOWN_DRAIN_DELAY` + 10 + 10 + 10 + 5 seconds (40 s with the defaults) to
cover the worst case.

## Frontend image (web remote)

`web/` holds the operator UI as a Module Federation remote, not a standalone
app. The files that package it:

| File | Role |
| --- | --- |
| `web/Dockerfile` | multi-stage build: `node:22-bookworm-slim` builds `@warehouse/ui-kit` (a sibling checkout, supplied as the named BuildKit context `uikit`), then `npm ci && npm run build` for the remote, then copies `dist/` into `nginxinc/nginx-unprivileged` |
| `web/nginx.conf` | `listen 8080`; `GET /healthz` answers `200 ok`; `remoteEntry.js` and `index.html` are `Cache-Control: no-cache`; `/assets/` is `public, max-age=31536000, immutable` (one header); unknown paths fall back to `index.html` |
| `web/.dockerignore` | keeps the host `node_modules` and `dist` out of the build context (a macOS `node_modules` would hide the linux native bindings) |

Facts a deployer needs (from `web/vite.config.ts`):

| Item | Value |
| --- | --- |
| Federation container name | `slotting_mfe` |
| Exposed module | `./App` |
| Entry file | `remoteEntry.js` |
| Public base path (build) | `/mfes/slotting-optimization/` |
| Container port | `8080` |
| Dev / preview port | `5194` |
| Image build context | `web/` with `--build-context uikit=<path to warehouse-ui-kit>` |
| Chart component | `frontend` (`frontend.enabled`, default `false`; image `warehouse/slotting-optimization-frontend`) |

Build and check it locally from the repository root:

```bash
docker build --build-context uikit=../warehouse-ui-kit \
  -t warehouse/slotting-optimization-frontend:local web
docker run --rm -p 18194:8080 warehouse/slotting-optimization-frontend:local
curl -s localhost:18194/healthz                                  # ok
curl -s -o /dev/null -w '%{http_code}\n' localhost:18194/remoteEntry.js   # 200
```

The Nginx web gateway of `warehouse-infra` strips the `/mfes/slotting-optimization`
prefix before proxying to the pod, so the pod itself sees root-relative paths.
The pod serves static bytes only: the SPA reaches the REST API at
`${apiOrigin}/api/slotting-optimization`, with `apiOrigin` read at runtime
from the console's `/config.json`. Kong never serves these assets. The CI job
`web` runs lint, `tsc -b`, the vitest suite and the production build, then
builds this image and requests `/healthz` and `remoteEntry.js` from it.

## Migrations

| Fact | Value |
| --- | --- |
| Files | `internal/adapters/outbound/postgres/migrations/0001_slotting_schema.{up,down}.sql` and `0002_slot_plan_move_executions.{up,down}.sql` (additive, ADR 0010), embedded with `//go:embed` |
| Runner | golang-migrate (`postgres.RunMigrations`), `Up()`; `ErrNoChange` is success |
| When | at every boot, before the pool opens, only when `DATABASE_URL` is set |
| DSN | `MIGRATIONS_DATABASE_URL`, falling back to `DATABASE_URL`. Use a direct connection: the migrate advisory lock does not survive PgBouncer transaction pooling. |
| Concurrency | several replicas booting together serialise on the advisory lock; the losers find `ErrNoChange` |
| Bookkeeping | `schema_migrations` (golang-migrate's table) |
| Tables created | `slot_plans`, `slot_plan_assignments`, `slot_plan_moves`, `slot_plan_unassigned`, `demand_lines`, `product_profiles`, `zones`, `slots`, `outbox_events`, `processed_events`, `idempotency_keys` (0001); `slot_plan_move_executions` (0002) ([Entity-relationship](/docs/ddd/entity-relationship)) |

There is no separate migrate command or Job. A failed migration leaves
`schema_migrations.dirty = true` and every later boot fails with the same error
until it is fixed by hand ([Troubleshooting](/docs/operations/troubleshooting)).

## Kafka

One fleet broker (in-cluster, external access `localhost:9092`). Every
message is a CloudEvents 1.0 structured-mode JSON value with the header
`content-type: application/cloudevents+json; charset=UTF-8`
(`internal/adapters/kafka/cloudevents`).

### Produced

| Topic | Types | Key | Producer settings |
| --- | --- | --- | --- |
| `warehouse.slotting-optimization.events` | `com.warehouse.wms.slotting-optimization.slotplan.SlotPlanGenerated`, `...SlotPlanApproved`, `...SlotPlanRejected` | the plan id | outbox relay, `EVENT_PUBLISHER=kafka`; `RequireAll` acks, Hash balancer, 10 ms batch timeout, auto topic creation (`outbound/kafka/relay_sink.go`) |

### Consumed

| Topic | Types acted on | Mode / group variables | Claim name in `processed_events` | DLQ topic |
| --- | --- | --- | --- | --- |
| `warehouse.order-management.events` | `com.warehouse.wes.order-management.siteskudemand.SiteSkuDemandChanged` | `DEMAND_MODE`, `DEMAND_CONSUMER_GROUP` | `slotting-demand` | `warehouse.order-management.events.dlq` |
| `warehouse.product-master.events` | `com.warehouse.wms.product-master.product.ProductClassified`, `...ProductDimensionsDeclared`, `...ProductMeasured` | `PRODUCT_MODE`, `PRODUCT_CONSUMER_GROUP` | `slotting-product` | `warehouse.product-master.events.dlq` |
| `warehouse.facility.events` | `com.warehouse.wms.facility-layout.zone.ZoneRegistered`, `com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered`, `...LocationSlotDecommissioned` | `LAYOUT_MODE`, `LAYOUT_CONSUMER_GROUP` | `slotting-layout` | `warehouse.facility.events.dlq` |
| `warehouse.inventory.events` | `com.warehouse.wms.inventory-storage.stock.StockRelocated` (ADR 0010) | `RELOCATION_MODE`, `RELOCATION_CONSUMER_GROUP` | `slotting-relocation` | `warehouse.inventory.events.dlq` |

There are no default group ids: the deployment chooses them (the chart's
`config.relocationConsumerGroup`, e.g. `slotting-optimization-relocation`).
Other types on the four topics are ignored silently. A new relocation group
replays `warehouse.inventory.events` from the start; only `StockRelocated`
is acted on, and a replayed relocation is recorded once per
`relocation_id`.

### Delivery semantics

- **Consumers: at-least-once, atomic effect.** `FetchMessage`, handle, then
  `CommitMessages` (no `CommitInterval`, so the commit is synchronous). The
  claim of `(consumer, CloudEvents id)` and the upsert run in one transaction
  (`usecases.Intake`), so a redelivery is a no-op (`outcome=duplicate`).
- **Deterministic problems are skipped**: not a CloudEvent, malformed `data`,
  or values the use case refuses (`ErrInvalidEvent`) are logged at WARN and
  committed past. They never reach the DLQ. **Exception (ADR 0010):** a
  `StockRelocated` whose data cannot be decoded or misses a required field is
  dead-lettered at once to `warehouse.inventory.events.dlq` (no retry), then
  committed past. A relocation that matches no slot-plan move is not a
  problem: it is claimed, logged (`relocation matched no slot-plan move`,
  with `reason`) and counted.
- **Shutdown is never a dead letter**: a cancelled context returns before any
  dead-letter call and leaves the offset uncommitted.
- **Transient failures** retry the same message (200 ms doubling to 5 s) up to
  5 handling attempts, then the message goes to `<topic>.dlq` with its key,
  value and headers plus `x-dlq-source-topic`, `x-dlq-source-partition`,
  `x-dlq-source-offset`, `x-dlq-error` and `x-dlq-failed-at`. The DLQ write
  itself retries forever, so the partition blocks rather than drops a message.
  A failed offset commit is also retried forever.
- **Relay: at-least-once, ordered up to the attempt cap.** Each pass claims
  up to 100 unpublished rows `FOR UPDATE SKIP LOCKED` in id order (skipping
  rows whose `attempts` reached `OUTBOX_MAX_ATTEMPTS`, default 20), sends them
  one at a time, marks each `published_at`, and stops at the first failure
  (recording `attempts` and `last_error` on that row) so a later event never
  overtakes an earlier one that is still being retried. The CloudEvents id is
  minted at encode time and stored, so a resend carries the same id. A row
  that reached the cap is overtaken by later rows, including rows of the same
  plan ([ADR 0008](/docs/adr/0008-outbox-relay-attempt-cap)).

### Outbox relay attempt cap

A row the broker never accepts (for example a `SlotPlanApproved` over the
broker's message size limit) fails every pass. After `OUTBOX_MAX_ATTEMPTS`
failures (default 20, about 20 s with the 1 s interval) the relay logs once
at ERROR

```
outbox relay: row reached the attempt cap and is excluded from future claims; re-arm it with attempts = 0 after fixing the cause
```

with `outbox_id`, `topic`, `event_type`, `key`, `attempts`, `max_attempts`
and `error`, and stops claiming the row. The rows behind it publish again.
The row is kept, not deleted. To find excluded rows (with the default cap):

```sql
SELECT id, topic, event_type, key, attempts, last_error
FROM outbox_events
WHERE published_at IS NULL AND attempts >= 20
ORDER BY id;
```

Fix the cause (broker size limit, topic, ACLs), then re-arm the row; the next
pass sends it with its original CloudEvents id:

```sql
UPDATE outbox_events SET attempts = 0 WHERE id = 42;
```

A broker outage longer than the cap excludes the rows at the head of the
queue one per pass; re-arm all of them with
`UPDATE outbox_events SET attempts = 0 WHERE published_at IS NULL;` once the
broker is back. `OUTBOX_MAX_ATTEMPTS=0` restores unlimited retries (the
relay then stalls behind a poison row instead).

## Housekeeping

There is **no sweeper** on `develop`. Three tables only grow:

| Table | Grows with | Safe cleanup |
| --- | --- | --- |
| `outbox_events` | every plan decision | rows with `published_at IS NOT NULL` are never read again: `DELETE FROM outbox_events WHERE published_at < now() - interval '7 days';` |
| `idempotency_keys` | every `POST /slot-plans` | `idx_idempotency_keys_created_at` exists for this: `DELETE FROM idempotency_keys WHERE created_at < now() - interval '1 day';` (a client retrying with an older key then gets a fresh plan) |
| `processed_events` | every consumed event | only delete claims older than the topic's retention; a younger claim protects against a redelivery |

`slot_plans` keeps every plan by design (the audit trail of decisions).

## Scaling

- **Replicas.** Safe to run more than one: the relay's `SKIP LOCKED` claim
  keeps two relays off the same row; consumers in the same group split the
  partitions; the idempotency middleware serialises on the
  `idempotency_keys` primary key; approvals of one site race on the partial
  unique index `uq_slot_plans_one_approved_per_site` (the loser gets
  `409 approved-plan-conflict`). The chart has an optional HPA for the api
  Deployment (`autoscaling.api`, off by default).
- **Connections.** `MaxConns = 10` per process, every query bounded by
  `statement_timeout = 5s`. Size Postgres for `replicas x 10`.
- **Plan generation** reads the whole demand window and every forward slot
  of the site in one transaction; a very large site makes `POST /slot-plans`
  the slowest call, and the 5 s statement timeout is its ceiling.

## Routine procedures

### Rebuild a local copy from the producers' topics

The claims are keyed by consumer name, not by group id, so a new group alone
replays nothing (every event comes back as `duplicate`). To rebuild, with
that consumer stopped:

```sql
-- demand copy (use slotting-product / product_profiles, or
-- slotting-layout / zones + slots for the others)
BEGIN;
DELETE FROM processed_events WHERE consumer = 'slotting-demand';
TRUNCATE demand_lines;
COMMIT;
```

then start it with a **new** `DEMAND_CONSUMER_GROUP`: a group with no
committed offset starts at the first offset (kafka-go's default
`FirstOffset`) and reads whatever the topic still retains.

### Re-publish plan events

```sql
UPDATE outbox_events SET published_at = NULL, last_error = NULL, attempts = 0
WHERE subject = 'plan-...';
```

The relay sends them again with their original CloudEvents ids, so
consumers that dedupe by id see duplicates, not new events.

### Execution progress (ADR 0010)

1. The StockRelocated consumer is always on (ADR 0011): it needs
   `config.relocationConsumerGroup` (for example
   `slotting-optimization-relocation`, set in warehouse-infra's
   `helm-values/slotting-optimization.yaml`); the chart refuses to render
   without it, and `RELOCATION_MODE` accepts only `kafka`.
2. The consumer starts at the first offset of `warehouse.inventory.events`
   and records every `StockRelocated` that executes a move of a known plan.
3. Check: `GET /slot-plans/{planId}` shows `execution.movesExecuted` growing;
   `slotting_optimization.relocations.observed` counts `applied` /
   `unmatched` / `duplicate`.

Progress per plan, straight from the table:

```sql
SELECT plan_id, count(DISTINCT move_seq) AS moves_executed, sum(quantity) AS units, max(executed_at) AS last_executed_at
FROM slot_plan_move_executions GROUP BY plan_id ORDER BY last_executed_at DESC;
```

To rebuild it, stop the consumer, then
`DELETE FROM processed_events WHERE consumer = 'slotting-relocation'; TRUNCATE slot_plan_move_executions;`
in one transaction and start it with a new `RELOCATION_CONSUMER_GROUP`.

### Replay a dead-lettered message

There is no tool. Read the message from `<topic>.dlq`, check `x-dlq-error`,
fix the cause, and produce the original value (with its key) back onto the
source topic. Its claim was rolled back with the failed transaction, so it
is applied normally. A `StockRelocated` on `warehouse.inventory.events.dlq`
was malformed at the producer (`x-dlq-error` names the missing field):
replaying the same bytes dead-letters it again, so raise it with
inventory-storage instead.

### Rotate database credentials

The DSNs are read once at boot. Change `DATABASE_URL` (and
`MIGRATIONS_DATABASE_URL`) and restart; the drain sequence above makes a
rolling restart lossless.
