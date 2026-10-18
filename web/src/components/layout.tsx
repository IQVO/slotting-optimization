import type { ReactNode } from "react";
import { StatusPill } from "@warehouse/ui-kit";
import type { MoveKind, PlanState } from "../types";

export function PageHeader({ title, subtitle }: { title: string; subtitle: ReactNode }) {
  return (
    <div>
      <h1 style={{ fontSize: "var(--wh-font-size-2xl)", margin: 0 }}>{title}</h1>
      <p style={{ color: "var(--wh-color-text-muted)", marginTop: 4 }}>{subtitle}</p>
    </div>
  );
}

export function Stack({ children, gap = 4 }: { children: ReactNode; gap?: 3 | 4 | 5 }) {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: `var(--wh-space-${gap})` }}>
      {children}
    </div>
  );
}

/** A label/value list (dl) for a record's scalar fields. */
export function Facts({ items }: { items: { label: string; value: ReactNode }[] }) {
  return (
    <dl
      style={{
        display: "grid",
        gridTemplateColumns: "max-content 1fr",
        gap: "var(--wh-space-2) var(--wh-space-4)",
        margin: 0,
        fontSize: "var(--wh-font-size-sm)",
      }}
    >
      {items.map((item) => (
        <div key={item.label} style={{ display: "contents" }}>
          <dt style={{ color: "var(--wh-color-text-muted)" }}>{item.label}</dt>
          <dd style={{ margin: 0 }}>{item.value}</dd>
        </div>
      ))}
    </dl>
  );
}

type Tone = "neutral" | "progress" | "success" | "warning" | "danger";

/** These lifecycles are slotting-optimization's own, so StatusPill's tone
 *  override carries them. */
const STATE_TONE: Record<PlanState, Tone> = {
  Draft: "neutral",
  Approved: "success",
  Rejected: "danger",
  Superseded: "warning",
};

export function StatePill({ state }: { state: PlanState }) {
  return <StatusPill status={state} tone={STATE_TONE[state]} size="sm" />;
}

const KIND_TONE: Record<MoveKind, Tone> = {
  Assign: "success",
  Relocate: "progress",
  Vacate: "warning",
};

export function MoveKindPill({ kind }: { kind: MoveKind }) {
  return <StatusPill status={kind} tone={KIND_TONE[kind]} size="sm" />;
}
