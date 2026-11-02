# ADR 0009: The W3C trace context crosses the outbox to Kafka

## Status

Amended by [ADR 0011](./0011-postgres-and-kafka-are-the-only-runtime.md): no binary runs the in-memory outbox any more (it stays a test double).

Accepted (2026-10-12, ecosystem gap fix D27). Additive: no migration (the
existing `outbox_events.headers jsonb` column carries it), optional Kafka
headers only; payloads and the CloudEvents envelope
([ADR 0004](./0004-cloudevents-envelope-and-type-catalogue.md)) are unchanged.

## Context

Every write of this context (`POST /slot-plans`, approve, reject) runs
inside an HTTP request span (otelchi on the api router) and leaves through
the transactional outbox: the use case inserts the encoded events into
`outbox_events` in its transaction, and the relay (`cmd/api`,
[ADR 0008](./0008-outbox-relay-attempt-cap.md)) later publishes them from a
background goroutine with no span of its own. The outbox stored only the
CloudEvents `content-type` header, so the trace stopped at the database: a
consumer of `warehouse.slotting-optimization.events` could never link its
work to the request that generated, approved or rejected the plan.

The fleet decision (gap-fix wave 2026-10-12, D27) is trace continuity across
the outbox, using the storage the fleet's reference implementation already
uses: the trace context lives in the outbox row's `headers jsonb` column,
injected with `otel.GetTextMapPropagator().Inject` at publish time, and the
relay sets the stored headers as Kafka message headers.

## Decision

1. **Capture at insert.** `postgres.OutboxRepo.Insert` (the publish point:
   it receives the use case's context, which carries the request span)
   injects the trace context with the global propagator that
   `telemetry.Setup` installs (W3C trace-context + baggage) into a map
   carrier and appends `traceparent`, and `tracestate` / `baggage` when
   present, to each row's headers, after the encoder's `content-type`. A key
   the encoder already set is never overwritten. Without an active span the
   propagator injects nothing and the row keeps only `content-type`.
2. **No schema change.** `outbox_events.headers` already exists
   (`jsonb NOT NULL DEFAULT '[]'`, migration 0001) and the relay already
   copies every stored header onto the Kafka message
   (`kafka.RelaySink.Send`). Rows written before this change relay exactly as
   before.
3. **Consumers.** This service's consumers (the demand, product and layout
   local copies, ADR 0003) open no spans, so they do not extract; when one
   starts a span it must call `Extract` on the message headers first. The
   contract (`apis/asyncapi.yaml`) documents the headers as optional:
   consumers may use them and must not require them.
4. **In-memory outbox.** `memory.OutboxRepo` (no `DATABASE_URL`, local and
   unit tests only) does not inject; it is never used with a real broker in
   the cluster.

## Consequences

- A consumer that extracts the headers parents its span on the producing
  request's span: one trace from `POST /slot-plans/{planId}/approvals` to the
  consumer's handling of `SlotPlanApproved`.
- Each outbox row grows by one ~55-byte header (more with `tracestate` or
  baggage).
- Baggage set by a caller is forwarded to Kafka consumers; this service sets
  none and the HTTP edge has no auth, so nothing sensitive is expected there.
- Tests (real Postgres and Kafka via testcontainers, in
  `internal/adapters/outbound/outbox/outbox_trace_integration_test.go`):
  `TestOutboxTrace_TraceparentCrossesTheOutboxToKafka` (the request span's
  `traceparent` is stored in the row after `content-type`, sent by a relay
  pass that runs on a background context, and extracts to the request's
  trace and span) and
  `TestOutboxTrace_RowWithoutTraceHeadersRelaysUnchanged` (a row without
  stored headers relays with its key and value unchanged and no headers).
