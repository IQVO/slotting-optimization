---
name: how-to-write-an-adr
description: Write an Architecture Decision Record in this repo's numbering and format, including companion ADRs for cross-repo changes. Use when a design decision should be recorded or a change contradicts an existing ADR.
---

# How to write an ADR

Use when a change is architecturally significant — a new bounded-context
integration, a reversal of a prior decision, a cross-repo contract change, a
second ranking policy, or anything a future reader would otherwise have to
reverse-engineer from the diff. Not every change needs one: a bug fix or a
routine feature inside an already-decided architecture doesn't.

## Numbering and location

`docs/adr/NNNN-kebab-case-title.md`, four-digit zero-padded, sequential. Check
the highest existing number (`ls docs/adr/`, or
`git ls-tree --name-only origin/develop -- docs/adr/`) and take the next
integer; never reuse or guess. Today they run from
`0001-slotting-optimization-bounded-context.md` to
`0004-cloudevents-envelope-and-type-catalogue.md`.

There is no docs site in this repo, so no frontmatter: an ADR is plain
Markdown that starts with the heading.

## Format: Michael Nygard's template, as the existing ADRs use it

```markdown
# ADR NNNN: title as a short noun phrase

## Status

Accepted (YYYY-MM-DD).      <!-- or Proposed | Superseded by ADR NNNN -->

## Context

The forces at play, in the past tense, as if explaining to someone who wasn't
there. State the alternatives seriously considered, not just the one chosen;
a reader six months from now needs to know a simpler option was weighed.

## Decision

What was decided, as an active present-tense declaration ("we will..."). Be
specific about the mechanism so a reader can implement it from scratch.

## Consequences

What becomes easier, what becomes harder, what future work this creates or
forecloses. Be honest about the downsides.
```

The `## Decision` section is the part worth the most editing effort. Model
examples: `docs/adr/0003-local-copies-and-consumed-contracts.md` (states the
exact mechanism — event-fed local copies instead of a synchronous lookup, the
mode and consumer-group switches, the live-data finding rule) and
`docs/adr/0002-slotplan-aggregate-and-abc-velocity-policy.md` (states the
invariants and the policy precisely enough to implement).

## Superseding an earlier ADR

Don't edit the old ADR's Decision section. Change its `## Status` to
`Superseded by ADR NNNN` (a one-line patch) and open the new ADR with
`Accepted (date). Supersedes ADR NNNN.` in its Status.

## Cross-repo decisions: use a companion ADR

When a decision spans two bounded-context repos, write ONE ADR per repo, each
referencing the other as "the companion ADR" with a one-line description of
the split of responsibility. This service's contracts with its neighbours are
recorded in `docs/adr/0001-slotting-optimization-bounded-context.md` (context
map) and `docs/adr/0003-local-copies-and-consumed-contracts.md`; a change to a
consumed contract needs the producer's ADR referenced there.

## After writing

Keep the contracts in step in the SAME PR: `apis/openapi.yaml` and
`apis/asyncapi.yaml` (then `spectral lint` both, as the `api-lint` CI job
does), `.claude/rules/*.md` where the rule text changes, and
`make guide-lint` so no guide cites a path the ADR moved.
