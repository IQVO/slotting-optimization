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
| Binaries | one, `cmd/api`: REST, the three local-copy consumers and the outbox relay in one process ([Architecture](/docs/overview/architecture)) |
| Container image | the `docker-publish` job of `.github/workflows/ci.yml` pushes `ghcr.io/iqvo/slotting-optimization:{latest,<short-sha>}` on a push to `main`, built from `.` with Buildx. **There is no `Dockerfile` in the repository yet**, so that job cannot succeed until one is added. |
| Helm chart | the `release` job packages `charts/slotting-optimization`. **There is no `charts/` directory yet**, and `warehouse-infra` (`origin/develop`) has no values file, Kong route or ArgoCD application for this context. ADR 0001 records the wiring as a later "wiring wave". |
| Edge route | planned: Kong `:8000` under `/api/slotting-optimization` (ADR 0001). Not wired. |
| Auth | none, by fleet decision (REST and MCP are unauthenticated; `TestNoAuthMiddlewareReintroduced`) |

So today the service runs from source or a locally built binary
([Quickstart](/docs/overview/quickstart)). The rest of this page describes the
process behaviour any deployment must respect.

### One Deployment, one port

When a chart is added it needs exactly one Deployment (the binary has no
role switch) and one Service port, `HTTP_ADDR` (default `:8080`), serving:

| Path | Purpose | Answer |
| --- | --- | --- |
| `GET /healthz` | liveness | always `200 {"status":"ok"}` while the process runs |
| `GET /readyz` | readiness | `200 {"status":"ready"}`; `503 {"status":"not_ready"}` once shutdown began |
| `GET /metrics` | Prometheus scrape | Go runtime and process collectors ([Observability](/docs/operations/observability)) |
| everything else | the REST API of `apis/openapi.yaml` | see [API overview](/docs/api-reference) |

### What readiness waits for

`/readyz` is a shutdown gate, not a dependency check
(`internal/adapters/inbound/http/readiness.go`): its zero value is *ready*,
and only `serveUntilSignal` flips it. It never probes Postgres or Kafka.
Readiness still implies a working database, because the listener only opens
after `buildAdapters` has run the migrations and pinged the pool: a process
that cannot reach Postgres exits before it serves anything. Kafka is never a
boot dependency: readers and writers dial lazily, so a broker outage shows up
as consumer and relay errors in the logs, not as an unready pod.

### Boot sequence

1. Logger (`LOG_LEVEL`), then OpenTelemetry (`OTEL_EXPORTER_OTLP_ENDPOINT`, non-blocking).
2. With `DATABASE_URL` set: run the embedded migrations through
   `MIGRATIONS_DATABASE_URL`, open the pool, ping it. Both steps retry 5 times
   with 1 s, 2 s, 4 s, 8 s waits (`internal/bootretry`) because a pod's first
   outbound dial can be reset by its Istio sidecar. The last error is returned
   and the process exits 1 (`service exited with error`).
3. Without `DATABASE_URL`: in-memory adapters (log line
   `DATABASE_URL not configured; using in-memory adapters`).
4. Build the router, then start every consumer whose `*_MODE=kafka`
   (`consumer running` with `consumer`, `topic`, `group_id`, `brokers`), then
   the relay (`outbox relay running` with `publisher`, `interval`, `topic`).
5. Listen (`http server listening`).

A misconfiguration refuses to boot before the listener opens: an unknown
`EVENT_PUBLISHER` or `*_MODE`, a `*_MODE=kafka` without its
`*_CONSUMER_GROUP`, or Kafka needed without `KAFKA_BROKERS`.

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

## Migrations

| Fact | Value |
| --- | --- |
| Files | `internal/adapters/outbound/postgres/migrations/0001_slotting_schema.{up,down}.sql`, embedded with `//go:embed` |
| Runner | golang-migrate (`postgres.RunMigrations`), `Up()`; `ErrNoChange` is success |
| When | at every boot, before the pool opens, only when `DATABASE_URL` is set |
| DSN | `MIGRATIONS_DATABASE_URL`, falling back to `DATABASE_URL`. Use a direct connection: the migrate advisory lock does not survive PgBouncer transaction pooling. |
| Concurrency | several replicas booting together serialise on the advisory lock; the losers find `ErrNoChange` |
| Bookkeeping | `schema_migrations` (golang-migrate's table) |
| Tables created | `slot_plans`, `slot_plan_assignments`, `slot_plan_moves`, `slot_plan_unassigned`, `demand_lines`, `product_profiles`, `zones`, `slots`, `outbox_events`, `processed_events`, `idempotency_keys` ([Entity-relationship](/docs/ddd/entity-relationship)) |

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

There are no default group ids: the deployment chooses them. Other types on
the three topics are ignored silently.

### Delivery semantics

- **Consumers: at-least-once, atomic effect.** `FetchMessage`, handle, then
  `CommitMessages` (no `CommitInterval`, so the commit is synchronous). The
  claim of `(consumer, CloudEvents id)` and the upsert run in one transaction
  (`usecases.Intake`), so a redelivery is a no-op (`outcome=duplicate`).
- **Deterministic problems are skipped**: not a CloudEvent, malformed `data`,
  or values the use case refuses (`ErrInvalidEvent`) are logged at WARN and
  committed past. They never reach the DLQ.
- **Transient failures** retry the same message (200 ms doubling to 5 s) up to
  5 handling attempts, then the message goes to `<topic>.dlq` with its key,
  value and headers plus `x-dlq-source-topic`, `x-dlq-source-partition`,
  `x-dlq-source-offset`, `x-dlq-error` and `x-dlq-failed-at`. The DLQ write
  itself retries forever, so the partition blocks rather than drops a message.
  A failed offset commit is also retried forever.
- **Relay: at-least-once, ordered.** Each pass claims up to 100 unpublished
  rows `FOR UPDATE SKIP LOCKED` in id order, sends them one at a time, marks
  each `published_at`, and stops at the first failure (recording `attempts`
  and `last_error` on that row) so a later event never overtakes an earlier
  one. The CloudEvents id is minted at encode time and stored, so a resend
  carries the same id.

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
  `409 approved-plan-conflict`). There is no HPA because there is no chart.
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
UPDATE outbox_events SET published_at = NULL, last_error = NULL
WHERE subject = 'plan-...';
```

The relay sends them again with their original CloudEvents ids, so
consumers that dedupe by id see duplicates, not new events.

### Replay a dead-lettered message

There is no tool. Read the message from `<topic>.dlq`, check `x-dlq-error`,
fix the cause, and produce the original value (with its key) back onto the
source topic. Its claim was rolled back with the failed transaction, so it
is applied normally.

### Rotate database credentials

The DSNs are read once at boot. Change `DATABASE_URL` (and
`MIGRATIONS_DATABASE_URL`) and restart; the drain sequence above makes a
rolling restart lossless.
