import { describe, expect, it } from "vitest";
import { injectedWebSearchFormSchema } from "./schemas";

// Save only ever writes a web search tool; removing it is the Remove button's job. A
// half-filled form (server changed, its name or tool not yet resolved) must not be
// savable, or Save would send injected_tools: null and wipe the saved setting.
describe("injectedWebSearchFormSchema", () => {
	it("accepts a complete selection", () => {
		expect(injectedWebSearchFormSchema.safeParse({ mcp_client_id: "id-1", mcp_client_name: "tavily", tool_name: "search" }).success).toBe(
			true,
		);
	});

	it("rejects a server whose name has not resolved yet", () => {
		expect(injectedWebSearchFormSchema.safeParse({ mcp_client_id: "id-1", mcp_client_name: "", tool_name: "" }).success).toBe(false);
	});

	it("rejects a server with no tool picked", () => {
		expect(injectedWebSearchFormSchema.safeParse({ mcp_client_id: "id-1", mcp_client_name: "tavily", tool_name: "" }).success).toBe(false);
	});

	it("rejects an empty form, which would clear the setting", () => {
		expect(injectedWebSearchFormSchema.safeParse({ mcp_client_id: "", mcp_client_name: "", tool_name: "" }).success).toBe(false);
	});
});