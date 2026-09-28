/**
 * Warp is the dashboard agent that answers questions about the deployment's own
 * telemetry. These types mirror core/schemas/warp.go.
 */

/** What the read API returns. The stored credential is never included. */
export interface WarpConfig {
	/**
	 * Whether Warp has everything it needs to answer: enabled, with a provider
	 * and a model. A key is deliberately not part of this test, since a provider
	 * using ambient credentials may need none.
	 */
	configured: boolean;
	enabled: boolean;
	provider: string;
	model: string;
	/**
	 * Which of the provider's configured keys Warp uses. A reference, not a
	 * credential, so it round-trips in the clear. Empty when none is needed.
	 */
	api_key_id?: string;
	max_iterations: number;
	request_timeout_seconds: number;
	/**
	 * How long a saved chat is kept after its last turn. Resolved value, so a
	 * deployment that never set one reports the default rather than zero.
	 *
	 * Separate from the log store's retention on purpose: how long request
	 * telemetry is worth keeping and how long someone's conversations stay
	 * theirs to reopen are different questions.
	 */
	history_retention_days: number;
	system_prompt_suffix?: string;
	/**
	 * Absent (not 0) means unset: the provider's own default applies. 0 is a
	 * real, fully deterministic value some operators specifically want, so it
	 * has to round-trip distinctly from "never configured".
	 */
	temperature?: number;
	/** One of WARP_REASONING_EFFORTS, or absent to leave reasoning unset. */
	reasoning_effort?: string;
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
	api_key_id?: string;
	max_iterations?: number;
	request_timeout_seconds?: number;
	/** Zero means "use the default". There is no maximum. */
	history_retention_days?: number;
	system_prompt_suffix?: string;
	temperature?: number;
	reasoning_effort?: string;
	embedding_provider: string;
	embedding_model: string;
	embedding_api_key_id?: string;
	embedding_dimension: number;
	log_vector_store_namespace: string;
	semantic_search_threshold?: number;
	semantic_search_limit?: number;
}

/**
 * Every value reasoning_effort accepts, in the order the settings page lists
 * them. Mirrors schemas.WarpReasoningEfforts.
 */
export const WARP_REASONING_EFFORTS = ["none", "minimal", "low", "medium", "high", "xhigh", "max"] as const;

export const WARP_MIN_TEMPERATURE = 0;
export const WARP_MAX_TEMPERATURE = 2;

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
	/** Discard a resumable checkpoint from a prior failed/cancelled run over this window and scan from the beginning anyway. */
	restart?: boolean;
}

export type WarpBackfillState = "idle" | "pending" | "running" | "completed" | "failed" | "cancelled" | "cancelling";

/**
 * A backfill that exists. Counters and id are only meaningful here - the idle
 * response carries an id-less zeroed body, and reading its "0 / 0 scanned" as a
 * job is how an empty progress bar ends up rendered for a job that never ran.
 */
export interface WarpBackfillJob {
	id: string;
	status: Exclude<WarpBackfillState, "idle">;
	start_time?: string;
	end_time?: string;
	total: number;
	scanned: number;
	indexed: number;
	skipped: number;
	failed: number;
	/** Tokens the job's embedding calls have consumed so far, across resumes. */
	embedding_tokens?: number;
	/** USD cost of those calls. Absent when the deployment cannot price them - unknown, not free. */
	embedding_cost?: number;
	last_error?: string;
	message?: string;
	created_at?: string;
	started_at?: string;
	completed_at?: string;
}

/** No job for this deployment. `id?: undefined` is what makes `status.id` narrow. */
export interface WarpBackfillIdle {
	status: "idle";
	id?: undefined;
}

export type WarpBackfillStatus = WarpBackfillIdle | WarpBackfillJob;

/** One saved thread, without its transcript. Mirrors schemas.WarpConversation. */
export interface WarpConversation {
	id: string;
	title: string;
	message_count: number;
	total_tokens: number;
	total_cost: number;
	created_at: string;
	updated_at: string;
}

export interface WarpStoredToolCall {
	name: string;
	duration_ms?: number;
	failed?: boolean;
	/** Unicode code points of the answer that preceded this call. */
	text_offset?: number;
}

/** One persisted turn. Mirrors schemas.WarpStoredMessage. */
export interface WarpStoredMessage {
	role: "user" | "assistant";
	content: string;
	tool_calls?: WarpStoredToolCall[];
	error?: string;
	finish_reason?: string;
	/**
	 * The structured clarifying question this turn ended with, when it ended by
	 * asking. Mirrors the live question event's shape, so a reopened thread
	 * rebuilds the same selectable card with its hints intact.
	 */
	question?: {
		question: string;
		options?: { label: string; hint?: string }[];
		allow_other?: boolean;
		kind?: string;
	};
	total_tokens?: number;
	cost?: number;
	created_at: string;
}

export interface WarpConversationDetail extends WarpConversation {
	messages: WarpStoredMessage[];
	/**
	 * The transcript hit the repository bound and older messages were cut.
	 * message_count still reports the thread's real size, so a bounded
	 * transcript renders as bounded rather than complete.
	 */
	truncated?: boolean;
}

/**
 * Whether semantic search is usable, in one word. Mirrors the states the
 * log-index status endpoint reports.
 */
export type WarpLogIndexState = "unavailable" | "not_configured" | "indexing" | "failed" | "ready";

export interface WarpLogIndexStatus {
	state: WarpLogIndexState;
	vector_store_connected: boolean;
	embedding_configured: boolean;
	backfill?: WarpBackfillStatus;
}