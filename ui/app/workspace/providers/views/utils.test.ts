import type { ModelProvider } from "@/lib/types/config";
import { describe, expect, it } from "vitest";
import { buildProviderUpdatePayload } from "./utils";

const provider = {
	name: "openai",
	provider_status: "active",
	injected_tools: { web_search: { mcp_client_name: "tavily", tool_name: "search" } },
} as ModelProvider;

describe("buildProviderUpdatePayload injected_tools", () => {
	it("echoes the saved block when another tab saves", () => {
		expect(buildProviderUpdatePayload(provider, { prompt_cache: { auto_inject: true } }).injected_tools).toEqual(provider.injected_tools);
	});

	it("sends an explicit null so the server clears the block", () => {
		expect(buildProviderUpdatePayload(provider, { injected_tools: null }).injected_tools).toBeNull();
	});

	it("replaces the block with the one supplied", () => {
		const next = { web_search: { mcp_client_name: "exa", tool_name: "web_search_exa" } };
		expect(buildProviderUpdatePayload(provider, { injected_tools: next }).injected_tools).toEqual(next);
	});
});