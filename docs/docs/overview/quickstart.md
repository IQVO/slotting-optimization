---
id: quickstart
title: Quickstart
sidebar_label: Quickstart
sidebar_position: 3
---

# Quickstart

Build, test and run `slotting-optimization` on your machine, then call it.
The commands on this page were run against this repository (`cmd/api` on the
`docker-compose.yml` Postgres and Kafka) on 2026-10-19.

## Prerequisites

| Tool | Version | Why |
| --- | --- | --- |
| Go | the `go` directive in `go.mod` (`1.27.2`) | build and test |
| Docker | any recent | a local Postgres, and `make integration` (testcontainers starts its own Postgres `postgres:16-alpine` and Kafka `confluentinc/confluent-local:7.6.1`) |
| golangci-lint | `v2.14.0` (pinned in `Makefile` and CI) | `make lint`, `make check` |
| gremlins | `v0.6.0` | `make mutation` (optional) |
| govulncheck | latest | `make vuln` (optional) |
| lefthook | `>= 1.7.0` | optional git hooks: `lefthook install` runs `make fmt-check vet lint` on commit and `make check` on push |
| Node.js | `>= 20` | only for this documentation site (`docs/`) |

## Build and test

```bash
make build        # go build ./...
make test         # go test ./... -race  (unit + httptest + BDD, no database needed)
make check        # fmt-check vet build lint test: the fast pre-commit gate
make check-all    # check + coverage (90 % gate) + arch-test + bdd
make integration  # go test -tags=integration ./... -race -count=1 (needs Docker only)
```

`make help` (the default target) lists every target. The full test pyramid is
on [Testing](/docs/development/testing).

## Run locally (real Postgres and real Kafka)

Postgres and Kafka are the only runtime ([ADR 0011](/docs/adr/0011-postgres-and-kafka-are-the-only-runtime)):
the binary has no in-memory mode, so it exits at boot without `DATABASE_URL`,
`KAFKA_BROKERS` and the four `*_CONSUMER_GROUP` variables, naming the missing
one. The `docker-compose.yml` at the repository root starts both
dependencies (Postgres on `localhost:55432`, a single-node Kafka on
`localhost:9092`, advertised as `localhost` so the first produce works):

```bash
docker compose up -d --wait
# port 9092 taken (the kind cluster publishes its broker there)?
#   KAFKA_HOST_PORT=19092 docker compose up -d --wait   and KAFKA_BROKERS=localhost:19092

go build -o bin/slotting-api ./cmd/api
HTTP_ADDR=:8080 DEFAULT_SITE_ID=WH1 \
  DATABASE_URL='postgres://slotting:slotting@localhost:55432/slotting?sslmode=disable' \
  KAFKA_BROKERS=localhost:9092 \
  DEMAND_CONSUMER_GROUP=slotting-optimization-demand-local \
  PRODUCT_CONSUMER_GROUP=slotting-optimization-product-local \
  LAYOUT_CONSUMER_GROUP=slotting-optimization-layout-local \
  RELOCATION_CONSUMER_GROUP=slotting-optimization-relocation-local \
  ./bin/slotting-api
```

Use group ids that no deployed instance uses: a shared group id on the shared
broker splits the partitions between your laptop and the cluster (the reason
`TestKafkaConsumerGroupNeverHardcodedInline` exists). Stop with
`docker compose down -v`.

The binary applies the embedded migrations `0001_slotting_schema.up.sql` and
`0002_slot_plan_move_executions.up.sql` at boot (through `MIGRATIONS_DATABASE_URL`, which defaults to `DATABASE_URL`),
starts the four consumers and the outbox relay, and only then binds the
listener. The process logs JSON lines to stdout:

```json
{"level":"INFO","msg":"postgres adapters configured"}
{"level":"INFO","msg":"consumer running","consumer":"demand consumer","topic":"warehouse.order-management.events","group_id":"slotting-optimization-demand-local","brokers":["localhost:9092"]}
{"level":"INFO","msg":"consumer running","consumer":"layout consumer","topic":"warehouse.facility.events","group_id":"slotting-optimization-layout-local","brokers":["localhost:9092"]}
{"level":"INFO","msg":"consumer running","consumer":"product consumer","topic":"warehouse.product-master.events","group_id":"slotting-optimization-product-local","brokers":["localhost:9092"]}
{"level":"INFO","msg":"consumer running","consumer":"relocation consumer","topic":"warehouse.inventory.events","group_id":"slotting-optimization-relocation-local","brokers":["localhost:9092"]}
{"level":"INFO","msg":"http server listening","addr":":8080"}
{"level":"INFO","msg":"outbox relay running","publisher":"kafka","interval":1000000000,"max_attempts":20,"topic":"warehouse.slotting-optimization.events"}
```

```bash
curl -s localhost:8080/healthz                     # {"status":"ok"}
curl -s localhost:8080/readyz                      # {"status":"ready"} (Postgres ping + Kafka metadata answered)
```

`/readyz` is 503 `dependency-unavailable` (naming `postgres` and/or `kafka`)
while a dependency is unreachable. The first relay write to a topic that does
not exist yet fails once (`[3] Unknown Topic Or Partition`, logged as
`outbox relay pass failed`) and succeeds on the next pass: the broker
auto-creates the topic on that first write, and the outbox row is published
with `attempts = 2`.

### Seed the local copies

In the cluster the copies are fed by Kafka. Locally you can insert rows
directly; this is exactly what the consumers would write:

```sql
-- One forward zone (code FWD) at site WH1 and two forward slots in it.
INSERT INTO zones (zone_id, site_code, area_code, zone_code, temperature_class, hazmat)
VALUES ('zone-wh1-fwd', 'WH1', 'PICK', 'FWD', 'Ambient', false);

INSERT INTO slots (location_code, zone_id, role, max_weight_kg, max_volume_m3) VALUES
  ('FWD-01-01', 'zone-wh1-fwd', 'Storage', 25, 0.05),
  ('FWD-01-02', 'zone-wh1-fwd', 'Storage', 25, 0.05);

-- Effective unit size of two SKUs (SKU-C has none yet).
INSERT INTO product_profiles (sku, handling_tags, temperature_class, volume_mm3, weight_g, version) VALUES
  ('SKU-A', '{}', 'Ambient', 1200000, 800, 1),
  ('SKU-B', '{}', 'Ambient', 900000, 400, 1);
INSERT INTO product_profiles (sku, version) VALUES ('SKU-C', 1);

-- ACTIVE demand lines due inside the default 28-day window.
INSERT INTO demand_lines (source_order_id, line_no, site_id, sku, units, due_at, state) VALUES
  ('SO-1', 1, 'WH1', 'SKU-A', 3, now() - interval '1 day', 'ACTIVE'),
  ('SO-2', 1, 'WH1', 'SKU-A', 1, now() - interval '2 days', 'ACTIVE'),
  ('SO-3', 1, 'WH1', 'SKU-A', 2, now() - interval '3 days', 'ACTIVE'),
  ('SO-3', 2, 'WH1', 'SKU-B', 5, now() - interval '3 days', 'ACTIVE'),
  ('SO-4', 1, 'WH1', 'SKU-C', 1, now() - interval '4 days', 'ACTIVE');
```

```bash
docker compose exec -T postgres psql -U slotting -d slotting < seed.sql
```

The demand `site_id` must equal the zone's `site_code`, and the zone code must
be one of `FORWARD_ZONE_CODES` (default `FWD`): `ForwardSlots` joins on both.

### First calls

With the real runtime `POST /slot-plans` requires an `Idempotency-Key`:

```bash
curl -s -X POST localhost:8080/slot-plans -d '{}'
# 400 {"type":"https://errors.slotting-optimization.warehouse-systems.dev/idempotency-key-required",...}

curl -s -i -X POST localhost:8080/slot-plans \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: demo-1' -d '{"siteId":"WH1"}'
```

```json
{"planId":"plan-ee0b77f6-...","siteId":"WH1","state":"Draft","policy":"abc-velocity-v1",
 "windowFrom":"2026-09-11T11:46:27.771349Z","windowTo":"2026-10-09T11:46:27.771349Z",
 "generatedAt":"2026-10-09T11:46:27.771349Z","version":1,
 "assignments":[{"sku":"SKU-A","slot":"FWD-01-01","abcClass":"A","picks":3,"units":6},
                {"sku":"SKU-B","slot":"FWD-01-02","abcClass":"A","picks":1,"units":5}],
 "moves":[{"sku":"SKU-A","toSlot":"FWD-01-01","kind":"Assign"},
          {"sku":"SKU-B","toSlot":"FWD-01-02","kind":"Assign"}],
 "unassigned":[{"sku":"SKU-C","reason":"NoPhysicalProfile"}]}
```

Repeating the same key and body replays the stored `201`; the same key with a
different body (or on another route) is `409 idempotency-key-reused`; a replay
carries `Idempotency-Replayed: true`. Approvals and rejections take a key too.

```bash
curl -s localhost:8080/sku-velocity?siteId=WH1
# {"siteId":"WH1",...,"items":[{"sku":"SKU-A","picks":3,"units":6},{"sku":"SKU-B","picks":1,"units":5},{"sku":"SKU-C","picks":1,"units":1}]}

curl -s -X POST localhost:8080/slot-plans/plan-ee0b77f6-.../approvals      # Approved, version 2
curl -s localhost:8080/forward-slots?siteId=WH1
# {"siteId":"WH1","planId":"plan-ee0b77f6-...","approvedAt":"...","assignments":[{"sku":"SKU-A","slot":"FWD-01-01","abcClass":"A"},...]}

curl -s -X POST localhost:8080/slot-plans/<another-draft>/rejections \
  -H 'Content-Type: application/json' -d '{"reason":"wait for the new FWD aisle"}'
curl -s 'localhost:8080/slot-plans?siteId=WH1&limit=1'                   # newest first, with nextCursor
```

Every write lands in `outbox_events` in the same transaction:

```sql
SELECT id, event_type, subject, published_at IS NOT NULL AS published FROM outbox_events ORDER BY id;
```

## Run the analytics read side (optional)

The projector and the reports binary need a second, analytical database
(`ANALYTICS_DATABASE_URL`, ADR 0006). `docker-compose.yml` starts only one
Postgres, so create one beside the OLTP database and run both binaries
against it (the projector applies its own migration at boot):

```bash
docker compose exec -T postgres createdb -U slotting slotting_analytics

go build -o bin/slotting-projector ./cmd/slotting-projector
go build -o bin/slotting-reports ./cmd/slotting-reports
ANALYTICS_DATABASE_URL='postgres://slotting:***@localhost:55432/slotting_analytics?sslmode=disable' \
  KAFKA_BROKERS=localhost:9092 ANALYTICS_CONSUMER_GROUP=slotting-optimization-analytics-local \
  ./bin/slotting-projector          # admin :8091 (/healthz, /readyz)
ANALYTICS_DATABASE_URL='postgres://slotting:***@localhost:55432/slotting_analytics?sslmode=disable' \
  ./bin/slotting-reports            # :8092

curl -s localhost:8092/reports/freshness
curl -s 'localhost:8092/reports/slot-plan-funnel?siteId=WH1'
```

`/reports/freshness` answers `{"as_of": null, "lag_seconds": null}` until the
projector has applied an event. Every plan decision of `cmd/api` above is
published twice (integration and analytics topic, one CloudEvents id), so
the funnel counts the plans generated, approved and rejected per UTC day of
the requested range (default: the 30 days ending now). These commands are
not captured from a run on this page; the response shapes come from
`internal/adapters/inbound/http/reports_handler.go` and `apis/openapi.yaml`.

## Fill the copies from the producers' topics

The fleet runs **one** Kafka broker inside the kind cluster, exposed on
`localhost:9092` through its external access listener. To read the producers'
real topics instead of the compose broker, point `KAFKA_BROKERS` at it (the
cluster's broker is shared: keep the `*-local` group ids). `readerConfig`
leaves kafka-go's `StartOffset` at its default, `FirstOffset`, so a fresh
group with no committed offset reads each topic from the beginning and
rebuilds the copy (the claims in `processed_events` make a replay
idempotent).

Known kafka-go behaviour, found in inbound-receiving and not measured for this
service: a group consumer that starts before its topic exists may not pick the
topic up when it appears until the next restart. The compose broker creates the
four consumed topics at the first metadata request of the consumers, which
happens at boot.

## The documentation site

```bash
cd docs
npm ci
npm run start          # dev server
npm run build          # what the Docs workflow runs on every pull request
npm run gen-api-docs   # regenerate docs/api-reference/rest from apis/openapi.yaml
```
