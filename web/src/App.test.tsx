import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import App from "./App";
import { json, mockApi } from "./test/fetchMock";
import { PLAN_ID, draftPlan, forwardSlots, planSummary, skuVelocity } from "./test/fixtures";

function mount(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <App />
    </MemoryRouter>,
  );
}

/** Mounted the way the console mounts the remote: inside the host's own
 *  `<Route path="/slotting-optimization/*">` splat route. Relative links
 *  behave differently there than at the router root, which root-mounted
 *  tests cannot see. */
function mountUnderHost(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/slotting-optimization/*" element={<App />} />
      </Routes>
    </MemoryRouter>,
  );
}

const SECTIONS = [
  "/slotting-optimization/plans",
  "/slotting-optimization/forward-slots",
  "/slotting-optimization/sku-velocity",
];

function subNavHrefs(): string[] {
  const nav = screen.getByRole("navigation", { name: "Slotting optimization sections" });
  return within(nav)
    .getAllByRole("link")
    .map((a) => a.getAttribute("href") ?? "");
}

function routes() {
  return mockApi({
    "GET /slot-plans": json({ items: [planSummary()] }),
    [`GET /slot-plans/${PLAN_ID}`]: json(draftPlan()),
    "GET /forward-slots": json(forwardSlots()),
    "GET /sku-velocity": json(skuVelocity()),
  });
}

describe("App (the exposed ./App)", () => {
  it("redirects the index route to the plan list and shows the sub-nav", async () => {
    routes();
    mount("/");
    expect(await screen.findByRole("heading", { name: "Slot plans" })).toBeInTheDocument();
    const nav = screen.getByRole("navigation", { name: "Slotting optimization sections" });
    for (const label of ["Slot plans", "Forward slots", "SKU velocity"]) expect(nav).toHaveTextContent(label);
  });

  it("routes relatively at the root: every section and detail route renders", async () => {
    routes();
    const cases: [string, string][] = [
      ["/forward-slots", "Forward slots"],
      ["/sku-velocity", "SKU velocity"],
      [`/plans/${PLAN_ID}`, PLAN_ID],
    ];
    for (const [path, heading] of cases) {
      const { unmount } = mount(path);
      expect(await screen.findByRole("heading", { name: heading })).toBeInTheDocument();
      unmount();
    }
  });

  it("renders with the API unreachable (degraded, no crash) -- the console loads remotes before login/network", async () => {
    mockApi({});
    mount("/");
    expect(await screen.findByRole("heading", { name: "Slot plans" })).toBeInTheDocument();
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Network error");
  });

  describe("mounted under the console's /slotting-optimization/* splat route", () => {
    it("redirects /slotting-optimization to /slotting-optimization/plans", async () => {
      routes();
      mountUnderHost("/slotting-optimization");
      expect(await screen.findByRole("heading", { name: "Slot plans" })).toBeInTheDocument();
    });

    it.each([
      "/slotting-optimization/plans",
      "/slotting-optimization/forward-slots",
      "/slotting-optimization/sku-velocity",
      `/slotting-optimization/plans/${PLAN_ID}`,
    ])("sub-nav links resolve against the mount point, not the current URL (%s)", async (start) => {
      routes();
      mountUnderHost(start);
      await screen.findByRole("navigation", { name: "Slotting optimization sections" });
      expect(subNavHrefs()).toEqual(SECTIONS);
    });

    it("marks only the current section active", async () => {
      routes();
      mountUnderHost("/slotting-optimization/sku-velocity");
      const nav = await screen.findByRole("navigation", { name: "Slotting optimization sections" });
      for (const name of ["Slot plans", "Forward slots", "SKU velocity"]) {
        const link = within(nav).getByRole("link", { name });
        if (name === "SKU velocity") expect(link).toHaveAttribute("aria-current", "page");
        else expect(link).not.toHaveAttribute("aria-current", "page");
      }
    });

    it("links a plan row to its page and back, under the prefix", async () => {
      routes();
      const user = userEvent.setup();
      mountUnderHost("/slotting-optimization/plans");
      const row = await screen.findByRole("link", { name: PLAN_ID });
      expect(row).toHaveAttribute("href", `/slotting-optimization/plans/${PLAN_ID}`);

      await user.click(row);
      expect(await screen.findByRole("heading", { name: PLAN_ID })).toBeInTheDocument();
      const back = screen.getByRole("link", { name: "← All slot plans" });
      expect(back).toHaveAttribute("href", "/slotting-optimization/plans");

      await user.click(back);
      expect(await screen.findByRole("heading", { name: "Slot plans" })).toBeInTheDocument();
    });

    it("navigates between all sections in any order, hop after hop", async () => {
      routes();
      const user = userEvent.setup();
      mountUnderHost("/slotting-optimization/plans");
      expect(await screen.findByRole("heading", { name: "Slot plans" })).toBeInTheDocument();

      await user.click(screen.getByRole("link", { name: "Forward slots" }));
      expect(await screen.findByRole("heading", { name: "Forward slots" })).toBeInTheDocument();

      await user.click(screen.getByRole("link", { name: "SKU velocity" }));
      expect(await screen.findByRole("heading", { name: "SKU velocity" })).toBeInTheDocument();

      await user.click(screen.getByRole("link", { name: "Slot plans" }));
      expect(await screen.findByRole("heading", { name: "Slot plans" })).toBeInTheDocument();
      expect(subNavHrefs()).toEqual(SECTIONS);
    });
  });
});
