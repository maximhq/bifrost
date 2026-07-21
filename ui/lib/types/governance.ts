// Governance types that match the Go backend structures

import { ModelProviderName, RequestType } from "./config";

export interface Budget {
	id: string;
	max_limit: number; // In dollars
	reset_duration: string; // e.g., "30s", "5m", "1h", "1d", "1w", "1M"
	current_usage: number; // In dollars
	last_reset: string; // ISO timestamp
}

export interface RateLimit {
	id: string;
	// Flexible token limits
	token_max_limit?: number; // Maximum tokens allowed
	token_reset_duration?: string; // e.g., "30s", "5m", "1h", "1d", "1w", "1M"
	token_current_usage: number; // Current token usage
	token_last_reset: string; // ISO timestamp
	// Flexible request limits
	request_max_limit?: number; // Maximum requests allowed
	request_reset_duration?: string; // e.g., "30s", "5m", "1h", "1d", "1w", "1M"
	request_current_usage: number; // Current request usage
	request_last_reset: string; // ISO timestamp
}

export interface Team {
	id: string;
	name: string;
	customer_id?: string;
	rate_limit_id?: string;
	// Team-wide: applies to all team budgets and the team rate limit
	calendar_aligned?: boolean;
	// Populated relationships
	customer?: Customer;
	budgets?: Budget[]; // Multi-budget: each with a distinct reset_duration
	rate_limit?: RateLimit;
}

export interface Customer {
	id: string;
	name: string;
	rate_limit_id?: string;
	calendar_aligned?: boolean;
	// Populated relationships
	teams?: Team[];
	budgets?: Budget[];
	rate_limit?: RateLimit;
}

export interface DBKey {
	key_id: string; // UUID identifier for the key
	name: string; // Name of the key
	provider_id: string; // identifier for the provider
	models: string[]; // List of models this key can access
	provider: ModelProviderName; // Provider name
}

export interface RedactedDBKey {
	id: string;
	name: string;
	models: string[];
	weight: number;
}

export interface VirtualKey {
	id: string;
	name: string;
	value: string; // The actual key value
	description?: string;
	provider_configs?: VirtualKeyProviderConfig[];
	mcp_configs?: VirtualKeyMCPConfig[];
	team_id?: string;
	customer_id?: string;
	rate_limit_id?: string;
	is_active: boolean;
	expires_at?: string | null; // ISO 8601 UTC timestamp; null or absent means never expires
	previous_value_expires_at?: string | null; // When set, the pre-rotation value still authenticates until this time
	rotated_at?: string | null; // Timestamp of the last value rotation
	calendar_aligned?: boolean;
	created_at: string;
	updated_at: string;
	// Populated relationships
	team?: Team;
	customer?: Customer;
	budgets?: Budget[];
	rate_limit?: RateLimit;
	config_hash?: string; // Present when config is synced from config.json
}

export interface VirtualKeyProviderConfig {
	id?: number;
	provider: string;
	weight: number | null;
	allowed_models: string[];
	blacklisted_models: string[];
	allow_all_keys: boolean; // True means all keys allowed; false with empty keys means no keys allowed
	budgets?: Budget[];
	rate_limit?: RateLimit;
	keys?: DBKey[]; // Associated database keys for this provider (only used when allow_all_keys is false)
}

export interface VirtualKeyMCPConfig {
	id?: number;
	virtual_key_id?: string;
	mcp_client_id?: number;
	mcp_client?: {
		id: number;
		name: string;
		connection_type: string;
		connection_string?: string;
		tools_to_execute: string[];
		created_at: string;
		updated_at: string;
	};
	tools_to_execute?: string[];
}

// Request interfaces for create/update operations (still use mcp_client_name)
export interface VirtualKeyMCPConfigRequest {
	id?: number;
	mcp_client_name: string;
	tools_to_execute?: string[];
}

export interface UsageStats {
	virtual_key_id: string;
	provider?: string;
	model?: string;
	tokens_current_usage: number;
	requests_current_usage: number;
	tokens_last_reset: string;
	requests_last_reset: string;
}

// Request interfaces for provider config operations
export interface VirtualKeyProviderConfigRequest {
	provider: string;
	weight?: number | null;
	allowed_models?: string[];
	blacklisted_models?: string[];
	budgets?: CreateBudgetRequest[];
	rate_limit?: CreateRateLimitRequest;
	key_ids?: string[]; // List of DBKey UUIDs to associate with this provider config
}

export interface VirtualKeyProviderConfigUpdateRequest {
	id?: number;
	provider: string;
	weight?: number | null;
	allowed_models?: string[];
	blacklisted_models?: string[];
	budgets?: CreateBudgetRequest[];
	rate_limit?: UpdateRateLimitRequest;
	key_ids?: string[]; // List of DBKey UUIDs to associate with this provider config
}

// Request types for API calls
export interface CreateVirtualKeyRequest {
	name: string;
	description?: string;
	provider_configs?: VirtualKeyProviderConfigRequest[];
	mcp_configs?: VirtualKeyMCPConfigRequest[];
	team_id?: string;
	customer_id?: string;
	budgets?: CreateBudgetRequest[];
	rate_limit?: CreateRateLimitRequest;
	is_active?: boolean;
	calendar_aligned?: boolean;
	expires_at?: string; // RFC3339 UTC timestamp; omit for a key that never expires
}

export interface UpdateVirtualKeyRequest {
	name?: string;
	description?: string;
	provider_configs?: VirtualKeyProviderConfigUpdateRequest[];
	mcp_configs?: VirtualKeyMCPConfigRequest[];
	team_id?: string | null;
	customer_id?: string | null;
	budgets?: CreateBudgetRequest[];
	rate_limit?: UpdateRateLimitRequest;
	is_active?: boolean;
	calendar_aligned?: boolean;
	reset_budget_usage?: boolean;
	expires_at?: string; // RFC3339 UTC timestamp sets a new expiry, "" clears it, omit to leave unchanged
}

export interface BulkRotateVirtualKeysRequest {
	ids: string[];
}

export interface BulkRotateVirtualKeysResponse {
	message: string;
	virtual_keys: VirtualKey[];
	errors?: Record<string, string>;
}

export interface CreateTeamRequest {
	name: string;
	customer_id?: string;
	budgets?: CreateBudgetRequest[]; // Multi-budget: each must have a unique reset_duration
	rate_limit?: CreateRateLimitRequest;
	calendar_aligned?: boolean; // Team-wide: applies to all team budgets and the team rate limit
}

export interface UpdateTeamRequest {
	name?: string;
	customer_id?: string;
	budgets?: CreateBudgetRequest[]; // Replaces all team budgets; empty array clears
	rate_limit?: UpdateRateLimitRequest;
	calendar_aligned?: boolean;
}

export interface CreateCustomerRequest {
	name: string;
	budgets?: CreateBudgetRequest[];
	budget?: CreateBudgetRequest; // deprecated: use budgets
	rate_limit?: CreateRateLimitRequest;
	calendar_aligned?: boolean;
}

export interface UpdateCustomerRequest {
	name?: string;
	budgets?: CreateBudgetRequest[]; // nil=no change, []=remove all
	budget?: UpdateBudgetRequest; // deprecated: use budgets
	rate_limit?: UpdateRateLimitRequest;
	calendar_aligned?: boolean;
}

export interface CreateBudgetRequest {
	id?: string;
	max_limit: number; // In dollars
	reset_duration: string; // e.g., "30s", "5m", "1h", "1d", "1w", "1M"
}

export interface UpdateBudgetRequest {
	max_limit?: number;
	reset_duration?: string;
}

export interface CreateRateLimitRequest {
	token_max_limit?: number; // Maximum tokens allowed
	token_reset_duration?: string; // e.g., "30s", "5m", "1h", "1d", "1w", "1M"
	request_max_limit?: number; // Maximum requests allowed
	request_reset_duration?: string; // e.g., "30s", "5m", "1h", "1d", "1w", "1M"
}

export interface UpdateRateLimitRequest {
	token_max_limit?: number | null; // Maximum tokens allowed (null to clear)
	token_reset_duration?: string | null; // e.g., "30s", "5m", "1h", "1d", "1w", "1M" (null to clear)
	request_max_limit?: number | null; // Maximum requests allowed (null to clear)
	request_reset_duration?: string | null; // e.g., "30s", "5m", "1h", "1d", "1w", "1M" (null to clear)
}

export interface ResetUsageRequest {
	virtual_key_id: string;
	provider?: string;
	model?: string;
}

// Query params
export interface GetVirtualKeysParams {
	limit?: number;
	offset?: number;
	search?: string;
	customer_id?: string;
	team_id?: string;
	exclude_access_profile_managed_virtual?: boolean;
	exclude_assigned_virtual_keys?: boolean;
	for_user_assignment?: boolean;
	sort_by?: "name" | "budget_spent" | "created_at" | "status";
	order?: "asc" | "desc";
	export?: boolean;
}

// Response types
export interface GetVirtualKeysResponse {
	virtual_keys: VirtualKey[];
	count: number;
	total_count: number;
	limit: number;
	offset: number;
}

export interface GetTeamsParams {
	limit?: number;
	offset?: number;
	search?: string;
	customer_id?: string;
}

export interface GetTeamsResponse {
	teams: Team[];
	count: number;
	total_count: number;
	limit: number;
	offset: number;
}

export interface GetCustomersParams {
	limit?: number;
	offset?: number;
	search?: string;
}

export interface GetCustomersResponse {
	customers: Customer[];
	count: number;
	total_count: number;
	limit: number;
	offset: number;
}

export interface GetBudgetsResponse {
	budgets: Budget[];
	count: number;
}

export interface GetRateLimitsResponse {
	rate_limits: RateLimit[];
	count: number;
}

export interface GetUsageStatsResponse {
	virtual_key_id?: string;
	usage_stats: UsageStats | UsageStats[];
}

export interface DebugStatsResponse {
	plugin_stats: Record<string, any>;
	database_stats: {
		virtual_keys_count: number;
		teams_count: number;
		customers_count: number;
		budgets_count: number;
		rate_limits_count: number;
		usage_tracking_count: number;
		audit_logs_count: number;
	};
	timestamp: string;
}

export interface HealthCheckResponse {
	status: "healthy" | "unhealthy" | "warning";
	timestamp: string;
	checks: Record<
		string,
		{
			status: "healthy" | "unhealthy" | "warning";
			error?: string;
			message?: string;
		}
	>;
}

// Model Config for per-model budgeting and rate limiting
export interface ModelConfig {
	id: string;
	model_name: string;
	provider?: string; // Optional provider - if empty/null, applies to all providers
	scope?: string; // "global" (default) or "virtual_key"
	scope_id?: string; // Target of a non-global scope (e.g. the virtual key ID)
	scope_name?: string; // Resolved, human-readable name of the scope target (read-only)
	calendar_aligned?: boolean; // Snap budget resets to calendar boundaries (inherited from VK for vk scope)
	rate_limit_id?: string;
	// Populated relationships
	budgets?: Budget[]; // Multi-budget: each with a distinct reset_duration
	budget?: Budget; // Deprecated: superseded by budgets (kept for back-compat reads)
	rate_limit?: RateLimit;
	created_at: string;
	updated_at: string;
}

// Request types for model config operations
export interface CreateModelConfigRequest {
	model_name: string;
	provider?: string; // Optional provider - if empty/null, applies to all providers
	scope?: string; // Defaults to "global" if omitted
	scope_id?: string; // Required for non-global scopes (e.g. the virtual key ID)
	budgets?: CreateBudgetRequest[];
	rate_limit?: CreateRateLimitRequest;
}

export interface UpdateModelConfigRequest {
	model_name?: string;
	provider?: string; // Optional provider - if empty/null, applies to all providers
	budgets?: CreateBudgetRequest[]; // Full desired set; reconciled against existing
	rate_limit?: UpdateRateLimitRequest;
}

export interface GetModelConfigsParams {
	limit?: number;
	offset?: number;
	search?: string;
	scope?: string;
	provider?: string;
}

// Response types for model configs
export interface GetModelConfigsResponse {
	model_configs: ModelConfig[];
	count: number;
	total_count: number;
	limit: number;
	offset: number;
}

export type PricingOverrideScopeKind =
	| "global"
	| "provider"
	| "provider_key"
	| "virtual_key"
	| "virtual_key_provider"
	| "virtual_key_provider_key";
export type PricingOverrideMatchType = "exact" | "wildcard";

export interface PricingOverridePatch {
	// Token
	input_cost_per_token?: number;
	output_cost_per_token?: number;
	input_cost_per_token_batches?: number;
	output_cost_per_token_batches?: number;
	input_cost_per_token_priority?: number;
	output_cost_per_token_priority?: number;
	input_cost_per_token_flex?: number;
	output_cost_per_token_flex?: number;
	input_cost_per_character?: number;
	input_cost_per_token_fast?: number;
	output_cost_per_token_fast?: number;
	// 128k tier
	input_cost_per_token_above_128k_tokens?: number;
	output_cost_per_token_above_128k_tokens?: number;
	input_cost_per_image_above_128k_tokens?: number;
	input_cost_per_video_per_second_above_128k_tokens?: number;
	input_cost_per_audio_per_second_above_128k_tokens?: number;
	// 200k tier
	input_cost_per_token_above_200k_tokens?: number;
	input_cost_per_token_above_200k_tokens_priority?: number;
	output_cost_per_token_above_200k_tokens?: number;
	output_cost_per_token_above_200k_tokens_priority?: number;
	// 272k tier
	input_cost_per_token_above_272k_tokens?: number;
	input_cost_per_token_above_272k_tokens_priority?: number;
	input_cost_per_token_flex_above_272k_tokens?: number;
	output_cost_per_token_above_272k_tokens?: number;
	output_cost_per_token_above_272k_tokens_priority?: number;
	output_cost_per_token_flex_above_272k_tokens?: number;
	// Cache
	cache_creation_input_token_cost?: number;
	cache_read_input_token_cost?: number;
	cache_creation_input_token_cost_above_200k_tokens?: number;
	cache_read_input_token_cost_above_200k_tokens?: number;
	cache_read_input_token_cost_above_200k_tokens_priority?: number;
	cache_creation_input_token_cost_above_1hr?: number;
	cache_creation_input_token_cost_above_1hr_above_200k_tokens?: number;
	cache_creation_input_audio_token_cost?: number;
	cache_read_input_token_cost_priority?: number;
	cache_read_input_token_cost_flex?: number;
	cache_read_input_image_token_cost?: number;
	cache_read_input_token_cost_above_272k_tokens?: number;
	cache_read_input_token_cost_above_272k_tokens_priority?: number;
	cache_read_input_token_cost_flex_above_272k_tokens?: number;
	cache_creation_input_token_cost_above_272k_tokens?: number;
	cache_creation_input_token_cost_flex?: number;
	cache_creation_input_token_cost_flex_above_272k_tokens?: number;
	cache_creation_input_token_cost_priority?: number;
	cache_creation_input_token_cost_fast?: number;
	cache_creation_input_token_cost_above_1hr_fast?: number;
	cache_read_input_token_cost_fast?: number;
	// Image
	input_cost_per_image_token?: number;
	output_cost_per_image_token?: number;
	input_cost_per_image?: number;
	input_cost_per_pixel?: number;
	output_cost_per_image?: number;
	output_cost_per_pixel?: number;
	output_cost_per_image_premium_image?: number;
	output_cost_per_image_above_512_and_512_pixels?: number;
	output_cost_per_image_above_512_and_512_pixels_and_premium_image?: number;
	output_cost_per_image_above_1024_and_1024_pixels?: number;
	output_cost_per_image_above_1024_and_1024_pixels_and_premium_image?: number;
	output_cost_per_image_low_quality?: number;
	output_cost_per_image_medium_quality?: number;
	output_cost_per_image_high_quality?: number;
	output_cost_per_image_auto_quality?: number;
	// Audio/Video
	input_cost_per_audio_token?: number;
	input_cost_per_audio_per_second?: number;
	input_cost_per_second?: number;
	input_cost_per_video_per_second?: number;
	output_cost_per_audio_token?: number;
	output_cost_per_video_per_second?: number;
	output_cost_per_second?: number;
	// Other
	search_context_cost_per_query?: number;
	code_interpreter_cost_per_session?: number;
	inference_geo_us_multiplier?: number;
	// OCR
	ocr_cost_per_page?: number;
	annotation_cost_per_page?: number;
}

export interface PricingOverride {
	id: string;
	name: string;
	scope_kind: PricingOverrideScopeKind;
	virtual_key_id?: string;
	provider_id?: string;
	provider_key_id?: string;
	match_type: PricingOverrideMatchType;
	pattern: string;
	request_types?: RequestType[];
	pricing_patch: string;
	config_hash?: string;
	created_at: string;
	updated_at: string;
}

export interface CreatePricingOverrideRequest {
	name: string;
	scope_kind: PricingOverrideScopeKind;
	virtual_key_id?: string;
	provider_id?: string;
	provider_key_id?: string;
	match_type: PricingOverrideMatchType;
	pattern: string;
	request_types: RequestType[];
	patch?: PricingOverridePatch;
}

export interface UpdatePricingOverrideRequest {
	name?: string;
	scope_kind?: PricingOverrideScopeKind;
	virtual_key_id?: string;
	provider_id?: string;
	provider_key_id?: string;
	match_type?: PricingOverrideMatchType;
	pattern?: string;
	request_types?: string[];
	patch?: PricingOverridePatch;
}

export interface GetPricingOverridesResponse {
	pricing_overrides: PricingOverride[];
	count: number;
	total_count: number;
	limit: number;
	offset: number;
}

// Provider governance - for extending provider with budget/rate limit
export interface ProviderGovernance {
	provider: string;
	budget?: Budget; // deprecated: use budgets
	budgets?: Budget[];
	rate_limit?: RateLimit;
	calendar_aligned?: boolean;
}

export interface UpdateProviderGovernanceRequest {
	budgets?: CreateBudgetRequest[]; // [] = remove all
	rate_limit?: UpdateRateLimitRequest;
	calendar_aligned?: boolean;
}

export interface GetProviderGovernanceResponse {
	providers: ProviderGovernance[];
	count: number;
}