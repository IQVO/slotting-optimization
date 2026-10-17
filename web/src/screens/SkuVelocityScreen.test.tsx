import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { SkuVelocityScreen } from "./SkuVelocityScreen";
import { json, mockApi, problem } from "../test/fetchMock";
import { skuVelocity } from "../test/fixtures";

function mount() {
  return render(
    <MemoryRouter initialEntries={["/sku-velocity"]}>
      <SkuVelocityScreen />
    </MemoryRouter>,
  );
}

describe("SkuVelocityScreen", () => {
  it("loads the default 28-day window on mount and lists the SKUs", async () => {
    const api = mockApi({ "GET /sku-velocity": json(skuVelocity()) });
    mount();
    expect(await screen.findByText("SKU-1")).toBeInTheDocument();
    expect(screen.getByText("2026-09-10 → 2026-10-08")).toBeInTheDocument();
    const rows = within(screen.getByRole("table")).getAllByRole("row").slice(1);
    expect(rows.map((r) => r.textContent)).toEqual(["SKU-180160", "SKU-22040"]);
    expect(api.calls[0].query.get("windowDays")).toBe("28");
    expect(api.calls[0].query.get("limit")).toBe("100");
  });

  it("re-queries with the entered window, limit and site", async () => {
    const api = mockApi({ "GET /sku-velocity": json(skuVelocity()) });
    const user = userEvent.setup();
    mount();
    await screen.findByText("SKU-1");

    await user.clear(screen.getByLabelText(/Window \(days\)/));
    await user.type(screen.getByLabelText(/Window \(days\)/), "7");
    await user.clear(screen.getByLabelText("Top SKUs"));
    await user.type(screen.getByLabelText("Top SKUs"), "50");
    await user.type(screen.getByLabelText("Site"), "SITE-2");
    await user.click(screen.getByRole("button", { name: "Show velocity" }));

    const q = api.calls.at(-1)?.query;
    expect([q?.get("windowDays"), q?.get("limit"), q?.get("siteId")]).toEqual(["7", "50", "SITE-2"]);
  });

  it("refuses an out-of-range window locally (the API would 400 invalid-limit)", async () => {
    const api = mockApi({ "GET /sku-velocity": json(skuVelocity()) });
    const user = userEvent.setup();
    mount();
    await screen.findByText("SKU-1");
    await user.clear(screen.getByLabelText(/Window \(days\)/));
    await user.type(screen.getByLabelText(/Window \(days\)/), "400");
    expect(screen.getByRole("button", { name: "Show velocity" })).toBeDisabled();
    expect(screen.getByText("Window is 1 to 365 days.")).toBeInTheDocument();
    expect(api.calls).toHaveLength(1);
  });

  it("renders the problem when the query fails", async () => {
    mockApi({ "GET /sku-velocity": problem(500, "internal-error", "Internal server error", "an unexpected error occurred") });
    mount();
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Internal server error");
  });
});
