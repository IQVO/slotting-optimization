#!/usr/bin/env python3
"""Assert this chart's Service selectors isolate each component.

Why this exists (fleet-wide): `app.kubernetes.io/name` + `instance` are identical
on every pod a release creates (api, mcp). A Service that selects on those two
alone selects ALL of them -- verified live in other fleet charts, where a Kong
request for an api /healthz was answered by the wrong pod. Every Deployment here
carries `app.kubernetes.io/component` in selector.matchLabels and its pod
labels, and every Service selects on it, so each Service selects EXACTLY ONE
Deployment. This test fails if that ever stops being true.

It mirrors inbound-receiving's charts/.../tests/test_service_selectors.py (and
warehouse-infra's scripts/check-chart-selectors.py), restricted to the
components this chart has: api (always), mcp (optional, default off), the
analytics read side of ADR 0006 -- analytics-projector and analytics-reports
-- and the frontend (optional, default off), run with every other component
enabled TOGETHER. The frontend is the nginx pod serving the slotting_mfe
remote: its own workload, component=frontend, a ClusterIP Service, never
routed by this chart (warehouse-infra's Nginx web gateway owns
/mfes/slotting-optimization/).

Run: python3 charts/slotting-optimization/tests/test_service_selectors.py
Needs: helm, PyYAML.
"""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

CHART_DIR = Path(__file__).resolve().parents[1]
RELEASE = "slotting-optimization"

# Dummy values, no password: the chart refuses to render without a database
# source, the Kafka brokers and the four consumer groups (ADR 0011: Postgres
# and Kafka are the only runtime). None is ever dialled.
GROUPS = {
    "demandConsumerGroup": "slotting-optimization-demand",
    "productConsumerGroup": "slotting-optimization-product",
    "layoutConsumerGroup": "slotting-optimization-layout",
    "relocationConsumerGroup": "slotting-optimization-relocation",
}


def runtime(skip: str | None = None) -> list[str]:
    """The mandatory values, minus the one named by skip (database, kafka or a group key)."""
    args: list[str] = []
    if skip != "database":
        args += ["--set", "database.url=postgres://u@example.invalid:5432/db"]
    if skip != "kafka":
        args += ["--set", "kafka.brokers=kafka.example.invalid:9092"]
    for key, value in GROUPS.items():
        if skip != key:
            args += ["--set", f"config.{key}={value}"]
    return args


BASE = runtime()
ENABLE_EVERYTHING = BASE + [
    "--set", "mcp.enabled=true",
    "--set", "frontend.enabled=true",
    "--set", "autoscaling.api.enabled=true",
    "--set", "config.demandSiteId=SITE-1",
    "--set", "config.defaultSiteId=SITE-1",
    "--set", "config.lookbackDays=28",
    "--set", "config.forwardZoneCodes=FWD\\,FWD2",
    "--set", "kafka.enabled=true",
    "--set", "gatewayApi.enabled=true",
    "--set", "ingress.enabled=true",
    # ADR 0006: the analytics read side ON together with everything else.
    "--set", "analytics.enabled=true",
    "--set", "analytics.url=postgres://u@example.invalid:5432/analytics",
    "--set", "analytics.consumerGroup=slotting-optimization-analytics",
]

# Every env var cmd/api/main.go reads that has a dedicated chart value.
API_ENV = (
    "HTTP_ADDR", "LOG_LEVEL", "EVENT_PUBLISHER", "KAFKA_BROKERS", "DATABASE_URL", "MIGRATIONS_DATABASE_URL",
    "DEMAND_SITE_ID", "DEFAULT_SITE_ID", "LOOKBACK_DAYS", "FORWARD_ZONE_CODES",
    "DEMAND_MODE", "DEMAND_CONSUMER_GROUP", "PRODUCT_MODE", "PRODUCT_CONSUMER_GROUP",
    "LAYOUT_MODE", "LAYOUT_CONSUMER_GROUP", "RELOCATION_MODE", "RELOCATION_CONSUMER_GROUP",
    "ENVIRONMENT", "SERVICE_VERSION", "OTEL_EXPORTER_OTLP_ENDPOINT",
    "OUTBOX_MAX_ATTEMPTS",
)


def helm_template(extra_args: list[str]) -> subprocess.CompletedProcess:
    return subprocess.run(
        ["helm", "template", RELEASE, str(CHART_DIR), *extra_args],
        capture_output=True, text=True,
    )


def render(extra_args: list[str]) -> list[dict]:
    result = helm_template(extra_args)
    if result.returncode != 0:
        raise SystemExit(f"FAIL: helm template {' '.join(extra_args)}:\n{result.stderr}")
    try:
        import yaml  # type: ignore
    except ModuleNotFoundError:  # pragma: no cover - environment guard
        print("SKIP: PyYAML not available; cannot assert selectors", file=sys.stderr)
        raise SystemExit(0)
    return [d for d in yaml.safe_load_all(result.stdout) if d]


def selector_of(doc: dict) -> dict:
    return doc.get("spec", {}).get("selector") or {}


def pod_labels_of(doc: dict) -> dict:
    return doc.get("spec", {}).get("template", {}).get("metadata", {}).get("labels") or {}


def matches(selector: dict, labels: dict) -> bool:
    return bool(selector) and all(labels.get(k) == v for k, v in selector.items())


def env_of(dep: dict) -> list[dict]:
    return dep["spec"]["template"]["spec"]["containers"][0].get("env", [])


def env_names(dep: dict) -> list[str]:
    return [e["name"] for e in env_of(dep)]


def env_value(dep: dict, name: str) -> str | None:
    return next((e.get("value") for e in env_of(dep) if e["name"] == name), None)


def check_components(docs: list[dict], failures: list[str]) -> None:
    services = {d["metadata"]["name"]: d for d in docs if d.get("kind") == "Service"}
    deployments = {d["metadata"]["name"]: d for d in docs if d.get("kind") == "Deployment"}

    if RELEASE not in services:
        failures.append("the api Service was not rendered")
    elif selector_of(services[RELEASE]).get("app.kubernetes.io/component") != "api":
        failures.append("the api Service selector must pin component=api")

    mcp = f"{RELEASE}-mcp"
    if mcp not in services:
        failures.append("the MCP Service was not rendered with mcp.enabled=true")
    elif selector_of(services[mcp]).get("app.kubernetes.io/component") != "mcp":
        failures.append("the MCP Service selector must pin component=mcp")
    if mcp not in deployments:
        failures.append("the MCP Deployment was not rendered with mcp.enabled=true")
    else:
        container = deployments[mcp]["spec"]["template"]["spec"]["containers"][0]
        if container.get("command") != ["/app/mcp"]:
            failures.append("the MCP Deployment must run /app/mcp")
        mcp_env = set(env_names(deployments[mcp]))
        stray = {
            "KAFKA_BROKERS", "EVENT_PUBLISHER", "OUTBOX_RELAY_INTERVAL", "OUTBOX_MAX_ATTEMPTS",
            "DEMAND_MODE", "DEMAND_CONSUMER_GROUP", "PRODUCT_MODE", "PRODUCT_CONSUMER_GROUP",
            "LAYOUT_MODE", "LAYOUT_CONSUMER_GROUP", "LOOKBACK_DAYS", "FORWARD_ZONE_CODES",
        } & mcp_env
        if stray:
            failures.append(f"the MCP Deployment must not get Kafka/relay/consumer/planning env (it never dials Kafka): {sorted(stray)}")
        for want in ("DATABASE_URL", "MIGRATIONS_DATABASE_URL", "MCP_ADDR", "DEMAND_SITE_ID"):
            if want not in mcp_env:
                failures.append(f"the MCP Deployment does not render {want}")

    # The web remote (slotting_mfe): its own Deployment + ClusterIP Service,
    # component=frontend, none of the api's OLTP/Kafka env, and no route from
    # this chart (warehouse-infra's Nginx web gateway owns /mfes/slotting-optimization/).
    frontend = f"{RELEASE}-frontend"
    if frontend not in services:
        failures.append("the frontend Service was not rendered with frontend.enabled=true")
    else:
        if selector_of(services[frontend]).get("app.kubernetes.io/component") != "frontend":
            failures.append("the frontend Service selector must pin component=frontend")
        if services[frontend]["spec"].get("type") != "ClusterIP":
            failures.append("the frontend Service must be ClusterIP")
    if frontend not in deployments:
        failures.append("the frontend Deployment was not rendered with frontend.enabled=true")
    elif {"DATABASE_URL", "KAFKA_BROKERS"} & set(env_names(deployments[frontend])):
        failures.append("the frontend Deployment must not get database/Kafka env (it serves static bytes)")
    for d in docs:
        if d.get("kind") in {"Ingress", "HTTPRoute"} and "frontend" in d["metadata"]["name"]:
            failures.append(f"{d['kind']} {d['metadata']['name']}: frontend routing must not live in this chart")

    # ADR 0006: the analytics projector (writer) and reports (reader).
    projector, reports = f"{RELEASE}-projector", f"{RELEASE}-reports"
    if projector not in deployments:
        failures.append("the projector Deployment was not rendered with analytics.enabled=true")
    else:
        container = deployments[projector]["spec"]["template"]["spec"]["containers"][0]
        if container.get("command") != ["/app/slotting-projector"]:
            failures.append("the projector Deployment must run /app/slotting-projector")
        projector_env = set(env_names(deployments[projector]))
        # The projector never opens the OLTP database and runs none of the
        # api's consumers; only its own env may reach it.
        stray = {
            "DATABASE_URL", "MIGRATIONS_DATABASE_URL", "EVENT_PUBLISHER", "OUTBOX_RELAY_INTERVAL",
            "OUTBOX_MAX_ATTEMPTS", "DEMAND_MODE", "DEMAND_CONSUMER_GROUP", "PRODUCT_MODE",
            "PRODUCT_CONSUMER_GROUP", "LAYOUT_MODE", "LAYOUT_CONSUMER_GROUP", "RELOCATION_MODE",
            "RELOCATION_CONSUMER_GROUP", "LOOKBACK_DAYS", "FORWARD_ZONE_CODES",
        } & projector_env
        if stray:
            failures.append(f"the projector Deployment must not get the api's OLTP env: {sorted(stray)}")
        for want in ("ANALYTICS_DATABASE_URL", "KAFKA_BROKERS", "ANALYTICS_CONSUMER_GROUP", "ADMIN_ADDR", "ENVIRONMENT"):
            if want not in projector_env:
                failures.append(f"the projector Deployment does not render {want}")
    if reports not in deployments:
        failures.append("the reports Deployment was not rendered with analytics.enabled=true")
    else:
        container = deployments[reports]["spec"]["template"]["spec"]["containers"][0]
        if container.get("command") != ["/app/slotting-reports"]:
            failures.append("the reports Deployment must run /app/slotting-reports")
        reports_env = set(env_names(deployments[reports]))
        # The reports binary never dials Kafka and never opens OLTP.
        stray = {
            "KAFKA_BROKERS", "DATABASE_URL", "MIGRATIONS_DATABASE_URL", "EVENT_PUBLISHER",
        } & reports_env
        if stray:
            failures.append(f"the reports Deployment must not get Kafka/OLTP env (it reads the analytical database only): {sorted(stray)}")
        for want in ("ANALYTICS_DATABASE_URL", "HTTP_ADDR", "ENVIRONMENT"):
            if want not in reports_env:
                failures.append(f"the reports Deployment does not render {want}")
    if reports not in services:
        failures.append("the reports Service was not rendered with analytics.enabled=true")
    elif selector_of(services[reports]).get("app.kubernetes.io/component") != "analytics-reports":
        failures.append("the reports Service selector must pin component=analytics-reports")
    if projector in services:
        failures.append("the projector must not render a Service (its admin port is probes-only)")

    # Every Deployment's own selector must pin a component too, and be
    # satisfied by its pod labels.
    for name, dep in deployments.items():
        match_labels = dep["spec"]["selector"].get("matchLabels") or {}
        if "app.kubernetes.io/component" not in match_labels:
            failures.append(f"Deployment {name} selector.matchLabels lacks app.kubernetes.io/component")
        if not matches(match_labels, pod_labels_of(dep)):
            failures.append(f"Deployment {name} pod labels do not satisfy its own selector")

    # The real invariant: each Service selects exactly one Deployment.
    for svc_name, svc in services.items():
        sel = selector_of(svc)
        hit = [d for d, dep in deployments.items() if matches(sel, pod_labels_of(dep))]
        if len(hit) != 1:
            failures.append(f"Service {svc_name} selects {len(hit)} Deployments {sorted(hit)}; expected exactly 1")

    # The api HPA owns replicas when enabled.
    hpa = next((d for d in docs if d.get("kind") == "HorizontalPodAutoscaler"), None)
    if hpa is None or hpa["spec"]["scaleTargetRef"]["name"] != RELEASE:
        failures.append("the api HPA was not rendered (or does not scale the api Deployment)")
    elif "replicas" in deployments.get(RELEASE, {}).get("spec", {}):
        failures.append("the api Deployment must omit replicas when its HPA owns them")

    api = deployments.get(RELEASE)
    if api is None:
        failures.append("the api Deployment was not rendered")
        return
    api_env = env_names(api)
    for want in API_ENV:
        if want not in api_env:
            failures.append(f"the api Deployment does not render {want} from its dedicated value")
    duplicates = sorted({n for n in api_env if api_env.count(n) > 1})
    if duplicates:
        failures.append(f"the api Deployment renders env vars twice: {duplicates}")
    for name, want in (("DEMAND_SITE_ID", "SITE-1"), ("LOOKBACK_DAYS", "28"), ("FORWARD_ZONE_CODES", "FWD,FWD2")):
        if env_value(api, name) != want:
            failures.append(f"the api Deployment renders {name}={env_value(api, name)!r}, want {want!r}")


def check_refusals(failures: list[str]) -> None:
    cases: list[tuple[str, list[str], str]] = [
        ("without a database source", runtime("database"), "requires database.url or database.existingSecret"),
        ("without kafka.brokers", runtime("kafka"), "kafka.brokers"),
        ("kafka switched off", BASE + ["--set", "kafka.enabled=false"], "Kafka cannot be switched off"),
        ("the log publisher", BASE + ["--set", "config.eventPublisher=log"], "the only publisher is"),
        ("an unknown publisher", BASE + ["--set", "config.eventPublisher=nats"], "the only publisher is"),
        ("EVENT_PUBLISHER=log through extraEnv",
         BASE + ["--set", "extraEnv[0].name=EVENT_PUBLISHER", "--set", "extraEnv[0].value=log"], "extraEnv sets EVENT_PUBLISHER=log"),
        ("a *_MODE through extraEnv",
         BASE + ["--set", "extraEnv[0].name=DEMAND_MODE", "--set", "extraEnv[0].value=permissive"], "extraEnv sets DEMAND_MODE"),
        ("a grace period under 20s", BASE + ["--set", "terminationGracePeriodSeconds=19"], "terminationGracePeriodSeconds"),
        ("LOOKBACK_DAYS out of range", BASE + ["--set", "config.lookbackDays=400"], "want 1..365"),
        ("analytics without a DSN source", BASE + ["--set", "analytics.enabled=true"],
         "neither analytics.url nor analytics.existingSecret"),
    ]
    for key in GROUPS:
        cases.append((f"without config.{key}", runtime(key), f"config.{key} is required"))
    for mode in ("demandMode", "productMode", "layoutMode", "relocationMode"):
        for bad in ("permissive", "off", "strict"):
            cases.append((f"{mode}={bad}", BASE + ["--set", f"config.{mode}={bad}"], f"config.{mode} is"))
    for label, args, needle in cases:
        result = helm_template(args)
        if result.returncode == 0 or needle not in result.stderr:
            failures.append(f"chart rendered (or failed for another reason) {label}")


def main() -> int:
    failures: list[str] = []

    check_components(render(ENABLE_EVERYTHING), failures)

    # Default values must not deploy the MCP or analytics components, an HPA
    # or a route.
    defaults = render(BASE)
    stray = [d["metadata"]["name"] for d in defaults if d["metadata"]["name"].endswith(("-mcp", "-frontend", "-projector", "-reports", "-analytics"))]
    stray += [d["kind"] for d in defaults if d.get("kind") in {"HorizontalPodAutoscaler", "Ingress", "HTTPRoute"}]
    if stray:
        failures.append(f"optional components rendered with default values: {stray}")
    dep = next(d for d in defaults if d.get("kind") == "Deployment")
    names = env_names(dep)
    off = {"DEMAND_SITE_ID", "DEFAULT_SITE_ID", "LOOKBACK_DAYS", "FORWARD_ZONE_CODES"}
    if off & set(names):
        failures.append(f"these must be off with default values: {sorted(off & set(names))}")
    # ADR 0011: the real runtime is always rendered, every mode is kafka.
    for mode in ("EVENT_PUBLISHER", "DEMAND_MODE", "PRODUCT_MODE", "LAYOUT_MODE", "RELOCATION_MODE"):
        if env_value(dep, mode) != "kafka":
            failures.append(f"{mode} must always be rendered as kafka, got {env_value(dep, mode)!r}")
    for want in ("KAFKA_BROKERS", *(k for k in API_ENV if k.endswith("_CONSUMER_GROUP"))):
        if want not in names:
            failures.append(f"{want} is mandatory and must always be rendered")

    check_refusals(failures)

    if failures:
        for f in failures:
            print(f"FAIL: {f}")
        return 1

    print("PASS: every Service selects exactly one Deployment (api, mcp, frontend, analytics-reports); mcp, frontend, "
          "analytics, HPA and routes are off by default; every cmd/api env var has a dedicated value; the chart refuses to render "
          "without a database source, brokers or any consumer group, with a log/unknown publisher or a permissive/off/unknown "
          "mode (also through extraEnv), with kafka switched off, with a grace period under 20s, with LOOKBACK_DAYS out of "
          "range, or with analytics on but no DSN source")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
