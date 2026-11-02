# ADR 0014: Adopt the fleet REST API guideline

## Status

Accepted (2026-11-01). Amends the REST surface of
[ADR 0001](./0001-slotting-optimization-bounded-context.md) and the
idempotency statements of [ADR 0011](./0011-postgres-and-kafka-are-the-only-runtime.md).
Hard cutover: no aliases, no dual routes, no toggles.

## Context

The fleet REST API guideline (warehouse-docs, STANDARD.md) removes the
divergences found in the 2026-10-30 portfolio assessment: verb routes, an
unkeyed set of writes, two spellings of the replay header, key reuse answered
`422`, and unpaginated lists. This service had two verb routes, two unkeyed
POSTs and two collection GETs that did not meet the list envelope.

## Decision

- **Nouns, not verbs (R1, R2).** `POST /slot-plans/{planId}/approve` becomes
  `POST /slot-plans/{planId}/approvals`; `POST .../reject` becomes
  `POST .../rejections`. Status codes, bodies and `operationId`s are unchanged.
- **Every POST is idempotent (R3-R6, R8).** The middleware wraps `/slot-plans`,
  `.../approvals` and `.../rejections`. The key is global (the
  `idempotency_keys` primary key is the key alone; no migration needed). The
  fingerprint is method + path + SHA-256(body). Same key and fingerprint
  replays the stored answer with `Idempotency-Replayed: true`; a different
  fingerprint is `409 idempotency-key-reused` (was `422`); a missing key is
  `400 idempotency-key-required`; a key not 1-128 printable ASCII is
  `400 idempotency-key-invalid`. The key row commits in the same transaction as
  the aggregate and its outbox rows (unchanged design). There is no `PUT` that
  appends, so no `PUT` is keyed.
- **Pagination (R7).** `GET /slot-plans` keeps its keyset cursor;
  `GET /sku-velocity` gains `cursor` and `nextCursor`, keyset over
  (picks DESC, units DESC, sku ASC) using the fleet `page` package encoding.
  Collections answer `400 invalid-limit` / `invalid-cursor` /
  `unknown-query-parameter` (the retired `after`, `offset`, `from`,
  `nextToken` fail loudly).
- **Exemption.** `GET /forward-slots` is `x-pagination-exempt: view`: one
  composite resource, the site's single current approved map (plan id, approval
  time and assignments belong together and are read atomically).
- **Consumers.** The web remote (`web/src/api.ts`) uses the new routes and the
  velocity cursor; it already sent a fresh key per POST. No sibling service is
  called by this context (ADR 0003). Known callers elsewhere: e2e-tests
  (`slotting_test.go`), delivery-promise and last-mile-delivery
  (`nul_test.go`), changed in their own PRs.

## Consequences

Breaking: clients of the two renamed routes, of `422` reuse handling and of the
`sku-velocity` body (unchanged shape, plus `nextCursor`) must deploy with this
change. Keys stored before the cutover hashed the body only; replaying such an
old key is answered `409` once (retention is 24 h, so the window is short).
