import { useState } from "react";
import { Card, DataTable } from "@warehouse/ui-kit";
import { listSkuVelocity } from "../api";
import { Form, FormRow, InlineError, SubmitButton, TextField } from "../components/formkit";
import { Facts, PageHeader, Stack } from "../components/layout";
import { RequestView } from "../components/ProblemAlert";
import { useRequest } from "../hooks/useRequest";
import { positiveWhole, siteIdProblem, windowLabel } from "../lib";

interface Query {
  windowDays: string;
  limit: string;
  siteId: string;
}

const INITIAL: Query = { windowDays: "28", limit: "100", siteId: "" };

/** One input field's local problem, or null. */
function fieldProblem(name: keyof Query, value: string): string | null {
  if (name === "siteId") return siteIdProblem(value);
  const n = positiveWhole(value);
  if (n === null) return value === "" ? "Enter a number." : "Enter a whole number.";
  if (name === "windowDays" && (n < 1 || n > 365)) return "Window is 1 to 365 days.";
  if (name === "limit" && (n < 1 || n > 500)) return "Limit is 1 to 500.";
  return null;
}

/**
 * The demand the planner works from: GET /sku-velocity, per SKU the ACTIVE
 * demand lines (picks) and their units over a lookback window, ordered by
 * picks. Read-only. The window and page size are inputs, not filters of a
 * stored report -- every change is a new query.
 */
export function SkuVelocityScreen() {
  const [draft, setDraft] = useState<Query>(INITIAL);
  const [applied, setApplied] = useState<Query>(INITIAL);
  const velocity = useRequest(
    () =>
      listSkuVelocity({
        windowDays: Number(applied.windowDays),
        limit: Number(applied.limit),
        siteId: applied.siteId.trim() || undefined,
      }),
    `sku-velocity|${applied.windowDays}|${applied.limit}|${applied.siteId}`,
  );

  const problems: (readonly [keyof Query, string])[] = [];
  for (const field of ["windowDays", "limit", "siteId"] as const) {
    const p = fieldProblem(field, draft[field]);
    if (p !== null) problems.push([field, p]);
  }
  const firstProblem = problems.length > 0 ? problems[0][1] : null;

  const apply = () => {
    if (firstProblem) return;
    setApplied({ ...draft, siteId: draft.siteId.trim() });
  };

  return (
    <Stack gap={5}>
      <PageHeader
        title="SKU velocity"
        subtitle="slotting-optimization · the demand the planner ranks SKUs by (read-only)"
      />
      <Card title="Query">
        <Form label="SKU velocity query" onSubmit={apply}>
          <FormRow>
            <TextField
              label="Window (days)"
              value={draft.windowDays}
              onChange={(v) => setDraft({ ...draft, windowDays: v })}
              hint="1 to 365, ending now"
              inputMode="numeric"
            />
            <TextField
              label="Top SKUs"
              value={draft.limit}
              onChange={(v) => setDraft({ ...draft, limit: v })}
              hint="1 to 500"
              inputMode="numeric"
            />
            <TextField
              label="Site"
              value={draft.siteId}
              onChange={(v) => setDraft({ ...draft, siteId: v })}
              placeholder="Default site"
              hint="Optional; the service's default site when empty"
            />
            <SubmitButton disabled={firstProblem !== null}>Show velocity</SubmitButton>
          </FormRow>
          <InlineError message={firstProblem} />
        </Form>
      </Card>
      <Card title="Velocity">
        <RequestView state={velocity} what="SKU velocity" isEmpty={(v) => v.items.length === 0} empty="No demand in this window: every SKU has zero ACTIVE lines.">
          {(v) => (
            <Stack gap={4}>
              <Facts
                items={[
                  { label: "Site", value: v.siteId },
                  { label: "Window", value: windowLabel(v.windowFrom, v.windowTo) },
                  { label: "SKUs", value: String(v.items.length) },
                ]}
              />
              <DataTable
                rowKey={(s) => s.sku}
                rows={v.items}
                columns={[
                  { key: "sku", header: "SKU", render: (s) => s.sku },
                  { key: "picks", header: "Picks", align: "right", render: (s) => String(s.picks) },
                  { key: "units", header: "Units", align: "right", render: (s) => String(s.units) },
                ]}
              />
            </Stack>
          )}
        </RequestView>
      </Card>
    </Stack>
  );
}
