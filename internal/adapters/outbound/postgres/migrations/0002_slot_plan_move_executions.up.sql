-- 0002_slot_plan_move_executions.up.sql: the execution-progress read model
-- of the slot plans (ADR 0010). Additive: one new table, nothing else
-- changes.
--
-- One row per relocation inventory-storage recorded (StockRelocated) that
-- executes a move of a plan. relocation_id is the primary key so a replay of
-- the same relocation under a new CloudEvents id cannot count twice (the
-- CloudEvents id itself is claimed in processed_events in the SAME
-- transaction). move_seq is the 0-based position of the move in the plan's
-- moves (sorted by SKU, the order SlotPlanApproved publishes them in).
-- Progress (moves executed / total, last executed at, per-move quantity) is
-- derived from these rows at read time, never stored as a counter.
CREATE TABLE slot_plan_move_executions (
    relocation_id TEXT        PRIMARY KEY,
    plan_id       TEXT COLLATE "C" NOT NULL REFERENCES slot_plans (plan_id) ON DELETE CASCADE,
    move_seq      INTEGER     NOT NULL CHECK (move_seq >= 0),
    sku           TEXT COLLATE "C" NOT NULL,
    from_bin      TEXT        NOT NULL,
    to_bin        TEXT        NOT NULL,
    quantity      BIGINT      NOT NULL CHECK (quantity >= 1),
    executed_at   TIMESTAMPTZ NOT NULL,
    recorded_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- GET /slot-plans/{planId} reads every execution of one plan.
CREATE INDEX idx_slot_plan_move_executions_plan ON slot_plan_move_executions (plan_id, move_seq);
