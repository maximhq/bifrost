import type { ModelProvider } from "@/lib/types/config";
import i18n from "@/lib/i18n";
import { beforeAll, describe, expect, it } from "vitest";
import {
	embeddingSpaceChanged,
	normalizeWarpNamespace,
	supportsWarpEmbedding,
	validateWarpEmbedding,
	type WarpEmbeddingFields,
} from "./warpConfig.utils";

const valid: WarpEmbeddingFields = {
	embeddingProvider: "openai",
	embeddingModel: "text-embedding-3-small",
	embeddingDimension: 1536,
	namespace: "BifrostWarpLogs",
	threshold: 0.8,
	searchLimit: 10,
};

beforeAll(async () => {
	await i18n.changeLanguage("en");
});

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
// Changing the embedding space invalidates every vector already indexed, which
// is why a rename is demanded. But there is nothing to invalidate before the
// first space is saved - and demanding a new namespace there blocks the very
// first setup behind a rule about data that does not exist yet.
describe("embeddingSpaceChanged with no saved space", () => {
	const filled: WarpEmbeddingFields = {
		embeddingProvider: "openai",
		embeddingModel: "text-embedding-3-small",
		embeddingDimension: 1536,
		namespace: "BifrostWarpLogs",
		threshold: 0.8,
		searchLimit: 10,
	};

	it("reports no change when nothing was saved before", () => {
		for (const saved of [
			{ ...filled, embeddingProvider: "", embeddingModel: "", embeddingDimension: 0 },
			{ ...filled, embeddingProvider: "openai", embeddingModel: "", embeddingDimension: 0 },
			{ ...filled, embeddingDimension: 0 },
		]) {
			expect(embeddingSpaceChanged(filled, saved)).toBe(false);
		}
	});

	it("still reports a change between two complete spaces", () => {
		expect(embeddingSpaceChanged({ ...filled, embeddingModel: "text-embedding-3-large" }, filled)).toBe(true);
		expect(embeddingSpaceChanged(filled, filled)).toBe(false);
	});
});
describe("normalizeWarpNamespace", () => {
	it("makes the rename guard see what the save path actually sends", () => {
		// onSubmit sends form.namespace.trim(). Comparing the raw value let a
		// trailing space count as "you renamed it", after which the old namespace
		// was submitted anyway and the reindex silently overwrote the old space.
		expect(normalizeWarpNamespace("  warp-logs  ")).toBe(normalizeWarpNamespace("warp-logs"));
		expect(normalizeWarpNamespace("warp-logs-v2")).not.toBe(normalizeWarpNamespace("warp-logs"));
		expect(normalizeWarpNamespace("   ")).toBe("");
	});
});
describe("embeddingSpaceChanged normalizes before comparing", () => {
	const saved = {
		embeddingProvider: "openai",
		embeddingModel: "text-embedding-3-small",
		embeddingDimension: 1536,
		namespace: "BifrostWarpLogs",
		threshold: 0.5,
		searchLimit: 10,
	};

	it("does not call whitespace a different embedding space", () => {
		// onSubmit sends embedding_model.trim() and embedding_provider.trim(), so
		// comparing the raw field demanded a namespace rename for a payload that
		// carried the identical space.
		expect(embeddingSpaceChanged({ ...saved, embeddingModel: "  text-embedding-3-small  " }, saved)).toBe(false);
		expect(embeddingSpaceChanged({ ...saved, embeddingProvider: " openai " }, saved)).toBe(false);
	});

	it("still sees a real change", () => {
		expect(embeddingSpaceChanged({ ...saved, embeddingModel: "text-embedding-3-large" }, saved)).toBe(true);
		expect(embeddingSpaceChanged({ ...saved, embeddingDimension: 3072 }, saved)).toBe(true);
	});
});