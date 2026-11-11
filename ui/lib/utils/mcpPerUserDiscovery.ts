// Helpers for MCP servers with per_user_discovery on, which discover tools per
// caller and keep no admin credential, shared by the MCP registry and the
// pickers that list a server's tools.

import i18n from "@/lib/i18n";
import type { MCPAuthType, MCPClient, MCPClientConfig, MCPConnectionType } from "@/lib/types/mcp";
import type { MCPSessionRow } from "@/lib/types/mcpSessions";
import { formatRelativePast } from "@/lib/utils/mcpCredential";

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

/** SessionDiscoveryCell is the Discovery column's text for one session row. */
export interface SessionDiscoveryCell {
	text: string;
	sub?: string;
	muted: boolean;
}

/**
 * sessionDiscoveryCell describes what a session row's binding holds on its
 * server. A server without per_user_discovery keeps one discovery every
 * caller shares. On one with it, a held discovery shows its counts whatever
 * the credential's status; otherwise a session-bound caller keeps none, a
 * pending sign-in has no credential yet, an active credential gets one on the
 * caller's next connect, and one that needs action had it deleted with it.
 */
export function sessionDiscoveryCell(
	row: Pick<MCPSessionRow, "kind" | "auth_mode" | "status" | "mcp_client" | "discovery">,
): SessionDiscoveryCell {
	if (!row.mcp_client?.per_user_discovery) {
		return { text: i18n.t("sessions.discoveryCell.shared", { ns: "mcp" }), muted: true };
	}
	const discovery = row.discovery;
	if (discovery) {
		const parts = [
			discovery.tool_count === 0
				? i18n.t("sessions.discoveryCell.noTools", { ns: "mcp" })
				: i18n.t("sessions.discoveryCell.tools", { ns: "mcp", count: discovery.tool_count }),
		];
		if (discovery.prompt_count > 0) parts.push(i18n.t("sessions.discoveryCell.prompts", { ns: "mcp", count: discovery.prompt_count }));
		if (discovery.resource_count > 0)
			parts.push(i18n.t("sessions.discoveryCell.resources", { ns: "mcp", count: discovery.resource_count }));
		return {
			text: parts.join(" · "),
			sub: i18n.t("sessions.discoveryCell.discoveredAgo", { ns: "mcp", time: formatRelativePast(discovery.discovered_at) }),
			muted: false,
		};
	}
	if (row.auth_mode === "session") {
		return { text: "-", muted: true };
	}
	if (row.kind === "flow") {
		return { text: i18n.t("sessions.discoveryCell.notYet", { ns: "mcp" }), muted: true };
	}
	if (row.status === "active") {
		return {
			text: i18n.t("sessions.discoveryCell.notYet", { ns: "mcp" }),
			sub: i18n.t("sessions.discoveryCell.onNextConnect", { ns: "mcp" }),
			muted: true,
		};
	}
	return { text: "-", muted: true };
}