// SBS fork: MetaMCP-style namespace semantics on top of Virtual MCP tool specs.
//
//   server ACTIVE   = the namespace has a spec for the server with at least one tool ("*" or names)
//   server INACTIVE = the spec is kept with tool_names [] (overrides survive the toggle)
//   tool ACTIVE     = tool_names has "*" or the tool's own name
import type { SBSToolOverride } from "@/lib/types/sbsMcp";
import type { VirtualMCPToolSpec } from "@/lib/types/virtualMcps";

export const TOOL_WILDCARD = "*";

export function specFor(specs: VirtualMCPToolSpec[], clientId: string): VirtualMCPToolSpec | undefined {
	return specs.find((s) => s.mcp_client_id === clientId);
}

export function isServerActive(spec?: VirtualMCPToolSpec): boolean {
	return !!spec && spec.tool_names.length > 0;
}

export function isToolActive(spec: VirtualMCPToolSpec | undefined, tool: string): boolean {
	if (!spec) return false;
	return spec.tool_names.includes(TOOL_WILDCARD) || spec.tool_names.includes(tool);
}

export function followsFutureTools(spec?: VirtualMCPToolSpec): boolean {
	return !!spec && spec.tool_names.includes(TOOL_WILDCARD);
}

/** Returns a copy of specs with fn applied to clientId's spec (created when missing). */
export function updateSpec(
	specs: VirtualMCPToolSpec[],
	clientId: string,
	fn: (spec: VirtualMCPToolSpec) => VirtualMCPToolSpec | null,
): VirtualMCPToolSpec[] {
	const existing = specFor(specs, clientId) ?? { mcp_client_id: clientId, tool_names: [] };
	const next = fn({ ...existing, tool_names: [...existing.tool_names] });
	const others = specs.filter((s) => s.mcp_client_id !== clientId);
	return next ? [...others, next] : others;
}

export function setServerActive(specs: VirtualMCPToolSpec[], clientId: string, active: boolean): VirtualMCPToolSpec[] {
	return updateSpec(specs, clientId, (spec) => ({ ...spec, tool_names: active ? [TOOL_WILDCARD] : [] }));
}

export function setToolActive(
	specs: VirtualMCPToolSpec[],
	clientId: string,
	allTools: string[],
	tool: string,
	active: boolean,
): VirtualMCPToolSpec[] {
	return updateSpec(specs, clientId, (spec) => {
		let names = spec.tool_names.includes(TOOL_WILDCARD) ? [...allTools] : spec.tool_names.filter((n) => n !== TOOL_WILDCARD);
		names = active ? Array.from(new Set([...names, tool])) : names.filter((n) => n !== tool);
		// Every current tool on again: follow future tools too, as MetaMCP's "all active" does.
		if (active && allTools.length > 0 && allTools.every((n) => names.includes(n))) names = [TOOL_WILDCARD];
		return { ...spec, tool_names: names };
	});
}

export function setAllToolsActive(specs: VirtualMCPToolSpec[], clientId: string, active: boolean): VirtualMCPToolSpec[] {
	return setServerActive(specs, clientId, active);
}

export function removeServer(specs: VirtualMCPToolSpec[], clientId: string): VirtualMCPToolSpec[] {
	return specs.filter((s) => s.mcp_client_id !== clientId);
}

/** Tool specs without overrides: what the upstream Virtual MCP API stores (overrides are kept server side). */
export function stripOverrides(specs: VirtualMCPToolSpec[]): VirtualMCPToolSpec[] {
	return specs.map(({ mcp_client_id, tool_names }) => ({ mcp_client_id, tool_names }));
}

export function gatewayToolName(clientName: string, tool: string): string {
	return `${clientName}-${tool}`;
}

export function overrideIsEmpty(ov?: SBSToolOverride): boolean {
	if (!ov) return true;
	const blank = (s?: string) => !s || !s.trim();
	return blank(ov.name) && blank(ov.title) && blank(ov.description) && (!ov.annotations || Object.keys(ov.annotations).length === 0);
}
