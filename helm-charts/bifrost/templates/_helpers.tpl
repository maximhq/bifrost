{{- define "bifrost.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

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

{{- define "bifrost.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "bifrost.labels" -}}
helm.sh/chart: {{ include "bifrost.chart" . }}
{{ include "bifrost.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "bifrost.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bifrost.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "bifrost.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "bifrost.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "bifrost.postgresql.host" -}}
{{- if .Values.postgresql.external.enabled }}
{{- .Values.postgresql.external.host }}
{{- else }}
{{- printf "%s-postgresql" (include "bifrost.fullname" .) }}
{{- end }}
{{- end }}

{{- define "bifrost.postgresql.port" -}}
{{- if .Values.postgresql.external.enabled -}}
{{- .Values.postgresql.external.port -}}
{{- else -}}
5432
{{- end -}}
{{- end -}}

{{- define "bifrost.postgresql.database" -}}
{{- if .Values.postgresql.external.enabled }}
{{- .Values.postgresql.external.database }}
{{- else }}
{{- .Values.postgresql.auth.database }}
{{- end }}
{{- end }}

{{- define "bifrost.postgresql.username" -}}
{{- if .Values.postgresql.external.enabled }}
{{- .Values.postgresql.external.user }}
{{- else }}
{{- .Values.postgresql.auth.username }}
{{- end }}
{{- end }}

{{- define "bifrost.postgresql.password" -}}
{{- if .Values.postgresql.external.enabled }}
{{- .Values.postgresql.external.password }}
{{- else }}
{{- .Values.postgresql.auth.password }}
{{- end }}
{{- end }}

{{- define "bifrost.postgresql.sslMode" -}}
{{- if .Values.postgresql.external.enabled -}}
{{- .Values.postgresql.external.sslMode -}}
{{- else -}}
disable
{{- end -}}
{{- end -}}

{{- define "bifrost.weaviate.host" -}}
{{- if .Values.vectorStore.weaviate.external.enabled }}
{{- .Values.vectorStore.weaviate.external.host }}
{{- else }}
{{- printf "%s-weaviate" (include "bifrost.fullname" .) }}
{{- end }}
{{- end }}

{{- define "bifrost.weaviate.scheme" -}}
{{- if .Values.vectorStore.weaviate.external.enabled -}}
{{- .Values.vectorStore.weaviate.external.scheme -}}
{{- else -}}
http
{{- end -}}
{{- end -}}

{{- define "bifrost.redis.host" -}}
{{- if .Values.vectorStore.redis.external.enabled }}
{{- .Values.vectorStore.redis.external.host }}
{{- else }}
{{- printf "%s-redis-master" (include "bifrost.fullname" .) }}
{{- end }}
{{- end }}

{{- define "bifrost.redis.port" -}}
{{- if .Values.vectorStore.redis.external.enabled -}}
{{- .Values.vectorStore.redis.external.port -}}
{{- else -}}
6379
{{- end -}}
{{- end -}}

{{- define "bifrost.redis.password" -}}
{{- if .Values.vectorStore.redis.external.enabled }}
{{- .Values.vectorStore.redis.external.password }}
{{- else }}
{{- .Values.vectorStore.redis.auth.password }}
{{- end }}
{{- end }}

{{- define "bifrost.qdrant.host" -}}
{{- if .Values.vectorStore.qdrant.external.enabled }}
{{- .Values.vectorStore.qdrant.external.host }}
{{- else }}
{{- printf "%s-qdrant" (include "bifrost.fullname" .) }}
{{- end }}
{{- end }}

{{- define "bifrost.qdrant.port" -}}
{{- if .Values.vectorStore.qdrant.external.enabled -}}
{{- .Values.vectorStore.qdrant.external.port -}}
{{- else -}}
6334
{{- end -}}
{{- end -}}

{{- define "bifrost.qdrant.apiKey" -}}
{{- if .Values.vectorStore.qdrant.external.enabled }}
{{- .Values.vectorStore.qdrant.external.apiKey }}
{{- end }}
{{- end }}

{{- define "bifrost.qdrant.useTls" -}}
{{- if .Values.vectorStore.qdrant.external.enabled -}}
{{- .Values.vectorStore.qdrant.external.useTls -}}
{{- else -}}
false
{{- end -}}
{{- end -}}

{{- define "bifrost.config" -}}
{{- $config := dict }}
{{- if .Values.bifrost.encryptionKey }}
{{- $_ := set $config "encryption_key" .Values.bifrost.encryptionKey }}
{{- end }}
{{- if .Values.bifrost.client }}
{{- $client := dict }}
{{- if hasKey .Values.bifrost.client "dropExcessRequests" }}
{{- $_ := set $client "drop_excess_requests" .Values.bifrost.client.dropExcessRequests }}
{{- end }}
{{- if .Values.bifrost.client.initialPoolSize }}
{{- $_ := set $client "initial_pool_size" .Values.bifrost.client.initialPoolSize }}
{{- end }}
{{- if .Values.bifrost.client.allowedOrigins }}
{{- $_ := set $client "allowed_origins" .Values.bifrost.client.allowedOrigins }}
{{- end }}
{{- if hasKey .Values.bifrost.client "enableLogging" }}
{{- $_ := set $client "enable_logging" .Values.bifrost.client.enableLogging }}
{{- end }}
{{- if hasKey .Values.bifrost.client "enableGovernance" }}
{{- $_ := set $client "enable_governance" .Values.bifrost.client.enableGovernance }}
{{- end }}
{{- if hasKey .Values.bifrost.client "enforceGovernanceHeader" }}
{{- $_ := set $client "enforce_governance_header" .Values.bifrost.client.enforceGovernanceHeader }}
{{- end }}
{{- if hasKey .Values.bifrost.client "allowDirectKeys" }}
{{- $_ := set $client "allow_direct_keys" .Values.bifrost.client.allowDirectKeys }}
{{- end }}
{{- if .Values.bifrost.client.maxRequestBodySizeMb }}
{{- $_ := set $client "max_request_body_size_mb" .Values.bifrost.client.maxRequestBodySizeMb }}
{{- end }}
{{- if hasKey .Values.bifrost.client "enableLitellmFallbacks" }}
{{- $_ := set $client "enable_litellm_fallbacks" .Values.bifrost.client.enableLitellmFallbacks }}
{{- end }}
{{- if .Values.bifrost.client.prometheusLabels }}
{{- $_ := set $client "prometheus_labels" .Values.bifrost.client.prometheusLabels }}
{{- end }}
{{- $_ := set $config "client" $client }}
{{- end }}
{{- if .Values.bifrost.providers }}
{{- $_ := set $config "providers" .Values.bifrost.providers }}
{{- end }}
{{- if eq .Values.storage.mode "postgres" }}
{{- $configStore := dict "type" "postgres" }}
{{- $_ := set $configStore "postgres" (dict "host" (include "bifrost.postgresql.host" .) "port" (include "bifrost.postgresql.port" . | int) "database" (include "bifrost.postgresql.database" .) "user" (include "bifrost.postgresql.username" .) "password" (include "bifrost.postgresql.password" .) "ssl_mode" (include "bifrost.postgresql.sslMode" .)) }}
{{- $_ := set $config "config_store" $configStore }}
{{- $logsStore := dict "type" "postgres" }}
{{- $_ := set $logsStore "postgres" (dict "host" (include "bifrost.postgresql.host" .) "port" (include "bifrost.postgresql.port" . | int) "database" (include "bifrost.postgresql.database" .) "user" (include "bifrost.postgresql.username" .) "password" (include "bifrost.postgresql.password" .) "ssl_mode" (include "bifrost.postgresql.sslMode" .)) }}
{{- $_ := set $config "logs_store" $logsStore }}
{{- else }}
{{- $configStore := dict "type" "sqlite" }}
{{- $_ := set $configStore "sqlite" (dict "db_path" (printf "%s/config.db" .Values.bifrost.appDir)) }}
{{- $_ := set $config "config_store" $configStore }}
{{- $logsStore := dict "type" "sqlite" }}
{{- $_ := set $logsStore "sqlite" (dict "db_path" (printf "%s/logs.db" .Values.bifrost.appDir)) }}
{{- $_ := set $config "logs_store" $logsStore }}
{{- end }}
{{- if and .Values.vectorStore.enabled (ne .Values.vectorStore.type "none") }}
{{- $vectorStore := dict "type" .Values.vectorStore.type }}
{{- if eq .Values.vectorStore.type "weaviate" }}
{{- $weaviateConfig := dict "scheme" (include "bifrost.weaviate.scheme" .) "host" (include "bifrost.weaviate.host" .) }}
{{- if .Values.vectorStore.weaviate.external.enabled }}
{{- if .Values.vectorStore.weaviate.external.apiKey }}
{{- $_ := set $weaviateConfig "api_key" .Values.vectorStore.weaviate.external.apiKey }}
{{- end }}
{{- if .Values.vectorStore.weaviate.external.grpcHost }}
{{- $_ := set $weaviateConfig "grpc_host" .Values.vectorStore.weaviate.external.grpcHost }}
{{- end }}
{{- if hasKey .Values.vectorStore.weaviate.external "grpcSecured" }}
{{- $_ := set $weaviateConfig "grpc_secured" .Values.vectorStore.weaviate.external.grpcSecured }}
{{- end }}
{{- end }}
{{- $_ := set $vectorStore "weaviate" $weaviateConfig }}
{{- else if eq .Values.vectorStore.type "redis" }}
{{- $redisConfig := dict "host" (include "bifrost.redis.host" .) "port" (include "bifrost.redis.port" . | int) }}
{{- $password := include "bifrost.redis.password" . }}
{{- if $password }}
{{- $_ := set $redisConfig "password" $password }}
{{- end }}
{{- if .Values.vectorStore.redis.external.enabled }}
{{- if .Values.vectorStore.redis.external.database }}
{{- $_ := set $redisConfig "database" .Values.vectorStore.redis.external.database }}
{{- end }}
{{- end }}
{{- $_ := set $vectorStore "redis" $redisConfig }}
{{- else if eq .Values.vectorStore.type "qdrant" }}
{{- $qdrantConfig := dict "host" (include "bifrost.qdrant.host" .) "port" (include "bifrost.qdrant.port" . | int) }}
{{- $apiKey := include "bifrost.qdrant.apiKey" . }}
{{- if $apiKey }}
{{- $_ := set $qdrantConfig "api_key" $apiKey }}
{{- end }}
{{- $useTls := include "bifrost.qdrant.useTls" . }}
{{- if eq $useTls "true" }}
{{- $_ := set $qdrantConfig "use_tls" true }}
{{- else }}
{{- $_ := set $qdrantConfig "use_tls" false }}
{{- end }}
{{- $_ := set $vectorStore "qdrant" $qdrantConfig }}
{{- end }}
{{- $_ := set $config "vector_store" $vectorStore }}
{{- end }}
{{- if .Values.bifrost.mcp.enabled }}
{{- $_ := set $config "mcp" (dict "enabled" true "client_configs" .Values.bifrost.mcp.clientConfigs) }}
{{- end }}
{{- $plugins := dict }}
{{- if .Values.bifrost.plugins.telemetry.enabled }}
{{- $_ := set $plugins "telemetry" (dict "enabled" true "config" .Values.bifrost.plugins.telemetry.config) }}
{{- end }}
{{- if .Values.bifrost.plugins.logging.enabled }}
{{- $_ := set $plugins "logging" (dict "enabled" true "config" .Values.bifrost.plugins.logging.config) }}
{{- end }}
{{- if .Values.bifrost.plugins.governance.enabled }}
{{- $_ := set $plugins "governance" (dict "enabled" true "config" .Values.bifrost.plugins.governance.config) }}
{{- end }}
{{- if .Values.bifrost.plugins.maxim.enabled }}
{{- $_ := set $plugins "maxim" (dict "enabled" true "config" .Values.bifrost.plugins.maxim.config) }}
{{- end }}
{{- if .Values.bifrost.plugins.semanticCache.enabled }}
{{- $semanticCacheConfig := .Values.bifrost.plugins.semanticCache.config | default dict }}
{{- $_ := set $plugins "semantic_cache" (dict "enabled" true "config" $semanticCacheConfig) }}
{{- end }}
{{- if .Values.bifrost.plugins.otel.enabled }}
{{- $_ := set $plugins "otel" (dict "enabled" true "config" .Values.bifrost.plugins.otel.config) }}
{{- end }}
{{- if $plugins }}
{{- $_ := set $config "plugins" $plugins }}
{{- end }}
{{- $config | toJson }}
{{- end }}
