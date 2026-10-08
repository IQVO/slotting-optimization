---
name: how-to-add-an-integration-event
description: Publish or consume a cross-service Kafka event: CloudEvents 1.0 type naming, AsyncAPI, transactional outbox, consumer-group rules. Use when touching internal/adapters kafka or outbox code, a publisher/consumer, or apis/asyncapi*.yaml.
---

# How to add an integration event (publish and consume)

Use when asked to publish a new cross-context integration event, or consume
one from a sibling bounded context. This fleet's Kafka is ONE broker
platform-wide — every design decision below exists because that shared-broker
reality has already caused a real incident once.

## Publishing a new integration event

### 1. Is it actually cross-service?

This service publishes three events on `warehouse.slotting-optimization.events`
(`SlotPlanGenerated`, `SlotPlanApproved`, `SlotPlanRejected`; the catalogue is
`docs/adr/0004-cloudevents-envelope-and-type-catalogue.md`). Before adding
one, confirm a sibling context genuinely needs to react to it, and add it to
ADR 0004 first. The context map is
`docs/adr/0001-slotting-optimization-bounded-context.md`.

### 2. Envelope: CloudEvents 1.0, structured mode — MANDATORY

Every message is a CloudEvents 1.0 JSON document in structured content mode
(Kafka value = `application/cloudevents+json`), with the Kafka header
`content-type: application/cloudevents+json; charset=UTF-8`. There is no other
envelope in this fleet — no flat `event_id`/`event_type`/`occurred_at` shape,
no dual-write, no envelope toggle (see `.claude/rules/integration-events.md`;
`TestCloudEventsOnly` in `internal/architecture/events_fitness_test.go` and
`TestNoEventEnvelopeToggleOrFlatEnvelope` in `fitness_test.go` enforce it).

```json
{
  "specversion": "1.0",
  "id": "<uuid v4, minted once, persisted with the outbox row>",
  "source": "/warehouse/slotting-optimization",
  "type": "com.warehouse.wms.slotting-optimization.slotplan.<EventName>",
  "subject": "<plan id>",
  "time": "<domain occurred-at, RFC3339 UTC>",
  "datacontenttype": "application/json",
  "dataschema": "urn:warehouse:slotting-optimization:events:<EventName>:v1",
  "data": { "the": "actual payload, business types only" }
}
```

`type` follows the platform-wide reverse-DNS convention
`com.warehouse.<subdomain>.<bounded-context>.<entity>.<EventName>`; for this
service the entity segment is `slotplan`. A breaking payload change is a new
`.v2` type, never a mutation.

### 3. Implementation

1. Add the event to `internal/domain/slotplan/events.go` (the aggregate raises
   it; publishing wires an EXISTING domain event onto Kafka, it does not
   invent a payload shape at the adapter).
2. Encode it in `internal/adapters/outbound/kafka/encoder.go`: a payload
   struct (the wire shape from `apis/asyncapi.yaml`: snake_case fields such as
   `plan_id`, `to_slot`), a case in `payloadFor`, and the build through
   `internal/adapters/kafka/cloudevents` (`cloudevents.New(Spec{...})`) only.
   Nothing else hand-builds an envelope.
3. The key is the PLAN ID (`Subject` = plan id), so one plan's events stay
   ordered on one partition; the relay (`internal/adapters/outbound/outbox`,
   sink `kafka/relay_sink.go`) uses the `kafkago.Hash{}` balancer.
4. The event leaves through the transactional outbox only: the use case calls
   the `Writer` inside its `ports.UnitOfWork`, so the plan row and the encoded
   messages commit together. Never write to Kafka from a handler or use case.

### 4. Contract

Add the message to `apis/asyncapi.yaml` (the shared CloudEvents envelope
schema with every attribute required, the exact `type` const and
`dataschema`), then
`spectral lint apis/asyncapi.yaml --ruleset .spectral.asyncapi.yaml --fail-severity=warn`
(the `api-lint` CI job). `TestEventCatalogueMatchesContract` in
`internal/architecture/catalogue_fitness_test.go` fails when the type
catalogue, the encoder and the AsyncAPI disagree.

### 5. Test

Add a case to `TestEncoder_GoldenWireFormat` in
`internal/adapters/outbound/kafka/encoder_test.go` (golden exact JSON) asserting every CloudEvents attribute, the
`type` string and the `content-type` header (never a real broker in a unit
test). Real delivery is asserted in
`internal/adapters/outbound/outbox/relay_integration_test.go`
(`TestRelay_RealPostgresAndKafka_PublishesCloudEventsKeyedByPlanID`), which
MUST use testcontainers (`TestKafkaIntegrationTestsUseTestcontainers` in
`internal/architecture/fitness_test.go`: a skip-gated `KAFKA_BROKERS` test or a
hardcoded `localhost:9092` fails CI).

## Consuming an integration event from a sibling context

### 1. Never import the sibling's Go packages

This service knows a sibling's topic name, its exact CloudEvents `type`
strings and payload shape ONLY — never its Go types (ADR 0003 lists the three
consumed contracts). Hand-mirror the payload struct locally, as
`internal/adapters/inbound/kafka/demand_consumer.go` does with
`siteSkuDemandChangedData`; do not add a Go module dependency on the sibling
repo, and restate the contract in `apis/asyncapi.yaml`.

### 2. Decode CloudEvents only, dispatch on the full `type`

`Consumer.HandleMessage` in `internal/adapters/inbound/kafka/consumer.go` is
the pattern: `cloudevents.Decode` first (not a CloudEvent → WARN and commit
past, never crash, never block the partition), then look the FULL `type` up in
the `Handlers` map (an unknown type is ignored, not an error), decode `data`
into the local mirror struct (`decodeData`; a malformed payload wraps
`errBadPayload` → WARN and skip), and call an `Apply*` use case from
`internal/application/usecases/consumers.go`.

Never parse a legacy flat shape as a fallback, never dispatch on a short name
or suffix. `consumers_test.go` proves a legacy flat envelope is skipped
without opening a unit of work.

### 3. One transaction, offset after success, DLQ after the bound

Each `Apply*` use case claims the CloudEvents id (`ports.ProcessedEvents`)
and upserts in ONE unit of work (`Intake.apply`); a redelivery finds the claim
and does nothing. The offset is committed only after `HandleMessage` returned
nil (`FetchMessage` + `CommitMessages` in `consumeLoop`, `kafka.go`). A transient error
retries the SAME message with capped backoff and, after
`domainMaxHandlerAttempts` (5), publishes it to `<topic>.dlq` with `x-dlq-*`
headers (`deadletter.go`) so a stuck message cannot wedge the partition.
Stale messages are dropped by `version` (`ApplyProductClassified`,
`ApplyPhysicalProfile`).

### 4. Consumer groups and modes come from the environment

Each consumer has a mode env (`DEMAND_MODE`, `PRODUCT_MODE`, `LAYOUT_MODE`:
`kafka` or `permissive`, default `permissive` = not started) and a STABLE
group id from `DEMAND_CONSUMER_GROUP`, `PRODUCT_CONSUMER_GROUP`,
`LAYOUT_CONSUMER_GROUP` — never a string literal (`cmd/api/main.go`
`planConsumers`; mode `kafka` without its group is a boot error;
`TestKafkaConsumerGroupNeverHardcodedInline` enforces it). These consumers
feed a durable Postgres copy and resume from their committed offset, so a
fixed group is the right pattern. Only a consumer that rebuilds an in-memory
read model from the full topic on every start needs a per-process-unique
group id (hostname+PID+timestamp), because a new process joining a group an
earlier instance already consumed resumes from that instance's offset and
silently replays nothing. Never hardcode the id: a local process joining the
live Deployment's group starves one of the two (the real incident behind the
rule).

### 5. Test

Unit: `internal/adapters/inbound/kafka/consumers_test.go` and
`run_loop_test.go` (scripted `fakeReader`, commit order, retry, DLQ). End to
end against real Kafka and Postgres:
`consumers_integration_test.go` (testcontainers via
`internal/testing/kafkatest` and `pgtest`).

## Verify before opening the PR

```bash
make check-all     # includes arch-test: catches a sibling-package import
make integration   # real Kafka/Postgres via testcontainers (needs Docker)
```
