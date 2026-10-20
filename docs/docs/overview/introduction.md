---
id: introduction
title: Introduction
sidebar_label: Introduction
sidebar_position: 1
---

# Slotting Optimization

`slotting-optimization` is the bounded context of the
[warehouse-systems](https://github.com/IQVO) fleet that decides **which SKUs
deserve a forward pick slot, and which slot each one gets**. It computes a
reviewable proposal (a *slot plan*), a human approves or rejects it, and an
approved plan is published as a CloudEvent carrying the full forward
assignment map and the physical moves it implies.

| Fact | Value | Where it comes from |
| --- | --- | --- |
| Subdomain class | **Supporting** | [Subdomain classification](/docs/ddd/subdomain-classification), ADR 0001 |
| Tier | **WMS** (CloudEvents subdomain segment `wms`) | `internal/adapters/kafka/cloudevents/cloudevents.go` (`Subdomain = "wms"`) |
| Aggregate | `SlotPlan` (package `internal/domain/slotplan`) | ADR 0002 |
| Policy | `abc-velocity-v1` (package `internal/domain/planning`) | ADR 0002 |
| Binaries | four, in one image: `cmd/api` (REST on `HTTP_ADDR`, default `:8080`, plus four Kafka consumers and the outbox relay as goroutines, all always on), `cmd/mcp` (read-only MCP server, `:8090`), `cmd/slotting-projector` (analytics writer, admin `:8091`) and `cmd/slotting-reports` (read-only reports, `:8092`) | `cmd/*/main.go`, [Architecture](/docs/overview/architecture) |
| Data stores | the OLTP Postgres database (`DATABASE_URL`, embedded migrations `0001`-`0002`) and a separate analytical Postgres database (`ANALYTICS_DATABASE_URL`, embedded migration `analytics/migrations/0001`); there is no in-memory mode (ADR 0011) | `internal/adapters/outbound/postgres`, `internal/adapters/outbound/analyticsstore` |
| Kafka | publishes `warehouse.slotting-optimization.events` (integration) and `warehouse.slotting-optimization.analytics` (analytics, ADR 0006); consumes `warehouse.order-management.events`, `warehouse.product-master.events`, `warehouse.facility.events`, `warehouse.inventory.events` (api) and `warehouse.slotting-optimization.analytics` (projector); the only runtime (ADR 0011) | ADR 0003, ADR 0004, ADR 0006, ADR 0010 |
| Auth | none (fleet-wide decision; `TestNoAuthMiddlewareReintroduced` keeps it out) | `internal/adapters/inbound/http/server.go` |
| MCP | `cmd/mcp`: four read-only tools (`get_slot_plan`, `list_slot_plans`, `get_forward_slots`, `get_sku_velocity`), Streamable HTTP, unauthenticated | ADR 0005, `internal/adapters/inbound/mcp/tools.go` |
| Web | `web/`: the `slotting_mfe` Module Federation remote (plan list and approval workbench, forward slots, SKU velocity) | `web/src/App.tsx` |

## What it owns

- **The SlotPlan.** A proposal for one site and one demand window
  (`[windowFrom, windowTo)`, default 28 days back from now): the assignments
  `{sku, slot, abcClass, picks, units}`, the moves against the site's
  currently Approved plan (`Assign`, `Relocate`, `Vacate`) and the SKUs it could
  not place with a reason (`NoPhysicalProfile`, `NoEligibleSlot`,
  `NoCapacityFit`).
- **The decision on it.** `Draft` -> `Approved` or `Rejected`. Approving a plan
  supersedes the site's previous Approved plan in the same transaction, so a
  site has at most one Approved plan (`Superseded` is the fourth state).
- **The published forward-slot map.** `SlotPlanApproved` carries every
  `{sku, slot}` of the plan and the moves, so a consumer needs no lookup.

It does **not** own demand, product facts or the slot layout. It keeps three
event-fed local copies of them (demand from `order-management`, product
classification and unit size from `product-master`, zones and slots from
`facility-layout`) and never calls a sibling at request time. It moves no
stock itself: wes-work-planning plans the moves of an Approved plan as
RELOCATE work, fulfillment-execution executes them and inventory-storage
records them; this context reads the resulting `StockRelocated` back as the
plan's execution progress (ADR 0010).

## Main capabilities

| Capability | Interface | Use case |
| --- | --- | --- |
| Generate a Draft plan for a site | `POST /slot-plans` (requires `Idempotency-Key`) | `GeneratePlan` |
| Approve a Draft (supersedes the previous Approved plan) | `POST /slot-plans/{planId}/approve` | `ApprovePlan` |
| Reject a Draft with an optional reason | `POST /slot-plans/{planId}/reject` | `RejectPlan` |
| Read plans | `GET /slot-plans`, `GET /slot-plans/{planId}` | `ListPlans`, `GetPlan` |
| Read the current forward-slot map of a site | `GET /forward-slots` | `ListForwardSlots` |
| Read SKU velocity (the planner's demand input) | `GET /sku-velocity` | `ListSkuVelocity` |
| Keep the demand, product and layout copies current | Kafka consumers (always on; `DEMAND_MODE`, `PRODUCT_MODE`, `LAYOUT_MODE` are `kafka`, the only value) | `ApplyDemandChanged`, `ApplyProductClassified`, `ApplyPhysicalProfile`, `ApplyZoneRegistered`, `ApplyLocationSlotRegistered`, `ApplyLocationSlotDecommissioned` |
| Record the execution progress of an Approved plan | Kafka consumer of `StockRelocated` (always on, `RELOCATION_MODE=kafka`) | `RecordRelocation` |
| Publish plan decisions | transactional outbox + relay (always on, `EVENT_PUBLISHER=kafka`): each event goes to the integration topic and, under the same CloudEvents id, to the analytics topic | every write use case |
| Read plans and velocity from an agent | MCP tools over the same database | `GetPlan`, `ListPlans`, `ListForwardSlots`, `ListSkuVelocity` |
| Report the slot-plan funnel (generated, approved, rejected, approval latency, supersessions per day) | `GET /reports/slot-plan-funnel`, `GET /reports/freshness` on `cmd/slotting-reports` | `internal/analytics/report` (not an OLTP use case) |

## Where to go next

| You want to | Read |
| --- | --- |
| See the binaries, layers and stores | [Architecture](/docs/overview/architecture) |
| Run it on your machine and call it | [Quickstart](/docs/overview/quickstart) |
| Configure it | [Configuration](/docs/operations/configuration) |
| Deploy and operate it | [Runbook](/docs/operations/runbook), [Observability](/docs/operations/observability), [Troubleshooting](/docs/operations/troubleshooting) |
| Run and extend the tests | [Testing](/docs/development/testing) |
| Know who it talks to | [Integration](/docs/ecosystem/integration) |
| Learn the model | [Use cases](/docs/ddd/use-cases), [Ubiquitous language](/docs/ddd/ubiquitous-language), [DDD artifacts](/docs/ddd/ddd-artifacts), [Subdomain classification](/docs/ddd/subdomain-classification) |
| Read the contracts | [API overview](/docs/api-reference), [Event catalogue](/docs/api-reference/events) |
| Read the decisions | [ADR index](/docs/adr) |

:::note[Study project]
Like the rest of the warehouse-systems fleet this repository is a personal
study project (DDD, hexagonal architecture, AI-agent harness engineering). It
is not production software.
:::
