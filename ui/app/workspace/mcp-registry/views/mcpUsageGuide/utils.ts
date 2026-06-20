import type { CoreConfig } from "@/lib/types/config";
import type { VirtualKey } from "@/lib/types/governance";
import type { MCPClient } from "@/lib/types/mcp";
import type { HarnessPlatform, ServerScope } from "./types";

/** Default port Bifrost serves on; used when guessing the gateway URL in local dev. */
const DEFAULT_BIFROST_PORT = "8080";

/**
 * Resolve the externally reachable Bifrost base URL used in generated commands/configs.
 *
 * Order of preference:
 *  1. The admin-configured `mcp_external_client_url`. When sourced from an env var the
 *     value is still honoured as long as it resolves to a concrete http(s) URL — a
 *     redacted/empty value falls through to the window-origin heuristic below.
 *  2. The current window origin. For local dev on a non-default port we assume the
 *     gateway listens on DEFAULT_BIFROST_PORT (the UI is often proxied on another port).
 *  3. A placeholder the user must replace by hand.
 */
export function getExternalBaseUrl(clientConfig?: CoreConfig): string {
	const configuredURL = clientConfig?.mcp_external_client_url?.value?.trim();
	if (configuredURL && /^https?:\/\//i.test(configuredURL)) {
		return configuredURL.replace(/\/+$/, "");
	}
	if (typeof window !== "undefined" && window.location.origin) {
		const { protocol, hostname, port } = window.location;
		const isLocalHost = hostname === "localhost" || hostname === "127.0.0.1" || hostname === "::1";
		if (isLocalHost && port && port !== DEFAULT_BIFROST_PORT) {
			return `${protocol}//${hostname}:${DEFAULT_BIFROST_PORT}`;
		}
		return window.location.origin.replace(/\/+$/, "");
	}
	return "<YOUR_BIFROST_URL>";
}

/** Quote a value for safe inclusion in a POSIX shell command. */
export function quoteShellValue(value: string): string {
	if (/^[a-zA-Z0-9_./:@%+=,-]+$/.test(value)) return value;
	// Replace control characters first so they don't appear as raw bytes in the
	// quoted string (POSIX double-quoted strings don't interpret \n etc., but
	// downstream consumers like `claude mcp add` would see literal newlines).
	const sanitized = value
		.replace(/\n/g, "\\n")
		.replace(/\r/g, "\\r")
		.replace(/\t/g, "\\t");
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
	if (client.config.allow_on_all_virtual_keys) return true;
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
		return `${selectedServers.length} ${selectedServers.length === 1 ? "server" : "servers"}`;
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
