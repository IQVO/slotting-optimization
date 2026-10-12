---
paths:
  - "internal/adapters/**/kafka/**"
  - "internal/adapters/outbound/events/**"
  - "apis/asyncapi*"
---

# Cross-service integration events (Kafka)

This service PUBLISHES slot-plan decisions on `warehouse.slotting-optimization.events`
(through the transactional outbox) and CONSUMES, into durable local copies
(ADR 0003), demand from `warehouse.order-management.events`, product facts from
`warehouse.product-master.events` and zones/slots from `warehouse.facility.events`.
`warehouse.slotting-optimization.analytics` is reserved for this service's own
analytics read side (a later phase); nothing is published there yet.

## Events: CloudEvents 1.0 is MANDATORY

Every Kafka message this service produces or consumes (integration
`warehouse.<ctx>.events` AND analytics `warehouse.<ctx>.analytics`) is a
CloudEvents 1.0 event in structured content mode. This is a hard fleet rule,
not a preference — there is nothing to "choose" here:

- No flat envelope (`event_id`/`event_type`/`occurred_at`), no dual-write,
  no dual-read, no envelope toggle env var (`EVENT_ENVELOPE_MODE` is gone
  fleet-wide). `internal/architecture/fitness_test.go`'s
  `TestNoEventEnvelopeToggleOrFlatEnvelope` fails CI on any of them.
- Build/validate/(un)marshal with `github.com/cloudevents/sdk-go/v2/event`
  (latest v2) via ONE helper package,
  `internal/adapters/kafka/cloudevents/` — copy it from this template's
  `templates/cloudevents/cloudevents.go.tmpl` (+ its test) and change only
  the per-repo constants. Transport stays `segmentio/kafka-go` (no sdk-go
  protocol/client packages, no hand-rolled CloudEvent structs).
- Every produced message carries the Kafka header
  `content-type: application/cloudevents+json; charset=UTF-8`
  (`cloudevents.ContentTypeHeader()`), next to the W3C trace headers
  (`traceparent`/`tracestate` stay in headers, never duplicated into
  extension attributes). Message key = aggregate id, `kafkago.Hash{}`
  balancer.
- Required attributes: `specversion=1.0`; `id` (UUID v4 minted ONCE per
  domain event and persisted with the outbox row, so redelivery carries
  the same id); `source=/warehouse/<repo>`; `type`; `subject` (aggregate
  instance id, never empty); `time` (domain occurred-at, UTC);
  `datacontenttype=application/json`;
  `dataschema=urn:warehouse:<repo>:<events|analytics>:<EventName>:v<N>`.
  No custom extension attributes without an ADR.
- `type` = `com.warehouse.<subdomain>.<bounded-context>.<entity>.<EventName>`
  (this repo: `com.warehouse.wms.slotting-optimization`; entity segment
  `slotplan`). The SAME `type` names the
  occurrence on both the integration and the analytics topic; `dataschema`
  names the payload shape. Breaking payload change => new `.v2` type + new
  dataschema version, never mutate an existing one.
- Consumers decode with `cloudevents.Decode` (validates), dispatch on the
  FULL `type` string (never a short name or suffix match), ignore unknown
  types, read `time`/`subject` from attributes and the payload via
  `DataAs`, dedupe on `id`, and DLQ/skip — WARN log + commit past, never
  crash, never block the partition, never fall back to parsing a legacy
  shape — anything that fails CloudEvents validation.
- Tests: a golden exact-JSON test per published `type` (all attributes +
  the `content-type` header); a legacy-flat-message-rejected test per
  consumer; Kafka integration tests via testcontainers only.

Full standard, subdomain table and the fleet's cross-service type
catalogue: the warehouse-docs repo's Event Standard page
(docs/strategic-design/event-standard-cloudevents.md there, not in this repo).
This repo's ADR: `docs/adr/0004-cloudevents-envelope-and-type-catalogue.md`.

### Published types

Topic `warehouse.slotting-optimization.events`; `subject` and Kafka key = the
plan id (`plan-<uuid>`). All `data` is snake_case, optional fields are omitted
when unset, timestamps are RFC 3339 UTC.

| `type` | `dataschema` | `data` |
| --- | --- | --- |
| `com.warehouse.wms.slotting-optimization.slotplan.SlotPlanGenerated` | `urn:warehouse:slotting-optimization:events:SlotPlanGenerated:v1` | `{plan_id, site_id, window_from, window_to, policy, assignment_count, move_count, unassigned_count}` |
| `com.warehouse.wms.slotting-optimization.slotplan.SlotPlanApproved` | `urn:warehouse:slotting-optimization:events:SlotPlanApproved:v1` | `{plan_id, site_id, approved_at, supersedes_plan_id?, assignments:[{sku, slot}], moves:[{sku, from_slot?, to_slot, kind}]}`; `kind` = `Assign`, `Relocate`, `Vacate` (`Vacate`: `to_slot` omitted, `from_slot` set) |
| `com.warehouse.wms.slotting-optimization.slotplan.SlotPlanRejected` | `urn:warehouse:slotting-optimization:events:SlotPlanRejected:v1` | `{plan_id, site_id, rejected_at, reason?}` |

`SlotPlanApproved` carries the FULL assignment map, so a consumer needs no
lookup. Golden exact-JSON tests pin every type.

### Consumed types

| `type` | topic | producer |
| --- | --- | --- |
| `com.warehouse.wes.order-management.siteskudemand.SiteSkuDemandChanged` | `warehouse.order-management.events` | order-management (key/subject `<order>/line/<n>`; last writer wins per line, `REMOVED` deactivates) |
| `com.warehouse.wms.product-master.product.ProductClassified` | `warehouse.product-master.events` | product-master (apply when `version` > stored) |
| `com.warehouse.wms.product-master.product.ProductDimensionsDeclared` | `warehouse.product-master.events` | product-master (act on `effective`, apply when `version` > stored) |
| `com.warehouse.wms.product-master.product.ProductMeasured` | `warehouse.product-master.events` | product-master (same payload and rule) |
| `com.warehouse.wms.facility-layout.zone.ZoneRegistered` | `warehouse.facility.events` | facility-layout (key `zoneId`) |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered` | `warehouse.facility.events` | facility-layout (key `locationCode`; absent `role` = Storage) |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotDecommissioned` | `warehouse.facility.events` | facility-layout (key `locationCode`; irreversible) |

Every other type on those topics is ignored. The CloudEvents `id` claim and the
effect commit in ONE database transaction; the Kafka offset is committed only
afterwards (`FetchMessage` + `CommitMessages`).

## Consumer group id and modes

Each consumer has a mode env (`kafka` or `permissive`, default `permissive` =
no consumer started) and a STABLE consumer group id read from the environment,
never a hardcoded string literal:

| Concern | Mode | Group id |
| --- | --- | --- |
| Demand | `DEMAND_MODE` | `DEMAND_CONSUMER_GROUP` |
| Product | `PRODUCT_MODE` | `PRODUCT_CONSUMER_GROUP` |
| Layout | `LAYOUT_MODE` | `LAYOUT_CONSUMER_GROUP` |

Mode `kafka` with the group variable unset is a boot error.
`internal/architecture/fitness_test.go`'s
TestKafkaConsumerGroupNeverHardcodedInline enforces env-configurable group ids
(a real incident: wes-work-planning's hardcoded group id let a locally-run
e2e-tests process silently collide with the live in-cluster Deployment's
consumer group on the shared fleet Kafka broker).

These consumers resume from their committed offset (they feed a durable copy).
If one ever replays from FirstOffset on every start to build an in-memory read
model instead, its group id must additionally be UNIQUE PER PROCESS INSTANCE
(hostname+PID+timestamp), not just configurable -- see HARNESS.md's Kafka
section for why a shared group breaks that pattern specifically.
