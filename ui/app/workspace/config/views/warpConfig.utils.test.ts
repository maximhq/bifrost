import type { ModelProvider } from "@/lib/types/config";
import { describe, expect, it } from "vitest";
import {
	embeddingSpaceChanged,
	normalizeWarpNamespace,
	supportsWarpEmbedding,
	validateWarpEmbedding,
	validateWarpModelRows,
	warpModelRowsChanged,
	warpModelRowsFromConfig,
	warpModelsPayload,
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

describe("Warp model rows", () => {
	const stored = {
		provider: "openai",
		model: "gpt-4o",
		api_key_id: "key-1",
		additional_models: [{ provider: "anthropic", model: "claude-sonnet-5" }],
	};
	const values = (rows: { provider: string; model: string; apiKeyID: string }[]) =>
		rows.map(({ provider, model, apiKeyID }) => ({ provider, model, apiKeyID }));

	it("hydrates the default first, then one row per additional model", () => {
		const rows = warpModelRowsFromConfig(stored);
		expect(values(rows)).toEqual([
			{ provider: "openai", model: "gpt-4o", apiKeyID: "key-1" },
			{ provider: "anthropic", model: "claude-sonnet-5", apiKeyID: "" },
		]);
		expect(new Set(rows.map((row) => row.id)).size).toBe(2);
	});

	it("always has a default row, blank on a config that never chose a model", () => {
		expect(values(warpModelRowsFromConfig({ provider: "", model: "" }))).toEqual([{ provider: "", model: "", apiKeyID: "" }]);
	});

	it("writes the first row as the default and the rest as additional_models", () => {
		expect(
			warpModelsPayload([
				{ provider: " openai ", model: " gpt-4o ", apiKeyID: "key-1" },
				{ provider: "anthropic", model: "claude-sonnet-5", apiKeyID: "" },
				{ provider: "openai", model: "gpt-4o-mini", apiKeyID: "key-2" },
			]),
		).toEqual({
			provider: "openai",
			model: "gpt-4o",
			api_key_id: "key-1",
			additional_models: [
				{ provider: "anthropic", model: "claude-sonnet-5" },
				{ provider: "openai", model: "gpt-4o-mini", api_key_id: "key-2" },
			],
		});
	});

	it("sends an empty list when only the default is left, so removed models are cleared", () => {
		expect(warpModelsPayload([{ provider: "openai", model: "gpt-4o", apiKeyID: "" }]).additional_models).toEqual([]);
	});

	it("reads a freshly loaded config as unchanged", () => {
		expect(warpModelRowsChanged(warpModelRowsFromConfig(stored), stored)).toBe(false);
		expect(warpModelRowsChanged(warpModelRowsFromConfig({ provider: "", model: "" }), { provider: "", model: "" })).toBe(false);
	});

	it("sees an added, removed, edited or reordered model as a change", () => {
		const rows = warpModelRowsFromConfig(stored);
		expect(warpModelRowsChanged([...rows, { id: "new", provider: "openai", model: "gpt-4o-mini", apiKeyID: "" }], stored)).toBe(true);
		expect(warpModelRowsChanged(rows.slice(0, 1), stored)).toBe(true);
		expect(warpModelRowsChanged([rows[0], { ...rows[1], apiKeyID: "key-9" }], stored)).toBe(true);
		// Making another model the default is a reorder.
		expect(warpModelRowsChanged([rows[1], rows[0]], stored)).toBe(true);
	});

	it("lets the default stay blank only while Warp is off", () => {
		const blank = [{ provider: "", model: "", apiKeyID: "" }];
		expect(validateWarpModelRows(blank, false)).toEqual([null]);
		expect(validateWarpModelRows(blank, true)).toEqual(["Choose a provider and model to enable Warp."]);
		expect(validateWarpModelRows([{ provider: "openai", model: "", apiKeyID: "" }], true)[0]).toContain("Choose a provider and model");
	});

	it("never accepts a half-filled additional model, enabled or not", () => {
		const rows = [
			{ provider: "openai", model: "gpt-4o", apiKeyID: "" },
			{ provider: "anthropic", model: "", apiKeyID: "" },
		];
		expect(validateWarpModelRows(rows, false)).toEqual([null, "Choose a provider and model, or remove this model."]);
		expect(validateWarpModelRows(rows, true)[1]).toContain("remove this model");
	});

	it("flags a repeated provider and model on the later row, whatever its key", () => {
		expect(
			validateWarpModelRows(
				[
					{ provider: "openai", model: "gpt-4o", apiKeyID: "key-1" },
					{ provider: "azure", model: "gpt-4o", apiKeyID: "" },
					{ provider: "openai", model: " gpt-4o ", apiKeyID: "key-2" },
				],
				true,
			),
		).toEqual([null, null, "This provider and model is already listed."]);
	});
});
