import type { InjectedTool } from "@/lib/types/config";
import type { InjectedToolRowSchema, InjectedToolsFormSchema } from "@/lib/types/schemas";

export type ToolMenuState = "no-server" | "loading" | "error" | "empty" | "ready";

// hydratedValues returns the saved rows to load into the form once a saved client's id
// resolves, or null when the user has already started editing: the lookup can land after
// the first change, and must never undo it.
export function hydratedValues<T>(isDirty: boolean, saved: T): T | null {
	return isDirty ? null : saved;
}

// WEB_SEARCH_TOOL_NAME is the injected tool name that also replaces native web search.
export const WEB_SEARCH_TOOL_NAME = "web_search";

// replacesNativeWebSearch reports whether a row named name swaps out the native web
// search tools and web_search_options a client sends.
export function replacesNativeWebSearch(name: string): boolean {
	return name === WEB_SEARCH_TOOL_NAME;
}

// defaultToolName is the model-facing name suggested for an MCP tool: its own name with
// anything a provider rejects in a function name turned into _, cut to 64 characters.
export function defaultToolName(toolName: string): string {
	return toolName.replace(/[^a-zA-Z0-9_-]/g, "_").slice(0, 64);
}

// prefilledName is the row name after the tool changes from previousTool to nextTool. A
// name the user typed is kept; an empty one, or one still equal to the previous
// suggestion, follows the new tool.
export function prefilledName(currentName: string, previousTool: string, nextTool: string): string {
	if (currentName === "" || currentName === defaultToolName(previousTool)) return defaultToolName(nextTool);
	return currentName;
}

// toolMenuState tells a failed tool lookup apart from a server that exposes no tools.
export function toolMenuState(clientId: string, query: { isFetching: boolean; isError: boolean }, toolCount: number): ToolMenuState {
	if (clientId === "") return "no-server";
	if (query.isFetching) return "loading";
	if (query.isError) return "error";
	return toolCount === 0 ? "empty" : "ready";
}

// findClientByExactName picks the client whose name is exactly the saved one. The list
// search is a substring match, so other clients can come back alongside it.
export function findClientByExactName<T extends { config: { name: string } }>(clients: T[] | undefined, name: string): T | undefined {
	return clients?.find((client) => client.config.name === name);
}

// nextLookupOffset is the offset of the next search page, or null when the results have
// run out.
export function nextLookupOffset(offset: number, pageSize: number, total: number | undefined): number | null {
	if (total === undefined) return null;
	const next = offset + pageSize;
	return next < total ? next : null;
}
// nameAfterServerChange is the row name once its MCP server changes and its tool is
// cleared. A name still equal to the old tool's suggestion is cleared too, so the next
// tool picked suggests its own; a name the user typed is kept.
export function nameAfterServerChange(currentName: string, previousTool: string): string {
	return previousTool !== "" && currentName === defaultToolName(previousTool) ? "" : currentName;
}

// savedFormValues is the form for the saved list. Client ids start empty: the provider
// stores clients by name, and each row resolves its id by name once it mounts.
export function savedFormValues(saved: InjectedTool[] | null | undefined): InjectedToolsFormSchema {
	return {
		tools: (saved ?? []).map((tool) => ({
			name: tool.name,
			mcp_client_id: "",
			mcp_client_name: tool.mcp_client_name,
			tool_name: tool.tool_name,
		})),
	};
}

// emptyToolRow is the row "Add tool" appends.
export function emptyToolRow(): InjectedToolRowSchema {
	return { name: "", mcp_client_id: "", mcp_client_name: "", tool_name: "" };
}

// toInjectedTools is the list Save writes: the picker-only client id is dropped, and an
// empty table is null, which clears the provider's injected tools.
export function toInjectedTools(data: InjectedToolsFormSchema): InjectedTool[] | null {
	if (data.tools.length === 0) return null;
	return data.tools.map((tool) => ({ name: tool.name, mcp_client_name: tool.mcp_client_name, tool_name: tool.tool_name }));
}

// withResolvedClientId is values with row index's client id set, or null when that row no
// longer names the client the id was resolved for (it was edited or removed meanwhile).
export function withResolvedClientId(
	values: InjectedToolsFormSchema,
	index: number,
	clientName: string,
	id: string,
): InjectedToolsFormSchema | null {
	const row = values.tools[index];
	if (!row || row.mcp_client_name !== clientName || row.mcp_client_id !== "") return null;
	return { tools: values.tools.map((tool, i) => (i === index ? { ...tool, mcp_client_id: id } : tool)) };
}
export type SavedClientLookup = { status: "found"; id: string } | { status: "missing" } | { status: "error" } | { status: "cancelled" };

// lookupSavedClient pages through the client search until the exact saved name turns up.
// A failed request is an error, not a missing client: the form shows it and offers Retry
// instead of leaving the server picker silently empty.
export async function lookupSavedClient<T extends { config: { name: string; client_id: string } }>(
	fetchPage: (offset: number) => Promise<{ clients?: T[]; total_count?: number }>,
	name: string,
	pageSize: number,
	isCancelled: () => boolean,
): Promise<SavedClientLookup> {
	let offset: number | null = 0;
	while (offset !== null) {
		if (isCancelled()) return { status: "cancelled" };
		let page: { clients?: T[]; total_count?: number };
		try {
			page = await fetchPage(offset);
		} catch {
			return { status: "error" };
		}
		const match = findClientByExactName(page.clients, name);
		if (match) return { status: "found", id: match.config.client_id };
		offset = nextLookupOffset(offset, pageSize, page.total_count);
	}
	return { status: "missing" };
}

// pickersDisabled locks the server and tool pickers without update access and while a
// save is in flight, so the selection cannot change under the request being saved.
export function pickersDisabled(hasUpdateAccess: boolean, isUpdating: boolean): boolean {
	return !hasUpdateAccess || isUpdating;
}
export type SavedClientState = { name: string; id: string; status: "idle" | "loading" | "found" | "missing" | "error" };

// nextSavedClientState is the saved-client state after a lookup for name finishes, or
// null when nothing changes. Every finished lookup replaces the previous outcome, so a
// Retry that finds the server gone reports that instead of the earlier failure.
export function nextSavedClientState(name: string, result: SavedClientLookup): SavedClientState | null {
	switch (result.status) {
		case "found":
			return { name, id: result.id, status: "found" };
		case "missing":
			return { name, id: "", status: "missing" };
		case "error":
			return { name, id: "", status: "error" };
		default:
			return null;
	}
}
// startedSavedClientState is the saved-client state while a lookup for name runs. An
// earlier outcome (such as an error that Retry is answering) is replaced at once, and an
// id is kept only when it was resolved for the same name.
export function startedSavedClientState(name: string, previous: SavedClientState): SavedClientState {
	return { name, id: previous.name === name ? previous.id : "", status: "loading" };
}