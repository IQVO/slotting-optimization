-- 0001_slotting_schema.up.sql: slotting-optimization's OLTP schema.
--
-- Text keys that are compared or ordered in Go (plan ids, SKUs, slot codes)
-- use the "C" collation so ORDER BY and keyset comparisons follow byte
-- order, the same order the in-memory adapters use.

-- slot_plans: one row per SlotPlan aggregate (ADR 0002). The content of a
-- plan (assignments, moves, unassigned) is written once with the plan and
-- never changes; only the decision columns and version change afterwards.
-- version starts at 1 and guards every UPDATE (UPDATE ... WHERE version =
-- <loaded>).
CREATE TABLE slot_plans (
    plan_id            TEXT COLLATE "C" PRIMARY KEY,
    site_id            TEXT        NOT NULL,
    state              TEXT        NOT NULL CHECK (state IN ('Draft', 'Approved', 'Rejected', 'Superseded')),
    policy             TEXT        NOT NULL,
    window_from        TIMESTAMPTZ NOT NULL,
    window_to          TIMESTAMPTZ NOT NULL,
    generated_at       TIMESTAMPTZ NOT NULL,
    approved_at        TIMESTAMPTZ,
    rejected_at        TIMESTAMPTZ,
    superseded_at      TIMESTAMPTZ,
    reject_reason      TEXT,
    supersedes_plan_id TEXT,
    version            BIGINT      NOT NULL CHECK (version >= 1),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (window_from < window_to)
);

-- At most ONE Approved plan per site (the rule spans plans, so the use case
-- supersedes the previous plan first and this index backs it: the losing
-- approval of a race fails here and becomes 409 approved-plan-conflict).
CREATE UNIQUE INDEX uq_slot_plans_one_approved_per_site ON slot_plans (site_id) WHERE state = 'Approved';

-- GET /slot-plans: newest first, keyset paged on (generated_at, plan_id).
CREATE INDEX idx_slot_plans_listing ON slot_plans (generated_at DESC, plan_id DESC);
CREATE INDEX idx_slot_plans_site_state ON slot_plans (site_id, state);

CREATE TABLE slot_plan_assignments (
    plan_id   TEXT COLLATE "C" NOT NULL REFERENCES slot_plans (plan_id) ON DELETE CASCADE,
    sku       TEXT COLLATE "C" NOT NULL,
    slot      TEXT COLLATE "C" NOT NULL,
    abc_class TEXT   NOT NULL CHECK (abc_class IN ('A', 'B', 'C')),
    picks     BIGINT NOT NULL CHECK (picks >= 1),
    units     BIGINT NOT NULL CHECK (units >= 0),
    PRIMARY KEY (plan_id, sku),
    UNIQUE (plan_id, slot)
);

CREATE TABLE slot_plan_moves (
    plan_id   TEXT COLLATE "C" NOT NULL REFERENCES slot_plans (plan_id) ON DELETE CASCADE,
    sku       TEXT COLLATE "C" NOT NULL,
    from_slot TEXT,
    to_slot   TEXT,
    kind      TEXT NOT NULL CHECK (kind IN ('Assign', 'Relocate', 'Vacate')),
    PRIMARY KEY (plan_id, sku)
);

CREATE TABLE slot_plan_unassigned (
    plan_id TEXT COLLATE "C" NOT NULL REFERENCES slot_plans (plan_id) ON DELETE CASCADE,
    sku     TEXT COLLATE "C" NOT NULL,
    reason  TEXT NOT NULL CHECK (reason IN ('NoEligibleSlot', 'NoPhysicalProfile', 'NoCapacityFit')),
    PRIMARY KEY (plan_id, sku)
);

-- demand_lines: the demand copy (ADR 0003), one row per source order line,
-- last writer wins. A REMOVED line stays (state = 'REMOVED') so a replay
-- cannot resurrect it; only ACTIVE rows count as picks.
CREATE TABLE demand_lines (
    source_order_id TEXT        NOT NULL,
    line_no         INTEGER     NOT NULL CHECK (line_no >= 1),
    site_id         TEXT        NOT NULL,
    sku             TEXT COLLATE "C" NOT NULL,
    units           BIGINT      NOT NULL CHECK (units >= 0),
    due_at          TIMESTAMPTZ NOT NULL,
    state           TEXT        NOT NULL CHECK (state IN ('ACTIVE', 'REMOVED')),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (source_order_id, line_no)
);

CREATE INDEX idx_demand_lines_sku_due ON demand_lines (sku, due_at);
-- The velocity query: ACTIVE lines of one site in a due_at window, by SKU.
CREATE INDEX idx_demand_lines_site_due_active ON demand_lines (site_id, due_at) WHERE state = 'ACTIVE';

-- product_profiles: the product copy, one row per SKU, guarded by the
-- product-master aggregate version of the last message applied. The
-- dimensions are the EFFECTIVE ones (measured if present, else declared).
CREATE TABLE product_profiles (
    sku              TEXT COLLATE "C" PRIMARY KEY,
    handling_tags    TEXT[]  NOT NULL DEFAULT '{}',
    temperature_class TEXT   NOT NULL DEFAULT '',
    volume_mm3       BIGINT  CHECK (volume_mm3 >= 1),
    weight_g         BIGINT  CHECK (weight_g >= 1),
    version          BIGINT  NOT NULL CHECK (version >= 0),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((volume_mm3 IS NULL) = (weight_g IS NULL))
);

-- zones and slots: the layout copy. A slot joins its zone through zone_id at
-- plan time, so the two may arrive in any order.
CREATE TABLE zones (
    zone_id           TEXT PRIMARY KEY,
    site_code         TEXT    NOT NULL,
    area_code         TEXT    NOT NULL DEFAULT '',
    zone_code         TEXT    NOT NULL,
    temperature_class TEXT    NOT NULL DEFAULT '',
    hazmat            BOOLEAN NOT NULL DEFAULT false,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_zones_site_zone_code ON zones (site_code, zone_code);

-- active = false is a decommissioned slot: irreversible, and the row stays so
-- a late registration cannot bring the slot back.
CREATE TABLE slots (
    location_code TEXT COLLATE "C" PRIMARY KEY,
    zone_id       TEXT             NOT NULL DEFAULT '',
    role          TEXT             NOT NULL DEFAULT 'Storage',
    active        BOOLEAN          NOT NULL DEFAULT true,
    max_weight_kg DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (max_weight_kg >= 0),
    max_volume_m3 DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (max_volume_m3 >= 0),
    updated_at    TIMESTAMPTZ      NOT NULL DEFAULT now()
);

CREATE INDEX idx_slots_zone ON slots (zone_id) WHERE active;

-- outbox_events: the transactional outbox (mirrors product-master's and
-- warehouse-planning's, including the (event_id, topic) identity). One row
-- per already-encoded Kafka message, inserted in the SAME transaction as the
-- plan row; the relay drains unpublished rows to Kafka.
--   event_id    the CloudEvents `id`, minted once at encode time and
--               persisted: a relay retry republishes the same id.
--   event_type  the FULL CloudEvents `type`
--               (com.warehouse.wms.slotting-optimization.slotplan.<EventName>).
--   subject     the CloudEvents `subject` (the plan id).
--   key         the Kafka message key (the plan id).
--   dataschema  the CloudEvents `dataschema` URN.
--   value       the structured-mode CloudEvents JSON bytes.
--   headers     [{"key":..,"value":..}] Kafka headers (content-type).
CREATE TABLE outbox_events (
    id           BIGSERIAL   PRIMARY KEY,
    event_id     TEXT        NOT NULL,
    topic        TEXT        NOT NULL,
    event_type   TEXT        NOT NULL,
    subject      TEXT        NOT NULL,
    key          BYTEA,
    dataschema   TEXT        NOT NULL,
    value        BYTEA       NOT NULL,
    headers      JSONB       NOT NULL DEFAULT '[]',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,
    attempts     INTEGER     NOT NULL DEFAULT 0,
    last_error   TEXT,
    CONSTRAINT outbox_events_event_id_topic_key UNIQUE (event_id, topic)
);

-- The relay only ever scans the unpublished tail; keep that scan tiny.
CREATE INDEX idx_outbox_events_unpublished ON outbox_events (id) WHERE published_at IS NULL;

-- processed_events: idempotency guard of the inbound consumers. (consumer,
-- event_id) is claimed with INSERT ... ON CONFLICT DO NOTHING in the SAME
-- transaction as the effect, so a rollback un-claims it.
CREATE TABLE processed_events (
    consumer     TEXT        NOT NULL,
    event_id     TEXT        NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (consumer, event_id)
);

-- idempotency_keys: the fleet Idempotency-Key middleware (POST /slot-plans).
-- The middleware INSERTs a bare row inside its own transaction before calling
-- the handler, then UPDATEs it with the outcome and commits, all in ONE
-- transaction shared with the use case's own writes, so a COMMITTED row
-- always has a status_code.
CREATE TABLE idempotency_keys (
    key              TEXT PRIMARY KEY,
    method           TEXT        NOT NULL,
    path             TEXT        NOT NULL,
    request_hash     TEXT        NOT NULL,
    status_code      INTEGER,
    response_body    BYTEA,
    response_headers JSONB,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at     TIMESTAMPTZ
);

CREATE INDEX idx_idempotency_keys_created_at ON idempotency_keys (created_at);
