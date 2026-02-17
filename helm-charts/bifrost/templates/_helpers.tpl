{{/*
Expand the name of the chart.
*/}}
{{- define "bifrost.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "bifrost.fullname" -}}
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
Create chart name and version as used by the chart label.
*/}}
{{- define "bifrost.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "bifrost.labels" -}}
helm.sh/chart: {{ include "bifrost.chart" . }}
{{ include "bifrost.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "bifrost.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bifrost.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "bifrost.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "bifrost.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
PostgreSQL host
*/}}
{{- define "bifrost.postgresql.host" -}}
{{- if .Values.postgresql.external.enabled }}
{{- .Values.postgresql.external.host }}
{{- else }}
{{- printf "%s-postgresql" .Release.Name }}
{{- end }}
{{- end }}

{{/*
PostgreSQL port
*/}}
{{- define "bifrost.postgresql.port" -}}
{{- if .Values.postgresql.external.enabled }}
{{- .Values.postgresql.external.port | default 5432 }}
{{- else }}
{{- 5432 }}
{{- end }}
{{- end }}

{{/*
PostgreSQL database name
*/}}
{{- define "bifrost.postgresql.database" -}}
{{- if .Values.postgresql.external.enabled }}
{{- .Values.postgresql.external.dbName | default "bifrost" }}
{{- else }}
{{- .Values.postgresql.auth.database | default "bifrost" }}
{{- end }}
{{- end }}

{{/*
PostgreSQL username
*/}}
{{- define "bifrost.postgresql.username" -}}
{{- if .Values.postgresql.external.enabled }}
{{- .Values.postgresql.external.user | default "bifrost" }}
{{- else }}
{{- .Values.postgresql.auth.username | default "bifrost" }}
{{- end }}
{{- end }}

{{/*
PostgreSQL password
*/}}
{{- define "bifrost.postgresql.password" -}}
{{- if .Values.postgresql.external.enabled }}
{{- .Values.postgresql.external.password }}
{{- else }}
{{- .Values.postgresql.auth.password }}
{{- end }}
{{- end }}

{{/*
PostgreSQL SSL mode
*/}}
{{- define "bifrost.postgresql.sslMode" -}}
{{- if .Values.postgresql.external.enabled }}
{{- .Values.postgresql.external.sslMode | default "disable" }}
{{- else }}
{{- "disable" }}
{{- end }}
{{- end }}

{{/*
Redis host
*/}}
{{- define "bifrost.redis.host" -}}
{{- if .Values.redis.external.enabled }}
{{- .Values.redis.external.host }}
{{- else }}
{{- printf "%s-redis-master" .Release.Name }}
{{- end }}
{{- end }}

{{/*
Redis port
*/}}
{{- define "bifrost.redis.port" -}}
{{- if .Values.redis.external.enabled }}
{{- .Values.redis.external.port | default 6379 }}
{{- else }}
{{- 6379 }}
{{- end }}
{{- end }}

{{/*
Redis password
*/}}
{{- define "bifrost.redis.password" -}}
{{- if .Values.redis.external.enabled }}
{{- .Values.redis.external.password | default "" }}
{{- else }}
{{- .Values.redis.auth.password | default "" }}
{{- end }}
{{- end }}

{{/*
Redis database index
*/}}
{{- define "bifrost.redis.db" -}}
{{- if .Values.redis.external.enabled }}
{{- .Values.redis.external.db | default 0 }}
{{- else }}
{{- 0 }}
{{- end }}
{{- end }}

{{/*
Redis SSL enabled
*/}}
{{- define "bifrost.redis.ssl" -}}
{{- if .Values.redis.external.enabled }}
{{- .Values.redis.external.ssl | default false }}
{{- else }}
{{- false }}
{{- end }}
{{- end }}

{{/*
Qdrant host
*/}}
{{- define "bifrost.qdrant.host" -}}
{{- if .Values.qdrant.external.enabled }}
{{- .Values.qdrant.external.host }}
{{- else }}
{{- printf "%s-qdrant" .Release.Name }}
{{- end }}
{{- end }}

{{/*
Qdrant port
*/}}
{{- define "bifrost.qdrant.port" -}}
{{- if .Values.qdrant.external.enabled }}
{{- .Values.qdrant.external.port | default 6334 }}
{{- else }}
{{- 6334 }}
{{- end }}
{{- end }}

{{/*
Qdrant API key
*/}}
{{- define "bifrost.qdrant.apiKey" -}}
{{- if .Values.qdrant.external.enabled }}
{{- .Values.qdrant.external.apiKey | default "" }}
{{- else }}
{{- "" }}
{{- end }}
{{- end }}

{{/*
Qdrant use TLS
*/}}
{{- define "bifrost.qdrant.useTLS" -}}
{{- if .Values.qdrant.external.enabled }}
{{- .Values.qdrant.external.useTLS | default false }}
{{- else }}
{{- false }}
{{- end }}
{{- end }}

{{/*
Generate config.json content
*/}}
{{- define "bifrost.config" -}}
{{- $config := dict }}

{{/* Server Configuration */}}
{{- $serverConfig := dict "host" "0.0.0.0" "port" (.Values.service.port | default 8080 | int) }}
{{- $_ := set $config "server" $serverConfig }}

{{/* Providers Configuration */}}
{{- if .Values.providers }}
{{- $providersList := list }}
{{- range .Values.providers }}
{{- $provider := dict "name" .name }}
{{- if .apiKey }}
{{- $_ := set $provider "api_key" .apiKey }}
{{- end }}
{{- if .apiBase }}
{{- $_ := set $provider "api_base" .apiBase }}
{{- end }}
{{- if .awsAccessKey }}
{{- $_ := set $provider "aws_access_key" .awsAccessKey }}
{{- end }}
{{- if .awsSecretKey }}
{{- $_ := set $provider "aws_secret_key" .awsSecretKey }}
{{- end }}
{{- if .awsRegion }}
{{- $_ := set $provider "aws_region" .awsRegion }}
{{- end }}
{{- if .useAwsSdkDefault }}
{{- $_ := set $provider "use_aws_sdk_default" .useAwsSdkDefault }}
{{- end }}
{{- if .gcpCredentials }}
{{- $_ := set $provider "gcp_credentials" .gcpCredentials }}
{{- end }}
{{- if .gcpProject }}
{{- $_ := set $provider "gcp_project" .gcpProject }}
{{- end }}
{{- if .gcpRegion }}
{{- $_ := set $provider "gcp_region" .gcpRegion }}
{{- end }}
{{- if .azureAPIVersion }}
{{- $_ := set $provider "azure_api_version" .azureAPIVersion }}
{{- end }}
{{- if .environmentVarName }}
{{- $_ := set $provider "environment_var_name" .environmentVarName }}
{{- end }}
{{- $providersList = append $providersList $provider }}
{{- end }}
{{- $_ := set $config "providers" $providersList }}
{{- end }}

{{/* Auth Configuration */}}
{{- $authConfig := dict }}
{{- if .Values.authConfig }}
{{- if .Values.authConfig.adminUsername }}
{{- $_ := set $authConfig "admin_username" .Values.authConfig.adminUsername }}
{{- end }}
{{- if .Values.authConfig.adminPassword }}
{{- $_ := set $authConfig "admin_password" .Values.authConfig.adminPassword }}
{{- end }}
{{- if .Values.authConfig.existingSecret }}
{{- $_ := set $authConfig "admin_username" "env.BIFROST_ADMIN_USERNAME" }}
{{- $_ := set $authConfig "admin_password" "env.BIFROST_ADMIN_PASSWORD" }}
{{- end }}
{{- if .Values.authConfig.disableAuthOnInference }}
{{- $_ := set $authConfig "disable_auth_on_inference" .Values.authConfig.disableAuthOnInference }}
{{- end }}
{{- $_ := set $config "auth" $authConfig }}
{{- end }}

{{/* Config Store Configuration */}}
{{- $configStoreConfig := dict }}
{{- if or .Values.postgresql.enabled .Values.postgresql.external.enabled }}
{{- $configStoreConfig = dict "type" "postgres" }}
{{- $pgConfig := dict "host" (include "bifrost.postgresql.host" .) "port" (include "bifrost.postgresql.port" . | int) "db_name" (include "bifrost.postgresql.database" .) "user" (include "bifrost.postgresql.username" .) "password" (include "bifrost.postgresql.password" .) "ssl_mode" (include "bifrost.postgresql.sslMode" .) }}
{{- $_ := set $configStoreConfig "config" $pgConfig }}
{{- else }}
{{- $configStoreConfig = dict "type" "sqlite" "config" (dict "path" "/app/data/config.db") }}
{{- end }}
{{- $_ := set $config "config_store" $configStoreConfig }}

{{/* Logs Store Configuration */}}
{{- $logsStoreConfig := dict }}
{{- if or .Values.postgresql.enabled .Values.postgresql.external.enabled }}
{{- $logsStoreConfig = dict "type" "postgres" }}
{{- $pgConfig := dict "host" (include "bifrost.postgresql.host" .) "port" (include "bifrost.postgresql.port" . | int) "db_name" (include "bifrost.postgresql.database" .) "user" (include "bifrost.postgresql.username" .) "password" (include "bifrost.postgresql.password" .) "ssl_mode" (include "bifrost.postgresql.sslMode" .) }}
{{- $_ := set $logsStoreConfig "config" $pgConfig }}
{{- else }}
{{- $logsStoreConfig = dict "type" "sqlite" "config" (dict "path" "/app/data/logs.db") }}
{{- end }}
{{- $_ := set $config "logs_store" $logsStoreConfig }}

{{/* Caching Configuration */}}
{{- $cachingConfig := dict }}
{{- if or .Values.redis.enabled .Values.redis.external.enabled }}
{{- $cachingConfig = dict "type" "redis" }}
{{- $redisConfig := dict "addr" (printf "%s:%s" (include "bifrost.redis.host" .) (include "bifrost.redis.port" .)) "password" (include "bifrost.redis.password" .) "db" (include "bifrost.redis.db" . | int) "ssl" (include "bifrost.redis.ssl" . | eq "true") }}
{{- $_ := set $cachingConfig "config" $redisConfig }}
{{- else }}
{{- $cachingConfig = dict "type" "local" }}
{{- end }}
{{- $_ := set $config "caching" $cachingConfig }}

{{/* Plugins Configuration */}}
{{- if .Values.plugins }}
{{- $pluginsList := list }}
{{- range .Values.plugins }}
{{- $plugin := dict "name" .name }}
{{- if .config }}
{{- $_ := set $plugin "config" .config }}
{{- end }}
{{- $pluginsList = append $pluginsList $plugin }}
{{- end }}
{{- $_ := set $config "plugins" $pluginsList }}
{{- end }}

{{/* Qdrant Configuration */}}
{{- if or .Values.qdrant.enabled .Values.qdrant.external.enabled }}
{{- $qdrantConfig := dict "host" (include "bifrost.qdrant.host" .) "port" (include "bifrost.qdrant.port" . | int) }}
{{- $qdrantApiKey := (include "bifrost.qdrant.apiKey" .) }}
{{- if $qdrantApiKey }}
{{- $_ := set $qdrantConfig "api_key" $qdrantApiKey }}
{{- end }}
{{- $_ := set $qdrantConfig "use_tls" (include "bifrost.qdrant.useTLS" . | eq "true") }}
{{- $_ := set $config "qdrant" $qdrantConfig }}
{{- end }}

{{/* MCP Configuration */}}
{{- if .Values.mcp }}
{{- $mcpConfig := dict }}
{{- if .Values.mcp.clientConfigs }}
{{- $clientConfigsList := list }}
{{- range .Values.mcp.clientConfigs }}
{{- $clientConfig := dict }}
{{- if .transportType }}
{{- $_ := set $clientConfig "transport_type" .transportType }}
{{- end }}
{{- if .name }}
{{- $_ := set $clientConfig "name" .name }}
{{- end }}
{{- if .command }}
{{- $_ := set $clientConfig "command" .command }}
{{- end }}
{{- if .args }}
{{- $_ := set $clientConfig "args" .args }}
{{- end }}
{{- if .url }}
{{- $_ := set $clientConfig "url" .url }}
{{- end }}
{{- if .headers }}
{{- $_ := set $clientConfig "headers" .headers }}
{{- end }}
{{- if .readTimeout }}
{{- $_ := set $clientConfig "read_timeout" .readTimeout }}
{{- end }}
{{- if .env }}
{{- $_ := set $clientConfig "env" .env }}
{{- end }}
{{- $clientConfigsList = append $clientConfigsList $clientConfig }}
{{- end }}
{{- $_ := set $mcpConfig "client_configs" $clientConfigsList }}
{{- end }}
{{- $_ := set $config "mcp" $mcpConfig }}
{{- end }}

{{- $config | toJson }}
{{- end }}
