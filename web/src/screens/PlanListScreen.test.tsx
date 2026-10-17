import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { PlanListScreen } from "./PlanListScreen";
import { json, mockApi } from "../test/fetchMock";
import { PLAN_ID, planSummary } from "../test/fixtures";

function mount() {
  return render(
    <MemoryRouter initialEntries={["/plans"]}>
      <PlanListScreen />
    </MemoryRouter>,
  );
}

describe("PlanListScreen", () => {
  it("lists the plans with state, window, counts and a link to the detail", async () => {
    mockApi({ "GET /slot-plans": json({ items: [planSummary()] }) });
    mount();
    const link = await screen.findByRole("link", { name: PLAN_ID });
    expect(link).toHaveAttribute("href", `/plans/${PLAN_ID}`);

    const rows = within(screen.getByRole("table")).getAllByRole("row").slice(1);
    expect(rows[0].textContent).toBe(
      `${PLAN_ID}SITE-1Approved2026-09-10 → 2026-10-082026-10-08 12:00Z2026-10-08 12:30Z2312`,
    );
  });

  it("sends the state and site filters when applied", async () => {
    const api = mockApi({ "GET /slot-plans": json({ items: [] }) });
    const user = userEvent.setup();
    mount();
    await screen.findByText("No slot plans match these filters.");

    await user.selectOptions(screen.getByLabelText("State"), "Draft");
    await user.type(screen.getByLabelText("Site"), "SITE-2");
    await user.click(screen.getByRole("button", { name: "Apply filters" }));

    const q = api.calls.at(-1)?.query;
    expect([q?.get("state"), q?.get("siteId"), q?.get("limit")]).toEqual(["Draft", "SITE-2", "25"]);
  });

  it("follows the cursor forward and back", async () => {
    const api = mockApi({
      "GET /slot-plans": (call) =>
        call.query.get("cursor")
          ? json({ items: [planSummary({ planId: "plan-page2" })] })
          : json({ items: [planSummary({ planId: "plan-page1" })], nextCursor: "c2" }),
    });
    const user = userEvent.setup();
    mount();
    expect(await screen.findByRole("link", { name: "plan-page1" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Previous page" })).toBeDisabled();

    await user.click(screen.getByRole("button", { name: "Next page" }));
    expect(await screen.findByRole("link", { name: "plan-page2" })).toBeInTheDocument();
    expect(api.calls.at(-1)?.query.get("cursor")).toBe("c2");

    await user.click(screen.getByRole("button", { name: "Previous page" }));
    expect(await screen.findByRole("link", { name: "plan-page1" })).toBeInTheDocument();
  });

  it("renders the degraded state when the API is unreachable (no route claims the call)", async () => {
    // An empty route table makes the mocked fetch REJECT, exactly like a
    // network/CORS failure; the console loads remotes before login/network in
    // some setups, so the screen must render its problem, not crash.
    mockApi({});
    mount();
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Network error");
    expect(screen.getByRole("heading", { name: "Slot plans" })).toBeInTheDocument();
  });
});
