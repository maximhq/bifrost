import type { VirtualMCP } from "@/lib/types/virtualMcps";

/**
 * Backend pagination clamp for Virtual MCP list requests (transports/bifrost-http/handlers/utils.go).
 */
export const VIRTUAL_MCPS_PAGE_SIZE = 100;

/**
 * Upper safety limit for consecutive pagination requests to prevent runaways.
 */
export const MAX_VIRTUAL_MCPS_PAGES = 20;

export interface MemberVirtualMCP {
	vmcp: VirtualMCP;
	toolNames: string[];
}

/**
 * Derives which Virtual MCPs bundle tools from the given MCP server client ID.
 *
 * @param virtualMcps Complete list of Virtual MCP definitions.
 * @param clientID The UUID of the MCP client server being inspected.
 * @returns An array of matching Virtual MCPs alongside their exposed tool names.
 */
export function deriveMemberVirtualMCPs(virtualMcps: VirtualMCP[] | undefined, clientID: string): MemberVirtualMCP[] {
	if (!virtualMcps || !clientID) {
		return [];
	}

	const out: MemberVirtualMCP[] = [];
	for (const vmcp of virtualMcps) {
		const spec = vmcp.tools?.find((t) => t.mcp_client_id === clientID);
		if (spec) {
			out.push({ vmcp, toolNames: spec.tool_names ?? [] });
		}
	}
	return out;
}

/**
 * Merges incoming page items into existing items while deduplicating by ID.
 * Deduplication guards against shifts in pagination windows caused by concurrent additions/deletions.
 *
 * @param existing Previously accumulated Virtual MCPs.
 * @param incoming Newly fetched page of Virtual MCPs.
 * @returns Deduplicated array of combined Virtual MCPs.
 */
export function mergeVirtualMCPPages(existing: VirtualMCP[], incoming: VirtualMCP[]): VirtualMCP[] {
	if (!incoming || incoming.length === 0) {
		return existing;
	}
	if (!existing || existing.length === 0) {
		return [...incoming];
	}

	const seen = new Set(existing.map((item) => item.id));
	const result = [...existing];

	for (const item of incoming) {
		if (!seen.has(item.id)) {
			seen.add(item.id);
			result.push(item);
		}
	}

	return result;
}

/**
 * Determines the next offset for pagination, or returns null if all items have been retrieved.
 *
 * @param currentCount Number of items fetched so far.
 * @param totalCount Total count reported by the server.
 * @returns The next offset number, or null if complete.
 */
export function getNextVirtualMCPPageOffset(
	currentCount: number,
	totalCount: number,
): number | null {
	if (totalCount <= 0 || currentCount >= totalCount) {
		return null;
	}
	return currentCount;
}
