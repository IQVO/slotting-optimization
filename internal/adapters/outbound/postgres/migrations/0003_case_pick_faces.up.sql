-- 0003_case_pick_faces.up.sql: case-pick faces (ADR 0012). Additive: one new
-- table and two new columns with defaults that describe every existing row.

-- pack_levels: the local copy of product-master's pack levels, one row per
-- (SKU, level), the latest state guarded by the product aggregate `version`
-- of the last message applied. declared_* are the dimensions of the last
-- ProductPackLevelDeclared; measured_* the last ProductPackLevelMeasured of
-- the CURRENT gtin (cleared when the gtin changes). The effective dimensions
-- are the measured ones if present, else the declared ones. A retired level
-- stays (effective_to set): an end-dated level is history, not a deletion.
CREATE TABLE pack_levels (
    sku                TEXT COLLATE "C" NOT NULL,
    level              TEXT        NOT NULL CHECK (level IN ('EACH', 'INNER', 'CASE', 'PALLET')),
    gtin               TEXT        NOT NULL,
    units_per_pack     BIGINT      NOT NULL CHECK (units_per_pack >= 1),
    declared_length_mm BIGINT CHECK (declared_length_mm >= 1),
    declared_width_mm  BIGINT CHECK (declared_width_mm >= 1),
    declared_height_mm BIGINT CHECK (declared_height_mm >= 1),
    declared_weight_g  BIGINT CHECK (declared_weight_g >= 1),
    measured_length_mm BIGINT CHECK (measured_length_mm >= 1),
    measured_width_mm  BIGINT CHECK (measured_width_mm >= 1),
    measured_height_mm BIGINT CHECK (measured_height_mm >= 1),
    measured_weight_g  BIGINT CHECK (measured_weight_g >= 1),
    effective_from     TIMESTAMPTZ NOT NULL,
    effective_to       TIMESTAMPTZ,
    version            BIGINT      NOT NULL CHECK (version >= 1),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (sku, level),
    CHECK ((declared_length_mm IS NULL) = (declared_width_mm IS NULL)
       AND (declared_length_mm IS NULL) = (declared_height_mm IS NULL)
       AND (declared_length_mm IS NULL) = (declared_weight_g IS NULL)),
    CHECK ((measured_length_mm IS NULL) = (measured_width_mm IS NULL)
       AND (measured_length_mm IS NULL) = (measured_height_mm IS NULL)
       AND (measured_length_mm IS NULL) = (measured_weight_g IS NULL))
);

-- slot_plan_assignments gains the pick unit of the assignment. Every existing
-- row is an EACH-pick assignment, which is exactly what it was.
ALTER TABLE slot_plan_assignments
    ADD COLUMN pick_unit      TEXT   NOT NULL DEFAULT 'EACH' CHECK (pick_unit IN ('EACH', 'CASE')),
    ADD COLUMN cases_capacity BIGINT CHECK (cases_capacity >= 1),
    ADD CONSTRAINT slot_plan_assignments_cases_capacity_only_case
        CHECK ((pick_unit = 'CASE') = (cases_capacity IS NOT NULL));
