import { describe, expect, it } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { PlanDetailRoute } from "./PlanDetailScreen";
import { json, mockApi, problem } from "../test/fetchMock";
import { OLD_PLAN_ID, PLAN_ID, approvedPlan, draftPlan, supersededPlan } from "../test/fixtures";

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

function mount(path = `/plans/${PLAN_ID}`) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/">
          <Route path="plans/:planId" element={<PlanDetailRoute />} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

describe("PlanDetailScreen (read view)", () => {
  it(
    "shows a Draft plan: assignments, moves without progress, unassigned, and the decision",
    async () => {
      mockApi({ [`GET /slot-plans/${PLAN_ID}`]: json(draftPlan()) });
      mount();
      expect(await screen.findByRole("heading", { name: PLAN_ID })).toBeInTheDocument();
      expect(screen.getByText("Draft")).toBeInTheDocument();
      expect(screen.getByText("2026-09-10 → 2026-10-08")).toBeInTheDocument();

      const tables = screen.getAllByRole("table");
      const assignments = within(tables[0]).getAllByRole("row").slice(1);
      expect(assignments.map((r) => r.textContent)).toEqual(["SKU-1WH1-FWD-A01-01-01-AA80160", "SKU-2WH1-FWD-A01-01-02-AB2040"]);

      const moves = within(tables[1]).getAllByRole("row").slice(1);
      expect(moves.map((r) => r.textContent)).toEqual([
        "0SKU-1Assign→ WH1-FWD-A01-01-01-A",
        "1SKU-2RelocateWH1-FWD-A02-03-01-A → WH1-FWD-A01-01-02-A",
        "2SKU-3VacateWH1-FWD-A01-01-03-A →",
      ]);
      // A Draft has no execution yet: no Executed column, but the hint.
      expect(screen.queryByText("Executed")).not.toBeInTheDocument();
      expect(screen.getByText(/Execution progress appears once the plan is Approved/)).toBeInTheDocument();

      const unassigned = within(tables[2]).getAllByRole("row").slice(1);
      expect(unassigned.map((r) => r.textContent)).toEqual(["SKU-4NoPhysicalProfile"]);

      expect(screen.getByRole("button", { name: "Approve plan…" })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Reject plan…" })).toBeInTheDocument();
    },
    // This test walks three tables under a busy CI box; the default 5s has
    // flaked under parallel fleet work.
    15000,
  );

  it("shows the execution view on an Approved plan: per-move status and the summary", async () => {
    mockApi({ [`GET /slot-plans/${PLAN_ID}`]: json(approvedPlan()) });
    mount();
    expect(await screen.findByRole("heading", { name: PLAN_ID })).toBeInTheDocument();
    expect(screen.getByText("1 of 3 moves executed · last 2026-10-08 13:05Z")).toBeInTheDocument();
    const moves = within(screen.getAllByRole("table")[1]).getAllByRole("row").slice(1);
    expect(moves[0].textContent).toContain("✓ yes");
    expect(moves[0].textContent).toContain("160");
    expect(moves[1].textContent).toContain("— not yet");
    // No decision on a non-Draft.
    expect(screen.queryByRole("button", { name: "Approve plan…" })).not.toBeInTheDocument();
  });

  it("shows supersession info and links the superseded plan", async () => {
    mockApi({ [`GET /slot-plans/${PLAN_ID}`]: json(approvedPlan()) });
    mount();
    const link = await screen.findByRole("link", { name: OLD_PLAN_ID });
    expect(link).toHaveAttribute("href", `/plans/${OLD_PLAN_ID}`);
  });

  it("renders the problem when the plan cannot be loaded (404)", async () => {
    mockApi({ [`GET /slot-plans/${PLAN_ID}`]: problem(404, "plan-not-found", "Slot plan not found", "slot plan not found") });
    mount();
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Slot plan not found");
    expect(alert).toHaveTextContent("slot plan not found");
  });
});

describe("PlanDetailScreen (approve flow)", () => {
  it("confirms before submitting, sends the Idempotency-Key, and reloads the approved plan", async () => {
    // First load: Draft. After the POST the screen reloads the plan; serve
    // the approved shape from then on.
    let approved = false;
    const api = mockApi({
      [`GET /slot-plans/${PLAN_ID}`]: () => json(approved ? approvedPlan() : draftPlan()),
      [`POST /slot-plans/${PLAN_ID}/approvals`]: () => {
        approved = true;
        return json(approvedPlan());
      },
    });
    const user = userEvent.setup();
    mount();

    // Arming only shows the confirmation panel -- nothing is sent yet.
    await user.click(await screen.findByRole("button", { name: "Approve plan…" }));
    expect(screen.getByText(/Approve this plan\?/)).toBeInTheDocument();
    expect(api.calls.filter((c) => c.method === "POST")).toHaveLength(0);

    await user.click(screen.getByRole("button", { name: "Confirm approval" }));
    expect(await screen.findByText("Plan approved. It is now the site's forward-slot map.")).toBeInTheDocument();

    const post = api.to(`POST /slot-plans/${PLAN_ID}/approvals`);
    expect(post).toHaveLength(1);
    expect(post[0].headers["Idempotency-Key"]).toMatch(UUID);
    expect(post[0].body).toBeUndefined();
    // The screen reloaded the plan after the decision (2 GETs: initial + after).
    await waitFor(() => expect(api.to(`GET /slot-plans/${PLAN_ID}`)).toHaveLength(2));
    expect(await screen.findByText("1 of 3 moves executed · last 2026-10-08 13:05Z")).toBeInTheDocument();
  });

  it("cancel returns to the decision without POSTing", async () => {
    const api = mockApi({ [`GET /slot-plans/${PLAN_ID}`]: json(draftPlan()) });
    const user = userEvent.setup();
    mount();
    await user.click(await screen.findByRole("button", { name: "Approve plan…" }));
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.getByRole("button", { name: "Approve plan…" })).toBeInTheDocument();
    expect(api.calls.filter((c) => c.method === "POST")).toHaveLength(0);
  });

  it("renders the 409 problem when the plan is no longer a Draft, with reload guidance", async () => {
    mockApi({
      [`GET /slot-plans/${PLAN_ID}`]: json(draftPlan()),
      [`POST /slot-plans/${PLAN_ID}/approvals`]: problem(
        409,
        "plan-not-draft",
        "Only a Draft plan can be approved or rejected",
        "slot plan is not a Draft",
      ),
    });
    const user = userEvent.setup();
    mount();
    await user.click(await screen.findByRole("button", { name: "Approve plan…" }));
    await user.click(screen.getByRole("button", { name: "Confirm approval" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Only a Draft plan can be approved or rejected");
    expect(alert).toHaveTextContent("slot plan is not a Draft");
    expect(alert).toHaveTextContent("This plan is no longer a Draft -- reload the list to see the latest state.");
    expect(alert).toHaveTextContent("HTTP 409");
  });

  it("renders the 409 superseded-race problem with its own guidance", async () => {
    mockApi({
      [`GET /slot-plans/${PLAN_ID}`]: json(draftPlan()),
      [`POST /slot-plans/${PLAN_ID}/approvals`]: problem(
        409,
        "approved-plan-conflict",
        "Another plan of this site was approved at the same time",
        "the site already has a newer Approved plan; re-fetch and retry",
      ),
    });
    const user = userEvent.setup();
    mount();
    await user.click(await screen.findByRole("button", { name: "Approve plan…" }));
    await user.click(screen.getByRole("button", { name: "Confirm approval" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Another plan of this site was approved at the same time");
    expect(alert).toHaveTextContent("Another plan of this site was approved first; re-fetch and review the new state.");
  });
});

describe("PlanDetailScreen (reject flow)", () => {
  it("sends the typed reason in the body with an Idempotency-Key", async () => {
    let rejected = false;
    const api = mockApi({
      [`GET /slot-plans/${PLAN_ID}`]: () => json(rejected ? draftPlan({ state: "Rejected", rejectedAt: "2026-10-08T12:45:00Z", rejectReason: "Too many relocations before peak season." }) : draftPlan()),
      [`POST /slot-plans/${PLAN_ID}/rejections`]: () => {
        rejected = true;
        return json(draftPlan({ state: "Rejected", rejectedAt: "2026-10-08T12:45:00Z", rejectReason: "Too many relocations before peak season." }));
      },
    });
    const user = userEvent.setup();
    mount();
    await user.click(await screen.findByRole("button", { name: "Reject plan…" }));
    await user.type(screen.getByLabelText(/^Reason/), "Too many relocations before peak season.");
    await user.click(screen.getByRole("button", { name: "Confirm rejection" }));
    expect(await screen.findByText("Plan rejected.")).toBeInTheDocument();
    expect(await screen.findByText("Too many relocations before peak season.")).toBeInTheDocument();

    const post = api.to(`POST /slot-plans/${PLAN_ID}/rejections`);
    expect(post).toHaveLength(1);
    expect(post[0].headers["Idempotency-Key"]).toMatch(UUID);
    expect(post[0].body).toEqual({ reason: "Too many relocations before peak season." });
  });

  it("submits no body when the reason is left empty", async () => {
    const api = mockApi({
      [`GET /slot-plans/${PLAN_ID}`]: json(draftPlan()),
      [`POST /slot-plans/${PLAN_ID}/rejections`]: json(draftPlan({ state: "Rejected", rejectedAt: "2026-10-08T12:45:00Z" })),
    });
    const user = userEvent.setup();
    mount();
    await user.click(await screen.findByRole("button", { name: "Reject plan…" }));
    await user.click(screen.getByRole("button", { name: "Confirm rejection" }));
    const post = api.to(`POST /slot-plans/${PLAN_ID}/rejections`);
    expect(post[0].body).toBeUndefined();
    expect(post[0].headers["Content-Type"]).toBeUndefined();
  });

  it("refuses an over-long reason locally (the API would 400 reject-reason-too-long)", async () => {
    const api = mockApi({ [`GET /slot-plans/${PLAN_ID}`]: json(draftPlan()) });
    const user = userEvent.setup();
    mount();
    await user.click(await screen.findByRole("button", { name: "Reject plan…" }));
    // fireEvent.change, not 501 simulated keystrokes: the point is the
    // validation, not the typing.
    fireEvent.change(screen.getByLabelText(/^Reason/), { target: { value: "x".repeat(501) } });
    expect(screen.getByRole("button", { name: "Confirm rejection" })).toBeDisabled();
    expect(screen.getByText("A reject reason is at most 500 characters.")).toBeInTheDocument();
    expect(api.calls.filter((c) => c.method === "POST")).toHaveLength(0);
  });

  it("renders a Superseded plan without any decision UI", async () => {
    mockApi({ [`GET /slot-plans/${OLD_PLAN_ID}`]: json(supersededPlan()) });
    mount(`/plans/${OLD_PLAN_ID}`);
    expect(await screen.findByRole("heading", { name: OLD_PLAN_ID })).toBeInTheDocument();
    expect(screen.getByText(/a newer plan took the forward-slot map/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve plan…" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Reject plan…" })).not.toBeInTheDocument();
  });
});
