---
id: configuration
title: Configuration
sidebar_label: Configuration
sidebar_position: 1
---

# Configuration

`slotting-optimization` ships one binary, `cmd/api`, and it is configured
only through environment variables. There is no config file and no flag.
Every variable below is read in `cmd/api/main.go` unless the *Source* column
says otherwise, and the list is complete: it comes from every `os.Getenv` /
`getenv` call in `cmd/` and `internal/` outside `_test.go` files.

## `cmd/api`

### Server, logging and telemetry

| Variable | Default | Required | Meaning | Source |
| --- | --- | --- | --- | --- |
| `HTTP_ADDR` | `:8080` | no | Listen address of the REST API, `/healthz`, `/readyz` and `/metrics`. `ReadHeaderTimeout` is fixed at 5 s. | `cmd/api/main.go` (`run`) |
| `LOG_LEVEL` | `info` | no | `debug`, `info`, `warn` or `error` (case-insensitive). Any other value falls back to `info` silently. Logs are JSON lines on stdout (`log/slog`). | `cmd/api/main.go` (`newLogger`) |
| `SERVICE_VERSION` | `dev` | no | OTel resource attribute `service.version`. | `cmd/api/main.go` (`setupTelemetry`) |
| `ENVIRONMENT` | `local` | no | OTel resource attribute `deployment.environment.name`. | `internal/adapters/outbound/telemetry/telemetry.go` (`Environment`) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | no | OTLP/gRPC endpoint (host:port, insecure) for traces and metrics. The exporters connect lazily: no Collector means telemetry is dropped, never a failed boot. | `cmd/api/main.go`, `telemetry.DefaultOTLPEndpoint` |
| `CORS_ALLOWED_ORIGINS` | `http://localhost:5173` | no | Comma-separated origins allowed by the CORS middleware. Methods `GET` and `POST`; headers `Accept`, `Content-Type`, `Idempotency-Key`; no credentials. | `internal/adapters/inbound/http/server.go` (`corsAllowedOrigins`) |
| `SHUTDOWN_DRAIN_DELAY` | `5s` | no | How long the process waits after flipping `/readyz` to 503 before it stops accepting connections. Go duration; `0` disables the wait; a negative or unparsable value logs `ignoring invalid SHUTDOWN_DRAIN_DELAY` and uses 5 s. | `cmd/api/main.go` (`shutdownDrainDelay`) |

### Database

| Variable | Default | Required | Meaning | Source |
| --- | --- | --- | --- | --- |
| `DATABASE_URL` | unset | no | Postgres DSN of the pgx pool. **Unset selects the in-memory adapters**: no migrations, no `Idempotency-Key` middleware, nothing survives a restart. Set, the process runs the embedded migrations, opens a pool (`MaxConns = 10`, `statement_timeout = 5s`) and pings it; failure after the retry budget exits the process. | `cmd/api/main.go` (`buildAdapters`), `internal/adapters/outbound/postgres/pool.go` |
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
| `EVENT_PUBLISHER` | `log` | no | Sink of the outbox relay. `log` writes `event published (log sink)` lines and marks rows published; `kafka` writes to `warehouse.slotting-optimization.events`. Any other value refuses to boot (`unknown EVENT_PUBLISHER`). | `cmd/api/main.go` (`parsePublisherMode`, `startOutboxRelay`) |
| `KAFKA_BROKERS` | unset | when `EVENT_PUBLISHER=kafka` or any `*_MODE=kafka` | Comma-separated bootstrap brokers, shared by the relay writer, the three readers and the three DLQ writers. Missing when needed refuses to boot. The fleet broker is reachable at `localhost:9092` from a laptop. | `cmd/api/main.go` (`planConsumers`, `startOutboxRelay`) |
| `OUTBOX_RELAY_INTERVAL` | `1s` | no | Sleep between relay passes that did not fill a batch of 100 rows. Go duration (`500ms`) or plain seconds (`0.5`); invalid or non-positive logs `invalid OUTBOX_RELAY_INTERVAL; using the default`. | `cmd/api/main.go` (`parseRelayInterval`), `outbox.DefaultInterval` |

### Kafka: consumers

Each local copy has its own mode and consumer group. A mode is `permissive`
(the default, consumer **not started**) or `kafka`. An unknown mode, or
`kafka` without its group variable, refuses to boot. Group ids are never
hard-coded (`TestKafkaConsumerGroupNeverHardcodedInline`).

| Variable | Default | Required | Meaning | Source |
| --- | --- | --- | --- | --- |
| `DEMAND_MODE` | `permissive` | no | `kafka` starts the demand consumer on `warehouse.order-management.events`. | `cmd/api/main.go` (`consumerSpecs`) |
| `DEMAND_CONSUMER_GROUP` | unset | when `DEMAND_MODE=kafka` | Consumer group id of the demand consumer. | `cmd/api/main.go` (`planConsumers`) |
| `PRODUCT_MODE` | `permissive` | no | `kafka` starts the product consumer on `warehouse.product-master.events`. | `cmd/api/main.go` (`consumerSpecs`) |
| `PRODUCT_CONSUMER_GROUP` | unset | when `PRODUCT_MODE=kafka` | Consumer group id of the product consumer. | `cmd/api/main.go` (`planConsumers`) |
| `LAYOUT_MODE` | `permissive` | no | `kafka` starts the layout consumer on `warehouse.facility.events`. | `cmd/api/main.go` (`consumerSpecs`) |
| `LAYOUT_CONSUMER_GROUP` | unset | when `LAYOUT_MODE=kafka` | Consumer group id of the layout consumer. | `cmd/api/main.go` (`planConsumers`) |

## Fixed values (not configurable)

These are constants in code; changing them is a code change.

| Value | Constant | Source |
| --- | --- | --- |
| HTTP graceful drain budget 10 s; consumer and relay drain 10 s each; telemetry flush 5 s | `shutdownTimeout`, `workerDrainTimeout` | `cmd/api/main.go` |
| Boot retry: 5 attempts with 1 s, 2 s, 4 s, 8 s waits for the migrations and the first ping | `bootretry.Retries`, `bootretry.Delay` | `internal/bootretry/bootretry.go` |
| Pool: `MaxConns = 10`, `statement_timeout = 5s` | `postgres.MaxConns`, `postgres.StatementTimeout` | `internal/adapters/outbound/postgres/pool.go` |
| Relay batch 100 rows | `outbox.DefaultBatchSize` | `internal/adapters/outbound/outbox/relay.go` |
| Consumer retry: 200 ms doubling to 5 s, 5 handling attempts, then `<topic>.dlq` | `DefaultRetryInitial`, `DefaultRetryMax`, `domainMaxHandlerAttempts` | `internal/adapters/inbound/kafka/kafka.go`, `deadletter.go` |
| ABC thresholds 80 % / 95 % of picks | `planning.DefaultParameters` | `internal/domain/planning/planner.go` |
| Request body limit 64 KiB | `maxBodyBytes` | `internal/adapters/inbound/http/response.go` |
| OTLP metric export every 30 s | `metricExportInterval` | `internal/adapters/outbound/telemetry/telemetry.go` |

## Typical profiles

| Profile | Variables |
| --- | --- |
| Unit tests, BDD, a quick look | nothing (in-memory, log sink, no consumers); add `DEFAULT_SITE_ID=WH1` to omit `siteId` |
| Local Postgres | `DATABASE_URL`, `DEFAULT_SITE_ID` |
| Local Postgres + the fleet broker | the above plus `EVENT_PUBLISHER=kafka`, `KAFKA_BROKERS=localhost:9092` and, per copy you want filled, `*_MODE=kafka` with a `*_CONSUMER_GROUP` no deployed instance uses ([Quickstart](/docs/overview/quickstart#run-with-kafka)) |
| A cluster deployment | everything above with real DSNs, plus `MIGRATIONS_DATABASE_URL` when `DATABASE_URL` goes through a pooler, `OTEL_EXPORTER_OTLP_ENDPOINT`, `SERVICE_VERSION`, `ENVIRONMENT` and `CORS_ALLOWED_ORIGINS`. No Helm chart exists yet ([Runbook](/docs/operations/runbook#deployment)). |
