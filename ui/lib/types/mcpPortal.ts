export interface MCPPortalIdentity {
	mode: "user" | "vk";
	id: string;
	name: string;
}

export type MCPPortalCredentialStatus = "ready" | "missing" | "needs_reauth" | "needs_update" | "admin_managed";

export interface MCPPortalServer {
	id: string;
	name: string;
	description?: string;
	auth_type: string;
	connection_state: string;
	credential_status: MCPPortalCredentialStatus;
	can_configure: boolean;
	required_header_keys?: string[];
}

export interface MCPPortalParams {
	virtual_key_id?: string;
}

export interface MCPPortalResponse {
	identity: MCPPortalIdentity | null;
	servers: MCPPortalServer[];
	count: number;
}

export interface MCPPortalAuthenticateRequest extends MCPPortalParams {
	id: string;
}

export interface MCPPortalAuthenticateResponse {
	auth_url: string;
}