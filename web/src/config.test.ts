import { describe, expect, it } from "vitest";
import { SLOTTING_API_BASE, resolveSlottingApiBase } from "./config";

describe("resolveSlottingApiBase", () => {
  it("builds the production API base from the runtime API origin", () => {
    expect(resolveSlottingApiBase({ apiOrigin: "http://localhost:8000" }, true)).toBe(
      "http://localhost:8000/api/slotting-optimization",
    );
  });

  it("normalizes trailing slashes on the runtime API origin", () => {
    expect(resolveSlottingApiBase({ apiOrigin: "https://warehouse.example/" }, true)).toBe(
      "https://warehouse.example/api/slotting-optimization",
    );
    expect(resolveSlottingApiBase({ apiOrigin: "https://warehouse.example///" }, true)).toBe(
      "https://warehouse.example/api/slotting-optimization",
    );
  });

  it("fails loudly when production runtime configuration has no API origin", () => {
    expect(() => resolveSlottingApiBase({}, true)).toThrow(
      "window.__WAREHOUSE_CONFIG__.apiOrigin is required in production",
    );
    expect(() => resolveSlottingApiBase({ apiOrigin: "" }, true)).toThrow(
      "window.__WAREHOUSE_CONFIG__.apiOrigin is required in production",
    );
  });

  it("falls back to the service's own dev port outside production", () => {
    expect(resolveSlottingApiBase({}, false)).toBe("http://localhost:8080");
  });

  it("still prefers a runtime origin over the dev fallback", () => {
    expect(resolveSlottingApiBase({ apiOrigin: "http://localhost:8000" }, false)).toBe(
      "http://localhost:8000/api/slotting-optimization",
    );
  });
});

describe("SLOTTING_API_BASE", () => {
  it("loads without /config.json (window.__WAREHOUSE_CONFIG__ absent) in a non-production build", () => {
    expect(window.__WAREHOUSE_CONFIG__).toBeUndefined();
    expect(SLOTTING_API_BASE).toBe("http://localhost:8080");
  });
});
