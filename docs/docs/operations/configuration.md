---
id: configuration
title: Configuration
sidebar_label: Configuration
sidebar_position: 1
---

# Configuration

`slotting-optimization` ships five binaries, `cmd/api`, `cmd/mcp`, `cmd/a2a`,
`cmd/slotting-projector` and `cmd/slotting-reports`, and all five are
configured only through environment variables (one table per binary below). There is no config
file and no flag. Every variable below is read in `cmd/api/main.go` unless
the *Source* column says otherwise, and the list is complete: it comes from
every `os.Getenv` / `getenv` call in `cmd/` and `internal/` outside
`_test.go` files.

In the Helm chart every `cmd/api` variable has a `config.*` (or `kafka.*`,
`otel.*`, `database.*`, `environment`) value, is rendered as explicit `env`
on the api Deployment and is also in the chart ConfigMap, which the api
Deployment mounts with `envFrom` (explicit `env` wins for the same key).
`charts/slotting-optimization/tests/render_test.sh` (CI job `chart-tests`)
reads the `os.Getenv` / `getenv` literals of `cmd/api/main.go`,
`cmd/mcp/main.go`, `cmd/slotting-projector/main.go` and
`cmd/slotting-reports/main.go` and fails when the matching Deployment does
not render one of them, and when the ConfigMap has a key no binary reads.

## `cmd/api`

### Server, logging and telemetry

| Variable | Default | Required | Meaning | Source |
| --- | --- | --- | --- | --- |
| `HTTP_ADDR` | `:8080` | no | Listen address of the REST API, `/healthz`, `/readyz` and `/metrics`. `ReadHeaderTimeout` is fixed at 5 s. | `cmd/api/main.go` (`run`) |
| `LOG_LEVEL` | `info` | no | `debug`, `info`, `warn` (or `warning`, accepted like `warn` fleet-wide) or `error` (case-insensitive). Any other value falls back to `info` silently. `cmd/mcp` parses it the same way. Logs are JSON lines on stdout (`log/slog`). | `cmd/api/main.go` (`newLogger`) |
| `SERVICE_VERSION` | `dev` | no | OTel resource attribute `service.version`. | `cmd/api/main.go` (`setupTelemetry`) |
| `ENVIRONMENT` | `local` | no | OTel resource attribute `deployment.environment.name`. | `internal/adapters/outbound/telemetry/telemetry.go` (`Environment`) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | no | OTLP/gRPC endpoint (host:port, insecure) for traces and metrics. The exporters connect lazily: no Collector means telemetry is dropped, never a failed boot. | `cmd/api/main.go`, `telemetry.DefaultOTLPEndpoint` |
| `CORS_ALLOWED_ORIGINS` | `http://localhost:5173` | no | Comma-separated origins allowed by the CORS middleware. Methods `GET` and `POST`; headers `Accept`, `Content-Type`, `Idempotency-Key`; no credentials. | `internal/adapters/inbound/http/server.go` (`corsAllowedOrigins`) |
| `SHUTDOWN_DRAIN_DELAY` | `5s` | no | How long the process waits after flipping `/readyz` to 503 before it stops accepting connections. Go duration; `0` disables the wait; a negative or unparsable value logs `ignoring invalid SHUTDOWN_DRAIN_DELAY` and uses 5 s. | `cmd/api/main.go` (`shutdownDrainDelay`) |

### Database

| Variable | Default | Required | Meaning | Source |
| --- | --- | --- | --- | --- |
| `DATABASE_URL` | none | **yes** | Postgres DSN of the pgx pool. There is no in-memory mode ([ADR 0011](/docs/adr/0011-postgres-and-kafka-are-the-only-runtime)): unset, the process exits at boot with `DATABASE_URL is required: this context runs on real Postgres, there is no in-memory mode`, before the listener binds. Set, the process runs the embedded migrations, opens a pool (`MaxConns = 10`, `statement_timeout = 5s`) and pings it; failure after the retry budget exits the process. | `cmd/api/main.go` (`loadRuntime`, `buildAdapters`), `internal/adapters/outbound/postgres/pool.go` |
| `MIGRATIONS_DATABASE_URL` | value of `DATABASE_URL` | no | DSN used **only** for the boot migrations. Point it at a direct (non-PgBouncer) connection: golang-migrate takes a Postgres advisory lock that does not survive transaction pooling. | `cmd/api/main.go` (`migrationsDatabaseURL`) |

### Planning

| Variable | Default | Required | Meaning | Source |
| --- | --- | --- | --- | --- |
| `DEMAND_SITE_ID` | unset | no | Two effects. (1) It is the default site of `POST /slot-plans`, `GET /forward-slots` and `GET /sku-velocity` when the request names none. (2) The demand consumer **drops** `SiteSkuDemandChanged` lines of every other site (outcome `ignored`). | `cmd/api/main.go` (`configFromEnv`), `usecases.ApplyDemandChanged.OnlySite` |
| `DEFAULT_SITE_ID` | unset | no | Default site used only when `DEMAND_SITE_ID` is unset. It does **not** filter demand. With neither set, a request without `siteId` is `400 invalid-site-id` (`ErrNoSite`). | `cmd/api/main.go` (`configFromEnv`) |
| `LOOKBACK_DAYS` | `28` | no | Demand window, in days, of a plan whose request has no `lookbackDays`. Integer 1 to 365; anything else logs `invalid LOOKBACK_DAYS; using the default` and uses 28. It does not change the default `windowDays` of `GET /sku-velocity`, which is always 28. | `cmd/api/main.go` (`parseLookbackDays`), `usecases.DefaultLookbackDays` |
| `FORWARD_ZONE_CODES` | `FWD` | no | Comma-separated zone codes whose active `Storage` slots are forward pick slots. Blank entries are dropped; an empty list means `FWD`. | `cmd/api/main.go` (`parseZoneCodes`), `postgres.SlotCatalogue.ForwardSlots` |

### Kafka: publishing

| Variable | Default | Required | Meaning | Source |
| --- | --- | --- | --- | --- |
| `EVENT_PUBLISHER` | `kafka` | no | Kept because the chart and warehouse-infra set it. The only value is `kafka` (the outbox relay writes to `warehouse.slotting-optimization.events`, always on). `log` refuses to boot (`EVENT_PUBLISHER=log is not supported: this context publishes to real Kafka`), any other value too (`EVENT_PUBLISHER: unknown value ... (want kafka)`). | `cmd/api/main.go` (`parsePublisherMode`, `startOutboxRelay`) |
| `KAFKA_BROKERS` | none | **yes** | Comma-separated bootstrap brokers, shared by the relay writer, the four readers, the four DLQ writers and the `/readyz` broker check. Unset exits at boot with `KAFKA_BROKERS is required: this context runs on real Kafka (outbox relay, consumers, /readyz)`. The fleet broker is reachable at `localhost:9092` from a laptop. | `cmd/api/main.go` (`loadRuntime`, `startConsumers`, `startOutboxRelay`) |
| `OUTBOX_RELAY_INTERVAL` | `1s` | no | Sleep between relay passes that did not fill a batch of 100 rows. Go duration (`500ms`) or plain seconds (`0.5`); invalid or non-positive logs `invalid OUTBOX_RELAY_INTERVAL; using the default`. | `cmd/api/main.go` (`parseRelayInterval`), `outbox.DefaultInterval` |
| `OUTBOX_MAX_ATTEMPTS` | `20` | no | How many times one outbox row may fail before the relay **excludes it from claims** ([ADR 0008](/docs/adr/0008-outbox-relay-attempt-cap)), so a poison row cannot block every row behind it forever. The excluded row stays in `outbox_events` with `attempts` and `last_error`; the failure that reaches the cap logs one ERROR (`outbox relay: row reached the attempt cap ...`). `0` = unlimited (the old behaviour); a negative or non-integer value logs `invalid OUTBOX_MAX_ATTEMPTS; using the default` and uses 20. Re-arm a row with `UPDATE outbox_events SET attempts = 0 WHERE id = ...` ([Runbook](/docs/operations/runbook#outbox-relay-attempt-cap)). Chart value `config.outboxMaxAttempts`. | `cmd/api/main.go` (`outboxMaxAttempts`), `outbox.DefaultMaxAttempts` |

### Kafka: consumers

Each local copy has its own mode variable and consumer group. All four
consumers **always run** ([ADR 0011](/docs/adr/0011-postgres-and-kafka-are-the-only-runtime)).
The mode names are kept because the chart and warehouse-infra set them, but
the only value is `kafka` (also the default when unset): `permissive` and
`off` exit at boot with `<MODE>=permissive is not supported: this context runs
on real Kafka`, an unknown value with `<MODE>: unknown mode "x" (want kafka)`.
A missing group exits at boot with `<GROUP> is required: the <MODE> consumer
always runs on real Kafka`. Group ids are never hard-coded
(`TestKafkaConsumerGroupNeverHardcodedInline`).

| Variable | Default | Required | Meaning | Source |
| --- | --- | --- | --- | --- |
| `DEMAND_MODE` | `kafka` | no | The demand consumer on `warehouse.order-management.events`. Only `kafka`. | `cmd/api/main.go` (`consumerSpecs`, `parseConsumerMode`) |
| `DEMAND_CONSUMER_GROUP` | none | **yes** | Consumer group id of the demand consumer. | `cmd/api/main.go` (`loadRuntime`) |
| `PRODUCT_MODE` | `kafka` | no | The product consumer on `warehouse.product-master.events`. Only `kafka`. | `cmd/api/main.go` (`consumerSpecs`, `parseConsumerMode`) |
| `PRODUCT_CONSUMER_GROUP` | none | **yes** | Consumer group id of the product consumer. | `cmd/api/main.go` (`loadRuntime`) |
| `LAYOUT_MODE` | `kafka` | no | The layout consumer on `warehouse.facility.events`. Only `kafka`. | `cmd/api/main.go` (`consumerSpecs`, `parseConsumerMode`) |
| `LAYOUT_CONSUMER_GROUP` | none | **yes** | Consumer group id of the layout consumer. | `cmd/api/main.go` (`loadRuntime`) |
| `RELOCATION_MODE` | `kafka` | no | The StockRelocated consumer on `warehouse.inventory.events`: the execution progress of the slot plans (ADR 0010). Only `kafka`. Chart value `config.relocationMode`. | `cmd/api/main.go` (`consumerSpecs`, `parseConsumerMode`) |
| `RELOCATION_CONSUMER_GROUP` | none | **yes** | Consumer group id of the StockRelocated consumer (e.g. `slotting-optimization-relocation`). Chart value `config.relocationConsumerGroup`. | `cmd/api/main.go` (`loadRuntime`) |

### Readiness and worker failure

`/healthz` is liveness only. `/readyz` is 200 only while the drain gate is open
**and** Postgres answers a ping **and** a Kafka broker answers a metadata
request with a topic list (an empty list is healthy: the service never
requires topics, the writer auto-creates them); otherwise 503
`dependency-unavailable` naming `postgres` and/or `kafka`. The verdict is cached
for 3 s and a check is bounded at 2 s. When the outbox relay or a consumer
returns on its own (for example after the broker went away), the process logs
`<worker> stopped unexpectedly: exiting so the orchestrator restarts the
service` and exits non-zero, so the pod restarts instead of staying alive with
a dead worker.

## `cmd/mcp`

The read-only MCP server (ADR 0005). It runs no relay and no consumer and
never dials Kafka. Chart: `mcp.enabled`, `mcp.httpAddr`; the rest is shared
with the api values.

| Variable | Default | Required | Meaning | Source |
| --- | --- | --- | --- | --- |
| `MCP_ADDR` | `:8090` | no | Listen address of the Streamable HTTP MCP endpoint and `GET /healthz`. | `cmd/mcp/main.go` (`run`) |
| `DATABASE_URL` | none | **yes** | As for `cmd/api`: unset exits at boot (`DATABASE_URL is required: this context runs on real Postgres, there is no in-memory mode`). It reads the same Postgres as `cmd/api`. | `cmd/mcp/main.go` (`buildDeps`) |
| `MIGRATIONS_DATABASE_URL` | `DATABASE_URL` | no | As for `cmd/api`: the embedded migrations run before the pgx pool opens. | `cmd/mcp/main.go` (`buildDeps`) |
| `DEMAND_SITE_ID`, `DEFAULT_SITE_ID` | unset | no | The site `get_forward_slots` and `get_sku_velocity` default to (`DEMAND_SITE_ID`, else `DEFAULT_SITE_ID`), matching `cmd/api`. | `cmd/mcp/main.go` (`defaultSite`) |
| `LOG_LEVEL`, `SERVICE_VERSION`, `ENVIRONMENT`, `OTEL_EXPORTER_OTLP_ENDPOINT` | as for `cmd/api` | no | Logging and telemetry, parsed the same way. | `cmd/mcp/main.go`, `telemetry.go` |

## `cmd/a2a`

The read-only A2A agent (ADR 0013), the same read surface as `cmd/mcp` as an
Agent2Agent agent. It runs no relay and no consumer and never dials Kafka.
Chart: `a2a.enabled`, `a2a.httpAddr`, `a2a.agentUrl`; the rest is shared with
the api values.

| Variable | Default | Required | Meaning | Source |
| --- | --- | --- | --- | --- |
| `A2A_AGENT_URL` | none | **yes** | Absolute http(s) URL of the JSON-RPC endpoint, advertised on the Agent Card. Unset, relative or schemeless exits at boot naming the variable. The chart defaults it to `http://<release>-a2a.<ns>.svc.cluster.local:8093/` (`a2a.agentUrl` overrides). | `cmd/a2a/main.go` (`loadConfig`) |
| `A2A_ADDR` | `:8093` | no | Listen address of the JSON-RPC endpoint (`/` and `/a2a`), the Agent Card (`/.well-known/agent-card.json`) and `GET /healthz`. | `cmd/a2a/main.go` (`run`) |
| `DATABASE_URL` | none | **yes** | As for `cmd/mcp`: unset exits at boot. It reads the same Postgres as `cmd/api`. | `cmd/a2a/main.go` (`loadConfig`) |
| `MIGRATIONS_DATABASE_URL` | `DATABASE_URL` | no | As for `cmd/api`: the embedded migrations run before the pgx pool opens. | `cmd/a2a/main.go` (`buildDeps`) |
| `DEMAND_SITE_ID`, `DEFAULT_SITE_ID` | unset | no | The site `forward_slot_map` defaults to (`DEMAND_SITE_ID`, else `DEFAULT_SITE_ID`), matching `cmd/api`. | `cmd/a2a/main.go` (`defaultSite`) |
| `LOG_LEVEL`, `SERVICE_VERSION`, `ENVIRONMENT`, `OTEL_EXPORTER_OTLP_ENDPOINT` | as for `cmd/api` | no | Logging and telemetry, parsed the same way. | `cmd/a2a/main.go`, `telemetry.go` |

## `cmd/slotting-projector`

The analytics writer (ADR 0006). It opens only the analytical database and
Kafka, never the OLTP database. Chart: `analytics.enabled` (default `false`),
`analytics.url` or `analytics.existingSecret` / `analytics.existingSecretKey`
(the chart refuses `analytics.enabled` without one of them),
`analytics.consumerGroup`, `analytics.projector.adminAddr`; `KAFKA_BROKERS`
comes from `kafka.brokers`, `LOG_LEVEL` from `config.logLevel`.

| Variable | Default | Required | Meaning | Source |
| --- | --- | --- | --- | --- |
| `ANALYTICS_DATABASE_URL` | none | **yes** | DIRECT (non-PgBouncer) DSN of the analytical database. The projector runs the embedded `analytics/migrations` through it (5 boot-retry attempts), then opens its writer pool (`MaxConns = 5`, `statement_timeout = 10s`). Unset exits at boot with `ANALYTICS_DATABASE_URL is required: the projector writes only to the analytical database`. | `cmd/slotting-projector/main.go` (`loadConfig`, `openAnalyticsPool`), `analyticsstore/pool.go` |
| `KAFKA_BROKERS` | none | **yes** | Comma-separated brokers of the analytics reader and its DLQ writer. Unset exits with `KAFKA_BROKERS is required: the projector consumes the analytics topic`. The reader dials lazily, so a broker outage does not fail the boot. | `cmd/slotting-projector/main.go` (`loadConfig`) |
| `ANALYTICS_CONSUMER_GROUP` | `slotting-optimization-analytics` | no | Consumer group on `warehouse.slotting-optimization.analytics`. A new group starts at the first retained offset: that is the backfill / rebuild. | `cmd/slotting-projector/main.go` (`defaultConsumerGroup`) |
| `ADMIN_ADDR` | `:8091` | no | Listen address of `GET /healthz` and `GET /readyz` (no other route). | `cmd/slotting-projector/main.go` (`loadConfig`) |
| `LOG_LEVEL` | `info` | no | As for `cmd/api`. | `cmd/slotting-projector/main.go` (`newLogger`) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | no | As for `cmd/api`; `service.name` is `slotting-optimization-projector`. | `cmd/slotting-projector/main.go` (`setupTelemetry`) |
| `SERVICE_VERSION`, `ENVIRONMENT` | `dev`, `local` | no | OTel resource attributes, as for `cmd/api`. | `cmd/slotting-projector/main.go`, `telemetry.go` |

## `cmd/slotting-reports`

The read-only reports binary (ADR 0006). It opens only the analytical
database, through a read-only pool, and never Kafka. Chart: the same
`analytics.*` DSN values as the projector, `analytics.reports.httpAddr`,
`analytics.reports.service.port` (default `80`, target `8092`).

| Variable | Default | Required | Meaning | Source |
| --- | --- | --- | --- | --- |
| `ANALYTICS_DATABASE_URL` | none | **yes** | DSN of the analytical database, ideally a read-only role. The pool sets `default_transaction_read_only = on`, `MaxConns = 5`, `statement_timeout = 15s`; the first ping is boot-retried. Unset exits with `ANALYTICS_DATABASE_URL is required: the reports are served from the analytical database`. warehouse-infra's `slotting-optimization-analytics-db` Secret holds the same direct DSN under `ANALYTICS_DATABASE_URL` and `ANALYTICS_READER_DATABASE_URL`; the chart reads only `ANALYTICS_DATABASE_URL`, so the reports run as the projector's role, made read-only by the pool (ADR 0006 names a single analytics DSN). | `cmd/slotting-reports/main.go` (`run`), `analyticsstore/pool.go` (`NewReadOnlyPool`) |
| `HTTP_ADDR` | `:8092` | no | Listen address of `/reports/slot-plan-funnel`, `/reports/freshness` and `/healthz`. | `cmd/slotting-reports/main.go` (`run`) |
| `LOG_LEVEL` | `info` | no | As for `cmd/api`. | `cmd/slotting-reports/main.go` (`newLogger`) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | no | As for `cmd/api`; `service.name` is `slotting-optimization-reports`. | `cmd/slotting-reports/main.go` (`run`) |
| `SERVICE_VERSION`, `ENVIRONMENT` | `dev`, `local` | no | OTel resource attributes, as for `cmd/api`. | `cmd/slotting-reports/main.go`, `telemetry.go` |

## Fixed values (not configurable)

These are constants in code; changing them is a code change.

| Value | Constant | Source |
| --- | --- | --- |
| HTTP graceful drain budget 10 s; consumer and relay drain 10 s each; telemetry flush 5 s | `shutdownTimeout`, `workerDrainTimeout` | `cmd/api/main.go` |
| Projector and reports drain 10 s | `shutdownTimeout` | `cmd/slotting-projector/main.go`, `cmd/slotting-reports/main.go` |
| Analytical pools: projector `MaxConns = 5`, `statement_timeout = 10s`; reports `MaxConns = 5`, `statement_timeout = 15s`, read-only | `analyticsstore.MaxConns`, `StatementTimeout`, `ReportsMaxConns`, `ReportsStatementTimeout` | `internal/adapters/outbound/analyticsstore/pool.go` |
| Report range: default 30 days ending now, at most 366 days; granularity `day` only | `report.DefaultWindow`, `report.MaxWindow`, `report.GranularityDay` | `internal/analytics/report/range.go` |
| Boot retry: 5 attempts with 1 s, 2 s, 4 s, 8 s waits (15 s of backoff in total, no wait after the last attempt) for the migrations and the first ping | `bootretry.Retries`, `bootretry.Delay` | `internal/bootretry/bootretry.go` |
| Pool: `MaxConns = 10`, `statement_timeout = 5s` | `postgres.MaxConns`, `postgres.StatementTimeout` | `internal/adapters/outbound/postgres/pool.go` |
| Relay batch 100 rows | `outbox.DefaultBatchSize` | `internal/adapters/outbound/outbox/relay.go` |
| Consumer retry: 200 ms doubling to 5 s, 5 handling attempts, then `<topic>.dlq` | `DefaultRetryInitial`, `DefaultRetryMax`, `domainMaxHandlerAttempts` | `internal/adapters/inbound/kafka/kafka.go`, `deadletter.go` |
| Projector consumer: a transient failure retries the same message forever (200 ms doubling to 5 s); a deterministic failure is dead-lettered at once | `AnalyticsConsumer.Run` (no `maxAttempts`) | `internal/adapters/inbound/kafka/analytics_consumer.go` |
| ABC thresholds 80 % / 95 % of picks | `planning.DefaultParameters` | `internal/domain/planning/planner.go` |
| Request body limit 64 KiB | `maxBodyBytes` | `internal/adapters/inbound/http/response.go` |
| OTLP metric export every 30 s | `metricExportInterval` | `internal/adapters/outbound/telemetry/telemetry.go` |

## Typical profiles

| Profile | Variables |
| --- | --- |
| Unit tests, BDD | nothing: they use the in-memory test doubles directly and never boot the binary |
| Local run (`docker compose up -d`, [Quickstart](/docs/overview/quickstart)) | `DATABASE_URL`, `KAFKA_BROKERS`, the four `*_CONSUMER_GROUP`, and `DEFAULT_SITE_ID=WH1` to omit `siteId` |
| Local analytics read side ([Quickstart](/docs/overview/quickstart#run-the-analytics-read-side-optional)) | `ANALYTICS_DATABASE_URL` (a second database), `KAFKA_BROKERS` and a `*-local` `ANALYTICS_CONSUMER_GROUP` for the projector; `ANALYTICS_DATABASE_URL` for the reports |
| A cluster deployment | everything above with real DSNs and brokers, plus `MIGRATIONS_DATABASE_URL` when `DATABASE_URL` goes through a pooler, `OTEL_EXPORTER_OTLP_ENDPOINT`, `SERVICE_VERSION`, `ENVIRONMENT` and `CORS_ALLOWED_ORIGINS`, all set by `charts/slotting-optimization` from its values; the chart refuses to render without `database.*`, `kafka.brokers` and the four consumer groups, and without an analytics DSN source when `analytics.enabled` ([Runbook](/docs/operations/runbook#deployment)). warehouse-infra sets `mcp.enabled: true` and `analytics.enabled: true` with `analytics.existingSecret: slotting-optimization-analytics-db`. |
