# ADR 0001: slotting-optimization as the owner of forward-pick slotting decisions

## Status

Accepted (2026-10-08).

## Context

The fleet knows where things physically are (`facility-layout` models sites,
zones, aisles and coded slots, including forward and reserve zones) and what a
SKU is (`product-master`: handling classification and physical profile). It
knows what customers ordered (`order-management`) and where stock sits
(`inventory-storage`). Nothing decides **which SKUs deserve a forward pick
slot, and which slot each one gets**. Today that decision is implicit in
whoever stows first, so fast movers can end up in far or awkward slots while
slow movers occupy the best ones. Gartner's 2026 WMS Magic Quadrant lists
slotting among the capabilities "expected as standard"; Manhattan, Blue Yonder,
SAP EWM and Infor all ship it.

Slotting is a planning computation over declarative inputs (demand, product
facts, slot facts). It has a different lifecycle from every existing context
(it runs when a planner asks, produces a proposal, and a human accepts or
rejects it), so it is a context of its own, not a feature of `facility-layout`
(which owns structure, not demand) or `inventory-storage` (which owns the
stock ledger and must stay available on the floor).

## Decision

Introduce `slotting-optimization` as a new bounded context. It owns the
**SlotPlan**: a reviewable proposal that assigns SKUs to forward pick slots for
one site over a demand window, and the human decision taken on it.

### Bounded context canvas

| Field | Value |
|---|---|
| Name | slotting-optimization |
| Purpose | Decide, explainably and repeatably, which SKUs get a forward pick slot and which slot, and publish the approved assignment map. |
| Strategic classification | **Supporting subdomain**. Necessary for efficient picking and specific to this warehouse's policy, but replaceable policy objects (ranking, eligibility) rather than the competitive core; the WES conductor stays Core. |
| Tier / CloudEvents subdomain | **WMS** ("what and where"), subdomain `wms`: the fifth `wms` context after `facility-layout`, `inventory-storage`, `product-master` and `inbound-receiving`. Type prefix `com.warehouse.wms.slotting-optimization.`, source `/warehouse/slotting-optimization`. |
| Domain roles | Policy engine (computes proposals), approval workflow (Draft, then Approved or Rejected). |
| Inbound communication | Events from `order-management`, `product-master`, `facility-layout` (ADR 0003); REST commands from planners (`POST /slot-plans`, approve, reject). |
| Outbound communication | `SlotPlanGenerated`, `SlotPlanApproved`, `SlotPlanRejected` on `warehouse.slotting-optimization.events` (ADR 0004); REST reads for operators, the console and agents. |
| Ubiquitous language | SlotPlan, window, policy, assignment, move (Assign, Relocate, Vacate), unassigned (NoEligibleSlot, NoPhysicalProfile, NoCapacityFit), ABC class, forward slot, stickiness. |
| Business decisions | Which SKUs are candidates; ABC class; slot eligibility; slot ranking; churn limit; human approval (ADR 0002). |
| Assumptions | Forward slots are `Storage` slots in zones listed in `FORWARD_ZONE_CODES` (default `FWD`); one SKU per forward slot; demand site id equals the facility site code (both verified against live data later, ADR 0003). |
| Verification | Deterministic output (same input, same plan), mutation gate over both domain packages, golden tests per published type. |

### The model (v1)

One aggregate, `SlotPlan`, and one pure domain service, `planning.Planner`
(policy `abc-velocity-v1`). The details and invariants are ADR 0002.

### Context map

| Edge | Relationship | Status |
|---|---|---|
| order-management -> slotting-optimization | Published Language (`SiteSkuDemandChanged`, `warehouse.order-management.events`); slotting-optimization is a **Conformist** and keeps a local demand copy | Contract verified on `origin/develop` 2026-10-08; consumer built in the service phase |
| product-master -> slotting-optimization | Published Language (`ProductClassified`, `ProductDimensionsDeclared`, `ProductMeasured`); **Conformist**, local product copy | Contract verified 2026-10-08; consumer in the service phase |
| facility-layout -> slotting-optimization | Open Host Service / Published Language (`ZoneRegistered`, `LocationSlotRegistered`, `LocationSlotDecommissioned`); **Conformist**, local slot copy | Contract verified 2026-10-08; consumer in the service phase |
| slotting-optimization -> warehouse-ops-agent, warehouse-console | **Open Host Service** (REST; MCP read tools in a later phase) | Planned, later phases |
| slotting-optimization -> process-path-management -> wes-work-planning -> fulfillment-execution | `SlotPlanApproved` carries the moves; turning them into MOVE/REPLENISH work through the process-path catalogue is the execution path | **PLANNED, not built** (ADR 0002): needs its own ADRs in those contexts and a new task type |

### Exclusions

- **No edge to `inventory-storage`.** An approved plan does not move stock and
  does not change stow behaviour. Stow stays an RF action (item scan, location
  scan). The forward-slot map is advice published for others to use.
- **No edge to `inbound-receiving` or `warehouse-planning`.**
- Out of scope for v1 (later ADRs): replenishment min/max and triggers,
  affinity / ML slotting, multi-SKU slots, reserve-zone slotting, a
  travel-distance ranking (needs layout geometry events, ADR 0002), seasonal
  re-slotting calendars.

### No live cross-context lookup, in either direction

- slotting-optimization never calls a sibling context at request time. It has
  no outbound HTTP client and no MCP client.
- No sibling calls slotting-optimization either. They read
  `warehouse.slotting-optimization.events`; the REST `GET` endpoints exist for
  operators, the console and agents.

## Consequences

- A new deployable (API, Postgres database, Kafka topic, Kong route
  `/api/slotting-optimization`) joins the fleet; `warehouse-infra` wires it by
  hand in the wiring wave.
- The fleet CloudEvents rule that names the `wms` contexts and the
  warehouse-docs subdomain table gain two rows (this context and
  `inbound-receiving`); that change travels in the docs wave, not in this repo.
- Plans are only as good as the local copies feeding them. An empty or stale
  copy yields an empty or partial plan; the plan lists SKUs it could not place
  with a reason instead of guessing.
- Nothing happens on the floor until the planned execution edge exists.
