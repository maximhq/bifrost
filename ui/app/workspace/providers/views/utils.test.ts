import type { ModelProvider } from "@/lib/types/config";
import { describe, expect, it } from "vitest";
import { buildProviderUpdatePayload } from "./utils";

const provider = {
	name: "openai",
	provider_status: "active",
	injected_tools: [{ name: "web_search", mcp_client_name: "tavily", tool_name: "search" }],
} as ModelProvider;

describe("buildProviderUpdatePayload injected_tools", () => {
	it("echoes the saved list when another tab saves", () => {
		expect(buildProviderUpdatePayload(provider, { prompt_cache: { auto_inject: true } }).injected_tools).toEqual(provider.injected_tools);
	});

	it("sends an explicit null so the server clears the list", () => {
		expect(buildProviderUpdatePayload(provider, { injected_tools: null }).injected_tools).toBeNull();
	});

	it("replaces the list with the one supplied", () => {
		const next = [
			{ name: "web_search", mcp_client_name: "exa", tool_name: "web_search_exa" },
			{ name: "read_page", mcp_client_name: "exa", tool_name: "crawl" },
		];
		expect(buildProviderUpdatePayload(provider, { injected_tools: next }).injected_tools).toEqual(next);
	});
});