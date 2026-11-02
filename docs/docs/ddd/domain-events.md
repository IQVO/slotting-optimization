---
id: domain-events
title: Domain events
sidebar_label: Domain events
sidebar_position: 20
---

# Domain events

Every event `slotting-optimization` publishes or consumes, with its full
CloudEvents `type`, topic, key and payload. The published payloads are the
structs in `internal/adapters/outbound/kafka/encoder.go`; the consumed ones
are the restated structs in `internal/adapters/inbound/kafka/*_consumer.go`.
Both are pinned in `apis/asyncapi.yaml` (`TestEventCatalogueMatchesContract`).

## Envelope

| Attribute | Value for published events |
| --- | --- |
| `specversion` | `1.0` |
| `id` | UUID v4, minted once at encode time and stored in `outbox_events.event_id`: a resend carries the same id |
| `source` | `/warehouse/slotting-optimization` |
| `type` | `com.warehouse.wms.slotting-optimization.slotplan.<EventName>` |
| `subject` | the plan id |
| `time` | the domain occurred-at (UTC) |
| `datacontenttype` | `application/json` |
| `dataschema` | `urn:warehouse:slotting-optimization:events:<EventName>:v1` |
| Kafka key | the plan id (one plan's events stay ordered on one partition; a row the relay excluded after `OUTBOX_MAX_ATTEMPTS` failures is overtaken, [ADR 0008](/docs/adr/0008-outbox-relay-attempt-cap)) |
| Kafka header | `content-type: application/cloudevents+json; charset=UTF-8` |
| Optional Kafka headers | `traceparent` (and `tracestate` / `baggage` when present): the W3C trace context of the request that wrote the outbox row ([ADR 0009](/docs/adr/0009-trace-context-crosses-the-outbox)). Consumers may extract it and must not require it |

Mode: **structured** (the whole envelope is the Kafka value), built only by
`cloudevents.New`; consumers only accept what `cloudevents.Decode` accepts.

## Published on `warehouse.slotting-optimization.events`

### `SlotPlanGenerated`

`com.warehouse.wms.slotting-optimization.slotplan.SlotPlanGenerated`, raised
by `slotplan.Generate` through `POST /slot-plans`.

| Field | Type | Meaning |
| --- | --- | --- |
| `plan_id` | string | `plan-<uuid>` |
| `site_id` | string | site of the plan |
| `window_from`, `window_to` | RFC 3339 UTC | the demand window |
| `policy` | string | `abc-velocity-v1` |
| `assignment_count`, `move_count`, `unassigned_count` | integer | size of the proposal |

### `SlotPlanApproved`

`com.warehouse.wms.slotting-optimization.slotplan.SlotPlanApproved`, raised
by `SlotPlan.Approve` through `POST /slot-plans/{planId}/approvals`. It
carries the **full** map so a consumer needs no lookup.

| Field | Type | Meaning |
| --- | --- | --- |
| `plan_id`, `site_id` | string | |
| `approved_at` | RFC 3339 UTC | |
| `supersedes_plan_id` | string, omitted when none | the site's previous Approved plan, now Superseded |
| `assignments` | array of `{sku, slot}`, never null | the new forward-slot map |
| `moves` | array of `{sku, from_slot?, to_slot?, kind}`, never null | `Assign` (to only), `Relocate` (both), `Vacate` (from only) |

```json
{
  "specversion": "1.0",
  "id": "bfa2833a-...",
  "source": "/warehouse/slotting-optimization",
  "type": "com.warehouse.wms.slotting-optimization.slotplan.SlotPlanApproved",
  "subject": "plan-ee0b77f6-...",
  "time": "2026-10-09T11:50:02Z",
  "datacontenttype": "application/json",
  "dataschema": "urn:warehouse:slotting-optimization:events:SlotPlanApproved:v1",
  "data": {
    "plan_id": "plan-ee0b77f6-...",
    "site_id": "WH1",
    "approved_at": "2026-10-09T11:50:02Z",
    "assignments": [{"sku": "SKU-A", "slot": "FWD-01-01"}, {"sku": "SKU-B", "slot": "FWD-01-02"}],
    "moves": [{"sku": "SKU-A", "to_slot": "FWD-01-01", "kind": "Assign"}, {"sku": "SKU-B", "to_slot": "FWD-01-02", "kind": "Assign"}]
  }
}
```

(Illustrative values in the shape `encoder.go` produces; ids shortened.)

### `SlotPlanRejected`

`com.warehouse.wms.slotting-optimization.slotplan.SlotPlanRejected`, raised
by `SlotPlan.Reject` through `POST /slot-plans/{planId}/rejections`.

| Field | Type | Meaning |
| --- | --- | --- |
| `plan_id`, `site_id` | string | |
| `rejected_at` | RFC 3339 UTC | |
| `reason` | string, omitted when empty | trimmed, at most 500 characters |

`Supersede` raises **no** event: consumers learn it from
`supersedes_plan_id` on the newer `SlotPlanApproved`.

**Consumers:** `wes-work-planning` consumes `SlotPlanApproved` and plans one
RELOCATE work unit per move (its ADR 0040; `move_seq` = the move's 0-based
position in `moves`), executed by fulfillment-execution and recorded by
inventory-storage ([ADR 0010](/docs/adr/0010-approved-slot-plans-are-executed-as-wes-relocation-work),
[Integration](/docs/ecosystem/integration#downstream)). Nothing consumes
`SlotPlanGenerated` or `SlotPlanRejected`.

## Consumed

| Type | Topic | Producer | Fields acted on | Use case |
| --- | --- | --- | --- | --- |
| `com.warehouse.wes.order-management.siteskudemand.SiteSkuDemandChanged` | `warehouse.order-management.events` | order-management | `source_order_id`, `line_no`, `site_id`, `sku`, `demanded_units`, `due_at`, `state` (`assignment_version` is decoded and ignored) | `ApplyDemandChanged` |
| `com.warehouse.wms.product-master.product.ProductClassified` | `warehouse.product-master.events` | product-master | `sku`, `handling_tags`, `temperature_class`, `version` | `ApplyProductClassified` |
| `com.warehouse.wms.product-master.product.ProductDimensionsDeclared` | `warehouse.product-master.events` | product-master | `sku`, `effective.volume_mm3`, `effective.weight_g`, `effective_source`, `version` | `ApplyPhysicalProfile` |
| `com.warehouse.wms.product-master.product.ProductMeasured` | `warehouse.product-master.events` | product-master | same as above | `ApplyPhysicalProfile` |
| `com.warehouse.wms.facility-layout.zone.ZoneRegistered` | `warehouse.facility.events` | facility-layout | `zoneId`, `siteCode`, `areaCode`, `zoneCode`, `temperatureClass`, `hazmat` | `ApplyZoneRegistered` |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered` | `warehouse.facility.events` | facility-layout | `locationCode`, `zoneId`, `role`, `maxWeightKg`, `maxVolumeM3` (`aisleId`, `locationType`, `dockFlow`, `activities` ignored) | `ApplyLocationSlotRegistered` |
| `com.warehouse.wms.facility-layout.locationslot.LocationSlotDecommissioned` | `warehouse.facility.events` | facility-layout | `locationCode` | `ApplyLocationSlotDecommissioned` |
| `com.warehouse.wms.inventory-storage.stock.StockRelocated` | `warehouse.inventory.events` | inventory-storage (its ADR 0039) | `relocation_id`, `site_code`, `sku`, `from_bin`, `to_bin`, `quantity`, `source_ref`, `occurred_at` (`reason` decoded, not acted on) | `RecordRelocation` (ADR 0010) |

Dispatch is on the full `type` string; every other type on those topics is
ignored. Note the casing: order-management, product-master and
inventory-storage payloads are snake_case, facility-layout payloads are
camelCase, exactly as each producer publishes them.

### `StockRelocated` and the execution progress

A relocation executes a move of a plan when its `source_ref` is the move
reference `<plan_id>:<move_seq>` of an Approved or Superseded plan of
`site_code` whose move at `move_seq` is for `sku`; when `source_ref` is no
move reference (empty, a task id), the move of the site's Approved plan with
that `sku` whose bins agree with its kind matches (`Relocate`: both slots,
`Assign`: `to_bin`, `Vacate`: `from_bin`). A match is recorded once per
`relocation_id` in `slot_plan_move_executions`; anything else is
`unmatched` with a reason (`unknown-plan`, `no-current-plan`,
`plan-not-approved`, `site-mismatch`, `unknown-move`, `sku-mismatch`,
`no-matching-move`), counted and logged, never dead-lettered. A
`StockRelocated` missing a required field is dead-lettered at once.
Nothing is published in response: the progress is a read model, shown on
`GET /slot-plans/{planId}` and the MCP `get_slot_plan` tool.

## Event flow

```mermaid
flowchart LR
    om["order-management"] -->|SiteSkuDemandChanged| d[("demand_lines")]
    pm["product-master"] -->|"ProductClassified, ProductDimensionsDeclared, ProductMeasured"| p[("product_profiles")]
    fl["facility-layout"] -->|"ZoneRegistered, LocationSlotRegistered, LocationSlotDecommissioned"| s[("zones, slots")]
    d --> gen["GeneratePlan"]
    p --> gen
    s --> gen
    is["inventory-storage"] -->|StockRelocated| x[("slot_plan_move_executions")]
    gen -->|SlotPlanGenerated| ob[("outbox_events")]
    ap["ApprovePlan"] -->|SlotPlanApproved| ob
    rj["RejectPlan"] -->|SlotPlanRejected| ob
    ob -->|relay| t["warehouse.slotting-optimization.events"]
```

Source: `internal/adapters/inbound/kafka/*_consumer.go`,
`internal/application/usecases/*.go`,
`internal/adapters/outbound/kafka/encoder.go`.
