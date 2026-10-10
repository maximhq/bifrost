import type { InjectedWebSearchFormSchema } from "@/lib/types/schemas";

export type ToolMenuState = "no-server" | "loading" | "error" | "empty" | "ready";

// hydratedValues returns the saved selection to load into the form once the saved
// client's id resolves, or null when the user has already started editing: the lookup
// can land after the first change, and must never undo it.
export function hydratedValues(isDirty: boolean, saved: InjectedWebSearchFormSchema): InjectedWebSearchFormSchema | null {
	return isDirty ? null : saved;
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
// savedFormValues is the form for the saved setting. The client id is resolved by name, so
// it is kept only while it was resolved for the saved name: a provider switch or a
// tool-only save keeps it, a different client never inherits it.
export function savedFormValues(savedName: string, savedTool: string, resolved: { name: string; id: string }): InjectedWebSearchFormSchema {
	return {
		mcp_client_id: resolved.name === savedName ? resolved.id : "",
		mcp_client_name: savedName,
		tool_name: savedTool,
	};
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