export interface AccessProfileBudgetLine {
	id: string;
	scope: string;
	max_limit: number;
	reset_duration: string;
	current_usage: number;
	last_reset: string;
	override_amount?: number;
	override_mode?: "cycles" | "forever";
	override_cycles_remaining?: number;
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

export interface VKCreationPolicyResponse {
	has_access_profile: boolean;
	profile_name?: string;
}

/** The entity kinds an access profile can be attached to. Mirrors the enterprise type so the
 * fallback component stubs can carry the same signature. */
export type AccessProfileEntityKind = "team" | "business_unit" | "customer";

/**
 * The shape OSS consumers read off the entity-profile query.
 *
 * Only the fields the OSS build actually touches: the budget editors and the virtual key sheet ask
 * whether a profile governs the entity, and which one. Typed rather than left as `undefined` so those consumers
 * compile against the same property they read in the enterprise build.
 */
export interface EntityAccessProfile {
	id: number;
	entity_type: AccessProfileEntityKind;
	entity_id: string;
	name: string;
	is_active: boolean;
}

export interface GetEntityAccessProfileResponse {
	access_profile: EntityAccessProfile;
	virtual_keys: Array<{ id: string; name: string; is_active: boolean }>;
}