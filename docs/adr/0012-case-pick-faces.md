# ADR 0012: Case-pick faces and the CASE pick unit

## Status

Accepted (2026-10-24). Extends
[ADR 0003](./0003-local-copies-and-consumed-contracts.md) (local copies of
consumed contracts) and respects
[ADR 0011](./0011-postgres-and-kafka-are-the-only-runtime.md) (real Postgres and
Kafka only). Additive on the wire; one migration (`0003_case_pick_faces`). Part
of the mastercase / GTIN programme (contract P11-1).

## Context

A SKU that is picked in whole cases does not belong on an each-pick face sized
for its eaches: the face must hold CASES, and its capacity is a number of cases
derived from the case's cube and weight. Product-master declares the pack levels
(`ProductPackLevelDeclared` / `ProductPackLevelRetired`); this context must not
look them up live.

## Decision

1. A local copy of the pack levels is kept in Postgres by a consumer of
   product-master's pack-level events (`PACK_LEVEL_MODE=kafka`, stable group
   `PACK_LEVEL_CONSUMER_GROUP`), version-guarded and idempotent by CloudEvents id.
2. A face is a CASE-pick face when its zone is in `CASE_PICK_ZONE_CODES`
   (comma separated; default empty, meaning no case-pick faces and the previous
   behaviour byte for byte).
3. A SKU is planned as `pickUnit: CASE` only when case faces exist, it has an
   active CASE pack level with a size, and the greatest common divisor of its
   demand line units is a whole number of cases. Otherwise it is `EACH`.
   A CASE SKU is placed only on case faces and an EACH SKU only on other faces.
4. The face's `casesCapacity` is the number of whole cases it holds, the tighter
   of the volume and the weight limit (at least one, or the face is not
   eligible: `no-capacity-fit`).
5. Output is additive: assignments in REST responses carry `pickUnit` and, for
   CASE, `casesCapacity`; `SlotPlanApproved.assignments[]` carries `pick_unit`
   and `cases_capacity` only for CASE assignments, so the payload of an each
   assignment is byte-identical to before. Quantities elsewhere remain eaches.

## Consequences

- Two new env vars: `PACK_LEVEL_CONSUMER_GROUP` (mandatory with
  `PACK_LEVEL_MODE=kafka`) and `CASE_PICK_ZONE_CODES` (optional). Both are chart
  values (`config.packLevelConsumerGroup`, `config.casePickZoneCodes`).
- Retiring the CASE level makes the SKU fall back to an each-pick face at the
  next plan; a plan already Approved is not rewritten.
- Facility layout is not asked which faces are case faces: the zone list is
  declarative configuration of this context.
