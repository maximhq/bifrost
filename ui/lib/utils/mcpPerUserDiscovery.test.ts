import { beforeAll, describe, expect, test } from "vitest";
import i18n from "@/lib/i18n";
import type { MCPClient } from "@/lib/types/mcp";
import { perUserDiscoveryOn, supportsPerUserDiscovery, toolSubtitle } from "./mcpPerUserDiscovery";

beforeAll(async () => {
	await i18n.changeLanguage("en");
});

describe("mcpPerUserDiscovery", () => {
	test("supportsPerUserDiscovery allows only per-user auth types on a non-openapi server", () => {
		expect(supportsPerUserDiscovery("per_user_oauth", "http")).toBe(true);
		expect(supportsPerUserDiscovery("per_user_headers", "sse")).toBe(true);
		expect(supportsPerUserDiscovery("token_exchange", "http")).toBe(true);
		expect(supportsPerUserDiscovery("per_user_headers", "openapi")).toBe(false);
		expect(supportsPerUserDiscovery("oauth", "http")).toBe(false);
		expect(supportsPerUserDiscovery("headers", "http")).toBe(false);
		expect(supportsPerUserDiscovery(undefined, "http")).toBe(false);
	});

	test("perUserDiscoveryOn reads only an explicit true", () => {
		expect(perUserDiscoveryOn({ per_user_discovery: true })).toBe(true);
		expect(perUserDiscoveryOn({ per_user_discovery: false })).toBe(false);
		expect(perUserDiscoveryOn({})).toBe(false);
		expect(perUserDiscoveryOn(undefined)).toBe(false);
	});

	test("toolSubtitle counts callers instead of a description for a per-user discovery server", () => {
		const perUser = {
			config: { per_user_discovery: true },
			callers_by_tool: { search: 3, read: 1 },
		} as unknown as MCPClient;
		expect(toolSubtitle(perUser, { name: "search", description: "one caller's words" })).toBe("Discovered by 3 callers");
		expect(toolSubtitle(perUser, { name: "read" })).toBe("Discovered by 1 caller");
		expect(toolSubtitle(perUser, { name: "write" })).toBe("Not seen in any caller's discovery yet");

		const shared = { config: {} } as unknown as MCPClient;
		expect(toolSubtitle(shared, { name: "search", description: "Search the notes" })).toBe("Search the notes");
		expect(toolSubtitle(shared, { name: "search" })).toBeUndefined();
	});
});