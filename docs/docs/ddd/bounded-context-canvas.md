---
id: bounded-context-canvas
title: Bounded context canvas
sidebar_label: Bounded context canvas
sidebar_position: 12
---

# Bounded context canvas

ddd-crew [Bounded Context Canvas v5](https://github.com/ddd-crew/bounded-context-canvas),
filled from the code on `develop` and ADR 0001.

## Canvas at a glance

Two pictures of the sections below, laid out like the ddd-crew canvas:
first the inbound collaborators feeding this context (purpose, domain roles
and the business decisions it enforces), then the outbound collaborators it
calls or notifies. Arrows follow the message type: **solid blue**
commands (`cmd`), **dotted green** queries (`qry`), **thick orange** events
(`evt`); each arrow names its channel and the messages it carries. Box shapes
mark the collaborator kind: rounded = people, UI or any client; blue =
sibling bounded context; double border = external system or platform;
grey = this context's own projector, reports or dead-letter path; dashed =
published today with no consumer. Channel names on the arrows are
abbreviated (REST, MCP, Kafka, HTTP, port); the Inbound and Outbound
Communication tables carry the exact route, tool, topic and CloudEvents
type for every message.

### Inbound

```mermaid
%%{init: {"flowchart": {"nodeSpacing": 14, "rankSpacing": 40, "padding": 6}}}%%
flowchart LR
    classDef bc fill:#fff3bf,stroke:#e67700,stroke-width:1.5px,color:#000
    classDef card fill:#fff9db,stroke:#f08c00,color:#000,text-align:left
    classDef ctx fill:#e7f5ff,stroke:#1c7ed6,color:#000
    classDef actor fill:#fff4e6,stroke:#e8590c,color:#000
    classDef ext fill:#ffe3e3,stroke:#c92a2a,color:#000
    classDef self fill:#f1f3f5,stroke:#868e96,color:#000
    classDef none fill:#f8f9fa,stroke:#adb5bd,stroke-dasharray:5 5,color:#495057
    subgraph IN["Inbound - who commands, queries or feeds it"]
        direction TB
        in0(["Planner (human, console,<br/>agent)"]):::actor
        in1["order-management"]:::ctx
        in2["product-master"]:::ctx
        in3["facility-layout"]:::ctx
    end
    subgraph BC["slotting-optimization - Supporting subdomain - WMS tier"]
        direction TB
        purpose["<b>Purpose</b><br/>Decide, explainably and repeatably, which SKUs get a<br/>forward pick slot and which slot each one gets; let a<br/>person approve or reject the proposal; publish the<br/>approved assignment map with the physical moves it<br/>implies."]:::card
        roles["<b>Domain roles</b><br/>- Policy engine (planning.Planner, abc-velocity-v1)<br/>- Approval workflow (Draft to Approved or Rejected; previous<br/>Approved becomes Superseded)"]:::card
        rules["<b>Business decisions it enforces</b><br/>- Candidates: SKUs with at least one ACTIVE demand line due<br/>in the window<br/>- Rank: picks desc, units desc, SKU asc; ABC by cumulative<br/>pick share (80 / 95 %)<br/>- Forward slots: active Storage slots in the<br/>FORWARD_ZONE_CODES zones of the site<br/>- Eligibility: hazmat and temperature match, one unit fits<br/>volume and weight<br/>- Churn limit: keep the previous slot when still eligible<br/>and free<br/>- Nothing becomes the site map without approve; one Approved<br/>plan per site"]:::card
        purpose ~~~ roles ~~~ rules
    end
    in0 -->|"cmd - REST<br/>Generate slot plan, Approve,<br/>Reject"| BC
    in0 -.->|"qry - REST<br/>Get plan, List plans, Forward<br/>slots, SKU velocity"| BC
    in1 ==>|"evt - Kafka<br/>warehouse.order-management.events<br/>SiteSkuDemandChanged"| BC
    in2 ==>|"evt - Kafka<br/>warehouse.product-master.events<br/>ProductClassified,<br/>ProductDimensionsDeclared,<br/>ProductMeasured"| BC
    in3 ==>|"evt - Kafka<br/>warehouse.facility.events<br/>ZoneRegistered,<br/>LocationSlotRegistered,<br/>LocationSlotDecommissioned"| BC
    linkStyle 0 stroke:#1971c2,stroke-width:1.5px
    linkStyle 1 stroke:#2f9e44,stroke-width:1.5px
    linkStyle 2 stroke:#e8590c,stroke-width:2.5px
    linkStyle 3 stroke:#e8590c,stroke-width:2.5px
    linkStyle 4 stroke:#e8590c,stroke-width:2.5px
    class BC bc
```

### Outbound

```mermaid
%%{init: {"flowchart": {"nodeSpacing": 14, "rankSpacing": 40, "padding": 6}}}%%
flowchart LR
    classDef bc fill:#fff3bf,stroke:#e67700,stroke-width:1.5px,color:#000
    classDef card fill:#fff9db,stroke:#f08c00,color:#000,text-align:left
    classDef ctx fill:#e7f5ff,stroke:#1c7ed6,color:#000
    classDef actor fill:#fff4e6,stroke:#e8590c,color:#000
    classDef ext fill:#ffe3e3,stroke:#c92a2a,color:#000
    classDef self fill:#f1f3f5,stroke:#868e96,color:#000
    classDef none fill:#f8f9fa,stroke:#adb5bd,stroke-dasharray:5 5,color:#495057
    BC["<b>slotting-optimization - Supporting subdomain - WMS tier</b>"]:::bc
    subgraph OUT["Outbound - whom it calls or notifies"]
        direction TB
        out0["wes-work-planning<br/>(RELOCATE work per move)"]
    end
    BC ==>|"evt - Kafka<br/>warehouse.slotting-optimization.events<br/>SlotPlanGenerated,<br/>SlotPlanApproved,<br/>SlotPlanRejected"| out0
    linkStyle 0 stroke:#e8590c,stroke-width:2.5px
```

Source: the Inbound Communication and Outbound Communication tables of this
page, which map to `internal/adapters/inbound/{http,kafka,mcp}` and `internal/adapters/outbound/`. The diagram omits assumptions, verification metrics, open
questions and the ubiquitous language (sections below), the relationship
pattern per message (OHS, ACL, Conformist, Customer/Supplier - see the
tables) and anything the tables mark as deprecated or platform-only
(health probes).

## Name

**slotting-optimization**

## Purpose

Decide, explainably and repeatably, which SKUs get a forward pick slot and
which slot each one gets; let a person approve or reject that proposal; and
publish the approved assignment map with the physical moves it implies.

## Strategic classification

| Dimension | Value |
| --- | --- |
| Domain | **Supporting** ([Subdomain classification](/docs/ddd/subdomain-classification)) |
| Tier | WMS, CloudEvents subdomain `wms` |
| Business model | Cost reduction (pick travel and labor) |
| Evolution | Custom-built, genesis |

## Domain roles

- **Policy engine**: computes a proposal from declarative inputs
  (`planning.Planner`, policy `abc-velocity-v1`).
- **Approval workflow**: a Draft becomes Approved or Rejected by a human
  decision; the previous Approved plan of the site becomes Superseded.

## Inbound communication

```mermaid
flowchart LR
    om["order-management"] -->|"SiteSkuDemandChanged (event)"| so["slotting-optimization"]
    pm["product-master"] -->|"ProductClassified, ProductDimensionsDeclared, ProductMeasured (events)"| so
    fl["facility-layout"] -->|"ZoneRegistered, LocationSlotRegistered, LocationSlotDecommissioned (events)"| so
    pl["planner, console, agent"] -->|"Generate, Approve, Reject (REST commands)"| so
    pl -->|"plans, forward slots, velocity (REST queries)"| so
```

Source: `internal/adapters/inbound/kafka/{demand,product,layout}_consumer.go`,
`internal/adapters/inbound/http/server.go`.

| Collaborator | Message | Kind | Channel |
| --- | --- | --- | --- |
| order-management | `SiteSkuDemandChanged` | event | `warehouse.order-management.events` |
| product-master | `ProductClassified`, `ProductDimensionsDeclared`, `ProductMeasured` | events | `warehouse.product-master.events` |
| facility-layout | `ZoneRegistered`, `LocationSlotRegistered`, `LocationSlotDecommissioned` | events | `warehouse.facility.events` |
| planner (human, console, agent) | Generate slot plan, Approve, Reject | commands | `POST /slot-plans`, `POST /slot-plans/{planId}/approve`, `POST /slot-plans/{planId}/reject` |
| planner (human, console, agent) | Get plan, List plans, Forward slots, SKU velocity | queries | `GET /slot-plans/{planId}`, `GET /slot-plans`, `GET /forward-slots`, `GET /sku-velocity` |

## Outbound communication

```mermaid
flowchart LR
    so["slotting-optimization"] -->|"SlotPlanGenerated, SlotPlanApproved, SlotPlanRejected (events)"| topic["warehouse.slotting-optimization.events"]
    topic -->|"SlotPlanApproved: RELOCATE work per move"| exec["wes-work-planning, fulfillment-execution"]
    exec -->|"StockRelocated via inventory-storage"| so
```

Source: `internal/adapters/outbound/kafka/encoder.go`, ADR 0001 (context map).

| Message | Kind | Channel | Consumers |
| --- | --- | --- | --- |
| `SlotPlanGenerated` | event | `warehouse.slotting-optimization.events` | none yet |
| `SlotPlanApproved` | event | same | wes-work-planning (RELOCATE work per move, its ADR 0040; ADR 0010) |
| `SlotPlanRejected` | event | same | none yet |

## Ubiquitous language

SlotPlan, window, lookback, policy, Draft, Approved, Rejected, Superseded,
assignment, move (Assign, Relocate, Vacate), unassigned (NoEligibleSlot,
NoPhysicalProfile, NoCapacityFit), ABC class, velocity (picks, units),
forward slot, eligibility, stickiness, local copy. Full glossary:
[Ubiquitous language](/docs/ddd/ubiquitous-language).

## Business decisions

| Decision | Rule in code |
| --- | --- |
| Who is a candidate | SKUs with at least one ACTIVE demand line due in the window |
| Rank of candidates | picks desc, units desc, SKU asc |
| ABC class | cumulative pick share before the SKU: below 80 % is A, below 95 % is B, else C |
| Which slots are forward slots | active, role `Storage`, zone of the site, zone code in `FORWARD_ZONE_CODES` |
| Eligibility | hazmat matches, temperature class matches (empty is Ambient), one unit fits volume and weight |
| Slot ranking | lexical by location code (v1, a port) |
| Churn limit | keep the previous slot when still eligible and free |
| Human approval | nothing becomes the site's map without `approve`; one Approved plan per site |

## Assumptions

- Forward slots are `Storage` slots in zones listed in `FORWARD_ZONE_CODES`
  (default `FWD`); one SKU per forward slot.
- The demand `site_id` equals the facility `siteCode` (ADR 0003 calls for
  verifying this against live data).
- The local copies are current enough: a stale copy yields a partial plan
  that lists unplaceable SKUs instead of guessing.

## Verification metrics

- Deterministic output: same input, same plan (planner tests).
- Mutation gate: 99 % efficacy and mutant coverage on both domain packages.
- Coverage gate: 90 % over domain and application.
- Golden CloudEvents tests per published type and
  `TestEventCatalogueMatchesContract`.

## Open questions

- Who executes the moves? Decided:
  [ADR 0010](/docs/adr/0010-approved-slot-plans-are-executed-as-wes-relocation-work)
  (wes-work-planning RELOCATE work, inventory-storage records it; progress
  comes back as `StockRelocated`). Should a new proposal wait for the
  previous plan's execution? Not in v1.
- Which consumer will read `SlotPlanApproved` first?
- A travel-distance ranking needs layout geometry the consumer does not read
  yet (`LocationGeometryUpdated` is ignored today).
