// Virtual MCPs: named bundles of tools drawn from one or more MCP clients,
// served at /mcp/<slug> and assignable to virtual keys. Mirrors the backend
// DTOs in transports/bifrost-http/handlers/virtualmcp.go.

// One tool selection from a source MCP client.
export interface VirtualMCPToolSpec {
	mcp_client_id: string;
	tool_names: string[];
}

export interface VirtualMCP {
	id: number;
	name: string;
	endpoint_slug: string;
	description?: string;
	enabled: boolean;
	tools: VirtualMCPToolSpec[];
	// Assigned VK ids: batch-loaded on list, loaded on get/update.
	virtual_key_ids: string[] | null;
	created_at: string;
	updated_at: string;
}

// Create/update body. endpoint_slug is honored only on create (immutable after).
export interface VirtualMCPRequest {
	name: string;
	endpoint_slug?: string;
	description?: string;
	enabled?: boolean;
	tools: VirtualMCPToolSpec[];
}

// One access-profile template that grants a Virtual MCP (enterprise reverse lookup).
export interface VirtualMCPAccessProfileUsage {
	id: number;
	name: string;
	user_count: number;
}

export interface GetVirtualMCPsParams {
	search?: string;
	limit?: number;
	offset?: number;
}

export interface GetVirtualMCPsResponse {
	virtual_mcps: VirtualMCP[];
	count: number;
	total_count: number;
	limit: number;
	offset: number;
}
