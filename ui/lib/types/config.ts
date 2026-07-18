// Configuration types that match the Go backend structures

import { KnownProvidersNames } from "@/lib/constants/logs";
import { SecretVar } from "./schemas";

// Known provider names - all supported standard providers
export type KnownProvider = (typeof KnownProvidersNames)[number];

// Base provider names - all supported base providers
export type BaseProvider = "openai" | "anthropic" | "cohere" | "gemini" | "bedrock" | "replicate" | "fireworks";

// Branded type for custom provider names to prevent collision with known providers
export type CustomProviderName = string & { readonly __brand: "CustomProviderName" };

// ModelProvider union - either known providers or branded custom providers
export type ModelProviderName = KnownProvider | CustomProviderName;

// Helper function to check if a provider name is a known provider
export const isKnownProvider = (provider: string): provider is KnownProvider => {
	return KnownProvidersNames.includes(provider.toLowerCase() as KnownProvider);
};

// ModelFamily matching Go's schemas.ModelFamily — 1st-tier family routing enum
export type ModelFamily =
	| "anthropic"
	| "openai"
	| "mistral"
	| "cohere"
	| "gemini"
	| "gemma"
	| "llama"
	| "imagen"
	| "veo"
	| "nova"
	| "titan";

export const ModelFamilyValues: ModelFamily[] = [
	"anthropic",
	"openai",
	"mistral",
	"cohere",
	"gemini",
	"gemma",
	"llama",
	"imagen",
	"veo",
	"nova",
	"titan",
];

// AliasConfig matching Go's schemas.AliasConfig.
// Go embeds AzureAliasCfg/VertexAliasCfg/BedrockAliasCfg/ReplicateAliasCfg as
// pointer structs which flatten on the wire — sub-config fields live at the
// top level of the JSON object.
export interface AliasConfig {
	model_id: string;
	model_name?: string;
	model_family?: ModelFamily;
	description?: string;
	region?: SecretVar;
	// Azure overrides
	api_version?: string;
	anthropic_version?: string;
	endpoint?: SecretVar;
	// Shared per-alias project override (Vertex GCP project; Bedrock / Bedrock Mantle
	// project sent via OpenAI-Project / anthropic-workspace-id). Kept top-level in Go
	// so the flat project_id key doesn't collide across embedded sub-configs.
	project_id?: SecretVar;
	// Vertex overrides
	project_number?: SecretVar;
	force_single_region?: boolean;
	// Bedrock overrides
	inference_profile_arn?: SecretVar;
	// Replicate overrides
	use_deployments_endpoint?: boolean;
}

// AzureKeyConfig matching Go's schemas.AzureKeyConfig
export interface AzureKeyConfig {
	endpoint: SecretVar;
	client_id?: SecretVar;
	client_secret?: SecretVar;
	tenant_id?: SecretVar;
	scopes?: string[];
}

export const DefaultAzureKeyConfig: AzureKeyConfig = {
	endpoint: { value: "", ref: "" },
	client_id: { value: "", ref: "" },
	client_secret: { value: "", ref: "" },
	tenant_id: { value: "", ref: "" },
	scopes: [],
} as const satisfies Required<AzureKeyConfig>;

// VertexKeyConfig matching Go's schemas.VertexKeyConfig
export interface VertexKeyConfig {
	project_id: SecretVar;
	project_number?: SecretVar;
	region: SecretVar;
	auth_credentials?: SecretVar;
	force_single_region?: boolean;
}

export const DefaultVertexKeyConfig: VertexKeyConfig = {
	project_id: { value: "", ref: "" },
	project_number: { value: "", ref: "" },
	region: { value: "", ref: "" },
	auth_credentials: { value: "", ref: "" },
	force_single_region: false,
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
	access_key?: SecretVar;
	secret_key?: SecretVar;
	session_token?: SecretVar;
	region?: SecretVar;
	arn?: SecretVar;
	project_id?: SecretVar;
	batch_s3_config?: BatchS3Config;
}

// Default BedrockKeyConfig
export const DefaultBedrockKeyConfig: BedrockKeyConfig = {
	access_key: { value: "", ref: "" },
	secret_key: { value: "", ref: "" },
	session_token: undefined as unknown as SecretVar,
	region: { value: "us-east-1", ref: "" },
	arn: { value: "", ref: "" },
	project_id: { value: "", ref: "" },
	batch_s3_config: undefined as unknown as BatchS3Config,
} as const satisfies Required<BedrockKeyConfig>;

// BedrockMantleKeyConfig matching Go's schemas.BedrockMantleKeyConfig
export interface BedrockMantleKeyConfig {
	access_key?: SecretVar;
	secret_key?: SecretVar;
	session_token?: SecretVar;
	region?: SecretVar;
	role_arn?: SecretVar;
	external_id?: SecretVar;
	session_name?: SecretVar;
	project_id?: SecretVar;
}

// Default BedrockMantleKeyConfig
export const DefaultBedrockMantleKeyConfig: BedrockMantleKeyConfig = {
	access_key: { value: "", ref: "" },
	secret_key: { value: "", ref: "" },
	session_token: undefined as unknown as SecretVar,
	region: { value: "us-east-1", ref: "" },
	role_arn: undefined as unknown as SecretVar,
	external_id: undefined as unknown as SecretVar,
	session_name: undefined as unknown as SecretVar,
	project_id: undefined as unknown as SecretVar,
} as const satisfies Required<BedrockMantleKeyConfig>;

// VLLMKeyConfig matching Go's schemas.VLLMKeyConfig
export interface VLLMKeyConfig {
	url: SecretVar;
	model_name: string;
}

// Default VLLMKeyConfig
export const DefaultVLLMKeyConfig: VLLMKeyConfig = {
	url: { value: "", ref: "" },
	model_name: "",
} as const satisfies Required<VLLMKeyConfig>;

// ReplicateKeyConfig matching Go's schemas.ReplicateKeyConfig
export interface ReplicateKeyConfig {
	use_deployments_endpoint: boolean;
}

// Default ReplicateKeyConfig
export const DefaultReplicateKeyConfig: ReplicateKeyConfig = {
	use_deployments_endpoint: false,
} as const satisfies Required<ReplicateKeyConfig>;

// OllamaKeyConfig matching Go's schemas.OllamaKeyConfig
export interface OllamaKeyConfig {
	url: SecretVar;
}

// Default OllamaKeyConfig
export const DefaultOllamaKeyConfig: OllamaKeyConfig = {
	url: { value: "", ref: "" },
} as const satisfies Required<OllamaKeyConfig>;

// SGLKeyConfig matching Go's schemas.SGLKeyConfig
export interface SGLKeyConfig {
	url: SecretVar;
}

// Default SGLKeyConfig
export const DefaultSGLKeyConfig: SGLKeyConfig = {
	url: { value: "", ref: "" },
} as const satisfies Required<SGLKeyConfig>;

// Key structure matching Go's schemas.Key
export interface ModelProviderKey {
	id: string;
	name: string;
	value?: SecretVar;
	models?: string[];
	blacklisted_models?: string[];
	weight: number;
	enabled?: boolean;
	use_for_batch_api?: boolean;
	aliases?: Record<string, AliasConfig>;
	azure_key_config?: AzureKeyConfig;
	vertex_key_config?: VertexKeyConfig;
	bedrock_key_config?: BedrockKeyConfig;
	bedrock_mantle_key_config?: BedrockMantleKeyConfig;
	vllm_key_config?: VLLMKeyConfig;
	replicate_key_config?: ReplicateKeyConfig;
	ollama_key_config?: OllamaKeyConfig;
	sgl_key_config?: SGLKeyConfig;
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
		ref: "",
	},
	models: [],
	blacklisted_models: [],
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
	insecure_skip_verify?: boolean;
	ca_cert_pem?: SecretVar;
	stream_idle_timeout_in_seconds?: number;
	keep_alive_timeout_in_seconds?: number;
	max_conns_per_host?: number;
	enforce_http2?: boolean;
	beta_header_overrides?: Record<string, boolean>;
	allow_private_network?: boolean;
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
	url?: SecretVar;
	username?: SecretVar;
	password?: SecretVar;
	ca_cert_pem?: SecretVar;
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
	| "responses_retrieve"
	| "responses_delete"
	| "responses_cancel"
	| "responses_input_items"
	| "embedding"
	| "rerank"
	| "speech"
	| "speech_stream"
	| "transcription"
	| "transcription_stream"
	| "image_generation"
	| "image_generation_stream"
	| "image_edit"
	| "image_edit_stream"
	| "image_variation"
	| "ocr"
	| "ocr_stream"
	| "video_generation"
	| "video_retrieve"
	| "video_download"
	| "video_delete"
	| "video_list"
	| "video_remix"
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
	| "container_file_delete"
	| "websocket_responses"
	| "realtime";

// AllowedRequests matching Go's schemas.AllowedRequests
export interface AllowedRequests {
	text_completion: boolean;
	text_completion_stream: boolean;
	chat_completion: boolean;
	chat_completion_stream: boolean;
	responses: boolean;
	responses_stream: boolean;
	responses_retrieve?: boolean;
	responses_delete?: boolean;
	responses_cancel?: boolean;
	responses_input_items?: boolean;
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
	ocr?: boolean;
	ocr_stream?: boolean;
	count_tokens: boolean;
	list_models: boolean;
	rerank: boolean;
	video_generation: boolean;
	video_retrieve: boolean;
	video_download: boolean;
	video_delete: boolean;
	video_list: boolean;
	video_remix: boolean;
	websocket_responses: boolean;
	realtime: boolean;
}

// CustomProviderConfig matching Go's schemas.CustomProviderConfig
export interface CustomProviderConfig {
	base_provider_type: KnownProvider;
	is_key_less?: boolean;
	allowed_requests?: AllowedRequests;
	request_path_overrides?: Record<string, string>;
}

// OpenAIConfig holds OpenAI-specific provider configuration.
export interface OpenAIConfig {
	disable_store?: boolean;
}

// ProviderConfig matching Go's lib.ProviderConfig
export interface ModelProviderConfig {
	network_config?: NetworkConfig;
	concurrency_and_buffer_size?: ConcurrencyAndBufferSize;
	proxy_config?: ProxyConfig;
	send_back_raw_request?: boolean;
	send_back_raw_response?: boolean;
	store_raw_request_response?: boolean;
	custom_provider_config?: CustomProviderConfig;
	openai_config?: OpenAIConfig;
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
	network_config?: NetworkConfig;
	concurrency_and_buffer_size?: ConcurrencyAndBufferSize;
	proxy_config?: ProxyConfig;
	send_back_raw_request?: boolean;
	send_back_raw_response?: boolean;
	store_raw_request_response?: boolean;
	custom_provider_config?: CustomProviderConfig;
	openai_config?: OpenAIConfig;
}

// UpdateProviderRequest matching Go's UpdateProviderRequest
export interface UpdateProviderRequest {
	network_config: NetworkConfig;
	concurrency_and_buffer_size: ConcurrencyAndBufferSize;
	proxy_config?: ProxyConfig;
	send_back_raw_request?: boolean;
	send_back_raw_response?: boolean;
	store_raw_request_response?: boolean;
	custom_provider_config?: CustomProviderConfig;
	openai_config?: OpenAIConfig;
}

export interface CreateProviderKeyRequest extends ModelProviderKey {}

export interface UpdateProviderKeyRequest extends ModelProviderKey {}

export interface ListProviderKeysResponse {
	keys: ModelProviderKey[];
	total: number;
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
	model_parameters_url: string;
	mcp_library_url?: string;
	mcp_library_sync_interval?: number;
}

// Auth config
export interface AuthConfig {
	admin_username: SecretVar;
	admin_password: SecretVar;
	is_enabled: boolean;
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
export type PluginSpanFilterMode = "include" | "exclude";

export interface PluginSpanFilter {
	mode: PluginSpanFilterMode;
	plugins: string[];
}

export interface BifrostConfig {
	client_config: CoreConfig;
	framework_config: FrameworkConfig;
	auth_config?: AuthConfig;
	proxy_config?: GlobalProxyConfig;
	restart_required?: RestartRequiredConfig;
	is_db_connected: boolean;
	is_cache_connected: boolean;
	is_logs_connected: boolean;
	is_git_available: boolean;
	auth_token?: string;
	metadata?: Record<string, unknown>;
	env_label?: string;
}

export interface CompatConfig {
	convert_text_to_chat: boolean;
	convert_chat_to_responses: boolean;
	should_drop_params: boolean;
	should_convert_params: boolean;
}

// Core Bifrost configuration types
export interface CoreConfig {
	drop_excess_requests: boolean;
	initial_pool_size: number;
	prometheus_labels: string[];
	enable_logging: boolean;
	disable_content_logging: boolean;
	allow_per_request_content_storage_override: boolean;
	allow_per_request_raw_override: boolean;
	allow_direct_keys: boolean;
	disable_db_pings_in_health: boolean;
	dump_errors_in_console_logs: boolean;
	log_retention_days: number;
	enforce_auth_on_inference: boolean;
	dual_credential_conflict_behavior?: "error" | "prefer_vk" | "prefer_idp";
	allowed_origins: string[];
	allowed_headers: string[];
	max_request_body_size_mb: number;
	compat: CompatConfig;
	mcp_agent_depth: number;
	mcp_tool_execution_timeout: number;
	mcp_code_mode_binding_level?: string;
	mcp_tool_sync_interval: number;
	mcp_disable_auto_tool_inject: boolean;
	mcp_enable_temp_token_auth: boolean;
	async_job_result_ttl: number;
	required_headers: string[];
	logging_headers: string[];
	whitelisted_routes: string[];
	hide_deleted_virtual_keys_in_filters: boolean;
	routing_chain_max_depth: number;
	header_filter_config?: GlobalHeaderFilterConfig;
	mcp_external_client_url?: SecretVar;
	mcp_server_auth_mode?: "headers" | "both" | "oauth";
	oauth2_server_config?: {
		issuer_url?: SecretVar;
		auth_code_ttl?: number;
		access_token_ttl?: number;
		disable_vk_identity?: boolean;
	};
}

export const DefaultCoreConfig: CoreConfig = {
	drop_excess_requests: false,
	initial_pool_size: 1000,
	prometheus_labels: [],
	enable_logging: true,
	disable_content_logging: false,
	allow_per_request_content_storage_override: false,
	allow_per_request_raw_override: false,
	allow_direct_keys: false,
	disable_db_pings_in_health: false,
	dump_errors_in_console_logs: false,
	log_retention_days: 365,
	enforce_auth_on_inference: false,
	dual_credential_conflict_behavior: "prefer_idp",
	allowed_origins: [],
	max_request_body_size_mb: 100,
	compat: { convert_text_to_chat: false, convert_chat_to_responses: false, should_drop_params: false, should_convert_params: false },
	mcp_agent_depth: 10,
	mcp_tool_execution_timeout: 30,
	mcp_code_mode_binding_level: "server",
	mcp_tool_sync_interval: 10,
	mcp_disable_auto_tool_inject: false,
	mcp_enable_temp_token_auth: false,
	async_job_result_ttl: 3600,
	allowed_headers: [],
	required_headers: [],
	logging_headers: [],
	whitelisted_routes: [],
	hide_deleted_virtual_keys_in_filters: false,
	routing_chain_max_depth: 10,
};

// Semantic cache configuration types
interface BaseCacheConfig {
	ttl: number;
	threshold: number;
	conversation_history_threshold?: number;
	exclude_system_prompt?: boolean;
	cache_by_model: boolean;
	cache_by_provider: boolean;
	vector_store_namespace?: string;
	default_cache_key?: string;
	created_at?: string;
	updated_at?: string;
}

export interface DirectCacheConfig extends BaseCacheConfig {
	dimension: 1;
	provider?: undefined;
	embedding_model?: undefined;
}

export interface ProviderBackedCacheConfig extends BaseCacheConfig {
	provider: ModelProviderName;
	embedding_model: string;
	dimension: number;
}

export type CacheConfig = DirectCacheConfig | ProviderBackedCacheConfig;

export interface EditorCacheConfig extends BaseCacheConfig {
	provider?: ModelProviderName;
	embedding_model?: string;
	dimension?: number;
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