---
id: eventstorming
title: EventStorming
sidebar_label: EventStorming
sidebar_position: 16
---

# EventStorming

A design-level EventStorming of `slotting-optimization`, using the sticky
colours of the ddd-crew
[EventStorming glossary and cheat sheet](https://github.com/ddd-crew/eventstorming-glossary-cheat-sheet):
**orange** domain event, **blue** command, **yellow** aggregate, **lilac**
policy, **green** read model, **pink** external system, **red** hotspot,
small yellow actor.

## Planning flow

```mermaid
flowchart LR
    classDef event fill:#ff9f43,stroke:#333,color:#000
    classDef command fill:#54a0ff,stroke:#333,color:#000
    classDef aggregate fill:#feca57,stroke:#333,color:#000
    classDef policy fill:#c8a2c8,stroke:#333,color:#000
    classDef readmodel fill:#1dd1a1,stroke:#333,color:#000
    classDef external fill:#ff9ff3,stroke:#333,color:#000
    classDef actor fill:#fffa65,stroke:#333,color:#000
    classDef hotspot fill:#ff6b6b,stroke:#333,color:#000

    planner(["Planner"]):::actor
    vel["SKU velocity"]:::readmodel
    gen["Generate slot plan"]:::command
    agg["SlotPlan"]:::aggregate
    pol["abc-velocity-v1 planner policy"]:::policy
    generated["Slot plan generated"]:::event
    approve["Approve slot plan"]:::command
    reject["Reject slot plan"]:::command
    sup["One Approved plan per site"]:::policy
    approved["Slot plan approved"]:::event
    rejected["Slot plan rejected"]:::event
    fwd["Forward-slot map"]:::readmodel
    hs1["Who executes the moves?"]:::hotspot
    wwp["wes-work-planning<br/>one RELOCATE work unit per move"]:::external
    ist["inventory-storage<br/>forward-face read model"]:::external
    pfr["pick-face-replenishment<br/>forward assignment map"]:::external

    planner --> vel --> gen --> agg
    pol --> agg
    agg --> generated
    generated --> approve --> agg
    generated --> reject --> agg
    sup --> agg
    agg --> approved --> fwd
    agg --> rejected
    approved --> hs1
    approved --> wwp
    approved --> ist
    approved --> pfr
```

The three sibling consumers of `SlotPlanApproved` are in wes-work-planning
(`internal/adapters/inbound/kafka/consumer.go`), inventory-storage
(`internal/adapters/inbound/kafka/slot_plan_consumer.go`) and
pick-face-replenishment (`internal/adapters/inbound/kafka/slotting_consumer.go`),
each on its `origin/develop`.

Source: `internal/application/usecases/{generate_plan,decide_plan,queries}.go`,
`internal/domain/slotplan/plan.go`, `internal/domain/planning/planner.go`.

## Local-copy flow

```mermaid
flowchart LR
    classDef event fill:#ff9f43,stroke:#333,color:#000
    classDef policy fill:#c8a2c8,stroke:#333,color:#000
    classDef readmodel fill:#1dd1a1,stroke:#333,color:#000
    classDef external fill:#ff9ff3,stroke:#333,color:#000
    classDef hotspot fill:#ff6b6b,stroke:#333,color:#000

    om["order-management"]:::external
    pm["product-master"]:::external
    fl["facility-layout"]:::external
    is["inventory-storage"]:::external
    e1["Site SKU demand changed"]:::event
    e2["Product classified"]:::event
    e3["Product dimensions declared or measured"]:::event
    e7["Product pack level declared, retired or measured"]:::event
    e8["Stock relocated"]:::event
    e4["Zone registered"]:::event
    e5["Location slot registered"]:::event
    e6["Location slot decommissioned"]:::event
    p1["Whenever demand changes, upsert the line (last writer wins)"]:::policy
    p2["Whenever a product changes, keep the newest version"]:::policy
    p3["Whenever the layout changes, upsert, never revive a retired slot"]:::policy
    p4["Whenever a pack level changes, keep the newest version"]:::policy
    p5["Whenever stock is relocated, record it once per relocation id against the plan's move"]:::policy
    r1["Demand copy"]:::readmodel
    r2["Product copy"]:::readmodel
    r3["Slot copy"]:::readmodel
    r4["Pack-level copy"]:::readmodel
    r5["Move execution progress"]:::readmodel
    hs2["Demand site id equals facility site code?"]:::hotspot

    om --> e1 --> p1 --> r1
    pm --> e2 --> p2
    pm --> e3 --> p2 --> r2
    fl --> e4 --> p3
    fl --> e5 --> p3
    fl --> e6 --> p3 --> r3
    pm --> e7 --> p4 --> r4
    is --> e8 --> p5 --> r5
    r1 --> hs2
    r3 --> hs2
```

Source: `cmd/api/wire_kafka.go` (`consumerSpecs`: five consumers),
`internal/adapters/inbound/kafka/{demand,product,packlevel,layout,stock}_consumer.go`,
`internal/application/usecases/consumers.go`,
`internal/adapters/outbound/postgres/read_models.go`.

## Hotspots

| Hotspot | Why it is open | Where it shows |
| --- | --- | --- |
| Who executes the moves? | decided by ADR 0010 (supersedes ADR 0007): wes-work-planning plans one RELOCATE work unit per move of `SlotPlanApproved`, fulfillment-execution executes it, inventory-storage records `StockRelocated`, and this context records the progress per move | resolved; open follow-up: gating new proposals on unfinished execution is out of scope in v1 |
| Demand site id equals facility site code? | `ForwardSlots` joins `zones.site_code` to the plan's site, which comes from demand `site_id`; ADR 0003 calls for verifying this against live data | a mismatch makes every SKU `NoEligibleSlot` |
| Lexical slot ranking | layout events carry no travel distance, so v1 ranks slots by location code; `LocationGeometryUpdated` is ignored | "best" slot means "first code", not "closest" |
| No Kong route to the reports Service, web remote not deployed | the chart routes the api, the MCP server and the A2A agent through Kong, but renders no route for `cmd/slotting-reports`, and warehouse-infra deploys no `slotting_mfe` image | [Integration](/docs/ecosystem/integration#edge-and-infrastructure), [Runbook](/docs/operations/runbook#deployment) |
