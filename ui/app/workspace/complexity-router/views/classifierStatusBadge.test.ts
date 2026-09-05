import type { SemanticStatusInfo } from "@/lib/types/complexityRouter";
import { describe, expect, it } from "vitest";
import { semanticWarmupFailureMessage, semanticWarmupImpactMessage } from "./classifierStatusBadge.utils";

const failedStatus = (overrides: Partial<SemanticStatusInfo> = {}): SemanticStatusInfo => ({
	state: "failed",
	loaded: 0,
	total: 150,
	...overrides,
});

describe("semantic warmup status copy", () => {
	it("maps safe failure reasons to actionable guidance", () => {
		expect(semanticWarmupFailureMessage(failedStatus({ failure_reason: "authentication" }))).toContain("Update its API key");
		expect(semanticWarmupFailureMessage(failedStatus({ failure_reason: "rate_limited" }))).toContain("Wait for capacity");
		expect(semanticWarmupFailureMessage(failedStatus({ failure_reason: "vector_store_unavailable" }))).toContain("vector store");
	});

	it("does not show the old check-server-logs message during a rolling upgrade", () => {
		const message = semanticWarmupFailureMessage(failedStatus({ error: "semantic warmup failed; check server logs" }));
		expect(message).toContain("Review the embedding provider");
		expect(message).not.toContain("server logs");
	});

	it("distinguishes a failed replacement from having no serving classifier", () => {
		expect(semanticWarmupImpactMessage(failedStatus({ serving_previous: true }))).toContain("previous classifier is still routing");
		expect(semanticWarmupImpactMessage(failedStatus({ serving_previous: false }))).toContain("rules are paused");
	});
});