// Types for the MCP Auth Sessions tab + auth landing flow.
// Mirrors the wire shapes in transports/bifrost-http/handlers/mcp_sessions.go.

export type AuthMode = "user" | "vk" | "session";

export type MCPSessionKind = "token" | "flow" | "header";

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
//   header: "active" | "orphaned" | "needs_update"
//     - "active":       caller-submitted headers, usable
//     - "orphaned":     parallel of OAuth orphaned (lost MCP access via VK
//                       reassignment); reactivates if access is restored
//     - "needs_update": admin changed the PerUserHeaderKeys schema; caller
//                       must resubmit their values
export type MCPSessionStatus = "active" | "orphaned" | "pending" | "needs_reauth" | "needs_update";

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
	// auth_kind disambiguates OAuth vs Headers within "flow" rows so the UI
	// routes Complete-authentication to the correct landing-page kind. For
	// "token" rows it's always "oauth"; for "header" rows always "headers".
	auth_kind: "oauth" | "headers";
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
	// updated_at: header credential rows only — timestamp of the caller's
	// last submission/edit. OAuth rows omit this field.
	updated_at?: string | null;
	oauth_config_id?: string;
	// can_reauth mirrors the server-side identity gate on POST /reauth. For
	// user-bound rows it's true only when the calling user matches the row's
	// bound user; for vk/session rows it's always true. The UI hides the
	// Re-authenticate / Edit values action when false. Always false for
	// kind === "flow" rows — those are completed via /api/oauth/per-user/
	// flows/{id}/start, not /reauth, and the UI's flow branch ignores this field.
	can_reauth: boolean;
}

export interface MCPSessionsListResponse {
	sessions: MCPSessionRow[];
	// Pagination envelope mirrors the backend in handlers/mcp_sessions.go.
	// Optional on this interface so older deployments (or future callers
	// that ignore paging) still type-check.
	count?: number;
	total_count?: number;
	limit?: number;
	offset?: number;
}

// MCPSessionsQueryParams maps to the query string accepted by
// GET /api/mcp/sessions. Array fields are csv-encoded by the API layer.
// Empty/omitted values are not sent on the wire — the backend treats
// missing as "no filter" for that field.
export interface MCPSessionsQueryParams {
	q?: string;
	kind?: MCPSessionKind[];
	status?: MCPSessionStatus[];
	auth_mode?: AuthMode[];
	mcp_client_id?: string[];
	limit?: number;
	offset?: number;
}

export interface MCPSessionReauthResponse {
	// authorize_url is the URL the caller should redirect to. For OAuth rows
	// it's the upstream authorize endpoint; for header credential rows it's
	// the bifrost auth-landing page that serves the submission form.
	authorize_url: string;
	// submit_url is set on header re-auth and matches authorize_url; kept
	// for callers that want to be explicit about the underlying surface.
	submit_url?: string;
	session_id: string;
	kind?: "oauth" | "headers";
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