---
name: architecture-review
description: Bounded-context boundary and ADR-compliance review of a change (expensive, post-integration): hexagonal direction, cross-context coupling, contradicted ADRs. Invoke explicitly: /architecture-review [range].
disable-model-invocation: true
argument-hint: "[git range]"
---

Perform a bounded-context boundary and ADR-compliance review of the
current changes (or `$ARGUMENTS` if given, e.g. a branch/PR diff range).

This is EXPENSIVE relative to `/code-review` — it reasons about
cross-repo/cross-context implications, not just this diff's local
correctness. Use it post-integration (before merging a PR that touches
architecture, not on every small commit) or when asked explicitly to
check a design decision against this fleet's standing architecture.

## What to check, in priority order

1. **Hexagonal dependency direction.** Domain depends on nothing;
   application depends on domain+ports; adapters depend on
   application+domain; only `cmd/` wires every layer together. Run
   `go test ./internal/architecture/... -v` first — if it's already red,
   report that and stop; don't hand-review what a fitness test already
   caught.
2. **Customer/Supplier direction, per `.claude/rules/domain-model.md`.**
   This service is downstream of order-management (demand), product-master
   (profiles) and facility-layout (slots). Check
   `docs/adr/0001-slotting-optimization-bounded-context.md` and
   `docs/adr/0003-local-copies-and-consumed-contracts.md` before assuming a
   new integration is fine. There is NO HTTP client to a sibling context:
   flag any outbound call as a new architectural decision that needs its
   own ADR, not a code change slipped in silently.
3. **Local-copy discipline (ADR 0003).** The consumers in
   `internal/adapters/inbound/kafka/` may only write the local copies
   (`demand_lines`, `product_profiles`, `zones`, `slots`) through the
   `Apply*` use cases in `internal/application/usecases/consumers.go`, and
   each must claim the CloudEvents id and upsert in ONE unit of work. A
   consumer that writes a `SlotPlan`, or skips the claim, is a blocking
   finding.
4. **Planner purity.** `internal/domain/planning` does no I/O and reads no
   clock; the `GeneratePlan` use case supplies the inputs. A second ranking
   policy needs an ADR (see
   `docs/adr/0002-slotplan-aggregate-and-abc-velocity-policy.md`).
5. **Kafka consumer-group pattern correctness.** A new Kafka consumer
   must use either (a) a named long-lived constant for a genuinely
   single-instance consumer, or (b) a per-process-unique generated group
   id for an event-sourced local-cache consumer that replays full
   history on every start. Flag any new consumer whose pattern doesn't
   match its actual replay behavior — this is a correctness bug, not a
   style issue (see this repo's `.claude/skills/how-to-add-an-integration-event/SKILL.md`
   for the two patterns and the incident that taught this fleet the
   difference).
6. **A new bounded-context integration with no companion documentation.**
   If this change introduces or changes a cross-repo contract (a new
   REST call, a new Kafka topic subscription, a new MCP tool consumed by
   a sibling), check whether an ADR documents the decision — and whether
   a companion ADR should exist in the OTHER repo too, per this fleet's
   companion-ADR convention (see `.claude/skills/how-to-write-an-adr/SKILL.md`).
7. **Auth-reintroduction and sibling-call bans**, same as `/code-review`
   items 6-8, but reasoned about more thoroughly here — check not just
   "is there a Bearer literal" but "does this change's INTENT require
   re-litigating the fleet-wide auth-removal decision," which would need
   its own ADR, not a silent code change.

## Output format

State clearly: PASS (no architectural concerns), CONCERNS (list them,
each tied to the specific rule/ADR it would violate), or NEEDS-ADR (the
change is architecturally sound but undocumented — name what the ADR
should cover). Cite the specific file/rule/ADR for every finding; a
finding with no citation is not actionable.

This is advisory. It never blocks a merge on its own, and it never
modifies files. If a finding conflicts with a decision explicitly stated
in this repo's own AGENTS.md/CLAUDE.md, defer to that document and say
so — this command reasons about the fleet's general conventions, not a
higher authority than the repo's own explicit guidance.
