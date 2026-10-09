---
id: subdomain-classification
title: Subdomain classification
sidebar_label: Subdomain classification
sidebar_position: 3
---

# Subdomain classification

**`slotting-optimization` is a Supporting subdomain in the WMS tier**
(CloudEvents subdomain segment `wms`). The verdict is ADR 0001's
("Strategic classification") and matches the fleet table in warehouse-docs
(`docs/strategic-design/subdomain-classification.md` on `origin/main`,
decided 2026-10-08).

## Why Supporting

| Test | Answer | Evidence in this repository |
| --- | --- | --- |
| Does it differentiate the business? | Not on its own. Good slotting lowers pick travel and labor, but the competitive core of the fleet stays the WES conductor (`wes-work-planning`, `fulfillment-execution`). | ADR 0001 |
| Is it specific to this warehouse? | Yes. Which SKUs are candidates, how they are classed, which slots are eligible and how slots are ranked are this operation's policy, so an off-the-shelf rule set does not fit as is. That rules out Generic. | `internal/domain/planning/planner.go` (`DefaultParameters`, `compatibleWith`, `holds`) |
| Is the logic replaceable? | Yes, by design. The v1 policy `abc-velocity-v1` is deterministic, the ranking is a port (`SlotRankingPolicy`, v1 `LexicalRanking`), and a travel-distance policy can replace it without touching the aggregate. | `planning.SlotRankingPolicy`, ADR 0002 |
| How big is the model? | One aggregate (`SlotPlan`), one pure domain service (`Planner`), three event-fed local copies. | `internal/domain/slotplan`, `internal/domain/planning` |
| Does a human stay in the loop? | Yes: a plan is only a proposal until a person approves it, and nothing moves on the floor yet. | `ApprovePlan`, ADR 0002 |

Business model: **cost reduction** (pick travel and labor). Evolution:
**custom-built, genesis**. A [Core domain chart](/docs/ddd/core-domain-chart)
places it on the two axes.

## The neighbours

Copied from the fleet table (warehouse-docs, `origin/main`), never from
memory:

| Context | Classification | CloudEvents subdomain | Relation to this context |
| --- | --- | --- | --- |
| `order-management` | Generic/Supporting | `wes` | upstream: demand (`SiteSkuDemandChanged`) |
| `product-master` | Supporting | `wms` | upstream: classification and physical profile |
| `facility-layout` | Generic | `wms` | upstream: zones and location slots |
| `inventory-storage` | Core | `wms` | no edge (ADR 0001: an approved plan moves no stock) |
| `inbound-receiving` | Supporting | `wms` | no edge |
| `warehouse-planning` | Core | `wes` | no edge |
| `process-path-management` | Generic | `wes` | planned downstream for executing the moves (not built) |
| `wes-work-planning` | Core | `wes` | planned downstream (not built) |
| `fulfillment-execution` | Core | `wes` | planned downstream (not built) |
| `warehouse-ops-agent` | Supporting | `wes` | planned REST/MCP reader (not built) |

## Tier is not classification

The `wms` segment says *which tier* the context belongs to ("what and
where"), not how much it differentiates. In the same `wms` tier,
`inventory-storage` is Core, `product-master`, `inbound-receiving` and this
context are Supporting, and `facility-layout` is Generic.

## What would move it

- **Towards Core**: a slotting policy that measurably beats the industry's
  (travel-distance or affinity ranking, seasonal re-slotting) and that the
  WES depends on for throughput. ADR 0001 lists these as out of scope for v1.
- **Towards Generic**: replacing the planner with a bought slotting engine
  behind an anti-corruption layer, keeping only the approval workflow.
