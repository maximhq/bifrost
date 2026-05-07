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

{{- define "bifrost.serverSelectorLabels" -}}
{{ include "bifrost.selectorLabels" . }}
app.kubernetes.io/component: server
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

{{- define "bifrost.pinecone.apiKey" -}}
{{- if .Values.vectorStore.pinecone.external.enabled -}}
{{- if .Values.vectorStore.pinecone.external.existingSecret -}}
env.BIFROST_PINECONE_API_KEY
{{- else -}}
{{- .Values.vectorStore.pinecone.external.apiKey -}}
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
{{- $config := dict "$schema" "https://www.getbifrost.ai/schema" }}
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
{{- if hasKey .Values.bifrost.client "enforceAuthOnInference" }}
{{- $_ := set $client "enforce_auth_on_inference" .Values.bifrost.client.enforceAuthOnInference }}
{{- end }}
{{- if hasKey .Values.bifrost.client "enforceGovernanceHeader" }}
{{- $_ := set $client "enforce_governance_header" .Values.bifrost.client.enforceGovernanceHeader }}
{{- end }}
{{- if .Values.bifrost.client.maxRequestBodySizeMb }}
{{- $_ := set $client "max_request_body_size_mb" .Values.bifrost.client.maxRequestBodySizeMb }}
{{- end }}
{{- if .Values.bifrost.client.compat }}
{{- $compat := dict }}
{{- if hasKey .Values.bifrost.client.compat "convertTextToChat" }}
{{- $_ := set $compat "convert_text_to_chat" .Values.bifrost.client.compat.convertTextToChat }}
{{- end }}
{{- if hasKey .Values.bifrost.client.compat "convertChatToResponses" }}
{{- $_ := set $compat "convert_chat_to_responses" .Values.bifrost.client.compat.convertChatToResponses }}
{{- end }}
{{- if hasKey .Values.bifrost.client.compat "shouldDropParams" }}
{{- $_ := set $compat "should_drop_params" .Values.bifrost.client.compat.shouldDropParams }}
{{- end }}
{{- if hasKey .Values.bifrost.client.compat "shouldConvertParams" }}
{{- $_ := set $compat "should_convert_params" .Values.bifrost.client.compat.shouldConvertParams }}
{{- end }}
{{- $_ := set $client "compat" $compat }}
{{- end }}
{{- if .Values.bifrost.client.prometheusLabels }}
{{- $_ := set $client "prometheus_labels" .Values.bifrost.client.prometheusLabels }}
{{- end }}
{{- if hasKey .Values.bifrost.client "disableContentLogging" }}
{{- $_ := set $client "disable_content_logging" .Values.bifrost.client.disableContentLogging }}
{{- end }}
{{- if hasKey .Values.bifrost.client "allowPerRequestContentStorageOverride" }}
{{- $_ := set $client "allow_per_request_content_storage_override" .Values.bifrost.client.allowPerRequestContentStorageOverride }}
{{- end }}
{{- if hasKey .Values.bifrost.client "allowPerRequestRawOverride" }}
{{- $_ := set $client "allow_per_request_raw_override" .Values.bifrost.client.allowPerRequestRawOverride }}
{{- end }}
{{- if .Values.bifrost.client.logRetentionDays }}
{{- $_ := set $client "log_retention_days" .Values.bifrost.client.logRetentionDays }}
{{- end }}
{{- if hasKey .Values.bifrost.client "disableDbPingsInHealth" }}
{{- $_ := set $client "disable_db_pings_in_health" .Values.bifrost.client.disableDbPingsInHealth }}
{{- end }}
{{- if .Values.bifrost.client.headerFilterConfig }}
{{- $headerFilter := dict }}
{{- if .Values.bifrost.client.headerFilterConfig.allowlist }}
{{- $_ := set $headerFilter "allowlist" .Values.bifrost.client.headerFilterConfig.allowlist }}
{{- end }}
{{- if .Values.bifrost.client.headerFilterConfig.denylist }}
{{- $_ := set $headerFilter "denylist" .Values.bifrost.client.headerFilterConfig.denylist }}
{{- end }}
{{- if or $headerFilter.allowlist $headerFilter.denylist }}
{{- $_ := set $client "header_filter_config" $headerFilter }}
{{- end }}
{{- end }}
{{- if .Values.bifrost.client.asyncJobResultTTL }}
{{- $_ := set $client "async_job_result_ttl" .Values.bifrost.client.asyncJobResultTTL }}
{{- end }}
{{- if .Values.bifrost.client.requiredHeaders }}
{{- $_ := set $client "required_headers" .Values.bifrost.client.requiredHeaders }}
{{- end }}
{{- if .Values.bifrost.client.loggingHeaders }}
{{- $_ := set $client "logging_headers" .Values.bifrost.client.loggingHeaders }}
{{- end }}
{{- if .Values.bifrost.client.whitelistedRoutes }}
{{- $_ := set $client "whitelisted_routes" .Values.bifrost.client.whitelistedRoutes }}
{{- end }}
{{- if .Values.bifrost.client.allowedHeaders }}
{{- $_ := set $client "allowed_headers" .Values.bifrost.client.allowedHeaders }}
{{- end }}
{{- if .Values.bifrost.client.mcpAgentDepth }}
{{- $_ := set $client "mcp_agent_depth" .Values.bifrost.client.mcpAgentDepth }}
{{- end }}
{{- if .Values.bifrost.client.mcpToolExecutionTimeout }}
{{- $_ := set $client "mcp_tool_execution_timeout" .Values.bifrost.client.mcpToolExecutionTimeout }}
{{- end }}
{{- if .Values.bifrost.client.mcpCodeModeBindingLevel }}
{{- $_ := set $client "mcp_code_mode_binding_level" .Values.bifrost.client.mcpCodeModeBindingLevel }}
{{- end }}
{{- if hasKey .Values.bifrost.client "mcpToolSyncInterval" }}
{{- $_ := set $client "mcp_tool_sync_interval" .Values.bifrost.client.mcpToolSyncInterval }}
{{- end }}
{{- if hasKey .Values.bifrost.client "hideDeletedVirtualKeysInFilters" }}
{{- $_ := set $client "hide_deleted_virtual_keys_in_filters" .Values.bifrost.client.hideDeletedVirtualKeysInFilters }}
{{- end }}
{{- if hasKey .Values.bifrost.client "mcpDisableAutoToolInject" }}
{{- $_ := set $client "mcp_disable_auto_tool_inject" .Values.bifrost.client.mcpDisableAutoToolInject }}
{{- end }}
{{- if .Values.bifrost.client.routingChainMaxDepth }}
{{- $_ := set $client "routing_chain_max_depth" .Values.bifrost.client.routingChainMaxDepth }}
{{- end }}
{{- if hasKey .Values.bifrost.client "mcpExternalBaseUrl" }}
{{- $mcpExternalBaseUrl := .Values.bifrost.client.mcpExternalBaseUrl }}
{{- if kindIs "map" $mcpExternalBaseUrl }}
{{- $envVar := dict }}
{{- if hasKey $mcpExternalBaseUrl "value" }}
{{- $_ := set $envVar "value" $mcpExternalBaseUrl.value }}
{{- end }}
{{- if hasKey $mcpExternalBaseUrl "envVar" }}
{{- $_ := set $envVar "env_var" $mcpExternalBaseUrl.envVar }}
{{- end }}
{{- if hasKey $mcpExternalBaseUrl "fromEnv" }}
{{- $_ := set $envVar "from_env" $mcpExternalBaseUrl.fromEnv }}
{{- end }}
{{- $_ := set $client "mcp_external_base_url" $envVar }}
{{- else }}
{{- $_ := set $client "mcp_external_base_url" $mcpExternalBaseUrl }}
{{- end }}
{{- end }}
{{- $_ := set $config "client" $client }}
{{- end }}
{{- /* Framework */ -}}
{{- if .Values.bifrost.framework }}
{{- $framework := dict }}
{{- if .Values.bifrost.framework.pricing }}
{{- $pricing := dict }}
{{- if .Values.bifrost.framework.pricing.pricingUrl }}
{{- $_ := set $pricing "pricing_url" .Values.bifrost.framework.pricing.pricingUrl }}
{{- end }}
{{- if .Values.bifrost.framework.pricing.pricingSyncInterval }}
{{- $_ := set $pricing "pricing_sync_interval" .Values.bifrost.framework.pricing.pricingSyncInterval }}
{{- end }}
{{- if or $pricing.pricing_url $pricing.pricing_sync_interval }}
{{- $_ := set $framework "pricing" $pricing }}
{{- end }}
{{- end }}
{{- if $framework }}
{{- $_ := set $config "framework" $framework }}
{{- end }}
{{- end }}
{{- if .Values.bifrost.providers }}
{{- $providers := dict }}
{{- range $providerName, $providerConfig := .Values.bifrost.providers }}
{{- $providerCopy := deepCopy $providerConfig }}
{{- if $providerConfig.network_config }}
{{- $networkConfig := dict }}
{{- if $providerConfig.network_config.base_url }}
{{- $_ := set $networkConfig "base_url" $providerConfig.network_config.base_url }}
{{- end }}
{{- if $providerConfig.network_config.extra_headers }}
{{- $_ := set $networkConfig "extra_headers" $providerConfig.network_config.extra_headers }}
{{- end }}
{{- if hasKey $providerConfig.network_config "default_request_timeout_in_seconds" }}
{{- $_ := set $networkConfig "default_request_timeout_in_seconds" $providerConfig.network_config.default_request_timeout_in_seconds }}
{{- end }}
{{- if hasKey $providerConfig.network_config "max_retries" }}
{{- $_ := set $networkConfig "max_retries" $providerConfig.network_config.max_retries }}
{{- end }}
{{- if hasKey $providerConfig.network_config "retry_backoff_initial" }}
{{- $_ := set $networkConfig "retry_backoff_initial" $providerConfig.network_config.retry_backoff_initial }}
{{- end }}
{{- if hasKey $providerConfig.network_config "retry_backoff_initial_ms" }}
{{- $_ := set $networkConfig "retry_backoff_initial" $providerConfig.network_config.retry_backoff_initial_ms }}
{{- end }}
{{- if hasKey $providerConfig.network_config "retry_backoff_max" }}
{{- $_ := set $networkConfig "retry_backoff_max" $providerConfig.network_config.retry_backoff_max }}
{{- end }}
{{- if hasKey $providerConfig.network_config "retry_backoff_max_ms" }}
{{- $_ := set $networkConfig "retry_backoff_max" $providerConfig.network_config.retry_backoff_max_ms }}
{{- end }}
{{- if hasKey $providerConfig.network_config "insecure_skip_verify" }}
{{- $_ := set $networkConfig "insecure_skip_verify" $providerConfig.network_config.insecure_skip_verify }}
{{- end }}
{{- if hasKey $providerConfig.network_config "ca_cert_pem" }}
{{- $_ := set $networkConfig "ca_cert_pem" $providerConfig.network_config.ca_cert_pem }}
{{- end }}
{{- if hasKey $providerConfig.network_config "stream_idle_timeout_in_seconds" }}
{{- $_ := set $networkConfig "stream_idle_timeout_in_seconds" $providerConfig.network_config.stream_idle_timeout_in_seconds }}
{{- end }}
{{- if hasKey $providerConfig.network_config "max_conns_per_host" }}
{{- $_ := set $networkConfig "max_conns_per_host" $providerConfig.network_config.max_conns_per_host }}
{{- end }}
{{- if hasKey $providerConfig.network_config "enforce_http2" }}
{{- $_ := set $networkConfig "enforce_http2" $providerConfig.network_config.enforce_http2 }}
{{- end }}
{{- if $providerConfig.network_config.beta_header_overrides }}
{{- $_ := set $networkConfig "beta_header_overrides" $providerConfig.network_config.beta_header_overrides }}
{{- end }}
{{- $_ := set $providerCopy "network_config" $networkConfig }}
{{- end }}
{{- if $providerConfig.keys }}
{{- $keys := list }}
{{- range $key := $providerConfig.keys }}
{{- $keyCopy := deepCopy $key }}
{{- if and (not (hasKey $keyCopy "id")) (hasKey $keyCopy "name") $keyCopy.name }}
{{- $_ := set $keyCopy "id" $keyCopy.name }}
{{- end }}
{{- if not (hasKey $keyCopy "weight") }}
{{- $_ := set $keyCopy "weight" 1 }}
{{- end }}
{{- $keys = append $keys $keyCopy }}
{{- end }}
{{- $_ := set $providerCopy "keys" $keys }}
{{- end }}
{{- $_ := set $providers $providerName $providerCopy }}
{{- end }}
{{- $_ := set $config "providers" $providers }}
{{- end }}
{{- /* Governance */ -}}
{{- if .Values.bifrost.governance }}
{{- $governance := dict }}
{{- if .Values.bifrost.governance.budgets }}
{{- $_ := set $governance "budgets" .Values.bifrost.governance.budgets }}
{{- end }}
{{- if .Values.bifrost.governance.rateLimits }}
{{- $rateLimits := list }}
{{- range .Values.bifrost.governance.rateLimits }}
{{- $rl := dict "id" .id }}
{{- if .token_max_limit }}{{- $_ := set $rl "token_max_limit" .token_max_limit }}{{- end }}
{{- if .token_reset_duration }}{{- $_ := set $rl "token_reset_duration" .token_reset_duration }}{{- end }}
{{- if .request_max_limit }}{{- $_ := set $rl "request_max_limit" .request_max_limit }}{{- end }}
{{- if .request_reset_duration }}{{- $_ := set $rl "request_reset_duration" .request_reset_duration }}{{- end }}
{{- $rateLimits = append $rateLimits $rl }}
{{- end }}
{{- $_ := set $governance "rate_limits" $rateLimits }}
{{- end }}
{{- if .Values.bifrost.governance.customers }}
{{- $_ := set $governance "customers" .Values.bifrost.governance.customers }}
{{- end }}
{{- if .Values.bifrost.governance.teams }}
{{- $_ := set $governance "teams" .Values.bifrost.governance.teams }}
{{- end }}
{{- if .Values.bifrost.governance.businessUnits }}
{{- $businessUnits := list }}
{{- range .Values.bifrost.governance.businessUnits }}
{{- $bu := dict "id" .id "name" .name }}
{{- if .budget_id }}{{- $_ := set $bu "budget_id" .budget_id }}{{- end }}
{{- if .rate_limit_id }}{{- $_ := set $bu "rate_limit_id" .rate_limit_id }}{{- end }}
{{- if .profile }}{{- $_ := set $bu "profile" .profile }}{{- end }}
{{- if .config }}{{- $_ := set $bu "config" .config }}{{- end }}
{{- if .claims }}{{- $_ := set $bu "claims" .claims }}{{- end }}
{{- if .teamIds }}{{- $_ := set $bu "team_ids" .teamIds }}{{- end }}
{{- $businessUnits = append $businessUnits $bu }}
{{- end }}
{{- $_ := set $governance "business_units" $businessUnits }}
{{- end }}
{{- if .Values.bifrost.governance.virtualKeys }}
{{- $vks := list }}
{{- range .Values.bifrost.governance.virtualKeys }}
{{- $vk := dict "id" .id "name" .name }}
{{- if .value }}{{- $_ := set $vk "value" .value }}{{- end }}
{{- if .description }}{{- $_ := set $vk "description" .description }}{{- end }}
{{- if hasKey . "is_active" }}{{- $_ := set $vk "is_active" .is_active }}{{- end }}
{{- if .team_id }}{{- $_ := set $vk "team_id" .team_id }}{{- end }}
{{- if .customer_id }}{{- $_ := set $vk "customer_id" .customer_id }}{{- end }}
{{- if .rate_limit_id }}{{- $_ := set $vk "rate_limit_id" .rate_limit_id }}{{- end }}
{{- if .provider_configs }}{{- $_ := set $vk "provider_configs" .provider_configs }}{{- end }}
{{- if .mcp_configs }}{{- $_ := set $vk "mcp_configs" .mcp_configs }}{{- end }}
{{- $vks = append $vks $vk }}
{{- end }}
{{- $_ := set $governance "virtual_keys" $vks }}
{{- end }}
{{- if .Values.bifrost.governance.routingRules }}
{{- $_ := set $governance "routing_rules" .Values.bifrost.governance.routingRules }}
{{- end }}
{{- if .Values.bifrost.governance.modelConfigs }}
{{- $_ := set $governance "model_configs" .Values.bifrost.governance.modelConfigs }}
{{- end }}
{{- if .Values.bifrost.governance.providers }}
{{- $_ := set $governance "providers" .Values.bifrost.governance.providers }}
{{- end }}
{{- if .Values.bifrost.governance.pricingOverrides }}
{{- $_ := set $governance "pricing_overrides" .Values.bifrost.governance.pricingOverrides }}
{{- end }}
{{- if .Values.bifrost.governance.authConfig }}
{{- $authConfig := dict }}
{{- if and .Values.bifrost.governance.authConfig.existingSecret .Values.bifrost.governance.authConfig.usernameKey }}
{{- $_ := set $authConfig "admin_username" "env.BIFROST_ADMIN_USERNAME" }}
{{- else if .Values.bifrost.governance.authConfig.adminUsername }}
{{- $_ := set $authConfig "admin_username" .Values.bifrost.governance.authConfig.adminUsername }}
{{- end }}
{{- if and .Values.bifrost.governance.authConfig.existingSecret .Values.bifrost.governance.authConfig.passwordKey }}
{{- $_ := set $authConfig "admin_password" "env.BIFROST_ADMIN_PASSWORD" }}
{{- else if .Values.bifrost.governance.authConfig.adminPassword }}
{{- $_ := set $authConfig "admin_password" .Values.bifrost.governance.authConfig.adminPassword }}
{{- end }}
{{- if hasKey .Values.bifrost.governance.authConfig "isEnabled" }}
{{- $_ := set $authConfig "is_enabled" .Values.bifrost.governance.authConfig.isEnabled }}
{{- end }}
{{- if hasKey .Values.bifrost.governance.authConfig "disableAuthOnInference" }}
{{- $_ := set $authConfig "disable_auth_on_inference" .Values.bifrost.governance.authConfig.disableAuthOnInference }}
{{- end }}
{{- if or $authConfig.admin_username $authConfig.admin_password $authConfig.is_enabled $authConfig.disable_auth_on_inference }}
{{- $_ := set $governance "auth_config" $authConfig }}
{{- end }}
{{- end }}
{{- if or $governance.budgets $governance.rate_limits $governance.customers $governance.teams $governance.business_units $governance.virtual_keys $governance.routing_rules $governance.model_configs $governance.providers $governance.pricing_overrides $governance.auth_config }}
{{- $_ := set $config "governance" $governance }}
{{- end }}
{{- end }}
{{- /* Top-level Auth Config - for main Bifrost authentication */ -}}
{{- if .Values.bifrost.authConfig }}
{{- $authConfig := dict }}
{{- /* Only use env var reference if governance auth secret is NOT already configured (to avoid referencing uninjected env vars) */ -}}
{{- if and .Values.bifrost.authConfig.existingSecret .Values.bifrost.authConfig.usernameKey (not (and .Values.bifrost.governance .Values.bifrost.governance.authConfig .Values.bifrost.governance.authConfig.existingSecret)) }}
{{- $_ := set $authConfig "admin_username" "env.BIFROST_ADMIN_USERNAME" }}
{{- else if .Values.bifrost.authConfig.adminUsername }}
{{- $_ := set $authConfig "admin_username" .Values.bifrost.authConfig.adminUsername }}
{{- end }}
{{- if and .Values.bifrost.authConfig.existingSecret .Values.bifrost.authConfig.passwordKey (not (and .Values.bifrost.governance .Values.bifrost.governance.authConfig .Values.bifrost.governance.authConfig.existingSecret)) }}
{{- $_ := set $authConfig "admin_password" "env.BIFROST_ADMIN_PASSWORD" }}
{{- else if .Values.bifrost.authConfig.adminPassword }}
{{- $_ := set $authConfig "admin_password" .Values.bifrost.authConfig.adminPassword }}
{{- end }}
{{- if hasKey .Values.bifrost.authConfig "isEnabled" }}
{{- $_ := set $authConfig "is_enabled" .Values.bifrost.authConfig.isEnabled }}
{{- end }}
{{- if hasKey .Values.bifrost.authConfig "disableAuthOnInference" }}
{{- $_ := set $authConfig "disable_auth_on_inference" .Values.bifrost.authConfig.disableAuthOnInference }}
{{- end }}
{{- if or $authConfig.admin_username $authConfig.admin_password $authConfig.is_enabled $authConfig.disable_auth_on_inference }}
{{- $_ := set $config "auth_config" $authConfig }}
{{- end }}
{{- end }}
{{- /* Cluster Config */ -}}
{{- if and .Values.bifrost.cluster .Values.bifrost.cluster.enabled }}
{{- $cluster := dict "enabled" true }}
{{- if .Values.bifrost.cluster.peers }}
{{- $_ := set $cluster "peers" .Values.bifrost.cluster.peers }}
{{- end }}
{{- if .Values.bifrost.cluster.region }}
{{- $_ := set $cluster "region" .Values.bifrost.cluster.region }}
{{- end }}
{{- if .Values.bifrost.cluster.gossip }}
{{- $gossip := dict }}
{{- if .Values.bifrost.cluster.gossip.port }}
{{- $_ := set $gossip "port" .Values.bifrost.cluster.gossip.port }}
{{- end }}
{{- if .Values.bifrost.cluster.gossip.config }}
{{- $gossipConfig := dict }}
{{- if .Values.bifrost.cluster.gossip.config.timeoutSeconds }}
{{- $_ := set $gossipConfig "timeout_seconds" .Values.bifrost.cluster.gossip.config.timeoutSeconds }}
{{- end }}
{{- if .Values.bifrost.cluster.gossip.config.successThreshold }}
{{- $_ := set $gossipConfig "success_threshold" .Values.bifrost.cluster.gossip.config.successThreshold }}
{{- end }}
{{- if .Values.bifrost.cluster.gossip.config.failureThreshold }}
{{- $_ := set $gossipConfig "failure_threshold" .Values.bifrost.cluster.gossip.config.failureThreshold }}
{{- end }}
{{- $_ := set $gossip "config" $gossipConfig }}
{{- end }}
{{- $_ := set $cluster "gossip" $gossip }}
{{- end }}
{{- if .Values.bifrost.cluster.grpc }}
{{- $grpc := dict }}
{{- if .Values.bifrost.cluster.grpc.port }}
{{- $_ := set $grpc "port" .Values.bifrost.cluster.grpc.port }}
{{- end }}
{{- if .Values.bifrost.cluster.grpc.dialTimeoutSeconds }}
{{- $_ := set $grpc "dial_timeout_seconds" .Values.bifrost.cluster.grpc.dialTimeoutSeconds }}
{{- end }}
{{- if $grpc }}
{{- $_ := set $cluster "grpc" $grpc }}
{{- end }}
{{- end }}
{{- if and .Values.bifrost.cluster.discovery .Values.bifrost.cluster.discovery.enabled }}
{{- $discovery := dict "enabled" true "type" .Values.bifrost.cluster.discovery.type }}
{{- $serviceName := .Values.bifrost.cluster.discovery.serviceName }}
{{- if and (not $serviceName) (or (eq .Values.bifrost.cluster.discovery.type "consul") (eq .Values.bifrost.cluster.discovery.type "etcd") (eq .Values.bifrost.cluster.discovery.type "udp")) }}
{{- fail "ERROR: bifrost.cluster.discovery.serviceName is required for consul/etcd/udp discovery." }}
{{- end }}
{{- if $serviceName }}
{{- $_ := set $discovery "service_name" $serviceName }}
{{- end }}
{{- if .Values.bifrost.cluster.discovery.bindPort }}
{{- $_ := set $discovery "bind_port" .Values.bifrost.cluster.discovery.bindPort }}
{{- end }}
{{- if .Values.bifrost.cluster.discovery.dialTimeout }}
{{- $_ := set $discovery "dial_timeout" .Values.bifrost.cluster.discovery.dialTimeout }}
{{- end }}
{{- if .Values.bifrost.cluster.discovery.allowedAddressSpace }}
{{- $_ := set $discovery "allowed_address_space" .Values.bifrost.cluster.discovery.allowedAddressSpace }}
{{- end }}
{{- if .Values.bifrost.cluster.discovery.k8sNamespace }}
{{- $_ := set $discovery "k8s_namespace" .Values.bifrost.cluster.discovery.k8sNamespace }}
{{- end }}
{{- if .Values.bifrost.cluster.discovery.k8sLabelSelector }}
{{- $_ := set $discovery "k8s_label_selector" .Values.bifrost.cluster.discovery.k8sLabelSelector }}
{{- end }}
{{- if .Values.bifrost.cluster.discovery.dnsNames }}
{{- $_ := set $discovery "dns_names" .Values.bifrost.cluster.discovery.dnsNames }}
{{- end }}
{{- if .Values.bifrost.cluster.discovery.udpBroadcastPort }}
{{- $_ := set $discovery "udp_broadcast_port" .Values.bifrost.cluster.discovery.udpBroadcastPort }}
{{- end }}
{{- if .Values.bifrost.cluster.discovery.consulAddress }}
{{- $_ := set $discovery "consul_address" .Values.bifrost.cluster.discovery.consulAddress }}
{{- end }}
{{- if .Values.bifrost.cluster.discovery.etcdEndpoints }}
{{- $_ := set $discovery "etcd_endpoints" .Values.bifrost.cluster.discovery.etcdEndpoints }}
{{- end }}
{{- if .Values.bifrost.cluster.discovery.mdnsService }}
{{- $_ := set $discovery "mdns_service" .Values.bifrost.cluster.discovery.mdnsService }}
{{- end }}
{{- $_ := set $cluster "discovery" $discovery }}
{{- end }}
{{- $_ := set $config "cluster_config" $cluster }}
{{- end }}
{{- /* SCIM Config */ -}}
{{- $scimValues := .Values.bifrost.scim }}
{{- if and $scimValues $scimValues.enabled }}
{{- $scim := dict "enabled" true }}
{{- if $scimValues.provider }}
{{- $_ := set $scim "provider" $scimValues.provider }}
{{- end }}
{{- if $scimValues.config }}
{{- $_ := set $scim "config" $scimValues.config }}
{{- end }}
{{- $_ := set $config "scim_config" $scim }}
{{- end }}
{{- /* Load Balancer Config */ -}}
{{- if and .Values.bifrost.loadBalancer .Values.bifrost.loadBalancer.enabled }}
{{- $lb := dict "enabled" true }}
{{- if .Values.bifrost.loadBalancer.trackerConfig }}
{{- $_ := set $lb "tracker_config" .Values.bifrost.loadBalancer.trackerConfig }}
{{- end }}
{{- if .Values.bifrost.loadBalancer.bootstrap }}
{{- $_ := set $lb "bootstrap" .Values.bifrost.loadBalancer.bootstrap }}
{{- end }}
{{- $_ := set $config "load_balancer_config" $lb }}
{{- end }}
{{- /* Guardrails Config */ -}}
{{- if .Values.bifrost.guardrails }}
{{- $guardrails := dict }}
{{- if .Values.bifrost.guardrails.rules }}
{{- $rules := list }}
{{- range .Values.bifrost.guardrails.rules }}
{{- $rule := dict "id" .id "name" .name "enabled" .enabled "cel_expression" .cel_expression "apply_to" .apply_to }}
{{- if .description }}{{- $_ := set $rule "description" .description }}{{- end }}
{{- if hasKey . "query" }}{{- $_ := set $rule "query" .query }}{{- end }}
{{- if .sampling_rate }}{{- $_ := set $rule "sampling_rate" .sampling_rate }}{{- end }}
{{- if .timeout }}{{- $_ := set $rule "timeout" .timeout }}{{- end }}
{{- if .provider_config_ids }}{{- $_ := set $rule "provider_config_ids" .provider_config_ids }}{{- end }}
{{- $rules = append $rules $rule }}
{{- end }}
{{- $_ := set $guardrails "guardrail_rules" $rules }}
{{- end }}
{{- if .Values.bifrost.guardrails.providers }}
{{- $providers := list }}
{{- range .Values.bifrost.guardrails.providers }}
{{- $provider := dict "id" .id "provider_name" .provider_name "policy_name" .policy_name "enabled" .enabled }}
{{- if .timeout }}{{- $_ := set $provider "timeout" .timeout }}{{- end }}
{{- if .config }}{{- $_ := set $provider "config" .config }}{{- end }}
{{- $providers = append $providers $provider }}
{{- end }}
{{- $_ := set $guardrails "guardrail_providers" $providers }}
{{- end }}
{{- if or $guardrails.guardrail_rules $guardrails.guardrail_providers }}
{{- $_ := set $config "guardrails_config" $guardrails }}
{{- end }}
{{- end }}
{{- /* Access Profiles (Enterprise) */ -}}
{{- if .Values.bifrost.accessProfiles }}
{{- $_ := set $config "access_profiles" .Values.bifrost.accessProfiles }}
{{- end }}
{{- /* Config Store */ -}}
{{- if .Values.storage.configStore.enabled }}
{{- $configStoreType := .Values.storage.configStore.type | default .Values.storage.mode }}
{{- if eq $configStoreType "postgres" }}
{{- $pgConfig := dict "host" (include "bifrost.postgresql.host" .) "port" (include "bifrost.postgresql.port" .) "db_name" (include "bifrost.postgresql.database" .) "user" (include "bifrost.postgresql.username" .) "password" (include "bifrost.postgresql.password" .) "ssl_mode" (include "bifrost.postgresql.sslMode" .) }}
{{- if .Values.storage.configStore.maxIdleConns }}
{{- $_ := set $pgConfig "max_idle_conns" (.Values.storage.configStore.maxIdleConns | int) }}
{{- end }}
{{- if .Values.storage.configStore.maxOpenConns }}
{{- $_ := set $pgConfig "max_open_conns" (.Values.storage.configStore.maxOpenConns | int) }}
{{- end }}
{{- $configStore := dict "enabled" true "type" "postgres" "config" $pgConfig }}
{{- $_ := set $config "config_store" $configStore }}
{{- else }}
{{- $sqliteConfigStore := dict "enabled" true "type" "sqlite" "config" (dict "path" (printf "%s/config.db" .Values.bifrost.appDir)) }}
{{- $_ := set $config "config_store" $sqliteConfigStore }}
{{- end }}
{{- end }}
{{- /* Logs Store */ -}}
{{- if .Values.storage.logsStore.enabled }}
{{- $logsStoreType := .Values.storage.logsStore.type | default .Values.storage.mode }}
{{- if eq $logsStoreType "postgres" }}
{{- $pgConfig := dict "host" (include "bifrost.postgresql.host" .) "port" (include "bifrost.postgresql.port" .) "db_name" (include "bifrost.postgresql.database" .) "user" (include "bifrost.postgresql.username" .) "password" (include "bifrost.postgresql.password" .) "ssl_mode" (include "bifrost.postgresql.sslMode" .) }}
{{- if .Values.storage.logsStore.maxIdleConns }}
{{- $_ := set $pgConfig "max_idle_conns" (.Values.storage.logsStore.maxIdleConns | int) }}
{{- end }}
{{- if .Values.storage.logsStore.maxOpenConns }}
{{- $_ := set $pgConfig "max_open_conns" (.Values.storage.logsStore.maxOpenConns | int) }}
{{- end }}
{{- $logsStore := dict "enabled" true "type" "postgres" "config" $pgConfig }}
{{- $_ := set $config "logs_store" $logsStore }}
{{- else }}
{{- $sqliteLogsStore := dict "enabled" true "type" "sqlite" "config" (dict "path" (printf "%s/logs.db" .Values.bifrost.appDir)) }}
{{- $_ := set $config "logs_store" $sqliteLogsStore }}
{{- end }}
{{- /* Object Storage for log payloads */ -}}
{{- if and .Values.storage.logsStore.objectStorage .Values.storage.logsStore.objectStorage.enabled }}
{{- $os := .Values.storage.logsStore.objectStorage }}
{{- $osConfig := dict "type" $os.type "bucket" $os.bucket }}
{{- if $os.prefix }}
{{- $_ := set $osConfig "prefix" $os.prefix }}
{{- end }}
{{- if $os.compress }}
{{- $_ := set $osConfig "compress" true }}
{{- end }}
{{- if eq $os.type "s3" }}
{{- if $os.region }}
{{- $_ := set $osConfig "region" $os.region }}
{{- end }}
{{- if $os.endpoint }}
{{- $_ := set $osConfig "endpoint" $os.endpoint }}
{{- end }}
{{- if $os.existingSecret }}
{{- if $os.accessKeyIdKey }}
{{- $_ := set $osConfig "access_key_id" "env.BIFROST_OBJECT_STORAGE_ACCESS_KEY_ID" }}
{{- end }}
{{- if $os.secretAccessKeyKey }}
{{- $_ := set $osConfig "secret_access_key" "env.BIFROST_OBJECT_STORAGE_SECRET_ACCESS_KEY" }}
{{- end }}
{{- if $os.sessionTokenKey }}
{{- $_ := set $osConfig "session_token" "env.BIFROST_OBJECT_STORAGE_SESSION_TOKEN" }}
{{- end }}
{{- $_ := set $osConfig "role_arn" "env.BIFROST_OBJECT_STORAGE_ROLE_ARN" }}
{{- else }}
{{- if $os.accessKeyId }}
{{- $_ := set $osConfig "access_key_id" $os.accessKeyId }}
{{- end }}
{{- if $os.secretAccessKey }}
{{- $_ := set $osConfig "secret_access_key" $os.secretAccessKey }}
{{- end }}
{{- if $os.sessionToken }}
{{- $_ := set $osConfig "session_token" $os.sessionToken }}
{{- end }}
{{- if $os.roleArn }}
{{- $_ := set $osConfig "role_arn" $os.roleArn }}
{{- end }}
{{- end }}
{{- if $os.forcePathStyle }}
{{- $_ := set $osConfig "force_path_style" true }}
{{- end }}
{{- end }}
{{- if eq $os.type "gcs" }}
{{- if $os.projectId }}
{{- $_ := set $osConfig "project_id" $os.projectId }}
{{- end }}
{{- if $os.existingSecret }}
{{- $_ := set $osConfig "credentials_json" "env.BIFROST_OBJECT_STORAGE_CREDENTIALS_JSON" }}
{{- else if $os.credentialsJson }}
{{- $_ := set $osConfig "credentials_json" $os.credentialsJson }}
{{- end }}
{{- end }}
{{- $_ := set (index $config "logs_store") "object_storage" $osConfig }}
{{- end }}
{{- if .Values.storage.logsStore.objectStorageExcludeFields }}
{{- $_ := set (index $config "logs_store") "object_storage_exclude_fields" .Values.storage.logsStore.objectStorageExcludeFields }}
{{- end }}
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
{{- if .Values.vectorStore.weaviate.external.timeout }}
{{- $_ := set $weaviateConfig "timeout" .Values.vectorStore.weaviate.external.timeout }}
{{- end }}
{{- if .Values.vectorStore.weaviate.external.className }}
{{- $_ := set $weaviateConfig "class_name" .Values.vectorStore.weaviate.external.className }}
{{- end }}
{{- end }}
{{- $_ := set $vectorStore "config" $weaviateConfig }}
{{- else if eq .Values.vectorStore.type "redis" }}
{{- $redisConfig := dict "addr" (printf "%s:%s" (include "bifrost.redis.host" .) (include "bifrost.redis.port" .)) }}
{{- $password := include "bifrost.redis.password" . }}
{{- if $password }}
{{- $_ := set $redisConfig "password" $password }}
{{- end }}
{{- if .Values.vectorStore.redis.external.enabled }}
{{- if .Values.vectorStore.redis.external.username }}
{{- $_ := set $redisConfig "username" .Values.vectorStore.redis.external.username }}
{{- end }}
{{- if .Values.vectorStore.redis.external.database }}
{{- $_ := set $redisConfig "db" .Values.vectorStore.redis.external.database }}
{{- end }}
{{- if .Values.vectorStore.redis.external.poolSize }}
{{- $_ := set $redisConfig "pool_size" .Values.vectorStore.redis.external.poolSize }}
{{- end }}
{{- if .Values.vectorStore.redis.external.maxActiveConns }}
{{- $_ := set $redisConfig "max_active_conns" .Values.vectorStore.redis.external.maxActiveConns }}
{{- end }}
{{- if .Values.vectorStore.redis.external.minIdleConns }}
{{- $_ := set $redisConfig "min_idle_conns" .Values.vectorStore.redis.external.minIdleConns }}
{{- end }}
{{- if .Values.vectorStore.redis.external.maxIdleConns }}
{{- $_ := set $redisConfig "max_idle_conns" .Values.vectorStore.redis.external.maxIdleConns }}
{{- end }}
{{- if .Values.vectorStore.redis.external.connMaxLifetime }}
{{- $_ := set $redisConfig "conn_max_lifetime" .Values.vectorStore.redis.external.connMaxLifetime }}
{{- end }}
{{- if .Values.vectorStore.redis.external.connMaxIdleTime }}
{{- $_ := set $redisConfig "conn_max_idle_time" .Values.vectorStore.redis.external.connMaxIdleTime }}
{{- end }}
{{- if .Values.vectorStore.redis.external.dialTimeout }}
{{- $_ := set $redisConfig "dial_timeout" .Values.vectorStore.redis.external.dialTimeout }}
{{- end }}
{{- if .Values.vectorStore.redis.external.readTimeout }}
{{- $_ := set $redisConfig "read_timeout" .Values.vectorStore.redis.external.readTimeout }}
{{- end }}
{{- if .Values.vectorStore.redis.external.writeTimeout }}
{{- $_ := set $redisConfig "write_timeout" .Values.vectorStore.redis.external.writeTimeout }}
{{- end }}
{{- if .Values.vectorStore.redis.external.contextTimeout }}
{{- $_ := set $redisConfig "context_timeout" .Values.vectorStore.redis.external.contextTimeout }}
{{- end }}
{{- if .Values.vectorStore.redis.external.useTls }}
{{- $_ := set $redisConfig "use_tls" true }}
{{- end }}
{{- if .Values.vectorStore.redis.external.insecureSkipVerify }}
{{- $_ := set $redisConfig "insecure_skip_verify" true }}
{{- end }}
{{- if .Values.vectorStore.redis.external.caCertPem }}
{{- $_ := set $redisConfig "ca_cert_pem" .Values.vectorStore.redis.external.caCertPem }}
{{- end }}
{{- if .Values.vectorStore.redis.external.clusterMode }}
{{- $_ := set $redisConfig "cluster_mode" true }}
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
{{- else if eq .Values.vectorStore.type "pinecone" }}
{{- $pineconeConfig := dict }}
{{- $apiKey := include "bifrost.pinecone.apiKey" . }}
{{- if $apiKey }}
{{- $_ := set $pineconeConfig "api_key" $apiKey }}
{{- end }}
{{- if .Values.vectorStore.pinecone.external.indexHost }}
{{- $_ := set $pineconeConfig "index_host" .Values.vectorStore.pinecone.external.indexHost }}
{{- end }}
{{- $_ := set $vectorStore "config" $pineconeConfig }}
{{- end }}
{{- $_ := set $config "vector_store" $vectorStore }}
{{- end }}
{{- /* MCP */ -}}
{{- if .Values.bifrost.mcp.enabled }}
{{- $clientConfigs := list }}
{{- range $idx, $client := .Values.bifrost.mcp.clientConfigs }}
{{- $cc := dict "name" $client.name }}
{{- /* Map connectionType: websocket -> sse, others pass through */ -}}
{{- if eq $client.connectionType "websocket" }}
{{- $_ := set $cc "connection_type" "sse" }}
{{- else }}
{{- $_ := set $cc "connection_type" $client.connectionType }}
{{- end }}
{{- /* Map httpConfig.url / websocketConfig.url -> connection_string */ -}}
{{- if and (eq $client.connectionType "http") $client.httpConfig }}
{{- $_ := set $cc "connection_string" $client.httpConfig.url }}
{{- end }}
{{- if and (eq $client.connectionType "websocket") $client.websocketConfig }}
{{- $_ := set $cc "connection_string" $client.websocketConfig.url }}
{{- end }}
{{- /* Map connectionString for SSE connections */ -}}
{{- if and (eq $client.connectionType "sse") $client.connectionString }}
{{- $_ := set $cc "connection_string" $client.connectionString }}
{{- end }}
{{- /* Map stdioConfig -> stdio_config */ -}}
{{- if $client.stdioConfig }}
{{- $stdio := dict "command" $client.stdioConfig.command }}
{{- if $client.stdioConfig.args }}
{{- $_ := set $stdio "args" $client.stdioConfig.args }}
{{- end }}
{{- if $client.stdioConfig.envs }}
{{- $_ := set $stdio "envs" $client.stdioConfig.envs }}
{{- end }}
{{- $_ := set $cc "stdio_config" $stdio }}
{{- end }}
{{- /* Pass through fields that are already snake_case or flat */ -}}
{{- if $client.headers }}
{{- $_ := set $cc "headers" $client.headers }}
{{- end }}
{{- if hasKey $client "tools_to_execute" }}
{{- $_ := set $cc "tools_to_execute" $client.tools_to_execute }}
{{- else if hasKey $client "toolsToExecute" }}
{{- $_ := set $cc "tools_to_execute" $client.toolsToExecute }}
{{- end }}
{{- if hasKey $client "tools_to_auto_execute" }}
{{- $_ := set $cc "tools_to_auto_execute" $client.tools_to_auto_execute }}
{{- else if hasKey $client "toolsToAutoExecute" }}
{{- $_ := set $cc "tools_to_auto_execute" $client.toolsToAutoExecute }}
{{- end }}
{{- if hasKey $client "auth_type" }}
{{- $_ := set $cc "auth_type" $client.auth_type }}
{{- else if hasKey $client "authType" }}
{{- $_ := set $cc "auth_type" $client.authType }}
{{- end }}
{{- if hasKey $client "oauth_config_id" }}
{{- $_ := set $cc "oauth_config_id" $client.oauth_config_id }}
{{- else if hasKey $client "oauthConfigId" }}
{{- $_ := set $cc "oauth_config_id" $client.oauthConfigId }}
{{- end }}
{{- if hasKey $client "isPingAvailable" }}
{{- $_ := set $cc "is_ping_available" $client.isPingAvailable }}
{{- end }}
{{- if $client.clientId }}
{{- $_ := set $cc "client_id" $client.clientId }}
{{- end }}
{{- if hasKey $client "isCodeModeClient" }}
{{- $_ := set $cc "is_code_mode_client" $client.isCodeModeClient }}
{{- end }}
{{- if $client.toolSyncInterval }}
{{- $_ := set $cc "tool_sync_interval" $client.toolSyncInterval }}
{{- end }}
{{- if $client.toolPricing }}
{{- $_ := set $cc "tool_pricing" $client.toolPricing }}
{{- end }}
{{- if $client.allowedExtraHeaders }}
{{- $_ := set $cc "allowed_extra_headers" $client.allowedExtraHeaders }}
{{- end }}
{{- if hasKey $client "allowOnAllVirtualKeys" }}
{{- $_ := set $cc "allow_on_all_virtual_keys" $client.allowOnAllVirtualKeys }}
{{- end }}
{{- /* Override connection_string with env var placeholder when secretRef is set */ -}}
{{- if and $client.secretRef $client.secretRef.name }}
{{- $envName := printf "BIFROST_MCP_%s_CONNECTION_STRING" (regexReplaceAll "[^A-Z0-9]+" (upper $client.name) "_") }}
{{- $_ := set $cc "connection_string" (printf "env.%s" $envName) }}
{{- end }}
{{- $clientConfigs = append $clientConfigs $cc }}
{{- end }}
{{- $mcpConfig := dict "client_configs" $clientConfigs }}
{{- if .Values.bifrost.mcp.toolManagerConfig }}
{{- $tmConfig := dict }}
{{- if .Values.bifrost.mcp.toolManagerConfig.toolExecutionTimeout }}
{{- $_ := set $tmConfig "tool_execution_timeout" .Values.bifrost.mcp.toolManagerConfig.toolExecutionTimeout }}
{{- end }}
{{- if .Values.bifrost.mcp.toolManagerConfig.maxAgentDepth }}
{{- $_ := set $tmConfig "max_agent_depth" .Values.bifrost.mcp.toolManagerConfig.maxAgentDepth }}
{{- end }}
{{- if .Values.bifrost.mcp.toolManagerConfig.codeModeBindingLevel }}
{{- $_ := set $tmConfig "code_mode_binding_level" .Values.bifrost.mcp.toolManagerConfig.codeModeBindingLevel }}
{{- end }}
{{- if hasKey .Values.bifrost.mcp.toolManagerConfig "disableAutoToolInject" }}
{{- $_ := set $tmConfig "disable_auto_tool_inject" .Values.bifrost.mcp.toolManagerConfig.disableAutoToolInject }}
{{- end }}
{{- if $tmConfig }}
{{- $_ := set $mcpConfig "tool_manager_config" $tmConfig }}
{{- end }}
{{- end }}
{{- if hasKey .Values.bifrost.mcp "toolSyncInterval" }}
{{- $_ := set $mcpConfig "tool_sync_interval" .Values.bifrost.mcp.toolSyncInterval }}
{{- end }}
{{- if .Values.bifrost.mcp.toolGroups }}
{{- $toolGroups := list }}
{{- range .Values.bifrost.mcp.toolGroups }}
{{- $group := dict "name" .name }}
{{- if hasKey . "enabled" }}{{- $_ := set $group "enabled" .enabled }}{{- end }}
{{- if .description }}{{- $_ := set $group "description" .description }}{{- end }}
{{- if .tools }}
{{- $tools := list }}
{{- range .tools }}
{{- $tool := dict }}
{{- if .mcpClientId }}{{- $_ := set $tool "mcp_client_id" .mcpClientId }}{{- end }}
{{- if .mcpClientName }}{{- $_ := set $tool "mcp_client_name" .mcpClientName }}{{- end }}
{{- if .toolNames }}{{- $_ := set $tool "tool_names" .toolNames }}{{- end }}
{{- $tools = append $tools $tool }}
{{- end }}
{{- $_ := set $group "tools" $tools }}
{{- end }}
{{- if .virtualKeyIds }}{{- $_ := set $group "virtual_key_ids" .virtualKeyIds }}{{- end }}
{{- if .teamIds }}{{- $_ := set $group "team_ids" .teamIds }}{{- end }}
{{- if .customerIds }}{{- $_ := set $group "customer_ids" .customerIds }}{{- end }}
{{- if .userIds }}{{- $_ := set $group "user_ids" .userIds }}{{- end }}
{{- if .providerNames }}{{- $_ := set $group "provider_names" .providerNames }}{{- end }}
{{- if .apiKeyIds }}{{- $_ := set $group "api_key_ids" .apiKeyIds }}{{- end }}
{{- $toolGroups = append $toolGroups $group }}
{{- end }}
{{- $_ := set $mcpConfig "tool_groups" $toolGroups }}
{{- end }}
{{- $_ := set $config "mcp" $mcpConfig }}
{{- end }}
{{- /* Plugins - as array per schema */ -}}
{{- $plugins := list }}
{{- if .Values.bifrost.plugins.telemetry.enabled }}
{{- $plugin := dict "enabled" true "name" "telemetry" "config" .Values.bifrost.plugins.telemetry.config }}
{{- if hasKey .Values.bifrost.plugins.telemetry "version" }}{{- $_ := set $plugin "version" (.Values.bifrost.plugins.telemetry.version | int) }}{{- end }}
{{- $plugins = append $plugins $plugin }}
{{- end }}
{{- if .Values.bifrost.plugins.logging.enabled }}
{{- $plugin := dict "enabled" true "name" "logging" "config" .Values.bifrost.plugins.logging.config }}
{{- if hasKey .Values.bifrost.plugins.logging "version" }}{{- $_ := set $plugin "version" (.Values.bifrost.plugins.logging.version | int) }}{{- end }}
{{- $plugins = append $plugins $plugin }}
{{- end }}
{{- if .Values.bifrost.plugins.governance.enabled }}
{{- $governanceConfig := dict }}
{{- if hasKey .Values.bifrost.plugins.governance.config "is_vk_mandatory" }}
{{- $_ := set $governanceConfig "is_vk_mandatory" .Values.bifrost.plugins.governance.config.is_vk_mandatory }}
{{- end }}
{{- if .Values.bifrost.plugins.governance.config.required_headers }}
{{- $_ := set $governanceConfig "required_headers" .Values.bifrost.plugins.governance.config.required_headers }}
{{- end }}
{{- if hasKey .Values.bifrost.plugins.governance.config "is_enterprise" }}
{{- $_ := set $governanceConfig "is_enterprise" .Values.bifrost.plugins.governance.config.is_enterprise }}
{{- end }}
{{- $plugin := dict "enabled" true "name" "governance" "config" $governanceConfig }}
{{- if hasKey .Values.bifrost.plugins.governance "version" }}{{- $_ := set $plugin "version" (.Values.bifrost.plugins.governance.version | int) }}{{- end }}
{{- $plugins = append $plugins $plugin }}
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
{{- $plugin := dict "enabled" true "name" "maxim" "config" $maximConfig }}
{{- if hasKey .Values.bifrost.plugins.maxim "version" }}{{- $_ := set $plugin "version" (.Values.bifrost.plugins.maxim.version | int) }}{{- end }}
{{- $plugins = append $plugins $plugin }}
{{- end }}
{{- if .Values.bifrost.plugins.semanticCache.enabled }}
{{- $scConfig := dict }}
{{- $inputConfig := .Values.bifrost.plugins.semanticCache.config | default dict }}
{{- if $inputConfig.dimension }}
{{- $_ := set $scConfig "dimension" $inputConfig.dimension }}
{{- end }}
{{/* Only include embedding provider config when not in direct cache mode (dimension: 1) */}}
{{- if ne (int ($inputConfig.dimension | default 1536)) 1 }}
{{- if $inputConfig.provider }}
{{- $_ := set $scConfig "provider" $inputConfig.provider }}
{{- end }}
{{- if $inputConfig.keys }}
{{- $_ := set $scConfig "keys" $inputConfig.keys }}
{{- end }}
{{- if $inputConfig.embedding_model }}
{{- $_ := set $scConfig "embedding_model" $inputConfig.embedding_model }}
{{- end }}
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
{{- if $inputConfig.default_cache_key }}
{{- $_ := set $scConfig "default_cache_key" $inputConfig.default_cache_key }}
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
{{- $plugin := dict "enabled" true "name" "semantic_cache" "config" $scConfig }}
{{- if hasKey .Values.bifrost.plugins.semanticCache "version" }}{{- $_ := set $plugin "version" (.Values.bifrost.plugins.semanticCache.version | int) }}{{- end }}
{{- $plugins = append $plugins $plugin }}
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
{{- if hasKey $inputConfig "metrics_enabled" }}
{{- $_ := set $otelConfig "metrics_enabled" $inputConfig.metrics_enabled }}
{{- end }}
{{- if $inputConfig.metrics_endpoint }}
{{- $_ := set $otelConfig "metrics_endpoint" $inputConfig.metrics_endpoint }}
{{- end }}
{{- if $inputConfig.metrics_push_interval }}
{{- $_ := set $otelConfig "metrics_push_interval" $inputConfig.metrics_push_interval }}
{{- end }}
{{- if $inputConfig.headers }}
{{- $_ := set $otelConfig "headers" $inputConfig.headers }}
{{- end }}
{{- if $inputConfig.tls_ca_cert }}
{{- $_ := set $otelConfig "tls_ca_cert" $inputConfig.tls_ca_cert }}
{{- end }}
{{- if hasKey $inputConfig "insecure" }}
{{- $_ := set $otelConfig "insecure" $inputConfig.insecure }}
{{- end }}
{{- $plugin := dict "enabled" true "name" "otel" "config" $otelConfig }}
{{- if hasKey .Values.bifrost.plugins.otel "version" }}{{- $_ := set $plugin "version" (.Values.bifrost.plugins.otel.version | int) }}{{- end }}
{{- $plugins = append $plugins $plugin }}
{{- end }}
{{- if .Values.bifrost.plugins.datadog.enabled }}
{{- $datadogConfig := dict }}
{{- $inputConfig := .Values.bifrost.plugins.datadog.config | default dict }}
{{- if $inputConfig.service_name }}
{{- $_ := set $datadogConfig "service_name" $inputConfig.service_name }}
{{- end }}
{{- if $inputConfig.agent_addr }}
{{- $_ := set $datadogConfig "agent_addr" $inputConfig.agent_addr }}
{{- end }}
{{- if $inputConfig.env }}
{{- $_ := set $datadogConfig "env" $inputConfig.env }}
{{- end }}
{{- if $inputConfig.version }}
{{- $_ := set $datadogConfig "version" $inputConfig.version }}
{{- end }}
{{- if $inputConfig.custom_tags }}
{{- $_ := set $datadogConfig "custom_tags" $inputConfig.custom_tags }}
{{- end }}
{{- if hasKey $inputConfig "enable_traces" }}
{{- $_ := set $datadogConfig "enable_traces" $inputConfig.enable_traces }}
{{- end }}
{{- $plugin := dict "enabled" true "name" "datadog" "config" $datadogConfig }}
{{- if hasKey .Values.bifrost.plugins.datadog "version" }}{{- $_ := set $plugin "version" (.Values.bifrost.plugins.datadog.version | int) }}{{- end }}
{{- $plugins = append $plugins $plugin }}
{{- end }}
{{- /* Custom plugins */ -}}
{{- if .Values.bifrost.plugins.custom }}
{{- range .Values.bifrost.plugins.custom }}
{{- $customPlugin := dict "enabled" .enabled "name" .name }}
{{- if .path }}{{- $_ := set $customPlugin "path" .path }}{{- end }}
{{- if hasKey . "version" }}{{- $_ := set $customPlugin "version" (.version | int) }}{{- end }}
{{- if .config }}{{- $_ := set $customPlugin "config" .config }}{{- end }}
{{- if .placement }}{{- $_ := set $customPlugin "placement" .placement }}{{- end }}
{{- if .order }}{{- $_ := set $customPlugin "order" (.order | int) }}{{- end }}
{{- $plugins = append $plugins $customPlugin }}
{{- end }}
{{- end }}
{{- if $plugins }}
{{- $_ := set $config "plugins" $plugins }}
{{- end }}
{{- /* Audit Logs */ -}}
{{- if .Values.bifrost.auditLogs }}
{{- $auditLogs := dict }}
{{- if hasKey .Values.bifrost.auditLogs "disabled" }}
{{- $_ := set $auditLogs "disabled" .Values.bifrost.auditLogs.disabled }}
{{- end }}
{{- if .Values.bifrost.auditLogs.hmacKey }}
{{- $_ := set $auditLogs "hmac_key" .Values.bifrost.auditLogs.hmacKey }}
{{- end }}
{{- if or (hasKey $auditLogs "disabled") $auditLogs.hmac_key }}
{{- $_ := set $config "audit_logs" $auditLogs }}
{{- end }}
{{- end }}
{{- /* Large Payload Optimization */ -}}
{{- if .Values.bifrost.largePayloadOptimization }}
{{- $lpo := dict }}
{{- if hasKey .Values.bifrost.largePayloadOptimization "enabled" }}
{{- $_ := set $lpo "enabled" .Values.bifrost.largePayloadOptimization.enabled }}
{{- end }}
{{- if hasKey .Values.bifrost.largePayloadOptimization "requestThresholdBytes" }}
{{- $_ := set $lpo "request_threshold_bytes" .Values.bifrost.largePayloadOptimization.requestThresholdBytes }}
{{- end }}
{{- if hasKey .Values.bifrost.largePayloadOptimization "responseThresholdBytes" }}
{{- $_ := set $lpo "response_threshold_bytes" .Values.bifrost.largePayloadOptimization.responseThresholdBytes }}
{{- end }}
{{- if hasKey .Values.bifrost.largePayloadOptimization "prefetchSizeBytes" }}
{{- $_ := set $lpo "prefetch_size_bytes" .Values.bifrost.largePayloadOptimization.prefetchSizeBytes }}
{{- end }}
{{- if hasKey .Values.bifrost.largePayloadOptimization "maxPayloadBytes" }}
{{- $_ := set $lpo "max_payload_bytes" .Values.bifrost.largePayloadOptimization.maxPayloadBytes }}
{{- end }}
{{- if hasKey .Values.bifrost.largePayloadOptimization "truncatedLogBytes" }}
{{- $_ := set $lpo "truncated_log_bytes" .Values.bifrost.largePayloadOptimization.truncatedLogBytes }}
{{- end }}
{{- if $lpo }}
{{- $_ := set $config "large_payload_optimization" $lpo }}
{{- end }}
{{- end }}
{{- /* WebSocket Config */ -}}
{{- if .Values.bifrost.websocket }}
{{- $ws := dict }}
{{- if .Values.bifrost.websocket.maxConnectionsPerUser }}
{{- $_ := set $ws "max_connections_per_user" .Values.bifrost.websocket.maxConnectionsPerUser }}
{{- end }}
{{- if .Values.bifrost.websocket.transcriptBufferSize }}
{{- $_ := set $ws "transcript_buffer_size" .Values.bifrost.websocket.transcriptBufferSize }}
{{- end }}
{{- if .Values.bifrost.websocket.pool }}
{{- $pool := dict }}
{{- if .Values.bifrost.websocket.pool.maxIdlePerKey }}
{{- $_ := set $pool "max_idle_per_key" .Values.bifrost.websocket.pool.maxIdlePerKey }}
{{- end }}
{{- if .Values.bifrost.websocket.pool.maxTotalConnections }}
{{- $_ := set $pool "max_total_connections" .Values.bifrost.websocket.pool.maxTotalConnections }}
{{- end }}
{{- if .Values.bifrost.websocket.pool.idleTimeoutSeconds }}
{{- $_ := set $pool "idle_timeout_seconds" .Values.bifrost.websocket.pool.idleTimeoutSeconds }}
{{- end }}
{{- if .Values.bifrost.websocket.pool.maxConnectionLifetimeSeconds }}
{{- $_ := set $pool "max_connection_lifetime_seconds" .Values.bifrost.websocket.pool.maxConnectionLifetimeSeconds }}
{{- end }}
{{- if $pool }}
{{- $_ := set $ws "pool" $pool }}
{{- end }}
{{- end }}
{{- if $ws }}
{{- $_ := set $config "websocket" $ws }}
{{- end }}
{{- end }}
{{- $config | toJson }}
{{- end }}

{{/*
Validation template - validates required fields from config.schema.json
Call this template at the beginning of deployment/stateful templates
*/}}
{{- define "bifrost.validate" -}}

{{/* Validate semantic cache plugin when enabled */}}
{{- if and .Values.bifrost.plugins.telemetry.enabled (hasKey .Values.bifrost.plugins.telemetry "version") (lt (int .Values.bifrost.plugins.telemetry.version) 1) }}
{{- fail "ERROR: bifrost.plugins.telemetry.version must be >= 1. Bump to >1 to force DB-backed plugin config updates." }}
{{- end }}
{{- if and .Values.bifrost.plugins.telemetry.enabled (hasKey .Values.bifrost.plugins.telemetry "version") (gt (int .Values.bifrost.plugins.telemetry.version) 32767) }}
{{- fail "ERROR: bifrost.plugins.telemetry.version must be <= 32767." }}
{{- end }}
{{- if and .Values.bifrost.plugins.logging.enabled (hasKey .Values.bifrost.plugins.logging "version") (lt (int .Values.bifrost.plugins.logging.version) 1) }}
{{- fail "ERROR: bifrost.plugins.logging.version must be >= 1. Bump to >1 to force DB-backed plugin config updates." }}
{{- end }}
{{- if and .Values.bifrost.plugins.logging.enabled (hasKey .Values.bifrost.plugins.logging "version") (gt (int .Values.bifrost.plugins.logging.version) 32767) }}
{{- fail "ERROR: bifrost.plugins.logging.version must be <= 32767." }}
{{- end }}
{{- if and .Values.bifrost.plugins.governance.enabled (hasKey .Values.bifrost.plugins.governance "version") (lt (int .Values.bifrost.plugins.governance.version) 1) }}
{{- fail "ERROR: bifrost.plugins.governance.version must be >= 1. Bump to >1 to force DB-backed plugin config updates." }}
{{- end }}
{{- if and .Values.bifrost.plugins.governance.enabled (hasKey .Values.bifrost.plugins.governance "version") (gt (int .Values.bifrost.plugins.governance.version) 32767) }}
{{- fail "ERROR: bifrost.plugins.governance.version must be <= 32767." }}
{{- end }}
{{- if and .Values.bifrost.plugins.maxim.enabled (hasKey .Values.bifrost.plugins.maxim "version") (lt (int .Values.bifrost.plugins.maxim.version) 1) }}
{{- fail "ERROR: bifrost.plugins.maxim.version must be >= 1. Bump to >1 to force DB-backed plugin config updates." }}
{{- end }}
{{- if and .Values.bifrost.plugins.maxim.enabled (hasKey .Values.bifrost.plugins.maxim "version") (gt (int .Values.bifrost.plugins.maxim.version) 32767) }}
{{- fail "ERROR: bifrost.plugins.maxim.version must be <= 32767." }}
{{- end }}
{{- if and .Values.bifrost.plugins.semanticCache.enabled (hasKey .Values.bifrost.plugins.semanticCache "version") (lt (int .Values.bifrost.plugins.semanticCache.version) 1) }}
{{- fail "ERROR: bifrost.plugins.semanticCache.version must be >= 1. Bump to >1 to force DB-backed plugin config updates." }}
{{- end }}
{{- if and .Values.bifrost.plugins.semanticCache.enabled (hasKey .Values.bifrost.plugins.semanticCache "version") (gt (int .Values.bifrost.plugins.semanticCache.version) 32767) }}
{{- fail "ERROR: bifrost.plugins.semanticCache.version must be <= 32767." }}
{{- end }}
{{- if and .Values.bifrost.plugins.otel.enabled (hasKey .Values.bifrost.plugins.otel "version") (lt (int .Values.bifrost.plugins.otel.version) 1) }}
{{- fail "ERROR: bifrost.plugins.otel.version must be >= 1. Bump to >1 to force DB-backed plugin config updates." }}
{{- end }}
{{- if and .Values.bifrost.plugins.otel.enabled (hasKey .Values.bifrost.plugins.otel "version") (gt (int .Values.bifrost.plugins.otel.version) 32767) }}
{{- fail "ERROR: bifrost.plugins.otel.version must be <= 32767." }}
{{- end }}
{{- if and .Values.bifrost.plugins.datadog.enabled (hasKey .Values.bifrost.plugins.datadog "version") (lt (int .Values.bifrost.plugins.datadog.version) 1) }}
{{- fail "ERROR: bifrost.plugins.datadog.version must be >= 1. Bump to >1 to force DB-backed plugin config updates." }}
{{- end }}
{{- if and .Values.bifrost.plugins.datadog.enabled (hasKey .Values.bifrost.plugins.datadog "version") (gt (int .Values.bifrost.plugins.datadog.version) 32767) }}
{{- fail "ERROR: bifrost.plugins.datadog.version must be <= 32767." }}
{{- end }}

{{/* Validate semantic cache plugin when enabled */}}
{{- if .Values.bifrost.plugins.semanticCache.enabled }}
{{/* When dimension is 1, direct (hash-based) caching is used — provider and keys are not required. */}}
{{- if ne (int .Values.bifrost.plugins.semanticCache.config.dimension) 1 }}
{{- if not .Values.bifrost.plugins.semanticCache.config.provider }}
{{- fail "ERROR: bifrost.plugins.semanticCache.config.provider is required for semantic caching. Supported providers: openai, anthropic, gemini, bedrock, azure, cohere, mistral, groq, ollama, openrouter, vertex, cerebras, parasail, perplexity, sgl, huggingface. For direct (hash-based) caching, set dimension: 1." }}
{{- end }}
{{- if not .Values.bifrost.plugins.semanticCache.config.keys }}
{{- fail "ERROR: bifrost.plugins.semanticCache.config.keys is required for semantic caching. Provide at least one API key for the embedding provider. For direct (hash-based) caching, set dimension: 1." }}
{{- end }}
{{- end }}
{{- end }}

{{/* Validate OTEL plugin when enabled */}}
{{- if .Values.bifrost.plugins.otel.enabled }}
{{- if not .Values.bifrost.plugins.otel.config.collector_url }}
{{- fail "ERROR: bifrost.plugins.otel.config.collector_url is required when OTEL plugin is enabled. Provide the URL of your OpenTelemetry collector." }}
{{- end }}
{{- if not .Values.bifrost.plugins.otel.config.trace_type }}
{{- fail "ERROR: bifrost.plugins.otel.config.trace_type is required when OTEL plugin is enabled. Supported values: genai_extension, vercel, open_inference" }}
{{- end }}
{{- if not .Values.bifrost.plugins.otel.config.protocol }}
{{- fail "ERROR: bifrost.plugins.otel.config.protocol is required when OTEL plugin is enabled. Supported values: http, grpc" }}
{{- end }}
{{- end }}

{{/* Validate Maxim plugin when enabled */}}
{{- if .Values.bifrost.plugins.maxim.enabled }}
{{- if and (not .Values.bifrost.plugins.maxim.config.api_key) (not .Values.bifrost.plugins.maxim.secretRef.name) }}
{{- fail "ERROR: bifrost.plugins.maxim.config.api_key or bifrost.plugins.maxim.secretRef.name is required when Maxim plugin is enabled." }}
{{- end }}
{{- end }}

{{/* Validate SCIM/SSO config when enabled */}}
{{- $scimValidation := .Values.bifrost.scim }}
{{- if and $scimValidation $scimValidation.enabled }}
{{- if eq $scimValidation.provider "okta" }}
{{- if not $scimValidation.config.issuerUrl }}
{{- fail "ERROR: bifrost.scim.config.issuerUrl is required when SCIM provider is Okta. Example: https://your-domain.okta.com/oauth2/default" }}
{{- end }}
{{- if not $scimValidation.config.clientId }}
{{- fail "ERROR: bifrost.scim.config.clientId is required when SCIM provider is Okta." }}
{{- end }}
{{- if not $scimValidation.config.clientSecret }}
{{- fail "ERROR: bifrost.scim.config.clientSecret is required when SCIM provider is Okta." }}
{{- end }}
{{- if not $scimValidation.config.apiToken }}
{{- fail "ERROR: bifrost.scim.config.apiToken is required when SCIM provider is Okta." }}
{{- end }}
{{- end }}
{{- if eq $scimValidation.provider "entra" }}
{{- if not $scimValidation.config.tenantId }}
{{- fail "ERROR: bifrost.scim.config.tenantId is required when SCIM provider is Entra (Azure AD)." }}
{{- end }}
{{- if not $scimValidation.config.clientId }}
{{- fail "ERROR: bifrost.scim.config.clientId is required when SCIM provider is Entra (Azure AD)." }}
{{- end }}
{{- end }}
{{- end }}

{{/* Validate cluster config when enabled */}}
{{- if and .Values.bifrost.cluster .Values.bifrost.cluster.enabled }}
{{- if not .Values.bifrost.cluster.gossip }}
{{- fail "ERROR: bifrost.cluster.gossip is required when cluster mode is enabled." }}
{{- end }}
{{- if not .Values.bifrost.cluster.gossip.port }}
{{- fail "ERROR: bifrost.cluster.gossip.port is required when cluster mode is enabled." }}
{{- end }}
{{- if not .Values.bifrost.cluster.gossip.config }}
{{- fail "ERROR: bifrost.cluster.gossip.config is required when cluster mode is enabled." }}
{{- end }}
{{- if not .Values.bifrost.cluster.gossip.config.timeoutSeconds }}
{{- fail "ERROR: bifrost.cluster.gossip.config.timeoutSeconds is required when cluster mode is enabled." }}
{{- end }}
{{- if not .Values.bifrost.cluster.gossip.config.successThreshold }}
{{- fail "ERROR: bifrost.cluster.gossip.config.successThreshold is required when cluster mode is enabled." }}
{{- end }}
{{- if not .Values.bifrost.cluster.gossip.config.failureThreshold }}
{{- fail "ERROR: bifrost.cluster.gossip.config.failureThreshold is required when cluster mode is enabled." }}
{{- end }}
{{- if and .Values.bifrost.cluster.discovery .Values.bifrost.cluster.discovery.enabled }}
{{- if not .Values.bifrost.cluster.discovery.type }}
{{- fail "ERROR: bifrost.cluster.discovery.type is required when cluster discovery is enabled. Supported types: kubernetes, dns, udp, consul, etcd, mdns" }}
{{- end }}
{{- if eq .Values.bifrost.cluster.discovery.type "udp" }}
{{- if not .Values.bifrost.cluster.discovery.udpBroadcastPort }}
{{- fail "ERROR: bifrost.cluster.discovery.udpBroadcastPort is required when using udp discovery." }}
{{- end }}
{{- if not .Values.bifrost.cluster.discovery.allowedAddressSpace }}
{{- fail "ERROR: bifrost.cluster.discovery.allowedAddressSpace is required when using udp discovery." }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}

{{/* Validate RBAC pod discovery + service account configuration */}}
{{- if and .Values.rbac .Values.rbac.podDiscovery .Values.rbac.podDiscovery.enabled }}
{{- if and .Values.bifrost.cluster.enabled .Values.bifrost.cluster.discovery.enabled (eq .Values.bifrost.cluster.discovery.type "kubernetes") }}
{{- if and (not .Values.serviceAccount.create) (not .Values.serviceAccount.name) }}
{{- fail "ERROR: rbac.podDiscovery.enabled requires either serviceAccount.create=true or an explicit serviceAccount.name when serviceAccount.create=false." }}
{{- end }}
{{- end }}
{{- end }}

{{/* Validate external Weaviate when vector store type is weaviate */}}
{{- if and .Values.vectorStore.enabled (eq .Values.vectorStore.type "weaviate") }}
{{- if .Values.vectorStore.weaviate.external.enabled }}
{{- if not .Values.vectorStore.weaviate.external.scheme }}
{{- fail "ERROR: vectorStore.weaviate.external.scheme is required when using external Weaviate. Values: http or https" }}
{{- end }}
{{- if not .Values.vectorStore.weaviate.external.host }}
{{- fail "ERROR: vectorStore.weaviate.external.host is required when using external Weaviate." }}
{{- end }}
{{- end }}
{{- end }}

{{/* Validate external Redis when vector store type is redis */}}
{{- if and .Values.vectorStore.enabled (eq .Values.vectorStore.type "redis") }}
{{- if .Values.vectorStore.redis.external.enabled }}
{{- if not .Values.vectorStore.redis.external.host }}
{{- fail "ERROR: vectorStore.redis.external.host is required when using external Redis." }}
{{- end }}
{{- end }}
{{- end }}

{{/* Validate external Qdrant when vector store type is qdrant */}}
{{- if and .Values.vectorStore.enabled (eq .Values.vectorStore.type "qdrant") }}
{{- if .Values.vectorStore.qdrant.external.enabled }}
{{- if not .Values.vectorStore.qdrant.external.host }}
{{- fail "ERROR: vectorStore.qdrant.external.host is required when using external Qdrant." }}
{{- end }}
{{- end }}
{{- end }}

{{/* Validate external PostgreSQL when enabled */}}
{{- if .Values.postgresql.external.enabled }}
{{- if not .Values.postgresql.external.host }}
{{- fail "ERROR: postgresql.external.host is required when using external PostgreSQL." }}
{{- end }}
{{- if not .Values.postgresql.external.database }}
{{- fail "ERROR: postgresql.external.database is required when using external PostgreSQL." }}
{{- end }}
{{- if not .Values.postgresql.external.user }}
{{- fail "ERROR: postgresql.external.user is required when using external PostgreSQL." }}
{{- end }}
{{- if not .Values.postgresql.external.sslMode }}
{{- fail "ERROR: postgresql.external.sslMode is required when using external PostgreSQL. Values: disable, allow, prefer, require, verify-ca, verify-full" }}
{{- end }}
{{- end }}

{{/* Validate governance budgets */}}
{{- if .Values.bifrost.governance.budgets }}
{{- range $idx, $budget := .Values.bifrost.governance.budgets }}
{{- if not $budget.id }}
{{- fail (printf "ERROR: bifrost.governance.budgets[%d].id is required." $idx) }}
{{- end }}
{{- if not $budget.max_limit }}
{{- fail (printf "ERROR: bifrost.governance.budgets[%d].max_limit is required for budget '%s'." $idx $budget.id) }}
{{- end }}
{{- if not $budget.reset_duration }}
{{- fail (printf "ERROR: bifrost.governance.budgets[%d].reset_duration is required for budget '%s'. Example values: 30s, 5m, 1h, 1d, 1w, 1M, 1Y" $idx $budget.id) }}
{{- end }}
{{- end }}
{{- end }}

{{/* Validate governance rate limits */}}
{{- if .Values.bifrost.governance.rateLimits }}
{{- range $idx, $rl := .Values.bifrost.governance.rateLimits }}
{{- if not $rl.id }}
{{- fail (printf "ERROR: bifrost.governance.rateLimits[%d].id is required." $idx) }}
{{- end }}
{{- end }}
{{- end }}

{{/* Validate governance customers */}}
{{- if .Values.bifrost.governance.customers }}
{{- range $idx, $customer := .Values.bifrost.governance.customers }}
{{- if not $customer.id }}
{{- fail (printf "ERROR: bifrost.governance.customers[%d].id is required." $idx) }}
{{- end }}
{{- if not $customer.name }}
{{- fail (printf "ERROR: bifrost.governance.customers[%d].name is required for customer '%s'." $idx $customer.id) }}
{{- end }}
{{- end }}
{{- end }}

{{/* Validate governance teams */}}
{{- if .Values.bifrost.governance.teams }}
{{- range $idx, $team := .Values.bifrost.governance.teams }}
{{- if not $team.id }}
{{- fail (printf "ERROR: bifrost.governance.teams[%d].id is required." $idx) }}
{{- end }}
{{- if not $team.name }}
{{- fail (printf "ERROR: bifrost.governance.teams[%d].name is required for team '%s'." $idx $team.id) }}
{{- end }}
{{- end }}
{{- end }}

{{/* Validate governance business units */}}
{{- if .Values.bifrost.governance.businessUnits }}
{{- range $idx, $bu := .Values.bifrost.governance.businessUnits }}
{{- if not $bu.id }}
{{- fail (printf "ERROR: bifrost.governance.businessUnits[%d].id is required." $idx) }}
{{- end }}
{{- if not $bu.name }}
{{- fail (printf "ERROR: bifrost.governance.businessUnits[%d].name is required for business unit '%s'." $idx $bu.id) }}
{{- end }}
{{- end }}
{{- end }}

{{/* Validate governance virtual keys */}}
{{- if .Values.bifrost.governance.virtualKeys }}
{{- range $idx, $vk := .Values.bifrost.governance.virtualKeys }}
{{- if not $vk.id }}
{{- fail (printf "ERROR: bifrost.governance.virtualKeys[%d].id is required." $idx) }}
{{- end }}
{{- if not $vk.name }}
{{- fail (printf "ERROR: bifrost.governance.virtualKeys[%d].name is required for virtual key '%s'." $idx $vk.id) }}
{{- end }}
{{- end }}
{{- end }}

{{/* Validate guardrails rules */}}
{{- if .Values.bifrost.guardrails.rules }}
{{- range $idx, $rule := .Values.bifrost.guardrails.rules }}
{{- if not $rule.id }}
{{- fail (printf "ERROR: bifrost.guardrails.rules[%d].id is required." $idx) }}
{{- end }}
{{- if not $rule.name }}
{{- fail (printf "ERROR: bifrost.guardrails.rules[%d].name is required for rule id '%v'." $idx $rule.id) }}
{{- end }}
{{- if not (hasKey $rule "enabled") }}
{{- fail (printf "ERROR: bifrost.guardrails.rules[%d].enabled is required for rule '%s'." $idx $rule.name) }}
{{- end }}
{{- if not $rule.cel_expression }}
{{- fail (printf "ERROR: bifrost.guardrails.rules[%d].cel_expression is required for rule '%s'." $idx $rule.name) }}
{{- end }}
{{- if not $rule.apply_to }}
{{- fail (printf "ERROR: bifrost.guardrails.rules[%d].apply_to is required for rule '%s'. Values: input, output, both" $idx $rule.name) }}
{{- end }}
{{- end }}
{{- end }}

{{/* Validate guardrails providers */}}
{{- if .Values.bifrost.guardrails.providers }}
{{- range $idx, $provider := .Values.bifrost.guardrails.providers }}
{{- if not $provider.id }}
{{- fail (printf "ERROR: bifrost.guardrails.providers[%d].id is required." $idx) }}
{{- end }}
{{- if not $provider.provider_name }}
{{- fail (printf "ERROR: bifrost.guardrails.providers[%d].provider_name is required for provider id '%v'." $idx $provider.id) }}
{{- end }}
{{- if not $provider.policy_name }}
{{- fail (printf "ERROR: bifrost.guardrails.providers[%d].policy_name is required for provider '%s'." $idx $provider.provider_name) }}
{{- end }}
{{- if not (hasKey $provider "enabled") }}
{{- fail (printf "ERROR: bifrost.guardrails.providers[%d].enabled is required for provider '%s'." $idx $provider.provider_name) }}
{{- end }}
{{- end }}
{{- end }}

{{/* Validate MCP client configs when MCP is enabled */}}
{{- if .Values.bifrost.mcp.enabled }}
{{- if .Values.bifrost.mcp.clientConfigs }}
{{- range $idx, $client := .Values.bifrost.mcp.clientConfigs }}
{{- if not $client.name }}
{{- fail (printf "ERROR: bifrost.mcp.clientConfigs[%d].name is required." $idx) }}
{{- end }}
{{- if not $client.connectionType }}
{{- fail (printf "ERROR: bifrost.mcp.clientConfigs[%d].connectionType is required for client '%s'. Values: stdio, websocket, http" $idx $client.name) }}
{{- end }}
{{- if eq $client.connectionType "stdio" }}
{{- if not $client.stdioConfig }}
{{- fail (printf "ERROR: bifrost.mcp.clientConfigs[%d].stdioConfig is required when connectionType is 'stdio' for client '%s'." $idx $client.name) }}
{{- end }}
{{- if not $client.stdioConfig.command }}
{{- fail (printf "ERROR: bifrost.mcp.clientConfigs[%d].stdioConfig.command is required for client '%s'." $idx $client.name) }}
{{- end }}
{{- end }}
{{- if eq $client.connectionType "websocket" }}
{{- if not $client.websocketConfig }}
{{- fail (printf "ERROR: bifrost.mcp.clientConfigs[%d].websocketConfig is required when connectionType is 'websocket' for client '%s'." $idx $client.name) }}
{{- end }}
{{- if not $client.websocketConfig.url }}
{{- fail (printf "ERROR: bifrost.mcp.clientConfigs[%d].websocketConfig.url is required for client '%s'." $idx $client.name) }}
{{- end }}
{{- end }}
{{- if eq $client.connectionType "http" }}
{{- if not $client.httpConfig }}
{{- fail (printf "ERROR: bifrost.mcp.clientConfigs[%d].httpConfig is required when connectionType is 'http' for client '%s'." $idx $client.name) }}
{{- end }}
{{- if not $client.httpConfig.url }}
{{- fail (printf "ERROR: bifrost.mcp.clientConfigs[%d].httpConfig.url is required for client '%s'." $idx $client.name) }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}
{{- if .Values.bifrost.mcp.toolGroups }}
{{- range $idx, $group := .Values.bifrost.mcp.toolGroups }}
{{- if not $group.name }}
{{- fail (printf "ERROR: bifrost.mcp.toolGroups[%d].name is required." $idx) }}
{{- end }}
{{- if not $group.tools }}
{{- fail (printf "ERROR: bifrost.mcp.toolGroups[%d].tools is required for group '%s'." $idx $group.name) }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}

{{/* Validate custom plugins */}}
{{- if .Values.bifrost.plugins.custom }}
{{- range $idx, $plugin := .Values.bifrost.plugins.custom }}
{{- if not $plugin.name }}
{{- fail (printf "ERROR: bifrost.plugins.custom[%d].name is required." $idx) }}
{{- end }}
{{- if not (hasKey $plugin "enabled") }}
{{- fail (printf "ERROR: bifrost.plugins.custom[%d].enabled is required for plugin '%s'." $idx $plugin.name) }}
{{- end }}
{{- if and (hasKey $plugin "version") (lt (int $plugin.version) 1) }}
{{- fail (printf "ERROR: bifrost.plugins.custom[%d].version must be >= 1 for plugin '%s'." $idx $plugin.name) }}
{{- end }}
{{- if and (hasKey $plugin "version") (gt (int $plugin.version) 32767) }}
{{- fail (printf "ERROR: bifrost.plugins.custom[%d].version must be <= 32767 for plugin '%s'." $idx $plugin.name) }}
{{- end }}
{{- end }}
{{- end }}

{{- end -}}