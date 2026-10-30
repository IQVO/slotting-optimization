{{/*
Expand the name of the chart.
*/}}
{{- define "slotting-optimization.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "slotting-optimization.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Chart name and version as used by the chart label.
*/}}
{{- define "slotting-optimization.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "slotting-optimization.labels" -}}
helm.sh/chart: {{ include "slotting-optimization.chart" . }}
{{ include "slotting-optimization.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels. Shared by every pod this release creates, so every
Deployment and Service ALSO pins app.kubernetes.io/component (api, mcp):
a Service selecting on these two alone would select every component.
*/}}
{{- define "slotting-optimization.selectorLabels" -}}
app.kubernetes.io/name: {{ include "slotting-optimization.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "slotting-optimization.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "slotting-optimization.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Name of the Secret holding DATABASE_URL: the operator's own
(database.existingSecret) or the one this chart creates from database.url.
*/}}
{{- define "slotting-optimization.databaseSecretName" -}}
{{- if .Values.database.existingSecret }}
{{- .Values.database.existingSecret }}
{{- else }}
{{- include "slotting-optimization.fullname" . }}-database
{{- end }}
{{- end }}

{{/*
Fully qualified name of the MCP server deployment/service.
*/}}
{{- define "slotting-optimization.mcpFullname" -}}
{{- include "slotting-optimization.fullname" . }}-mcp
{{- end }}

{{/*
Fully qualified name of the A2A agent Deployment/Service (<release>-a2a, ADR 0013).
*/}}
{{- define "slotting-optimization.a2aFullname" -}}
{{- include "slotting-optimization.fullname" . }}-a2a
{{- end }}

{{/*
Image reference shared by every Deployment (api, mcp, a2a).
*/}}
{{- define "slotting-optimization.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end }}

{{/*
MANDATORY (ADR 0011: Postgres is the only runtime). cmd/api, cmd/mcp and cmd/a2a exit
at boot without DATABASE_URL; there is no in-memory mode. Fails the render
naming the values instead of crash-looping.
*/}}
{{- define "slotting-optimization.requireDatabase" -}}
{{- if not (or .Values.database.url .Values.database.existingSecret) -}}
{{- fail "slotting-optimization requires database.url or database.existingSecret to be set (DATABASE_URL): the service runs on real Postgres only and exits at boot without it (ADR 0011)." -}}
{{- end -}}
{{- end -}}

{{/*
Fully qualified names of the analytics read-side components (ADR 0006):
the projector (writer) and the reports binary (reader). The reports
Service carries the same name as its Deployment (different kinds, one
name), like the api component.
*/}}
{{- define "slotting-optimization.analyticsProjectorFullname" -}}
{{- include "slotting-optimization.fullname" . }}-projector
{{- end }}

{{- define "slotting-optimization.analyticsReportsFullname" -}}
{{- include "slotting-optimization.fullname" . }}-reports
{{- end }}

{{/*
Name of the Secret holding ANALYTICS_DATABASE_URL: the operator's own
(analytics.existingSecret) or the one this chart creates from
analytics.url (analytics-secret.yaml).
*/}}
{{- define "slotting-optimization.analyticsSecretName" -}}
{{- if .Values.analytics.existingSecret -}}
{{- .Values.analytics.existingSecret -}}
{{- else -}}
{{- include "slotting-optimization.fullname" . }}-analytics
{{- end -}}
{{- end }}

{{/*
Analytics guard (ADR 0006 §9): the projector and reports binaries exit at
boot without ANALYTICS_DATABASE_URL. The projector consumes
warehouse.slotting-optimization.analytics from kafka.brokers, which the api
Deployment already requires (requireKafka), so only the DSN source is checked
here, and only while analytics is on.
*/}}
{{- define "slotting-optimization.requireAnalyticsConfig" -}}
{{- if not (or .Values.analytics.url .Values.analytics.existingSecret) -}}
{{- fail "analytics.enabled is true but neither analytics.url nor analytics.existingSecret is set — the projector and reports binaries exit at boot without ANALYTICS_DATABASE_URL." -}}
{{- end -}}
{{- end -}}

{{/*
MANDATORY (ADR 0011: Kafka is the only runtime). cmd/api exits at boot without
KAFKA_BROKERS and rejects EVENT_PUBLISHER=log; the projector needs the brokers
too. There is no default broker, no log sink and no way to switch Kafka off.
kafka.enabled is kept only so values written before ADR 0011 still render
(warehouse-infra sets it to "true"): it may be true, never false.
*/}}
{{- define "slotting-optimization.requireKafka" -}}
{{- $_ := required "slotting-optimization requires kafka.brokers to be set (KAFKA_BROKERS, comma-separated): the service runs on real Kafka only and exits at boot without it (ADR 0011)." .Values.kafka.brokers -}}
{{- if and (hasKey .Values.kafka "enabled") (or (not .Values.kafka.enabled) (eq (toString .Values.kafka.enabled) "false")) -}}
{{- fail "kafka.enabled is false — Kafka cannot be switched off: the service runs on real Kafka only (ADR 0011). Drop the key or set it to true and set kafka.brokers." -}}
{{- end -}}
{{- if ne (toString .Values.config.eventPublisher) "kafka" -}}
{{- fail (printf "config.eventPublisher is %q — the only publisher is \"kafka\": the log sink was removed (ADR 0011); the binary rejects EVENT_PUBLISHER=log at boot." (toString .Values.config.eventPublisher)) -}}
{{- end -}}
{{- end -}}

{{/*
DEMAND_MODE / PRODUCT_MODE / LAYOUT_MODE (ADR 0003) and RELOCATION_MODE
(ADR 0010) and PACK_LEVEL_MODE (ADR 0012): the only mode is "kafka" (the names are kept because docs and
infra set them). "permissive" and "off" no longer exist: the binary exits at
boot on them (ADR 0011).
*/}}
{{- define "slotting-optimization.requireConsumerModes" -}}
{{- range $key := list "demandMode" "productMode" "layoutMode" "relocationMode" "packLevelMode" -}}
{{- $mode := index $.Values.config $key -}}
{{- if ne (toString $mode) "kafka" -}}
{{- fail (printf "config.%s is %q — the only mode is \"kafka\": the local copies are always fed from real Kafka and permissive/off were removed (ADR 0011); the binary exits at boot otherwise." $key (toString $mode)) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
All five consumers always run, each under a STABLE consumer group id that must
come from values (cmd/api exits at boot without DEMAND_CONSUMER_GROUP,
PRODUCT_CONSUMER_GROUP, LAYOUT_CONSUMER_GROUP, RELOCATION_CONSUMER_GROUP and
PACK_LEVEL_CONSUMER_GROUP).
A stable id is what lets a rescheduled pod resume from its committed offsets.
*/}}
{{- define "slotting-optimization.requireConsumerGroups" -}}
{{- range $c := list (dict "key" "demandConsumerGroup" "env" "DEMAND_CONSUMER_GROUP") (dict "key" "productConsumerGroup" "env" "PRODUCT_CONSUMER_GROUP") (dict "key" "layoutConsumerGroup" "env" "LAYOUT_CONSUMER_GROUP") (dict "key" "relocationConsumerGroup" "env" "RELOCATION_CONSUMER_GROUP") (dict "key" "packLevelConsumerGroup" "env" "PACK_LEVEL_CONSUMER_GROUP") -}}
{{- if not (index $.Values.config $c.key) -}}
{{- fail (printf "config.%s is required (%s): the consumer always runs on real Kafka and cmd/api exits at boot without a group. Use a STABLE id (ADR 0011)." $c.key $c.env) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
extraEnv may not bring back a log or permissive/off mode (ADR 0011): no
EVENT_PUBLISHER other than kafka and no *_MODE variable at all (the modes have
dedicated values and the only legal value is "kafka"). Called with the list to
check and the name of its value for the message.
*/}}
{{- define "slotting-optimization.requireNoInMemoryEnv" -}}
{{- $where := .where -}}
{{- range .list -}}
{{- $name := toString .name -}}
{{- $value := toString (default "" .value) -}}
{{- if and (eq $name "EVENT_PUBLISHER") (ne $value "kafka") -}}
{{- fail (printf "%s sets EVENT_PUBLISHER=%s — the only publisher is \"kafka\" (ADR 0011)." $where $value) -}}
{{- end -}}
{{- if hasSuffix "_MODE" $name -}}
{{- fail (printf "%s sets %s — *_MODE variables are set through config.demandMode / productMode / layoutMode / relocationMode / packLevelMode and the only legal value is \"kafka\": slotting-optimization runs on real Postgres and real Kafka only, with no permissive or off mode (ADR 0011)." $where $name) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
The graceful shutdown is sequential (SHUTDOWN_DRAIN_DELAY 5s + HTTP drain 10s +
consumers drain 10s + outbox relay drain 10s). A grace period under 20s would
SIGKILL the pod between "consumers stopped" and "relay stopped", stranding an
uncommitted offset or an in-flight outbox batch.
*/}}
{{- define "slotting-optimization.requireGracePeriod" -}}
{{- if lt (int .Values.terminationGracePeriodSeconds) 20 -}}
{{- fail (printf "terminationGracePeriodSeconds is %v — want at least 20: the consumers and the outbox relay drain after the HTTP server, and a shorter grace period SIGKILLs them mid-drain (ADR 0011)." .Values.terminationGracePeriodSeconds) -}}
{{- end -}}
{{- end -}}

{{/*
Every render-time requirement of the real runtime, in one include. The api
Deployment (always rendered) includes it, so a mis-set release fails
`helm template` / `helm install` whichever component is enabled.
*/}}
{{- define "slotting-optimization.requireRealRuntime" -}}
{{- include "slotting-optimization.requireDatabase" . -}}
{{- include "slotting-optimization.requireKafka" . -}}
{{- include "slotting-optimization.requireConsumerModes" . -}}
{{- include "slotting-optimization.requireConsumerGroups" . -}}
{{- include "slotting-optimization.requireNoInMemoryEnv" (dict "list" .Values.extraEnv "where" "extraEnv") -}}
{{- include "slotting-optimization.requireNoInMemoryEnv" (dict "list" .Values.mcp.extraEnv "where" "mcp.extraEnv") -}}
{{- include "slotting-optimization.requireNoInMemoryEnv" (dict "list" .Values.a2a.extraEnv "where" "a2a.extraEnv") -}}
{{- include "slotting-optimization.requireGracePeriod" . -}}
{{- end -}}

{{/*
LOOKBACK_DAYS outside 1..365 is warned about and ignored by the binary: a
silent fallback to the 28-day default. Refuse it at render so the intent is
never lost.
*/}}
{{- define "slotting-optimization.requireLookbackDays" -}}
{{- with .Values.config.lookbackDays -}}
{{- if or (lt (int .) 1) (gt (int .) 365) -}}
{{- fail (printf "config.lookbackDays is %v — want 1..365 (the binary ignores anything else and uses 28)." .) -}}
{{- end -}}
{{- end -}}
{{- end -}}


{{/*
Comma-joined names of this chart's enabled KongPlugins, for the
konghq.com/plugins annotation on every HTTPRoute this chart renders.
Empty string (annotation omitted) when none are enabled.
*/}}
{{- define "slotting-optimization.kongPluginNames" -}}
{{- $names := list -}}
{{- range $name, $cfg := .Values.kongPlugins -}}
{{- if $cfg.enabled -}}
{{- $names = append $names (printf "%s-%s" (include "slotting-optimization.fullname" $) $name) -}}
{{- end -}}
{{- end -}}
{{- join "," $names -}}
{{- end -}}
