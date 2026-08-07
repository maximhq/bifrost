import { MCPClient } from "@/lib/types/mcp";

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
export function canReconnectMCPClient(config: Pick<MCPClient["config"], "auth_type" | "connection_type" | "needs_session_stickiness">): boolean {
	const alwaysPerCall = config.auth_type === "per_user_oauth" || config.auth_type === "per_user_headers" || config.auth_type === "token_exchange";
	const sharedButPerCallViaStickiness = config.connection_type === "http" && config.needs_session_stickiness !== true;
	return !(alwaysPerCall || sharedButPerCallViaStickiness);
}
