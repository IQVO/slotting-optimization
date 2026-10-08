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
components this chart has: api (always) and mcp (optional, default off).

Run: python3 charts/slotting-optimization/tests/test_service_selectors.py
Needs: helm, PyYAML.
"""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

CHART_DIR = Path(__file__).resolve().parents[1]
RELEASE = "slotting-optimization"

# Dummy DSN, no password: the chart refuses to render without a database source.
BASE = ["--set", "database.url=postgres://u@example.invalid:5432/db"]
ENABLE_EVERYTHING = BASE + [
    "--set", "mcp.enabled=true",
    "--set", "autoscaling.api.enabled=true",
    "--set", "config.eventPublisher=kafka",
    "--set", "config.demandSiteId=SITE-1",
    "--set", "config.defaultSiteId=SITE-1",
    "--set", "config.lookbackDays=28",
    "--set", "config.forwardZoneCodes=FWD\\,FWD2",
    "--set", "config.demandMode=kafka",
    "--set", "config.demandConsumerGroup=slotting-optimization-demand",
    "--set", "config.productMode=kafka",
    "--set", "config.productConsumerGroup=slotting-optimization-product",
    "--set", "config.layoutMode=kafka",
    "--set", "config.layoutConsumerGroup=slotting-optimization-layout",
    "--set", "kafka.enabled=true",
    "--set", "gatewayApi.enabled=true",
    "--set", "ingress.enabled=true",
]

# Every env var cmd/api/main.go reads that has a dedicated chart value.
API_ENV = (
    "HTTP_ADDR", "LOG_LEVEL", "EVENT_PUBLISHER", "KAFKA_BROKERS", "DATABASE_URL", "MIGRATIONS_DATABASE_URL",
    "DEMAND_SITE_ID", "DEFAULT_SITE_ID", "LOOKBACK_DAYS", "FORWARD_ZONE_CODES",
    "DEMAND_MODE", "DEMAND_CONSUMER_GROUP", "PRODUCT_MODE", "PRODUCT_CONSUMER_GROUP",
    "LAYOUT_MODE", "LAYOUT_CONSUMER_GROUP", "ENVIRONMENT", "SERVICE_VERSION", "OTEL_EXPORTER_OTLP_ENDPOINT",
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
            "KAFKA_BROKERS", "EVENT_PUBLISHER", "OUTBOX_RELAY_INTERVAL",
            "DEMAND_MODE", "DEMAND_CONSUMER_GROUP", "PRODUCT_MODE", "PRODUCT_CONSUMER_GROUP",
            "LAYOUT_MODE", "LAYOUT_CONSUMER_GROUP", "LOOKBACK_DAYS", "FORWARD_ZONE_CODES",
        } & mcp_env
        if stray:
            failures.append(f"the MCP Deployment must not get Kafka/relay/consumer/planning env (it never dials Kafka): {sorted(stray)}")
        for want in ("DATABASE_URL", "MIGRATIONS_DATABASE_URL", "MCP_ADDR", "DEMAND_SITE_ID"):
            if want not in mcp_env:
                failures.append(f"the MCP Deployment does not render {want}")

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
    kafka_demand = BASE + ["--set", "config.demandMode=kafka", "--set", "config.demandConsumerGroup=g"]
    for label, args, needle in (
        ("without a database source", [], "requires database.url or database.existingSecret"),
        ("EVENT_PUBLISHER=kafka without kafka", BASE + ["--set", "config.eventPublisher=kafka"], "kafka.enabled is false"),
        ("an unknown EVENT_PUBLISHER", BASE + ["--set", "config.eventPublisher=nats"], "want \"log\" or \"kafka\""),
        ("an unknown DEMAND_MODE", BASE + ["--set", "config.demandMode=strict"], "want \"permissive\" or \"kafka\""),
        ("an unknown PRODUCT_MODE", BASE + ["--set", "config.productMode=strict"], "want \"permissive\" or \"kafka\""),
        ("an unknown LAYOUT_MODE", BASE + ["--set", "config.layoutMode=strict"], "want \"permissive\" or \"kafka\""),
        ("DEMAND_MODE=kafka without a group", BASE + ["--set", "config.demandMode=kafka", "--set", "kafka.enabled=true"],
         "DEMAND_MODE=kafka requires DEMAND_CONSUMER_GROUP"),
        ("PRODUCT_MODE=kafka without a group", BASE + ["--set", "config.productMode=kafka", "--set", "kafka.enabled=true"],
         "PRODUCT_MODE=kafka requires PRODUCT_CONSUMER_GROUP"),
        ("LAYOUT_MODE=kafka without a group", BASE + ["--set", "config.layoutMode=kafka", "--set", "kafka.enabled=true"],
         "LAYOUT_MODE=kafka requires LAYOUT_CONSUMER_GROUP"),
        ("a kafka consumer mode without kafka", kafka_demand, "a consumer in kafka mode requires KAFKA_BROKERS"),
        ("a demand group in permissive mode", BASE + ["--set", "config.demandConsumerGroup=g"], "would silently not start"),
        ("a product group in permissive mode", BASE + ["--set", "config.productConsumerGroup=g"], "would silently not start"),
        ("a layout group in permissive mode", BASE + ["--set", "config.layoutConsumerGroup=g"], "would silently not start"),
        ("LOOKBACK_DAYS out of range", BASE + ["--set", "config.lookbackDays=400"], "want 1..365"),
    ):
        result = helm_template(args)
        if result.returncode == 0 or needle not in result.stderr:
            failures.append(f"chart rendered (or failed for another reason) {label}")


def main() -> int:
    failures: list[str] = []

    check_components(render(ENABLE_EVERYTHING), failures)

    # Default values must not deploy the MCP component, an HPA or a route.
    defaults = render(BASE)
    stray = [d["metadata"]["name"] for d in defaults if d["metadata"]["name"].endswith("-mcp")]
    stray += [d["kind"] for d in defaults if d.get("kind") in {"HorizontalPodAutoscaler", "Ingress", "HTTPRoute"}]
    if stray:
        failures.append(f"optional components rendered with default values: {stray}")
    dep = next(d for d in defaults if d.get("kind") == "Deployment")
    names = env_names(dep)
    off = {
        "DEMAND_CONSUMER_GROUP", "PRODUCT_CONSUMER_GROUP", "LAYOUT_CONSUMER_GROUP", "KAFKA_BROKERS",
        "DEMAND_SITE_ID", "DEFAULT_SITE_ID", "LOOKBACK_DAYS", "FORWARD_ZONE_CODES",
    }
    if off & set(names):
        failures.append(f"these must be off with default values: {sorted(off & set(names))}")
    for want in ("DEMAND_MODE", "PRODUCT_MODE", "LAYOUT_MODE"):
        if want not in names:
            failures.append(f"{want} must always be rendered (permissive by default)")

    check_refusals(failures)

    if failures:
        for f in failures:
            print(f"FAIL: {f}")
        return 1

    print("PASS: every Service selects exactly one Deployment (api, mcp); mcp, HPA and routes are off by "
          "default; every cmd/api env var has a dedicated value; the chart refuses to render without a "
          "database source, with an unknown publisher/mode, with a Kafka mode but no broker or no consumer "
          "group, with a group set but its consumer off, or with LOOKBACK_DAYS out of range")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
