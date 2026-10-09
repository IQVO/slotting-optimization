---
id: troubleshooting
title: Troubleshooting
sidebar_label: Troubleshooting
sidebar_position: 4
---

# Troubleshooting

Symptom, cause, check, fix, for the failure modes the code on `develop` can
actually produce. Log messages are quoted exactly; their fields are listed on
[Observability](/docs/operations/observability#logs).

## Boot and readiness

| Symptom | Cause | Check | Fix |
| --- | --- | --- | --- |
| Process exits 1 right after start with `unknown EVENT_PUBLISHER "..." (want kafka or log)` | `EVENT_PUBLISHER` is neither `log` nor `kafka` | the `service exited with error` line | set `log` or `kafka` |
| Exits with `unknown DEMAND_MODE "..." (want kafka or permissive)` (or `PRODUCT_MODE`, `LAYOUT_MODE`) | typo in a consumer mode | same | `kafka` or `permissive` (or unset) |
| Exits with `DEMAND_MODE=kafka requires DEMAND_CONSUMER_GROUP` | a consumer in `kafka` mode has no group id | same | set the matching `*_CONSUMER_GROUP`; there is no default by design |
| Exits with `a consumer in kafka mode requires KAFKA_BROKERS` or `EVENT_PUBLISHER=kafka requires KAFKA_BROKERS` | Kafka needed, no brokers | same | set `KAFKA_BROKERS` (`localhost:9092` for the fleet broker from a laptop) |
| Exits about 15 s after start with `run migrations (after 5 attempts): ...` or `ping postgres (after 5 attempts): ...` | Postgres unreachable, wrong DSN or credentials | the preceding `retrying` WARN lines (`op`, `err`) | fix `DATABASE_URL` / `MIGRATIONS_DATABASE_URL`, network policy, or the database |
| Exits with `Dirty database version 1. Fix and force version.` | an earlier migration failed half-way and golang-migrate marked `schema_migrations` dirty | `SELECT * FROM schema_migrations;` | repair the schema by hand, then `UPDATE schema_migrations SET dirty = false;` (or drop and recreate a throwaway database) |
| Migrations hang or fail with an advisory-lock error behind PgBouncer | migrate's session advisory lock under transaction pooling | which DSN migrations use | point `MIGRATIONS_DATABASE_URL` at a direct connection |
| Pod never becomes ready | `/readyz` is ready as soon as the listener opens; a pod that never answers is still in the boot retry or has exited | container logs, restart count | fix the boot error above. `/readyz` checks neither Postgres nor Kafka, so a broker outage never makes the pod unready |
| `/readyz` answers `503 {"status":"not_ready"}` | shutdown began (`SIGTERM`); it never flips back | `shutdown: readiness flipped to not-ready` log line | expected during a rollout; a pod stuck here is waiting out the drain timeouts |
| `worker did not stop before the shutdown drain deadline` at shutdown | a consumer was mid-retry or the relay mid-pass for more than 10 s | the worker name in the line | harmless: uncommitted messages are redelivered and unpublished rows are resent |

## Plans that come out empty or partial

`POST /slot-plans` succeeds with a plan built from whatever the three local
copies hold; it never fails because a copy is empty.

| Symptom | Cause | Check | Fix |
| --- | --- | --- | --- |
| `assignments`, `moves` and `unassigned` all empty | no ACTIVE demand for the site in the window | `GET /sku-velocity?siteId=...` returns no items; `SELECT count(*) FROM demand_lines WHERE site_id = '...' AND state = 'ACTIVE';` | run the demand consumer (`DEMAND_MODE=kafka`), widen `lookbackDays`, or check the site id |
| Velocity empty although `order-management` has orders | `DEMAND_SITE_ID` is set to another site: those lines are dropped with `outcome=ignored` | `event processed ... outcome=ignored` log lines | unset `DEMAND_SITE_ID` or set it to the right site |
| Every SKU is `NoPhysicalProfile` | the product copy has no effective dimensions for them (no `ProductDimensionsDeclared`/`ProductMeasured`, or `effective_source=none`) | `SELECT sku, volume_mm3, weight_g FROM product_profiles WHERE sku IN (...);` | run the product consumer (`PRODUCT_MODE=kafka`); declare or measure the product in `product-master` |
| Every SKU is `NoEligibleSlot` and there is no slot at all | no forward slot matches: the zone's `site_code` differs from the demand `site_id`, the zone code is not in `FORWARD_ZONE_CODES`, the slot role is not `Storage`, or the slot is decommissioned | `SELECT s.location_code, s.role, s.active, z.site_code, z.zone_code FROM slots s JOIN zones z ON z.zone_id = s.zone_id;` | run the layout consumer (`LAYOUT_MODE=kafka`), fix `FORWARD_ZONE_CODES`, align site ids |
| Some SKUs are `NoEligibleSlot` while slots exist | hazmat or temperature mismatch (a `Hazmat` SKU only goes to a hazmat zone and vice versa; temperature classes must match, empty = Ambient), or every eligible slot is taken by a better-ranked SKU | the SKU's `handling_tags` and `temperature_class` against the zones | add compatible forward slots |
| SKUs are `NoCapacityFit` | one unit is larger or heavier than every compatible slot. A slot registered without capacity stores `0`, which holds nothing | `max_volume_m3`, `max_weight_kg` of the slots | register capacity in `facility-layout` |
| A slot came back in a plan after being decommissioned | it cannot: `LocationSlotDecommissioned` sets `active = false` for good, and a later registration does not reactivate it (`SaveSlot ... WHERE slots.active`) | `SELECT active FROM slots WHERE location_code = '...';` | if the slot really returned, it needs a new location code |
| A product update had no effect | its `version` was not newer than the stored one (`outcome=stale`) | `event processed ... outcome=stale` | expected; `product-master` versions are authoritative |

## REST problem types

Every error is `application/problem+json` with
`type = https://errors.slotting-optimization.warehouse-systems.dev/<slug>`
(`internal/adapters/inbound/http/errors.go`).

| Status and slug | Cause | Fix |
| --- | --- | --- |
| `400 idempotency-key-required` | `POST /slot-plans` without `Idempotency-Key` while running on Postgres (the in-memory mode has no middleware) | send a unique key per logical request |
| `422 idempotency-key-reused` | same key, different body (compared by SHA-256 of the raw body, so whitespace counts) | new key for a new request; resend the identical bytes to replay |
| `400 malformed-request` | invalid JSON, an unknown field (`DisallowUnknownFields`), trailing data, wrong types, or a body over 64 KiB | send only `siteId`/`lookbackDays` (generate) or `reason` (reject) |
| `400 invalid-site-id` | malformed `siteId` (empty, longer than 64 characters, whitespace, control characters or `/`), or no `siteId` while neither `DEMAND_SITE_ID` nor `DEFAULT_SITE_ID` is set | pass `siteId` or configure a default |
| `400 invalid-lookback` | `lookbackDays` is 0 or outside 1 to 365 | 1 to 365 |
| `400 invalid-limit` | `limit` outside 1 to 500, **or** `windowDays` of `GET /sku-velocity` not a positive integer or above 365 (the code reuses this slug for the window) | fix the parameter |
| `400 invalid-cursor` | a `cursor` this API did not hand out | restart paging without a cursor |
| `400 invalid-state` | `state` filter not one of `Draft`, `Approved`, `Rejected`, `Superseded` | fix the filter |
| `400 invalid-plan-id` | the path id does not start with `plan-` or breaks the token rules | use the `planId` from the `Location` header |
| `400 reject-reason-too-long` | trimmed reason over 500 characters | shorten it |
| `404 plan-not-found` | unknown plan id | |
| `409 plan-not-draft` | approve or reject on a plan that is already `Approved`, `Rejected` or `Superseded` | generate a new plan |
| `409 approved-plan-conflict` | two approvals of the same site raced; the loser hit the partial unique index or saw the previous Approved plan change | re-read `GET /forward-slots`, then decide whether to approve again |
| `409 concurrent-modification` | the plan's `version` changed between load and save | re-fetch and retry |
| `500 internal-error` | anything unmapped, typically a Postgres outage or a query hitting `statement_timeout = 5s`. The detail is generic; the cause is in the `request failed` log line | check the log and the database |

There is no `412` in this service: no endpoint takes `If-Match` or returns
an `ETag`. A browser call blocked before it reaches the API is CORS: add the
console's origin to `CORS_ALLOWED_ORIGINS`.

## Kafka

| Symptom | Cause | Check | Fix |
| --- | --- | --- | --- |
| Approved plans never reach `warehouse.slotting-optimization.events` | `EVENT_PUBLISHER` is `log` (the default): the relay marks rows published after logging them | `outbox relay running ... publisher=log`; `event published (log sink)` lines | set `EVENT_PUBLISHER=kafka`; to resend, see [Re-publish plan events](/docs/operations/runbook#re-publish-plan-events) |
| Outbox backlog grows, `outbox relay pass failed` every interval | broker unreachable or the write is rejected | `SELECT id, attempts, last_error FROM outbox_events WHERE published_at IS NULL ORDER BY id LIMIT 5;` | fix the broker; rows stay unpublished and are sent in order once it is back. A row that can never be sent blocks the rows after it |
| Consumer group lag grows; `handling failed; retrying the same message` repeats | a transient error (database down, statement timeout) on one message | the `error` field | fix the dependency; after 5 attempts the message is dead-lettered and the partition moves on |
| Messages on `<topic>.dlq` | a message failed 5 times with a transient error | the `x-dlq-error` and `x-dlq-source-*` headers | fix the cause, then [replay it](/docs/operations/runbook#replay-a-dead-lettered-message) |
| `dead-letter publish failed; retrying the same message` forever | the DLQ topic cannot be written (ACLs, auto-creation disabled) | broker logs | create `<topic>.dlq` or grant access; the partition stays blocked until then, by design |
| `skipping a message that is not a valid CloudEvent` | a producer sent a non-CloudEvents value (e.g. the retired flat envelope) | the `topic` field | fix the producer; the message is committed past and lost for this copy |
| `skipping an event that can never be applied` | the payload decoded but failed validation (bad SKU, unknown `state`, `version` below 1, unknown temperature class, negative capacity) | `type`, `event_id`, `error` | fix the producer's data; it is not retried |
| A new group id replays nothing | claims in `processed_events` are keyed by consumer name (`slotting-demand`, `slotting-product`, `slotting-layout`), not by group | `outcome=duplicate` for every message | follow [Rebuild a local copy](/docs/operations/runbook#rebuild-a-local-copy-from-the-producers-topics) |
| A laptop consumer "steals" partitions from the cluster | the same group id on the shared broker | `kafka-consumer-groups.sh --describe` shows your host | use a group id nobody else uses |

## What does not exist

There is no circuit breaker, no outbound HTTP client, no MCP server and no
analytics read side in this service on `develop`, so none of their failure
modes apply.
