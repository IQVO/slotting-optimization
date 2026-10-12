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
Image reference shared by every Deployment (api, mcp).
*/}}
{{- define "slotting-optimization.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end }}

{{/*
Fails chart rendering with a clear message if no DATABASE_URL source is
configured. The binary would silently fall back to in-memory adapters (state
lost on restart) -- fine for `go run`, never a deployable state, so surface it
as a helm render-time error instead.
*/}}
{{- define "slotting-optimization.requireDatabase" -}}
{{- if not (or .Values.database.url .Values.database.existingSecret) -}}
{{- fail "slotting-optimization requires database.url or database.existingSecret to be set — without DATABASE_URL the binary silently runs on in-memory adapters, which is not a deployable state." -}}
{{- end -}}
{{- end -}}

{{/*
config.eventPublisher must be one of the values the binary parses
(parsePublisherMode): anything else exits at boot.
*/}}
{{- define "slotting-optimization.requireKnownPublisher" -}}
{{- if not (has .Values.config.eventPublisher (list "log" "kafka")) -}}
{{- fail (printf "config.eventPublisher is %q — want \"log\" or \"kafka\" (the binary exits at boot otherwise)." .Values.config.eventPublisher) -}}
{{- end -}}
{{- end -}}

{{/*
EVENT_PUBLISHER=kafka makes the binary refuse to boot without KAFKA_BROKERS
(cmd/api startOutboxRelay), and KAFKA_BROKERS is only rendered when
kafka.enabled is true -- so that combination would crash-loop. Fail at render.
*/}}
{{- define "slotting-optimization.requireKafkaForPublisher" -}}
{{- if and (eq .Values.config.eventPublisher "kafka") (not .Values.kafka.enabled) -}}
{{- fail "config.eventPublisher is \"kafka\" but kafka.enabled is false — the binary exits at boot (EVENT_PUBLISHER=kafka requires KAFKA_BROKERS). Set kafka.enabled=true and kafka.brokers." -}}
{{- end -}}
{{- end -}}

{{/*
DEMAND_MODE / PRODUCT_MODE / LAYOUT_MODE (ADR 0003) must be permissive or
kafka, and a kafka mode starts a consumer: it needs a stable consumer group id
(cmd/api planConsumers refuses to boot without one) and KAFKA_BROKERS. Without
a group the pod would crash-loop; without brokers it would too. Fail at render.
*/}}
{{- define "slotting-optimization.requireConsumerConfig" -}}
{{- range $c := list (dict "mode" "demandMode" "group" "demandConsumerGroup" "env" "DEMAND") (dict "mode" "productMode" "group" "productConsumerGroup" "env" "PRODUCT") (dict "mode" "layoutMode" "group" "layoutConsumerGroup" "env" "LAYOUT") -}}
{{- $mode := index $.Values.config $c.mode -}}
{{- if not (has $mode (list "permissive" "kafka")) -}}
{{- fail (printf "config.%s is %q — want \"permissive\" or \"kafka\" (the binary exits at boot otherwise)." $c.mode $mode) -}}
{{- end -}}
{{- if and (eq $mode "kafka") (not (index $.Values.config $c.group)) -}}
{{- fail (printf "config.%s is \"kafka\" but config.%s is empty — the binary exits at boot (%s_MODE=kafka requires %s_CONSUMER_GROUP). Use a STABLE id." $c.mode $c.group $c.env $c.env) -}}
{{- end -}}
{{- if and (index $.Values.config $c.group) (ne $mode "kafka") -}}
{{- fail (printf "config.%s is set but config.%s is not \"kafka\" — the %s consumer would silently not start. Set %s=kafka or clear the group." $c.group $c.mode (lower $c.env) $c.mode) -}}
{{- end -}}
{{- end -}}
{{- if and (or (eq .Values.config.demandMode "kafka") (eq .Values.config.productMode "kafka") (eq .Values.config.layoutMode "kafka")) (not .Values.kafka.enabled) -}}
{{- fail "config.demandMode/productMode/layoutMode is \"kafka\" but kafka.enabled is false — the binary exits at boot (a consumer in kafka mode requires KAFKA_BROKERS). Set kafka.enabled=true and kafka.brokers." -}}
{{- end -}}
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
