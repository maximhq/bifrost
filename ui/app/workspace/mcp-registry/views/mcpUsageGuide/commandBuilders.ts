import type { CoreConfig } from "@/lib/types/config";
import type { MCPClient } from "@/lib/types/mcp";
import type { ClaudeScope } from "./types";
import { encodeBase64, getExternalBaseUrl, getRegistrationName, quoteShellValue, quoteTomlString } from "./utils";

/**
 * Shared shape of every builder. `headers` is already resolved by the caller from
 * the chosen authentication method and server scope, and is empty when the client
 * authenticates through the OAuth consent flow.
 */
interface BuildArgs {
	clientConfig?: CoreConfig;
	headers: Record<string, string>;
	selectedServers?: MCPClient[];
}

// ── Claude Code ────────────────────────────────────────────────────────

export function buildClaudeCodeCommand({ clientConfig, headers, scope, selectedServers }: BuildArgs & { scope: ClaudeScope }): string {
	const gatewayUrl = `${getExternalBaseUrl(clientConfig)}/mcp`;
	const registrationName = getRegistrationName(selectedServers);

	const lines = [`claude mcp add --transport http ${quoteShellValue(registrationName)} --scope ${scope} ${quoteShellValue(gatewayUrl)}`];
	for (const [name, value] of Object.entries(headers)) {
		lines[lines.length - 1] += " \\";
		lines.push(`  --header ${quoteShellValue(`${name}: ${value}`)}`);
	}

	return lines.join("\n");
}

// ── Codex ──────────────────────────────────────────────────────────────

export function buildCodexConfig({ clientConfig, headers, selectedServers }: BuildArgs): string {
	const gatewayUrl = `${getExternalBaseUrl(clientConfig)}/mcp`;
	const registrationName = getRegistrationName(selectedServers);

	const lines = [`[mcp_servers.${quoteTomlString(registrationName)}]`, `url = ${quoteTomlString(gatewayUrl)}`];

	const headerEntries = Object.entries(headers).map(([name, value]) => `${quoteTomlString(name)} = ${quoteTomlString(value)}`);
	if (headerEntries.length > 0) {
		lines.push(`http_headers = { ${headerEntries.join(", ")} }`);
	}

	return lines.join("\n");
}

// ── Cursor ─────────────────────────────────────────────────────────────

function buildCursorServer({ clientConfig, headers, selectedServers }: BuildArgs): {
	name: string;
	server: { url: string; headers?: Record<string, string> };
} {
	const gatewayUrl = `${getExternalBaseUrl(clientConfig)}/mcp`;
	return {
		name: getRegistrationName(selectedServers),
		server: { url: gatewayUrl, ...(hasHeaders(headers) ? { headers } : {}) },
	};
}

export function buildCursorConfig(args: BuildArgs): string {
	const { name, server } = buildCursorServer(args);
	return JSON.stringify({ mcpServers: { [name]: server } }, null, 2);
}

/**
 * Cursor deeplink encodes the inner server config (not wrapped in `mcpServers`) as base64.
 * See https://cursor.com/docs/mcp.md (MCP Install Links).
 */
export function buildCursorDeeplink(args: BuildArgs): string {
	const { name, server } = buildCursorServer(args);
	const encodedConfig = encodeBase64(JSON.stringify(server));
	if (!encodedConfig) return "";
	return `cursor://anysphere.cursor-deeplink/mcp/install?name=${encodeURIComponent(name)}&config=${encodeURIComponent(encodedConfig)}`;
}

// ── Windsurf ───────────────────────────────────────────────────────────

export function buildWindsurfConfig({ clientConfig, headers, selectedServers }: BuildArgs): string {
	const gatewayUrl = `${getExternalBaseUrl(clientConfig)}/mcp`;
	const registrationName = getRegistrationName(selectedServers);

	return JSON.stringify(
		{
			mcpServers: {
				[registrationName]: {
					serverUrl: gatewayUrl,
					...(hasHeaders(headers) ? { headers } : {}),
				},
			},
		},
		null,
		2,
	);
}

// ── VS Code ────────────────────────────────────────────────────────────

function buildVSCodeServer({ clientConfig, headers, selectedServers }: BuildArgs): {
	name: string;
	server: { type: "http"; url: string; headers?: Record<string, string> };
} {
	const gatewayUrl = `${getExternalBaseUrl(clientConfig)}/mcp`;
	return {
		name: getRegistrationName(selectedServers),
		server: { type: "http", url: gatewayUrl, ...(hasHeaders(headers) ? { headers } : {}) },
	};
}

export function buildVSCodeConfig(args: BuildArgs): string {
	const { name, server } = buildVSCodeServer(args);
	return JSON.stringify({ servers: { [name]: server } }, null, 2);
}

/**
 * VS Code install link encodes the inline server config plus a `name` field (not wrapped
 * in `servers`) as a URL-encoded JSON string.
 * See https://code.visualstudio.com/api/extension-guides/ai/mcp#create-an-mcp-installation-url
 */
export function buildVSCodeDeeplink(args: BuildArgs): string {
	const { name, server } = buildVSCodeServer(args);
	return `vscode:mcp/install?${encodeURIComponent(JSON.stringify({ name, ...server }))}`;
}

// ── OpenCode ───────────────────────────────────────────────────────────

/**
 * OpenCode uses an `mcp` root object; remote servers require `type: "remote"`
 * with `url` and `headers`. Config lives in `opencode.json`.
 * See https://opencode.ai/docs/mcp-servers.md
 */
export function buildOpenCodeConfig({ clientConfig, headers, selectedServers }: BuildArgs): string {
	const gatewayUrl = `${getExternalBaseUrl(clientConfig)}/mcp`;
	const registrationName = getRegistrationName(selectedServers);

	return JSON.stringify(
		{
			$schema: "https://opencode.ai/config.json",
			mcp: {
				[registrationName]: {
					type: "remote",
					url: gatewayUrl,
					enabled: true,
					...(hasHeaders(headers) ? { headers } : {}),
				},
			},
		},
		null,
		2,
	);
}

// ── Antigravity ────────────────────────────────────────────────────────

export function buildAntigravityConfig({ clientConfig, headers, selectedServers }: BuildArgs): string {
	const gatewayUrl = `${getExternalBaseUrl(clientConfig)}/mcp`;
	const registrationName = getRegistrationName(selectedServers);

	return JSON.stringify(
		{
			mcpServers: {
				[registrationName]: {
					serverUrl: gatewayUrl,
					...(hasHeaders(headers) ? { headers } : {}),
				},
			},
		},
		null,
		2,
	);
}

/** Omit the `headers` key entirely rather than emitting an empty object. */
function hasHeaders(headers: Record<string, string>): boolean {
	return Object.keys(headers).length > 0;
}