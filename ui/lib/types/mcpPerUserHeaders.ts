// Types for the MCP per-user headers credential flow.
// Mirrors the wire shapes in transports/bifrost-http/handlers/mcp_per_user_headers.go.
//
// Admin verification + tool discovery happen inline on POST /api/mcp/client
// (see CreateMCPClientRequest.user_headers); this module only carries the
// end-user submission types.
import { MCPHeadersUserCredentialStatus } from "./mcp";
import { AuthMode, MCPClientSummary, UserSummary, VirtualKeySummary } from "./mcpSessions";

// MCPHeadersUserCredentialStatus is exported from mcp.ts so the sessions
// table can switch on it. Re-exported here for clarity from the headers-
// specific module.
export type { MCPHeadersUserCredentialStatus };

// Submit request body for PUT /api/mcp/per-user-headers/flows/{id}. The
// flow row identifies the (mode, identity, mcp_client) triple, so the
// caller doesn't carry any of those on the body — just the values.
// Extra keys are dropped server-side against the live PerUserHeaderKeys
// schema, so stale UI submissions can't persist deprecated keys.
export interface MCPPerUserHeadersSubmitRequest {
	headers: Record<string, string>;
}

export interface MCPPerUserHeadersSubmitResponse {
	status: "success";
	credential_id: string;
	updated_at: string;
}

// Flow detail for GET /api/mcp/per-user-headers/flows/{id}. Mirrors
// MCPFlowDetail on the OAuth side: identity binding for display + the
// schema (required + admin key names) needed to render the submit form.
// has_active_credential / submitted_keys are populated when the caller's
// identity already has a stored credential — the page renders this as an
// "edit existing" affordance.
export interface MCPHeadersFlowDetail {
	id: string;
	flow_mode: AuthMode;
	status: "pending" | "completed" | "expired";
	mcp_client?: MCPClientSummary | null;
	user_id?: string | null;
	user?: UserSummary | null;
	virtual_key?: VirtualKeySummary | null;
	session_id?: string | null;
	expires_at: string;
	created_at: string;
	required_header_keys: string[];
	admin_header_keys?: string[];
	submitted_keys?: string[];
	has_active_credential: boolean;
}

// Local re-export so consumers don't need to import from mcp.ts directly.
export type MCPHeadersFlowStatus = MCPHeadersFlowDetail["status"];

// Retained alias for the credential status enum used by sessions table rows.
export type { MCPHeadersUserCredentialStatus as _MCPHeadersUserCredentialStatus };