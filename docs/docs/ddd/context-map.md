---
id: context-map
title: Context map
sidebar_label: Context map
sidebar_position: 13
---

# Context map

This context's slice of the fleet context map, following ddd-crew
[Context Mapping](https://github.com/ddd-crew/context-mapping). U =
upstream, D = downstream. Solid edges exist in code on `develop`; dashed
edges are planned.

```mermaid
flowchart LR
    om["order-management (wes, Generic/Supporting)"]
    pm["product-master (wms, Supporting)"]
    fl["facility-layout (wms, Generic)"]
    so["slotting-optimization (wms, Supporting)"]
    ui["warehouse-console, warehouse-ops-agent"]
    wwp["wes-work-planning"]
    fe["fulfillment-execution"]
    is["inventory-storage (wms)"]
    om -->|"U: Published Language / D: Conformist (Kafka)"| so
    pm -->|"U: Published Language / D: Conformist (Kafka)"| so
    fl -->|"U: Open Host Service + Published Language / D: Conformist (Kafka)"| so
    so -.->|"U: Open Host Service (REST, MCP), no caller yet"| ui
    so -->|"U: Published Language SlotPlanApproved / D: Conformist"| wwp
    wwp --> fe
    fe --> is
    is -->|"U: Published Language StockRelocated / D: Conformist"| so
```

Source: `internal/adapters/inbound/kafka/consumer.go` (topics),
`internal/adapters/outbound/kafka/encoder.go`,
`docs/adr/0001-slotting-optimization-bounded-context.md` (context map),
`docs/adr/0003-local-copies-and-consumed-contracts.md`,
`docs/adr/0010-approved-slot-plans-are-executed-as-wes-relocation-work.md`.

## Edges

| Edge | Pattern | Technology | Contract | State |
| --- | --- | --- | --- | --- |
| order-management -> slotting-optimization | Published Language / **Conformist**, local demand copy | Kafka `warehouse.order-management.events`, group `DEMAND_CONSUMER_GROUP` | `SiteSkuDemandChanged` v1 | built (`DEMAND_MODE=kafka`) |
| product-master -> slotting-optimization | Published Language / **Conformist**, local product copy | Kafka `warehouse.product-master.events`, group `PRODUCT_CONSUMER_GROUP` | `ProductClassified`, `ProductDimensionsDeclared`, `ProductMeasured` v1 | built (`PRODUCT_MODE=kafka`) |
| facility-layout -> slotting-optimization | Open Host Service + Published Language / **Conformist**, local slot copy | Kafka `warehouse.facility.events`, group `LAYOUT_CONSUMER_GROUP` | `ZoneRegistered`, `LocationSlotRegistered`, `LocationSlotDecommissioned` v1 | built (`LAYOUT_MODE=kafka`) |
| slotting-optimization -> console, ops-agent | **Open Host Service** | REST (`apis/openapi.yaml`); MCP read tools later | the seven REST operations | API built, no caller yet |
| slotting-optimization -> wes-work-planning (-> fulfillment-execution -> inventory-storage) | Published Language / **Conformist** | Kafka `warehouse.slotting-optimization.events`; RELOCATE work on the process-path-management `RELOCATE` path (intake `SLOT_RELOCATION`) | `SlotPlanApproved` v1 (moves) | built in the siblings (wes-work-planning ADR 0040, fulfillment-execution ADR 0047, inventory-storage ADR 0039, process-path-management ADR 0033/0034); decided by ADR 0010 |
| inventory-storage -> slotting-optimization | Published Language / **Conformist**, execution-progress read model | Kafka `warehouse.inventory.events`, group `RELOCATION_CONSUMER_GROUP` | `StockRelocated` v1 | built (`RELOCATION_MODE=kafka`, ADR 0010) |

## Why Conformist upstream

The four producers own facts this context only reads (demand, product
facts, slot facts, executed relocations). Restating their payloads in local structs
(`siteSkuDemandChangedData`, `productClassifiedData`,
`physicalProfileData`, `zoneRegisteredData`, `locationSlotRegisteredData`,
`locationSlotDecommissionedData`) and acting only on the fields it needs is
cheaper than an anti-corruption layer, and the copies keep the planner
working when a producer is down. The price: a breaking change upstream
breaks the copy, which is why ADR 0003 pins the consumed contracts in
`apis/asyncapi.yaml`.

## No live lookup, in either direction

This context has no outbound HTTP or MCP client, and no sibling calls it at
request time (ADR 0001). Siblings learn about plans only from
`warehouse.slotting-optimization.events`.

## Explicit non-edges

No command edge to `inventory-storage`: this context never moves stock or
changes stow; it only reads `StockRelocated` (ADR 0010). No edge to
`inbound-receiving` or `warehouse-planning` (ADR 0001).
