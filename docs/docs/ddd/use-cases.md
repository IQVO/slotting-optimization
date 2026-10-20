---
id: use-cases
title: Use cases
sidebar_label: Use cases
sidebar_position: 2
---

# Use cases

Every use case in `internal/application/usecases`, with what triggers it,
what it takes, what it checks and what it raises. Write use cases share one
shape (`writer.go`): inside **one** `ports.UnitOfWork`, load the `SlotPlan`,
apply the aggregate command, save it guarded by the loaded `version`, and
insert the raised events into the outbox. Consumer use cases share another
(`consumers.go`, `Intake`): claim `(consumer, CloudEvents id)` in
`processed_events` and apply the effect in the same unit of work.

## Commands (REST)

| Use case | Trigger | Inputs | Checks | Raises |
| --- | --- | --- | --- | --- |
| `GeneratePlan` | `POST /slot-plans` (wrapped by the `Idempotency-Key` middleware on Postgres) | `siteId?` (default `DEMAND_SITE_ID`, then `DEFAULT_SITE_ID`), `lookbackDays?` (default `LOOKBACK_DAYS`, then 28) | site id is a valid token (`ErrNoSite` when none at all); lookback 1 to 365; window `[now - days, now)`; the computed plan passes every `SlotPlan` invariant (one slot per SKU, one SKU per slot, moves and unassigned consistent with the assignments) | `SlotPlanGenerated` |
| `ApprovePlan` | `POST /slot-plans/{planId}/approve` | `planId` | the id starts with `plan-`; the plan exists; it is `Draft` (`ErrNotDraft` before anything is touched); the site's current Approved plan, if any, is superseded and saved **first** with its own version guard (a lost race is `ErrApprovedPlanConflict`); the partial unique index allows one Approved plan per site | `SlotPlanApproved` (with `supersedes_plan_id`, the full map and the moves) |
| `RejectPlan` | `POST /slot-plans/{planId}/reject` | `planId`, `reason?` | the plan exists and is `Draft`; the trimmed reason has at most 500 characters | `SlotPlanRejected` |

### How `GeneratePlan` computes a plan

1. `DemandLedger.Velocity`: per SKU, the ACTIVE demand lines of the site due
   inside the window (`picks` = lines, `units` = their sum).
2. `ProfileDirectory.Many`: handling tags, temperature class and effective
   unit size of those SKUs.
3. `SlotCatalogue.ForwardSlots`: active `Storage` slots in zones of the site
   whose code is in `FORWARD_ZONE_CODES`.
4. `SlotPlanRepository.Current`: the site's Approved plan, as the previous
   map for stickiness.
5. `planning.Planner.Plan` (policy `abc-velocity-v1`, pure, deterministic):
   candidates are SKUs with picks, ordered picks desc, units desc, SKU asc;
   ABC class from the share of picks before each SKU (A below 80 %, B below
   95 %, else C); each SKU keeps its previous slot if still eligible and
   free, else takes the best free eligible slot (slots ranked by location
   code, `LexicalRanking`); moves are the diff against the previous map.
6. `slotplan.Generate` creates the Draft at version 1; save and enqueue.

## Queries (REST)

| Use case | Trigger | Inputs | Checks | Result |
| --- | --- | --- | --- | --- |
| `GetPlan` | `GET /slot-plans/{planId}`, MCP `get_slot_plan` | `planId` | valid id; exists | the plan plus its `execution` progress (`GetPlan.View`, ADR 0010): moves total and executed, last executed at, per move executed flag and quantity, derived from `slot_plan_move_executions` |
| `ListPlans` | `GET /slot-plans` | `limit?` (1 to 500, default 100), `cursor?`, `siteId?`, `state?` | each parameter (`invalid-limit`, `invalid-cursor`, `invalid-site-id`, `invalid-state`) | a page newest first (`generated_at`, then id, descending) and `nextCursor` while more remain |
| `ListForwardSlots` | `GET /forward-slots` | `siteId?` (default site) | site resolvable | the Approved plan's `{sku, slot, abcClass}` list; an empty list (not an error) when the site has none |
| `ListSkuVelocity` | `GET /sku-velocity` | `siteId?`, `limit?`, `windowDays?` (1 to 365, default 28) | site, limit and window (an invalid window reports `invalid-limit`) | the velocity of the window, truncated to `limit` |

## Consumers (Kafka)

All seven return an `Outcome` (`applied`, `duplicate`, `stale`, `ignored`,
and for `RecordRelocation` also `unmatched`) that the adapter logs. A payload
that can never be applied is wrapped in `ErrInvalidEvent` **before** anything
is claimed; the adapter skips it, except the StockRelocated consumer, which
dead-letters it at once. None of them raises a domain event or writes to the
outbox.

| Use case | Trigger (type on topic) | Claim name | Checks | Effect |
| --- | --- | --- | --- | --- |
| `ApplyDemandChanged` | `SiteSkuDemandChanged` on `warehouse.order-management.events` | `slotting-demand` | site id and SKU tokens; `source_order_id` and `line_no >= 1`; `due_at` present; `state` is `ACTIVE` or `REMOVED`; ACTIVE needs `demanded_units >= 1`; a site other than `DEMAND_SITE_ID` (when set) is `ignored` | upsert of the line, last writer wins; REMOVED keeps the row inactive |
| `ApplyProductClassified` | `ProductClassified` on `warehouse.product-master.events` | `slotting-product` | SKU token; temperature class empty, `Ambient`, `Chilled` or `Frozen`; `version >= 1` | replaces tags and temperature class when `version` is newer, else `stale` |
| `ApplyPhysicalProfile` | `ProductDimensionsDeclared` or `ProductMeasured` on `warehouse.product-master.events` | `slotting-product` | SKU token; `version >= 1`; effective volume and weight at least 1 when present | replaces the effective unit size (or clears it when `effective_source` is `none`) when `version` is newer, else `stale` |
| `ApplyZoneRegistered` | `ZoneRegistered` on `warehouse.facility.events` | `slotting-layout` | temperature class known; `zoneId`, `siteCode`, `zoneCode` present | upsert of the zone |
| `ApplyLocationSlotRegistered` | `LocationSlotRegistered` on `warehouse.facility.events` | `slotting-layout` | location code token; `zoneId` present; capacities not negative; empty role means `Storage` | upsert of the slot; a decommissioned slot stays decommissioned |
| `ApplyLocationSlotDecommissioned` | `LocationSlotDecommissioned` on `warehouse.facility.events` | `slotting-layout` | location code token | `active = false` for good, also for a slot never seen before |
| `RecordRelocation` (ADR 0010) | `StockRelocated` on `warehouse.inventory.events` | `slotting-relocation` | `relocation_id`, `site_code`, `sku`, `from_bin`, `to_bin` present; `quantity >= 1`; `occurred_at` present (else `ErrInvalidEvent`, dead-lettered) | matches the move (`source_ref` = `<plan_id>:<move_seq>` of an Approved or Superseded plan of the site whose move there is for the SKU; else the site's Approved plan's move for the SKU whose bins fit its kind) and records it in `slot_plan_move_executions` once per `relocation_id` (`applied`, or `duplicate` for a replayed relocation); no match is `unmatched` with a reason, claimed and counted, never dead-lettered. Proposals are not gated on it. |

## Analytics read side (not in this package)

The projector and the reports binary (ADR 0006) do not go through
`internal/application/usecases`: their logic is the self-contained region
`internal/analytics/report`, behind its own `Projection` and `Reader` ports.

| Operation | Trigger | Inputs | Checks | Effect / result |
| --- | --- | --- | --- | --- |
| Project an event (`Projection.Apply`) | a `SlotPlanGenerated`, `SlotPlanApproved` or `SlotPlanRejected` on `warehouse.slotting-optimization.analytics` (`cmd/slotting-projector`) | CloudEvents `id`, `time`, `subject`; `data.plan_id`, `data.site_id`, `data.supersedes_plan_id` (approvals) | `plan_id` equals `subject`; id, time, plan and site present; only an approval may supersede (`Event.Validate`, else `ErrInvalidEvent`: dead-lettered) | claims the id in `analytics_processed_events` and upserts `slot_plan_facts` in one transaction; a replayed id changes nothing |
| Slot-plan funnel (`Reader.FunnelDays`) | `GET /reports/slot-plan-funnel` | `from?`, `to?` (RFC 3339), `siteId?`, `granularity?` | range valid, not empty, at most 366 days (`invalid-report-range`); granularity `day` (`invalid-report-granularity`) | one row per UTC day of the range, zeros included: plans generated, approved, rejected, approval latency (count, p50, p95 in seconds) and supersessions |
| Freshness (`Reader.LastEventAt`) | `GET /reports/freshness` | none | | `as_of` (newest applied event time) and `lag_seconds` (never negative), both `null` before the first event |

## Not use cases

- **Supersede** is not triggered on its own: it happens inside
  `ApprovePlan` and raises no event.
- There is **no scheduler**: plans are generated only on request.
- The outbox relay and the HTTP idempotency middleware are adapters, not
  use cases.
