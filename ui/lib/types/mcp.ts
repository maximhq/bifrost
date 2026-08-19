import { Function as ToolFunction } from "./logs";
import { SecretVar } from "./schemas";

export type MCPConnectionType = "http" | "stdio" | "sse";

export type MCPConnectionState =
	| "healthy"
	| "unstable"
	| "error"
	| "pending_verification"
	| "disabled"
	| "needs_reauth"
	// Read-time aggregate value, never a single instance's own reported
	// state: multiple instances of a distributed deployment currently
	// report different states for this client. Never produced by a
	// single-instance deployment.
	| "degraded";

export type MCPAuthType = "none" | "headers" | "oauth" | "per_user_oauth" | "per_user_headers" | "token_exchange";

// Lifecycle states for a per-user MCP header credential row. Mirrors the
// status column on mcp_per_user_header_credentials.
//   - active:       caller-submitted, usable
//   - orphaned:     caller lost access via VK reassignment; auto-reactivates
//                   if access is regained
//   - needs_update: admin changed the PerUserHeaderKeys schema; caller must
//                   resubmit values
export type MCPHeadersUserCredentialStatus = "active" | "orphaned" | "needs_update";

export type { SecretVar };

export interface MCPStdioConfig {
	command: string;
	args: string[];
	envs: string[];
}

export interface MCPTLSConfig {
	insecure_skip_verify?: boolean;
	ca_cert_pem?: SecretVar;
}

// Delegated token-exchange scoping for auth_type === "token_exchange": each
// caller's identity-provider token is exchanged at runtime for a short-lived
// token scoped to this server's audience. Carries no credentials — the
// exchange endpoint and client come from the deployment's identity-provider
// integration.
export interface MCPTokenExchangeConfig {
	audience: string; // Resource identifier at the identity provider, e.g. "api://jira-mcp"
	// Identity-provider application authorized to perform exchanges for this
	// audience — typically a dedicated registration carrying the
	// token-exchange (or on-behalf-of) grant, separate from the SSO login
	// application. Required unless use_idp_credentials is true. Redacted on
	// GET.
	client_id?: SecretVar;
	client_secret?: SecretVar; // Omit for public clients. Ignored when use_idp_credentials is true. Redacted on GET.
	// When true, performs the exchange as the SSO login application itself
	// instead of client_id/client_secret above, which are then ignored. Some
	// providers require this — Microsoft Entra ID's on-behalf-of grant only
	// accepts an assertion audienced to the exchanging application, and the
	// SSO login flow always requests a token self-audienced to itself, so a
	// separate exchange application can never receive a usable one.
	use_idp_credentials?: boolean;
	// Optional scopes on the exchanged token. Include "offline_access" (where
	// the identity provider supports it) so the retained admin discovery
	// credential gets a refresh token and stays self-renewing.
	scopes?: string[];
	// Overrides where the exchange request is sent, for identity providers
	// that bind an audience to a specific authorization server distinct from
	// the one used for SSO login (e.g. Okta's per-resource Custom
	// Authorization Servers). Leave empty to use the deployment's SSO login
	// issuer, which is correct for providers with one tenant-wide token
	// endpoint (Entra, Auth0).
	authorization_server_url?: string;
}

export interface OAuthConfig {
	client_id: SecretVar;
	client_secret?: SecretVar; // Optional for public clients using PKCE
	authorize_url?: string; // Optional, will be discovered from server_url if not provided
	token_url?: string; // Optional, will be discovered from server_url if not provided
	registration_url?: string; // Optional, for dynamic client registration
	scopes?: string[]; // Optional, can be discovered
	server_url?: string; // MCP server URL for OAuth discovery (automatically set from connection_string)
	resource?: string; // Optional OAuth resource indicator; omitted when empty
}

/** OAuth fields allowed on MCP client update. Any field left unset keeps its stored value. */
export interface OAuthConfigUpdate {
	client_id?: SecretVar;
	client_secret?: SecretVar;
	authorize_url?: string;
	token_url?: string;
	registration_url?: string;
	scopes?: string[];
	resource?: string;
}

export interface MCPClientConfig {
	client_id: string; // Maps to ClientID in TableMCPClient
	name: string;
	is_code_mode_client?: boolean;
	connection_type: MCPConnectionType;
	connection_string?: SecretVar;
	stdio_config?: MCPStdioConfig;
	tls_config?: MCPTLSConfig;
	auth_type?: MCPAuthType;
	oauth_config_id?: string;
	oauth_client_id?: SecretVar; // Redacted existing client ID (populated on GET for oauth clients)
	oauth_client_secret?: SecretVar; // Redacted existing client secret (populated on GET for oauth clients)
	// Remaining oauth_config fields, populated on GET for oauth clients so the
	// full config can be reviewed and edited, not just the credentials.
	oauth_authorize_url?: string;
	oauth_token_url?: string;
	oauth_registration_url?: string;
	oauth_scopes?: string[];
	oauth_resource?: string;
	tools_to_execute?: string[];
	tools_to_auto_execute?: string[];
	headers?: Record<string, SecretVar>;
	// per_user_header_keys: admin-declared header *names* that each caller
	// must supply when auth_type === "per_user_headers". Values live per-user
	// in the credential store, not on the client config. Required (non-empty)
	// for per_user_headers auth; ignored for all other auth types.
	per_user_header_keys?: string[];
	// token_exchange-only: audience/scopes the exchanged tokens are scoped to.
	// Required (with a non-empty audience) for token_exchange auth.
	token_exchange?: MCPTokenExchangeConfig;
	is_ping_available?: boolean;
	// Only meaningful when connection_type === "http": whether this client
	// maintains one persistent connection reused across every caller (true)
	// or connects fresh per call (nil/false, the default for newly created
	// clients — pre-existing clients were backfilled to true). SSE and STDIO
	// always behave as sticky regardless of this field.
	needs_session_stickiness?: boolean;
	tool_pricing?: Record<string, number>;
	// Per-client override (0 = use global, -1 = disabled). API returns NANOSECONDS
	// (Go time.Duration), while updates send minutes — convert with
	// toolSyncIntervalToMinutes before showing or resending this value.
	tool_sync_interval?: number;
	tool_execution_timeout?: string | number; // Per-client tool execution timeout; API returns string e.g. "30s", UI sends integer seconds (0 = use global)
	allowed_extra_headers?: string[]; // Allowlist of x-bf-eh-* headers forwarded to this MCP server. ["*"] = allow all.
	allow_on_all_virtual_keys?: boolean; // When true, available to all VKs with all tools allowed by default; explicit VK config overrides this
	disabled?: boolean; // When true, connection/workers are shut down; tools are unavailable until re-enabled
}

export interface MCPVKConfigResponse {
	virtual_key_id: string;
	virtual_key_name: string;
	tools_to_execute: string[];
}

export interface MCPClient {
	config: MCPClientConfig;
	tools: ToolFunction[];
	state: MCPConnectionState;
	vk_configs: MCPVKConfigResponse[];
	// Per-instance breakdown behind `state` when it's "degraded" (instance ID
	// -> that instance's own self-reported state). Only ever present in a
	// distributed deployment; absent otherwise.
	node_states?: Record<string, string>;
}

export interface CreateMCPClientRequest {
	name: string;
	is_code_mode_client?: boolean;
	connection_type: MCPConnectionType;
	connection_string?: SecretVar;
	stdio_config?: MCPStdioConfig;
	tls_config?: MCPTLSConfig;
	auth_type?: MCPAuthType;
	oauth_config?: OAuthConfig;
	tools_to_execute?: string[];
	tools_to_auto_execute?: string[];
	headers?: Record<string, SecretVar>;
	// per_user_headers-only: admin-declared header schema (names only).
	per_user_header_keys?: string[];
	// token_exchange-only: audience/scopes the exchanged tokens are scoped to.
	// Verification runs automatically as the signed-in admin (their own
	// identity-provider token is the exchange subject); sessions without one
	// (e.g. API-key auth) create the client in pending_verification.
	token_exchange?: MCPTokenExchangeConfig;
	// per_user_headers-only: a sample set of header values supplied by the
	// admin so the server can verify upstream + discover tools in the same
	// create call. Discarded after verification (never persisted). Mirrors
	// the per-user OAuth flow where the admin's temp access token plays
	// the analogous role. Ignored for all other auth types.
	user_headers?: Record<string, string>;
	is_ping_available?: boolean;
	// Only meaningful when connection_type === "http". See MCPClientConfig's
	// field doc for the full contract.
	needs_session_stickiness?: boolean;
}

export interface OAuthFlowResponse {
	status: "pending_oauth";
	message: string;
	oauth_config_id: string;
	authorize_url: string;
	expires_at: string;
	mcp_client_id: string;
}

export interface OAuthStatusResponse {
	id: string;
	status: "pending" | "authorized" | "failed" | "expired" | "revoked";
	created_at: string;
	token_id?: string;
	token_expires_at?: string;
	token_scopes?: string;
}

export interface MCPVKConfig {
	virtual_key_id: string;
	tools_to_execute: string[];
}

export interface UpdateMCPClientRequest {
	name?: string;
	is_code_mode_client?: boolean;
	headers?: Record<string, SecretVar>;
	// Set to a new list (including empty) to replace per-user-headers schema.
	// Omitted = preserve existing. When this list changes against the stored
	// value, the backend flips all existing user credential rows to
	// 'needs_update' so callers re-submit on next tool use.
	per_user_header_keys?: string[];
	tools_to_execute?: string[];
	tools_to_auto_execute?: string[];
	is_ping_available?: boolean;
	// Only meaningful when connection_type === "http". Toggling this on an
	// existing client takes effect immediately: switching to true dials the
	// persistent connection now, switching to false closes it. See
	// MCPClientConfig's field doc for the full contract.
	needs_session_stickiness?: boolean;
	tool_pricing?: Record<string, number>;
	tool_sync_interval?: number; // Per-client override in minutes (0 = use global, -1 = disabled)
	tool_execution_timeout?: number; // Per-client tool execution timeout in seconds (0 = use global)
	allowed_extra_headers?: string[]; // Allowlist of x-bf-eh-* headers forwarded to this MCP server. ["*"] = allow all.
	allow_on_all_virtual_keys?: boolean; // When true, available to all VKs with all tools allowed by default; explicit VK config overrides this
	disabled?: boolean; // Set to true to shut down connection/workers; false to reconnect
	tls_config?: MCPTLSConfig; // TLS configuration for HTTP/SSE connections
	oauth_config?: OAuthConfigUpdate; // Only supported for existing oauth/per_user_oauth clients (credential rotation)
	token_exchange?: MCPTokenExchangeConfig; // Only supported for existing token_exchange clients; omitted = preserve
	vk_configs?: MCPVKConfig[]; // When provided, replaces all VK assignments for this MCP client
}

// Pagination + filter params for MCP clients list
export interface GetMCPClientsParams {
	limit?: number;
	offset?: number;
	search?: string;
	server?: string;
	// Comma-separated exact-match filters (OR semantics), mirroring the library page.
	connection_type?: string; // http,sse,stdio
	auth_type?: string; // none,headers,oauth,per_user_oauth,per_user_headers,token_exchange
	state?: string; // connected,disconnected — resolved against live engine state
	virtual_keys?: string; // comma-separated VK IDs the client is assigned to
	// Boolean facets — omit for "no filter".
	code_mode?: boolean; // filters is_code_mode_client
	disabled?: boolean; // filters disabled status
	all_virtual_keys?: boolean; // when true, include clients open to all virtual keys
}

// Paginated response for MCP clients list
export interface GetMCPClientsResponse {
	clients: MCPClient[];
	count: number;
	total_count: number;
	limit: number;
	offset: number;
}

// Types for MCP Tool Selector component
export interface SelectedTool {
	mcpClientId: string;
	toolName: string;
}

// MCP Tool Spec for tool groups (matches backend schema)
export interface MCPToolSpec {
	mcp_client_id: string;
	tool_names: string[];
}

// ---------------------------------------------------------------------------
// MCP Library (synced catalog)
// ---------------------------------------------------------------------------

/** A single entry from the synced MCP server catalog (`mcp_library` table). */
export interface MCPLibraryEntry {
	id: number;
	slug: string;
	name: string;
	description?: string;
	category?: string;
	connection_type: MCPConnectionType;
	connection_url?: string;
	stdio_config?: MCPStdioConfig;
	auth_type?: MCPAuthType;
	required_header_keys?: string[];
	icon_url?: string;
	docs_url?: string;
	publisher?: string;
	tags?: string[];
	metadata?: Record<string, unknown>;
	/** "remote" for synced rows, "custom" for org-published entries. */
	source?: "remote" | "custom";
	created_at: string;
	updated_at: string;
}

/** Body for POST /api/mcp/library — publish a custom (org-internal) library entry. */
export interface CreateMCPLibraryEntryRequest {
	name: string;
	description?: string;
	category?: string;
	connection_type: MCPConnectionType;
	connection_url?: string;
	stdio_config?: MCPStdioConfig;
	auth_type?: MCPAuthType;
	required_header_keys?: string[];
	icon_url?: string;
	docs_url?: string;
	publisher?: string;
	tags?: string[];
}

export interface GetMCPLibraryParams {
	search?: string;
	category?: string;
	connection_type?: string;
	auth_type?: string;
	tags?: string;
	sort_by?: string;
	order?: string;
	limit?: number;
	offset?: number;
}

export interface GetMCPLibraryResponse {
	servers: MCPLibraryEntry[];
	count: number;
	total_count: number;
	limit: number;
	offset: number;
}

export interface MCPLibraryFilterData {
	categories: string[];
	connection_types: string[];
	auth_types: string[];
	tags: string[];
}