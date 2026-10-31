import { describe, expect, it } from "vitest";
import { hasProviderIcon, resolveWarpModel, warpModelForRequest, warpModelKey, warpModelLabel, warpModels } from "./warpComposer.utils";

describe("warpModelLabel", () => {
	it("leaves the model alone when the provider has an icon", () => {
		expect(hasProviderIcon("openai")).toBe(true);
		expect(warpModelLabel("openai", "gpt-4o")).toBe("gpt-4o");
	});

	it("names the provider when there is no icon for it", () => {
		expect(hasProviderIcon("my-internal-llm")).toBe(false);
		expect(warpModelLabel("my-internal-llm", "llama-3.1-70b")).toBe("my-internal-llm · llama-3.1-70b");
	});

	it("falls back to the provider alone when no model is set", () => {
		expect(warpModelLabel("my-internal-llm", undefined)).toBe("my-internal-llm");
	});

	it("renders nothing extra when no provider is configured", () => {
		expect(warpModelLabel(undefined, "gpt-4o")).toBe("gpt-4o");
		expect(warpModelLabel("", "gpt-4o")).toBe("gpt-4o");
	});
});

describe("warpModels", () => {
	it("lists the default first, then the additional models in order", () => {
		expect(
			warpModels({
				provider: "openai",
				model: "gpt-4o",
				api_key_id: "key-1",
				additional_models: [
					{ provider: "anthropic", model: "claude-sonnet-5" },
					{ provider: "openai", model: "gpt-4o-mini" },
				],
			}),
		).toEqual([
			{ provider: "openai", model: "gpt-4o", api_key_id: "key-1" },
			{ provider: "anthropic", model: "claude-sonnet-5" },
			{ provider: "openai", model: "gpt-4o-mini" },
		]);
	});

	it("is just the default for a config with no additional models", () => {
		expect(warpModels({ provider: "openai", model: "gpt-4o" })).toEqual([{ provider: "openai", model: "gpt-4o", api_key_id: undefined }]);
	});

	it("is empty before a config has loaded or been filled in", () => {
		expect(warpModels(undefined)).toEqual([]);
		expect(warpModels({ provider: "", model: "" })).toEqual([]);
	});
});

describe("resolveWarpModel", () => {
	const models = [
		{ provider: "openai", model: "gpt-4o" },
		{ provider: "anthropic", model: "claude-sonnet-5" },
	];

	it("uses the remembered model while it is still exposed", () => {
		expect(resolveWarpModel(models, warpModelKey(models[1]))).toBe(models[1]);
	});

	it("falls back to the default with no choice, or one the operator removed", () => {
		expect(resolveWarpModel(models, null)).toBe(models[0]);
		expect(resolveWarpModel(models, warpModelKey({ provider: "openai", model: "gpt-5" }))).toBe(models[0]);
	});

	it("matches on the pair, so the same model under another provider is a different choice", () => {
		expect(resolveWarpModel(models, warpModelKey({ provider: "anthropic", model: "gpt-4o" }))).toBe(models[0]);
	});

	it("has nothing to resolve when no model is configured", () => {
		expect(resolveWarpModel([], warpModelKey(models[0]))).toBeUndefined();
	});
});

describe("warpModelForRequest", () => {
	const models = [
		{ provider: "openai", model: "gpt-4o", api_key_id: "key-1" },
		{ provider: "anthropic", model: "claude-sonnet-5", api_key_id: "key-2" },
	];

	it("leaves the default unnamed so the server's current default applies", () => {
		expect(warpModelForRequest(models, models[0])).toBeUndefined();
		expect(warpModelForRequest(models, undefined)).toBeUndefined();
		expect(warpModelForRequest([], models[1])).toBeUndefined();
	});

	it("names a non-default model by its pair only, never its key", () => {
		expect(warpModelForRequest(models, models[1])).toEqual({ provider: "anthropic", model: "claude-sonnet-5" });
	});
});
