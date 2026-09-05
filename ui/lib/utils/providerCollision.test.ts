import { describe, expect, it } from "vitest";

import { ModelProvider } from "@/lib/types/config";
import { findCustomProviderCollisions, findFirstPartyMatch, normalizeProviderName } from "./providerCollision";

const custom = (name: string): ModelProvider =>
	({ name, provider_status: "active", custom_provider_config: { base_provider_type: "openai" } }) as ModelProvider;

describe("providerCollision", () => {
	it("normalizes names to lowercase alphanumerics", () => {
		expect(normalizeProviderName("GitHub Copilot")).toBe("githubcopilot");
		expect(normalizeProviderName("  Databricks_1 ")).toBe("databricks1");
	});

	it("matches known provider keys case-insensitively", () => {
		expect(findFirstPartyMatch("databricks")).toBe("databricks");
		expect(findFirstPartyMatch("DataBricks")).toBe("databricks");
		expect(findFirstPartyMatch("github-copilot")).toBe("github-copilot");
	});

	it("matches known provider display labels", () => {
		expect(findFirstPartyMatch("GitHub Copilot")).toBe("github-copilot");
		expect(findFirstPartyMatch("AWS Bedrock")).toBe("bedrock");
		expect(findFirstPartyMatch("Vertex AI")).toBe("vertex");
	});

	it("does not match names that merely contain a known provider", () => {
		expect(findFirstPartyMatch("databricks-prod")).toBeUndefined();
		expect(findFirstPartyMatch("my-openai")).toBeUndefined();
		expect(findFirstPartyMatch("")).toBeUndefined();
	});

	it("only reports custom providers that collide with a first-party provider", () => {
		const providers: ModelProvider[] = [
			custom("Databricks"),
			custom("GitHub Copilot"),
			custom("databricks-prod"),
			{ name: "openai", provider_status: "active" } as ModelProvider,
		];
		expect(findCustomProviderCollisions(providers)).toEqual([
			{ customName: "Databricks", knownProvider: "databricks" },
			{ customName: "GitHub Copilot", knownProvider: "github-copilot" },
		]);
	});
});
