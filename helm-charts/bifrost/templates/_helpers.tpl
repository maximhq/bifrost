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
{{- if hasKey .Values.bifrost.client "disableContentLogging" }}
{{- $_ := set $client "disable_content_logging" .Values.bifrost.client.disableContentLogging }}
{{- end }}
{{- if .Values.bifrost.client.logRetentionDays }}
{{- $_ := set $client "log_retention_days" .Values.bifrost.client.logRetentionDays }}
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
{{- $_ := set $config "providers" .Values.bifrost.providers }}
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
{{- if .Values.bifrost.governance.virtualKeys }}
{{- $vks := list }}
{{- range .Values.bifrost.governance.virtualKeys }}
{{- $vk := dict "id" .id "name" .name "value" .value }}
{{- if hasKey . "is_active" }}{{- $_ := set $vk "is_active" .is_active }}{{- end }}
{{- if .team_id }}{{- $_ := set $vk "team_id" .team_id }}{{- end }}
{{- if .customer_id }}{{- $_ := set $vk "customer_id" .customer_id }}{{- end }}
{{- if .budget_id }}{{- $_ := set $vk "budget_id" .budget_id }}{{- end }}
{{- if .rate_limit_id }}{{- $_ := set $vk "rate_limit_id" .rate_limit_id }}{{- end }}
{{- if .provider_configs }}{{- $_ := set $vk "provider_configs" .provider_configs }}{{- end }}
{{- if .mcp_configs }}{{- $_ := set $vk "mcp_configs" .mcp_configs }}{{- end }}
{{- $vks = append $vks $vk }}
{{- end }}
{{- $_ := set $governance "virtual_keys" $vks }}
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
{{- if or $authConfig.admin_username $authConfig.admin_password $authConfig.is_enabled }}
{{- $_ := set $governance "auth_config" $authConfig }}
{{- end }}
{{- end }}
{{- if or $governance.budgets $governance.rate_limits $governance.customers $governance.teams $governance.virtual_keys $governance.auth_config }}
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
{{- if or $authConfig.admin_username $authConfig.admin_password $authConfig.is_enabled }}
{{- $_ := set $config "auth_config" $authConfig }}
{{- end }}
{{- end }}
{{- /* Cluster Config */ -}}
{{- if and .Values.bifrost.cluster .Values.bifrost.cluster.enabled }}
{{- $cluster := dict "enabled" true }}
{{- if .Values.bifrost.cluster.peers }}
{{- $_ := set $cluster "peers" .Values.bifrost.cluster.peers }}
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
{{- if and .Values.bifrost.cluster.discovery .Values.bifrost.cluster.discovery.enabled }}
{{- $discovery := dict "enabled" true "type" .Values.bifrost.cluster.discovery.type }}
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
{{- /* SAML Config */ -}}
{{- if and .Values.bifrost.saml .Values.bifrost.saml.enabled }}
{{- $saml := dict "enabled" true }}
{{- if .Values.bifrost.saml.provider }}
{{- $_ := set $saml "provider" .Values.bifrost.saml.provider }}
{{- end }}
{{- if .Values.bifrost.saml.config }}
{{- $_ := set $saml "config" .Values.bifrost.saml.config }}
{{- end }}
{{- $_ := set $config "saml_config" $saml }}
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
{{- if .config }}{{- $_ := set $provider "config" .config }}{{- end }}
{{- $providers = append $providers $provider }}
{{- end }}
{{- $_ := set $guardrails "guardrail_providers" $providers }}
{{- end }}
{{- if or $guardrails.guardrail_rules $guardrails.guardrail_providers }}
{{- $_ := set $config "guardrails_config" $guardrails }}
{{- end }}
{{- end }}
{{- /* Config Store */ -}}
{{- if .Values.storage.configStore.enabled }}
{{- $configStoreType := .Values.storage.configStore.type | default .Values.storage.mode }}
{{- if eq $configStoreType "postgres" }}
{{- $pgConfig := dict "host" (include "bifrost.postgresql.host" .) "port" (include "bifrost.postgresql.port" .) "db_name" (include "bifrost.postgresql.database" .) "user" (include "bifrost.postgresql.username" .) "password" (include "bifrost.postgresql.password" .) "ssl_mode" (include "bifrost.postgresql.sslMode" .) }}
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
{{- $logsStore := dict "enabled" true "type" "postgres" "config" $pgConfig }}
{{- $_ := set $config "logs_store" $logsStore }}
{{- else }}
{{- $sqliteLogsStore := dict "enabled" true "type" "sqlite" "config" (dict "path" (printf "%s/logs.db" .Values.bifrost.appDir)) }}
{{- $_ := set $config "logs_store" $sqliteLogsStore }}
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
{{- end }}
{{- $_ := set $vectorStore "config" $weaviateConfig }}
{{- else if eq .Values.vectorStore.type "redis" }}
{{- $redisConfig := dict "addr" (printf "%s:%s" (include "bifrost.redis.host" .) (include "bifrost.redis.port" .)) }}
{{- $password := include "bifrost.redis.password" . }}
{{- if $password }}
{{- $_ := set $redisConfig "password" $password }}
{{- end }}
{{- if .Values.vectorStore.redis.external.enabled }}
{{- if .Values.vectorStore.redis.external.database }}
{{- $_ := set $redisConfig "db" .Values.vectorStore.redis.external.database }}
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
{{- $plugins = append $plugins (dict "enabled" true "name" "datadog" "config" $datadogConfig) }}
{{- end }}
{{- if $plugins }}
{{- $_ := set $config "plugins" $plugins }}
{{- end }}
{{- $config | toJson }}
{{- end }}
