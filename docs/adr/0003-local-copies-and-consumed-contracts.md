# ADR 0003: event-fed local copies and the consumed contracts

## Status

Accepted (2026-10-08).

## Context

A plan needs three kinds of facts that other contexts own: demand (what is
ordered), product facts (handling class and unit size) and slot facts (which
slots exist, where, how big). ADR 0001 forbids live cross-context lookups, so
slotting-optimization keeps local, event-fed copies and computes plans from
them. This ADR records exactly what is consumed, how the copies converge, and
the switches that control them. Every contract below was read from the
producer's `apis/asyncapi.yaml` on `origin/develop` on 2026-10-08.

## Decision

### Consumed contracts

| Topic | Full `type` | Kafka key | Fields used |
|---|---|---|---|
| `warehouse.order-management.events` | `com.warehouse.wes.order-management.siteskudemand.SiteSkuDemandChanged` | `<order>/line/<n>` (= subject) | `source_order_id, line_no, site_id, sku, demanded_units, due_at, state (ACTIVE or REMOVED), assignment_version` |
| `warehouse.product-master.events` | `com.warehouse.wms.product-master.product.ProductClassified` | SKU | `sku, handling_tags[], temperature_class?, dot_hazard_class?, classification_source, version` |
| `warehouse.product-master.events` | `com.warehouse.wms.product-master.product.ProductDimensionsDeclared` | SKU | `sku, effective{length_mm, width_mm, height_mm, weight_g, volume_mm3}?, effective_source, version` |
| `warehouse.product-master.events` | `com.warehouse.wms.product-master.product.ProductMeasured` | SKU | same physical-profile payload |
| `warehouse.facility.events` | `com.warehouse.wms.facility-layout.zone.ZoneRegistered` | `zoneId` | `zoneId, siteCode, areaCode, zoneCode, temperatureClass, hazmat` |
| `warehouse.facility.events` | `com.warehouse.wms.facility-layout.locationslot.LocationSlotRegistered` | `locationCode` | `locationCode, aisleId, zoneId, locationType, role? (absent = Storage), maxWeightKg, maxVolumeM3` |
| `warehouse.facility.events` | `com.warehouse.wms.facility-layout.locationslot.LocationSlotDecommissioned` | `locationCode` | `locationCode` |

Every other type on those topics is ignored. Facility-layout `data` also
carries `eventName`, `eventType` and `occurredAt`; they are decoded and
ignored. A message that is not a valid CloudEvent is logged at WARN and
committed past.

### How each copy converges

All three copies are durable Postgres tables fed by a normal
process-and-commit consumer under a **stable** consumer group (not a full
replay). The CloudEvents `id` claim and the upsert commit in ONE database
transaction; the Kafka offset is committed only afterwards.

- **Demand** (`DEMAND_*`). **Last writer wins per source line.** The key
  `<order>/line/<n>` puts one line's whole stream on one partition, so it is
  totally ordered and the newest message is the truth. The copy has one row per
  `(source_order_id, line_no)`. `state = REMOVED` deactivates the row (it stays,
  so a replay cannot resurrect it); only `ACTIVE` rows count as picks. Demand is
  **not netted against cancellations**: cancellation events are analytics-only
  in order-management, and the integration stream already says `REMOVED` when
  cancellation took a line's demand away. The `site_id` on the event is the
  static demand site configured in order-management
  (`DEMAND_PROJECTION_SITE_ID` there; `assignment_version = static-site-v1`);
  slotting-optimization takes it as given and plans per `site_id`.
  A line belongs to a window when its `due_at` falls in `[from, to)`; `due_at`
  is the only date the event carries and is the promise cutoff of the line, so
  it is a proxy for when the pick happens.
- **Product** (`PRODUCT_*`). One row per SKU per concern (classification,
  physical profile), applied only when the message `version` is greater than the
  stored one; replays and out-of-order delivery are harmless. The physical
  concern acts on `effective` (measured if present, else declared), full-state
  replace, never a merge.
- **Layout** (`LAYOUT_*`). Zones keyed by `zoneId`, slots keyed by
  `locationCode`. Facility-layout events carry no version, but each key is
  ordered on its own partition and decommissioning is irreversible, so
  "registered upserts, decommissioned deletes" converges. A slot joins its zone
  at plan time through `zoneId`, so a zone and its slots may arrive in any order
  (convergent).

The three copies are independent: a plan reads whatever each has.

### Switches and consumer groups

| Concern | Mode env | Consumer group env |
|---|---|---|
| Demand | `DEMAND_MODE` | `DEMAND_CONSUMER_GROUP` |
| Product | `PRODUCT_MODE` | `PRODUCT_CONSUMER_GROUP` |
| Layout | `LAYOUT_MODE` | `LAYOUT_CONSUMER_GROUP` |

- A mode is `kafka` or `permissive`; **default `permissive`** (no consumer
  starts, the copy is whatever is in the database, which keeps tests and local
  runs free of a broker). The cluster sets `kafka`.
- Consumer groups come from the environment, never a string literal at the call
  site (fleet rule, enforced by `TestKafkaConsumerGroupNeverHardcodedInline`).
  **Mode `kafka` with the group variable unset is a boot error**, not a silent
  no-op.
- `FORWARD_ZONE_CODES` (default `FWD`) lists the zone codes whose `Storage`
  slots are forward pick slots. `DEFAULT_SITE_ID` is the site used when a
  request does not name one.

### Live-data finding rule

The defaults above are assumptions about data this context has not seen.
Before the first live plan, in the wiring wave, someone runs read-only SQL on
the service's own database and answers, from rows rather than from this ADR:

1. Which distinct `zoneCode` values exist in the zone copy, and does one match
   `FWD`? Facility-layout's published examples use composite ids such as
   `WH1-STOR-AMB-A07-03-02-B`, so the real forward zone code may not be `FWD`.
   If it is not, set `FORWARD_ZONE_CODES` to the real value(s); do not invent a
   zone and do not rewrite this default silently (record the finding in an ADR
   addendum).
2. Does the demand `site_id` equal the facility `siteCode` of the forward
   zones? v1 assumes the same code space. If not, the fix is a configured
   mapping, decided with the user.
3. Does every active SKU with picks have an effective physical profile? The
   share that does not appears as `NoPhysicalProfile` in the first plans.

A plan over data that fails these checks is not wrong; it is empty or short,
and its `unassigned` list says why.

## Consequences

- No synchronous dependency on four sibling contexts; plan generation is a
  local read.
- The copies need no schema beyond what each event carries, and each is rebuilt
  by replaying its topic.
- Plans are as fresh as the copies. A plan's `generatedAt` says when it was
  computed, not how current the inputs were; the analytics phase adds a
  freshness read (ADR 0006, later).
