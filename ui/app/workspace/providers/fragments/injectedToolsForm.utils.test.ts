import { describe, expect, it } from "vitest";
import {
	findClientByExactName,
	hydratedValues,
	lookupSavedClient,
	nextLookupOffset,
	nextSavedClientState,
	pickersDisabled,
	savedFormValues,
	startedSavedClientState,
	toolMenuState,
} from "./injectedToolsForm.utils";

const saved = { mcp_client_id: "id-1", mcp_client_name: "tavily", tool_name: "search" };

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
	it("keeps the resolved id when the saved client is unchanged (provider switch, tool-only save)", () => {
		expect(savedFormValues("tavily", "crawl", { name: "tavily", id: "id-1" })).toEqual({
			mcp_client_id: "id-1",
			mcp_client_name: "tavily",
			tool_name: "crawl",
		});
	});

	it("never carries an id resolved for a different client name", () => {
		expect(savedFormValues("exa", "search", { name: "tavily", id: "id-1" }).mcp_client_id).toBe("");
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