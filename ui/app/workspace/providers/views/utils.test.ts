import { describe, expect, it } from "vitest";

import type { ModelProvider } from "@/lib/types/config";

import { buildProviderUpdatePayload, resolvePassthroughExtraParams } from "./utils";

const provider = (name: string, passthroughExtraParams?: boolean): ModelProvider =>
	({
		name,
		provider_status: "active",
		passthrough_extra_params: passthroughExtraParams,
	}) as ModelProvider;

describe("resolvePassthroughExtraParams", () => {
	it("uses the provider default only when the API response is nil", () => {
		expect(resolvePassthroughExtraParams(provider("deepseek"))).toBe(true);
		expect(resolvePassthroughExtraParams(provider("vllm"))).toBe(true);
		expect(resolvePassthroughExtraParams(provider("sgl"))).toBe(true);
		expect(resolvePassthroughExtraParams(provider("openai"))).toBe(false);
	});

	it("keeps an explicit false instead of applying a provider default", () => {
		expect(resolvePassthroughExtraParams(provider("deepseek", false))).toBe(false);
	});
});

describe("buildProviderUpdatePayload", () => {
	it("preserves the field across unrelated full-PUT saves", () => {
		const payload = buildProviderUpdatePayload(provider("openai", true), {});

		expect(payload.passthrough_extra_params).toBe(true);
	});

	it("sends an explicit false from the Network form", () => {
		const payload = buildProviderUpdatePayload(provider("deepseek", true), {
			passthrough_extra_params: false,
		});

		expect(payload.passthrough_extra_params).toBe(false);
	});
});