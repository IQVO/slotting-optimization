import { useState } from "react";
import { Link } from "react-router-dom";
import { Card, DataTable } from "@warehouse/ui-kit";
import { listSlotPlans } from "../api";
import { Form, FormRow, SelectField, SubmitButton, TextField } from "../components/formkit";
import { PageHeader, Stack, StatePill } from "../components/layout";
import { RequestView } from "../components/ProblemAlert";
import { useRequest } from "../hooks/useRequest";
import { siteIdProblem, utcLabel, windowLabel } from "../lib";
import { PLAN_STATES } from "../types";
import type { PlanState, SlotPlanSummary } from "../types";

const STATE_OPTIONS = PLAN_STATES.map((s) => ({ value: s, label: s }));

interface Filters {
  state: "" | PlanState;
  siteId: string;
}

const INITIAL: Filters = { state: "", siteId: "" };

/**
 * Slot-plan list: GET /slot-plans, newest first, a page at a time, optionally
 * narrowed to one site and one lifecycle state. Paging follows the opaque
 * `nextCursor`; the cursors already visited are kept so "Previous" walks back
 * without the API having to support it. Changing the filters starts again at
 * the first page.
 */
export function PlanListScreen() {
  const [draft, setDraft] = useState<Filters>(INITIAL);
  const [applied, setApplied] = useState<Filters>(INITIAL);
  // cursors[i] is the cursor of page i+1 ("" = the first page).
  const [cursors, setCursors] = useState<string[]>([""]);
  const cursor = cursors[cursors.length - 1];
  const page = useRequest(
    () =>
      listSlotPlans({
        limit: 25,
        cursor: cursor || undefined,
        siteId: applied.siteId.trim() || undefined,
        state: applied.state || undefined,
      }),
    `plans|${applied.state}|${applied.siteId}|${cursor}`,
  );

  const siteProblem = siteIdProblem(draft.siteId);
  const apply = () => {
    if (siteProblem) return;
    setApplied({ state: draft.state, siteId: draft.siteId.trim() });
    setCursors([""]);
  };

  return (
    <Stack gap={5}>
      <PageHeader
        title="Slot plans"
        subtitle="slotting-optimization · proposed forward-slot maps and the decision on each"
      />
      <Card title="Filters">
        <Form label="Slot plan filters" onSubmit={apply}>
          <FormRow>
            <SelectField
              label="State"
              value={draft.state}
              onChange={(v) => setDraft({ ...draft, state: v as Filters["state"] })}
              options={STATE_OPTIONS}
              emptyLabel="Any state"
            />
            <TextField
              label="Site"
              value={draft.siteId}
              onChange={(v) => setDraft({ ...draft, siteId: v })}
              placeholder="Default site"
              hint="Optional; the service's default site when empty"
            />
            <SubmitButton disabled={siteProblem !== null}>Apply filters</SubmitButton>
          </FormRow>
        </Form>
      </Card>
      <Card
        title={`Page ${cursors.length}`}
        actions={
          <div style={{ display: "flex", gap: "var(--wh-space-2)" }}>
            <SubmitButton
              type="button"
              secondary
              disabled={cursors.length === 1 || page.status === "loading"}
              onClick={() => setCursors(cursors.slice(0, -1))}
            >
              Previous page
            </SubmitButton>
            <SubmitButton
              type="button"
              secondary
              disabled={page.status !== "success" || !page.data.nextCursor}
              onClick={() => {
                if (page.status === "success" && page.data.nextCursor) {
                  setCursors([...cursors, page.data.nextCursor]);
                }
              }}
            >
              Next page
            </SubmitButton>
          </div>
        }
      >
        <RequestView
          state={page}
          what="slot plans"
          isEmpty={(p) => p.items.length === 0}
          empty={cursors.length === 1 ? "No slot plans match these filters." : "No more slot plans on this page."}
        >
          {(p) => <PlanTable plans={p.items} />}
        </RequestView>
      </Card>
    </Stack>
  );
}

function PlanTable({ plans }: { plans: SlotPlanSummary[] }) {
  return (
    <DataTable
      rowKey={(p) => p.planId}
      rows={plans}
      columns={[
        {
          key: "plan",
          header: "Plan",
          // Relative link: `..` is the parent route (this remote's mount
          // point), so it works under the console's /slotting-optimization/*
          // and standalone at /.
          render: (p) => <Link to={`../plans/${encodeURIComponent(p.planId)}`}>{p.planId}</Link>,
        },
        { key: "site", header: "Site", render: (p) => p.siteId },
        { key: "state", header: "State", render: (p) => <StatePill state={p.state} /> },
        { key: "window", header: "Demand window", render: (p) => windowLabel(p.windowFrom, p.windowTo) },
        { key: "generated", header: "Generated", render: (p) => utcLabel(p.generatedAt) },
        { key: "decided", header: "Approved", render: (p) => (p.approvedAt ? utcLabel(p.approvedAt) : "—") },
        { key: "assignments", header: "Assignments", align: "right", render: (p) => String(p.assignmentCount) },
        { key: "moves", header: "Moves", align: "right", render: (p) => String(p.moveCount) },
        { key: "unassigned", header: "Unassigned", align: "right", render: (p) => String(p.unassignedCount) },
        { key: "version", header: "Version", align: "right", render: (p) => String(p.version) },
      ]}
    />
  );
}
