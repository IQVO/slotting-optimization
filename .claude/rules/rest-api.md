---
paths:
  - "internal/adapters/inbound/http/**"
  - "apis/openapi*.yaml"
  - "apis/openapi/**"
---

# REST API (inbound adapter)

Source of truth: `apis/openapi.yaml`. Keep this list in sync with it. Kong path
prefix `/api/slotting-optimization`.

- `POST /slot-plans`                 -> `GenerateSlotPlan` (201 Draft; body `{siteId?, lookbackDays?}` optional; `Idempotency-Key`)
- `GET  /slot-plans?limit=&cursor=&siteId=&state=` -> `ListSlotPlans` (newest first, cursor paging)
- `GET  /slot-plans/{planId}`        -> `GetSlotPlan`
- `POST /slot-plans/{planId}/approve` -> `ApprovePlan` (200; 409 `plan-not-draft`, 409 `approved-plan-conflict`)
- `POST /slot-plans/{planId}/reject`  -> `RejectPlan` (200; body `{reason?}`; 409 `plan-not-draft`)
- `GET  /forward-slots?siteId=`      -> `ListForwardSlots` (the current approved assignment map; empty without `planId` when none)
- `GET  /sku-velocity?limit=&windowDays=&siteId=` -> `ListSkuVelocity`
- `GET  /healthz`, `GET /readyz` (503 while draining), `GET /metrics`

## Conventions

- Errors: RFC 7807 `application/problem+json`,
  `type = https://errors.slotting-optimization.warehouse-systems.dev/<slug>`.
  One lookup table maps typed errors to (status, slug, title). Slugs:
  `malformed-request`, `invalid-plan-id`, `invalid-site-id`,
  `invalid-lookback`, `invalid-limit`, `invalid-cursor`, `invalid-state`,
  `reject-reason-too-long`, `idempotency-key-required`,
  `idempotency-key-reused`, `plan-not-found`, `plan-not-draft`,
  `approved-plan-conflict`, `concurrent-modification`, `internal-error`.
- Request bodies reject unknown fields (`DisallowUnknownFields`).
- `POST /slot-plans` creates a resource: the fleet `Idempotency-Key` middleware
  applies (missing key = 400 `idempotency-key-required`; same key + different
  body = 422 `idempotency-key-reused`). Approve and reject are state transitions
  on an existing resource and do not take a key: repeating one answers 409
  `plan-not-draft`.
- Lists use opaque cursors (`nextCursor` absent on the last page).
- Auth: none (fleet-wide revert 2026-09-11; `TestNoAuthMiddlewareReintroduced`).
- No outbound HTTP client and no MCP client: plans are computed from event-fed
  local copies (ADR 0003).
