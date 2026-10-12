# ADR 0004: CloudEvents envelope and type catalogue

## Status

Accepted (2026-10-08).

## Context

Every Kafka message in the fleet is a CloudEvents 1.0 event in structured mode
(fleet rule `.claude/rules/fleet/cloudevents.md`, warehouse-docs
`docs/strategic-design/event-standard-cloudevents.md`). This ADR is the
catalogue of every type slotting-optimization publishes or consumes;
`TestEventCatalogueMatchesContract` checks it against `apis/asyncapi.yaml`.

## Decision

### Envelope

- `specversion` `1.0`; `id` a UUID persisted with the outbox row (stable across
  relay retries; consumers dedupe on it); `source`
  `/warehouse/slotting-optimization`; `subject` the plan id (`plan-<uuid>`);
  `time` the occurred-at instant in UTC; `datacontenttype` `application/json`;
  `dataschema` `urn:warehouse:slotting-optimization:events:<EventName>:v1`.
- Kafka value = the JSON event format; header
  `content-type: application/cloudevents+json; charset=UTF-8`; Kafka key = the
  plan id, so all events of one plan stay ordered on one partition.
- `data` is snake_case; optional fields are omitted when unset; timestamps are
  RFC 3339 UTC.
- Built and decoded only through `internal/adapters/kafka/cloudevents`.

### Published on `warehouse.slotting-optimization.events`

`<entity>` is the raising aggregate, lowercase, no separators: `slotplan`.

| type | raised when | `data` |
|---|---|---|
| `com.warehouse.wms.slotting-optimization.slotplan.SlotPlanGenerated` | a Draft plan is generated | `{plan_id, site_id, window_from, window_to, policy, assignment_count, move_count, unassigned_count}` |
| `com.warehouse.wms.slotting-optimization.slotplan.SlotPlanApproved` | a Draft plan is approved | `{plan_id, site_id, approved_at, supersedes_plan_id?, assignments:[{sku, slot}], moves:[{sku, from_slot?, to_slot, kind}]}` |
| `com.warehouse.wms.slotting-optimization.slotplan.SlotPlanRejected` | a Draft plan is rejected | `{plan_id, site_id, rejected_at, reason?}` |

Payload rules:

- `SlotPlanApproved` carries the **full** forward assignment map (every
  `{sku, slot}` of the plan) and the moves, so a consumer replaces its picture
  of the forward slots from this one message and needs no lookup. `kind` is
  `Assign`, `Relocate` or `Vacate`. For `Vacate`, `to_slot` is omitted and
  `from_slot` is set; for `Assign`, `from_slot` is omitted.
- `supersedes_plan_id` is the site's previously Approved plan, omitted when
  there was none. No event announces the supersession itself.
- `SlotPlanGenerated` carries sizes only, not the assignments: a Draft is a
  proposal and is not part of the published picture.
- `reason` on `SlotPlanRejected` is omitted when no reason was given.
- A breaking payload change is a new `.v2` type and dataschema, never a
  mutation of v1.

### Consumed (ADR 0003)

| type | topic | consumer group env |
|---|---|---|
| `com.warehouse.wes.order-management.siteskudemand.SiteSkuDemandChanged` | `warehouse.order-management.events` | `DEMAND_CONSUMER_GROUP` |
| `com.warehouse.wms.product-master.product.ProductClassified` | `warehouse.product-master.events` | `PRODUCT_CONSUMER_GROUP` |
| `com.warehouse.wms.product-master.product.ProductDimensionsDeclared` | `warehouse.product-master.events` | `PRODUCT_CONSUMER_GROUP` |
| `com.warehouse.wms.product-master.product.ProductMeasured` | `warehouse.product-master.events` | `PRODUCT_CONSUMER_GROUP` |
| `com.warehouse.wms.facility-layout.zone.ZoneRegistered` | `warehouse.facility.events` | `LAYOUT_CONSUMER_GROUP` |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered` | `warehouse.facility.events` | `LAYOUT_CONSUMER_GROUP` |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotDecommissioned` | `warehouse.facility.events` | `LAYOUT_CONSUMER_GROUP` |

Every other type on those topics is ignored. A message that is not a valid
CloudEvent is logged and skipped, never retried forever.

### Analytics topic (reserved, later phase)

`warehouse.slotting-optimization.analytics` is reserved for this service's own
analytics read side (ADR 0006, a later phase). It will carry the same three
types with the same `id` per occurrence and
`dataschema` `urn:warehouse:slotting-optimization:analytics:<EventName>:v1`.
Nothing is published there yet.

## Consequences

- Consumers need nothing but this catalogue and `apis/asyncapi.yaml`; there is
  no shared Go code.
- The full assignment map in `SlotPlanApproved` makes the message grow with the
  forward-slot count (a few kilobytes per thousand slots); that is accepted so
  consumers stay lookup-free.
