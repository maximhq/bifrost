import type { CoreConfig } from "@/lib/types/config";
import type { VirtualKey } from "@/lib/types/governance";
import type { MCPClient } from "@/lib/types/mcp";
import i18n from "@/lib/i18n";
import { getExampleBaseUrl } from "@/lib/utils/port";
import type { AuthMethod, HarnessPlatform, ServerScope } from "./types";

/** The client-config settings that can pin Bifrost's public base URL for a given surface. */
export type ExternalClientUrlKey = "mcp_external_client_url" | "a2a_external_client_url";

/**
 * Resolve the externally reachable Bifrost base URL used in generated commands/configs.
 *
 * Order of preference:
 *  1. The admin-configured external client URL (`mcp_external_client_url` by default,
 *     `a2a_external_client_url` for agent-gateway surfaces). It is returned verbatim: the
 *     admin's value already includes any `server.base_path`. When sourced from an env var
 *     the value is still honoured as long as it resolves to a concrete http(s) URL — a
 *     redacted/empty value falls through to the window-origin heuristic below.
 *  2. The gateway origin plus `server.base_path` (`getExampleBaseUrl`): the window origin
 *     for a production build, which Go serves itself, or the Go server port for a Vite dev
 *     build. The build mode decides, never the port, so a Bifrost listening on any
 *     localhost port prints its real origin.
 *  3. A placeholder the user must replace by hand.
 */
export function getExternalBaseUrl(clientConfig?: CoreConfig, urlKey: ExternalClientUrlKey = "mcp_external_client_url"): string {
	const configuredURL = clientConfig?.[urlKey]?.value?.trim();
	if (configuredURL && /^https?:\/\//i.test(configuredURL)) {
		return configuredURL.replace(/\/+$/, "");
	}
	const baseURL = getExampleBaseUrl();
	if (/^https?:\/\//i.test(baseURL)) {
		return baseURL;
	}
	return "<YOUR_BIFROST_URL>";
}

/** Quote a value for safe inclusion in a POSIX shell command. */
export function quoteShellValue(value: string): string {
	if (/^[a-zA-Z0-9_./:@%+=,-]+$/.test(value)) return value;
	// Replace control characters first so they don't appear as raw bytes in the
	// quoted string (POSIX double-quoted strings don't interpret \n etc., but
	// downstream consumers like `claude mcp add` would see literal newlines).
	const sanitized = value.replace(/\n/g, "\\n").replace(/\r/g, "\\r").replace(/\t/g, "\\t");
	return `"${sanitized.replace(/(["\\$`])/g, "\\$1")}"`;
}

/** Quote a value as a TOML basic string, escaping control characters per the TOML spec. */
export function quoteTomlString(value: string): string {
	const escaped = value
		.replace(/\\/g, "\\\\")
		.replace(/"/g, '\\"')
		.replace(/\x08/g, "\\b")
		.replace(/\t/g, "\\t")
		.replace(/\n/g, "\\n")
		.replace(/\f/g, "\\f")
		.replace(/\r/g, "\\r")
		// Any remaining control char (U+0000–U+001F, U+007F) must use the \uXXXX form.
		.replace(/[\x00-\x1f\x7f]/g, (c) => `\\u${c.charCodeAt(0).toString(16).padStart(4, "0").toUpperCase()}`);
	return `"${escaped}"`;
}

/** UTF-8 safe base64 encoding for the browser (used by the Cursor deeplink). */
export function encodeBase64(value: string): string {
	if (typeof window === "undefined") return "";
	return window.btoa(String.fromCharCode(...new TextEncoder().encode(value)));
}

/** Whether an MCP client is reachable using the given virtual key. */
export function isClientAllowedForVirtualKey(client: MCPClient, virtualKey: VirtualKey): boolean {
	if (client.config.disabled) return false;
	if (client.config.allow_by_default) return true;
	return client.vk_configs?.some((config) => config.virtual_key_id === virtualKey.id) ?? false;
}

/** Mask a secret value, keeping a short prefix/suffix for recognisability. */
export function maskSecret(value?: string): string {
	if (!value) return "";
	if (value.length <= 12) return "********";
	return `${value.slice(0, 6)}****${value.slice(-4)}`;
}

/** Human-readable label describing how many servers a command registers. */
export function getRegistrationLabel(serverScope: ServerScope, selectedServers: MCPClient[]): string {
	if (serverScope === "selected" && selectedServers.length > 0) {
		return i18n.t("usageGuide.serverCount", { ns: "mcp", count: selectedServers.length });
	}
	return "bifrost";
}

/** The registration name used for the generated MCP server entry. */
export function getRegistrationName(selectedServers?: MCPClient[]): string {
	return selectedServers?.length === 1 ? selectedServers[0].config.name : "bifrost";
}

/** Comma-joined list of selected server names, or undefined when none are selected. */
export function getIncludeClients(selectedServers?: MCPClient[]): string | undefined {
	if (!selectedServers?.length) return undefined;
	return selectedServers.map((server) => server.config.name).join(",");
}

export function getUserHomePrefix(platform: HarnessPlatform): string {
	return platform === "windows" ? "%USERPROFILE%" : "~";
}

/** Stand-in for the identity-provider bearer the user pastes into their own config. */
export const IDP_TOKEN_PLACEHOLDER = "<YOUR_IDP_ACCESS_TOKEN>";

/**
 * Headers the generated client config must send, derived from the chosen
 * authentication method. OAuth clients carry no credential at all: the token
 * arrives from the consent flow, and sending a virtual key alongside it is
 * rejected by the gateway as conflicting credentials. The server filter is a
 * credential-free header, but it is only offered on the virtual-key path, where
 * the allowed server list can be resolved from the key.
 */
export function buildMCPHeaders({
	authMethod,
	selectedServers,
	virtualKey,
}: {
	authMethod: AuthMethod;
	selectedServers?: MCPClient[];
	virtualKey?: VirtualKey;
}): Record<string, string> {
	if (authMethod === "oauth") return {};
	if (authMethod === "idp_token") return { Authorization: `Bearer ${IDP_TOKEN_PLACEHOLDER}` };

	if (!virtualKey) return {};
	const headers: Record<string, string> = { "x-bf-vk": virtualKey.value };
	const includeClients = getIncludeClients(selectedServers);
	if (includeClients) headers["x-bf-mcp-include-clients"] = includeClients;
	return headers;
}