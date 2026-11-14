// Runtime registry for a kind of access grant a downstream build adds to an
// MCP server's view sheet, beyond the virtual keys and Virtual MCPs every
// build shows. The base build registers none, so the sheet shows only those
// two. A downstream build registers one from its registrations module, which
// the view sheet imports for its side effect.

import type { ComponentType } from "react";

export interface MCPClientAccessGrantsSectionProps {
	mcpClientId: string;
	/** Whether the Access tab is showing, so the section can defer work until it is. */
	active: boolean;
}

/**
 * The wording for a kind of grant, as whole translated strings: the sheet
 * places them as they are and never builds a sentence around them.
 */
export interface MCPClientAccessGrantLabels {
	count: (n: number) => string; // The Access figure's count, e.g. "2 grants"
	loading: string; // The Access figure's sub-line while the count loads
	noOverrides: string; // The sub-line when Allow by Default is on and nothing overrides it
	restrictedExplanation: string; // The Access tab's sentence when Allow by Default is off
}

export interface MCPClientAccessGrants {
	/**
	 * Counts the grants reaching the server, or undefined while they load.
	 * Called as a hook on every render of the view sheet.
	 */
	useCount: (mcpClientId: string) => number | undefined;
	/** Returns the grant's wording, translated when called so it follows the UI language. */
	labels: () => MCPClientAccessGrantLabels;
	/** Lists the grants on the Access tab, under Virtual Key Assignments. */
	Section: ComponentType<MCPClientAccessGrantsSectionProps>;
}

let accessGrants: MCPClientAccessGrants | undefined;

/**
 * Registers (or replaces) the extra access grants. Intended to be called at
 * module load, once, before the first render that reads the registry.
 */
export function registerMCPClientAccessGrants(grants: MCPClientAccessGrants): void {
	accessGrants = grants;
}

/** Returns the registered access grants, or undefined in builds without any. */
export function getMCPClientAccessGrants(): MCPClientAccessGrants | undefined {
	return accessGrants;
}

const noCount = (): number | undefined => undefined;

/**
 * useMCPClientAccessGrantCount counts the registered grants reaching the
 * server, or returns undefined when none are registered. Registration happens
 * once at module load, so the hook it calls is the same on every render.
 */
export function useMCPClientAccessGrantCount(mcpClientId: string): number | undefined {
	const useCount = accessGrants?.useCount ?? noCount;
	return useCount(mcpClientId);
}