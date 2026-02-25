// Configuration types that match the Go backend structures

import { KnownProvidersNames } from "@/lib/constants/logs";
import { EnvVar } from "./schemas";

// Known provider names - all supported standard providers
export type KnownProvider = (typeof KnownProvidersNames)[number];

// Base provider names - all supported base providers
export type BaseProvider = "openai" | "anthropic" | "cohere" | "gemini" | "bedrock" | "replicate";

// Branded type for custom provider names to prevent collision with known providers
export type CustomProviderName = string & { readonly __brand: "CustomProviderName" };

// ModelProvider union - either known providers or branded custom providers
export type ModelProviderName = KnownProvider | CustomProviderName;

// Helper function to check if a provider name is a known provider
export const isKnownProvider = (provider: string): provider is KnownProvider => {
	return KnownProvidersNames.includes(provider.toLowerCase() as KnownProvider);
};

// AzureKeyConfig matching Go's schemas.AzureKeyConfig
export interface AzureKeyConfig {
	endpoint: EnvVar;
	deployments?: Record<string, string> | string; // Allow string during editing
	api_version?: EnvVar;
	client_id?: EnvVar;
	client_secret?: EnvVar;
	tenant_id?: EnvVar;
	scopes?: string[];
}

export const DefaultAzureKeyConfig: AzureKeyConfig = {
	endpoint: { value: "", env_var: "", from_env: false },
	deployments: {},
	api_version: { value: "2024-02-01", env_var: "", from_env: false },
	client_id: { value: "", env_var: "", from_env: false },
	client_secret: { value: "", env_var: "", from_env: false },
	tenant_id: { value: "", env_var: "", from_env: false },
	scopes: [],
} as const satisfies Required<AzureKeyConfig>;

// VertexKeyConfig matching Go's schemas.VertexKeyConfig
export interface VertexKeyConfig {
	project_id: EnvVar;
	project_number?: EnvVar;
	region: EnvVar;
	auth_credentials?: EnvVar;
	deployments?: Record<string, string> | string; // Allow string during editing
}

export const DefaultVertexKeyConfig: VertexKeyConfig = {
	project_id: { value: "", env_var: "", from_env: false },
	project_number: { value: "", env_var: "", from_env: false },
	region: { value: "", env_var: "", from_env: false },
	auth_credentials: { value: "", env_var: "", from_env: false },
	deployments: {},
} as const satisfies Required<VertexKeyConfig>;

export interface S3BucketConfig {
	bucket_name: string;
	prefix?: string;
	is_default?: boolean;
}

export interface BatchS3Config {
	buckets?: S3BucketConfig[];
}

// BedrockKeyConfig matching Go's schemas.BedrockKeyConfig
export interface BedrockKeyConfig {
	access_key?: EnvVar;
	secret_key?: EnvVar;
	session_token?: EnvVar;
	region?: EnvVar;
	arn?: EnvVar;
	deployments?: Record<string, string> | string; // Allow string during editing
	batch_s3_config?: BatchS3Config;
}

// Default BedrockKeyConfig
export const DefaultBedrockKeyConfig: BedrockKeyConfig = {
	access_key: { value: "", env_var: "", from_env: false },
	secret_key: { value: "", env_var: "", from_env: false },
	session_token: undefined as unknown as EnvVar,
	region: { value: "us-east-1", env_var: "", from_env: false },
	arn: { value: "", env_var: "", from_env: false },
	deployments: {},
	batch_s3_config: undefined as unknown as BatchS3Config,
} as const satisfies Required<BedrockKeyConfig>;

// ReplicateKeyConfig matching Go's schemas.ReplicateKeyConfig
export interface ReplicateKeyConfig {
	deployments?: Record<string, string> | string; // Allow string during editing
}

// Default ReplicateKeyConfig
export const DefaultReplicateKeyConfig: ReplicateKeyConfig = {
	deployments: {},
} as const satisfies Required<ReplicateKeyConfig>;

// VLLMKeyConfig matching Go's schemas.VLLMKeyConfig
export interface VLLMKeyConfig {
	url: EnvVar;
	model_name: string;
}

// Default VLLMKeyConfig
export const DefaultVLLMKeyConfig: VLLMKeyConfig = {
	url: { value: "", env_var: "", from_env: false },
	model_name: "",
} as const satisfies Required<VLLMKeyConfig>;

// Key structure matching Go's schemas.Key
export interface ModelProviderKey {
	id: string;
	name: string;
	value?: EnvVar;
	models?: string[];
	weight: number;
	enabled?: boolean;
	use_for_batch_api?: boolean;
	azure_key_config?: AzureKeyConfig;
	vertex_key_config?: VertexKeyConfig;
	bedrock_key_config?: BedrockKeyConfig;
	replicate_key_config?: ReplicateKeyConfig;
	vllm_key_config?: VLLMKeyConfig;
	config_hash?: string; // Present when config is synced from config.json
	status?: "unknown" | "success" | "list_models_failed";
	description?: string;
}

// Default ModelProviderKey
export const DefaultModelProviderKey: ModelProviderKey = {
	id: "",
	name: "",
	value: {
		value: "",
		env_var: "",
		from_env: false,
	},
	models: [],
	weight: 1.0,
	enabled: true,
};

// NetworkConfig matching Go's schemas.NetworkConfig
export interface NetworkConfig {
	base_url?: string;
	is_key_less?: boolean;
	extra_headers?: Record<string, string>;
	default_request_timeout_in_seconds: number;
	max_retries: number;
	retry_backoff_initial: number; // Duration in milliseconds
	retry_backoff_max: number; // Duration in milliseconds
}

// ConcurrencyAndBufferSize matching Go's schemas.ConcurrencyAndBufferSize
export interface ConcurrencyAndBufferSize {
	concurrency: number;
	buffer_size: number;
}

// Proxy types matching Go's schemas.ProxyType
export type ProxyType = "none" | "http" | "socks5" | "environment";

// ProxyConfig matching Go's schemas.ProxyConfig
export interface ProxyConfig {
	type: ProxyType;
	url?: string;
	username?: string;
	password?: string;
	ca_cert_pem?: string;
}

// Request types matching Go's schemas.RequestType
export type RequestType =
	| "list_models"
	| "text_completion"
	| "text_completion_stream"
	| "chat_completion"
	| "chat_completion_stream"
	| "responses"
	| "responses_stream"
	| "embedding"
	| "speech"
	| "speech_stream"
	| "transcription"
	| "transcription_stream"
	| "image_generation"
	| "image_generation_stream"
	| "image_edit"
	| "image_edit_stream"
	| "image_variation"
	| "count_tokens"
	| "batch_create"
	| "batch_list"
	| "batch_retrieve"
	| "batch_cancel"
	| "batch_results"
	| "file_upload"
	| "file_list"
	| "file_retrieve"
	| "file_delete"
	| "file_content"
	| "mcp_tool_execution"
	| "container_create"
	| "container_list"
	| "container_retrieve"
	| "container_delete"
	| "container_file_create"
	| "container_file_list"
	| "container_file_retrieve"
	| "container_file_content"
	| "container_file_delete";

// AllowedRequests matching Go's schemas.AllowedRequests
export interface AllowedRequests {
	text_completion: boolean;
	text_completion_stream: boolean;
	chat_completion: boolean;
	chat_completion_stream: boolean;
	responses: boolean;
	responses_stream: boolean;
	embedding: boolean;
	speech: boolean;
	speech_stream: boolean;
	transcription: boolean;
	transcription_stream: boolean;
	image_generation: boolean;
	image_generation_stream: boolean;
	image_edit: boolean;
	image_edit_stream: boolean;
	image_variation: boolean;
	count_tokens: boolean;
	list_models: boolean;
}

// CustomProviderConfig matching Go's schemas.CustomProviderConfig
export interface CustomProviderConfig {
	base_provider_type: KnownProvider;
	is_key_less?: boolean;
	allowed_requests?: AllowedRequests;
	request_path_overrides?: Record<string, string>;
}

export type PricingOverrideMatchType = "exact" | "wildcard" | "regex";

export interface ProviderPricingOverride {
	model_pattern: string;
	match_type: PricingOverrideMatchType;
	request_types?: RequestType[];
	input_cost_per_token?: number;
	output_cost_per_token?: number;
	input_cost_per_video_per_second?: number;
	input_cost_per_audio_per_second?: number;
	input_cost_per_character?: number;
	output_cost_per_character?: number;
	input_cost_per_token_above_128k_tokens?: number;
	input_cost_per_character_above_128k_tokens?: number;
	input_cost_per_image_above_128k_tokens?: number;
	input_cost_per_video_per_second_above_128k_tokens?: number;
	input_cost_per_audio_per_second_above_128k_tokens?: number;
	output_cost_per_token_above_128k_tokens?: number;
	output_cost_per_character_above_128k_tokens?: number;
	input_cost_per_token_above_200k_tokens?: number;
	output_cost_per_token_above_200k_tokens?: number;
	cache_creation_input_token_cost_above_200k_tokens?: number;
	cache_read_input_token_cost_above_200k_tokens?: number;
	cache_read_input_token_cost?: number;
	cache_creation_input_token_cost?: number;
	input_cost_per_token_batches?: number;
	output_cost_per_token_batches?: number;
	input_cost_per_image_token?: number;
	output_cost_per_image_token?: number;
	input_cost_per_image?: number;
	output_cost_per_image?: number;
	cache_read_input_image_token_cost?: number;
}

// ProviderConfig matching Go's lib.ProviderConfig
export interface ModelProviderConfig {
	keys: ModelProviderKey[];
	network_config?: NetworkConfig;
	concurrency_and_buffer_size?: ConcurrencyAndBufferSize;
	proxy_config?: ProxyConfig;
	send_back_raw_request?: boolean;
	send_back_raw_response?: boolean;
	custom_provider_config?: CustomProviderConfig;
	pricing_overrides?: ProviderPricingOverride[];
	status?: "unknown" | "success" | "list_models_failed";
	description?: string;
}

// ProviderResponse matching Go's ProviderResponse
export interface ModelProvider extends ModelProviderConfig {
	name: ModelProviderName;
	provider_status: ProviderStatus;
	config_hash?: string; // Present when config is synced from config.json
}

// ListProvidersResponse matching Go's ListProvidersResponse
export interface ListProvidersResponse {
	providers?: ModelProvider[];
	total: number;
}

// AddProviderRequest matching Go's AddProviderRequest
export interface AddProviderRequest {
	provider: ModelProviderName;
	keys: ModelProviderKey[];
	network_config?: NetworkConfig;
	concurrency_and_buffer_size?: ConcurrencyAndBufferSize;
	proxy_config?: ProxyConfig;
	send_back_raw_request?: boolean;
	send_back_raw_response?: boolean;
	custom_provider_config?: CustomProviderConfig;
	pricing_overrides?: ProviderPricingOverride[];
}

// UpdateProviderRequest matching Go's UpdateProviderRequest
export interface UpdateProviderRequest {
	keys: ModelProviderKey[];
	network_config: NetworkConfig;
	concurrency_and_buffer_size: ConcurrencyAndBufferSize;
	proxy_config: ProxyConfig;
	send_back_raw_request?: boolean;
	send_back_raw_response?: boolean;
	custom_provider_config?: CustomProviderConfig;
	pricing_overrides?: ProviderPricingOverride[];
}

// BifrostErrorResponse matching Go's schemas.BifrostError
export interface BifrostErrorResponse {
	event_id?: string;
	type?: string;
	is_bifrost_error: boolean;
	status_code?: number;
	error: {
		message: string;
		type?: string;
		code?: string;
		param?: string;
	};
}

// LatestReleaseResponse matching Go's LatestReleaseResponse
export interface LatestReleaseResponse {
	name: string;
	changelogUrl: string;
}

export interface FrameworkConfig {
	id: number;
	pricing_url: string;
	pricing_sync_interval: number;
}

// Auth config
export interface AuthConfig {
	admin_username: EnvVar;
	admin_password: EnvVar;
	is_enabled: boolean;
	disable_auth_on_inference?: boolean;
}

// Global proxy type (for global proxy configuration, not per-provider)
export type GlobalProxyType = "http" | "socks5" | "tcp";

// Global proxy configuration matching Go's tables.GlobalProxyConfig
export interface GlobalProxyConfig {
	enabled: boolean;
	type: GlobalProxyType;
	url: string;
	username?: string;
	password?: string;
	ca_cert_pem?: string;
	no_proxy?: string;
	timeout?: number;
	skip_tls_verify?: boolean;
	enable_for_scim: boolean;
	enable_for_inference: boolean;
	enable_for_api: boolean;
}

// Default GlobalProxyConfig
export const DefaultGlobalProxyConfig: GlobalProxyConfig = {
	enabled: false,
	type: "http",
	url: "",
	username: "",
	password: "",
	no_proxy: "",
	timeout: 30,
	skip_tls_verify: false,
	enable_for_scim: false,
	enable_for_inference: false,
	enable_for_api: false,
};

// Global header filter configuration matching Go's tables.GlobalHeaderFilterConfig
// Controls which headers with the x-bf-eh-* prefix are forwarded to LLM providers
export interface GlobalHeaderFilterConfig {
	allowlist?: string[]; // If non-empty, only these headers are allowed
	denylist?: string[]; // Headers to always block
}

// Default GlobalHeaderFilterConfig
export const DefaultGlobalHeaderFilterConfig: GlobalHeaderFilterConfig = {
	allowlist: [],
	denylist: [],
};

// Restart required configuration
export interface RestartRequiredConfig {
	required: boolean;
	reason?: string;
}

// Bifrost Config
export interface BifrostConfig {
	client_config: CoreConfig;
	framework_config: FrameworkConfig;
	auth_config?: AuthConfig;
	proxy_config?: GlobalProxyConfig;
	restart_required?: RestartRequiredConfig;
	is_db_connected: boolean;
	is_cache_connected: boolean;
	is_logs_connected: boolean;
	auth_token?: string;
}

// Core Bifrost configuration types
export interface CoreConfig {
	drop_excess_requests: boolean;
	initial_pool_size: number;
	prometheus_labels: string[];
	enable_logging: boolean;
	disable_content_logging: boolean;
	disable_db_pings_in_health: boolean;
	log_retention_days: number;
	enforce_auth_on_inference: boolean;
	allow_direct_keys: boolean;
	allowed_origins: string[];
	allowed_headers: string[];
	max_request_body_size_mb: number;
	enable_litellm_fallbacks: boolean;
	mcp_agent_depth: number;
	mcp_tool_execution_timeout: number;
	mcp_code_mode_binding_level?: string;
	mcp_tool_sync_interval: number;
	async_job_result_ttl: number;
	required_headers: string[];
	logging_headers: string[];
	header_filter_config?: GlobalHeaderFilterConfig;
}

export const DefaultCoreConfig: CoreConfig = {
	drop_excess_requests: false,
	initial_pool_size: 1000,
	prometheus_labels: [],
	enable_logging: true,
	disable_content_logging: false,
	disable_db_pings_in_health: false,
	log_retention_days: 365,
	enforce_auth_on_inference: false,
	allow_direct_keys: false,
	allowed_origins: [],
	max_request_body_size_mb: 100,
	enable_litellm_fallbacks: false,
	mcp_agent_depth: 10,
	mcp_tool_execution_timeout: 30,
	mcp_code_mode_binding_level: "server",
	mcp_tool_sync_interval: 10,
	async_job_result_ttl: 3600,
	allowed_headers: [],
	required_headers: [],
	logging_headers: [],
};

// Semantic cache configuration types
export interface CacheConfig {
	provider: ModelProviderName;
	keys: ModelProviderKey[];
	embedding_model: string;
	dimension: number;
	ttl_seconds: number;
	threshold: number;
	conversation_history_threshold?: number;
	exclude_system_prompt?: boolean;
	cache_by_model: boolean;
	cache_by_provider: boolean;
	created_at?: string;
	updated_at?: string;
}

// Maxim configuration types
export interface MaximConfig {
	api_key: string;
	log_repo_id: string;
}

// Form-specific custom provider config that allows any string for base_provider_type
export interface FormCustomProviderConfig extends Omit<CustomProviderConfig, "base_provider_type"> {
	base_provider_type: string;
}

// Form-specific provider type that allows any string for name
export interface FormModelProvider extends Omit<ModelProvider, "name" | "custom_provider_config"> {
	name: string;
	custom_provider_config?: FormCustomProviderConfig;
}

// Utility types for form handling
export interface ProviderFormData {
	provider: FormModelProvider;
	keys: ModelProviderKey[];
	network_config?: {
		baseURL?: string;
		defaultRequestTimeoutInSeconds: number;
		maxRetries: number;
	};
	concurrency_and_buffer_size?: {
		concurrency: number;
		bufferSize: number;
	};
	custom_provider_config?: FormCustomProviderConfig;
}

// Status types
export type ProviderStatus = "active" | "error" | "deleted";
