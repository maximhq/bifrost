import { beforeAll, describe, expect, it } from "vitest";
import i18n from "@/lib/i18n";
import type { MCPClient, MCPClientConfig } from "@/lib/types/mcp";
import type { MCPSessionRow } from "@/lib/types/mcpSessions";
import {
	accessStat,
	alertFor,
	allowedToolsBadges,
	authRows,
	bindingLabel,
	callerStatuses,
	callersState,
	callersSummary,
	connectionTarget,
	heroChips,
	heroRows,
	mcpGlobalSettings,
	schemaParams,
	schemaSummary,
	sessionKinds,
	settingsRows,
	toolAccess,
	toolHints,
	toolStat,
} from "./mcpClientDetailsSheet.utils";

beforeAll(async () => {
	await i18n.changeLanguage("en");
});

const config = (overrides: Partial<MCPClientConfig> = {}): MCPClientConfig => ({
	client_id: "c1",
	name: "github",
	endpoint_slug: "github",
	connection_type: "http",
	connection_string: { value: "https://mcp.example.com/github", type: "plain_text" },
	...overrides,
});

const client = (overrides: Partial<MCPClient> = {}, configOverrides: Partial<MCPClientConfig> = {}): MCPClient => ({
	config: config(configOverrides),
	tools: [],
	state: "healthy",
	vk_configs: [],
	...overrides,
});

const globals = { toolSyncIntervalMinutes: 10, maxProtocolVersion: "auto", toolExecutionTimeoutSeconds: 30, maxInstructionsLength: 4096 };

const row = (rows: { key: string }[], key: string) => rows.find((r) => r.key === key) as Record<string, unknown> | undefined;
const keysOf = (rows: { key: string }[]) => rows.map((r) => r.key);

describe("connectionTarget", () => {
	it("names what each connection type reaches", () => {
		expect(connectionTarget(config())).toBe("https://mcp.example.com/github");
		expect(connectionTarget(config({ connection_string: { ref: "env.MCP_URL", type: "env" } }))).toBe("env.MCP_URL");
		expect(connectionTarget(config({ connection_type: "stdio", stdio_config: { command: "npx", args: ["-y", "server"], envs: [] } }))).toBe(
			"npx -y server",
		);
		expect(connectionTarget(config({ connection_type: "openapi", openapi_config: { base_url: "https://api.example.com" } }))).toBe(
			"https://api.example.com",
		);
		expect(connectionTarget(config({ connection_type: "openapi", openapi_config: {} }))).toBe("base URL from the document's servers");
		expect(connectionTarget(config({ connection_string: undefined }))).toBe("-");
	});
});

describe("mcpGlobalSettings", () => {
	it("falls back to the server's own defaults, and reads a zero sync interval as the built-in ten minutes", () => {
		expect(mcpGlobalSettings(undefined)).toEqual(globals);
		expect(
			mcpGlobalSettings({
				mcp_tool_sync_interval: 0,
				mcp_max_protocol_version: "",
				mcp_tool_execution_timeout: 45,
				mcp_max_instructions_per_client: 0,
			}),
		).toEqual({ ...globals, toolExecutionTimeoutSeconds: 45 });
		expect(
			mcpGlobalSettings({
				mcp_tool_sync_interval: 5,
				mcp_max_protocol_version: "2025-06-18",
				mcp_tool_execution_timeout: 30,
				mcp_max_instructions_per_client: 2048,
			}),
		).toEqual({
			toolSyncIntervalMinutes: 5,
			maxProtocolVersion: "2025-06-18",
			toolExecutionTimeoutSeconds: 30,
			maxInstructionsLength: 2048,
		});
	});
});

describe("settingsRows", () => {
	it("shows the global value an unset override falls back to", () => {
		const rows = settingsRows(config(), globals);
		expect(keysOf(rows)).toEqual([
			"ping",
			"session_stickiness",
			"tool_sync_interval",
			"max_protocol_version",
			"tool_execution_timeout",
			"max_instructions_length",
		]);
		expect(row(rows, "ping")).toMatchObject({ value: "On" });
		expect(row(rows, "session_stickiness")).toMatchObject({ value: "Off" });
		expect(row(rows, "tool_sync_interval")).toMatchObject({ value: "10 min", note: "global default" });
		expect(row(rows, "max_protocol_version")).toMatchObject({ value: "Auto (latest)", note: "global default" });
		expect(row(rows, "tool_execution_timeout")).toMatchObject({ value: "30 s", note: "global default" });
		expect(row(rows, "max_instructions_length")).toMatchObject({ value: "4,096 bytes", note: "global default" });
	});

	it("shows a server's own values in the edit sheet's units, with no note", () => {
		const rows = settingsRows(
			config({
				is_ping_available: false,
				needs_session_stickiness: true,
				tool_sync_interval: 5 * 60e9,
				max_protocol_version: "2025-06-18",
				tool_execution_timeout: "1m30s",
				max_instructions_length: 2048,
			}),
			globals,
		);
		expect(row(rows, "ping")).toMatchObject({ value: "Off" });
		expect(row(rows, "session_stickiness")).toMatchObject({ value: "On" });
		expect(row(rows, "tool_sync_interval")).toEqual({ key: "tool_sync_interval", label: "Tool sync interval", value: "5 min" });
		expect(row(rows, "max_protocol_version")).toMatchObject({ value: "2025-06-18" });
		expect(row(rows, "tool_execution_timeout")).toMatchObject({ value: "90 s" });
		expect(row(rows, "max_instructions_length")).toMatchObject({ value: "2,048 bytes" });
	});

	it("leaves out rows that do not apply to the connection or auth type", () => {
		expect(keysOf(settingsRows(config({ connection_type: "openapi" }), globals))).not.toContain("max_protocol_version");
		expect(keysOf(settingsRows(config({ connection_type: "openapi" }), globals))).not.toContain("session_stickiness");
		expect(keysOf(settingsRows(config({ connection_type: "sse" }), globals))).not.toContain("session_stickiness");
		expect(keysOf(settingsRows(config({ auth_type: "per_user_oauth" }), globals))).not.toContain("session_stickiness");
		expect(keysOf(settingsRows(config({ auth_type: "headers" }), globals))).not.toContain("per_user_discovery");
	});

	it("explains Per-Caller Discovery, and that the sync interval is not used while it is on", () => {
		const on = settingsRows(config({ auth_type: "per_user_oauth", per_user_discovery: true }), globals);
		expect(row(on, "per_user_discovery")).toMatchObject({
			value: "On",
			hint: "Each caller's tools, prompts and resources are discovered with their own credential.",
		});
		expect(row(on, "tool_sync_interval")).toMatchObject({ value: "Not used", hint: "This server keeps no discovery of its own to sync." });

		const off = settingsRows(config({ auth_type: "token_exchange" }), globals);
		expect(row(off, "per_user_discovery")).toMatchObject({ value: "Off", hint: "Every caller sees what the admin credential discovers." });
		expect(row(off, "tool_sync_interval")).toMatchObject({ value: "10 min", note: "global default" });
	});
});

describe("authRows", () => {
	it("lists a headers server's header names, never their values", () => {
		const rows = authRows(
			config({
				auth_type: "headers",
				headers: { Authorization: { value: "Bearer secret", type: "plain_text" }, "X-Org": { value: "acme", type: "plain_text" } },
				allowed_extra_headers: ["*"],
			}),
			"Shared",
		);
		expect(keysOf(rows)).toEqual(["auth_type", "headers", "allowed_extra_headers", "tls_verification", "tls_ca"]);
		expect(row(rows, "auth_type")).toMatchObject({ value: "Headers · Shared" });
		expect(row(rows, "headers")).toMatchObject({ chips: ["Authorization", "X-Org"] });
		expect(JSON.stringify(rows)).not.toContain("Bearer secret");
		expect(row(rows, "allowed_extra_headers")).toMatchObject({ value: "All" });
		expect(row(rows, "tls_verification")).toMatchObject({ value: "On" });
		expect(row(rows, "tls_ca")).toMatchObject({ value: "None" });
	});

	it("reads empty lists as none, and shows TLS only for http and sse", () => {
		const rows = authRows(config({ connection_type: "stdio", allowed_extra_headers: ["x-user-id", "x-team"] }), "Shared");
		expect(row(rows, "auth_type")).toMatchObject({ value: "None · Shared" });
		expect(row(rows, "headers")).toMatchObject({ value: "None" });
		expect(row(rows, "allowed_extra_headers")).toMatchObject({ chips: ["x-user-id", "x-team"] });
		expect(keysOf(rows)).not.toContain("tls_verification");

		const tls = authRows(config({ tls_config: { insecure_skip_verify: true, ca_cert_pem: { ref: "env.CA_PEM", type: "env" } } }), "Shared");
		expect(row(tls, "tls_verification")).toMatchObject({ value: "Skipped" });
		expect(row(tls, "tls_ca")).toMatchObject({ value: "Set" });
	});

	it("adds the OAuth endpoints and scopes", () => {
		const rows = authRows(
			config({
				auth_type: "oauth",
				oauth_authorize_url: "https://idp.example.com/authorize",
				oauth_scopes: ["read", "write"],
				oauth_resource: "https://mcp.example.com",
			}),
			"Shared",
		);
		expect(row(rows, "auth_type")).toMatchObject({ value: "OAuth · Shared" });
		expect(row(rows, "oauth_authorize_url")).toMatchObject({ value: "https://idp.example.com/authorize" });
		expect(row(rows, "oauth_token_url")).toMatchObject({ value: "Not set" });
		expect(keysOf(rows)).not.toContain("oauth_registration_url");
		expect(row(rows, "oauth_scopes")).toMatchObject({ chips: ["read", "write"] });
		expect(row(rows, "oauth_resource")).toMatchObject({ value: "https://mcp.example.com" });
	});

	it("adds the required header names for a per-user headers server", () => {
		const rows = authRows(config({ auth_type: "per_user_headers", per_user_header_keys: ["X-API-Key", "X-Tenant"] }), "Per-User");
		expect(row(rows, "auth_type")).toMatchObject({ value: "Headers · Per-User" });
		expect(row(rows, "per_user_header_keys")).toMatchObject({ chips: ["X-API-Key", "X-Tenant"] });
	});

	it("summarizes token exchange, which always uses the SSO login application for Cross App Access", () => {
		const sso = authRows(
			config({ auth_type: "token_exchange", token_exchange: { audience: "api://hr", use_idp_credentials: true, scopes: [] } }),
			"Per-User",
		);
		expect(row(sso, "token_exchange_audience")).toMatchObject({ value: "api://hr" });
		expect(row(sso, "token_exchange_application")).toMatchObject({ value: "Identity provider application" });
		expect(row(sso, "token_exchange_server")).toMatchObject({ value: "SSO login issuer" });
		expect(row(sso, "token_exchange_scopes")).toMatchObject({ value: "None" });
		expect(keysOf(sso)).not.toContain("token_exchange_profile");

		const dedicated = authRows(
			config({ auth_type: "token_exchange", token_exchange: { audience: "api://hr", scopes: ["offline_access"] } }),
			"Per-User",
		);
		expect(row(dedicated, "token_exchange_application")).toMatchObject({ value: "Dedicated application" });
		expect(row(dedicated, "token_exchange_scopes")).toMatchObject({ chips: ["offline_access"] });

		const jag = authRows(
			config({
				auth_type: "token_exchange",
				token_exchange: { audience: "https://as.example.com", authorization_server_url: "https://idp.example.com", profile: "id_jag" },
			}),
			"Per-User",
		);
		expect(row(jag, "token_exchange_application")).toMatchObject({ value: "Identity provider application" });
		expect(row(jag, "token_exchange_server")).toMatchObject({ value: "https://idp.example.com" });
		expect(row(jag, "token_exchange_profile")).toMatchObject({ value: "Cross App Access (ID-JAG)" });
	});

	it("names an OpenAPI server's credential schemes", () => {
		const rows = authRows(
			config({ connection_type: "openapi", openapi_config: { security_credentials: { api_key: { value: { value: "s" } } } } }),
			"Shared",
		);
		expect(row(rows, "openapi_credentials")).toMatchObject({ chips: ["api_key"] });
		expect(keysOf(rows)).not.toContain("tls_verification");
	});
});

describe("heroChips", () => {
	it("names the transport, the auth and scope, and the modes that change behaviour", () => {
		expect(heroChips(config({ auth_type: "headers" }), "Shared")).toEqual([
			{ label: "HTTP", variant: "outline" },
			{ label: "Headers · Shared", variant: "outline" },
		]);
		expect(heroChips(config({ connection_type: "openapi" }), "Shared")).toEqual([
			{ label: "OpenAPI", variant: "outline" },
			{ label: "No auth", variant: "outline" },
		]);
		expect(heroChips(config({ auth_type: "per_user_oauth", per_user_discovery: true, is_code_mode_client: true }), "Per-User")).toEqual([
			{ label: "HTTP", variant: "outline" },
			{ label: "OAuth · Per-User", variant: "outline" },
			{ label: "Per-Caller Discovery", variant: "default" },
			{ label: "Code mode", variant: "secondary" },
		]);
	});
});

describe("heroRows", () => {
	it("gives the endpoint and what the server reaches, each with what to copy", () => {
		expect(heroRows(config(), "https://gw.example.com", "")).toEqual([
			{ key: "endpoint", label: "Endpoint", value: "/mcp/github", copy: "https://gw.example.com/mcp/github" },
			{ key: "url", label: "URL", value: "https://mcp.example.com/github", copy: "https://mcp.example.com/github" },
		]);
		expect(heroRows(config({ connection_type: "stdio", stdio_config: { command: "npx", args: ["srv"], envs: [] } }), "", "")[1]).toEqual({
			key: "command",
			label: "Command",
			value: "npx srv",
			copy: "npx srv",
		});
		expect(heroRows(config({ connection_type: "openapi", openapi_config: {} }), "", "Petstore · inline spec")).toEqual([
			{ key: "endpoint", label: "Endpoint", value: "/mcp/github", copy: "/mcp/github" },
			{ key: "spec", label: "Spec", value: "Petstore · inline spec" },
			{ key: "calls", label: "Calls", value: "base URL from the document's servers" },
		]);
	});
});

describe("toolStat", () => {
	const tools = [{ name: "a" }, { name: "b" }, { name: "c" }] as MCPClient["tools"];
	it("counts enabled and auto-executed tools among the discovered ones", () => {
		expect(toolStat(client({ tools }, { tools_to_execute: ["a", "b", "gone"], tools_to_auto_execute: ["b"] }))).toEqual({
			value: "2 of 3 enabled",
			sub: "1 auto-execute",
		});
		expect(toolStat(client({ tools }, { tools_to_execute: ["*"] }))).toEqual({ value: "3 of 3 enabled", sub: "none auto-execute" });
	});
	it("reads a server with no discovered tools by why it has none", () => {
		expect(toolStat(client({ state: "pending_verification" }))).toEqual({ value: "None yet", sub: "discovered after verification" });
		expect(toolStat(client())).toEqual({ value: "None", sub: "the server lists no tools" });
		expect(toolStat(client({ tools }, { per_user_discovery: true }))).toEqual({ value: "3 seen", sub: "in callers' discoveries" });
		expect(toolStat(client({}, { per_user_discovery: true }))).toEqual({ value: "None seen", sub: "no caller has connected yet" });
	});
});

describe("accessStat", () => {
	it("counts the keys and Virtual MCPs that reach the server", () => {
		expect(accessStat(config(), 3, 1)).toEqual({ value: "3 virtual keys", sub: "and 1 Virtual MCP · not allowed by default" });
		expect(accessStat(config(), 1, 0)).toEqual({ value: "1 virtual key", sub: "not allowed by default" });
		expect(accessStat(config(), 0, 0)).toEqual({ value: "No access", sub: "not allowed by default" });
		expect(accessStat(config({ allow_by_default: true }), 0, 0)).toEqual({ value: "Allowed by default", sub: "no key overrides" });
		expect(accessStat(config({ allow_by_default: true }), 2, 1)).toEqual({
			value: "Allowed by default",
			sub: "and 2 virtual keys · 1 Virtual MCP",
		});
	});
});

describe("toolHints", () => {
	it("shows only hints the server sets to true", () => {
		expect(toolHints(undefined)).toEqual([]);
		expect(toolHints({ readOnlyHint: true, idempotentHint: true, openWorldHint: false })).toEqual([
			{ label: "Read-only", tone: "read" },
			{ label: "Idempotent", tone: "plain" },
		]);
		expect(toolHints({ destructiveHint: true, openWorldHint: true })).toEqual([
			{ label: "Destructive", tone: "danger" },
			{ label: "Open world", tone: "plain" },
		]);
	});
	it("drops a destructive hint on a read-only tool, which the spec says it does not apply to", () => {
		expect(toolHints({ readOnlyHint: true, destructiveHint: true })).toEqual([{ label: "Read-only", tone: "read" }]);
	});
});

describe("schemaParams and schemaSummary", () => {
	it("lists parameters with their types and whether they are required", () => {
		expect(
			schemaParams({
				type: "object",
				required: ["owner"],
				properties: {
					owner: { type: "string", description: "Repository owner" },
					labels: { type: "array", items: { type: "string" } },
					state: { type: "string", enum: ["open", "closed"] },
					limit: { type: ["integer", "null"] },
					filter: { anyOf: [{ type: "string" }, { type: "object" }] },
				},
			}),
		).toEqual([
			{ name: "owner", type: "string", required: true, description: "Repository owner" },
			{ name: "labels", type: "string[]", required: false, description: "" },
			{ name: "state", type: "open | closed", required: false, description: "" },
			{ name: "limit", type: "integer | null", required: false, description: "" },
			{ name: "filter", type: "string | object", required: false, description: "" },
		]);
		expect(schemaParams(undefined)).toEqual([]);
	});
	it("summarizes an output schema's fields on one line", () => {
		expect(schemaSummary({ type: "object", properties: { url: { type: "string" }, number: { type: "integer" } } })).toBe(
			"url: string · number: integer",
		);
		expect(schemaSummary(undefined)).toBe("");
	});
});

describe("alertFor", () => {
	const failure = {
		stage: "credential" as const,
		message: "refresh token rejected: invalid_grant",
		at: new Date().toISOString(),
		since: new Date().toISOString(),
	};

	it("has nothing to say about a healthy server", () => {
		expect(alertFor(client(), true)).toBeNull();
	});

	it("offers the repair the actions menu offers for each auth type", () => {
		expect(alertFor(client({ state: "needs_reauth", last_failure: failure }, { auth_type: "oauth" }), false)).toMatchObject({
			variant: "destructive",
			title: "Reauthorization required",
			action: { kind: "reauthorize", label: "Reauthorize" },
		});
		expect(alertFor(client({ state: "needs_reauth" }, { auth_type: "per_user_oauth" }), false)?.action).toEqual({
			kind: "reauthorize",
			label: "Refresh admin credential",
		});
		expect(alertFor(client({ state: "needs_reauth" }, { auth_type: "per_user_headers" }), false)?.action).toEqual({
			kind: "refresh_headers",
			label: "Refresh admin credential",
		});
		expect(alertFor(client({ state: "needs_reauth" }, { auth_type: "token_exchange" }), false)?.action).toEqual({
			kind: "verify_exchange",
			label: "Re-verify as me",
		});
		expect(
			alertFor(client({ state: "needs_reauth" }, { auth_type: "per_user_oauth", per_user_discovery: true }), false)?.action,
		).toBeUndefined();
	});

	it("names the failure, and offers Reconnect only where it applies", () => {
		const unstable = alertFor(client({ state: "unstable", last_failure: { ...failure, stage: "connect", message: "i/o timeout" } }), true);
		expect(unstable).toMatchObject({
			variant: "warning",
			title: "Last connection check failed",
			action: { kind: "reconnect", label: "Reconnect" },
		});
		expect(unstable?.lines[0]).toBe("Connecting: i/o timeout");
		expect(alertFor(client({ state: "unstable" }), false)?.action).toBeUndefined();
	});

	it("lists each instance's view of a degraded server", () => {
		const degraded = alertFor(
			client({
				state: "degraded",
				node_states: {
					a: { state: "healthy" },
					b: { state: "healthy" },
					c: { state: "unstable", last_failure: { ...failure, stage: "connect", message: "connection refused" } },
				},
			}),
			true,
		);
		expect(degraded).toMatchObject({ variant: "info", title: "Instances disagree about this server's state" });
		// Grouped the way the table's state popover groups them, problems first.
		expect(degraded?.lines[0]).toContain("1 instance: unstable, Connecting: connection refused");
		expect(degraded?.lines[1]).toBe("2 instances: healthy.");
	});

	it("asks for the one-time verification of a pending server", () => {
		expect(alertFor(client({ state: "pending_verification" }, { auth_type: "per_user_oauth" }), false)).toMatchObject({
			variant: "warning",
			title: "Verification required",
			action: { kind: "authorize", label: "Authorize" },
		});
	});

	it("says how to turn a disabled server back on, with no button", () => {
		const disabled = alertFor(client({ state: "disabled" }, { disabled: true }), true);
		expect(disabled).toMatchObject({ variant: "default", title: "Disabled" });
		expect(disabled?.action).toBeUndefined();
	});
});

describe("toolAccess", () => {
	it("enables by name or wildcard and auto-executes only an enabled tool", () => {
		expect(toolAccess({ tools_to_execute: ["*"], tools_to_auto_execute: ["*"] }, "echo")).toEqual({ enabled: true, autoExecute: true });
		expect(toolAccess({ tools_to_execute: ["echo"] }, "echo")).toEqual({ enabled: true, autoExecute: false });
		expect(toolAccess({ tools_to_execute: ["add"], tools_to_auto_execute: ["*"] }, "echo")).toEqual({ enabled: false, autoExecute: false });
	});
});

describe("allowedToolsBadges", () => {
	it("reads an allow-list the way the virtual key sheet does", () => {
		expect(allowedToolsBadges(["*"])).toEqual([{ label: "All Tools", variant: "success" }]);
		expect(allowedToolsBadges([])).toEqual([{ label: "No tools (deny all)", variant: "destructive" }]);
		expect(allowedToolsBadges(["echo", "add"])).toEqual([
			{ label: "echo", variant: "secondary" },
			{ label: "add", variant: "secondary" },
		]);
	});
});

describe("caller sessions", () => {
	it("filters the sessions list to the rows a server's auth type stores", () => {
		expect(sessionKinds("per_user_oauth")).toEqual(["token", "flow"]);
		expect(sessionKinds("per_user_headers")).toEqual(["header", "flow"]);
		expect(sessionKinds("oauth")).toEqual([]);
	});

	it("counts by the statuses each auth type's rows can have", () => {
		expect(callerStatuses("per_user_oauth").map((s) => s.status)).toEqual(["active", "needs_reauth", "pending", "orphaned"]);
		expect(callerStatuses("per_user_headers").map((s) => s.label)).toEqual(["Active", "Needs update", "Pending", "Orphaned"]);
	});

	it("sums the counts and calls out the ones a caller must act on", () => {
		expect(callersSummary({ active: 12, needs_reauth: 1, pending: 1, orphaned: 0 }, "ready")).toEqual({
			total: 14,
			value: "14",
			sub: "12 active · 2 need attention",
		});
		expect(callersSummary({ active: 3, needs_update: 0, pending: 0, orphaned: 1 }, "ready")).toEqual({
			total: 4,
			value: "4",
			sub: "3 active",
		});
		expect(callersSummary({}, "ready")).toEqual({ total: 0, value: "0", sub: "no sessions yet" });
	});

	it("says the callers are loading or failed instead of counting nobody", () => {
		expect(callersSummary({}, "loading")).toEqual({ value: "…", sub: "loading callers" });
		expect(callersSummary({}, "error")).toEqual({ value: "-", sub: "couldn't load callers" });
	});

	it("folds the caller queries into one state", () => {
		const ok = { currentData: { total_count: 0 }, isError: false };
		expect(callersState([ok, ok])).toBe("ready");
		expect(callersState([ok, { currentData: undefined, isError: false }])).toBe("loading");
		expect(
			callersState([
				{ currentData: undefined, isError: false },
				{ currentData: undefined, isError: true },
			]),
		).toBe("error");
	});

	// RTK Query keeps a query's last result in data while it fetches for new arguments (another
	// filter, another server); only currentData belongs to the arguments asked for now.
	it("reads a query holding only another arguments' result as loading", () => {
		const current = { data: { total_count: 1 }, currentData: { total_count: 1 }, isLoading: false, isFetching: false, isError: false };
		const stale = { data: { total_count: 3 }, currentData: undefined, isLoading: false, isFetching: true, isError: false };
		expect(callersState([current, stale])).toBe("loading");
		expect(callersState([current, current])).toBe("ready");
	});

	it("names who a session is bound to", () => {
		const base = { id: "1", kind: "token", auth_kind: "oauth", status: "active", created_at: "", can_reauth: true } as const;
		expect(
			bindingLabel({ ...base, auth_mode: "user", user_id: "u1", user: { id: "u1", email: "a@example.com" } } as MCPSessionRow),
		).toEqual({ name: "a@example.com", kind: "User" });
		expect(bindingLabel({ ...base, auth_mode: "user", user_id: "u1" } as MCPSessionRow)).toEqual({ name: "u1", kind: "User" });
		expect(bindingLabel({ ...base, auth_mode: "vk", virtual_key: { id: "vk1", name: "support-bot" } } as MCPSessionRow)).toEqual({
			name: "support-bot",
			kind: "Virtual key",
		});
		expect(bindingLabel({ ...base, auth_mode: "session", session_id: "sess_1" } as MCPSessionRow)).toEqual({
			name: "sess_1",
			kind: "Session",
		});
	});
});