import type { CoreConfig } from "@/lib/types/config";
import type { VirtualKey } from "@/lib/types/governance";
import type { MCPClient } from "@/lib/types/mcp";
import type { ClaudeScope } from "./types";
import { encodeBase64, getExternalBaseUrl, getIncludeClients, getRegistrationName, quoteShellValue, quoteTomlString } from "./utils";

// ── Claude Code ────────────────────────────────────────────────────────

export function buildClaudeCodeCommand({
	clientConfig,
	scope,
	selectedServers,
	virtualKey,
}: {
	clientConfig?: CoreConfig;
	scope: ClaudeScope;
	selectedServers?: MCPClient[];
	virtualKey: VirtualKey;
}): string {
	const gatewayUrl = `${getExternalBaseUrl(clientConfig)}/mcp`;
	const registrationName = getRegistrationName(selectedServers);

	const lines = [
		`claude mcp add --transport http ${quoteShellValue(registrationName)} --scope ${scope} ${quoteShellValue(gatewayUrl)} \\`,
		`  --header ${quoteShellValue(`x-bf-vk: ${virtualKey.value}`)}`,
	];

	const includeClients = getIncludeClients(selectedServers);
	if (includeClients) {
		lines[lines.length - 1] += " \\";
		lines.push(`  --header ${quoteShellValue(`x-bf-mcp-include-clients: ${includeClients}`)}`);
	}

	return lines.join("\n");
}

// ── Codex ──────────────────────────────────────────────────────────────

export function buildCodexConfig({
	clientConfig,
	selectedServers,
	virtualKey,
}: {
	clientConfig?: CoreConfig;
	selectedServers?: MCPClient[];
	virtualKey: VirtualKey;
}): string {
	const gatewayUrl = `${getExternalBaseUrl(clientConfig)}/mcp`;
	const registrationName = getRegistrationName(selectedServers);
	const includeClients = getIncludeClients(selectedServers);

	const headerEntries = [`"x-bf-vk" = ${quoteTomlString(virtualKey.value)}`];
	if (includeClients) {
		headerEntries.push(`"x-bf-mcp-include-clients" = ${quoteTomlString(includeClients)}`);
	}

	return [
		`[mcp_servers.${quoteTomlString(registrationName)}]`,
		`url = ${quoteTomlString(gatewayUrl)}`,
		`http_headers = { ${headerEntries.join(", ")} }`,
	].join("\n");
}

// ── Cursor ─────────────────────────────────────────────────────────────

function buildCursorServer({
	clientConfig,
	selectedServers,
	virtualKey,
}: {
	clientConfig?: CoreConfig;
	selectedServers?: MCPClient[];
	virtualKey: VirtualKey;
}): { name: string; server: { url: string; headers: Record<string, string> } } {
	const gatewayUrl = `${getExternalBaseUrl(clientConfig)}/mcp`;
	const registrationName = getRegistrationName(selectedServers);
	const headers: Record<string, string> = { "x-bf-vk": virtualKey.value };

	const includeClients = getIncludeClients(selectedServers);
	if (includeClients) {
		headers["x-bf-mcp-include-clients"] = includeClients;
	}

	return { name: registrationName, server: { url: gatewayUrl, headers } };
}

export function buildCursorConfig(args: { clientConfig?: CoreConfig; selectedServers?: MCPClient[]; virtualKey: VirtualKey }): string {
	const { name, server } = buildCursorServer(args);
	return JSON.stringify({ mcpServers: { [name]: server } }, null, 2);
}

/**
 * Cursor deeplink encodes the inner server config (not wrapped in `mcpServers`) as base64.
 * See https://cursor.com/docs/mcp.md (MCP Install Links).
 */
export function buildCursorDeeplink(args: { clientConfig?: CoreConfig; selectedServers?: MCPClient[]; virtualKey: VirtualKey }): string {
	const { name, server } = buildCursorServer(args);
	const encodedConfig = encodeBase64(JSON.stringify(server));
	if (!encodedConfig) return "";
	return `cursor://anysphere.cursor-deeplink/mcp/install?name=${encodeURIComponent(name)}&config=${encodeURIComponent(encodedConfig)}`;
}

// ── Windsurf ───────────────────────────────────────────────────────────

export function buildWindsurfConfig({
	clientConfig,
	selectedServers,
	virtualKey,
}: {
	clientConfig?: CoreConfig;
	selectedServers?: MCPClient[];
	virtualKey: VirtualKey;
}): string {
	const gatewayUrl = `${getExternalBaseUrl(clientConfig)}/mcp`;
	const registrationName = getRegistrationName(selectedServers);
	const headers: Record<string, string> = { "x-bf-vk": virtualKey.value };

	const includeClients = getIncludeClients(selectedServers);
	if (includeClients) {
		headers["x-bf-mcp-include-clients"] = includeClients;
	}

	return JSON.stringify(
		{
			mcpServers: {
				[registrationName]: {
					serverUrl: gatewayUrl,
					headers,
				},
			},
		},
		null,
		2,
	);
}

// ── VS Code ────────────────────────────────────────────────────────────

function buildVSCodeServer({
	clientConfig,
	selectedServers,
	virtualKey,
}: {
	clientConfig?: CoreConfig;
	selectedServers?: MCPClient[];
	virtualKey: VirtualKey;
}): { name: string; server: { type: "http"; url: string; headers: Record<string, string> } } {
	const gatewayUrl = `${getExternalBaseUrl(clientConfig)}/mcp`;
	const registrationName = getRegistrationName(selectedServers);
	const headers: Record<string, string> = { "x-bf-vk": virtualKey.value };

	const includeClients = getIncludeClients(selectedServers);
	if (includeClients) {
		headers["x-bf-mcp-include-clients"] = includeClients;
	}

	return { name: registrationName, server: { type: "http", url: gatewayUrl, headers } };
}

export function buildVSCodeConfig(args: { clientConfig?: CoreConfig; selectedServers?: MCPClient[]; virtualKey: VirtualKey }): string {
	const { name, server } = buildVSCodeServer(args);
	return JSON.stringify({ servers: { [name]: server } }, null, 2);
}

/**
 * VS Code install link encodes the inline server config plus a `name` field (not wrapped
 * in `servers`) as a URL-encoded JSON string.
 * See https://code.visualstudio.com/api/extension-guides/ai/mcp#create-an-mcp-installation-url
 */
export function buildVSCodeDeeplink(args: { clientConfig?: CoreConfig; selectedServers?: MCPClient[]; virtualKey: VirtualKey }): string {
	const { name, server } = buildVSCodeServer(args);
	return `vscode:mcp/install?${encodeURIComponent(JSON.stringify({ name, ...server }))}`;
}

// ── OpenCode ───────────────────────────────────────────────────────────

/**
 * OpenCode uses an `mcp` root object; remote servers require `type: "remote"`
 * with `url` and `headers`. Config lives in `opencode.json`.
 * See https://opencode.ai/docs/mcp-servers.md
 */
export function buildOpenCodeConfig({
	clientConfig,
	selectedServers,
	virtualKey,
}: {
	clientConfig?: CoreConfig;
	selectedServers?: MCPClient[];
	virtualKey: VirtualKey;
}): string {
	const gatewayUrl = `${getExternalBaseUrl(clientConfig)}/mcp`;
	const registrationName = getRegistrationName(selectedServers);
	const headers: Record<string, string> = { "x-bf-vk": virtualKey.value };

	const includeClients = getIncludeClients(selectedServers);
	if (includeClients) {
		headers["x-bf-mcp-include-clients"] = includeClients;
	}

	return JSON.stringify(
		{
			$schema: "https://opencode.ai/config.json",
			mcp: {
				[registrationName]: {
					type: "remote",
					url: gatewayUrl,
					enabled: true,
					headers,
				},
			},
		},
		null,
		2,
	);
}

// ── Antigravity ────────────────────────────────────────────────────────

export function buildAntigravityConfig({
	clientConfig,
	selectedServers,
	virtualKey,
}: {
	clientConfig?: CoreConfig;
	selectedServers?: MCPClient[];
	virtualKey: VirtualKey;
}): string {
	const gatewayUrl = `${getExternalBaseUrl(clientConfig)}/mcp`;
	const registrationName = getRegistrationName(selectedServers);
	const headers: Record<string, string> = { "x-bf-vk": virtualKey.value };

	const includeClients = getIncludeClients(selectedServers);
	if (includeClients) {
		headers["x-bf-mcp-include-clients"] = includeClients;
	}

	return JSON.stringify(
		{
			mcpServers: {
				[registrationName]: {
					serverUrl: gatewayUrl,
					headers,
				},
			},
		},
		null,
		2,
	);
}