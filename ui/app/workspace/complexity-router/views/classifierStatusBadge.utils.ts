import type { SemanticStatusInfo } from "@/lib/types/complexityRouter";

const FAILURE_MESSAGES: Partial<Record<NonNullable<SemanticStatusInfo["failure_reason"]>, string>> = {
	authentication: "Authentication failed for the selected embedding provider. Update its API key to restart warmup.",
	model_unavailable: "The selected embedding model could not be used. Choose a supported embedding model and save the configuration again.",
	rate_limited: "The embedding provider's rate limit prevented warmup. Wait for capacity, then save the configuration again.",
	timeout: "The embedding provider did not respond during warmup. Check provider availability, then save the configuration again.",
	provider_unavailable: "The embedding provider is unavailable. Updating its configuration or API key restarts warmup.",
	vector_store_unavailable:
		"The vector store could not prepare the classifier index. Check its connectivity and configuration, then save again.",
	invalid_response: "The selected model returned an invalid embedding response. Choose a model that supports embeddings and save again.",
	unknown:
		"The classifier could not prepare the reference phrases. Review the embedding provider, model, and vector store settings, then save again.",
};

export function semanticWarmupFailureMessage(status: SemanticStatusInfo): string {
	// Rolling deployments may briefly pair this UI with an older gateway that
	// only returns the former non-actionable message. Do not regress to it.
	if (status.error && !/check server logs/i.test(status.error)) return status.error;
	if (status.failure_reason) return FAILURE_MESSAGES[status.failure_reason] ?? FAILURE_MESSAGES.unknown!;
	return FAILURE_MESSAGES.unknown!;
}

export function semanticWarmupImpactMessage(status: SemanticStatusInfo): string {
	return status.serving_previous
		? "The previous classifier is still routing requests. Your latest phrase or embedding changes will become active after warmup succeeds."
		: "Complexity-tier rules are paused until the classifier is ready. Other routing rules continue normally.";
}