export interface AccessProfileBudgetLine {
	id: string;
	scope: string;
	max_limit: number;
	reset_duration: string;
	current_usage: number;
	last_reset: string;
	alert_thresholds?: number[];
}

export interface AccessProfileRateLimitLine {
	token_max_limit?: number;
	token_reset_duration?: string;
	token_current_usage?: number;
	token_last_reset?: string;
	request_max_limit?: number;
	request_reset_duration?: string;
	request_current_usage?: number;
	request_last_reset?: string;
}

export interface UserAccessProfile {
	id: number;
	user_id: string;
	parent_profile_id?: number;
	virtual_key_ids?: string[];
	virtual_key_values?: Record<string, string>;
	name: string;
	is_active: boolean;
	expires_at?: string;
	provider_configs?: unknown[];
	budgets?: AccessProfileBudgetLine[];
	rate_limit?: AccessProfileRateLimitLine;
	mcp_configs?: unknown;
	created_at: string;
	updated_at: string;
}

export interface GetUserAccessProfilesResponse {
	access_profiles: UserAccessProfile[];
}