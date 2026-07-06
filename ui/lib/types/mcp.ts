import { Function as ToolFunction } from "./logs";
import { SecretVar } from "./schemas";

export type MCPConnectionType = "http" | "stdio" | "sse";

export type MCPConnectionState = "connected" | "disconnected" | "error" | "pending_tools" | "disabled";

export type MCPAuthType = "none" | "headers" | "oauth" | "per_user_oauth" | "per_user_headers";

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

export interface OAuthConfig {
	client_id: SecretVar;
	client_secret?: SecretVar; // Optional for public clients using PKCE
	authorize_url?: string; // Optional, will be discovered from server_url if not provided
	token_url?: string; // Optional, will be discovered from server_url if not provided
	registration_url?: string; // Optional, for dynamic client registration
	scopes?: string[]; // Optional, can be discovered
	server_url?: string; // MCP server URL for OAuth discovery (automatically set from connection_string)
}

/** OAuth fields allowed on MCP client update (e.g. client_secret-only rotation). */
export interface OAuthConfigUpdate {
	client_id?: SecretVar;
	client_secret?: SecretVar;
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
	tools_to_execute?: string[];
	tools_to_auto_execute?: string[];
	headers?: Record<string, SecretVar>;
	// per_user_header_keys: admin-declared header *names* that each caller
	// must supply when auth_type === "per_user_headers". Values live per-user
	// in the credential store, not on the client config. Required (non-empty)
	// for per_user_headers auth; ignored for all other auth types.
	per_user_header_keys?: string[];
	is_ping_available?: boolean;
	tool_pricing?: Record<string, number>;
	tool_sync_interval?: number; // Per-client override in minutes (0 = use global, -1 = disabled)
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
	// per_user_headers-only: a sample set of header values supplied by the
	// admin so the server can verify upstream + discover tools in the same
	// create call. Discarded after verification (never persisted). Mirrors
	// the per-user OAuth flow where the admin's temp access token plays
	// the analogous role. Ignored for all other auth types.
	user_headers?: Record<string, string>;
	is_ping_available?: boolean;
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
	expires_at: string;
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
	tool_pricing?: Record<string, number>;
	tool_sync_interval?: number; // Per-client override in minutes (0 = use global, -1 = disabled)
	allowed_extra_headers?: string[]; // Allowlist of x-bf-eh-* headers forwarded to this MCP server. ["*"] = allow all.
	allow_on_all_virtual_keys?: boolean; // When true, available to all VKs with all tools allowed by default; explicit VK config overrides this
	disabled?: boolean; // Set to true to shut down connection/workers; false to reconnect
	tls_config?: MCPTLSConfig; // TLS configuration for HTTP/SSE connections
	oauth_config?: OAuthConfigUpdate; // Only supported for existing oauth/per_user_oauth clients (credential rotation)
	vk_configs?: MCPVKConfig[]; // When provided, replaces all VK assignments for this MCP client
}

// Pagination params for MCP clients list
export interface GetMCPClientsParams {
	limit?: number;
	offset?: number;
	search?: string;
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