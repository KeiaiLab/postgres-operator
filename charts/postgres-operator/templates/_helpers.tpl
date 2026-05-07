{{/*
공통 helper templates (P1-4, ADR 0007).
모든 chart template이 사용하는 라벨/이름 helper를 단일 출처(SOT)로 통일.
*/}}

{{/*
Chart의 fullname (release name 기반). 대부분 자원의 metadata.name으로 사용.
*/}}
{{- define "postgres-operator.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Chart name. selector 라벨에 사용.
*/}}
{{- define "postgres-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
표준 라벨 셋. 모든 자원에 부착하여 cluster-wide 식별 보장.
SelectorLabels(internal/controller/names.go)와 일관된 패턴.
*/}}
{{- define "postgres-operator.labels" -}}
app.kubernetes.io/name: {{ include "postgres-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: postgres-operator
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end -}}

{{/*
Selector 라벨 (Service/Deployment selector 용. version은 제외 — rolling update 시 selector 변경 회피).
*/}}
{{- define "postgres-operator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "postgres-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
control-plane: controller-manager
{{- end -}}

{{/*
Service Account name. values.serviceAccount.name이 빈 값이면 fullname을 사용.
*/}}
{{- define "postgres-operator.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "postgres-operator.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Container image — values.image.tag가 빈 값이면 Chart.AppVersion 자동 사용.
*/}}
{{- define "postgres-operator.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}
