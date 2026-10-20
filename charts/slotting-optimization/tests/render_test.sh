#!/usr/bin/env bash
# Chart render assertions (gap fixes D12 and D14 + the chart-vs-binary env
# check, 2026-10-12, and the real runtime of ADR 0011: Postgres and Kafka are
# the only runtime, so the mandatory configuration has no silent default, no
# permissive/off/log mode is ever rendered and the env the binaries read is the
# env the chart renders).
# Run: bash charts/slotting-optimization/tests/render_test.sh   (needs helm)
set -euo pipefail

chart_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_dir="$(cd "$chart_dir/../.." && pwd)"
release=slotting-optimization
# Dummy values, no password: the chart refuses to render without a database
# source, Kafka brokers and the four consumer groups (ADR 0011). None is ever
# dialled.
groups=(demandConsumerGroup:slotting-optimization-demand
  productConsumerGroup:slotting-optimization-product
  layoutConsumerGroup:slotting-optimization-layout
  relocationConsumerGroup:slotting-optimization-relocation)
base=(--set database.url=postgres://u@example.invalid:5432/db
  --set kafka.brokers=kafka.example.invalid:9092)
for g in "${groups[@]}"; do base+=(--set "config.${g%%:*}=${g#*:}"); done
# Every optional value set (ct lint's values file), so every env var a binary
# can read is rendered.
everything=(-f "$chart_dir/ci/all-components-values.yaml")

fail() { echo "FAIL: $*" >&2; exit 1; }

configmap="$(helm template "$release" "$chart_dir" "${base[@]}" --show-only templates/configmap.yaml)"
api_deployment="$(helm template "$release" "$chart_dir" "${base[@]}" --show-only templates/deployment.yaml)"
configmap_name="$(awk '/^  name:/ {print $2; exit}' <<<"$configmap")"
[[ -n "$configmap_name" ]] || fail "could not read the ConfigMap name"

# D14: every config.* value is rendered into the chart ConfigMap, and the api
# Deployment -- the only workload whose binary (cmd/api) reads those keys --
# must mount it with envFrom, or a value that is only in the ConfigMap
# silently does nothing.
grep -A3 'envFrom:' <<<"$api_deployment" | grep -q 'configMapRef:' \
  || fail "api Deployment does not mount the ConfigMap via envFrom"
grep -A3 'envFrom:' <<<"$api_deployment" | grep -q "name: $configmap_name" \
  || fail "api Deployment envFrom does not reference ConfigMap $configmap_name"

# A tuned config.* value reaches the ConfigMap the api mounts.
tuned="$(helm template "$release" "$chart_dir" "${base[@]}" --set config.outboxRelayInterval=7s --set config.outboxMaxAttempts=5 --show-only templates/configmap.yaml)"
grep -q 'OUTBOX_RELAY_INTERVAL: "7s"' <<<"$tuned" || fail "config.outboxRelayInterval is not rendered into the ConfigMap"
grep -q 'OUTBOX_MAX_ATTEMPTS: "5"' <<<"$tuned" || fail "config.outboxMaxAttempts is not rendered into the ConfigMap"

# D12 / ADR 0008: the relay attempt cap defaults to 20 and 0 (unlimited) is
# rendered as is.
grep -q 'OUTBOX_MAX_ATTEMPTS: "20"' <<<"$configmap" || fail "OUTBOX_MAX_ATTEMPTS must default to 20 (ADR 0008)"
grep -A1 'name: OUTBOX_MAX_ATTEMPTS' <<<"$api_deployment" | grep -q 'value: "20"' \
  || fail "api Deployment does not set OUTBOX_MAX_ATTEMPTS"
unlimited="$(helm template "$release" "$chart_dir" "${base[@]}" --set config.outboxMaxAttempts=0 --show-only templates/configmap.yaml)"
grep -q 'OUTBOX_MAX_ATTEMPTS: "0"' <<<"$unlimited" || fail "config.outboxMaxAttempts=0 is not rendered"

# The mcp Deployment reads its own explicit env and must NOT inherit the
# api's ConfigMap (it would bind nothing new but carry the api's relay and
# consumer settings into a read-only binary).
if helm template "$release" "$chart_dir" "${everything[@]}" --show-only templates/mcp-deployment.yaml | grep -q 'configMapRef:'; then
  fail "mcp-deployment.yaml must not mount the api ConfigMap"
fi

# Chart vs binary: every env var a binary reads must be rendered into its
# Deployment when every optional value is set. The names come from the
# binary's own os.Getenv / getenv("...") / env("...") literals, so a new variable in
# cmd/<binary>/main.go fails this test until the chart renders it; the
# extras are read through a variable (the *_MODE / *_CONSUMER_GROUP specs)
# or by a shared internal package (ENVIRONMENT in telemetry,
# CORS_ALLOWED_ORIGINS in the HTTP adapter).
binary_env() {
  grep -ohE '(Getenv|getenv|env)\("[A-Z][A-Z0-9_]*"' "$repo_dir/cmd/$1/main.go" | sed -E 's/.*\("//; s/"$//' | sort -u
}
assert_env_rendered() {
  local template="$1"; shift
  local rendered
  rendered="$(helm template "$release" "$chart_dir" "${everything[@]}" --show-only "templates/$template")"
  for var in "$@"; do
    grep -q "name: $var\$" <<<"$rendered" || fail "$template does not render $var, which the binary reads"
  done
}
# Word splitting is safe: the names are [A-Z0-9_] only (bash 3.2 has no mapfile).
# shellcheck disable=SC2207
api_vars=($(binary_env api))
# shellcheck disable=SC2207
mcp_vars=($(binary_env mcp))
# shellcheck disable=SC2207
projector_vars=($(binary_env slotting-projector))
# shellcheck disable=SC2207
reports_vars=($(binary_env slotting-reports))
(( ${#api_vars[@]} >= 10 )) || fail "found only ${#api_vars[@]} env literals in cmd/api/main.go; the scan is looking in the wrong place"
(( ${#mcp_vars[@]} >= 5 )) || fail "found only ${#mcp_vars[@]} env literals in cmd/mcp/main.go; the scan is looking in the wrong place"
(( ${#projector_vars[@]} >= 4 )) || fail "found only ${#projector_vars[@]} env literals in cmd/slotting-projector/main.go; the scan is looking in the wrong place"
(( ${#reports_vars[@]} >= 4 )) || fail "found only ${#reports_vars[@]} env literals in cmd/slotting-reports/main.go; the scan is looking in the wrong place"
assert_env_rendered deployment.yaml "${api_vars[@]}" \
  DEMAND_MODE DEMAND_CONSUMER_GROUP PRODUCT_MODE PRODUCT_CONSUMER_GROUP LAYOUT_MODE LAYOUT_CONSUMER_GROUP \
  RELOCATION_MODE RELOCATION_CONSUMER_GROUP ENVIRONMENT CORS_ALLOWED_ORIGINS
assert_env_rendered mcp-deployment.yaml "${mcp_vars[@]}" ENVIRONMENT
# The analytics binaries (ADR 0006): ENVIRONMENT is read by the shared
# telemetry adapter, and the projector reads ANALYTICS_CONSUMER_GROUP,
# ADMIN_ADDR and LOG_LEVEL through a variable (loadConfig's or()), so they
# are asserted on top of the scanned literals.
assert_env_rendered projector-deployment.yaml "${projector_vars[@]}" ENVIRONMENT ANALYTICS_CONSUMER_GROUP ADMIN_ADDR LOG_LEVEL
assert_env_rendered reports-deployment.yaml "${reports_vars[@]}" ENVIRONMENT LOG_LEVEL

# Every key the ConfigMap renders is a variable cmd/api reads (no dead keys).
full_configmap="$(helm template "$release" "$chart_dir" "${everything[@]}" --show-only templates/configmap.yaml)"
api_known=" ${api_vars[*]} DEMAND_MODE DEMAND_CONSUMER_GROUP PRODUCT_MODE PRODUCT_CONSUMER_GROUP LAYOUT_MODE LAYOUT_CONSUMER_GROUP RELOCATION_MODE RELOCATION_CONSUMER_GROUP ENVIRONMENT CORS_ALLOWED_ORIGINS "
while read -r key; do
  [[ "$api_known" == *" $key "* ]] || fail "ConfigMap key $key is read by no binary"
done < <(awk '/^data:/ {d=1; next} d && /^  [A-Z]/ {sub(/:.*/, ""); sub(/^  /, ""); print}' <<<"$full_configmap")

# --- ADR 0011: the mandatory configuration is rendered from values -----------------
env_value() { # env_value <rendered> <NAME>
  grep -A1 "name: $2\$" <<<"$1" | grep -m1 'value:' | sed -E 's/.*value: "?//; s/"$//'
}
[[ "$(env_value "$api_deployment" KAFKA_BROKERS)" == "kafka.example.invalid:9092" ]] || fail "KAFKA_BROKERS is not rendered from kafka.brokers"
grep -q '^  KAFKA_BROKERS: "kafka.example.invalid:9092"' <<<"$configmap" || fail "the ConfigMap does not carry KAFKA_BROKERS"
for kv in 'EVENT_PUBLISHER=kafka' 'DEMAND_MODE=kafka' 'PRODUCT_MODE=kafka' 'LAYOUT_MODE=kafka' 'RELOCATION_MODE=kafka' \
  'DEMAND_CONSUMER_GROUP=slotting-optimization-demand' 'PRODUCT_CONSUMER_GROUP=slotting-optimization-product' \
  'LAYOUT_CONSUMER_GROUP=slotting-optimization-layout' 'RELOCATION_CONSUMER_GROUP=slotting-optimization-relocation'; do
  [[ "$(env_value "$api_deployment" "${kv%%=*}")" == "${kv#*=}" ]] || fail "the api Deployment does not render $kv"
  grep -q "^  ${kv%%=*}: \"${kv#*=}\"" <<<"$configmap" || fail "the ConfigMap does not carry $kv"
done
grep -A4 'name: DATABASE_URL$' <<<"$api_deployment" | grep -q 'secretKeyRef:' || fail "DATABASE_URL must come from a Secret (secretKeyRef)"
if grep -qE '^  (MIGRATIONS_)?DATABASE_URL:' <<<"$configmap"; then fail "a DSN must never land in the ConfigMap"; fi
render_all="$(helm template "$release" "$chart_dir" "${base[@]}" "${everything[@]}")"
if grep -qiE 'value: "?(permissive|log|off)"?$' <<<"$render_all"; then fail "the render carries permissive/log/off, which the binaries reject at boot"; fi
if grep -qiE 'in-memory|silently runs|permissive' <<<"$render_all"$'\n'"$(cat "$chart_dir/templates/NOTES.txt")"; then
  fail "the render or NOTES.txt mentions an in-memory / permissive mode"
fi
mcp_dep="$(helm template "$release" "$chart_dir" "${base[@]}" --set mcp.enabled=true --show-only templates/mcp-deployment.yaml)"
grep -A4 'name: DATABASE_URL$' <<<"$mcp_dep" | grep -q 'secretKeyRef:' || fail "the mcp DATABASE_URL must come from a Secret"

# --- chart vs binary: every mandatory variable a binary names is rendered -----------
# A new `errors.New("X is required ...")` in a boot config fails here until the
# chart carries X for that binary (the api's consumer groups are named through a
# format string, so they are listed above).
analytics=(--set analytics.enabled=true --set analytics.url=postgres://u@example.invalid:5432/analytics)
projector_dep="$(helm template "$release" "$chart_dir" "${base[@]}" "${analytics[@]}" --show-only templates/projector-deployment.yaml)"
reports_dep="$(helm template "$release" "$chart_dir" "${base[@]}" "${analytics[@]}" --show-only templates/reports-deployment.yaml)"
for bin in api mcp slotting-projector slotting-reports; do
  case "$bin" in
    api) rendered="$api_deployment" ;;
    mcp) rendered="$mcp_dep" ;;
    slotting-projector) rendered="$projector_dep" ;;
    *) rendered="$reports_dep" ;;
  esac
  vars="$(grep -ho 'errors.New("[A-Z_]* is required' "$repo_dir"/cmd/"$bin"/main.go | sed -E 's/errors.New\("//; s/ is required//' | sort -u)"
  [[ -n "$vars" ]] || fail "found no mandatory variables in cmd/$bin; the scan is looking in the wrong place"
  for var in $vars; do
    grep -q "name: $var\$" <<<"$rendered" || fail "the $bin Deployment does not render $var, which cmd/$bin requires"
  done
done

# --- refusals: the mandatory values name themselves ---------------------------------
expect_refusal() { # expect_refusal <why> <needle> <helm args...>
  local why="$1" needle="$2" err; shift 2
  if err="$(helm template "$release" "$chart_dir" "$@" 2>&1)"; then
    fail "the chart must refuse to render: $why"
  fi
  grep -qF -- "$needle" <<<"$err" || fail "the refusal for '$why' does not name '$needle':
$err"
}
# args that satisfy everything except the one value a case removes.
mandatory_except() { # mandatory_except <database|kafka|<group key>>
  local skip="$1" out=()
  [[ "$skip" == database ]] || out+=(--set database.url=postgres://u@h/db)
  [[ "$skip" == kafka ]] || out+=(--set kafka.brokers=k:9092)
  for g in "${groups[@]}"; do
    [[ "${g%%:*}" == "$skip" ]] || out+=(--set "config.${g%%:*}=${g#*:}")
  done
  printf '%s\n' "${out[@]}"
}
refusal_without() { # refusal_without <value to leave out> <needle>
  local args=()
  while IFS= read -r line; do args+=("$line"); done < <(mandatory_except "$1")
  expect_refusal "no $1" "$2" "${args[@]}"
}
refusal_without database "database.url or database.existingSecret"
refusal_without kafka "kafka.brokers"
for g in "${groups[@]}"; do refusal_without "${g%%:*}" "config.${g%%:*}"; done
expect_refusal "kafka switched off" "Kafka cannot be switched off" "${base[@]}" --set kafka.enabled=false
expect_refusal "the log publisher" "config.eventPublisher" "${base[@]}" --set config.eventPublisher=log
for key in demandMode productMode layoutMode relocationMode; do
  expect_refusal "permissive $key" "config.$key" "${base[@]}" --set "config.$key=permissive"
  expect_refusal "off $key" "config.$key" "${base[@]}" --set "config.$key=off"
done
expect_refusal "the log publisher through extraEnv" "EVENT_PUBLISHER" "${base[@]}" --set 'extraEnv[0].name=EVENT_PUBLISHER' --set 'extraEnv[0].value=log'
expect_refusal "a *_MODE through extraEnv" "_MODE" "${base[@]}" --set 'extraEnv[0].name=DEMAND_MODE' --set 'extraEnv[0].value=permissive'
expect_refusal "a *_MODE through mcp.extraEnv" "_MODE" "${base[@]}" --set mcp.enabled=true --set 'mcp.extraEnv[0].name=LAYOUT_MODE' --set 'mcp.extraEnv[0].value=off'
expect_refusal "a grace period under 20s" "terminationGracePeriodSeconds" "${base[@]}" --set terminationGracePeriodSeconds=19
expect_refusal "no grace period" "terminationGracePeriodSeconds" "${base[@]}" --set terminationGracePeriodSeconds=0
helm template "$release" "$chart_dir" "${base[@]}" --set terminationGracePeriodSeconds=20 >/dev/null \
  || fail "a 20s grace period must still render"
expect_refusal "mcp without a database" "database.url or database.existingSecret" --set mcp.enabled=true --set kafka.brokers=k:9092
expect_refusal "an out-of-range lookbackDays" "config.lookbackDays" "${base[@]}" --set config.lookbackDays=400
expect_refusal "analytics without a DSN source" "analytics.url" "${base[@]}" --set analytics.enabled=true
# kafka.enabled=true (what warehouse-infra still sets) keeps rendering.
helm template "$release" "$chart_dir" "${base[@]}" --set kafka.enabled=true >/dev/null \
  || fail "kafka.enabled=true must keep rendering (warehouse-infra sets it)"

# ADR 0006: the analytics read side. Off by default (neither component
# renders); with analytics on, the projector Deployment renders WITHOUT a
# Service (admin port only) and the reports Deployment + its Service render;
# the projector never sees the api's OLTP env and gets the platform brokers.
default_render="$(helm template "$release" "$chart_dir" "${base[@]}")"
if grep -q 'app.kubernetes.io/component: analytics-projector' <<<"$default_render"; then
  fail "the projector must not render while analytics.enabled is false"
fi
if grep -q 'app.kubernetes.io/component: analytics-reports' <<<"$default_render"; then
  fail "the reports component must not render while analytics.enabled is false"
fi
grep -q '/app/slotting-projector' <<<"$projector_dep" || fail "the projector Deployment must run /app/slotting-projector"
if grep -q 'kind: Service' <<<"$projector_dep"; then
  fail "the projector must not render a Service (its admin port is probes-only)"
fi
if grep -Eq 'name: (DATABASE_URL|EVENT_PUBLISHER|DEMAND_MODE)$' <<<"$projector_dep"; then
  fail "the projector Deployment must not get the api's OLTP env (it never opens the OLTP database)"
fi
[[ "$(env_value "$projector_dep" KAFKA_BROKERS)" == "kafka.example.invalid:9092" ]] || fail "the projector KAFKA_BROKERS is not rendered from kafka.brokers"
grep -q '/app/slotting-reports' <<<"$reports_dep" || fail "the reports Deployment must run /app/slotting-reports"
grep -q 'kind: Service' <<<"$reports_dep" || fail "the reports Service was not rendered with analytics.enabled=true"
if grep -q 'name: KAFKA_BROKERS' <<<"$reports_dep"; then
  fail "the reports Deployment must not get KAFKA_BROKERS (it never dials Kafka)"
fi

# The web remote (slotting_mfe): off by default; with frontend.enabled=true
# its own Deployment + ClusterIP Service render (component=frontend), it gets
# none of the api's OLTP/Kafka env (it serves static bytes), and this chart
# renders no route for it (warehouse-infra's Nginx web gateway owns
# /mfes/slotting-optimization/).
if grep -q 'app.kubernetes.io/component: frontend' <<<"$default_render"; then
  fail "the frontend must not render while frontend.enabled is false"
fi
frontend_dep="$(helm template "$release" "$chart_dir" "${base[@]}" --set frontend.enabled=true --show-only templates/frontend-deployment.yaml)"
frontend_svc="$(helm template "$release" "$chart_dir" "${base[@]}" --set frontend.enabled=true --show-only templates/frontend-service.yaml)"
grep -q 'app.kubernetes.io/component: frontend' <<<"$frontend_dep" || fail "the frontend Deployment was not rendered with frontend.enabled=true"
grep -q 'app.kubernetes.io/component: frontend' <<<"$frontend_svc" || fail "the frontend Service was not rendered with frontend.enabled=true"
grep -q 'type: ClusterIP' <<<"$frontend_svc" || fail "the frontend Service must stay ClusterIP"
if grep -Eq 'name: (DATABASE_URL|KAFKA_BROKERS)$' <<<"$frontend_dep"; then
  fail "the frontend Deployment must not get database/Kafka env (it serves static bytes)"
fi

# ALL optional components render TOGETHER (ct lint's values file): api + mcp +
# projector + reports + frontend, and no route in this chart targets the
# frontend.
together="$(helm template "$release" "$chart_dir" "${base[@]}" "${everything[@]}")"
for component in api mcp analytics-projector analytics-reports frontend; do
  grep -q "app.kubernetes.io/component: $component" <<<"$together" \
    || fail "every optional component must render together; $component is missing"
done
if grep -E 'kind: (Ingress|HTTPRoute)' <<<"$together" | grep -q frontend; then
  fail "frontend routing must not live in this chart (the Nginx web gateway owns it)"
fi

# --- what warehouse-infra sets keeps working -----------------------------------------
# tests/warehouse-infra-platform-values.yaml, tests/warehouse-infra-values.yaml and
# tests/warehouse-infra-kind-resources-values.yaml are verbatim copies of
# helm-values/_platform.yaml, helm-values/slotting-optimization.yaml and
# helm-values/kind-resources/slotting-optimization.yaml of warehouse-infra
# (origin/develop), layered in the order ArgoCD uses: kafka.enabled=true,
# eventPublisher=kafka, the four modes kafka with their groups, the existing
# database and analytics secrets, mcp on, empty extraEnv and the platform
# brokers. Infra needs NO values change for ADR 0011.
infra=(-f "$chart_dir/tests/warehouse-infra-platform-values.yaml" -f "$chart_dir/tests/warehouse-infra-values.yaml"
  -f "$chart_dir/tests/warehouse-infra-kind-resources-values.yaml" --set image.tag=local-test)
infra_all="$(helm template "$release" "$chart_dir" "${infra[@]}")" || fail "the warehouse-infra values no longer render"
infra_api="$(helm template "$release" "$chart_dir" "${infra[@]}" --show-only templates/deployment.yaml)"
infra_mcp="$(helm template "$release" "$chart_dir" "${infra[@]}" --show-only templates/mcp-deployment.yaml)"
infra_proj="$(helm template "$release" "$chart_dir" "${infra[@]}" --show-only templates/projector-deployment.yaml)"
# Every mandatory variable of every binary is provided by the infra values.
for var in $(grep -ho 'errors.New("[A-Z_]* is required' "$repo_dir"/cmd/api/main.go | sed -E 's/errors.New\("//; s/ is required//' | sort -u) \
  DEMAND_CONSUMER_GROUP PRODUCT_CONSUMER_GROUP LAYOUT_CONSUMER_GROUP RELOCATION_CONSUMER_GROUP; do
  [[ -n "$(env_value "$infra_api" "$var")" ]] || grep -A4 "name: $var\$" <<<"$infra_api" | grep -q 'secretKeyRef:' \
    || fail "infra values: the api Deployment gets no $var"
done
[[ "$(env_value "$infra_api" KAFKA_BROKERS)" == "kafka.warehouse-systems.svc.cluster.local:9092" ]] || fail "infra values: KAFKA_BROKERS"
[[ "$(env_value "$infra_api" EVENT_PUBLISHER)" == "kafka" ]] || fail "infra values: EVENT_PUBLISHER"
for kv in DEMAND_CONSUMER_GROUP=slotting-optimization-demand PRODUCT_CONSUMER_GROUP=slotting-optimization-product \
  LAYOUT_CONSUMER_GROUP=slotting-optimization-layout RELOCATION_CONSUMER_GROUP=slotting-optimization-relocation; do
  [[ "$(env_value "$infra_api" "${kv%%=*}")" == "${kv#*=}" ]] || fail "infra values: ${kv%%=*}"
done
for mode in DEMAND_MODE PRODUCT_MODE LAYOUT_MODE RELOCATION_MODE; do
  [[ "$(env_value "$infra_api" "$mode")" == "kafka" ]] || fail "infra values: $mode"
done
grep -A4 'name: DATABASE_URL$' <<<"$infra_api" | grep -q 'slotting-optimization-db' || fail "infra values: DATABASE_URL must come from the slotting-optimization-db secret"
grep -A4 'name: DATABASE_URL$' <<<"$infra_mcp" | grep -q 'slotting-optimization-db' || fail "infra values: the mcp DATABASE_URL must come from the slotting-optimization-db secret"
[[ "$(env_value "$infra_proj" KAFKA_BROKERS)" == "kafka.warehouse-systems.svc.cluster.local:9092" ]] || fail "infra values: the projector KAFKA_BROKERS"
[[ "$(env_value "$infra_proj" ANALYTICS_CONSUMER_GROUP)" == "slotting-optimization-analytics" ]] || fail "infra values: ANALYTICS_CONSUMER_GROUP"
grep -A4 'name: ANALYTICS_DATABASE_URL$' <<<"$infra_proj" | grep -q 'slotting-optimization-analytics-db' || fail "infra values: the projector DSN must come from slotting-optimization-analytics-db"
for component in api mcp analytics-projector analytics-reports; do
  grep -q "app.kubernetes.io/component: $component" <<<"$infra_all" || fail "infra values: component $component not rendered"
done
if grep -qiE 'value: "?(permissive|log|off)"?$' <<<"$infra_all"; then fail "infra values: the render carries permissive/log/off"; fi

echo "PASS: slotting-optimization chart render assertions"
