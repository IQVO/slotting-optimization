import { useState } from "react";
import { Link } from "react-router-dom";
import { Card, DataTable } from "@warehouse/ui-kit";
import { listForwardSlots } from "../api";
import { Form, FormRow, InlineError, SubmitButton, TextField } from "../components/formkit";
import { Facts, PageHeader, Stack } from "../components/layout";
import { RequestView } from "../components/ProblemAlert";
import { useRequest } from "../hooks/useRequest";
import { siteIdProblem, utcLabel } from "../lib";

/**
 * The site's currently Approved forward-slot map: GET /forward-slots, in SKU
 * order. Read-only -- the map only changes by approving a new slot plan (see
 * the plans screens). While the site has no Approved plan the API returns an
 * empty map and no planId, and that state is rendered as such.
 */
export function ForwardSlotsScreen() {
  const [draftSite, setDraftSite] = useState("");
  const [appliedSite, setAppliedSite] = useState("");
  const map = useRequest(() => listForwardSlots(appliedSite.trim() || undefined), `forward-slots|${appliedSite}`);

  const siteErr = siteIdProblem(draftSite);
  const apply = () => {
    if (siteErr) return;
    setAppliedSite(draftSite.trim());
  };

  return (
    <Stack gap={5}>
      <PageHeader
        title="Forward slots"
        subtitle="slotting-optimization · the forward pick map of the site's Approved plan"
      />
      <Card title="Site">
        <Form label="Forward slots site" onSubmit={apply}>
          <FormRow>
            <TextField
              label="Site"
              value={draftSite}
              onChange={setDraftSite}
              placeholder="Default site"
              hint="Optional; the service's default site when empty"
            />
            <SubmitButton disabled={siteErr !== null}>Show forward slots</SubmitButton>
          </FormRow>
          <InlineError message={siteErr} />
        </Form>
      </Card>
      <Card title="Forward-slot map">
        <RequestView state={map} what="forward slots" isEmpty={(m) => m.assignments.length === 0} empty={
          <Stack gap={3}>
            <div>No forward-slot map: this site has no Approved plan yet.</div>
            <div>
              Generate and approve one from the <Link to="../plans">slot plans</Link> screens.
            </div>
          </Stack>
        }>
          {(m) => (
            <Stack gap={4}>
              <Facts
                items={[
                  { label: "Site", value: m.siteId },
                  {
                    label: "Approved plan",
                    value: m.planId ? (
                      <Link to={`../plans/${encodeURIComponent(m.planId)}`}>{m.planId}</Link>
                    ) : (
                      "none"
                    ),
                  },
                  { label: "Approved at", value: m.approvedAt ? utcLabel(m.approvedAt) : "—" },
                  { label: "Assignments", value: String(m.assignments.length) },
                ]}
              />
              <DataTable
                rowKey={(a) => a.sku}
                rows={m.assignments}
                columns={[
                  { key: "sku", header: "SKU", render: (a) => a.sku },
                  { key: "slot", header: "Forward slot", render: (a) => a.slot },
                  { key: "abc", header: "ABC", render: (a) => a.abcClass },
                ]}
              />
            </Stack>
          )}
        </RequestView>
      </Card>
    </Stack>
  );
}
