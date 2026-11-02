import { SLOTTING_API_BASE } from "./config";
import type {
  ForwardSlots,
  ListSlotPlansQuery,
  ListSkuVelocityQuery,
  SkuVelocityList,
  SlotPlan,
  SlotPlanPage,
} from "./types";

/** RFC 7807 problem+json body every error response from slotting-optimization
 *  returns (`type` = https://errors.slotting-optimization.warehouse-systems.dev/<slug>). */
export interface ProblemDetails {
  type?: string;
  title?: string;
  status?: number;
  detail?: string;
  instance?: string;
}

/**
 * Every failed call -- an HTTP error with a problem+json body, an HTTP error
 * without one, or a network failure -- surfaces as one ApiError, so a screen
 * has a single shape to render: `title` (the problem's category) and `detail`
 * (what exactly was wrong), plus `slug` to branch on a specific problem
 * (e.g. `plan-not-draft`, `approved-plan-conflict`).
 */
export class ApiError extends Error {
  readonly status: number;
  readonly problem: ProblemDetails | null;
  readonly title: string;
  readonly detail: string;
  /** Last path segment of problem.type (e.g. "plan-not-draft"); "" when unknown. */
  readonly slug: string;

  constructor(status: number, problem: ProblemDetails | null, fallbackTitle: string) {
    const title = problem?.title || fallbackTitle;
    const detail = problem?.detail ?? "";
    super(detail || title);
    this.name = "ApiError";
    this.status = status;
    this.problem = problem;
    this.title = title;
    this.detail = detail;
    this.slug = problem?.type ? (problem.type.split("/").pop() ?? "") : "";
  }
}

/** Normalizes anything thrown by a request into an ApiError. */
export function toApiError(err: unknown): ApiError {
  if (err instanceof ApiError) return err;
  const message = err instanceof Error ? err.message : String(err);
  return new ApiError(0, { detail: message }, "Network error");
}

/**
 * A fresh RFC 4122 v4 UUID for an Idempotency-Key header: minted once per
 * POST. `crypto.randomUUID` only exists in secure contexts, and the console
 * can be opened over plain http on a non-localhost host, so fall back to
 * getRandomValues.
 */
export function newIdempotencyKey(): string {
  if (typeof crypto.randomUUID === "function") return crypto.randomUUID();
  const b = crypto.getRandomValues(new Uint8Array(16));
  b[6] = (b[6] & 0x0f) | 0x40;
  b[8] = (b[8] & 0x3f) | 0x80;
  const h = Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}

type Query = Record<string, string | number | boolean | undefined>;

function withQuery(path: string, query?: Query): string {
  if (!query) return path;
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && value !== "") params.set(key, String(value));
  }
  const qs = params.toString();
  return qs ? `${path}?${qs}` : path;
}

export interface ApiResult<T> {
  status: number;
  data: T;
}

/**
 * Every POST (approve and reject, both of which change the plan's state and
 * REQUIRE the key in every deployed mode) carries its own `Idempotency-Key`,
 * a UUID minted for that request. GETs send no custom header, so they stay
 * simple CORS requests.
 */
async function request<T>(method: "GET" | "POST", path: string, body?: unknown): Promise<ApiResult<T>> {
  const headers: Record<string, string> = {};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (method === "POST") headers["Idempotency-Key"] = newIdempotencyKey();
  let res: Response;
  try {
    res = await fetch(`${SLOTTING_API_BASE}${path}`, {
      method,
      ...(Object.keys(headers).length > 0 ? { headers } : {}),
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });
  } catch (err) {
    throw toApiError(err);
  }
  if (!res.ok) {
    let problem: ProblemDetails | null = null;
    try {
      problem = (await res.json()) as ProblemDetails;
    } catch {
      // non-JSON error body -- fall through with problem = null
    }
    throw new ApiError(res.status, problem, `${res.status} ${res.statusText}`.trim());
  }
  return { status: res.status, data: (await res.json()) as T };
}

const seg = encodeURIComponent;

/** A plan as the UI uses it: the arrays are never undefined, even if the API
 *  leaves an empty one out (dto.go always sends them, but normalizing keeps
 *  the screens' types honest). */
function normalizePlan(p: SlotPlan): SlotPlan {
  return {
    ...p,
    assignments: p.assignments ?? [],
    moves: p.moves ?? [],
    unassigned: p.unassigned ?? [],
    ...(p.execution ? { execution: { ...p.execution, moves: p.execution.moves ?? [] } } : {}),
  };
}

/** GET /slot-plans?limit=&cursor=&siteId=&state= */
export async function listSlotPlans(q: ListSlotPlansQuery): Promise<SlotPlanPage> {
  const res = await request<SlotPlanPage>(
    "GET",
    withQuery("/slot-plans", { limit: q.limit, cursor: q.cursor, siteId: q.siteId, state: q.state }),
  );
  return { items: res.data.items ?? [], nextCursor: res.data.nextCursor || undefined };
}

/** GET /slot-plans/{planId} -- the only operation that carries `execution`. */
export async function getSlotPlan(planId: string): Promise<SlotPlan> {
  return normalizePlan((await request<SlotPlan>("GET", `/slot-plans/${seg(planId)}`)).data);
}

/** POST /slot-plans/{planId}/approvals (409 plan-not-draft / approved-plan-conflict / concurrent-modification). */
export async function approveSlotPlan(planId: string): Promise<SlotPlan> {
  return normalizePlan((await request<SlotPlan>("POST", `/slot-plans/${seg(planId)}/approvals`)).data);
}

/** POST /slot-plans/{planId}/rejections with an optional reason (409 plan-not-draft). */
export async function rejectSlotPlan(planId: string, reason?: string): Promise<SlotPlan> {
  const body = reason ? { reason } : undefined;
  return normalizePlan((await request<SlotPlan>("POST", `/slot-plans/${seg(planId)}/rejections`, body)).data);
}

/** GET /forward-slots?siteId= -- the site's currently Approved assignment map. */
export async function listForwardSlots(siteId?: string): Promise<ForwardSlots> {
  const res = (await request<ForwardSlots>("GET", withQuery("/forward-slots", { siteId }))).data;
  return { siteId: res.siteId, planId: res.planId, approvedAt: res.approvedAt, assignments: res.assignments ?? [] };
}

/** GET /sku-velocity?limit=&cursor=&windowDays=&siteId= (`nextCursor` absent on the last page). */
export async function listSkuVelocity(q: ListSkuVelocityQuery): Promise<SkuVelocityList> {
  const res = (await request<SkuVelocityList>(
    "GET",
    withQuery("/sku-velocity", { limit: q.limit, cursor: q.cursor, windowDays: q.windowDays, siteId: q.siteId }),
  )).data;
  return { ...res, items: res.items ?? [], nextCursor: res.nextCursor || undefined };
}
