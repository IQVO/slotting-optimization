---
paths:
  - "Dockerfile"
  - ".dockerignore"
  - "charts/slotting-optimization/**"
---

# Packaging: image and Helm chart

- `Dockerfile` builds EVERY `cmd/*/` binary generically (a loop over the
  directories), so a new composition root can never ship without its binary.
  Today: `/app/api` (the ENTRYPOINT, :8080) and `/app/mcp` (:8090). The
  golang-migrate files are embedded in the binaries; no migrations directory
  is copied. Runtime image: alpine, non-root uid 1000. Images publish to
  `ghcr.io/iqvo/slotting-optimization` (ci.yml `docker-publish`).
- Chart `charts/slotting-optimization`: the api Deployment + Service
  (component `api`), HPA (off), HTTPRoute (`gatewayApi`, off), Ingress (off)
  and the MCP Deployment + Service (component `mcp`, `mcp.enabled`, off; see
  `mcp.md`). Every Service selects on `app.kubernetes.io/component`, so each
  selects exactly one Deployment (`tests/test_service_selectors.py`).
- Every env var `cmd/api/main.go` reads has a DEDICATED chart value
  (`config.*`, `database.*`, `kafka.*`, `otel.*`) rendered into
  `charts/slotting-optimization/templates/deployment.yaml` AND mirrored in `charts/slotting-optimization/templates/configmap.yaml`
  (checksum rollout): `HTTP_ADDR`, `LOG_LEVEL`, `CORS_ALLOWED_ORIGINS`,
  `EVENT_PUBLISHER`, `OUTBOX_RELAY_INTERVAL`, `DEMAND_SITE_ID`,
  `DEFAULT_SITE_ID`, `LOOKBACK_DAYS`, `FORWARD_ZONE_CODES`, `DEMAND_MODE` /
  `PRODUCT_MODE` / `LAYOUT_MODE` and their `*_CONSUMER_GROUP`,
  `SHUTDOWN_DRAIN_DELAY`, `KAFKA_BROKERS`, `DATABASE_URL`,
  `MIGRATIONS_DATABASE_URL`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `ENVIRONMENT`,
  `SERVICE_VERSION`. Optional ones render only when set; the `*_MODE` ones
  always render (permissive by default). Never route a variable through
  `extraEnv` when it has a dedicated value (the test fails on duplicates).
- Silent-no-op states are refused at render time (`charts/slotting-optimization/templates/_helpers.tpl`:
  `requireDatabase`, `requireKafkaForPublisher`, `requireKnownPublisher`,
  `requireConsumerConfig`, `requireLookbackDays`): no database source, a
  kafka publisher or consumer without `kafka.enabled`, a kafka consumer
  without its stable group, a group set while its mode is permissive, an
  unknown mode, `lookbackDays` outside 1..365.
- When `cmd/api` gains or drops an env var, change in the same PR: the
  `main.go` header, `values.yaml`, `deployment.yaml`, `configmap.yaml`,
  `ci/all-components-values.yaml`, and `API_ENV` in the selector test.
- Gates: `helm lint charts/slotting-optimization -f charts/slotting-optimization/ci/<file>`,
  `helm template` for both ci values files,
  `python3 charts/slotting-optimization/tests/test_service_selectors.py`
  (needs helm + PyYAML), `docker build -t slotting-optimization:local .`.
  CI runs the lint + selector test as `helm-lint` and a Trivy image scan as
  `trivy-scan` on pull requests (both non-required).
