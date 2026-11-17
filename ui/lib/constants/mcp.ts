import i18n from "@/lib/i18n";

// Whether the dashboard shows the Bifrost MCP server's settings (Bifrost URL, OpenAPI URL,
// allow configuration changes). The server at /v1/mcp is not released yet, so they stay
// hidden; the stored values are kept and saved unchanged. Turn on when it ships.
export const BIFROST_MCP_SETTINGS_VISIBLE = false;

// "Use the global setting" for one MCP server's max protocol version; sent to the API as "".
export const MCP_PROTOCOL_VERSION_USE_GLOBAL = "global";

// Values a max protocol version can be set to, newest first.
export const MCP_MAX_PROTOCOL_VERSIONS = ["auto", "2026-07-28", "2025-11-25", "2025-06-18"] as const;

export const MCP_MAX_PROTOCOL_VERSION_OPTIONS = MCP_MAX_PROTOCOL_VERSIONS.map((value) => ({ value }));

// Resolved at call time so the label follows the active language.
export function mcpMaxProtocolVersionLabel(value?: string): string {
	const version = value || "auto";
	return version === "auto" ? i18n.t("registry.form.maxProtocolAuto", { ns: "mcp" }) : version;
}