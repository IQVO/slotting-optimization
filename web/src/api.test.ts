import { afterEach, describe, expect, it, vi } from "vitest";
import {
  ApiError,
  approveSlotPlan,
  getSlotPlan,
  listForwardSlots,
  listSkuVelocity,
  listSlotPlans,
  newIdempotencyKey,
  rejectSlotPlan,
  toApiError,
} from "./api";
import { json, mockApi, problem } from "./test/fetchMock";
import { PLAN_ID, draftPlan, forwardSlots, planSummary, skuVelocity } from "./test/fixtures";

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

describe("Idempotency-Key", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("both decision POSTs carry a UUID Idempotency-Key", async () => {
    const api = mockApi({
      [`POST /slot-plans/${PLAN_ID}/approvals`]: json(draftPlan({ state: "Approved" })),
      [`POST /slot-plans/${PLAN_ID}/rejections`]: json(draftPlan({ state: "Rejected" })),
    });
    await approveSlotPlan(PLAN_ID);
    await rejectSlotPlan(PLAN_ID);

    expect(api.calls).toHaveLength(2);
    for (const call of api.calls) {
      expect(call.method).toBe("POST");
      expect(call.headers["Idempotency-Key"]).toMatch(UUID);
    }
    expect(new Set(api.calls.map((c) => c.headers["Idempotency-Key"])).size).toBe(2);
  });

  it("a body-less approve sends no content type; a reject reason goes in the body", async () => {
    const api = mockApi({
      [`POST /slot-plans/${PLAN_ID}/approvals`]: json(draftPlan({ state: "Approved" })),
      [`POST /slot-plans/${PLAN_ID}/rejections`]: json(draftPlan({ state: "Rejected" })),
    });
    await approveSlotPlan(PLAN_ID);
    await rejectSlotPlan(PLAN_ID, "Too many relocations before peak season.");
    const [approve, reject] = api.calls;
    expect(approve.headers["Content-Type"]).toBeUndefined();
    expect(approve.body).toBeUndefined();
    expect(reject.headers["Content-Type"]).toBe("application/json");
    expect(reject.body).toEqual({ reason: "Too many relocations before peak season." });
  });

  it("a reject without a reason sends no body at all", async () => {
    const api = mockApi({ [`POST /slot-plans/${PLAN_ID}/rejections`]: json(draftPlan({ state: "Rejected" })) });
    await rejectSlotPlan(PLAN_ID);
    expect(api.calls[0].body).toBeUndefined();
    expect(api.calls[0].headers["Content-Type"]).toBeUndefined();
  });

  it("two identical approve submits still use two different keys (one key per POST)", async () => {
    const api = mockApi({ [`POST /slot-plans/${PLAN_ID}/approvals`]: json(draftPlan({ state: "Approved" })) });
    await approveSlotPlan(PLAN_ID);
    await approveSlotPlan(PLAN_ID);
    const [a, b] = api.calls.map((c) => c.headers["Idempotency-Key"]);
    expect(a).not.toBe(b);
  });

  it("GETs send no custom header (they stay simple CORS requests)", async () => {
    const api = mockApi({
      "GET /slot-plans": json({ items: [planSummary()] }),
      [`GET /slot-plans/${PLAN_ID}`]: json(draftPlan()),
      "GET /forward-slots": json(forwardSlots()),
      "GET /sku-velocity": json(skuVelocity()),
    });
    await listSlotPlans({});
    await getSlotPlan(PLAN_ID);
    await listForwardSlots();
    await listSkuVelocity({});
    expect(api.calls).toHaveLength(4);
    for (const call of api.calls) expect(call.headers).toEqual({});
  });

  it("falls back to getRandomValues when crypto.randomUUID is missing (plain-http origins)", () => {
    const real = globalThis.crypto;
    vi.stubGlobal("crypto", { getRandomValues: real.getRandomValues.bind(real) });
    expect(newIdempotencyKey()).toMatch(UUID);
  });
});

describe("queries", () => {
  it("listSlotPlans sends the filters and maps the page", async () => {
    const api = mockApi({ "GET /slot-plans": json({ items: [planSummary()], nextCursor: "cGxhbi0z" }) });
    const page = await listSlotPlans({ limit: 25, cursor: "cGxhbi0y", siteId: "SITE-1", state: "Approved" });
    expect(page.items).toHaveLength(1);
    expect(page.nextCursor).toBe("cGxhbi0z");
    const q = api.calls[0].query;
    expect([q.get("limit"), q.get("cursor"), q.get("siteId"), q.get("state")]).toEqual([
      "25",
      "cGxhbi0y",
      "SITE-1",
      "Approved",
    ]);
  });

  it("empty filters are not sent, and an absent nextCursor maps to undefined", async () => {
    const api = mockApi({ "GET /slot-plans": json({ items: [] }) });
    const page = await listSlotPlans({});
    expect(page.items).toEqual([]);
    expect(page.nextCursor).toBeUndefined();
    expect(api.calls[0].url.endsWith("/slot-plans")).toBe(true);
  });

  it("getSlotPlan normalizes omitted arrays to empty ones", async () => {
    mockApi({ [`GET /slot-plans/${PLAN_ID}`]: json({ ...draftPlan(), assignments: undefined, moves: undefined, unassigned: undefined }) });
    const plan = await getSlotPlan(PLAN_ID);
    expect([plan.assignments, plan.moves, plan.unassigned]).toEqual([[], [], []]);
  });

  it("listSkuVelocity sends window, limit and site", async () => {
    const api = mockApi({ "GET /sku-velocity": json(skuVelocity()) });
    await listSkuVelocity({ windowDays: 14, limit: 50, siteId: "SITE-2" });
    const q = api.calls[0].query;
    expect([q.get("windowDays"), q.get("limit"), q.get("siteId")]).toEqual(["14", "50", "SITE-2"]);
  });

  it("listForwardSlots keeps the absent planId absent (no approved plan)", async () => {
    mockApi({ "GET /forward-slots": json({ siteId: "SITE-1", assignments: [] }) });
    const map = await listForwardSlots();
    expect(map.planId).toBeUndefined();
    expect(map.assignments).toEqual([]);
  });
});

describe("errors", () => {
  it("turns a problem+json body into an ApiError with title, detail, status and slug", async () => {
    mockApi({
      [`POST /slot-plans/${PLAN_ID}/approvals`]: problem(
        409,
        "plan-not-draft",
        "Only a Draft plan can be approved or rejected",
        "slot plan is not a Draft",
      ),
    });
    const err = await approveSlotPlan(PLAN_ID).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    const e = err as ApiError;
    expect([e.status, e.title, e.detail, e.slug]).toEqual([
      409,
      "Only a Draft plan can be approved or rejected",
      "slot plan is not a Draft",
      "plan-not-draft",
    ]);
  });

  it("falls back to the status line when the error body is not JSON", async () => {
    mockApi({ [`GET /slot-plans/${PLAN_ID}`]: new Response("bad gateway", { status: 502, statusText: "Bad Gateway" }) });
    const e = (await getSlotPlan(PLAN_ID).catch((x: unknown) => x)) as ApiError;
    expect([e.status, e.title, e.problem]).toEqual([502, "502 Bad Gateway", null]);
  });

  it("wraps a network failure (CORS, offline) as an ApiError with status 0", async () => {
    const e = toApiError(new TypeError("Failed to fetch"));
    expect([e.status, e.title, e.detail]).toEqual([0, "Network error", "Failed to fetch"]);
  });
});
