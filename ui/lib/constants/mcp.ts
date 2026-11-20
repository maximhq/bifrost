import i18n from "@/lib/i18n";

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