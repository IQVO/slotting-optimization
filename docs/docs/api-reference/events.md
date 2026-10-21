---
id: events
title: Event catalogue
sidebar_label: Event catalogue
sidebar_position: 2
---

# Event catalogue

Every Kafka message this service produces or consumes is a CloudEvents 1.0
event in **structured mode**: the Kafka value is the whole JSON envelope and
the header `content-type` is `application/cloudevents+json; charset=UTF-8`.
Published messages may also carry the optional W3C `traceparent` header of
the request that wrote them (ADR 0009).
Envelopes are built and decoded only in
`internal/adapters/kafka/cloudevents` (ADR 0004). The contract is
`apis/asyncapi.yaml`; field-level detail is on
[Domain events](/docs/ddd/domain-events).

## Published

Topic `warehouse.slotting-optimization.events`, `source`
`/warehouse/slotting-optimization`, `subject` and Kafka key = the plan id,
`dataschema` `urn:warehouse:slotting-optimization:events:<EventName>:v1`.

| Type | Raised by | `data` |
| --- | --- | --- |
| `com.warehouse.wms.slotting-optimization.slotplan.SlotPlanGenerated` | `POST /slot-plans` | `plan_id`, `site_id`, `window_from`, `window_to`, `policy`, `assignment_count`, `move_count`, `unassigned_count` |
| `com.warehouse.wms.slotting-optimization.slotplan.SlotPlanApproved` | `POST /slot-plans/{planId}/approve` | `plan_id`, `site_id`, `approved_at`, `supersedes_plan_id?`, `assignments[{sku, slot}]`, `moves[{sku, from_slot?, to_slot?, kind}]` |
| `com.warehouse.wms.slotting-optimization.slotplan.SlotPlanRejected` | `POST /slot-plans/{planId}/reject` | `plan_id`, `site_id`, `rejected_at`, `reason?` |

Superseding the previous Approved plan raises no event of its own: it is
carried by `supersedes_plan_id` on the new `SlotPlanApproved`.

Consumers of this integration topic on `origin/develop` of the siblings:
`wes-work-planning` (`SlotPlanApproved`: one RELOCATE work unit per move,
its ADR-0040) and `pick-face-replenishment` (`SlotPlanApproved`: its local
forward-assignment map, `internal/adapters/inbound/kafka/slotting_consumer.go`).
No sibling consumes `SlotPlanGenerated` or `SlotPlanRejected`.

### Analytics stream

Topic `warehouse.slotting-optimization.analytics` (ADR 0006) carries the same
three types with the same CloudEvents `id`, `type`, `source`, `subject` and
key, and a byte-identical `data`; only the `dataschema` differs
(`urn:warehouse:slotting-optimization:analytics:<EventName>:v1`). `cmd/api`
writes both rows in one transaction (`FanoutEncoder`, integration row
first). It is not an integration contract: the only consumer is this
repository's `cmd/slotting-projector` (group `ANALYTICS_CONSUMER_GROUP`,
default `slotting-optimization-analytics`), whose poison messages go to
`warehouse.slotting-optimization.analytics.dlq`.

## Consumed

| Type | Topic | Consumer |
| --- | --- | --- |
| `com.warehouse.wes.order-management.siteskudemand.SiteSkuDemandChanged` | `warehouse.order-management.events` | demand consumer (`DEMAND_MODE`) |
| `com.warehouse.wms.product-master.product.ProductClassified` | `warehouse.product-master.events` | product consumer (`PRODUCT_MODE`) |
| `com.warehouse.wms.product-master.product.ProductDimensionsDeclared` | `warehouse.product-master.events` | product consumer |
| `com.warehouse.wms.product-master.product.ProductMeasured` | `warehouse.product-master.events` | product consumer |
| `com.warehouse.wms.facility-layout.zone.ZoneRegistered` | `warehouse.facility.events` | layout consumer (`LAYOUT_MODE`) |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered` | `warehouse.facility.events` | layout consumer |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotDecommissioned` | `warehouse.facility.events` | layout consumer |
| `com.warehouse.wms.inventory-storage.stock.StockRelocated` | `warehouse.inventory.events` | relocation consumer (`RELOCATION_MODE`): slot-plan execution progress (ADR 0010) |
| `com.warehouse.wms.slotting-optimization.slotplan.SlotPlanGenerated`, `...SlotPlanApproved`, `...SlotPlanRejected` | `warehouse.slotting-optimization.analytics` | `cmd/slotting-projector` (analytics read side, ADR 0006) |

Dead-letter topics: `<topic>.dlq` for each consumed topic, including
`warehouse.slotting-optimization.analytics.dlq`
([Runbook](/docs/operations/runbook#delivery-semantics)). A malformed
`StockRelocated` goes there at once; a relocation of no known move does not.
