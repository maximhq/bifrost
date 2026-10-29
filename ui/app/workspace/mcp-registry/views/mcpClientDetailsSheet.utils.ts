import i18n, { getLocale } from "@/lib/i18n";
// Pure helpers behind the MCP server sheets: the state sentence and the
// duration conversions both sheets share, and the label/value rows the
// read-only view sheet shows in place of the edit sheet's form fields.

import { mcpMaxProtocolVersionLabel } from "@/lib/constants/mcp";
import type { MCPClientAccessGrantLabels } from "@/lib/registries/mcpClientAccessGrants";
import type { CoreConfig } from "@/lib/types/config";
import type { MCPClient, MCPClientConfig, MCPClientTool } from "@/lib/types/mcp";
import type { MCPSessionKind, MCPSessionRow, MCPSessionStatus } from "@/lib/types/mcpSessions";
import {
	failureStageLabel,
	foldInstanceStates,
	formatDurationSince,
	formatFailureTiming,
	stateReasonTitle,
} from "@/lib/utils/mcpConnectionFailure";
import { perUserDiscoveryOn, supportsPerUserDiscovery } from "@/lib/utils/mcpPerUserDiscovery";
import { authTypeLabel, connectionTypeLabel } from "./mcpClientsTable.utils";

/** One label/value row of the view sheet; key is stable and names the row's test id. */
export interface MCPClientDetailRow {
	key: string;
	label: string;
	value?: string;
	note?: string;
	hint?: string;
	chips?: string[];
}

/** The global settings a server's unset overrides fall back to, in the units the view shows. */
export interface MCPClientGlobalSettings {
	toolSyncIntervalMinutes: number;
	maxProtocolVersion: string;
	toolExecutionTimeoutSeconds: number;
	maxInstructionsLength: number;
}

/** mcpGlobalSettings reads the global settings from the core config, each falling back to the server's own default. */
export function mcpGlobalSettings(
	clientConfig?: Pick<
		CoreConfig,
		"mcp_tool_sync_interval" | "mcp_max_protocol_version" | "mcp_tool_execution_timeout" | "mcp_max_instructions_per_client"
	>,
): MCPClientGlobalSettings {
	return {
		// Zero tells the server to use its built-in interval.
		toolSyncIntervalMinutes: clientConfig?.mcp_tool_sync_interval || 10,
		maxProtocolVersion: clientConfig?.mcp_max_protocol_version || "auto",
		toolExecutionTimeoutSeconds: clientConfig?.mcp_tool_execution_timeout ?? 30,
		maxInstructionsLength: clientConfig?.mcp_max_instructions_per_client || 4096,
	};
}

// stateDescription is the sentence under the sheet title. The pending and
// needs_reauth prose is unchanged; unstable and degraded gain a sentence
// naming what Bifrost's last check ran into, and needs_reauth appends the
// recorded reason so the explanation sits next to the instruction.
export function stateDescription(mcpClient: MCPClient, isPerUserAuth: boolean): string {
	const failure = mcpClient.last_failure;
	if (perUserDiscoveryOn(mcpClient.config)) {
		const perUserDescription = perUserDiscoveryStateDescription(mcpClient);
		if (perUserDescription) return perUserDescription;
	}
	switch (mcpClient.state) {
		case "pending_verification":
			return mcpClient.config.auth_type === "token_exchange"
				? i18n.t("registry.sheet.statePendingTokenExchange", { ns: "mcp" })
				: mcpClient.config.auth_type === "per_user_oauth"
					? i18n.t("registry.sheet.statePendingPerUserOauth", { ns: "mcp" })
					: i18n.t("registry.sheet.statePendingOauth", { ns: "mcp" });
		case "needs_reauth": {
			// Each auth type names the repair action the actions menu actually
			// offers for it, matching the credential block below.
			const action =
				mcpClient.config.auth_type === "token_exchange"
					? i18n.t("registry.actions.reverifyAsMe", { ns: "mcp" })
					: isPerUserAuth
						? i18n.t("registry.actions.refreshAdminCredential", { ns: "mcp" })
						: i18n.t("registry.actions.reauthorize", { ns: "mcp" });
			const key =
				mcpClient.config.auth_type === "token_exchange"
					? "registry.sheet.stateNeedsReauthTokenExchange"
					: isPerUserAuth
						? "registry.sheet.stateNeedsReauthPerUser"
						: "registry.sheet.stateNeedsReauthShared";
			const base = i18n.t(key, { ns: "mcp", action });
			return failure ? `${base}${i18n.t("registry.sheet.reasonPrefix", { ns: "mcp", message: failure.message })}` : base;
		}
		case "unstable":
			return failure
				? i18n.t("registry.sheet.stateUnstableTimed", {
						ns: "mcp",
						time: formatDurationSince(failure.at),
						stage: failureStageLabel(failure.stage).toLowerCase(),
					})
				: i18n.t("registry.sheet.stateUnstable", { ns: "mcp" });
		case "degraded":
			return i18n.t("registry.sheet.stateDegraded", { ns: "mcp" });
		default:
			return i18n.t("registry.sheet.stateDefault", { ns: "mcp" });
	}
}

// perUserDiscoveryStateDescription is the sentence for a server with
// per_user_discovery on while it is pending or healthy: its admin step only
// verifies the server, and each caller discovers tools with their own
// credential. Other states keep their general wording.
function perUserDiscoveryStateDescription(mcpClient: MCPClient): string | undefined {
	switch (mcpClient.state) {
		case "pending_verification":
			switch (mcpClient.config.auth_type) {
				case "token_exchange":
					return i18n.t("registry.view.pendingTokenExchangePerUserDiscovery", { ns: "mcp" });
				case "per_user_oauth":
					return i18n.t("registry.view.pendingOauthPerUserDiscovery", { ns: "mcp" });
				default:
					return i18n.t("registry.view.pendingHeadersPerUserDiscovery", { ns: "mcp" });
			}
		case "healthy":
			return i18n.t("registry.view.healthyPerUserDiscovery", { ns: "mcp" });
		default:
			return undefined;
	}
}

/** API sends tool_sync_interval as nanoseconds (Go time.Duration). Normalize to minutes for form/store. */
export function toolSyncIntervalToMinutes(v: number | undefined | null): number {
	if (v === undefined || v === null) return 0;
	const n = Number(v);
	if (Number.isNaN(n)) return 0;
	if (Math.abs(n) >= 1e9) return Math.round(n / 6e10);
	return n;
}

/** API sends tool_execution_timeout as a Go duration string e.g. "30s". Normalize to whole seconds for form. */
export function toolExecutionTimeoutToSeconds(v: string | number | undefined | null): number {
	if (v === undefined || v === null || v === "") return 0;
	if (typeof v === "number") return v;
	// Parse Go duration string: "30s", "1m30s", "2h", etc.
	let total = 0;
	const re = /(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)/g;
	let match;
	while ((match = re.exec(v)) !== null) {
		const n = parseFloat(match[1]);
		switch (match[2]) {
			case "ns":
				total += n / 1e9;
				break;
			case "us":
			case "µs":
				total += n / 1e6;
				break;
			case "ms":
				total += n / 1e3;
				break;
			case "s":
				total += n;
				break;
			case "m":
				total += n * 60;
				break;
			case "h":
				total += n * 3600;
				break;
		}
	}
	return Math.ceil(total);
}

/**
 * connectionTarget is what a server reaches: the command line of a stdio
 * server, the API an openapi server calls, or the URL (or its env or vault
 * reference) of an http or sse server.
 */
export function connectionTarget(config: MCPClientConfig): string {
	switch (config.connection_type) {
		case "stdio":
			return `${config.stdio_config?.command ?? ""} ${(config.stdio_config?.args ?? []).join(" ")}`.trim() || "-";
		case "openapi":
			return config.openapi_config?.base_url || i18n.t("registry.sheet.baseUrlFromDocument", { ns: "mcp" });
		default:
			return config.connection_string?.type === "env" || config.connection_string?.type === "vault"
				? config.connection_string.ref || "-"
				: config.connection_string?.value || "-";
	}
}

/**
 * toolAccess reads one tool's switches from the saved config: enabled by
 * name or by "*", and auto-executed only while enabled, the rule the edit
 * sheet and the table's counts both apply.
 */
export function toolAccess(config: Pick<MCPClientConfig, "tools_to_execute" | "tools_to_auto_execute">, toolName: string) {
	const enabled = !!config.tools_to_execute?.some((name) => name === "*" || name === toolName);
	const autoExecute = enabled && !!config.tools_to_auto_execute?.some((name) => name === "*" || name === toolName);
	return { enabled, autoExecute };
}

const PER_USER_AUTH_TYPES = new Set(["per_user_oauth", "per_user_headers", "token_exchange"]);

const onOff = (on: boolean) => (on ? i18n.t("registry.view.on", { ns: "mcp" }) : i18n.t("registry.view.off", { ns: "mcp" }));
const listRow = (key: string, label: string, items?: string[]): MCPClientDetailRow =>
	items && items.length > 0 ? { key, label, chips: items } : { key, label, value: i18n.t("registry.view.none", { ns: "mcp" }) };
const notSet = () => i18n.t("registry.view.notSet", { ns: "mcp" });

/**
 * settingsRows are the Configuration rows of the view sheet. A row exists only
 * where the edit sheet shows the field for this server, and an override left
 * unset shows the global value it falls back to, marked as the global default.
 */
export function settingsRows(config: MCPClientConfig, globals: MCPClientGlobalSettings): MCPClientDetailRow[] {
	const perUserDiscovery = perUserDiscoveryOn(config);
	const withDefault = (key: string, label: string, own: string | undefined, fallback: string): MCPClientDetailRow =>
		own ? { key, label, value: own } : { key, label, value: fallback, note: i18n.t("registry.view.globalDefault", { ns: "mcp" }) };
	const minutes = (value: number) => i18n.t("registry.view.minutes", { ns: "mcp", value });
	const seconds = (value: number) => i18n.t("registry.view.seconds", { ns: "mcp", value });
	const bytes = (count: number) => i18n.t("registry.view.bytes", { ns: "mcp", count, value: count.toLocaleString(getLocale()) });
	const syncMinutes = toolSyncIntervalToMinutes(config.tool_sync_interval);
	const timeoutSeconds = toolExecutionTimeoutToSeconds(config.tool_execution_timeout);
	const syncLabel = i18n.t("registry.view.toolSyncInterval", { ns: "mcp" });

	// Unset reads as on, as the edit form defaults it.
	const rows: MCPClientDetailRow[] = [
		{ key: "ping", label: i18n.t("registry.view.pingHealthCheck", { ns: "mcp" }), value: onOff(config.is_ping_available !== false) },
	];
	if (config.connection_type === "http" && !PER_USER_AUTH_TYPES.has(config.auth_type ?? "")) {
		rows.push({
			key: "session_stickiness",
			label: i18n.t("registry.view.persistentConnection", { ns: "mcp" }),
			value: onOff(config.needs_session_stickiness === true),
		});
	}
	if (supportsPerUserDiscovery(config.auth_type, config.connection_type)) {
		rows.push({
			key: "per_user_discovery",
			label: i18n.t("registry.form.perUserDiscovery", { ns: "mcp" }),
			value: onOff(perUserDiscovery),
			hint: perUserDiscovery
				? i18n.t("registry.view.perUserDiscoveryOnHint", { ns: "mcp" })
				: i18n.t("registry.view.perUserDiscoveryOffHint", { ns: "mcp" }),
		});
	}
	rows.push(
		perUserDiscovery
			? {
					key: "tool_sync_interval",
					label: syncLabel,
					value: i18n.t("registry.view.notUsed", { ns: "mcp" }),
					hint: i18n.t("registry.view.toolSyncNotUsedHint", { ns: "mcp" }),
				}
			: withDefault(
					"tool_sync_interval",
					syncLabel,
					syncMinutes ? minutes(syncMinutes) : undefined,
					minutes(globals.toolSyncIntervalMinutes),
				),
	);
	if (config.connection_type !== "openapi") {
		rows.push(
			withDefault(
				"max_protocol_version",
				i18n.t("registry.view.maxProtocolVersion", { ns: "mcp" }),
				config.max_protocol_version ? mcpMaxProtocolVersionLabel(config.max_protocol_version) : undefined,
				mcpMaxProtocolVersionLabel(globals.maxProtocolVersion),
			),
		);
	}
	rows.push(
		withDefault(
			"tool_execution_timeout",
			i18n.t("registry.view.toolExecutionTimeout", { ns: "mcp" }),
			timeoutSeconds ? seconds(timeoutSeconds) : undefined,
			seconds(globals.toolExecutionTimeoutSeconds),
		),
		withDefault(
			"max_instructions_length",
			i18n.t("registry.view.maxInstructionLength", { ns: "mcp" }),
			config.max_instructions_length ? bytes(config.max_instructions_length) : undefined,
			bytes(globals.maxInstructionsLength),
		),
	);
	return rows;
}

/**
 * authRows are the Authentication rows of the view sheet. Header values and
 * credentials are never shown: headers by name, a CA certificate as set or
 * not, and anything the API redacts stays out entirely.
 */
export function authRows(config: MCPClientConfig, scopeLabel: string): MCPClientDetailRow[] {
	const extraHeadersLabel = i18n.t("registry.view.allowedExtraHeaders", { ns: "mcp" });
	const rows: MCPClientDetailRow[] = [
		{
			key: "auth_type",
			label: i18n.t("registry.view.authType", { ns: "mcp" }),
			value: `${authTypeLabel(config.auth_type)} · ${scopeLabel}`,
		},
		listRow("headers", i18n.t("registry.view.headers", { ns: "mcp" }), Object.keys(config.headers ?? {})),
	];
	if (config.auth_type === "per_user_headers") {
		rows.push(listRow("per_user_header_keys", i18n.t("registry.view.requiredHeaders", { ns: "mcp" }), config.per_user_header_keys));
	}
	rows.push(
		config.allowed_extra_headers?.includes("*")
			? { key: "allowed_extra_headers", label: extraHeadersLabel, value: i18n.t("registry.view.allHeadersAllowed", { ns: "mcp" }) }
			: listRow("allowed_extra_headers", extraHeadersLabel, config.allowed_extra_headers),
	);
	if (config.auth_type === "oauth" || config.auth_type === "per_user_oauth") {
		rows.push(
			{
				key: "oauth_authorize_url",
				label: i18n.t("registry.sheet.authorizationUrl", { ns: "mcp" }),
				value: config.oauth_authorize_url || notSet(),
			},
			{ key: "oauth_token_url", label: i18n.t("registry.sheet.tokenUrl", { ns: "mcp" }), value: config.oauth_token_url || notSet() },
		);
		if (config.oauth_registration_url) {
			rows.push({
				key: "oauth_registration_url",
				label: i18n.t("registry.sheet.registrationUrl", { ns: "mcp" }),
				value: config.oauth_registration_url,
			});
		}
		rows.push(listRow("oauth_scopes", i18n.t("sessions.scopes", { ns: "mcp" }), config.oauth_scopes));
		if (config.oauth_resource) {
			rows.push({ key: "oauth_resource", label: i18n.t("registry.form.resource", { ns: "mcp" }), value: config.oauth_resource });
		}
	}
	if (config.auth_type === "token_exchange") {
		const te = config.token_exchange;
		// Same rule as exchangesAsSSOApplication: id_jag always exchanges as the SSO login application.
		const asSSOApplication = !!te?.use_idp_credentials || te?.profile === "id_jag";
		rows.push(
			{ key: "token_exchange_audience", label: i18n.t("registry.form.audience", { ns: "mcp" }), value: te?.audience || notSet() },
			{
				key: "token_exchange_application",
				label: i18n.t("registry.form.exchangeApplication", { ns: "mcp" }),
				value: asSSOApplication
					? i18n.t("registry.tokenExchange.idpApplication", { ns: "mcp" })
					: i18n.t("registry.tokenExchange.dedicatedApplication", { ns: "mcp" }),
			},
			{
				key: "token_exchange_server",
				label: i18n.t("registry.view.authorizationServer", { ns: "mcp" }),
				value: te?.authorization_server_url || i18n.t("registry.view.ssoLoginIssuer", { ns: "mcp" }),
			},
			listRow("token_exchange_scopes", i18n.t("sessions.scopes", { ns: "mcp" }), te?.scopes),
		);
		if (te?.profile === "id_jag") {
			rows.push({
				key: "token_exchange_profile",
				label: i18n.t("registry.view.profile", { ns: "mcp" }),
				value: i18n.t("registry.tokenExchange.profileIdJag", { ns: "mcp" }),
			});
		}
	}
	if (config.connection_type === "openapi") {
		const schemes = Object.keys(config.openapi_config?.security_credentials ?? {});
		if (schemes.length > 0) {
			rows.push({ key: "openapi_credentials", label: i18n.t("registry.view.apiCredentials", { ns: "mcp" }), chips: schemes });
		}
	}
	if (config.connection_type === "http" || config.connection_type === "sse") {
		const ca = config.tls_config?.ca_cert_pem;
		rows.push(
			{
				key: "tls_verification",
				label: i18n.t("registry.view.tlsVerification", { ns: "mcp" }),
				value: config.tls_config?.insecure_skip_verify ? i18n.t("registry.view.skipped", { ns: "mcp" }) : onOff(true),
			},
			{
				key: "tls_ca",
				label: i18n.t("registry.view.customCaCertificate", { ns: "mcp" }),
				value:
					ca?.value?.trim() || ca?.ref?.trim() ? i18n.t("registry.view.set", { ns: "mcp" }) : i18n.t("registry.view.none", { ns: "mcp" }),
			},
		);
	}
	return rows;
}

export type MCPClientBadgeVariant = "default" | "secondary" | "outline" | "success" | "destructive";

/** heroChips sum a server up at the top of the view: how it connects, how it signs in, and the modes that change its behaviour. */
export function heroChips(config: MCPClientConfig, scopeLabel: string): { label: string; variant: MCPClientBadgeVariant }[] {
	const authed = !!config.auth_type && config.auth_type !== "none";
	const chips: { label: string; variant: MCPClientBadgeVariant }[] = [
		{ label: connectionTypeLabel(config.connection_type), variant: "outline" },
		{
			label: authed ? `${authTypeLabel(config.auth_type)} · ${scopeLabel}` : i18n.t("library.auth.noAuth", { ns: "mcp" }),
			variant: "outline",
		},
	];
	if (perUserDiscoveryOn(config)) chips.push({ label: i18n.t("registry.form.perUserDiscovery", { ns: "mcp" }), variant: "default" });
	if (config.is_code_mode_client) chips.push({ label: i18n.t("registry.view.codeMode", { ns: "mcp" }), variant: "secondary" });
	return chips;
}

/** One identifier row at the top of the view; key is stable and names the row's test id. */
export interface MCPClientHeroRow {
	key: "endpoint" | "spec" | "calls" | "command" | "url";
	label: string;
	value: string;
	copy?: string;
}

/**
 * heroRows are the identifiers at the top of the view: the endpoint callers
 * connect to, and what the server itself reaches. copy is the text the copy
 * button puts on the clipboard, the full URL for the endpoint.
 */
export function heroRows(config: MCPClientConfig, baseUrl: string, specSummary: string): MCPClientHeroRow[] {
	const rows: MCPClientHeroRow[] = [];
	if (config.endpoint_slug) {
		rows.push({
			key: "endpoint",
			label: i18n.t("registry.columns.endpoint", { ns: "mcp" }),
			value: `/mcp/${config.endpoint_slug}`,
			copy: `${baseUrl}/mcp/${config.endpoint_slug}`,
		});
	}
	const target = connectionTarget(config);
	const copyable = target === "-" ? {} : { copy: target };
	switch (config.connection_type) {
		case "openapi":
			rows.push({ key: "spec", label: i18n.t("registry.view.spec", { ns: "mcp" }), value: specSummary });
			rows.push({
				key: "calls",
				label: i18n.t("registry.view.calls", { ns: "mcp" }),
				value: target,
				...(config.openapi_config?.base_url ? { copy: target } : {}),
			});
			break;
		case "stdio":
			rows.push({ key: "command", label: i18n.t("common.command", { ns: "mcp" }), value: target, ...copyable });
			break;
		default:
			rows.push({ key: "url", label: "URL", value: target, ...copyable });
	}
	return rows;
}

/**
 * toolStat is the Tools figure at the top of the view. Counts cover only the
 * discovered tools: nothing prunes a name from the config when the server
 * stops listing it, the same rule the servers table follows.
 */
export function toolStat(client: Pick<MCPClient, "config" | "tools" | "state">): { value: string; sub: string } {
	const tools = client.tools ?? [];
	if (perUserDiscoveryOn(client.config)) {
		return tools.length > 0
			? {
					value: i18n.t("registry.view.toolsSeen", { ns: "mcp", count: tools.length }),
					sub: i18n.t("registry.view.inCallersDiscoveries", { ns: "mcp" }),
				}
			: { value: i18n.t("registry.view.noneSeen", { ns: "mcp" }), sub: i18n.t("registry.view.noCallerConnected", { ns: "mcp" }) };
	}
	if (tools.length === 0) {
		return client.state === "pending_verification"
			? { value: i18n.t("registry.view.noneYet", { ns: "mcp" }), sub: i18n.t("registry.view.discoveredAfterVerification", { ns: "mcp" }) }
			: { value: i18n.t("registry.view.none", { ns: "mcp" }), sub: i18n.t("registry.view.serverListsNoTools", { ns: "mcp" }) };
	}
	const access = tools.map((t) => toolAccess(client.config, t.name));
	const enabled = access.filter((a) => a.enabled).length;
	const auto = access.filter((a) => a.autoExecute).length;
	return {
		value: i18n.t("registry.view.toolsEnabled", { ns: "mcp", enabled, count: tools.length }),
		sub:
			auto > 0
				? i18n.t("registry.view.autoExecuteCount", { ns: "mcp", count: auto })
				: i18n.t("registry.view.noneAutoExecute", { ns: "mcp" }),
	};
}

/** MCPClientAccessGrantCount is a kind of grant a build adds to the Access figure, counted and worded by the build. */
export interface MCPClientAccessGrantCount {
	count: number | undefined; // undefined while the build is still counting them
	labels: MCPClientAccessGrantLabels;
}

/**
 * accessStat is the Access figure at the top of the view: what reaches the
 * server besides its own endpoint. Grants a build adds are counted beside the
 * keys, which then read as "keys" to keep the figure short.
 */
export function accessStat(
	config: MCPClientConfig,
	virtualKeys: number,
	virtualMcps: number,
	grants?: MCPClientAccessGrantCount,
): { value: string; sub: string } {
	const keys = i18n.t("registry.view.virtualKeyCount", { ns: "mcp", count: virtualKeys });
	const vmcps = i18n.t("registry.view.virtualMcpCount", { ns: "mcp", count: virtualMcps });
	// Until the grants are counted, no conclusion about who has access is drawn.
	if (grants && grants.count === undefined) {
		const loading = grants.labels.loading;
		return config.allow_by_default
			? { value: i18n.t("registry.filter.allowedByDefault", { ns: "mcp" }), sub: loading }
			: { value: "…", sub: loading };
	}
	const granted = grants?.count ? grants.labels.count(grants.count) : "";
	if (config.allow_by_default) {
		const extras = [virtualKeys > 0 ? keys : "", granted, virtualMcps > 0 ? vmcps : ""].filter(Boolean);
		const none = grants ? grants.labels.noOverrides : i18n.t("registry.view.noKeyOverrides", { ns: "mcp" });
		return {
			value: i18n.t("registry.filter.allowedByDefault", { ns: "mcp" }),
			sub: extras.length > 0 ? i18n.t("registry.view.andAlso", { ns: "mcp", items: extras.join(" · ") }) : none,
		};
	}
	let value = virtualKeys > 0 ? keys : i18n.t("registry.view.noAccess", { ns: "mcp" });
	if (granted) {
		value = virtualKeys > 0 ? `${i18n.t("registry.view.keyCount", { ns: "mcp", count: virtualKeys })} · ${granted}` : granted;
	}
	return {
		value,
		sub: [
			virtualMcps > 0 ? i18n.t("registry.view.andAlso", { ns: "mcp", items: vmcps }) : "",
			i18n.t("registry.view.notAllowedByDefault", { ns: "mcp" }),
		]
			.filter(Boolean)
			.join(" · "),
	};
}

/** accessExplanation is the sentence beside Allow by Default on the Access tab: who may reach the server, in the build's own words when it registers grants. */
export function accessExplanation(allowByDefault: boolean, grantLabels?: MCPClientAccessGrantLabels): string {
	if (allowByDefault) {
		return i18n.t("registry.view.allowByDefaultOnDesc", { ns: "mcp" });
	}
	return grantLabels ? grantLabels.restrictedExplanation : i18n.t("registry.view.allowByDefaultOffDesc", { ns: "mcp" });
}

/**
 * toolHints are the behaviour hints a server declares on a tool. Only hints
 * set to true are shown: the spec's defaults would otherwise mark every
 * unannotated tool destructive and open-world. destructiveHint means nothing
 * on a read-only tool, so it is dropped there.
 */
export function toolHints(annotations: MCPClientTool["annotations"]): { label: string; tone: "read" | "danger" | "plain" }[] {
	if (!annotations) return [];
	const hints: { label: string; tone: "read" | "danger" | "plain" }[] = [];
	if (annotations.readOnlyHint === true) hints.push({ label: i18n.t("registry.view.readOnly", { ns: "mcp" }), tone: "read" });
	if (annotations.destructiveHint === true && annotations.readOnlyHint !== true) {
		hints.push({ label: i18n.t("registry.view.destructive", { ns: "mcp" }), tone: "danger" });
	}
	if (annotations.idempotentHint === true) hints.push({ label: i18n.t("registry.view.idempotent", { ns: "mcp" }), tone: "plain" });
	if (annotations.openWorldHint === true) hints.push({ label: i18n.t("registry.view.openWorld", { ns: "mcp" }), tone: "plain" });
	return hints;
}

function schemaTypeLabel(schema: unknown): string {
	if (!schema || typeof schema !== "object") return "any";
	const s = schema as Record<string, unknown>;
	if (Array.isArray(s.enum) && s.enum.length > 0) return s.enum.map(String).join(" | ");
	if (Array.isArray(s.type)) return s.type.map(String).join(" | ");
	if (s.type === "array") return `${schemaTypeLabel(s.items)}[]`;
	if (typeof s.type === "string") return s.type;
	const union = s.anyOf ?? s.oneOf;
	if (Array.isArray(union)) return union.map(schemaTypeLabel).join(" | ");
	return s.properties ? "object" : "any";
}

function schemaProperties(schema: unknown): [string, Record<string, unknown>][] {
	if (!schema || typeof schema !== "object") return [];
	const properties = (schema as { properties?: Record<string, unknown> }).properties ?? {};
	return Object.entries(properties).map(([name, value]) => [name, (value ?? {}) as Record<string, unknown>]);
}

/** schemaParams lists a tool's input schema as rows: name, a short type, whether it is required, and its description. */
export function schemaParams(schema: unknown): { name: string; type: string; required: boolean; description: string }[] {
	const required = new Set(((schema as { required?: string[] } | undefined)?.required ?? []).map(String));
	return schemaProperties(schema).map(([name, prop]) => ({
		name,
		type: schemaTypeLabel(prop),
		required: required.has(name),
		description: typeof prop.description === "string" ? prop.description : "",
	}));
}

/** schemaSummary is a tool's output schema on one line, its fields with their types. */
export function schemaSummary(schema: unknown): string {
	return schemaProperties(schema)
		.map(([name, prop]) => `${name}: ${schemaTypeLabel(prop)}`)
		.join(" · ");
}

export type MCPClientAlertActionKind = "authorize" | "reauthorize" | "refresh_headers" | "verify_exchange" | "reconnect";

export interface MCPClientAlert {
	variant: "default" | "destructive" | "warning" | "info";
	title: string;
	lines: string[];
	action?: { kind: MCPClientAlertActionKind; label: string };
}

// repairAction is the actions-menu item that repairs a needs_reauth server's
// credential, with the menu's own label. A server with per_user_discovery on
// keeps no credential of its own to repair.
function repairAction(config: MCPClientConfig): MCPClientAlert["action"] {
	if (perUserDiscoveryOn(config)) return undefined;
	switch (config.auth_type) {
		case "oauth":
			return { kind: "reauthorize", label: i18n.t("registry.actions.reauthorize", { ns: "mcp" }) };
		case "per_user_oauth":
			return { kind: "reauthorize", label: i18n.t("registry.actions.refreshAdminCredential", { ns: "mcp" }) };
		case "per_user_headers":
			return { kind: "refresh_headers", label: i18n.t("registry.actions.refreshAdminCredential", { ns: "mcp" }) };
		case "token_exchange":
			return { kind: "verify_exchange", label: i18n.t("registry.actions.reverifyAsMe", { ns: "mcp" }) };
		default:
			return undefined;
	}
}

function failureLine(failure: NonNullable<MCPClient["last_failure"]>): string {
	return i18n.t("registry.view.failureLine", { ns: "mcp", stage: failureStageLabel(failure.stage), message: failure.message });
}

/**
 * alertFor is the alert the view shows above everything else when a server is
 * not healthy: what is wrong, what Bifrost last saw, and at most one action,
 * the same one the actions menu offers for that state. canReconnect is
 * canReconnectMCPClient for the server.
 */
export function alertFor(client: MCPClient, canReconnect: boolean): MCPClientAlert | null {
	const failure = client.last_failure;
	const reconnect: MCPClientAlert["action"] =
		canReconnect && !client.config.disabled ? { kind: "reconnect", label: i18n.t("registry.actions.reconnect", { ns: "mcp" }) } : undefined;
	const failureLines = failure ? [failureLine(failure), formatFailureTiming(failure, client.state)] : [];
	switch (client.state) {
		case "pending_verification":
			return {
				variant: "warning",
				title: i18n.t("registry.view.verificationRequired", { ns: "mcp" }),
				lines: [stateDescription(client, PER_USER_AUTH_TYPES.has(client.config.auth_type ?? ""))],
				action: { kind: "authorize", label: i18n.t("registry.actions.authorize", { ns: "mcp" }) },
			};
		case "needs_reauth":
			return {
				variant: "destructive",
				title: stateReasonTitle("needs_reauth", false),
				lines: [
					PER_USER_AUTH_TYPES.has(client.config.auth_type ?? "")
						? i18n.t("registry.view.adminCredentialNeedsRepair", { ns: "mcp" })
						: i18n.t("registry.view.credentialsNeedReauth", { ns: "mcp" }),
					...failureLines,
				],
				action: repairAction(client.config),
			};
		case "unstable":
			return {
				variant: "warning",
				title: stateReasonTitle("unstable", !!client.node_states),
				lines: [...failureLines, i18n.t("registry.view.unstableNote", { ns: "mcp" })],
				action: reconnect,
			};
		case "degraded": {
			const groups = client.node_states ? foldInstanceStates(client.node_states) : [];
			return {
				variant: "info",
				title: stateReasonTitle("degraded", groups.length > 0),
				lines: [
					...groups.map((g) => {
						const state = g.state.replace(/_/g, " ");
						return g.last_failure
							? i18n.t("registry.view.instanceStateFailure", {
									ns: "mcp",
									count: g.count,
									state,
									failure: failureLine(g.last_failure),
									timing: formatFailureTiming(g.last_failure, g.state).toLowerCase(),
								})
							: i18n.t("registry.view.instanceState", { ns: "mcp", count: g.count, state });
					}),
					i18n.t("registry.view.degradedNote", { ns: "mcp" }),
				],
				action: reconnect,
			};
		}
		case "error":
			return { variant: "destructive", title: stateReasonTitle("error", false), lines: failureLines, action: reconnect };
		case "disabled":
			return {
				variant: "default",
				title: i18n.t("common.disabled", { ns: "mcp" }),
				lines: [i18n.t("registry.view.disabledBody", { ns: "mcp" })],
			};
		default:
			return null;
	}
}

/** allowedToolsBadges read a tool allow-list the way the virtual key sheet shows one. */
export function allowedToolsBadges(tools: string[]): { label: string; variant: MCPClientBadgeVariant }[] {
	if (tools.includes("*")) return [{ label: i18n.t("registry.view.allTools", { ns: "mcp" }), variant: "success" }];
	if (tools.length === 0) return [{ label: i18n.t("registry.view.noToolsDenyAll", { ns: "mcp" }), variant: "destructive" }];
	return tools.map((tool) => ({ label: tool, variant: "secondary" }));
}

/** sessionKinds are the session rows a server's auth type stores per caller: tokens or header values, plus pending sign-ins. */
export function sessionKinds(authType: MCPClientConfig["auth_type"]): MCPSessionKind[] {
	switch (authType) {
		case "per_user_oauth":
			return ["token", "flow"];
		case "per_user_headers":
			return ["header", "flow"];
		default:
			return [];
	}
}

/** callerStatuses are the statuses a server's caller rows can have, with the labels the Auth Sessions page uses. */
export function callerStatuses(authType: MCPClientConfig["auth_type"]): { status: MCPSessionStatus; label: string }[] {
	return authType === "per_user_headers"
		? [
				{ status: "active", label: i18n.t("sessions.status.active", { ns: "mcp" }) },
				{ status: "needs_update", label: i18n.t("sessions.status.needsUpdate", { ns: "mcp" }) },
				{ status: "pending", label: i18n.t("sessions.status.pending", { ns: "mcp" }) },
				{ status: "orphaned", label: i18n.t("sessions.status.orphaned", { ns: "mcp" }) },
			]
		: [
				{ status: "active", label: i18n.t("sessions.status.active", { ns: "mcp" }) },
				{ status: "needs_reauth", label: i18n.t("sessions.status.needsReauth", { ns: "mcp" }) },
				{ status: "pending", label: i18n.t("sessions.status.pending", { ns: "mcp" }) },
				{ status: "orphaned", label: i18n.t("sessions.status.orphaned", { ns: "mcp" }) },
			];
}

/** callersSummary is the Callers figure at the top of the view: every stored row, and how many need the caller to act, once the counts are ready. A count still loading or that failed says so rather than reading as no callers. */
export function callersSummary(
	counts: Partial<Record<MCPSessionStatus, number>>,
	state: CallersState,
): { total?: number; value: string; sub: string } {
	if (state === "loading") return { value: "…", sub: i18n.t("registry.view.loadingCallersShort", { ns: "mcp" }) };
	if (state === "error") return { value: "-", sub: i18n.t("registry.view.callersLoadFailedShort", { ns: "mcp" }) };
	const total = Object.values(counts).reduce((sum, n) => sum + (n ?? 0), 0);
	if (total === 0) return { total, value: "0", sub: i18n.t("registry.view.noSessionsYetShort", { ns: "mcp" }) };
	const attention = (counts.needs_reauth ?? 0) + (counts.needs_update ?? 0) + (counts.pending ?? 0);
	const active = counts.active ?? 0;
	return {
		total,
		value: String(total),
		sub:
			attention > 0
				? i18n.t("registry.view.activeNeedAttention", { ns: "mcp", active, count: attention })
				: i18n.t("registry.view.activeCount", { ns: "mcp", count: active }),
	};
}

/** CallersState is where the Callers queries stand: a figure is shown only once they are all ready. */
export type CallersState = "loading" | "error" | "ready";

/**
 * callersState folds the Callers queries into one state: failed if any failed, loading while any has
 * no result for the arguments it was last asked with. RTK Query keeps the previous arguments' result in
 * data while it fetches, so currentData is what is read: another filter's or server's rows never show.
 */
export function callersState(queries: { currentData?: unknown; isError: boolean }[]): CallersState {
	if (queries.some((q) => q.isError)) return "error";
	if (queries.some((q) => q.currentData === undefined)) return "loading";
	return "ready";
}

/** bindingLabel is who a caller row belongs to, the way the Auth Sessions page names it. */
export function bindingLabel(row: MCPSessionRow): { name: string; kind: string } {
	switch (row.auth_mode) {
		case "vk":
			return { name: row.virtual_key?.name || row.virtual_key?.id || "-", kind: i18n.t("sessions.filter.virtualKeyOption", { ns: "mcp" }) };
		case "session":
			return { name: row.session_id || "-", kind: i18n.t("sessions.filter.session", { ns: "mcp" }) };
		default:
			return { name: row.user?.name || row.user?.email || row.user_id || "-", kind: i18n.t("sessions.filter.user", { ns: "mcp" }) };
	}
}