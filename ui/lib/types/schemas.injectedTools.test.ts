import { describe, expect, it } from "vitest";
import { injectedToolsConfigSchema, injectedToolsFormSchema } from "./schemas";

const row = (name: string, client = "tavily", tool = "search") => ({
	name,
	mcp_client_id: `id-${client}`,
	mcp_client_name: client,
	tool_name: tool,
});

function issuePaths(tools: ReturnType<typeof row>[]) {
	const result = injectedToolsFormSchema.safeParse({ tools });
	return result.success ? [] : result.error.issues.map((issue) => issue.path.join("."));
}

// Save writes every row, so each must be complete: a half-filled row (server changed, its
// name or tool not yet resolved) would otherwise be written as an empty client or tool.
describe("injectedToolsFormSchema", () => {
	it("accepts complete rows and an empty table", () => {
		expect(issuePaths([row("web_search"), row("read_page", "fetcher", "fetch")])).toEqual([]);
		expect(issuePaths([])).toEqual([]);
	});

	it("rejects a row whose server name has not resolved yet", () => {
		expect(issuePaths([{ ...row("web_search"), mcp_client_name: "" }])).toContain("tools.0.mcp_client_name");
	});

	it("rejects a row with no server or tool picked", () => {
		expect(issuePaths([{ ...row("web_search"), mcp_client_id: "" }])).toContain("tools.0.mcp_client_id");
		expect(issuePaths([{ ...row("web_search"), tool_name: "" }])).toContain("tools.0.tool_name");
	});

	it("rejects a name a provider would refuse", () => {
		expect(issuePaths([row("")])).toContain("tools.0.name");
		expect(issuePaths([row("web search")])).toContain("tools.0.name");
		expect(issuePaths([row("a".repeat(65))])).toContain("tools.0.name");
		expect(issuePaths([row("Read-Page_2")])).toEqual([]);
	});

	it("flags a repeated name on the later row", () => {
		expect(issuePaths([row("web_search"), row("web_search", "fetcher", "fetch")])).toEqual(["tools.1.name"]);
	});

	it("flags the same MCP tool injected twice on the later row", () => {
		expect(issuePaths([row("web_search"), row("lookup")])).toEqual(["tools.1.tool_name"]);
	});
});

describe("injectedToolsConfigSchema", () => {
	it("accepts the saved list shape and rejects the old object shape", () => {
		expect(injectedToolsConfigSchema.safeParse([{ name: "web_search", mcp_client_name: "tavily", tool_name: "search" }]).success).toBe(
			true,
		);
		expect(injectedToolsConfigSchema.safeParse({ web_search: { mcp_client_name: "tavily", tool_name: "search" } }).success).toBe(false);
	});
});