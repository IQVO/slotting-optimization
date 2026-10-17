import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { ForwardSlotsScreen } from "./ForwardSlotsScreen";
import { json, mockApi } from "../test/fetchMock";
import { PLAN_ID, forwardSlots } from "../test/fixtures";

function mount() {
  return render(
    <MemoryRouter initialEntries={["/forward-slots"]}>
      <ForwardSlotsScreen />
    </MemoryRouter>,
  );
}

describe("ForwardSlotsScreen", () => {
  it("shows the approved map with its plan link and approval time", async () => {
    mockApi({ "GET /forward-slots": json(forwardSlots()) });
    mount();
    const planLink = await screen.findByRole("link", { name: PLAN_ID });
    expect(planLink).toHaveAttribute("href", `/plans/${PLAN_ID}`);
    expect(screen.getByText("2026-10-08 12:30Z")).toBeInTheDocument();

    const rows = within(screen.getByRole("table")).getAllByRole("row").slice(1);
    expect(rows.map((r) => r.textContent)).toEqual(["SKU-1WH1-FWD-A01-01-01-AA", "SKU-2WH1-FWD-A01-01-02-AB"]);
  });

  it("renders the no-approved-plan state when the map is empty", async () => {
    mockApi({ "GET /forward-slots": json({ siteId: "SITE-1", assignments: [] }) });
    mount();
    expect(await screen.findByText("No forward-slot map: this site has no Approved plan yet.")).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("queries the entered site", async () => {
    const api = mockApi({ "GET /forward-slots": json(forwardSlots({ siteId: "SITE-2" })) });
    const user = userEvent.setup();
    mount();
    await screen.findByText("SKU-1");
    await user.type(screen.getByLabelText("Site"), "SITE-2");
    await user.click(screen.getByRole("button", { name: "Show forward slots" }));
    expect(api.calls.at(-1)?.query.get("siteId")).toBe("SITE-2");
  });
});
