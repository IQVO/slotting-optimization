ALTER TABLE slot_plan_assignments
    DROP CONSTRAINT IF EXISTS slot_plan_assignments_cases_capacity_only_case,
    DROP COLUMN IF EXISTS cases_capacity,
    DROP COLUMN IF EXISTS pick_unit;
DROP TABLE IF EXISTS pack_levels;
