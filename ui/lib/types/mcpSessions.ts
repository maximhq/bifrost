// Types for the MCP Auth Sessions tab + auth landing flow.
// Mirrors the wire shapes in transports/bifrost-http/handlers/mcp_sessions.go.

export type AuthMode = "user" | "vk" | "session";

export type MCPSessionKind = "token" | "flow";

// Status values vary by Kind:
//   token:  "active" | "orphaned" | "needs_reauth"
//     - "active":       usable credential
//     - "orphaned":     access revoked (no granting VK), credential still intact;
//                       reactivates automatically if access is regained
//     - "needs_reauth": refresh token rejected upstream; user must re-authenticate
//   flow:   "pending"
//     - "pending":      fresh auth in progress; user must complete OAuth.
//                       The list endpoint only returns pending flow rows;
//                       "authorized" / "failed" / "expired" are filtered out
//                       server-side and never reach the wire here.
export type MCPSessionStatus = "active" | "orphaned" | "pending" | "needs_reauth";

export interface MCPClientSummary {
	client_id: string;
	name: string;
}

export interface VirtualKeySummary {
	id: string;
	name: string;
}

// UserSummary: preloaded on user-keyed session and flow rows by the
// enterprise backend (joins against the SCIM users table). OSS leaves it
// absent — the UI falls back to rendering the raw user_id.
export interface UserSummary {
	id: string;
	name?: string;
	email?: string;
}

export interface MCPSessionRow {
	id: string;
	kind: MCPSessionKind;
	auth_mode: AuthMode;
	user_id?: string | null;
	user?: UserSummary | null;
	virtual_key?: VirtualKeySummary | null;
	mcp_client?: MCPClientSummary | null;
	session_id?: string | null;
	status: MCPSessionStatus;
	expires_at?: string | null;
	created_at: string;
	last_refreshed_at?: string | null;
	oauth_config_id?: string;
}

export interface MCPSessionsListResponse {
	sessions: MCPSessionRow[];
}

export interface MCPSessionReauthResponse {
	authorize_url: string;
	session_id: string;
}

export interface MCPFlowDetail {
	id: string;
	flow_mode: AuthMode;
	status: "pending" | "authorized" | "failed" | "expired";
	mcp_client?: MCPClientSummary | null;
	oauth_config_id: string;
	user_id?: string | null;
	user?: UserSummary | null;
	virtual_key?: VirtualKeySummary | null;
	session_id?: string | null;
	expires_at: string;
	created_at: string;
	// True when an active token already exists for this binding. Combined with
	// status='pending' it means OAuth was re-initiated unnecessarily — the
	// auth page should treat it as already authenticated.
	has_active_token?: boolean;
}

export interface MCPFlowStartResponse {
	authorize_url: string;
}
