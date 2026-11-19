{{/* Common labels, so `kubectl get all -l app.kubernetes.io/instance=<release>` finds everything. */}}
{{- define "uw.labels" -}}
app.kubernetes.io/name: uptime-watch
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}

{{/* Selector labels must never change after install, so they carry no version. */}}
{{- define "uw.selector" -}}
app.kubernetes.io/name: uptime-watch
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{- define "uw.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}

{{/* Env shared by every container: config from the ConfigMap, secrets from the Secret. */}}
{{- define "uw.env" -}}
envFrom:
  - configMapRef:
      name: {{ .Release.Name }}
  - secretRef:
      name: {{ .Values.existingSecret }}
{{- end -}}

{{- define "uw.container" -}}
image: {{ include "uw.image" . }}
imagePullPolicy: {{ .Values.image.pullPolicy }}
securityContext:
  {{- toYaml .Values.securityContext | nindent 2 }}
{{ include "uw.env" . }}
{{- end -}}
