// Configuration types that match the Go backend structures

import { KnownProvidersNames } from "@/lib/constants/logs";

// Known provider names - all supported standard providers
export type KnownProvider = (typeof KnownProvidersNames)[number];

// Base provider names - all supported base providers
export type BaseProvider = "openai" | "anthropic" | "cohere" | "gemini" | "bedrock";

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
	endpoint: string;
	deployments?: Record<string, string> | string; // Allow string during editing
	api_version?: string;
}

export const DefaultAzureKeyConfig: AzureKeyConfig = {
	endpoint: "",
	deployments: {},
	api_version: "2024-02-01",
} as const satisfies Required<AzureKeyConfig>;

// VertexKeyConfig matching Go's schemas.VertexKeyConfig
export interface VertexKeyConfig {
	project_id: string;
	project_number?: string;
	region: string;
	auth_credentials?: string; // Always string - JSON string or env var
	deployments?: Record<string, string> | string; // Allow string during editing
}

export const DefaultVertexKeyConfig: VertexKeyConfig = {
	project_id: "",
	project_number: "",
	region: "",
	auth_credentials: "",
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
	access_key?: string;
	secret_key?: string;
	session_token?: string;
	region: string;
	arn?: string;
	deployments?: Record<string, string> | string; // Allow string during editing
	batch_s3_config?: BatchS3Config;
}

// Default BedrockKeyConfig
export const DefaultBedrockKeyConfig: BedrockKeyConfig = {
	access_key: "",
	secret_key: "",
	session_token: undefined as unknown as string,
	region: "us-east-1",
	arn: undefined as unknown as string,
	deployments: {},
	batch_s3_config: undefined as unknown as BatchS3Config,
} as const satisfies Required<BedrockKeyConfig>;

// Key structure matching Go's schemas.Key
export interface ModelProviderKey {
	id: string;
	name: string;
	value?: string;
	models?: string[];
	weight: number;
	enabled?: boolean;
	use_for_batch_api?: boolean;
	azure_key_config?: AzureKeyConfig;
	vertex_key_config?: VertexKeyConfig;
	bedrock_key_config?: BedrockKeyConfig;
}

// Default ModelProviderKey
export const DefaultModelProviderKey: ModelProviderKey = {
	id: "",
	name: "",
	value: "",
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
	| "file_content";

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

// ProviderConfig matching Go's lib.ProviderConfig
export interface ModelProviderConfig {
	keys: ModelProviderKey[];
	network_config?: NetworkConfig;
	concurrency_and_buffer_size?: ConcurrencyAndBufferSize;
	proxy_config?: ProxyConfig;
	send_back_raw_request?: boolean;
	send_back_raw_response?: boolean;
	custom_provider_config?: CustomProviderConfig;
}

// ProviderResponse matching Go's ProviderResponse
export interface ModelProvider extends ModelProviderConfig {
	name: ModelProviderName;
	status: ProviderStatus;
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
	admin_username: string;
	admin_password: string;
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

// Bifrost Config
export interface BifrostConfig {
	client_config: CoreConfig;
	framework_config: FrameworkConfig;
	auth_config?: AuthConfig;
	proxy_config?: GlobalProxyConfig;
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
	log_retention_days: number;
	enable_governance: boolean;
	enforce_governance_header: boolean;
	allow_direct_keys: boolean;
	allowed_origins: string[];
	max_request_body_size_mb: number;
	enable_litellm_fallbacks: boolean;
}

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
