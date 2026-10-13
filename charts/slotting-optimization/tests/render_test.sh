#!/usr/bin/env bash
# Chart render assertions (gap fixes D12 and D14 + the chart-vs-binary env
# check, 2026-10-12).
# Run: bash charts/slotting-optimization/tests/render_test.sh   (needs helm)
set -euo pipefail

chart_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_dir="$(cd "$chart_dir/../.." && pwd)"
release=slotting-optimization
# Dummy DSN, no password: the chart refuses to render without a database source.
base=(--set database.url=postgres://u@example.invalid:5432/db)
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
# binary's own os.Getenv / getenv("...") literals, so a new variable in
# cmd/<binary>/main.go fails this test until the chart renders it; the
# extras are read through a variable (the *_MODE / *_CONSUMER_GROUP specs)
# or by a shared internal package (ENVIRONMENT in telemetry,
# CORS_ALLOWED_ORIGINS in the HTTP adapter).
binary_env() {
  grep -ohE '[Gg]etenv\("[A-Z][A-Z0-9_]*"' "$repo_dir/cmd/$1/main.go" | sed -E 's/.*\("//; s/"$//' | sort -u
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
(( ${#api_vars[@]} >= 10 )) || fail "found only ${#api_vars[@]} env literals in cmd/api/main.go; the scan is looking in the wrong place"
(( ${#mcp_vars[@]} >= 5 )) || fail "found only ${#mcp_vars[@]} env literals in cmd/mcp/main.go; the scan is looking in the wrong place"
assert_env_rendered deployment.yaml "${api_vars[@]}" \
  DEMAND_MODE DEMAND_CONSUMER_GROUP PRODUCT_MODE PRODUCT_CONSUMER_GROUP LAYOUT_MODE LAYOUT_CONSUMER_GROUP \
  ENVIRONMENT CORS_ALLOWED_ORIGINS
assert_env_rendered mcp-deployment.yaml "${mcp_vars[@]}" ENVIRONMENT

# Every key the ConfigMap renders is a variable cmd/api reads (no dead keys).
full_configmap="$(helm template "$release" "$chart_dir" "${everything[@]}" --show-only templates/configmap.yaml)"
api_known=" ${api_vars[*]} DEMAND_MODE DEMAND_CONSUMER_GROUP PRODUCT_MODE PRODUCT_CONSUMER_GROUP LAYOUT_MODE LAYOUT_CONSUMER_GROUP ENVIRONMENT CORS_ALLOWED_ORIGINS "
while read -r key; do
  [[ "$api_known" == *" $key "* ]] || fail "ConfigMap key $key is read by no binary"
done < <(awk '/^data:/ {d=1; next} d && /^  [A-Z]/ {sub(/:.*/, ""); sub(/^  /, ""); print}' <<<"$full_configmap")

echo "PASS: slotting-optimization chart render assertions"
