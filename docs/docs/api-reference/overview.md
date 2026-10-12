---
id: overview
title: API overview
slug: /api-reference
sidebar_position: 1
---

# API overview

| Interface | Source of truth | Notes |
| --- | --- | --- |
| REST | `apis/openapi.yaml` (OpenAPI 3.0.3, version 1.0.0) | The pages under *REST* are generated from the spec with `npm run gen-api-docs`; do not edit them by hand. Servers: `http://localhost:8080` (local) and `http://localhost:8000/api/slotting-optimization` (kind cluster through Kong, once the service is wired into the cluster; it is not yet). |
| Events | `apis/asyncapi.yaml` (AsyncAPI 2.6.0, version 1.0.0) | Summarised on the [event catalogue](/docs/api-reference/events); every field on [Domain events](/docs/ddd/domain-events). |
| MCP | none | **Planned, not built** (ADR 0001 names MCP read tools as a later phase). |

## Endpoints

| Tag | Endpoint | Use case | Success |
| --- | --- | --- | --- |
| Slot plans | `POST /slot-plans` | `GeneratePlan` | `201`, `Location: /slot-plans/<planId>`, the Draft plan |
| Slot plans | `GET /slot-plans` | `ListPlans` | `200 {items, nextCursor?}`, newest first |
| Slot plans | `GET /slot-plans/{planId}` | `GetPlan` | `200`, the plan |
| Slot plans | `POST /slot-plans/{planId}/approve` | `ApprovePlan` | `200`, the Approved plan |
| Slot plans | `POST /slot-plans/{planId}/reject` | `RejectPlan` | `200`, the Rejected plan |
| Forward slots | `GET /forward-slots` | `ListForwardSlots` | `200 {siteId, planId?, approvedAt?, assignments}` |
| Velocity | `GET /sku-velocity` | `ListSkuVelocity` | `200 {siteId, windowFrom, windowTo, items}` |
| Health | `GET /healthz`, `GET /readyz` | | `200` / `503` ([Runbook](/docs/operations/runbook#one-deployment-one-port)) |

`GET /metrics` (Prometheus) is served by the binary but is not part of
`apis/openapi.yaml`.

## Conventions

- **Optional bodies.** `POST /slot-plans` takes `{siteId?, lookbackDays?}`
  and `POST /slot-plans/{planId}/reject` takes `{reason?}`; an empty body is
  valid. Unknown fields, trailing data and bodies over 64 KiB are
  `400 malformed-request`. JSON names are camelCase on REST and snake_case
  on the events.
- **Default site.** `siteId` may be omitted on generate, `/forward-slots` and
  `/sku-velocity` when `DEMAND_SITE_ID` or `DEFAULT_SITE_ID` is configured;
  otherwise it is `400 invalid-site-id`.
- **`Idempotency-Key`** is required on `POST /slot-plans` when the service
  runs on Postgres: missing is `400 idempotency-key-required`, the same key
  and body replays the stored response (4xx included), the same key with
  another body is `422 idempotency-key-reused`. A 5xx is never cached. The
  in-memory mode has no middleware and ignores the header. Approve and
  reject do not take it: repeating them is refused by the state machine
  (`409 plan-not-draft`).
- **Versions.** Every plan carries `version` (1 at generation, +1 per
  decision). There is no `ETag` or `If-Match`; concurrent writers are told
  apart by `409 concurrent-modification` and `409 approved-plan-conflict`.
- **Paging.** `GET /slot-plans` uses `limit` (1 to 500, default 100) and the
  opaque `cursor` (the previous page's `nextCursor`, base64url of the last
  plan id). Filters: `siteId`, `state` (`Draft`, `Approved`, `Rejected`,
  `Superseded`). `GET /sku-velocity` takes `limit` and `windowDays` (1 to
  365, default 28).
- **Errors** are RFC 7807 `application/problem+json` with
  `type = https://errors.slotting-optimization.warehouse-systems.dev/<slug>`
  from one table (`problemCatalogue` in
  `internal/adapters/inbound/http/errors.go`): `malformed-request`,
  `invalid-plan-id`, `invalid-site-id`, `invalid-lookback`, `invalid-limit`,
  `invalid-cursor`, `invalid-state`, `reject-reason-too-long`,
  `plan-not-found`, `plan-not-draft`, `approved-plan-conflict`,
  `concurrent-modification`, `idempotency-key-required`,
  `idempotency-key-reused` and `internal-error`. Causes and fixes are on
  [Troubleshooting](/docs/operations/troubleshooting#rest-problem-types).
- **Auth**: none. The fleet reverted REST and MCP auth on 2026-09-11, and
  `TestNoAuthMiddlewareReintroduced` fails CI if auth middleware returns.
- **CORS**: `GET` and `POST` from `CORS_ALLOWED_ORIGINS` (default
  `http://localhost:5173`), headers `Accept`, `Content-Type`,
  `Idempotency-Key`.
