import type { ModelProvider } from "@/lib/types/config";
import { describe, expect, it } from "vitest";
import { embeddingSpaceChanged, supportsWarpEmbedding, validateWarpEmbedding, type WarpEmbeddingFields } from "./warpConfig.utils";

const valid: WarpEmbeddingFields = {
	embeddingProvider: "openai",
	embeddingModel: "text-embedding-3-small",
	embeddingDimension: 1536,
	namespace: "BifrostWarpLogs",
	threshold: 0.8,
	searchLimit: 10,
};

describe("Warp embedding configuration", () => {
	it("requires a connected vector store and complete embedding space when enabled", () => {
		expect(validateWarpEmbedding(valid, true, false)).toContain("vector store");
		expect(validateWarpEmbedding({ ...valid, embeddingModel: "" }, true, true)).toContain("embedding model");
		expect(validateWarpEmbedding(valid, true, true)).toBeNull();
		expect(validateWarpEmbedding({ ...valid, embeddingModel: "" }, false, false)).toBeNull();
	});

	it("detects provider, model, and dimension changes as a new embedding space", () => {
		expect(embeddingSpaceChanged({ ...valid }, valid)).toBe(false);
		expect(embeddingSpaceChanged({ ...valid, embeddingDimension: 3072 }, valid)).toBe(true);
	});

	it("includes built-in embedding providers and explicit custom providers", () => {
		expect(supportsWarpEmbedding({ name: "openai" } as ModelProvider)).toBe(true);
		expect(
			supportsWarpEmbedding({
				name: "my-provider",
				custom_provider_config: { allowed_requests: { embedding: true } },
			} as ModelProvider),
		).toBe(true);
		expect(
			supportsWarpEmbedding({
				name: "my-chat-provider",
				custom_provider_config: { allowed_requests: { chat_completion: true } },
			} as ModelProvider),
		).toBe(false);
	});
});