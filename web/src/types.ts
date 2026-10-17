/**
 * Wire shapes of slotting-optimization's REST API (apis/openapi.yaml; the
 * JSON field names are the truth in internal/adapters/inbound/http/dto.go).
 * JSON is camelCase; optional parts of a resource are OMITTED, never null.
 */

export const PLAN_STATES = ["Draft", "Approved", "Rejected", "Superseded"] as const;
export type PlanState = (typeof PLAN_STATES)[number];

export const ABC_CLASSES = ["A", "B", "C"] as const;
export type AbcClass = (typeof ABC_CLASSES)[number];

export const MOVE_KINDS = ["Assign", "Relocate", "Vacate"] as const;
export type MoveKind = (typeof MOVE_KINDS)[number];

export const UNASSIGNED_REASONS = ["NoEligibleSlot", "NoPhysicalProfile", "NoCapacityFit"] as const;
export type UnassignedReason = (typeof UNASSIGNED_REASONS)[number];

export interface Assignment {
  sku: string;
  slot: string;
  abcClass: AbcClass;
  picks: number;
  units: number;
}

export interface Move {
  sku: string;
  /** Omitted for an Assign. */
  fromSlot?: string;
  /** Omitted for a Vacate. */
  toSlot?: string;
  kind: MoveKind;
}

export interface Unassigned {
  sku: string;
  reason: UnassignedReason;
}

/** Execution progress of one move (ADR 0010): GET /slot-plans/{planId} only. */
export interface MoveExecution {
  moveSeq: number;
  sku: string;
  kind: MoveKind;
  executed: boolean;
  executedQuantity: number;
  /** Absent until the move is executed. */
  lastExecutedAt?: string;
}

export interface ExecutionProgress {
  movesTotal: number;
  movesExecuted: number;
  /** Absent until a relocation is recorded. */
  lastExecutedAt?: string;
  moves: MoveExecution[];
}

export interface SlotPlan {
  planId: string;
  siteId: string;
  state: PlanState;
  policy: string;
  windowFrom: string;
  windowTo: string;
  generatedAt: string;
  approvedAt?: string;
  rejectedAt?: string;
  supersededAt?: string;
  rejectReason?: string;
  /** The plan this one replaced when it was approved. */
  supersedesPlanId?: string;
  version: number;
  assignments: Assignment[];
  moves: Move[];
  unassigned: Unassigned[];
  /** GET /slot-plans/{planId} only (ADR 0010). */
  execution?: ExecutionProgress;
}

export interface SlotPlanSummary {
  planId: string;
  siteId: string;
  state: PlanState;
  policy: string;
  windowFrom: string;
  windowTo: string;
  generatedAt: string;
  approvedAt?: string;
  assignmentCount: number;
  moveCount: number;
  unassignedCount: number;
  version: number;
}

export interface SlotPlanPage {
  items: SlotPlanSummary[];
  /** Absent on the last page. */
  nextCursor?: string;
}

export interface ForwardSlot {
  sku: string;
  slot: string;
  abcClass: AbcClass;
}

export interface ForwardSlots {
  siteId: string;
  /** The Approved plan the map comes from; absent when the site has none. */
  planId?: string;
  approvedAt?: string;
  assignments: ForwardSlot[];
}

export interface SkuVelocity {
  sku: string;
  picks: number;
  units: number;
}

export interface SkuVelocityList {
  siteId: string;
  windowFrom: string;
  windowTo: string;
  items: SkuVelocity[];
}

export interface ListSlotPlansQuery {
  limit?: number;
  cursor?: string;
  siteId?: string;
  state?: PlanState;
}

export interface ListSkuVelocityQuery {
  limit?: number;
  windowDays?: number;
  siteId?: string;
}
