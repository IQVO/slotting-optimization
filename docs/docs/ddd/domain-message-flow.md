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
    participant D as downstream (none yet)
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
    K-->>D: no consumer subscribed on develop
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

## 4. Planned: execute the moves

Not built. ADR 0001 and ADR 0002 describe the intended path: a consumer in
`process-path-management` (or the WES) turns the moves of
`SlotPlanApproved` into MOVE / REPLENISH work for `wes-work-planning` and
`fulfillment-execution`. It needs ADRs in those contexts and a new task
type, so no diagram is drawn here as if it existed.
