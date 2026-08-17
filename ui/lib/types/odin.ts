/**
 * Odin is the dashboard agent that answers questions about the deployment's own
 * telemetry. These types mirror core/schemas/odin.go.
 */

/** What the read API returns. The stored credential is never included. */
export interface OdinConfig {
	/**
	 * Whether Odin has everything it needs to answer: enabled, with a provider
	 * and a model. A credential is deliberately not part of this test, since a
	 * provider reached over a trusted network via base_url may need none.
	 */
	configured: boolean;
	enabled: boolean;
	provider: string;
	model: string;
	base_url?: string;
	/**
	 * Which of the provider's configured keys Odin uses. A reference, not a
	 * credential, so it round-trips in the clear. Empty when none is needed.
	 */
	api_key_id?: string;
	max_iterations: number;
	request_timeout_seconds: number;
	system_prompt_suffix?: string;
}

/** The write body. Every field round-trips; nothing here is write-only. */
export interface OdinConfigInput {
	enabled: boolean;
	provider: string;
	model: string;
	base_url?: string;
	api_key_id?: string;
	max_iterations?: number;
	request_timeout_seconds?: number;
	system_prompt_suffix?: string;
}

/**
 * Why Odin cannot answer. The two cases need opposite UI treatment: an
 * unconfigured Odin is fixable by the operator and stays visible with a link to
 * its settings, while a deployment with no log store has nothing to read and no
 * in-panel remedy.
 */
export type OdinUnavailableReason = "not_configured" | "no_log_store";