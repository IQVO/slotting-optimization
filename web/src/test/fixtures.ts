import type { ForwardSlots, SkuVelocityList, SlotPlan, SlotPlanSummary } from "../types";

export const PLAN_ID = "plan-3b0e8d2a-7c41-4f6a-9d15-2a8b6c4e1f07";
export const OLD_PLAN_ID = "plan-1a2b3c4d-5e6f-4a5b-8c9d-0e1f2a3b4c5d";

/** The openapi.yaml listSlotPlans example summary. */
export function planSummary(over: Partial<SlotPlanSummary> = {}): SlotPlanSummary {
  return {
    planId: PLAN_ID,
    siteId: "SITE-1",
    state: "Approved",
    policy: "abc-velocity-v1",
    windowFrom: "2026-09-10T12:00:00Z",
    windowTo: "2026-10-08T12:00:00Z",
    generatedAt: "2026-10-08T12:00:00Z",
    approvedAt: "2026-10-08T12:30:00Z",
    assignmentCount: 2,
    moveCount: 3,
    unassignedCount: 1,
    version: 2,
    ...over,
  };
}

/** The openapi.yaml generateSlotPlan example, as a full plan (Draft). */
export function draftPlan(over: Partial<SlotPlan> = {}): SlotPlan {
  return {
    planId: PLAN_ID,
    siteId: "SITE-1",
    state: "Draft",
    policy: "abc-velocity-v1",
    windowFrom: "2026-09-10T12:00:00Z",
    windowTo: "2026-10-08T12:00:00Z",
    generatedAt: "2026-10-08T12:00:00Z",
    version: 1,
    assignments: [
      { sku: "SKU-1", slot: "WH1-FWD-A01-01-01-A", abcClass: "A", picks: 80, units: 160 },
      { sku: "SKU-2", slot: "WH1-FWD-A01-01-02-A", abcClass: "B", picks: 20, units: 40 },
    ],
    moves: [
      { sku: "SKU-1", toSlot: "WH1-FWD-A01-01-01-A", kind: "Assign" },
      { sku: "SKU-2", fromSlot: "WH1-FWD-A02-03-01-A", toSlot: "WH1-FWD-A01-01-02-A", kind: "Relocate" },
      { sku: "SKU-3", fromSlot: "WH1-FWD-A01-01-03-A", kind: "Vacate" },
    ],
    unassigned: [{ sku: "SKU-4", reason: "NoPhysicalProfile" }],
    ...over,
  };
}

/** The same plan after approval, superseding OLD_PLAN_ID, half executed. */
export function approvedPlan(over: Partial<SlotPlan> = {}): SlotPlan {
  return draftPlan({
    state: "Approved",
    approvedAt: "2026-10-08T12:30:00Z",
    supersedesPlanId: OLD_PLAN_ID,
    version: 2,
    execution: {
      movesTotal: 3,
      movesExecuted: 1,
      lastExecutedAt: "2026-10-08T13:05:00Z",
      moves: [
        { moveSeq: 0, sku: "SKU-1", kind: "Assign", executed: true, executedQuantity: 160, lastExecutedAt: "2026-10-08T13:05:00Z" },
        { moveSeq: 1, sku: "SKU-2", kind: "Relocate", executed: false, executedQuantity: 0 },
        { moveSeq: 2, sku: "SKU-3", kind: "Vacate", executed: false, executedQuantity: 0 },
      ],
    },
    ...over,
  });
}

/** A plan another approval superseded. */
export function supersededPlan(over: Partial<SlotPlan> = {}): SlotPlan {
  return approvedPlan({
    planId: OLD_PLAN_ID,
    state: "Superseded",
    approvedAt: "2026-10-01T09:00:00Z",
    supersedesPlanId: undefined,
    supersededAt: "2026-10-08T12:30:00Z",
    version: 3,
    ...over,
  });
}

/** The openapi.yaml listForwardSlots example. */
export function forwardSlots(over: Partial<ForwardSlots> = {}): ForwardSlots {
  return {
    siteId: "SITE-1",
    planId: PLAN_ID,
    approvedAt: "2026-10-08T12:30:00Z",
    assignments: [
      { sku: "SKU-1", slot: "WH1-FWD-A01-01-01-A", abcClass: "A" },
      { sku: "SKU-2", slot: "WH1-FWD-A01-01-02-A", abcClass: "B" },
    ],
    ...over,
  };
}

/** The openapi.yaml listSkuVelocity example. */
export function skuVelocity(over: Partial<SkuVelocityList> = {}): SkuVelocityList {
  return {
    siteId: "SITE-1",
    windowFrom: "2026-09-10T12:00:00Z",
    windowTo: "2026-10-08T12:00:00Z",
    items: [
      { sku: "SKU-1", picks: 80, units: 160 },
      { sku: "SKU-2", picks: 20, units: 40 },
    ],
    ...over,
  };
}
