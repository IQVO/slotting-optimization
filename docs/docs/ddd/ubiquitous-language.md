---
id: ubiquitous-language
title: Ubiquitous language
sidebar_label: Ubiquitous language
sidebar_position: 1
---

# Ubiquitous language

The words of `slotting-optimization` and the code identifier that
implements each. When a word here and a name in the code differ, the code
is right and this page is a bug.

## The plan

| Term | Meaning | Code |
| --- | --- | --- |
| Slot plan | A reviewable proposal of SKU-to-forward-slot assignments for one site over one demand window, produced by a policy, plus the human decision on it. The aggregate root. | `slotplan.SlotPlan` |
| Plan id | `plan-` followed by a UUID v4 (minted by `ids.UUID`); at most 64 characters, no whitespace, control characters or `/`. | `slotplan.PlanID`, `NewPlanID` |
| Site | The warehouse a plan is for. Must equal the facility-layout `siteCode` and the demand `site_id`. | `slotplan.SiteID` |
| Window | The half-open demand window `[from, to)` a plan was computed over; `to` is the generation time, `from` is `to` minus the lookback. | `slotplan.Window` |
| Lookback | Length of the window in days, 1 to 365 (default 28, `LOOKBACK_DAYS`). | `usecases.DefaultLookbackDays`, `MaxLookbackDays` |
| Policy | The named algorithm that produced a plan. v1 is `abc-velocity-v1`. | `slotplan.PolicyName`, `PolicyABCVelocityV1` |
| Draft | A generated plan awaiting a decision. The only state that can be approved or rejected. | `slotplan.StateDraft` |
| Approved | The site's current forward-slot map. At most one per site. | `slotplan.StateApproved` |
| Rejected | A Draft a person turned down, with an optional reason (at most 500 characters). Final. | `slotplan.StateRejected` |
| Superseded | A former Approved plan replaced by a newer approval of the same site. Final. | `slotplan.StateSuperseded`, `SlotPlan.Supersede` |
| Version | Optimistic-concurrency counter: 1 at generation, +1 per decision or supersession. | `SlotPlan.Version` |

## What a plan contains

| Term | Meaning | Code |
| --- | --- | --- |
| Assignment | One SKU placed in one forward slot, with its ABC class, picks and units. A slot holds at most one SKU and a SKU is in at most one slot. | `slotplan.Assignment` |
| Move | A physical change the plan implies against the site's previous Approved map. | `slotplan.Move`, `MoveKind` |
| Assign | A SKU with no previous slot gets one (`to` only). | `slotplan.MoveAssign` |
| Relocate | A SKU moves from its previous slot to a different one (`from` and `to`). | `slotplan.MoveRelocate` |
| Vacate | A SKU that had a slot gets none in the new plan (`from` only). | `slotplan.MoveVacate` |
| Unassigned | A candidate SKU that got no slot, and why. | `slotplan.Unassigned`, `UnassignedReason` |
| NoPhysicalProfile | The SKU has no effective dimensions in the product copy. | `slotplan.ReasonNoPhysicalProfile` |
| NoEligibleSlot | No forward slot is compatible (hazmat, temperature), or every compatible one that fits is taken. | `slotplan.ReasonNoEligibleSlot` |
| NoCapacityFit | Compatible slots exist but one unit exceeds the volume or weight of all of them. | `slotplan.ReasonNoCapacityFit` |

## The planning policy

| Term | Meaning | Code |
| --- | --- | --- |
| Planner | The pure domain service that computes a proposal from its input alone (no I/O, no clock). | `planning.Planner` |
| Velocity | Per SKU over the window: **picks** = number of ACTIVE demand lines, **units** = their summed units. | `planning.SkuVelocity` |
| Candidate | A SKU with at least one pick, ordered picks desc, units desc, SKU asc. | `planning.candidates` |
| ABC class | A while the picks of the candidates ranked before a SKU are below 80 % of all picks, B below 95 %, else C. | `slotplan.ABCClass`, `planning.DefaultParameters` |
| Forward slot | An active location slot with role `Storage` in a zone of the site whose code is in `FORWARD_ZONE_CODES` (default `FWD`). | `planning.Slot`, `SlotCatalogue.ForwardSlots` |
| Eligibility | Compatible (a `Hazmat` SKU only in a hazmat zone and a non-hazmat SKU only in a non-hazmat zone; same temperature class, empty meaning `Ambient`) and fits (one unit within `maxVolumeM3` and `maxWeightKg`). | `planning.compatibleWith`, `planning.holds` |
| Slot ranking | The order in which free eligible slots are offered. v1 ranks by location code (`LexicalRanking`) because layout events carry no travel distance. | `planning.SlotRankingPolicy` |
| Stickiness | A SKU keeps its previous slot if that slot is still eligible and free; a better-ranked SKU may still take it first. The churn limit. | `planning.placer.choose` |
| Unit size | The effective volume (mm³) and weight (g) of one unit: measured if present, else declared. | `planning.UnitSize` |

## The local copies

| Term | Meaning | Code |
| --- | --- | --- |
| Local copy | An event-fed table of facts another context owns, read at plan time instead of calling that context. | ADR 0003 |
| Demand line | One source order line: site, SKU, units, due time, `ACTIVE` or `REMOVED`. Last writer wins. | `repository.DemandLine`, `demand_lines` |
| Product profile | Handling tags, temperature class and effective unit size of a SKU, guarded by product-master's version. | `repository.ProductProfile`, `product_profiles` |
| Zone, slot | Facility-layout's zones and location slots; a decommissioned slot never comes back. | `repository.Zone`, `repository.Slot`, `zones`, `slots` |
| Outcome | What a consumer did with a message: `applied`, `duplicate`, `stale`, `ignored`. | `usecases.Outcome` |
| Claim | The record that a consumer processed a CloudEvents id, taken in the same transaction as the effect. | `ports.ProcessedEvents`, `processed_events` |

## Events

| Term | Meaning | Code |
| --- | --- | --- |
| SlotPlanGenerated | A Draft was created; carries its size. | `slotplan.SlotPlanGenerated` |
| SlotPlanApproved | A Draft became the site's map; carries the full `{sku, slot}` map, the moves and the superseded plan id. | `slotplan.SlotPlanApproved` |
| SlotPlanRejected | A Draft was turned down; carries the reason. | `slotplan.SlotPlanRejected` |
