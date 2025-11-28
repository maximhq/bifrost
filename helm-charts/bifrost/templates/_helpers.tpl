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
{{- if .Values.postgresql.external.enabled -}}
{{- if .Values.postgresql.external.existingSecret -}}
env.BIFROST_POSTGRES_PASSWORD
{{- else -}}
{{- .Values.postgresql.external.password -}}
{{- end -}}
{{- else -}}
{{- .Values.postgresql.auth.password -}}
{{- end -}}
{{- end -}}

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

{{- define "bifrost.weaviate.apiKey" -}}
{{- if .Values.vectorStore.weaviate.external.enabled -}}
{{- if .Values.vectorStore.weaviate.external.existingSecret -}}
env.BIFROST_WEAVIATE_API_KEY
{{- else -}}
{{- .Values.vectorStore.weaviate.external.apiKey -}}
{{- end -}}
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
{{- if .Values.vectorStore.redis.external.enabled -}}
{{- if .Values.vectorStore.redis.external.existingSecret -}}
env.BIFROST_REDIS_PASSWORD
{{- else -}}
{{- .Values.vectorStore.redis.external.password -}}
{{- end -}}
{{- else -}}
{{- .Values.vectorStore.redis.auth.password -}}
{{- end -}}
{{- end -}}

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
{{- if .Values.vectorStore.qdrant.external.enabled -}}
{{- if .Values.vectorStore.qdrant.external.existingSecret -}}
env.BIFROST_QDRANT_API_KEY
{{- else -}}
{{- .Values.vectorStore.qdrant.external.apiKey -}}
{{- end -}}
{{- end -}}
{{- end -}}

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
{{- /* Config Store */ -}}
{{- if eq .Values.storage.mode "postgres" }}
{{- $pgConfig := dict "host" (include "bifrost.postgresql.host" .) "port" (include "bifrost.postgresql.port" .) "db_name" (include "bifrost.postgresql.database" .) "user" (include "bifrost.postgresql.username" .) "password" (include "bifrost.postgresql.password" .) "ssl_mode" (include "bifrost.postgresql.sslMode" .) }}
{{- $configStore := dict "enabled" true "type" "postgres" "config" $pgConfig }}
{{- $_ := set $config "config_store" $configStore }}
{{- $logsStore := dict "enabled" true "type" "postgres" "config" $pgConfig }}
{{- $_ := set $config "logs_store" $logsStore }}
{{- else }}
{{- $sqliteConfigStore := dict "enabled" true "type" "sqlite" "config" (dict "path" (printf "%s/config.db" .Values.bifrost.appDir)) }}
{{- $_ := set $config "config_store" $sqliteConfigStore }}
{{- $sqliteLogsStore := dict "enabled" true "type" "sqlite" "config" (dict "path" (printf "%s/logs.db" .Values.bifrost.appDir)) }}
{{- $_ := set $config "logs_store" $sqliteLogsStore }}
{{- end }}
{{- /* Vector Store */ -}}
{{- if and .Values.vectorStore.enabled (ne .Values.vectorStore.type "none") }}
{{- $vectorStore := dict "enabled" true "type" .Values.vectorStore.type }}
{{- if eq .Values.vectorStore.type "weaviate" }}
{{- $weaviateConfig := dict "scheme" (include "bifrost.weaviate.scheme" .) "host" (include "bifrost.weaviate.host" .) }}
{{- if .Values.vectorStore.weaviate.external.enabled }}
{{- $weaviateApiKey := include "bifrost.weaviate.apiKey" . }}
{{- if $weaviateApiKey }}
{{- $_ := set $weaviateConfig "api_key" $weaviateApiKey }}
{{- end }}
{{- if or .Values.vectorStore.weaviate.external.grpcHost (hasKey .Values.vectorStore.weaviate.external "grpcSecured") }}
{{- $grpcConfig := dict }}
{{- if .Values.vectorStore.weaviate.external.grpcHost }}
{{- $_ := set $grpcConfig "host" .Values.vectorStore.weaviate.external.grpcHost }}
{{- end }}
{{- if hasKey .Values.vectorStore.weaviate.external "grpcSecured" }}
{{- $_ := set $grpcConfig "secured" .Values.vectorStore.weaviate.external.grpcSecured }}
{{- end }}
{{- $_ := set $weaviateConfig "grpc_config" $grpcConfig }}
{{- end }}
{{- end }}
{{- $_ := set $vectorStore "config" $weaviateConfig }}
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
{{- $_ := set $vectorStore "config" $redisConfig }}
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
{{- $_ := set $vectorStore "config" $qdrantConfig }}
{{- end }}
{{- $_ := set $config "vector_store" $vectorStore }}
{{- end }}
{{- /* MCP */ -}}
{{- if .Values.bifrost.mcp.enabled }}
{{- $_ := set $config "mcp" (dict "client_configs" .Values.bifrost.mcp.clientConfigs) }}
{{- end }}
{{- /* Plugins - as array per schema */ -}}
{{- $plugins := list }}
{{- if .Values.bifrost.plugins.telemetry.enabled }}
{{- $plugins = append $plugins (dict "enabled" true "name" "telemetry" "config" .Values.bifrost.plugins.telemetry.config) }}
{{- end }}
{{- if .Values.bifrost.plugins.logging.enabled }}
{{- $plugins = append $plugins (dict "enabled" true "name" "logging" "config" .Values.bifrost.plugins.logging.config) }}
{{- end }}
{{- if .Values.bifrost.plugins.governance.enabled }}
{{- $governanceConfig := dict }}
{{- if hasKey .Values.bifrost.plugins.governance.config "is_vk_mandatory" }}
{{- $_ := set $governanceConfig "is_vk_mandatory" .Values.bifrost.plugins.governance.config.is_vk_mandatory }}
{{- end }}
{{- $plugins = append $plugins (dict "enabled" true "name" "governance" "config" $governanceConfig) }}
{{- end }}
{{- if .Values.bifrost.plugins.maxim.enabled }}
{{- $maximConfig := dict }}
{{- if and .Values.bifrost.plugins.maxim.secretRef .Values.bifrost.plugins.maxim.secretRef.name }}
{{- $_ := set $maximConfig "api_key" "env.BIFROST_MAXIM_API_KEY" }}
{{- else if .Values.bifrost.plugins.maxim.config.api_key }}
{{- $_ := set $maximConfig "api_key" .Values.bifrost.plugins.maxim.config.api_key }}
{{- end }}
{{- if .Values.bifrost.plugins.maxim.config.log_repo_id }}
{{- $_ := set $maximConfig "log_repo_id" .Values.bifrost.plugins.maxim.config.log_repo_id }}
{{- end }}
{{- $plugins = append $plugins (dict "enabled" true "name" "maxim" "config" $maximConfig) }}
{{- end }}
{{- if .Values.bifrost.plugins.semanticCache.enabled }}
{{- $scConfig := dict }}
{{- $inputConfig := .Values.bifrost.plugins.semanticCache.config | default dict }}
{{- if $inputConfig.provider }}
{{- $_ := set $scConfig "provider" $inputConfig.provider }}
{{- end }}
{{- if $inputConfig.keys }}
{{- $_ := set $scConfig "keys" $inputConfig.keys }}
{{- end }}
{{- if $inputConfig.embedding_model }}
{{- $_ := set $scConfig "embedding_model" $inputConfig.embedding_model }}
{{- end }}
{{- if $inputConfig.dimension }}
{{- $_ := set $scConfig "dimension" $inputConfig.dimension }}
{{- end }}
{{- if $inputConfig.threshold }}
{{- $_ := set $scConfig "threshold" $inputConfig.threshold }}
{{- end }}
{{- if $inputConfig.ttl }}
{{- $_ := set $scConfig "ttl" $inputConfig.ttl }}
{{- end }}
{{- if $inputConfig.vector_store_namespace }}
{{- $_ := set $scConfig "vector_store_namespace" $inputConfig.vector_store_namespace }}
{{- end }}
{{- if hasKey $inputConfig "conversation_history_threshold" }}
{{- $_ := set $scConfig "conversation_history_threshold" $inputConfig.conversation_history_threshold }}
{{- end }}
{{- if hasKey $inputConfig "cache_by_model" }}
{{- $_ := set $scConfig "cache_by_model" $inputConfig.cache_by_model }}
{{- end }}
{{- if hasKey $inputConfig "cache_by_provider" }}
{{- $_ := set $scConfig "cache_by_provider" $inputConfig.cache_by_provider }}
{{- end }}
{{- if hasKey $inputConfig "exclude_system_prompt" }}
{{- $_ := set $scConfig "exclude_system_prompt" $inputConfig.exclude_system_prompt }}
{{- end }}
{{- if hasKey $inputConfig "cleanup_on_shutdown" }}
{{- $_ := set $scConfig "cleanup_on_shutdown" $inputConfig.cleanup_on_shutdown }}
{{- end }}
{{- $plugins = append $plugins (dict "enabled" true "name" "semanticcache" "config" $scConfig) }}
{{- end }}
{{- if .Values.bifrost.plugins.otel.enabled }}
{{- $otelConfig := dict }}
{{- $inputConfig := .Values.bifrost.plugins.otel.config | default dict }}
{{- if $inputConfig.service_name }}
{{- $_ := set $otelConfig "service_name" $inputConfig.service_name }}
{{- end }}
{{- if $inputConfig.collector_url }}
{{- $_ := set $otelConfig "collector_url" $inputConfig.collector_url }}
{{- end }}
{{- if $inputConfig.trace_type }}
{{- $_ := set $otelConfig "trace_type" $inputConfig.trace_type }}
{{- end }}
{{- if $inputConfig.protocol }}
{{- $_ := set $otelConfig "protocol" $inputConfig.protocol }}
{{- end }}
{{- $plugins = append $plugins (dict "enabled" true "name" "otel" "config" $otelConfig) }}
{{- end }}
{{- if $plugins }}
{{- $_ := set $config "plugins" $plugins }}
{{- end }}
{{- $config | toJson }}
{{- end }}
