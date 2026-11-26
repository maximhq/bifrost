// SBS fork: MetaMCP-style MCP endpoints and tool overrides.
// Mirrors transports/bifrost-http/handlers/sbs_mcp.go.

export type SBSEndpointAuthMode = "virtual_key" | "public";

export interface SBSEndpoint {
	id: number;
	name: string;
	description?: string;
	virtual_mcp_id: number;
	enabled: boolean;
	auth_mode: SBSEndpointAuthMode;
	public_virtual_key_id?: string;
	allow_query_param_key: boolean;
	openapi_enabled: boolean;
	public_grant_added?: boolean; // the endpoint assigned its namespace to its public key
	created_at: string;
	updated_at: string;
	virtual_mcp_name?: string;
	virtual_mcp_slug?: string;
	available: boolean;
}

export interface SBSEndpointRequest {
	name: string;
	description?: string;
	virtual_mcp_id: number;
	enabled?: boolean;
	auth_mode: SBSEndpointAuthMode;
	public_virtual_key_id?: string;
	allow_query_param_key: boolean;
	openapi_enabled?: boolean;
}

export interface SBSToolOverride {
	name?: string;
	title?: string;
	description?: string;
	annotations?: Record<string, unknown>;
}

// client ID -> tool name (unprefixed) -> override
export type SBSOverridesMap = Record<string, Record<string, SBSToolOverride>>;
