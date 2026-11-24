import { beforeAll, describe, expect, test } from "vitest";
import i18n from "@/lib/i18n";
import type { MCPClient } from "@/lib/types/mcp";
import type { MCPSessionRow } from "@/lib/types/mcpSessions";
import { perUserDiscoveryOn, sessionDiscoveryCell, supportsPerUserDiscovery, toolSubtitle } from "./mcpPerUserDiscovery";

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

	describe("sessionDiscoveryCell", () => {
		const fourMinutesAgo = () => new Date(Date.now() - 4 * 60_000).toISOString();
		const row = (over: Partial<MCPSessionRow>): MCPSessionRow =>
			({
				kind: "token",
				auth_mode: "user",
				status: "active",
				mcp_client: { client_id: "c1", name: "notes", per_user_discovery: true },
				...over,
			}) as MCPSessionRow;
		const discovery = (tools: number, prompts: number, resources: number) => ({
			discovered_at: fourMinutesAgo(),
			tool_count: tools,
			prompt_count: prompts,
			resource_count: resources,
		});

		test("a server without per-caller discovery shares one discovery", () => {
			expect(sessionDiscoveryCell(row({ mcp_client: { client_id: "c1", name: "notes" } }))).toEqual({ text: "Shared", muted: true });
			expect(sessionDiscoveryCell(row({ mcp_client: null, kind: "flow" }))).toEqual({ text: "Shared", muted: true });
		});

		test("a held discovery lists what it holds and when it was made", () => {
			expect(sessionDiscoveryCell(row({ discovery: discovery(3, 1, 2) }))).toEqual({
				text: "3 tools · 1 prompt · 2 resources",
				sub: "discovered 4 min ago",
				muted: false,
			});
			expect(sessionDiscoveryCell(row({ kind: "header", auth_mode: "vk", discovery: discovery(1, 0, 1) })).text).toBe(
				"1 tool · 1 resource",
			);
			expect(sessionDiscoveryCell(row({ discovery: discovery(0, 2, 0) })).text).toBe("No tools · 2 prompts");
			expect(sessionDiscoveryCell(row({ discovery: discovery(0, 0, 0) })).text).toBe("No tools");
		});

		test("a held discovery shows whatever the credential's status", () => {
			expect(sessionDiscoveryCell(row({ status: "orphaned", discovery: discovery(2, 0, 0) })).text).toBe("2 tools");
		});

		test("an active credential without one gets it on the caller's next connect", () => {
			expect(sessionDiscoveryCell(row({}))).toEqual({ text: "Not yet", sub: "on the next connect", muted: true });
			expect(sessionDiscoveryCell(row({ kind: "header", auth_mode: "vk" }))).toEqual({
				text: "Not yet",
				sub: "on the next connect",
				muted: true,
			});
		});

		test("a pending sign-in has no discovery yet", () => {
			expect(sessionDiscoveryCell(row({ kind: "flow", status: "pending" }))).toEqual({ text: "Not yet", muted: true });
		});

		test("a credential that needs action, or a session-bound one, holds none", () => {
			for (const status of ["needs_reauth", "needs_update", "orphaned"] as const) {
				expect(sessionDiscoveryCell(row({ status }))).toEqual({ text: "-", muted: true });
			}
			expect(sessionDiscoveryCell(row({ auth_mode: "session" }))).toEqual({ text: "-", muted: true });
			expect(sessionDiscoveryCell(row({ auth_mode: "session", kind: "flow", status: "pending" }))).toEqual({ text: "-", muted: true });
		});
	});
});