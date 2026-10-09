---
id: quickstart
title: Quickstart
sidebar_label: Quickstart
sidebar_position: 3
---

# Quickstart

Build, test and run `slotting-optimization` on your machine, then call it.
Every command and response on this page was run against this repository
(`cmd/api`, in-memory and Postgres modes) on 2026-10-09.

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

## Run in memory (no database, no broker)

With `DATABASE_URL` unset the binary wires in-memory adapters, starts no
consumer (every `*_MODE` defaults to `permissive`) and drains the outbox into
its log (`EVENT_PUBLISHER` defaults to `log`). Set a default site so requests
may omit `siteId`:

```bash
go build -o bin/slotting-api ./cmd/api
HTTP_ADDR=:8080 DEFAULT_SITE_ID=WH1 ./bin/slotting-api
```

The process logs JSON lines to stdout:

```json
{"level":"INFO","msg":"DATABASE_URL not configured; using in-memory adapters"}
{"level":"INFO","msg":"no consumer is in kafka mode; the local copies are whatever the database holds"}
{"level":"INFO","msg":"outbox relay running","publisher":"log","interval":1000000000,"topic":"warehouse.slotting-optimization.events"}
{"level":"INFO","msg":"http server listening","addr":":8080"}
```

The local copies are empty in this mode, so a generated plan is empty, but the
whole lifecycle works:

```bash
curl -s localhost:8080/healthz                     # {"status":"ok"}
curl -s localhost:8080/readyz                      # {"status":"ready"}
curl -s -X POST localhost:8080/slot-plans -d '{"lookbackDays":14}'
# 201, Location: /slot-plans/plan-8b9257d1-...
# {"planId":"plan-8b9257d1-...","siteId":"WH1","state":"Draft","policy":"abc-velocity-v1",
#  "windowFrom":"2026-09-25T11:43:49.329263Z","windowTo":"2026-10-09T11:43:49.329263Z",
#  "generatedAt":"2026-10-09T11:43:49.329263Z","version":1,"assignments":[],"moves":[],"unassigned":[]}
curl -s -X POST localhost:8080/slot-plans/plan-8b9257d1-.../approve   # state Approved, version 2
curl -s -X POST localhost:8080/slot-plans/plan-8b9257d1-.../approve   # 409 plan-not-draft
```

and the relay logs each event it would have published:

```json
{"level":"INFO","msg":"event published (log sink)","topic":"warehouse.slotting-optimization.events","type":"com.warehouse.wms.slotting-optimization.slotplan.SlotPlanGenerated","subject":"plan-8b9257d1-...","id":"bfa2833a-..."}
```

## Run against a local Postgres

There is no `docker-compose.yml` in this repository. Start a throwaway
Postgres (the same major version the integration tests use):

```bash
docker run -d --rm --name slotting-pg -e POSTGRES_PASSWORD=postgres \
  -e POSTGRES_DB=slotting -p 55432:5432 postgres:16-alpine

HTTP_ADDR=:8080 DEFAULT_SITE_ID=WH1 \
  DATABASE_URL='postgres://postgres:postgres@localhost:55432/slotting?sslmode=disable' \
  ./bin/slotting-api
```

The binary applies the embedded migration `0001_slotting_schema.up.sql` at
boot (through `MIGRATIONS_DATABASE_URL`, which defaults to `DATABASE_URL`) and
logs `postgres adapters configured`.

### Seed the local copies

In the cluster the three copies are fed by Kafka. Locally you can insert rows
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
docker exec -i slotting-pg psql -U postgres -d slotting < seed.sql
```

The demand `site_id` must equal the zone's `site_code`, and the zone code must
be one of `FORWARD_ZONE_CODES` (default `FWD`): `ForwardSlots` joins on both.

### First calls

With Postgres, `POST /slot-plans` requires an `Idempotency-Key`:

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
different body is `422 idempotency-key-reused`.

```bash
curl -s localhost:8080/sku-velocity?siteId=WH1
# {"siteId":"WH1",...,"items":[{"sku":"SKU-A","picks":3,"units":6},{"sku":"SKU-B","picks":1,"units":5},{"sku":"SKU-C","picks":1,"units":1}]}

curl -s -X POST localhost:8080/slot-plans/plan-ee0b77f6-.../approve      # Approved, version 2
curl -s localhost:8080/forward-slots?siteId=WH1
# {"siteId":"WH1","planId":"plan-ee0b77f6-...","approvedAt":"...","assignments":[{"sku":"SKU-A","slot":"FWD-01-01","abcClass":"A"},...]}

curl -s -X POST localhost:8080/slot-plans/<another-draft>/reject \
  -H 'Content-Type: application/json' -d '{"reason":"wait for the new FWD aisle"}'
curl -s 'localhost:8080/slot-plans?siteId=WH1&limit=1'                   # newest first, with nextCursor
```

Every write lands in `outbox_events` in the same transaction:

```sql
SELECT id, event_type, subject, published_at IS NOT NULL AS published FROM outbox_events ORDER BY id;
```

## Run with Kafka

The fleet runs **one** Kafka broker, inside the kind cluster, exposed on
`localhost:9092` through its external access listener (the old docker-compose
Kafka is retired). To publish for real and fill the copies from the producers'
topics:

```bash
EVENT_PUBLISHER=kafka KAFKA_BROKERS=localhost:9092 \
DEMAND_MODE=kafka  DEMAND_CONSUMER_GROUP=slotting-optimization-demand-local \
PRODUCT_MODE=kafka PRODUCT_CONSUMER_GROUP=slotting-optimization-product-local \
LAYOUT_MODE=kafka  LAYOUT_CONSUMER_GROUP=slotting-optimization-layout-local \
DATABASE_URL='postgres://postgres:postgres@localhost:55432/slotting?sslmode=disable' \
./bin/slotting-api
```

Use group ids that no deployed instance uses: a shared group id on the shared
broker splits the partitions between your laptop and the cluster (the reason
`TestKafkaConsumerGroupNeverHardcodedInline` exists). `readerConfig` leaves
kafka-go's `StartOffset` at its default, `FirstOffset`, so a fresh group with no
committed offset reads each topic from the beginning and rebuilds the copy
(the claims in `processed_events` make a replay idempotent).

## The documentation site

```bash
cd docs
npm ci
npm run start          # dev server
npm run build          # what the Docs workflow runs on every pull request
npm run gen-api-docs   # regenerate docs/api-reference/rest from apis/openapi.yaml
```
