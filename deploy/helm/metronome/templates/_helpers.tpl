{{- define "metronome.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "metronome.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := include "metronome.name" . -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "metronome.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "metronome.selectorLabels" -}}
app.kubernetes.io/name: {{ include "metronome.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "metronome.labels" -}}
helm.sh/chart: {{ include "metronome.chart" . }}
{{ include "metronome.selectorLabels" . }}
{{- if and .Values.image.tag (not .Values.image.digest) }}
app.kubernetes.io/version: {{ .Values.image.tag | toString | trunc 63 | trimSuffix "-" | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "metronome.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "metronome.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "metronome.image" -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" .Values.image.repository (toString .Values.image.tag) -}}
{{- end -}}
{{- end -}}

{{- define "metronome.secretEnv" -}}
{{- range . }}
- name: {{ .name | quote }}
  valueFrom:
    secretKeyRef:
      name: {{ .secretName | quote }}
      key: {{ .key | quote }}
{{- end -}}
{{- end -}}

{{- define "metronome.validate" -}}
{{- if not (or .Values.image.tag .Values.image.digest) -}}
{{- fail "set image.tag to a published Metronome version (or image.digest); the chart does not default to a rolling tag" -}}
{{- end -}}
{{- if not .Values.actor -}}
{{- fail "actor must be nonempty (METRONOME_ACTOR)" -}}
{{- end -}}
{{- if not (has .Values.database.type (list "postgres" "sqlite")) -}}
{{- fail "database.type must be postgres or sqlite" -}}
{{- end -}}
{{- if and (eq .Values.database.type "postgres") (not .Values.database.postgres.existingSecret) -}}
{{- fail (printf "database.postgres.existingSecret is required when database.type is postgres: create a Secret holding the postgres:// URL under key %s, or set database.type=sqlite" .Values.database.postgres.key) -}}
{{- end -}}
{{- if and .Values.authProxy.enabled (not (and .Values.authProxy.image.repository .Values.authProxy.image.tag)) -}}
{{- fail "authProxy.enabled requires authProxy.image.repository and authProxy.image.tag" -}}
{{- end -}}
{{- if and .Values.ingress.enabled (not .Values.authProxy.enabled) (not .Values.ingress.allowWithoutProxy) -}}
{{- fail "the board and API are unauthenticated: enable authProxy, or set ingress.allowWithoutProxy=true if the ingress controller authenticates" -}}
{{- end -}}
{{- end -}}
