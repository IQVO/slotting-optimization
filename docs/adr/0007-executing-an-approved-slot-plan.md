# ADR 0007: Executing an approved slot plan (options, undecided)

## Status

Proposed (2026-10-12, ecosystem gap fix D11). Nothing is decided and nothing
is built by this ADR. It lays out the options for turning the moves of an
Approved plan into work on the floor, so the decision can be taken with the
owners of the other contexts. [ADR 0001](./0001-slotting-optimization-bounded-context.md)
and [ADR 0002](./0002-slotplan-aggregate-and-abc-velocity-policy.md) stay as
they are: execution is a planned edge, and until it exists an Approved plan is
an authoritative, published recommendation that moves no stock.

The number 0006 is skipped on purpose: ADR 0004 reserves it for this
service's analytics read side.

## Context

An Approved plan ends in `SlotPlanApproved` on
`warehouse.slotting-optimization.events` (ADR 0004). The event carries the
full forward assignment map (`assignments: [{sku, slot}]`) and the moves
against the previous plan (`moves: [{sku, from_slot?, to_slot?, kind}]`,
`kind` one of `Assign`, `Relocate`, `Vacate`). No context consumes it on
`develop`, so an approval changes nothing physical.

What the siblings offer today (checked on `origin/develop`, 2026-10-12):

- **inventory-storage** owns the stock ledger and bins. Its REST surface has
  receive, stow, reservations, confirm-pick, cycle count, bin reads and the
  inter-site transfer receipt/stow (`/transfers/{transferLineId}/...`). It has
  **no intra-site bin-to-bin move command**: moving units from one bin to
  another is not a modelled operation.
- **wes-work-planning** plans and releases order-driven work. Its contracts
  have no task kind for MOVE or REPLENISH work, and work there is planned
  from orders, not from a slot map.
- **process-path-management** owns the path catalogue (`PICK`, `PACK`, ...)
  that routes work; there is no path for a stock move.
- **fulfillment-execution** executes released work with operators; it would
  receive whatever wes-work-planning releases.
- **warehouse-console** has no slotting screen yet.

A forward-slot move is physical work: someone takes the units of a SKU out
of its old slot (or out of reserve) and puts them into the new slot, and the
stock ledger must reflect each step. The question is which context turns a
move into that work, and which one records it.

## Options

### Option A: wes-work-planning plans MOVE / REPLENISH tasks

wes-work-planning consumes `SlotPlanApproved`, turns each move into a task
(a `Relocate` into "move SKU from slot X to slot Y", an `Assign` into
"replenish slot Y from reserve", a `Vacate` into "empty slot X back to
reserve"), routes it through a new process path in process-path-management,
and releases it to fulfillment-execution like any other work.
fulfillment-execution confirms each step against inventory-storage.

- Consequences: one execution pipeline for all floor work (labour planning,
  capacity, operator assignment and telemetry all apply to slotting moves);
  ADR 0001's planned edge is honoured as written. It needs ADRs in three
  contexts, a new task kind, a new process path and an inventory-storage
  command for an intra-site move (Option B's command), so it is the largest
  change.
- Risks: slotting moves compete with order work for the same labour;
  wes-work-planning's planning model is order-driven and would gain a second
  demand source.

### Option B: inventory-storage relocation

inventory-storage gains an intra-site relocation command (move a quantity of
a SKU from bin X to bin Y, with its own event), and consumes
`SlotPlanApproved` to create pending relocations from the moves. Operators
confirm them through inventory-storage's own surface.

- Consequences: the smallest path to a correct ledger; the relocation
  command is needed by every option that moves stock, so it is reusable.
  But inventory-storage becomes a work planner, which ADR 0001 excluded on
  purpose ("No edge to inventory-storage"; inventory-storage must stay
  available on the floor), and the moves bypass labour planning and the WES
  telemetry.
- Risks: a second place that creates floor work, outside the WES conductor.

### Option C: a slotting-owned move list consumed by the console

slotting-optimization keeps the moves of the Approved plan as a move list
with a per-move status (pending, done, skipped), exposed over REST and MCP.
warehouse-console gets a slotting screen where a supervisor works through the
list; each confirmed move is executed with the floor's existing tools, and
slotting-optimization records the confirmation and publishes it.

- Consequences: entirely inside this context and the console, no sibling ADR
  needed to start, and the plan's own data gives the progress view. But
  slotting-optimization would track execution state, which is not planning,
  and the stock ledger only changes if the operator also records the move in
  inventory-storage (which today cannot record an intra-site move at all):
  the list could say "done" while the ledger disagrees.
- Risks: duplicated truth between the move list and the ledger; the console
  becomes the integration point for physical work.

## Open questions

1. Who owns the intra-site relocation command in inventory-storage, and what
   are its events? Options A and B both need it; Option C needs it to keep
   the ledger right.
2. Is a forward-slot move order-like work that the WES conductor should plan
   and staff (Option A), or housekeeping outside it (Options B and C)?
3. Partial execution: what happens to an Approved plan whose moves are half
   done when a newer plan supersedes it? Today supersession is instant and
   has no execution state.
4. Ordering inside one plan: a `Relocate` into a slot that another SKU is
   `Vacate`-ing must run after the vacate. Who sequences the moves?
5. `Assign` needs stock from reserve: which reserve location, and how much
   (no replenishment min/max exists; ADR 0002 keeps it out of scope)?
6. Does `SlotPlanApproved` carry enough? A consumer may need quantities and
   the reserve source, which would be a `.v2` payload, never a change to v1.
7. Feedback: should slotting-optimization learn that a move was executed
   (to stop proposing a plan the floor has not caught up with)?

## Decision

None yet. The decision is taken in a later ADR that supersedes this one,
together with ADRs in the contexts the chosen option touches. Until then no
consumer of `SlotPlanApproved` is built.

## Consequences

- An Approved plan keeps changing nothing on the floor; the docs pages
  (integration, context map, domain message flow) keep marking execution as
  planned, not built, and now point here for the options.
- No contract changes: `SlotPlanApproved` v1 is unchanged.
