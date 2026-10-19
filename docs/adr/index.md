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
| [0004](/docs/adr/0004-cloudevents-envelope-and-type-catalogue) | CloudEvents envelope and type catalogue | Accepted (2026-10-08); per-key ordering amended by ADR 0008 |
| [0005](/docs/adr/0005-mcp-server-adoption) | MCP server adoption (read-only, unauthenticated, additive) | Accepted (2026-10-08) |
| [0006](/docs/adr/0006-analytics-read-side) | analytics read side: stream, projector and slot-plan funnel report | Accepted (2026-10-17) |
| [0007](/docs/adr/0007-executing-an-approved-slot-plan) | executing an approved slot plan (options, undecided) | Superseded by ADR 0010 (2026-10-14) |
| [0008](/docs/adr/0008-outbox-relay-attempt-cap) | outbox relay attempt cap | Accepted (2026-10-12) |
| [0009](/docs/adr/0009-trace-context-crosses-the-outbox) | the W3C trace context crosses the outbox to Kafka | Accepted (2026-10-12) |
| [0010](/docs/adr/0010-approved-slot-plans-are-executed-as-wes-relocation-work) | approved slot plans are executed as WES relocation work | Accepted (2026-10-14); `RELOCATION_MODE` default amended by ADR 0011 |
| [0011](/docs/adr/0011-postgres-and-kafka-are-the-only-runtime) | Postgres and Kafka are the only runtime | Accepted (2026-10-19); supersedes the in-memory / `permissive` / log-sink statements of ADR 0003, 0005, 0009 and 0010 |

Where the code on `develop` has moved past an ADR's wording (for example
ADR 0001's "consumer built in the service phase" status column, now built),
the code is authoritative and the documentation pages describe the code.
