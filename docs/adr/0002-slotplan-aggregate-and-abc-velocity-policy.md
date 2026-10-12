# ADR 0002: the SlotPlan aggregate, its invariants and the abc-velocity-v1 policy

## Status

Accepted (2026-10-08).

## Context

ADR 0001 makes slotting-optimization the owner of the decision "which SKU gets
which forward pick slot". This ADR fixes the aggregate that holds a proposal
and its decision, the rules that make a proposal valid, and the policy that
computes one.

## Decision

### One aggregate, `SlotPlan`

`SlotPlan` (id `plan-<uuid>`) is the consistency boundary of one proposal:

- `siteId`, window `[from, to)` (half-open, UTC), `policy` name
  (`abc-velocity-v1`), `state`, `version`.
- `assignments`: `[{sku, slot, abcClass, picks, units}]`.
- `moves`: `[{sku, fromSlot?, toSlot?, kind}]`, `kind` is `Assign`,
  `Relocate` or `Vacate`.
- `unassigned`: `[{sku, reason}]`, `reason` is `NoEligibleSlot`,
  `NoPhysicalProfile` or `NoCapacityFit`.
- Timestamps `generatedAt`, `approvedAt`, `rejectedAt`, `supersededAt`, the
  optional `rejectReason`, and `supersedesPlanId` (set on approval).

The plan is one aggregate because its assignments, moves and unassigned list
describe one coherent proposal: the invariants below span all of them (a slot
holds one SKU, a move agrees with the assignments). Nothing smaller can be
changed on its own, and plans do not reference each other except through the
single `supersedesPlanId` pointer.

### States

```
Draft --Approve--> Approved --Supersede--> Superseded
  |
  +----Reject----> Rejected
```

`Approved`, `Rejected` and `Superseded` are immutable: their content never
changes and no transition leaves them except `Approved -> Superseded`.

### Invariants

1. A slot holds at most one SKU, and a SKU is in at most one slot (v1).
2. Every assignment has a valid SKU, slot code, ABC class, `picks >= 1` and
   `units >= 0`.
3. Moves agree with the assignments and with themselves: at most one move per
   SKU; `Assign` has a `toSlot` equal to the SKU's assigned slot and no
   `fromSlot`; `Relocate` has both, they differ, and `toSlot` is the assigned
   slot; `Vacate` has a `fromSlot`, no `toSlot`, and the SKU has no
   assignment.
4. An unassigned SKU appears once and is not also assigned.
5. `Approve` only from `Draft`. It raises `SlotPlanApproved` carrying the
   **full** assignment map and the moves, so a consumer needs no lookup, and
   `supersedes_plan_id`, the site's previously Approved plan, if any.
6. `Reject(reason)` only from `Draft`; the reason is optional, trimmed, at
   most 500 characters.
7. `Supersede` only from `Approved`. It raises no event: consumers learn of it
   from `supersedes_plan_id` on the newer `SlotPlanApproved`.
8. Rebuilding a plan from persisted state goes through the same validation
   (`Restore`), so a corrupt row cannot become a live aggregate; the decision
   timestamps must match the state exactly.

Commands return the raised events. `version` is 1 at generation and +1 per
accepted transition; it guards the repository write (optimistic concurrency,
409 on a race).

### At most one Approved plan per site

This rule spans plans, so no single aggregate can enforce it. Two layers do:

1. **Use case.** `ApprovePlan` loads the plan, loads the site's currently
   Approved plan (if any), calls `plan.Approve(previousId)` and
   `previous.Supersede()`, and saves both with their loaded versions plus the
   outbox rows in ONE unit of work.
2. **Database.** A partial unique index on the plans table,
   `UNIQUE (site_id) WHERE state = 'Approved'`, makes a race lose with a
   constraint violation (mapped to 409 `approved-plan-conflict`) instead of
   leaving two Approved plans.

### Human approval

Generation never changes the forward map. A planner generates a Draft, reviews
the assignments, moves and unassigned reasons, then approves or rejects it. Only
`SlotPlanApproved` changes what the world believes about forward slots. There
is no auto-approve in v1.

### Policy `abc-velocity-v1`

Implemented as `planning.Planner`, a pure function of its input (no I/O, no
clock). The same input in any order yields the same output.

**Input.** SKU velocities `[{sku, picks, units}]` (picks = ACTIVE demand lines
in the window, units = their summed units), a profile per SKU (handling tags,
temperature class, effective unit volume in mm3 and weight in g, or absent),
the active forward slots (code, zone code, temperature class, hazmat flag, max
weight kg, max volume m3), the previous assignment map `sku -> slot`, and the
class thresholds.

**Candidates.** SKUs with `picks > 0`, ordered by picks descending, units
descending, SKU ascending.

**ABC class.** By the cumulative share of picks held by the candidates ranked
strictly before a SKU: below 80 % of all picks is `A`, below 95 % is `B`, else
`C`. (So the SKU that crosses the 80 % line is still `A`, and the top SKU is
always `A`.)

**Slot eligibility for a SKU.**

- It needs an effective physical profile (declared or measured dimensions);
  otherwise it is unassigned `NoPhysicalProfile`.
- Hazmat SKUs (tag `Hazmat`) only go to hazmat slots; other SKUs only to
  non-hazmat slots.
- The SKU's temperature class (absent = `Ambient`) must equal the slot zone's.
- One effective unit must fit: `volume_mm3 <= maxVolumeM3 * 1e9` and
  `weight_g <= maxWeightKg * 1000`. A slot with no capacity holds nothing.

**Unassigned reasons.** `NoPhysicalProfile` as above. `NoCapacityFit`: slots
compatible by hazmat and temperature exist but none can hold one unit.
`NoEligibleSlot`: no compatible slot exists, or every eligible slot was taken
by a higher-ranked SKU.

**Slot ranking.** The `SlotRankingPolicy` port orders slots best first and
must return each slot exactly once. The v1 implementation is the lexical order
of the location code, because facility-layout events carry no travel distance.
A travel-distance policy is a later version behind the same port; it needs
geometry events from facility-layout and gets its own ADR.

**Stickiness (churn limit).** Candidates are walked in rank order. A SKU whose
previous slot is still eligible and not yet taken keeps it; every other
candidate takes the best free eligible slot. Rank order outranks stickiness:
a better-ranked SKU may take a slot another SKU used to hold, which then
relocates. This keeps day-to-day churn small without freezing the map.

**Moves.** A SKU with a slot and no previous slot is an `Assign`; a different
slot than before is a `Relocate` (`from` = previous); a previously assigned SKU
that is not assigned now (no demand, no profile, no slot, or its slot was
decommissioned and nothing replaced it) is a `Vacate`. A SKU that keeps its
slot has no move. Moves are sorted by SKU.

### Execution of approved moves is PLANNED, not built

Turning `Assign`/`Relocate`/`Vacate` into floor work (MOVE and REPLENISH tasks
through the process-path-management catalogue, wes-work-planning and
fulfillment-execution) touches three other contexts and a new task type. It is
its own ADR-gated phase in those contexts. Until then an approved plan is an
authoritative, published recommendation; nothing moves stock.

### Explicitly out of scope (later ADRs)

Replenishment min/max and triggers, affinity or ML slotting, multi-SKU slots,
reserve-zone slotting, travel-distance ranking, automatic approval, partial
approval of a plan.

## Consequences

- Every rule above is enforced in `internal/domain/slotplan` and
  `internal/domain/planning`; both packages sit under the mutation gate.
- A plan is cheap to review: it lists what changes (moves) and what could not
  be placed (unassigned, with a reason).
- Because stickiness is applied on one pass in rank order, a rare cascade is
  possible (A takes B's slot, B takes C's, ...). That is accepted for v1 and
  visible in the moves.
