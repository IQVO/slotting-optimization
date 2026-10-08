---
paths:
  - "internal/domain/**"
  - "internal/application/**"
  - "features/**"
---

# Domain model: ubiquitous language, aggregates, events, use cases

## Ubiquitous Language (use these exact names)

- **SlotPlan**: a reviewable proposal that assigns SKUs to forward pick slots
  for one site over a demand window, plus the human decision on it. Aggregate
  root, identity = `PlanID` (`plan-` + unique suffix).
- **Site**: 1..64 characters, no whitespace, control characters or `/`. SKU and
  slot code (the facility-layout location code) follow the same token rule.
- **Window**: half-open `[From, To)` in UTC, `From` strictly before `To`.
- **Policy**: the name of the planning policy that produced the plan;
  `abc-velocity-v1` (`slotplan.PolicyABCVelocityV1`).
- **State**: `Draft | Approved | Rejected | Superseded`.
- **Assignment**: `{SKU, Slot, ABCClass, Picks, Units}`; `Picks` >= 1 (ACTIVE
  demand lines in the window), `Units` >= 0.
- **ABCClass**: `A | B | C`, by the cumulative pick share of the SKUs ranked
  strictly before: below 80 % is A, below 95 % is B, else C.
- **Move**: `{SKU, From?, To?, Kind}`; `Assign` (no From), `Relocate` (From and
  To differ), `Vacate` (no To).
- **Unassigned**: `{SKU, Reason}`; `NoEligibleSlot`, `NoPhysicalProfile`,
  `NoCapacityFit`.
- **Forward slot**: an active `Storage` slot in a zone listed in
  `FORWARD_ZONE_CODES` (default `FWD`). One SKU per forward slot (v1).
- **Stickiness**: a SKU keeps its previous slot while that slot is still
  eligible and free (the churn limit).
- **SlotRankingPolicy**: the port that orders slots best first; v1 =
  lexical order of the location code.

## Aggregates and domain services

- **SlotPlan** (`internal/domain/slotplan`): a slot holds at most one SKU and a
  SKU is in at most one slot; moves and unassigned entries agree with the
  assignments; `Approve` and `Reject` only from `Draft`; `Supersede` only from
  `Approved`; Approved, Rejected and Superseded plans never change. Content is
  stored sorted by SKU. `Generate` and `Restore` run the same validation; the
  decision timestamps must match the state. At most ONE Approved plan per site
  spans plans, so the use case enforces it (supersede the previous plan in the
  same unit of work) and a partial unique index backs it (ADR 0002).
- **planning.Planner** (`internal/domain/planning`): pure domain service, policy
  `abc-velocity-v1`. No I/O, no clock reads; same input in any order gives the
  same output. Candidates = SKUs with picks > 0 (picks desc, units desc, SKU
  asc). Decision logic is written as `if` chains with early returns, because
  gremlins under-reports tagless `switch`.
- Both packages are under the mutation gate (`make mutation` runs gremlins on
  each); keep them at the measured efficacy.

## Domain events (all carry plan id, site id, version and occurred-at)

- `SlotPlanGenerated` (window, policy, assignment/move/unassigned counts): raised by `Generate`.
- `SlotPlanApproved` (supersedes plan id, FULL assignments, moves): raised by `Approve`.
- `SlotPlanRejected` (reason): raised by `Reject`. `Supersede` raises no event.

## Key use cases (`internal/application/usecases`, built in the service phase)

- `GenerateSlotPlan`: read the demand, product and layout copies, run the
  planner against the site's Approved map, store the Draft; idempotent per
  `Idempotency-Key`.
- `ApprovePlan`: load the plan and the site's Approved plan, `Approve`
  (+ `Supersede` the previous) and enqueue the events, in ONE unit of work.
- `RejectPlan`: load, `Reject`, enqueue.
- `GetSlotPlan`, `ListSlotPlans`, `ListForwardSlots`, `ListSkuVelocity`: reads.

Every write: load, apply the command, save with the loaded version as the
optimistic guard, and enqueue the events in the outbox in ONE unit of work.
