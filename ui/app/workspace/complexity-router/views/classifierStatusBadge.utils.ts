import type { ParseKeys } from "i18next";

import i18n from "@/lib/i18n";
import type { SemanticStatusInfo } from "@/lib/types/complexityRouter";

const complexityT = i18n.getFixedT(null, "models", "routing.complexityUi");

const FAILURE_KEYS: Partial<Record<NonNullable<SemanticStatusInfo["failure_reason"]>, ParseKeys<"models", {}, "routing.complexityUi">>> = {
	authentication: "failureAuthentication",
	model_unavailable: "failureModelUnavailable",
	rate_limited: "failureRateLimited",
	timeout: "failureTimeout",
	provider_unavailable: "failureProviderUnavailable",
	vector_store_unavailable: "failureVectorStore",
	invalid_response: "failureInvalidResponse",
	unknown: "failureUnknown",
};

export function semanticWarmupFailureMessage(status: SemanticStatusInfo): string {
	// Rolling deployments may briefly pair this UI with an older gateway that
	// only returns the former non-actionable message. Do not regress to it.
	if (status.error && !/check server logs/i.test(status.error)) return status.error;
	if (status.failure_reason) return complexityT(FAILURE_KEYS[status.failure_reason] ?? "failureUnknown");
	return complexityT("failureUnknown");
}

export function semanticWarmupImpactMessage(status: SemanticStatusInfo): string {
	return complexityT(status.serving_previous ? "impactServingPrevious" : "impactPaused");
}
