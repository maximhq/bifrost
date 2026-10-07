import type { ModelProvider } from "@/lib/types/config";
import { describe, expect, test } from "vitest";
import { decisionProviderIconKey } from "./decisionModelProviders";

const CLEF_URL = "https://api.cloudflare.com/client/v4/accounts/acct/ai/run/@cf/cloudflare/clef";

// custom builds a custom provider on the given base format, optionally with a decisions URL override.
const custom = (name: string, base: string, decisionsURL?: string) =>
	({
		name,
		custom_provider_config: {
			base_provider_type: base,
			...(decisionsURL && { request_path_overrides: { decisions: decisionsURL } }),
		},
	}) as unknown as ModelProvider;

describe("decisionProviderIconKey", () => {
	test("picks the model family of a Typesafe-based custom provider", () => {
		expect(decisionProviderIconKey(custom("Laya", "typesafe"))).toBe("laya");
		expect(decisionProviderIconKey(custom("nimble-gpu", "typesafe"))).toBe("nimble");
		expect(decisionProviderIconKey(custom("anything", "typesafe", CLEF_URL))).toBe("clef");
	});

	test("keeps the standard icon when the name says nothing about the model", () => {
		expect(decisionProviderIconKey(custom("my-gpu", "typesafe"))).toBeUndefined();
		expect(decisionProviderIconKey(custom("laya-nimble", "typesafe"))).toBeUndefined();
	});

	test("leaves custom providers on other base formats alone, even with a matching name or URL", () => {
		expect(decisionProviderIconKey(custom("nimble-gpu", "openai"))).toBeUndefined();
		expect(decisionProviderIconKey(custom("Laya", "bedrock"))).toBeUndefined();
		expect(decisionProviderIconKey(custom("cf", "openai", CLEF_URL))).toBeUndefined();
	});

	test("ignores built-in providers", () => {
		expect(decisionProviderIconKey({ name: "typesafe" } as ModelProvider)).toBeUndefined();
		expect(decisionProviderIconKey({ name: "openrouter" } as ModelProvider)).toBeUndefined();
	});
});