import { describe, expect, it } from "vitest";
import { movePath, positiveWhole, reasonProblem, siteIdProblem, utcLabel, windowLabel } from "./lib";
import { NO_VALUE } from "@warehouse/ui-kit";

describe("utcLabel", () => {
  it("formats an RFC 3339 instant as a compact UTC stamp", () => {
    expect(utcLabel("2026-10-08T12:30:00Z")).toBe("2026-10-08 12:30Z");
  });

  it("renders the placeholder for an absent instant and the raw text when unparseable", () => {
    expect(utcLabel(undefined)).toBe(NO_VALUE);
    expect(utcLabel("not-a-date")).toBe("not-a-date");
  });
});

describe("windowLabel", () => {
  it("renders a plan window as inclusive → exclusive dates", () => {
    expect(windowLabel("2026-09-10T12:00:00Z", "2026-10-08T12:00:00Z")).toBe("2026-09-10 → 2026-10-08");
  });
});

describe("positiveWhole", () => {
  it("accepts whole positive numbers only", () => {
    expect(positiveWhole("28")).toBe(28);
    expect(positiveWhole(" 7 ")).toBe(7);
    expect(positiveWhole("0")).toBeNull();
    expect(positiveWhole("12.5")).toBeNull();
    expect(positiveWhole("1e3")).toBeNull();
    expect(positiveWhole("abc")).toBeNull();
    expect(positiveWhole("")).toBeNull();
  });
});

describe("siteIdProblem", () => {
  it("accepts the empty site (the service's default) and well-formed ids", () => {
    expect(siteIdProblem("")).toBeNull();
    expect(siteIdProblem("SITE-1")).toBeNull();
  });

  it("rejects what the API rejects (invalid-site-id)", () => {
    expect(siteIdProblem("SITE 1")).not.toBeNull();
    expect(siteIdProblem("SITE/1")).not.toBeNull();
    expect(siteIdProblem("S".repeat(65))).not.toBeNull();
  });
});

describe("reasonProblem", () => {
  it("rejects only an over-long reject reason (reject-reason-too-long)", () => {
    expect(reasonProblem("")).toBeNull();
    expect(reasonProblem("Too many relocations before peak season.")).toBeNull();
    expect(reasonProblem("x".repeat(501))).not.toBeNull();
  });
});

describe("movePath", () => {
  it("renders each move kind's slot path", () => {
    expect(movePath(undefined, "WH1-FWD-A01-01-01-A")).toBe("→ WH1-FWD-A01-01-01-A");
    expect(movePath("WH1-FWD-A02-03-01-A", "WH1-FWD-A01-01-02-A")).toBe("WH1-FWD-A02-03-01-A → WH1-FWD-A01-01-02-A");
    expect(movePath("WH1-FWD-A01-01-03-A", undefined)).toBe("WH1-FWD-A01-01-03-A →");
  });
});
