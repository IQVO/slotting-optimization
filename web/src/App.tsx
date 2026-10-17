import { Navigate, NavLink, Outlet, Route, Routes } from "react-router-dom";
import { ForwardSlotsScreen } from "./screens/ForwardSlotsScreen";
import { PlanDetailRoute } from "./screens/PlanDetailScreen";
import { PlanListScreen } from "./screens/PlanListScreen";
import { SkuVelocityScreen } from "./screens/SkuVelocityScreen";

const SUB_NAV = [
  { to: "plans", label: "Slot plans", end: true },
  { to: "forward-slots", label: "Forward slots", end: false },
  { to: "sku-velocity", label: "SKU velocity", end: false },
];

const linkStyle = ({ isActive }: { isActive: boolean }) => ({
  display: "inline-flex",
  padding: "6px 12px",
  borderRadius: "var(--wh-radius-pill)",
  fontSize: "var(--wh-font-size-sm)",
  fontWeight: isActive ? 600 : 500,
  color: isActive ? "var(--wh-color-text)" : "var(--wh-color-text-muted)",
  background: isActive ? "var(--wh-color-accent-muted)" : "transparent",
  textDecoration: "none",
});

/** The sub-nav, rendered by a layout route with the path "/".
 *
 *  The console mounts this component inside its own `<Route
 *  path="/slotting-optimization/*">`. A relative `<NavLink>` rendered straight in
 *  that splat route resolves against the FULL current URL (react-router 7 uses
 *  the last path-contributing match's pathname, splat included), so from
 *  /slotting-optimization/forward-slots the link `plans` would point at
 *  /slotting-optimization/forward-slots/plans. A pathless layout route does not
 *  help (react-router drops pathless routes from relative resolution); a layout
 *  route WITH path "/" does: its own match is the mount point, under any
 *  prefix and standalone at `/`. (Same fix as the inbound-receiving remote;
 *  App.test.tsx mounts under the host's splat route to prove it.) */
function SlottingLayout() {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "var(--wh-space-5)" }}>
      <nav aria-label="Slotting optimization sections" style={{ display: "flex", gap: "var(--wh-space-2)" }}>
        {SUB_NAV.map((item) => (
          <NavLink key={item.to} to={item.to} end={item.end} style={linkStyle}>
            {item.label}
          </NavLink>
        ))}
      </nav>
      <Outlet />
    </div>
  );
}

/** Exposed as slotting_mfe/App via Module Federation. Takes NO props: the
 *  console mounts it under `/slotting-optimization/*` and provides the
 *  BrowserRouter, the design tokens and `window.__WAREHOUSE_CONFIG__`. Routes
 *  here are RELATIVE, so the component works identically under that prefix (in
 *  the shell) or at / (standalone dev, see main.tsx).
 *
 *   - (index): redirects to plans.
 *   - plans: the slot-plan list with state/site filters and paging.
 *   - plans/:planId: one plan -- assignments, moves with their execution
 *     status, unassigned SKUs, supersession info, and the approve/reject
 *     decision (each POST carries an Idempotency-Key).
 *   - forward-slots: the site's currently Approved forward-slot map.
 *   - sku-velocity: the demand the planner works from, read-only. */
export default function App() {
  return (
    <Routes>
      <Route path="/" element={<SlottingLayout />}>
        <Route index element={<Navigate to="plans" replace />} />
        <Route path="plans" element={<PlanListScreen />} />
        <Route path="plans/:planId" element={<PlanDetailRoute />} />
        <Route path="forward-slots" element={<ForwardSlotsScreen />} />
        <Route path="sku-velocity" element={<SkuVelocityScreen />} />
      </Route>
    </Routes>
  );
}
