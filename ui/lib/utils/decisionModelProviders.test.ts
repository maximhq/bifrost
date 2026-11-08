import type { ModelProvider } from "@/lib/types/config";
import { describe, expect, test } from "vitest";
import {
	clefModelFromProvider,
	decisionProviderIconKey,
	defaultDecisionModel,
	isDecisionProvider,
	isOpenAIDecisionProvider,
	listsDecisionModels,
	selfHostedModelGroups,
} from "./decisionModelProviders";

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

describe("decision model provider helpers", () => {
	const provider = (overrides: Partial<ModelProvider>) => ({ provider_status: "active", ...overrides }) as ModelProvider;
	const custom = (name: string, decisionsURL?: string) =>
		provider({
			name,
			custom_provider_config: {
				base_provider_type: "typesafe",
				...(decisionsURL && { request_path_overrides: { decisions: decisionsURL } }),
			},
		} as Partial<ModelProvider>);
	const typesafe = provider({ name: "typesafe" } as Partial<ModelProvider>);
	const openrouter = provider({ name: "openrouter" } as Partial<ModelProvider>);
	const openai = provider({ name: "openai" } as Partial<ModelProvider>);
	const cloudflare = provider({ name: "cloudflare" } as Partial<ModelProvider>);
	const perplexity = provider({ name: "perplexity" } as Partial<ModelProvider>);
	const ollama = provider({ name: "ollama" } as Partial<ModelProvider>);
	const azure = provider({ name: "azure" } as Partial<ModelProvider>);
	const databricks = provider({ name: "databricks" } as Partial<ModelProvider>);
	const vllm = provider({ name: "vllm" } as Partial<ModelProvider>);
	const sgl = provider({ name: "sgl" } as Partial<ModelProvider>);
	const laya = custom("Laya");
	const clef = custom("cloudflare clev", "https://api.cloudflare.com/client/v4/accounts/acct/ai/run/@cf/cloudflare/clef");
	const clefFlash = custom("clef-flash", "https://api.cloudflare.com/client/v4/accounts/acct/ai/run/@cf/cloudflare/clef-flash/");

	test("offers only providers that answer decisions natively", () => {
		for (const candidate of [typesafe, openrouter, openai, cloudflare, perplexity, ollama, azure, databricks, vllm, sgl, laya, clef])
			expect(isDecisionProvider(candidate)).toBe(true);
		expect(isDecisionProvider(provider({ name: "anthropic" } as Partial<ModelProvider>))).toBe(false);
		expect(
			isDecisionProvider(
				provider({ name: "my-anthropic", custom_provider_config: { base_provider_type: "anthropic" } } as Partial<ModelProvider>),
			),
		).toBe(false);
	});

	test("treats a custom OpenAI-based provider like OpenAI", () => {
		const customOpenAI = (name: string) =>
			provider({ name, custom_provider_config: { base_provider_type: "openai" } } as Partial<ModelProvider>);
		expect(isDecisionProvider(customOpenAI("my-openai"))).toBe(true);
		expect(isOpenAIDecisionProvider(customOpenAI("my-openai"))).toBe(true);
		expect(isOpenAIDecisionProvider(openai)).toBe(true);
		expect(isOpenAIDecisionProvider(typesafe)).toBe(false);
		// Only its listing says which decision models it serves, whatever it is named.
		expect(defaultDecisionModel(customOpenAI("my-openai"))).toBe("");
		expect(defaultDecisionModel(customOpenAI("laya-gateway"))).toBe("");
	});

	test("reads the Clef model from the provider's Cloudflare URL", () => {
		expect(clefModelFromProvider(clef)).toBe("clef");
		expect(clefModelFromProvider(clefFlash)).toBe("clef-flash");
		expect(clefModelFromProvider(laya)).toBeUndefined();
		expect(clefModelFromProvider(undefined)).toBeUndefined();
	});

	test("offers only the checkpoints of the model a provider is named after", () => {
		expect(selfHostedModelGroups("Laya").map((group) => group.label)).toEqual(["Laya"]);
		expect(selfHostedModelGroups("nimble-gpu").map((group) => group.label)).toEqual(["Nimble"]);
		expect(selfHostedModelGroups("ollama-clef").map((group) => group.label)).toEqual(["Clef"]);
		expect(selfHostedModelGroups("decisions-eu").map((group) => group.label)).toEqual(["Laya", "Nimble", "Clef"]);
	});

	test("starts each provider on its known model", () => {
		expect(defaultDecisionModel(typesafe)).toBe("jev-latest");
		expect(defaultDecisionModel(openrouter)).toBe("~typesafe/jev-latest");
		expect(defaultDecisionModel(openai)).toBe("gpt-6-luna");
		expect(defaultDecisionModel(cloudflare)).toBe("@cf/cloudflare/clef-flash");
		expect(defaultDecisionModel(perplexity)).toBe("pplx-decider-latest");
		// Only Ollama's listing says which System One models are pulled locally.
		expect(defaultDecisionModel(ollama)).toBe("");
		// Only a vLLM or SGLang server's listing says what it loaded.
		expect(defaultDecisionModel(vllm)).toBe("");
		expect(defaultDecisionModel(sgl)).toBe("");
		// The model name a key alias maps to the user's deployment.
		expect(defaultDecisionModel(azure)).toBe("Microsoft-Decision-1");
		// The ai_decide AI Function; the picker also lists the System One models.
		expect(defaultDecisionModel(databricks)).toBe("ai_decide");
		expect(defaultDecisionModel(clefFlash)).toBe("clef-flash");
		expect(defaultDecisionModel(laya)).toBe("english");
		expect(defaultDecisionModel(custom("nimble-gpu"))).toBe("nimble-latest");
		expect(defaultDecisionModel(custom("ollama-clef"))).toBe("clef-flash");
	});

	test("lists only decision models for providers whose catalog mixes in chat models", () => {
		for (const candidate of [openai, openrouter, cloudflare, perplexity, ollama, azure, databricks, vllm, sgl])
			expect(listsDecisionModels(candidate, candidate.name)).toBe(true);
		expect(
			listsDecisionModels(
				provider({ name: "my-openai", custom_provider_config: { base_provider_type: "openai" } } as Partial<ModelProvider>),
				"my-openai",
			),
		).toBe(true);
		expect(listsDecisionModels(undefined, "openai")).toBe(true);
		for (const candidate of [typesafe, laya, clef]) expect(listsDecisionModels(candidate, candidate.name)).toBe(false);
	});

	test("starts a self-hosted provider whose name says nothing on none", () => {
		expect(defaultDecisionModel(custom("decisions-eu"))).toBe("");
		expect(defaultDecisionModel(undefined)).toBe("");
	});
});