import i18n from "@/lib/i18n";
import { MCPClient } from "@/lib/types/mcp";
import { perUserDiscoveryOn } from "@/lib/utils/mcpPerUserDiscovery";

/**
 * exchangeVerifyCopy is the text of the token_exchange "Verify as me" dialog
 * for client, in the selected language. A server that discovers tools per
 * caller keeps nothing from the exchange, so its text speaks of a
 * verification only, never of a discovery credential.
 */
export function exchangeVerifyCopy(
	client: { state: MCPClient["state"]; config: Pick<MCPClient["config"], "name" | "per_user_discovery"> } | null,
): {
	title: string;
	description: string;
	body: string;
	detail: string;
} {
	const pending = client?.state === "pending_verification";
	const name = client?.config.name;
	const title = i18n.t(pending ? "registry.verify.verifyAsMe" : "registry.verify.reverifyAsMe", { ns: "mcp" });
	if (perUserDiscoveryOn(client?.config)) {
		return {
			title,
			description: i18n.t("registry.verify.perUserDiscoveryTitle", { ns: "mcp" }),
			body: i18n.t("registry.verify.exchangeBodyPerUserDiscovery", { ns: "mcp", name }),
			detail: i18n.t("registry.verify.perUserDiscoveryHint", { ns: "mcp" }),
		};
	}
	return {
		title,
		description: i18n.t(pending ? "registry.verify.establishTitle" : "registry.verify.renewTitle", { ns: "mcp" }),
		body: i18n.t("registry.verify.exchangeBody", {
			ns: "mcp",
			action: i18n.t(pending ? "registry.verify.establish" : "registry.verify.renew", { ns: "mcp" }),
			name,
		}),
		detail: i18n.t(pending ? "registry.verify.pendingHint" : "registry.verify.renewHint", { ns: "mcp" }),
	};
}

/** connectionTypeLabel is how the servers table and the server view name a connection type. */
export function connectionTypeLabel(type: string): string {
	switch (type) {
		case "http":
			return "HTTP";
		case "sse":
			return "SSE";
		case "stdio":
			return "STDIO";
		case "openapi":
			return "OpenAPI";
		default:
			return type.toUpperCase();
	}
}

/** authTypeLabel names an auth type without its scope, which is shown on its own. */
export function authTypeLabel(type: string | undefined): string {
	switch (type) {
		case "none":
		case undefined:
		case "":
			return i18n.t("registry.filter.none", { ns: "mcp" });
		case "headers":
		case "per_user_headers":
			return i18n.t("registry.filter.headers", { ns: "mcp" });
		case "oauth":
		case "per_user_oauth":
			return "OAuth";
		case "token_exchange":
			return i18n.t("registry.filter.tokenExchange", { ns: "mcp" });
		default:
			return type;
	}
}

/**
 * canReconnectMCPClient reports whether the "Reconnect" action applies to
 * this client at all — mirrors core/mcp/credstore's RequiresPerCallConnection
 * exactly, so the UI never offers an action the backend rejects with
 * ErrMCPReconnectNotApplicable:
 *
 *   - per_user_oauth, per_user_headers, token_exchange never hold a shared
 *     upstream connection — auth is resolved per request/user identity.
 *   - A shared oauth/headers/none client on an http connection with
 *     needs_session_stickiness nil/false (the default for newly created
 *     clients) is also per-call: it dials fresh on every call, so there is
 *     no persistent connection to reconnect either.
 *   - Every other combination (any non-http connection type, or an http
 *     shared client with needs_session_stickiness === true) holds a real
 *     persistent connection Reconnect can act on.
 */
export function canReconnectMCPClient(
	config: Pick<MCPClient["config"], "auth_type" | "connection_type" | "needs_session_stickiness">,
): boolean {
	const alwaysPerCall =
		config.auth_type === "per_user_oauth" || config.auth_type === "per_user_headers" || config.auth_type === "token_exchange";
	const sharedButPerCallViaStickiness = config.connection_type === "http" && config.needs_session_stickiness !== true;
	return !(alwaysPerCall || sharedButPerCallViaStickiness);
}