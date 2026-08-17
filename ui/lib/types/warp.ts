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
	/** Whether a credential is stored. The value itself never leaves the server. */
	/**
	 * Names one of the deployment's configured provider keys. A reference, not a
	 * secret, so it round-trips like any other field - no redaction, no
	 * presence flag, and no omitted-versus-empty ambiguity.
	 */
	api_key_id?: string;
	max_iterations: number;
	request_timeout_seconds: number;
	system_prompt_suffix?: string;
}

/**
 * The write body. Every field round-trips; nothing here is write-only.
 *
 * `api_key_id` names one of the deployment's already-configured provider keys
 * rather than carrying a credential, which is what removes the omitted-versus-
 * empty ambiguity a write-only secret forces: an ordinary save sends the current
 * reference back, and an empty value means "no key" - legitimate for a provider
 * on a trusted network or one using ambient credentials.
 */
export interface WarpConfigInput {
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
 * Why Warp cannot answer. The two cases need opposite UI treatment: an
 * unconfigured Warp is fixable by the operator and stays visible with a link to
 * its settings, while a deployment with no log store has nothing to read and no
 * in-panel remedy.
 */
export type WarpUnavailableReason = "not_configured" | "no_log_store";