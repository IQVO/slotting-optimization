# ADR 0006: Analytics read side: an analytics stream, a projector and a slot-plan funnel report over a separate analytical database

## Status

Accepted (2026-10-17). This ADR fills the number ADR 0004 reserved for the
analytics read side. Additive: the OLTP API, the integration topic
`warehouse.slotting-optimization.events` and its payloads are unchanged (the
integration encoder golden tests pass unchanged). No OLTP migration is
needed (see section 3). Adds a second, separate database.

## Context

Every OLTP bounded context of the fleet ships an analytics read side: a
separate analytical Postgres fed by a **projector** that consumes the
context's own analytics topic, and a read-only **reports** binary over that
database (warehouse-planning, product-master, inbound-receiving ADR 0006,
process-path-management ADR 0007, ...). slotting-optimization was the last
deployed context without one. ADR 0004 reserved the name
`warehouse.slotting-optimization.analytics`; this ADR fills it. It mirrors
inbound-receiving ADR 0006 (the reference implementation) and records where
this service differs.

The question the data product answers is the **review funnel of the slot
plans**: how many plans are proposed per day, how many are approved versus
rejected, how long an operator takes from proposal to approval, and how
often an approval replaces the site's standing plan. These are the
operational KPIs of the planning-review cadence.

## Decision

### 1. What is projected

All three published events
(`com.warehouse.wms.slotting-optimization.slotplan.SlotPlanGenerated`,
`...SlotPlanApproved`, `...SlotPlanRejected`) are ALSO written to
`warehouse.slotting-optimization.analytics` as CloudEvents 1.0 with the
**same `type` and the same `id` per occurrence** as the integration message,
the same `subject` and Kafka key (ADR 0004 table), and
`dataschema=urn:warehouse:slotting-optimization:analytics:<EventName>:v1`.
The DLQ is `warehouse.slotting-optimization.analytics.dlq`.

### 2. Payloads: equal to the integration payloads

The analytics payload of every event is byte-for-byte the integration
payload (the encoder's row builders are shared, so a payload published on
one stream can never be missing from the other). Nothing is added: the
funnel needs the plan id, the site and the CloudEvents `time`, all already
in the integration payload; `supersedes_plan_id` on `SlotPlanApproved`
carries the supersession. A later analytics-only field would be additive and
documented in `apis/asyncapi.yaml`.

### 3. One transaction, two topics, one id

`FanoutEncoder` (what `cmd/api` hands the use cases) turns every domain
event into **two outbox rows, the integration row first and the analytics
row second, under one CloudEvents id minted once and persisted with both**.
The use case's single `ports.UnitOfWork` inserts them with the aggregate
rows, so they commit or roll back together; a relay retry republishes each
row's persisted bytes, so the id never changes. The integration `Encoder`
and its bytes are untouched.

`outbox_events` was created with `UNIQUE (event_id, topic)` from migration
`0001`, so the shared id is legal and no OLTP migration is needed.

### 4. The analytical model

Facts, never aggregates, so the day buckets can be recomputed for any range
(`analytics/migrations/0001_slot_plan_funnel`):

- `analytics_processed_events`: the dedupe set and the freshness source
  (`max(occurred_at)`).
- `slot_plan_facts`, one row per plan the projection has seen: `site_id`,
  `generated_at`, `approved_at`, `rejected_at`, `superseded_at`,
  `supersedes_plan_id`.

Each milestone column is the CloudEvents `time` of the earliest such event
seen, kept with `LEAST`, so arrival order never matters. A supersession is
stamped on the row of the plan that LOST the forward-slot map: projecting a
`SlotPlanApproved` that names `supersedes_plan_id` also upserts that plan's
`superseded_at` (the superseded plan is of the same site by the approve
invariant, so an unseen superseded plan's row is seeded from the approving
event's site).

### 5. The projector (`cmd/slotting-projector`)

- Consumes the analytics topic under a **fixed consumer group read from env**
  (`ANALYTICS_CONSUMER_GROUP`, default `slotting-optimization-analytics`, set
  by the chart). At-least-once; committed offsets are honoured, a brand-new
  group starts at the earliest offset so the model can be rebuilt from
  retained history.
- **Dedupe and effect in one transaction**: `Projection.Apply` validates the
  event, inserts the CloudEvents `id` into `analytics_processed_events`
  (`ON CONFLICT DO NOTHING`) and, only if it was inserted, folds the event
  into the fact table in ONE analytical-database transaction. The offset is
  committed only after `Apply` returned.
- **Policy** (the reference policy, on this repo's consumer primitives):
  - not a CloudEvents 1.0 message: skipped and committed past, with a
    rate-limited WARN (the first, then at most one per minute with the
    number suppressed);
  - a valid CloudEvent of another type: acknowledged and ignored;
  - a known type whose payload contradicts its envelope (a `plan_id` that
    does not match the subject) or fails validation, or one the store
    deterministically rejects (`report.ErrRejected`: validation, Postgres
    SQLSTATE class 22 or 23): **dead-lettered at once** to
    `warehouse.slotting-optimization.analytics.dlq` (raw bytes plus the
    `x-dlq-*` headers of ADR 0010's convention), then committed past; a
    failed DLQ write retries the message, so poison is never lost;
  - a **transient** failure (database down, timeout): the same message is
    retried with capped exponential backoff and never dead-lettered.
- Boot: the embedded analytical migrations (`analytics/migrations`) and the
  first ping run under `internal/bootretry`; Kafka is dialled lazily.
  `/healthz` and `/readyz` on `:8091`; graceful shutdown flips readiness,
  drains, lets the in-flight message finish (commit or dead-letter), then
  closes the pool.
- It writes only to the analytical database (`ANALYTICS_DATABASE_URL`, a
  direct DSN) and never opens the OLTP database.

### 6. The report (`cmd/slotting-reports`, `:8092`)

Read-only HTTP over the analytical database through a read-only pool
(`default_transaction_read_only=on`); `/healthz`; RFC 7807 errors; no auth
(fleet rule); additive OpenAPI paths in `apis/openapi.yaml` (tag `Reports`,
server `http://localhost:8092`).

| endpoint | question | shape |
| --- | --- | --- |
| `GET /reports/slot-plan-funnel` | the per-day review funnel of the plans | `from`, `to`, `site_id?`, `granularity` (`day`, the only bucket), `days[]`: `day`, `plans_generated`, `plans_approved`, `plans_rejected`, `approval_latency{count,p50_seconds,p95_seconds}`, `supersessions` |
| `GET /reports/freshness` | how far the projection is behind (fleet analytics charter) | `as_of` (newest applied CloudEvents time), `lag_seconds` (now - as_of, never negative); both `null` until the first event |

Rules:

- `from` / `to` are optional RFC 3339 instants; both omitted = the 30 days
  ending now; at most 366 days; `from` inclusive, `to` exclusive; an empty or
  inverted range is a 400.
- `siteId` is optional and narrows every counter to one site; omitted = every
  site. `granularity=day` is the only value (an operator-review cadence
  report; the same decision as process-path-management's catalogue-growth
  report) and is the default; anything else is a 400.
- `days[]` is dense: one row per UTC calendar day that intersects
  `[from, to)`, zeros included. A day's counts cover the part of the day
  inside the range. A fact counts in the day its milestone `time` falls in:
  generations by `generated_at`, approvals and the latency by `approved_at`,
  rejections by `rejected_at`, supersessions by the superseded plan's
  `superseded_at`.
- `approval_latency` is the generated-to-approved time of the plans approved
  in the window whose generation the projection also saw; a plan decided
  (or still pending) without a seen generation has no latency — "null when
  not yet decided". p50/p95 are linear-interpolated percentiles in seconds
  (PostgreSQL `percentile_cont`) and are `null` when `count` is 0.
- SQL only counts and takes percentiles; the range rules, day windows, the
  granularity and the freshness lag live in `internal/analytics/report`
  (pure, imports nothing internal, in the coverage scope). The in-memory
  store and the Postgres store run the same contract test.

### 7. What is deliberately NOT reported

- **Per-assignment and per-move analytics** (which SKU went to which slot,
  move counts by kind): `SlotPlanApproved` carries them, but the funnel's
  question is the review cadence, not slot-level history; a later report can
  add a move-facts table from the same stream without touching this one.
- **Execution progress** (`StockRelocated` from inventory-storage, ADR 0010):
  that event is on `warehouse.inventory.events`, not this service's own
  streams, and it already serves the OLTP read model on
  `GET /slot-plans/{planId}`. Pulling a consumed context's topic into this
  projection would couple the analytical database to another context's
  contract.
- **Re-generation churn** (same-site plan frequency) beyond the generated
  counter: derivable from the funnel by the reader; no extra column until
  someone asks for it.

### 8. Why a separate analytical database

The projection is a derived copy that can be dropped and rebuilt from the
topic; keeping it out of the OLTP database means a heavy report cannot take
OLTP connections, the two schemas evolve independently, the reports role can
be read-only on a database that holds nothing transactional, and the
projector has no credentials on OLTP data. The cost is a second
role/database to provision in `warehouse-infra` (the orchestrator wires that
in a later change).

### 9. Packaging

The one image builds every `cmd/*` (the Dockerfile's build loop), so
`/app/slotting-projector` and `/app/slotting-reports` ship in it; the
analytical migrations are embedded. The chart gains two components,
`analytics-projector` (Deployment) and `analytics-reports` (Deployment and
Service `<release>-reports:80`), plus a Secret with `ANALYTICS_DATABASE_URL`
and `ANALYTICS_READER_DATABASE_URL`, behind `analytics.enabled` (default
`false`: the default render is unchanged). The chart refuses to render
analytics without a DSN source or without Kafka. External routing to the
reports Service is warehouse-infra's job, not the chart's.

## Consequences

- Two outbox rows per event; the relay publishes both through the same sink.
  Tests that count outbox rows for one write now see two per event (the
  wiring integration test asserts the pairs and their shared id).
- The analytics stream is not an integration contract: only
  `slotting-projector` consumes it. Other contexts keep reading
  `warehouse.slotting-optimization.events`.
- Plans generated before the analytics stream existed are only partly
  visible (an approval without a seen generation has no latency).
  Replaying from the earliest retained offset is the backfill.
- The three hexagonal isolation rules this repo's arch test template asks
  for when an `internal/analytics` region appears are added: the region
  depends on nothing internal except itself, and the OLTP domain and
  application layers never import it or the analytics store.

## Alternatives considered and rejected

- **Project from the integration topic** (a second consumer group on
  `warehouse.slotting-optimization.events`): the fleet convention is a
  dedicated analytics topic owned by the producer, and a future
  analytics-only field could not be added without touching the integration
  contract.
- **Two ids per occurrence**: breaks the "same type and id on both topics"
  rule that lets a consumer correlate the streams and keeps retries
  idempotent per occurrence.
- **Pre-aggregated daily counters**: cannot be re-bucketed for a partial
  first or last day, and a late or replayed event would need decrement
  logic. Facts are cheap at this volume (a handful of plans per site per
  day).
- **Compute the report from the OLTP tables**: plans and their states are
  there, but not the milestone history as events, and analytical queries
  would run on the OLTP database.
- **Hour granularity**: the review cadence is daily; a finer bucket would
  multiply empty rows without answering a question anyone asks (the
  granularity parameter exists so that can change without a new endpoint).
