# ADR 0011: Postgres and Kafka are the only runtime

## Status

Accepted (2026-10-19). Supersedes the in-memory, `permissive` and log-sink
statements of [ADR 0003](./0003-local-copies-and-consumed-contracts.md),
[ADR 0005](./0005-mcp-server-adoption.md) and
[ADR 0010](./0010-approved-slot-plans-are-executed-as-wes-relocation-work.md)
(each carries a one-line pointer here; their bodies are history). Additive on
the wire: no event, REST path or migration changes. `GET /readyz` gains a 503
`dependency-unavailable` problem.

## Context

The user's rule for the whole fleet is "everything must use real database and
broker". Until this ADR the shipped binaries of this context could boot into a
state that looked healthy and was not:

- `cmd/api` without `DATABASE_URL` logged `using in-memory adapters` and served
  from `internal/adapters/outbound/memory`: no migrations, no `Idempotency-Key`
  middleware, nothing survived a restart.
- `cmd/mcp` did the same for its read repositories.
- `EVENT_PUBLISHER` defaulted to `log`: the relay marked outbox rows published
  after writing them to the log, so a misconfigured deployment lost every event
  silently. `KAFKA_BROKERS` was optional.
- `DEMAND_MODE`, `PRODUCT_MODE`, `LAYOUT_MODE` and `RELOCATION_MODE` defaulted
  to `permissive` (consumer not started); a missing group was an error only in
  `kafka` mode, and `kafka` over the in-memory adapters only logged a warning.
- `/readyz` reported only the shutdown gate, so a pod stayed Ready with Postgres
  or Kafka gone, and it stayed alive with a dead relay or consumer.
- The chart mirrored all of it (`eventPublisher: log`, `*Mode: permissive`,
  `kafka.enabled: false` as the default).

`cmd/slotting-projector` and `cmd/slotting-reports` already refused to boot
without `ANALYTICS_DATABASE_URL` (and, for the projector, `KAFKA_BROKERS`) and
never imported the memory package: they needed no runtime change.

## Decision

Postgres and Kafka are the only runtime of every shipped binary. The memory
adapters stay in `internal/adapters/outbound/memory` as test doubles for unit
and BDD tests.

1. **Mandatory configuration, a boot error that names the variable**, checked
   before anything is opened or the listener binds (`loadRuntime` in
   `cmd/api`): `DATABASE_URL`, `KAFKA_BROKERS` and the four consumer groups
   `DEMAND_CONSUMER_GROUP`, `PRODUCT_CONSUMER_GROUP`, `LAYOUT_CONSUMER_GROUP`,
   `RELOCATION_CONSUMER_GROUP`. `cmd/mcp` requires `DATABASE_URL`.
2. **The `*_MODE` names stay, the only value is `kafka`** (the default). The
   names are kept because docs and warehouse-infra set them. `permissive` and
   `off` exit with `<NAME>=permissive is not supported: this context runs on
   real Kafka`; any other value exits with `<NAME>: unknown mode "x" (want
   kafka)`. All four consumers always start.
3. **`EVENT_PUBLISHER` stays, the only value is `kafka`** (the default),
   because warehouse-infra sets it. `log` exits with `EVENT_PUBLISHER=log is
   not supported: this context publishes to real Kafka`. The relay is always
   on; `OUTBOX_MAX_ATTEMPTS` (default 20) is unchanged.
4. **`/readyz` is real**: the drain gate AND a Postgres ping AND a Kafka broker
   answering a metadata request with an empty, non-nil topic list (topics are
   never required: the relay writer sets `AllowAutoTopicCreation: true`). The
   verdict is cached for 3 s and bounded at 2 s. A failure is a 503 problem
   `dependency-unavailable` whose detail names `postgres` and/or `kafka`.
   `/healthz` stays liveness only.
5. **The process exits non-zero when the relay or a consumer returns on its
   own** (`<worker> stopped unexpectedly: exiting so the orchestrator restarts
   the service`), instead of staying alive with a dead worker.
6. **NUL guard.** Postgres text cannot hold U+0000; a NUL in the path is a 404
   `resource-not-found`, in the body or query a 400 `malformed-request`,
   refused before any idempotency key is recorded (the in-memory boot hid this
   defect; it was found by facility-layout's real-Postgres contract run).
7. **Sensor** `TestRuntimeBinaryHasNoInMemoryAdapters` (and its bites test):
   no non-test file under `cmd/**` imports `internal/adapters/outbound/memory`.
8. **Chart.** `database.*`, `kafka.brokers` and the four `config.*ConsumerGroup`
   are required (the render fails naming the value). `kafka.enabled` is
   accepted only as `true` (warehouse-infra still sets it) and never as `false`;
   a publisher other than `kafka`, a mode other than `kafka`, `EVENT_PUBLISHER`
   or any `*_MODE` in `extraEnv`, and `terminationGracePeriodSeconds` under 20
   are refused. `charts/slotting-optimization/tests/warehouse-infra-*.yaml` are
   verbatim copies of the warehouse-infra values and `render_test.sh` asserts
   every mandatory env is provided by them (it is: no infra change is needed).
9. **Proof.** `TestRunOnRealPostgresAndKafka` boots the real `run()` against
   testcontainers Postgres and Kafka and drives it only through REST and Kafka.
   `docker-compose.yml` starts the same two dependencies for local runs.

## Consequences

- A misconfigured deployment now fails at boot (or at chart render) naming the
  variable, instead of running degraded. A Kafka outage turns the pod unready
  and, if a worker dies, restarts it; the outbox and the committed offsets make
  that safe.
- Local runs need Docker (`docker compose up -d --wait`). Unit and BDD tests
  are unchanged: they build the memory doubles directly.
- Observed, not fixed: the first relay write to an output topic that does not
  exist yet fails once with `[3] Unknown Topic Or Partition` and succeeds on the
  next pass (the broker auto-creates the topic; the outbox row ends with
  `attempts = 2`). inbound-receiving (its ADR 0011) also reports that a kafka-go
  group consumer that starts before its topic exists may not pick the topic up
  when it appears until the next restart; that was not measured for this
  service.
- `cmd/slotting-projector` and `cmd/slotting-reports` are unchanged (already
  real-only).

## Out of scope

- `*_MODE=permissive|http` of REST sync edges to sibling contexts (this context
  has none today) and anything that is not the database or the broker.
- A Schemathesis contract job: this repository has none (`ci.yml` and the
  `Makefile` have no contract, Schemathesis or compose-boot job), so none was
  moved or invented.
- Creating topics: infra owns them; the service still relies on broker
  auto-creation.
