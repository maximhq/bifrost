import { Function as ToolFunction } from "./logs";
import { EnvVar } from "./schemas";

export type MCPConnectionType = "http" | "stdio" | "sse";

export type MCPConnectionState = "connected" | "disconnected" | "error";

export type MCPAuthType = "none" | "headers" | "oauth";

export type { EnvVar };

export interface MCPStdioConfig {
	command: string;
	args: string[];
	envs: string[];
}

export interface OAuthConfig {
	client_id: string;
	client_secret?: string; // Optional for public clients using PKCE
	authorize_url?: string; // Optional, will be discovered from server_url if not provided
	token_url?: string; // Optional, will be discovered from server_url if not provided
	registration_url?: string; // Optional, for dynamic client registration
	scopes?: string[]; // Optional, can be discovered
	server_url?: string; // MCP server URL for OAuth discovery (automatically set from connection_string)
}

export interface MCPClientConfig {
	client_id: string; // Maps to ClientID in TableMCPClient
	name: string;
	is_code_mode_client?: boolean;
	connection_type: MCPConnectionType;
	connection_string?: EnvVar;
	stdio_config?: MCPStdioConfig;
	auth_type?: MCPAuthType;
	oauth_config_id?: string;
	tools_to_execute?: string[];
	tools_to_auto_execute?: string[];
	headers?: Record<string, EnvVar>;
	is_ping_available?: boolean;
	tool_pricing?: Record<string, number>;
	tool_sync_interval?: number; // Per-client override in minutes (0 = use global, -1 = disabled)
	allowed_extra_headers?: string[]; // Allowlist of x-bf-eh-* headers forwarded to this MCP server. ["*"] = allow all.
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
	connection_string?: EnvVar;
	stdio_config?: MCPStdioConfig;
	auth_type?: MCPAuthType;
	oauth_config?: OAuthConfig;
	tools_to_execute?: string[];
	tools_to_auto_execute?: string[];
	headers?: Record<string, EnvVar>;
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
	headers?: Record<string, EnvVar>;
	tools_to_execute?: string[];
	tools_to_auto_execute?: string[];
	is_ping_available?: boolean;
	tool_pricing?: Record<string, number>;
	tool_sync_interval?: number; // Per-client override in minutes (0 = use global, -1 = disabled)
	allowed_extra_headers?: string[]; // Allowlist of x-bf-eh-* headers forwarded to this MCP server. ["*"] = allow all.
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
