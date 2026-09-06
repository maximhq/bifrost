/**
 * Warp is the dashboard agent that answers questions about the deployment's own
 * telemetry. These types mirror core/schemas/warp.go.
 */

/** What the read API returns. The stored credential is never included. */
export interface WarpConfig {
	/**
	 * Whether Warp has everything it needs to answer: enabled, with a provider
	 * and a model. A credential is deliberately not part of this test, since a
	 * provider reached over a trusted network via base_url may need none.
	 */
	configured: boolean;
	enabled: boolean;
	provider: string;
	model: string;
	base_url?: string;
	/**
	 * Which of the provider's configured keys Warp uses. A reference, not a
	 * credential, so it round-trips in the clear. Empty when none is needed.
	 */
	api_key_id?: string;
	max_iterations: number;
	request_timeout_seconds: number;
	system_prompt_suffix?: string;
	embedding_provider: string;
	embedding_model: string;
	embedding_api_key_id?: string;
	embedding_dimension: number;
	log_vector_store_namespace: string;
	semantic_search_threshold: number;
	semantic_search_limit: number;
	vector_store_connected: boolean;
}

/** The write body. Every field round-trips; nothing here is write-only. */
export interface WarpConfigInput {
	enabled: boolean;
	provider: string;
	model: string;
	base_url?: string;
	api_key_id?: string;
	max_iterations?: number;
	request_timeout_seconds?: number;
	system_prompt_suffix?: string;
	embedding_provider: string;
	embedding_model: string;
	embedding_api_key_id?: string;
	embedding_dimension: number;
	log_vector_store_namespace: string;
	semantic_search_threshold?: number;
	semantic_search_limit?: number;
}

/**
 * Why Warp cannot answer. The two cases need opposite UI treatment: an
 * unconfigured Warp is fixable by the operator and stays visible with a link to
 * its settings, while a deployment with no log store has nothing to read and no
 * in-panel remedy.
 */
export type WarpUnavailableReason = "not_configured" | "no_log_store" | "no_vector_store";

export interface WarpBackfillInput {
	start_time: string;
	end_time: string;
}

export type WarpBackfillState = "idle" | "pending" | "running" | "completed" | "failed" | "cancelled" | "cancelling";

export interface WarpBackfillStatus {
	id?: string;
	status: WarpBackfillState;
	start_time?: string;
	end_time?: string;
	total: number;
	scanned: number;
	indexed: number;
	skipped: number;
	failed: number;
	last_error?: string;
	message?: string;
	created_at?: string;
	started_at?: string;
	completed_at?: string;
}