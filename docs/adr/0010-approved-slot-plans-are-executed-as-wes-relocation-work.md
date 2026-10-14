# ADR 0010: Approved slot plans are executed as WES relocation work

## Status

Accepted (2026-10-14, final-gaps wave decision E1). Supersedes
[ADR 0007](./0007-executing-an-approved-slot-plan.md), whose options and open
questions it decides. [ADR 0001](./0001-slotting-optimization-bounded-context.md)
and [ADR 0002](./0002-slotplan-aggregate-and-abc-velocity-policy.md) stand:
this context still only plans; it never creates floor work and never writes
the stock ledger. Additive only: `SlotPlanApproved` v1 is unchanged, one new
table (migration 0002), optional response fields on the plan read API and the
MCP plan tool.

## Context

ADR 0007 laid out three ways to turn an Approved plan's moves into floor work:
(A) wes-work-planning plans the moves as work, (B) inventory-storage creates
and tracks relocations itself, (C) a slotting-owned move list worked through
the console. It answered none of its seven open questions.

A forward-slot move is physical floor work. The fleet already has ONE conductor
for floor work (wes-work-planning plans, process-path-management routes,
fulfillment-execution executes with operators; labour and capacity planning
and the WES telemetry apply) and ONE stock ledger (inventory-storage), joined
by an existing pattern: fulfillment-execution's `TaskCompleted` drives
inventory-storage's ledger (confirm-pick, inventory-storage ADR 0035/0036).
Option B makes inventory-storage a second work planner, which ADR 0001 of this
context excluded. Option C duplicates truth between a move list and the
ledger. Option A keeps one planner, one executor and one ledger, and every
option needed inventory-storage's relocation command anyway.

## Decision

**Option A.** wes-work-planning plans and staffs the moves of an Approved plan;
fulfillment-execution executes them with operators; inventory-storage records
them on the ledger; this context learns the outcome from inventory-storage's
`StockRelocated` and keeps a per-plan execution-progress read model.

### Answers to ADR 0007's open questions

1. **Relocation command.** inventory-storage owns an intra-site
   **RelocateStock** command: move a quantity of one SKU from bin A to bin B in
   one site, atomically on the ledger (both bins in one transaction, never
   negative, optimistic concurrency like its other stock commands), raising
   **`com.warehouse.wms.inventory-storage.stock.StockRelocated`** (snake_case
   data: `relocation_id`, `site_code`, `sku`, `from_bin`, `to_bin`, `quantity`,
   `reason`, `source_ref`, `occurred_at`) through its outbox. It is exposed as
   REST `POST /relocations` (Idempotency-Key, like its other commands) AND
   applied from fulfillment-execution's `TaskCompleted` when `task_type` is
   `RELOCATE` (an additive consumer branch next to confirm-pick).
2. **Who plans it.** Yes: slot moves are floor work planned and staffed by
   wes-work-planning (a second demand source next to orders).
3. **Supersession.** When a newer plan's `SlotPlanApproved` arrives,
   wes-work-planning cancels the superseded plan's NOT-YET-RELEASED relocation
   work and plans the new plan's moves; released and in-progress tasks run to
   completion.
4. **Ordering.** wes-work-planning sequences inside one plan: a move into a
   slot that another move vacates is released only after that vacate's task
   completes (a dependency on `TaskCompleted`).
5. **Quantity and source.** v1 moves the **whole on-hand quantity of the SKU at
   the source bin**, counted and confirmed by the operator at execution (the
   fulfillment-execution task completion carries the confirmed quantity).
   `Relocate` source = `from_slot`; `Vacate` destination = a reserve bin chosen
   at execution (the operator scans it; inventory-storage validates the bin
   exists in the site); `Assign` source = any bin holding the SKU that is not a
   forward slot, chosen at execution (the operator scans it; inventory-storage
   validates the SKU is there). No min/max replenishment is introduced (ADR
   0002 keeps it out of scope).
6. **Payload.** `SlotPlanApproved` v1 is enough (`sku`, `from_slot?`,
   `to_slot?`, `kind`); the quantity is resolved at execution. No v2.
7. **Feedback.** slotting-optimization consumes `StockRelocated` to record
   per-move execution progress on the plan (read model: executed moves /
   total, last executed at), exposed on its plan read API and MCP plan tool;
   it does NOT gate proposals on it in v1.

### Implemented by (one ADR per context)

- **inventory-storage ADR 0039**: RelocateStock, `POST /relocations` and
  `GET /relocations/{id}`, `StockRelocated` on `warehouse.inventory.events`,
  and the `TaskCompleted` RELOCATE branch. On the task path `source_ref` is
  the task's `relocation_ref`, or the task id when the producer sent none.
- **fulfillment-execution ADR 0047**: the `RELOCATE` task type (sku,
  from_bin?, to_bin?, kind, plan_id, move_seq); completing it takes the
  operator-confirmed quantity and the scanned bins; `TaskCompleted` gains the
  additive optional fields `sku`, `from_bin`, `to_bin`, `quantity` and
  `relocation_ref` = `<plan_id>:<move_seq>`.
- **wes-work-planning ADR 0040**: consumes `SlotPlanApproved`, plans one
  RELOCATE work unit per move (`move_seq` = the 0-based position of the move
  in the event's `moves`), sequences (answer 4) and supersedes (answer 3) them,
  and releases them through the existing release flow via the RELOCATE path.
- **process-path-management ADR 0033** (the `RELOCATE` process path, required
  capability `relocate`) and **ADR 0034** (path intake: RELOCATE's intake is
  `SLOT_RELOCATION`, so approved slot plans, and only they, route onto it;
  customer order lines never do).

### This context's part

1. **Consumer.** A new consumer reads `warehouse.inventory.events` under a
   stable group (`RELOCATION_MODE=kafka`, `RELOCATION_CONSUMER_GROUP`, no
   default; mode `permissive` = no consumer, the default) and acts on
   `com.warehouse.wms.inventory-storage.stock.StockRelocated` only; every
   other type on the topic is ignored. CloudEvents 1.0 via
   `internal/adapters/kafka/cloudevents`, dispatch on the FULL `type`, the
   CloudEvents `id` claimed (consumer `slotting-relocation`) in the SAME
   transaction as the effect, offset committed afterwards
   ([ADR 0003](./0003-local-copies-and-consumed-contracts.md)'s pattern).
2. **Matching.** `source_ref` is matched first: `<plan_id>:<move_seq>` (split at
   the last `:`) names the move at that 0-based position of the plan's moves,
   which is the order `SlotPlanApproved` publishes them in (sorted by SKU).
   The move matches when the plan exists, is Approved or Superseded (its
   released tasks still run to completion, answer 3), is for `site_code`, has
   a move at `move_seq`, and that move is for `sku`. Only when `source_ref` is
   not a move reference (empty, a task id, a manual ticket) does the consumer
   fall back to the site's CURRENT Approved plan: the move for `sku` whose bins
   agree with its kind (`Relocate`: `from_slot` = `from_bin` and `to_slot` =
   `to_bin`; `Assign`: `to_slot` = `to_bin`; `Vacate`: `from_slot` =
   `from_bin`). `site_code` is this context's site id (the fleet's site codes).
3. **Unmatched is not poison.** A relocation that matches no move (unknown
   plan, plan not Approved/Superseded, other site, no move at that position,
   another SKU, no current plan, no move with those bins) is claimed, logged
   at INFO and counted (`slotting_optimization.relocations.observed`,
   `outcome=unmatched`, `reason=<why>`); it is never dead-lettered, because
   most relocations (manual, consolidation) are not slot-plan moves.
4. **Read model.** Table `slot_plan_move_executions`, one row per matched
   relocation (primary key `relocation_id`: a replay of the same relocation
   under a new CloudEvents id cannot count twice). Per plan: moves total
   (the plan's moves), moves executed (moves with at least one relocation),
   last executed at (latest `occurred_at`), and per move (`move_seq`, `sku`,
   `kind`) an executed flag and the executed quantity (sum of its
   relocations). Computed in the domain (`slotplan.Progress`), never stored as
   a counter.
5. **Exposure.** `GET /slot-plans/{planId}` gains an optional `execution`
   object (OpenAPI additive) and the MCP `get_slot_plan` tool an optional
   `execution` field (`omitempty`). Proposals (`POST /slot-plans`) and
   approvals are NOT gated on progress in v1.
6. **Failure classes.** A `StockRelocated` whose data cannot be decoded or
   misses a required field (or carries a quantity below 1) is a deterministic
   poison: dead-lettered at once to `warehouse.inventory.events.dlq` with the
   `x-dlq-*` headers, no retries. A message that is not a CloudEvent is
   skipped (WARN) like every other consumer here. A transient failure
   (database) is retried with capped backoff, then dead-lettered after the
   bounded attempts. Cancellation is never dead-lettered: the loop returns
   `ctx.Err()` before any dead-letter call and the message is redelivered.

## Consequences

- An Approved plan now changes the floor: the moves become RELOCATE work in
  wes-work-planning and stock moves in inventory-storage, and the plan read
  API shows how far execution has got. The docs (integration, context map,
  domain message flow) no longer mark execution as "planned, not built".
- The plan read model depends on inventory-storage's `source_ref` carrying the
  move reference; the bins fallback covers producers that send none.
- Progress is advisory in v1: a newer plan can be generated and approved while
  the previous one is half executed (wes-work-planning applies answer 3).
  Gating proposals on progress would need a new ADR.
- Deployment: the chart renders `RELOCATION_MODE` /
  `RELOCATION_CONSUMER_GROUP` from values (default `permissive`, no group);
  turning it on in the cluster is a values change in warehouse-infra.
