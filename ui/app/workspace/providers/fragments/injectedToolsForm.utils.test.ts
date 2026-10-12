import { describe, expect, it } from "vitest";
import {
	defaultToolName,
	emptyToolRow,
	findClientByExactName,
	hydratedValues,
	lookupSavedClient,
	nameAfterServerChange,
	nextLookupOffset,
	nextSavedClientState,
	pickersDisabled,
	prefilledName,
	replacesNativeWebSearch,
	savedFormValues,
	startedSavedClientState,
	toInjectedTools,
	toolMenuState,
	withResolvedClientId,
} from "./injectedToolsForm.utils";

const saved = { tools: [{ name: "web_search", mcp_client_id: "id-1", mcp_client_name: "tavily", tool_name: "search" }] };

describe("hydratedValues", () => {
	it("hydrates an untouched form once the saved client id resolves", () => {
		expect(hydratedValues(false, saved)).toEqual(saved);
	});

	it("never overwrites a selection the user already started", () => {
		expect(hydratedValues(true, saved)).toBeNull();
	});
});

describe("toolMenuState", () => {
	it("tells a lookup failure apart from a server with no tools", () => {
		expect(toolMenuState("", { isFetching: false, isError: false }, 0)).toBe("no-server");
		expect(toolMenuState("id-1", { isFetching: true, isError: false }, 0)).toBe("loading");
		expect(toolMenuState("id-1", { isFetching: false, isError: true }, 0)).toBe("error");
		expect(toolMenuState("id-1", { isFetching: false, isError: false }, 0)).toBe("empty");
		expect(toolMenuState("id-1", { isFetching: false, isError: false }, 3)).toBe("ready");
	});
});

describe("saved client lookup", () => {
	const client = (name: string) => ({ config: { name, client_id: `id-${name}` } });

	it("matches the exact name, not a name that merely contains it", () => {
		expect(findClientByExactName([client("tavily_eu"), client("tavily")], "tavily")?.config.client_id).toBe("id-tavily");
		expect(findClientByExactName([client("tavily_eu")], "tavily")).toBeUndefined();
	});

	it("pages on until the results run out", () => {
		expect(nextLookupOffset(0, 50, 120)).toBe(50);
		expect(nextLookupOffset(50, 50, 120)).toBe(100);
		expect(nextLookupOffset(100, 50, 120)).toBeNull();
		expect(nextLookupOffset(0, 50, undefined)).toBeNull();
	});
});
describe("savedFormValues", () => {
	it("loads each saved row with an unresolved client id", () => {
		expect(
			savedFormValues([
				{ name: "web_search", mcp_client_name: "tavily", tool_name: "search" },
				{ name: "read_page", mcp_client_name: "fetcher", tool_name: "fetch" },
			]),
		).toEqual({
			tools: [
				{ name: "web_search", mcp_client_id: "", mcp_client_name: "tavily", tool_name: "search" },
				{ name: "read_page", mcp_client_id: "", mcp_client_name: "fetcher", tool_name: "fetch" },
			],
		});
	});

	it("loads nothing for a provider without injected tools", () => {
		expect(savedFormValues(undefined)).toEqual({ tools: [] });
		expect(savedFormValues(null)).toEqual({ tools: [] });
	});
});

describe("toInjectedTools", () => {
	it("drops the picker-only client id", () => {
		expect(toInjectedTools(saved)).toEqual([{ name: "web_search", mcp_client_name: "tavily", tool_name: "search" }]);
	});

	it("writes null for an empty table, which clears the provider's tools", () => {
		expect(toInjectedTools({ tools: [] })).toBeNull();
	});

	it("appends empty rows", () => {
		expect(emptyToolRow()).toEqual({ name: "", mcp_client_id: "", mcp_client_name: "", tool_name: "" });
	});
});

describe("withResolvedClientId", () => {
	const unresolved = savedFormValues([
		{ name: "web_search", mcp_client_name: "tavily", tool_name: "search" },
		{ name: "read_page", mcp_client_name: "fetcher", tool_name: "fetch" },
	]);

	it("fills only the row the id was resolved for", () => {
		const next = withResolvedClientId(unresolved, 1, "fetcher", "id-f");
		expect(next?.tools.map((tool) => tool.mcp_client_id)).toEqual(["", "id-f"]);
	});

	it("ignores an id for a row that was edited, removed or already resolved", () => {
		expect(withResolvedClientId(unresolved, 1, "tavily", "id-t")).toBeNull();
		expect(withResolvedClientId(unresolved, 5, "fetcher", "id-f")).toBeNull();
		expect(withResolvedClientId(saved, 0, "tavily", "id-2")).toBeNull();
	});
});

describe("tool names", () => {
	it("suggests the MCP tool's own name, made safe for a provider", () => {
		expect(defaultToolName("search")).toBe("search");
		expect(defaultToolName("web.search v2")).toBe("web_search_v2");
		expect(defaultToolName("x".repeat(80))).toHaveLength(64);
	});

	it("follows the tool until the user types a name", () => {
		expect(prefilledName("", "", "search")).toBe("search");
		expect(prefilledName("search", "search", "crawl")).toBe("crawl");
		expect(prefilledName("web_search", "search", "crawl")).toBe("web_search");
	});

	it("drops a suggested name when the server changes, so the next tool suggests its own", () => {
		// Pick search (suggested "search"), switch server, pick crawl: the row must end up "crawl".
		const afterSwitch = nameAfterServerChange("search", "search");
		expect(afterSwitch).toBe("");
		expect(prefilledName(afterSwitch, "", "crawl")).toBe("crawl");
	});

	it("keeps a typed name when the server changes", () => {
		expect(nameAfterServerChange("web_search", "search")).toBe("web_search");
		expect(nameAfterServerChange("lookup", "")).toBe("lookup");
		expect(nameAfterServerChange("", "search")).toBe("");
	});

	it("marks only web_search as replacing native web search", () => {
		expect(replacesNativeWebSearch("web_search")).toBe(true);
		expect(replacesNativeWebSearch("search")).toBe(false);
		expect(replacesNativeWebSearch("Web_Search")).toBe(false);
	});
});
describe("lookupSavedClient", () => {
	const client = (name: string, id: string) => ({ config: { name, client_id: id } });

	it("finds the exact name on a later page", async () => {
		const pages = [
			{ clients: [client("tavily-staging", "id-0")], total_count: 2 },
			{ clients: [client("tavily", "id-1")], total_count: 2 },
		];
		const result = await lookupSavedClient(
			(offset) => Promise.resolve(pages[offset]),
			"tavily",
			1,
			() => false,
		);
		expect(result).toEqual({ status: "found", id: "id-1" });
	});

	it("reports a failed request as an error, not as a missing client, so the form can offer Retry", async () => {
		const result = await lookupSavedClient(
			() => Promise.reject(new Error("network")),
			"tavily",
			50,
			() => false,
		);
		expect(result).toEqual({ status: "error" });
	});

	it("reports a client that is not there as missing", async () => {
		const result = await lookupSavedClient(
			() => Promise.resolve({ clients: [], total_count: 0 }),
			"tavily",
			50,
			() => false,
		);
		expect(result).toEqual({ status: "missing" });
	});
});

describe("pickersDisabled", () => {
	it("locks the pickers while the provider update is in flight", () => {
		expect(pickersDisabled(true, true)).toBe(true);
	});

	it("leaves them editable otherwise, and locked without update access", () => {
		expect(pickersDisabled(true, false)).toBe(false);
		expect(pickersDisabled(false, false)).toBe(true);
	});
});
describe("nextSavedClientState", () => {
	it("records a found client", () => {
		expect(nextSavedClientState("tavily", { status: "found", id: "id-1" })).toEqual({ name: "tavily", id: "id-1", status: "found" });
	});

	it("replaces an earlier failure when Retry finds the saved server is gone", () => {
		expect(nextSavedClientState("tavily", { status: "missing" })).toEqual({ name: "tavily", id: "", status: "missing" });
	});

	it("leaves the state alone for a cancelled lookup", () => {
		expect(nextSavedClientState("tavily", { status: "cancelled" })).toBeNull();
	});
});
describe("startedSavedClientState", () => {
	it("shows the saved server as loading, and drops an earlier error as soon as Retry starts", () => {
		expect(startedSavedClientState("tavily", { name: "tavily", id: "", status: "error" })).toEqual({
			name: "tavily",
			id: "",
			status: "loading",
		});
	});

	it("keeps an id already resolved for the same name while it reloads", () => {
		expect(startedSavedClientState("tavily", { name: "tavily", id: "id-1", status: "found" })).toEqual({
			name: "tavily",
			id: "id-1",
			status: "loading",
		});
	});

	it("never carries another server's id into the new lookup", () => {
		expect(startedSavedClientState("exa", { name: "tavily", id: "id-1", status: "found" })).toEqual({
			name: "exa",
			id: "",
			status: "loading",
		});
	});
});