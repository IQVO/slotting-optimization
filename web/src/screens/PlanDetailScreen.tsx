import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { Card, DataTable } from "@warehouse/ui-kit";
import { approveSlotPlan, getSlotPlan, rejectSlotPlan, toApiError } from "../api";
import type { ApiError } from "../api";
import { Form, FormRow, InlineError, InlineSuccess, SubmitButton, TextField } from "../components/formkit";
import { Facts, MoveKindPill, PageHeader, Stack, StatePill } from "../components/layout";
import { ProblemAlert, RequestView } from "../components/ProblemAlert";
import { useRequest } from "../hooks/useRequest";
import { movePath, reasonProblem, utcLabel, windowLabel } from "../lib";
import type { MoveExecution, MoveKind, SlotPlan } from "../types";

/** Reads :planId and keys the screen on it, so moving between plans never
 *  carries decision state over. */
export function PlanDetailRoute() {
  const { planId = "" } = useParams();
  return <PlanDetailScreen key={planId} planId={planId} />;
}

/**
 * One slot plan: the assignment map it proposes, the moves it would make with
 * how far the floor has executed them (ADR 0010), the SKUs it could not place,
 * its supersession info, and -- while it is a Draft -- the human decision:
 * approve (it becomes the site's forward-slot map, superseding the previous
 * Approved plan) or reject. Both POSTs carry an Idempotency-Key and ask for an
 * explicit confirmation first.
 */
export function PlanDetailScreen({ planId }: { planId: string }) {
  const plan = useRequest(() => getSlotPlan(planId), `plan|${planId}`);
  // The decision's outcome note lives HERE, not in DecisionCard: a successful
  // POST reloads the plan, the reloaded plan is no longer a Draft, and the
  // DecisionCard unmounts with it -- the note must survive that.
  const [decided, setDecided] = useState<"approved" | "rejected" | null>(null);
  return (
    <Stack gap={5}>
      <div>
        <Link to="../plans">← All slot plans</Link>
      </div>
      <RequestView state={plan} what="slot plan">
        {(p) => (
          <PlanBody
            plan={p}
            decided={decided}
            onDecided={(action) => {
              setDecided(action === "approve" ? "approved" : "rejected");
              plan.reload();
            }}
          />
        )}
      </RequestView>
    </Stack>
  );
}

function PlanBody({
  plan,
  decided,
  onDecided,
}: {
  plan: SlotPlan;
  decided: "approved" | "rejected" | null;
  onDecided: (action: "approve" | "reject") => void;
}) {
  return (
    <>
      <PageHeader title={plan.planId} subtitle="slotting-optimization · slot plan" />
      {decided === "approved" && (
        <InlineSuccess message="Plan approved. It is now the site's forward-slot map." />
      )}
      {decided === "rejected" && <InlineSuccess message="Plan rejected." />}
      <Card title="Plan">
        <Facts
          items={[
            { label: "State", value: <StatePill state={plan.state} /> },
            { label: "Site", value: plan.siteId },
            { label: "Policy", value: plan.policy },
            { label: "Demand window", value: windowLabel(plan.windowFrom, plan.windowTo) },
            { label: "Generated", value: utcLabel(plan.generatedAt) },
            ...(plan.approvedAt ? [{ label: "Approved", value: utcLabel(plan.approvedAt) }] : []),
            ...(plan.rejectedAt ? [{ label: "Rejected", value: utcLabel(plan.rejectedAt) }] : []),
            ...(plan.rejectReason ? [{ label: "Reject reason", value: plan.rejectReason }] : []),
            ...(plan.supersedesPlanId
              ? [{ label: "Supersedes", value: <Link to={`../plans/${encodeURIComponent(plan.supersedesPlanId)}`}>{plan.supersedesPlanId}</Link> }]
              : []),
            ...(plan.supersededAt ? [{ label: "Superseded", value: `${utcLabel(plan.supersededAt)} (a newer plan took the forward-slot map)` }] : []),
            { label: "Version", value: String(plan.version) },
          ]}
        />
      </Card>
      {plan.state === "Draft" && <DecisionCard plan={plan} onDecided={onDecided} />}
      <AssignmentsCard plan={plan} />
      <MovesCard plan={plan} />
      <UnassignedCard plan={plan} />
    </>
  );
}

/**
 * The approve/reject decision on a Draft plan. Each action is a two-step
 * flow -- the button arms an explicit confirmation panel, only the confirmed
 * button POSTs -- and each POST carries its own Idempotency-Key, so a retried
 * submit can never double-apply. A 409 (plan-not-draft,
 * approved-plan-conflict, concurrent-modification) renders as a problem
 * alert; after a successful decision the plan reloads.
 */
function DecisionCard({ plan, onDecided }: { plan: SlotPlan; onDecided: (action: "approve" | "reject") => void }) {
  const [armed, setArmed] = useState<"approve" | "reject" | null>(null);
  const [reason, setReason] = useState("");
  const [problem, setProblem] = useState<ApiError | null>(null);
  const [saving, setSaving] = useState(false);

  const reasonErr = reason === "" ? null : reasonProblem(reason);

  const decide = async (action: "approve" | "reject") => {
    setProblem(null);
    setSaving(true);
    try {
      if (action === "approve") {
        await approveSlotPlan(plan.planId);
      } else {
        await rejectSlotPlan(plan.planId, reason.trim() || undefined);
      }
      setArmed(null);
      onDecided(action);
    } catch (err) {
      setProblem(toApiError(err));
    } finally {
      setSaving(false);
    }
  };

  const lead =
    problem?.slug === "plan-not-draft"
      ? "This plan is no longer a Draft -- reload the list to see the latest state."
      : problem?.slug === "approved-plan-conflict"
        ? "Another plan of this site was approved first; re-fetch and review the new state."
        : undefined;

  return (
    <Card title="Decision">
      <Stack gap={4}>
        <Form
          label="Plan decision"
          onSubmit={() => {
            if (armed === null) return;
            if (armed === "reject" && reasonProblem(reason) !== null) return;
            void decide(armed);
          }}
        >
          {armed === null && (
            <FormRow>
              <SubmitButton type="button" onClick={() => setArmed("approve")}>
                Approve plan…
              </SubmitButton>
              <SubmitButton type="button" secondary danger onClick={() => setArmed("reject")}>
                Reject plan…
              </SubmitButton>
            </FormRow>
          )}
          {armed === "approve" && (
            <Stack gap={3}>
              <div style={{ fontSize: "var(--wh-font-size-sm)" }}>
                Approve this plan? It becomes <strong>{plan.siteId}</strong>&apos;s forward-slot map and publishes its
                assignments and moves; the site&apos;s current Approved plan, if any, is superseded in the same
                transaction.
              </div>
              <FormRow>
                {/* type="submit": the Form's onSubmit decides the armed action. */}
                <SubmitButton disabled={saving}>{saving ? "Approving…" : "Confirm approval"}</SubmitButton>
                <SubmitButton type="button" secondary disabled={saving} onClick={() => setArmed(null)}>
                  Cancel
                </SubmitButton>
              </FormRow>
            </Stack>
          )}
          {armed === "reject" && (
            <Stack gap={3}>
              <TextField
                label="Reason"
                value={reason}
                onChange={setReason}
                hint="Optional, at most 500 characters"
                placeholder="Why this plan was rejected"
              />
              <FormRow>
                {/* type="submit": the Form's onSubmit decides the armed action. */}
                <SubmitButton danger disabled={saving || reasonErr !== null}>
                  {saving ? "Rejecting…" : "Confirm rejection"}
                </SubmitButton>
                <SubmitButton type="button" secondary disabled={saving} onClick={() => setArmed(null)}>
                  Cancel
                </SubmitButton>
              </FormRow>
              <InlineError message={reasonErr} />
            </Stack>
          )}
          {problem && <ProblemAlert error={problem} lead={lead} />}
        </Form>
      </Stack>
    </Card>
  );
}

function AssignmentsCard({ plan }: { plan: SlotPlan }) {
  return (
    <Card title={`Assignments (${plan.assignments.length})`}>
      {plan.assignments.length === 0 ? (
        <div style={{ color: "var(--wh-color-text-muted)", fontSize: "var(--wh-font-size-sm)" }}>
          This plan assigns no SKUs (no demand, or nothing could be placed).
        </div>
      ) : (
        <DataTable
          rowKey={(a) => a.sku}
          rows={plan.assignments}
          columns={[
            { key: "sku", header: "SKU", render: (a) => a.sku },
            { key: "slot", header: "Forward slot", render: (a) => a.slot },
            { key: "abc", header: "ABC", render: (a) => a.abcClass },
            { key: "picks", header: "Picks", align: "right", render: (a) => String(a.picks) },
            { key: "units", header: "Units", align: "right", render: (a) => String(a.units) },
          ]}
        />
      )}
    </Card>
  );
}

/**
 * The plan's moves and -- from GET /slot-plans/{planId} -- each move's
 * execution status (ADR 0010): which moves the floor has already made, out of
 * the relocations inventory-storage recorded. Only Approved and Superseded
 * plans can gain executions; a Draft shows its moves without progress.
 */
function MovesCard({ plan }: { plan: SlotPlan }) {
  const execution = plan.execution;
  // execution.moves[i] is the plan's moves[i] (moveSeq order = SKU order), so
  // each row carries its own 0-based seq (which IS the move's moveSeq).
  type MoveRow = { seq: number; sku: string; fromSlot?: string; toSlot?: string; kind: MoveKind; execution?: MoveExecution };
  const rows: MoveRow[] = plan.moves.map((m, i) => ({
    seq: i,
    ...m,
    execution: execution?.moves.find((x) => x.moveSeq === i),
  }));
  // The execution columns are added as a plain array (not `as const`): a
  // readonly tuple breaks DataTable's column inference and the render
  // callbacks lose their row type.
  const executionColumns = execution
    ? [
        {
          key: "executed",
          header: "Executed",
          render: (m: MoveRow) =>
            m.execution?.executed ? (
              <span style={{ color: "var(--wh-color-status-success)", fontWeight: 600 }}>✓ yes</span>
            ) : (
              <span style={{ color: "var(--wh-color-text-muted)" }}>— not yet</span>
            ),
        },
        {
          key: "qty",
          header: "Units moved",
          align: "right" as const,
          render: (m: MoveRow) => (m.execution?.executed ? String(m.execution.executedQuantity) : "0"),
        },
        {
          key: "lastAt",
          header: "Last executed",
          render: (m: MoveRow) => (m.execution?.lastExecutedAt ? utcLabel(m.execution.lastExecutedAt) : "—"),
        },
      ]
    : [];

  return (
    <Card
      title={`Moves (${plan.moves.length})`}
      {...(execution
        ? {
            actions: (
              <div style={{ fontSize: "var(--wh-font-size-sm)", color: "var(--wh-color-text-muted)" }}>
                {execution.movesExecuted} of {execution.movesTotal} moves executed
                {execution.lastExecutedAt ? ` · last ${utcLabel(execution.lastExecutedAt)}` : ""}
              </div>
            ),
          }
        : {})}
    >
      {plan.moves.length === 0 ? (
        <div style={{ color: "var(--wh-color-text-muted)", fontSize: "var(--wh-font-size-sm)" }}>
          This plan makes no moves (the current map already matches).
        </div>
      ) : (
        <DataTable
          rowKey={(m) => `${m.sku}-${m.seq}`}
          rows={rows}
          columns={[
            { key: "seq", header: "#", align: "right", render: (m) => String(m.seq) },
            { key: "sku", header: "SKU", render: (m) => m.sku },
            { key: "kind", header: "Kind", render: (m) => <MoveKindPill kind={m.kind} /> },
            { key: "path", header: "From → to", render: (m) => movePath(m.fromSlot, m.toSlot) },
            ...executionColumns,
          ]}
        />
      )}
      {!execution && plan.moves.length > 0 && (
        <div style={{ color: "var(--wh-color-text-faint)", fontSize: "var(--wh-font-size-xs)", marginTop: 8 }}>
          Execution progress appears once the plan is Approved (wes-work-planning turns its moves into RELOCATE work;
          inventory-storage&apos;s StockRelocated comes back as progress here).
        </div>
      )}
    </Card>
  );
}

function UnassignedCard({ plan }: { plan: SlotPlan }) {
  if (plan.unassigned.length === 0) return null;
  return (
    <Card title={`Unassigned SKUs (${plan.unassigned.length})`}>
      <div style={{ color: "var(--wh-color-text-muted)", fontSize: "var(--wh-font-size-sm)", marginBottom: 8 }}>
        SKUs the planner could not give a forward slot, each with its reason.
      </div>
      <DataTable
        rowKey={(u) => u.sku}
        rows={plan.unassigned}
        columns={[
          { key: "sku", header: "SKU", render: (u) => u.sku },
          { key: "reason", header: "Reason", render: (u) => u.reason },
        ]}
      />
    </Card>
  );
}
