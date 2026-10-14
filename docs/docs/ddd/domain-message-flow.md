---
id: domain-message-flow
title: Domain message flow
sidebar_label: Domain message flow
sidebar_position: 15
---

# Domain message flow

The key business scenarios as numbered command / event / query flows,
following ddd-crew
[Domain Message Flow Modelling](https://github.com/ddd-crew/domain-message-flow-modelling).
Messages are labelled *(command)*, *(event)* or *(query)*. Only edges that
exist in code are solid.

## 1. Keep the local copies current

```mermaid
sequenceDiagram
    autonumber
    participant OM as order-management
    participant PM as product-master
    participant FL as facility-layout
    participant SO as slotting-optimization
    OM->>SO: SiteSkuDemandChanged (event) on warehouse.order-management.events
    SO->>SO: ApplyDemandChanged upserts demand_lines
    PM->>SO: ProductClassified (event) on warehouse.product-master.events
    SO->>SO: ApplyProductClassified replaces tags and temperature if newer
    PM->>SO: ProductDimensionsDeclared or ProductMeasured (event)
    SO->>SO: ApplyPhysicalProfile replaces the effective unit size if newer
    FL->>SO: ZoneRegistered (event) on warehouse.facility.events
    SO->>SO: ApplyZoneRegistered upserts zones
    FL->>SO: LocationSlotRegistered (event)
    SO->>SO: ApplyLocationSlotRegistered upserts slots
    FL->>SO: LocationSlotDecommissioned (event)
    SO->>SO: ApplyLocationSlotDecommissioned retires the slot for good
```

Source: `internal/adapters/inbound/kafka/{demand,product,layout}_consumer.go`,
`internal/application/usecases/consumers.go`.

## 2. Generate a plan and approve it

```mermaid
sequenceDiagram
    autonumber
    actor P as Planner
    participant SO as slotting-optimization
    participant K as warehouse.slotting-optimization.events
    participant D as wes-work-planning
    P->>SO: GET /sku-velocity (query)
    SO-->>P: picks and units per SKU
    P->>SO: POST /slot-plans with Idempotency-Key (command GenerateSlotPlan)
    SO->>SO: read demand, profiles, forward slots, current Approved map
    SO->>SO: Planner computes assignments, moves, unassigned
    SO-->>P: 201 Draft plan
    SO->>K: SlotPlanGenerated (event, via outbox relay)
    P->>SO: POST /slot-plans/{planId}/approve (command ApproveSlotPlan)
    SO->>SO: supersede the previous Approved plan, approve this one
    SO-->>P: 200 Approved plan
    SO->>K: SlotPlanApproved (event, full map, moves, supersedes_plan_id)
    K-->>D: SlotPlanApproved plans RELOCATE work (flow 4)
    P->>SO: GET /forward-slots (query)
    SO-->>P: the new forward-slot map
```

Source: `internal/application/usecases/generate_plan.go`,
`internal/application/usecases/decide_plan.go`,
`internal/adapters/outbound/outbox/relay.go`.

## 3. Reject a plan

```mermaid
sequenceDiagram
    autonumber
    actor P as Planner
    participant SO as slotting-optimization
    participant K as warehouse.slotting-optimization.events
    P->>SO: GET /slot-plans/{planId} (query)
    SO-->>P: the Draft with its unassigned SKUs and reasons
    P->>SO: POST /slot-plans/{planId}/reject with reason (command RejectSlotPlan)
    SO-->>P: 200 Rejected plan
    SO->>K: SlotPlanRejected (event)
```

Source: `internal/application/usecases/decide_plan.go` (`RejectPlan`).

## 4. Execute the moves and record the progress

Decided by [ADR 0010](/docs/adr/0010-approved-slot-plans-are-executed-as-wes-relocation-work)
(supersedes [ADR 0007](/docs/adr/0007-executing-an-approved-slot-plan)).
The middle of the flow lives in the sibling contexts; this context publishes
the plan and reads back what the ledger recorded.

```mermaid
sequenceDiagram
    autonumber
    participant SO as slotting-optimization
    participant WWP as wes-work-planning
    participant FE as fulfillment-execution
    participant IS as inventory-storage
    SO->>WWP: SlotPlanApproved (event, moves)
    WWP->>WWP: one RELOCATE work unit per move, sequenced
    WWP->>FE: WorkReleased on the RELOCATE path (event)
    FE->>FE: operator counts and scans the bins
    FE->>IS: TaskCompleted RELOCATE (event, relocation_ref plan:seq)
    IS->>IS: RelocateStock moves the stock between bins
    IS->>SO: StockRelocated (event, source_ref plan:seq)
    SO->>SO: RecordRelocation matches the move, records it once
```

Source here: `internal/adapters/inbound/kafka/stock_consumer.go`,
`internal/application/usecases/record_relocation.go`,
`internal/domain/slotplan/execution.go`. The other steps are
wes-work-planning ADR 0040, fulfillment-execution ADR 0047 and
inventory-storage ADR 0039.

## 5. Read the execution progress

```mermaid
sequenceDiagram
    autonumber
    actor P as Planner or agent
    participant SO as slotting-optimization
    P->>SO: GET /slot-plans/{planId} or get_slot_plan (query)
    SO->>SO: load the plan and its recorded relocations
    SO-->>P: plan + execution (moves executed / total, last executed at, per move)
```

Source: `internal/application/usecases/queries.go` (`GetPlan.View`).
