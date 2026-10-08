# warehouse-harness-template

Canonical `harness-template: v2` for the [warehouse-systems](https://github.com/claudioed)
fleet's Go bounded-context services: `Makefile`, CI workflow, lefthook
hooks, linter/mutation-testing config, architecture fitness tests
(hexagonal dependency rules + fleet-wide invariants learned from real
incidents), and `.claude/` guides (rules, skills, commands).

This is a **template, not a runnable service** — it has no domain code,
no `cmd/`, no OpenAPI spec. It exists to answer one question for every
new (or drifting) bounded-context repo in the fleet: **what harness
should this repo have, and why?**

Start with [`HARNESS.md`](HARNESS.md) — it documents every sensor's
purpose, cost, and lifecycle position, and the specific incident each
architecture fitness test was written to prevent from recurring.

## Instantiating this template into a new (or existing) repo

```bash
git clone https://github.com/claudioed/warehouse-harness-template <new-repo>
cd <new-repo>
rm -rf .git && git init
bash scripts/new-service.sh <service-name> <richest-domain-aggregate-pkg> [<wms|wes> <event-context>]
```

Kafka-publishing/consuming services pass the last two arguments: the
script then generates the fleet-mandatory CloudEvents 1.0 helper
(`internal/adapters/kafka/cloudevents/`). CloudEvents is the only event
envelope in this fleet — there is no flat/dual mode to configure.

See `scripts/new-service.sh`'s header comment, or `HARNESS.md`'s
"Instantiating this template" section, for the full checklist —
substitution is only step one; `.claude/rules/*.md` content and
`.gremlins.yaml`'s measured thresholds still need real code to fill in.

## Study project

This repo, like the rest of the `warehouse-systems` fleet, is a personal
study project exploring Domain-Driven Design, hexagonal architecture, and
AI-agent harness engineering. It is not production software and carries
no support guarantee.
