-- slotting-optimization analytics read model (ADR 0006).
--
-- This is the ANALYTICAL database (slotting_optimization_analytics), separate
-- from the OLTP database. It is written only by cmd/slotting-projector and
-- read (read-only) by cmd/slotting-reports. Everything here is a projection
-- derived from warehouse.slotting-optimization.analytics, not a source of
-- truth: it can be dropped and rebuilt by replaying the topic under a new
-- consumer group.
--
-- Facts, not aggregates: every per-day number of the funnel report is
-- counted from these rows over the requested window. Each milestone column
-- is the CloudEvents time of the EARLIEST such event seen (kept with LEAST,
-- which ignores NULLs), so arrival order never matters.

-- Idempotency: every applied CloudEvents id is recorded here exactly once, in
-- the SAME transaction as its effect. occurred_at is the event's CloudEvents
-- `time`; max(occurred_at) is the freshness of the projection.
CREATE TABLE analytics_processed_events (
    event_id    TEXT        PRIMARY KEY,
    event_type  TEXT        NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_analytics_processed_events_occurred_at ON analytics_processed_events (occurred_at);

-- slot_plan_facts: one row per plan the projection has seen (any of its
-- events), one column per milestone.
--   generated_at   SlotPlanGenerated
--   approved_at    SlotPlanApproved (supersedes_plan_id names the plan it replaced)
--   rejected_at    SlotPlanRejected
--   superseded_at  set on the SUPERSEDED plan's row by the later approval
-- A plan is PENDING from its generation until approved_at or rejected_at is
-- set; approval latency is approved_at - generated_at and exists only when
-- the projection saw both.
CREATE TABLE slot_plan_facts (
    plan_id           TEXT COLLATE "C" PRIMARY KEY,
    site_id           TEXT COLLATE "C" NOT NULL,
    generated_at      TIMESTAMPTZ,
    approved_at       TIMESTAMPTZ,
    rejected_at       TIMESTAMPTZ,
    superseded_at     TIMESTAMPTZ,
    supersedes_plan_id TEXT COLLATE "C"
);

CREATE INDEX idx_slot_plan_facts_site              ON slot_plan_facts (site_id);
CREATE INDEX idx_slot_plan_facts_generated_at      ON slot_plan_facts (generated_at)      WHERE generated_at IS NOT NULL;
CREATE INDEX idx_slot_plan_facts_approved_at       ON slot_plan_facts (approved_at)       WHERE approved_at IS NOT NULL;
CREATE INDEX idx_slot_plan_facts_rejected_at       ON slot_plan_facts (rejected_at)       WHERE rejected_at IS NOT NULL;
CREATE INDEX idx_slot_plan_facts_superseded_at     ON slot_plan_facts (superseded_at)     WHERE superseded_at IS NOT NULL;
