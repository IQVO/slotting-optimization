# ADR 0016: Every problem type has a replay class

## Status

Accepted (2026-11-20). Amends the idempotency statements of
[ADR 0014](./0014-adopt-the-fleet-rest-api-guideline.md) (the fleet rule
`.claude/rules/fleet/idempotency-and-outbox.md`; the standard is
`docs/idempotency-replay-classes.md` in warehouse-harness-template). Decision
text of ADR 0014 is unchanged; a dated pointer was added there.
Addendum (2026-11-22): a 24h TTL and a bounded purge — the retention open
point below, fixed with the fleet pattern of order-sourcing #84 /
last-mile-delivery #86.

## Context

A request retried under the same `Idempotency-Key` was answered from the store
whenever a committed answer existed, a 4xx included (ADR 0014). A stored
refusal that a retry could pass pins a false answer for as long as the row
lives: a `409 concurrent-modification` replayed to a client that did the right
thing and retried, or a `409 approved-plan-conflict` replayed after the race's
winner was itself superseded. Classifying by status code cannot fix it (`409`
covers both a lost race and a terminal state), and "store only 2xx" would lose
a committed outcome (OUTCOME below). The fleet decision (owner, 2026-11-18)
gives every problem type exactly one replay class, declared on its catalogue
entry.

## Decision

Each problem type this service can answer declares one class on its catalogue
entry (`internal/adapters/inbound/http/errors.go`, the `class` field the
middleware reads; it holds no slug list and no status range beyond 2xx/5xx):

- **FINAL** — the answer is a function of the request alone (or of state that
  can never change back): stored and replayed.
- **OUTCOME** — the command COMMITTED state and then answered an error that
  must be remembered: stored, and its writes commit with the stored answer.
- **RETRYABLE** — nothing committed and the answer depends on state that can
  change: NOT stored; the whole transaction rolls back, key row included, and
  a retry under the same key executes again. A different body under a released
  key is a NEW request, not `409 idempotency-key-reused`.

A 2xx is stored; a 5xx and a panic are never stored; a replay carries
`Idempotency-Replayed: true`. A refusal that declares no class (or a misspelt
one) is released like RETRYABLE — the safe fallback: a released refusal costs
one more execution, a wrongly stored one pins a stale refusal forever (this
store keeps key rows indefinitely; there is no TTL — see Consequences).

### Class table

| Problem type | Status | Class |
|---|---|---|
| `idempotency-key-required` | 400 | FINAL |
| `idempotency-key-invalid` | 400 | FINAL |
| `idempotency-key-reused` | 409 | FINAL |
| `malformed-request` | 400 | FINAL |
| `invalid-plan-id` | 400 | FINAL |
| `invalid-site-id` | 400 | RETRYABLE |
| `invalid-lookback` | 400 | FINAL |
| `unknown-query-parameter` | 400 | FINAL |
| `invalid-limit` | 400 | FINAL |
| `invalid-cursor` | 400 | FINAL |
| `invalid-state` | 400 | FINAL |
| `reject-reason-too-long` | 400 | FINAL |
| `plan-not-found` | 404 | FINAL |
| `plan-not-draft` | 409 | FINAL |
| `approved-plan-conflict` | 409 | RETRYABLE |
| `concurrent-modification` | 409 | RETRYABLE |
| `internal-error` | 500 | RETRYABLE |
| `resource-not-found` | 404 | FINAL |
| `dependency-unavailable` | 503 | RETRYABLE |
| `invalid-report-range` | 400 | FINAL |
| `invalid-report-granularity` | 400 | FINAL |
| `report-store-error` | 500 | RETRYABLE |

Reasons for every non-obvious entry (code-cited):

- `idempotency-key-required` / `-invalid` / `-reused` FINAL: the middlewares'
  own answers, written outside the store; a function of the request alone.
  Nothing is ever stored under their key.
- `invalid-site-id` RETRYABLE: the slug is shared by two catalogue entries
  with different causes — `slotplan.ErrInvalidSiteID` (a request-only format
  check, `slotplan.NewSiteID`) and `usecases.ErrNoSite` ("no siteId given and
  no default site is configured", `usecases/writer.go:81`): the second depends
  on boot configuration a deploy can change, and a caller that omitted the
  site can send it on the retry. A slug that mixes request-only and
  state-dependent causes is RETRYABLE; splitting it is a recorded follow-up.
- `plan-not-found` FINAL: this service mints the plan ids
  (`plan-<uuid>`, `internal/adapters/outbound/ids/ids.go:17` — a caller cannot
  name an id before its create committed), nothing is ever deleted (no
  `DELETE FROM` anywhere in the Postgres adapter) and the slug is not shared
  with a referenced fact an event or another request may deliver later: it is
  answered only for the command's own target (`Plans.Get`,
  `usecases/decide_plan.go`, `usecases/queries.go`).
- `plan-not-draft` FINAL: only a Draft plan can be approved or rejected
  (`usecases/decide_plan.go:39`), and Approved, Rejected and Superseded plans
  never change (ADR 0002, CLAUDE.md rule 5): the refusal's cause can never
  clear by retrying, so the stored answer stays truthful. State-dependent but
  stable — the same reasoning as a version ceiling.
- `approved-plan-conflict` RETRYABLE: the approval lost the supersede of the
  site's Approved plan (`usecases/decide_plan.go:83-84` maps the previous
  plan's lost version race to `repository.ErrApprovedPlanConflict`); nothing
  of this request committed, and the retry supersedes whatever is Approved
  now and wins.
- `concurrent-modification` RETRYABLE: a lost optimistic-lock race
  (`postgres/slot_plan_repository.go:274` — the version-guarded UPDATE matched
  no row); nothing written, the retry re-reads and re-runs.
- `internal-error` / `report-store-error` / `dependency-unavailable`
  RETRYABLE: 5xx answers are never stored; the class is nominal.
- `resource-not-found` FINAL: the NUL-path 404 (`nul.go`), written BEFORE the
  idempotency middleware (`server.go`: `r.Use(rejectNUL)` before the
  route-scoped keyed group), so nothing is stored under any key; a path with
  a NUL can never name a resource.
- `unknown-query-parameter`, `invalid-limit`, `invalid-cursor`, `invalid-state`,
  `invalid-report-range`, `invalid-report-granularity` FINAL: the query string
  alone; all are GET-route answers, never stored.
- `malformed-request` FINAL: the request body/query alone (also the class the
  NUL query/body refusals take: they are written before the middleware).

### Why no problem type is OUTCOME

Every refusal of the two keyed commands is raised before the command's first
write: `GeneratePlan` validates `siteId`/`lookbackDays` before the unit of
work opens (`usecases/generate_plan.go`, `usecases/writer.go:resolveSite`,
`lookbackDays`); `ApprovePlan` raises `plan-not-draft` "before anything else
is touched" (`decide_plan.go:39`) and maps both race losses before its own
save; `RejectPlan` validates the reason in the domain, before `persist`. The
writes a use case makes before refusing (the superseded previous plan in
`supersedePrevious`) live in the SAME joined transaction and are rolled back
with it when the answer is not stored. No refusal commits state, so no entry
declares OUTCOME. The middleware still honours the class (a joined unit of
work and a released savepoint mean a 4xx's writes CAN commit with the key
row), and an integration probe proves the branch for future entries.

## Consequences

- A stored refusal is now always final for its request; a retryable refusal
  (`invalid-site-id`, `approved-plan-conflict`, `concurrent-modification`) is
  answered, released, and retried under the same key as a new execution —
  which is what a person retrying an approval after a race expects.
- Retention: nothing deletes expired `idempotency_keys` rows and no TTL exists
  (no delete at claim, no purge job, no age check on replay). The table grows
  without bound and a stored FINAL answer never expires. Recorded as an open
  point in the adopting PR; the fix follows the fleet purge shape
  (order-sourcing #84 / last-mile-delivery #86) in a separate PR.
- Clients that reused a key after a refusal on purpose (expecting the stored
  refusal back) now get their retry executed. No consumer in the checkout
  calls these routes with a deterministic key; the MCP and A2A agents are
  read-only (ADR 0015) and need nothing.
- Follow-up: split `invalid-site-id` into a request-only format refusal
  (FINAL) and a state/config-dependent no-default-site refusal (RETRYABLE);
  changes a wire slug, so it is its own PR.

## Evidence

`internal/adapters/inbound/http/replay_class_test.go` holds this table equal
to the catalogue and fails on any unclassified problem literal;
`idempotency_replay_classes_integration_test.go` (rendered from the harness
template) proves per class what the middleware does over a real Postgres;
`idempotency_replay_integration_test.go` drives the real router through both
RETRYABLE refusals of the approve route (the loser's key row is absent, its
retry executes, and the concurrent duplicate executes itself).

## Addendum (2026-11-22): a 24h TTL and a bounded purge

This resolves the retention open point of the Consequences above ("nothing
deletes expired `idempotency_keys` rows and no TTL exists") with the fleet
pattern already merged in order-sourcing (#84) and last-mile-delivery (#86).
**It is a behaviour change, the bigger one of the wave: until today a stored
answer replayed FOREVER; from this change a key is remembered for 24 hours.**

Decision (this store's Design-A shape: the middleware's own transaction,
`INSERT ... ON CONFLICT DO NOTHING`, the handler behind SAVEPOINT
`handler_run`):

- **One TTL constant**, `inboundhttp.IdempotencyTTL = 24h` (the fleet
  default), shared by every reader and writer of the window — the claim, the
  replay read and the purge — so they can never disagree about which rows
  are live.
- **The claim (expiry enforced at request time).** Before the INSERT, the
  request's own transaction deletes an expired row for the key
  (`created_at <= now() - TTL`); the INSERT then inserts and the request
  EXECUTES the work again under the same key. The DELETE takes the row lock,
  so of the requests racing on an expired key exactly one claims it; the
  losers block on the claimer's INSERT and then replay the fresh answer.
  First-writer-wins is unchanged INSIDE the window: a live row is never
  touched by the claim.
- **Age guard on the replay read.** The replay SELECT reads
  `created_at > now() - TTL` — the exact complement of the claim and the
  purge predicates — so a row past the TTL can never answer a replay between
  its expiry and the sweep. In the microscopic window where a row expires
  between the conflicting INSERT and the read, the request is failed (a 5xx
  is never stored) and the client's retry claims the expired key and
  executes.
- **Bounded background purge.** `PurgeExpiredIdempotencyKeys` deletes at
  most 500 rows per statement, `ORDER BY created_at LIMIT n FOR UPDATE SKIP
  LOCKED`, the age predicate re-checked on the DELETE itself, no advisory
  lock: a row a request holds (a claim or an outcome UPDATE in flight) is
  skipped, never waited for. `cmd/api` runs it at start and then hourly
  (`main_purge.go`); like the relay the worker only ever logs — a failed
  pass is retried by the next tick.

The replay-class premise, re-examined. `idempotency-key-reused` (FINAL)
rested on "this store keeps key rows indefinitely; there is no TTL". WITHIN
the 24h window that premise stands: the stored answer is immutable and the
conflict cannot clear by retrying, so FINAL is unchanged. PAST the window
the row is gone and the key is free — a retry is not a "cleared conflict"
but a first execution (the 409 itself is never stored: the middleware
answers it before any storage). No other class changes: a FINAL answer is
stored and replayed for 24h (previously forever), a RETRYABLE answer is
still never stored, a 5xx still releases the key, and OUTCOME (declared by
no problem type here) would store its answer for the same 24h.

Consequences:

- A caller must treat the key as unique per logical request for 24 hours (a
  UUID per submit: no practical collision). After 24h the same key and body
  EXECUTES again — its effects run a second time — and the same key with a
  different request is no longer a 409 but a new request. No consumer in the
  checkout calls these routes with a deterministic key; the MCP and A2A
  agents are read-only (ADR 0015) and need nothing.
- `idempotency_keys` stops growing: the hourly purge deletes every row the
  claim and the replay read already ignore, oldest first, bounded per
  statement. The runbook's manual `DELETE` recipe is retired.
- `apis/openapi.yaml` descriptions now state the 24-hour window
  (descriptions only; the spec-breaking gate is green); the REST reference
  under `docs/docs/api-reference/rest` regenerated.
- Tests: the red-first Postgres proof
  `TestAnExpiredKeyExecutesInsteadOfReplaying` (CI run 45020995846 red on the
  test-only commit), the purge proofs in
  `idempotency_purge_integration_test.go` (expired deleted / live kept, one
  batch per call, a held row skipped not waited for, requests racing the
  purge on an expired key stay correct), the window-complementarity unit
  tests in `idempotency_retention_test.go`, and the worker loop tests in
  `cmd/api/main_purge_test.go`.
