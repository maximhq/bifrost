// Helpers for MCP servers with per_user_discovery on, which discover tools per
// caller and keep no admin credential, shared by the MCP registry and the
// pickers that list a server's tools.

import i18n from "@/lib/i18n";
import type { MCPAuthType, MCPClient, MCPClientConfig, MCPConnectionType } from "@/lib/types/mcp";

const PER_USER_AUTH_TYPES: ReadonlySet<MCPAuthType> = new Set(["per_user_oauth", "per_user_headers", "token_exchange"]);

/**
 * supportsPerUserDiscovery reports whether a server of this auth and connection
 * type can discover tools per caller: only a per-user auth type has a
 * credential per caller, and an openapi server's tools come from its document.
 */
export function supportsPerUserDiscovery(authType?: MCPAuthType, connectionType?: MCPConnectionType): boolean {
	return !!authType && PER_USER_AUTH_TYPES.has(authType) && connectionType !== "openapi";
}

/** perUserDiscoveryOn reports whether config has per-caller discovery. */
export function perUserDiscoveryOn(config?: Pick<MCPClientConfig, "per_user_discovery">): boolean {
	return config?.per_user_discovery === true;
}

/**
 * toolSubtitle is the line shown under a tool's name: its description or, for
 * a server with per_user_discovery on, how many callers' discoveries include
 * it. Descriptions can differ from one caller to another, so none is shown.
 */
export function toolSubtitle(
	client: Pick<MCPClient, "config" | "callers_by_tool">,
	tool: { name: string; description?: string },
): string | undefined {
	if (!perUserDiscoveryOn(client.config)) {
		return tool.description;
	}
	const callers = client.callers_by_tool?.[tool.name] ?? 0;
	if (callers === 0) {
		return i18n.t("registry.sheet.toolNotSeenYet", { ns: "mcp" });
	}
	return i18n.t("registry.sheet.toolDiscoveredBy", { ns: "mcp", count: callers });
}