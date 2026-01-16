// Governance types that match the Go backend structures

import { ModelProviderName } from "./config";

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
	budget_id?: string;
	// Populated relationships
	customer?: Customer;
	budget?: Budget;
}

export interface Customer {
	id: string;
	name: string;
	budget_id?: string;
	// Populated relationships
	teams?: Team[];
	budget?: Budget;
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
	budget_id?: string;
	rate_limit_id?: string;
	is_active: boolean;
	created_at: string;
	updated_at: string;
	// Populated relationships
	team?: Team;
	customer?: Customer;
	budget?: Budget;
	rate_limit?: RateLimit;
	config_hash?: string; // Present when config is synced from config.json
}

export interface VirtualKeyProviderConfig {
	id?: number;
	provider: string;
	weight: number;
	allowed_models: string[];
	budget?: Budget;
	rate_limit?: RateLimit;
	keys?: DBKey[]; // Associated database keys for this provider
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
	weight?: number;
	allowed_models?: string[];
	budget?: CreateBudgetRequest;
	rate_limit?: CreateRateLimitRequest;
	key_ids?: string[]; // List of DBKey UUIDs to associate with this provider config
}

export interface VirtualKeyProviderConfigUpdateRequest {
	id?: number;
	provider: string;
	weight?: number;
	allowed_models?: string[];
	budget?: UpdateBudgetRequest;
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
	budget?: CreateBudgetRequest;
	rate_limit?: CreateRateLimitRequest;
	is_active?: boolean;
}

export interface UpdateVirtualKeyRequest {
	name?: string;
	description?: string;
	provider_configs?: VirtualKeyProviderConfigUpdateRequest[];
	mcp_configs?: VirtualKeyMCPConfigRequest[];
	team_id?: string;
	customer_id?: string;
	budget?: UpdateBudgetRequest;
	rate_limit?: UpdateRateLimitRequest;
	is_active?: boolean;
}

export interface CreateTeamRequest {
	name: string;
	customer_id?: string;
	budget?: CreateBudgetRequest;
}

export interface UpdateTeamRequest {
	name?: string;
	customer_id?: string;
	budget?: UpdateBudgetRequest;
}

export interface CreateCustomerRequest {
	name: string;
	budget?: CreateBudgetRequest;
}

export interface UpdateCustomerRequest {
	name?: string;
	budget?: UpdateBudgetRequest;
}

export interface CreateBudgetRequest {
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

// Response types
export interface GetVirtualKeysResponse {
	virtual_keys: VirtualKey[];
	count: number;
}

export interface GetTeamsResponse {
	teams: Team[];
	count: number;
}

export interface GetCustomersResponse {
	customers: Customer[];
	count: number;
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
	budget_id?: string;
	rate_limit_id?: string;
	// Populated relationships
	budget?: Budget;
	rate_limit?: RateLimit;
	created_at: string;
	updated_at: string;
}

// Request types for model config operations
export interface CreateModelConfigRequest {
	model_name: string;
	provider?: string; // Optional provider - if empty/null, applies to all providers
	budget?: CreateBudgetRequest;
	rate_limit?: CreateRateLimitRequest;
}

export interface UpdateModelConfigRequest {
	model_name?: string;
	provider?: string; // Optional provider - if empty/null, applies to all providers
	budget?: UpdateBudgetRequest;
	rate_limit?: UpdateRateLimitRequest;
}

// Response types for model configs
export interface GetModelConfigsResponse {
	model_configs: ModelConfig[];
	count: number;
}

// Provider governance - for extending provider with budget/rate limit
export interface ProviderGovernance {
	provider: string;
	budget_id?: string;
	rate_limit_id?: string;
	budget?: Budget;
	rate_limit?: RateLimit;
}

export interface UpdateProviderGovernanceRequest {
	budget?: UpdateBudgetRequest;
	rate_limit?: UpdateRateLimitRequest;
}

export interface GetProviderGovernanceResponse {
	providers: ProviderGovernance[];
	count: number;
}
