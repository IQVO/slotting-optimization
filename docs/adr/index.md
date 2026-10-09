---
id: index
title: Architecture decision records
sidebar_label: ADR index
slug: /
---

# Architecture decision records

The decisions of `slotting-optimization`, in the order they were taken. The
ADR bodies are immutable; a changed decision gets a new ADR that supersedes
the old one. The *Status* column is copied from each ADR's own `## Status`
section.

| ADR | Title | Status |
| --- | --- | --- |
| [0001](/docs/adr/0001-slotting-optimization-bounded-context) | slotting-optimization as the owner of forward-pick slotting decisions | Accepted (2026-10-08) |
| [0002](/docs/adr/0002-slotplan-aggregate-and-abc-velocity-policy) | the SlotPlan aggregate, its invariants and the abc-velocity-v1 policy | Accepted (2026-10-08) |
| [0003](/docs/adr/0003-local-copies-and-consumed-contracts) | event-fed local copies and the consumed contracts | Accepted (2026-10-08) |
| [0004](/docs/adr/0004-cloudevents-envelope-and-type-catalogue) | CloudEvents envelope and type catalogue | Accepted (2026-10-08) |

Where the code on `develop` has moved past an ADR's wording (for example
ADR 0001's "consumer built in the service phase" status column, now built),
the code is authoritative and the documentation pages describe the code.
