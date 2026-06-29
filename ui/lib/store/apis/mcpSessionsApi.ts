// RTK Query endpoints for the MCP Auth Sessions tab + auth landing flow.
// Mirrors the backend handler in transports/bifrost-http/handlers/mcp_sessions.go.

import {
	MCPFlowDetail,
	MCPFlowStartResponse,
	MCPSessionReauthResponse,
	MCPSessionsListResponse,
	MCPSessionsQueryParams,
} from "@/lib/types/mcpSessions";
import { baseApi } from "./baseApi";

// buildMCPSessionsListParams shapes the filter+page params for the URL.
// Array filters are csv-joined (matches the backend's parseCommaSeparated)
// and sorted so selection order doesn't fragment the RTK Query cache key
// (same logical filter set always produces the same key + canonical URL).
// Empty values are dropped so the key doesn't fragment on "" vs unset.
function buildMCPSessionsListParams(params?: MCPSessionsQueryParams) {
	if (!params) return {};
	const out: Record<string, string | number> = {};
	if (params.q) out.q = params.q;
	if (params.kind?.length) out.kind = [...params.kind].sort().join(",");
	if (params.status?.length) out.status = [...params.status].sort().join(",");
	if (params.auth_mode?.length) out.auth_mode = [...params.auth_mode].sort().join(",");
	if (params.mcp_client_id?.length) out.mcp_client_id = [...params.mcp_client_id].sort().join(",");
	if (params.limit !== undefined) out.limit = params.limit;
	if (params.offset !== undefined) out.offset = params.offset;
	return out;
}

export const mcpSessionsApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		// List session rows visible to the caller's identity, with optional
		// filters + pagination. Each filter combo gets its own cache entry
		// (RTK Query auto-derives the key from the params object).
		getMCPSessions: builder.query<MCPSessionsListResponse, MCPSessionsQueryParams | void>({
			query: (params) => ({
				url: "/mcp/sessions",
				params: buildMCPSessionsListParams(params ?? undefined),
			}),
			providesTags: ["MCPSessions"],
		}),

		// Start a fresh OAuth flow for the MCP client backing this token row.
		// Returns the upstream authorize URL the browser should redirect to.
		// No cache patch: the existing token row's status doesn't change at
		// mutation time — the new flow is only finalized when the upstream
		// callback fires, which is out of this client's hand. The browser
		// navigates away to the authorize URL anyway, so the next list query
		// after the user comes back picks up whatever state landed.
		reauthMCPSession: builder.mutation<MCPSessionReauthResponse, string>({
			query: (rowId) => ({ url: `/mcp/sessions/${rowId}/reauth`, method: "POST" }),
		}),

		// Best-effort upstream revoke + hard-delete the row. Invalidates the
		// MCPSessions tag so all keyed cache entries (per filter combo)
		// refetch. The previous optimistic patch only worked when the cache
		// was singleton-keyed; with per-filter keys, iterating every keyed
		// entry to splice the row out is more code than a single refetch is
		// worth, especially for a destructive action where the user expects
		// a tiny network roundtrip anyway.
		revokeMCPSession: builder.mutation<void, string>({
			query: (rowId) => ({ url: `/mcp/sessions/${rowId}`, method: "DELETE" }),
			invalidatesTags: ["MCPSessions"],
		}),

		// Flow metadata for the auth landing page.
		getMCPFlowDetail: builder.query<MCPFlowDetail, string>({
			query: (flowId) => ({ url: `/oauth/per-user/flows/${flowId}` }),
			providesTags: (_result, _err, id) => [{ type: "MCPSessions", id }],
		}),

		// Returns the upstream provider authorize URL for a pending flow.
		// The frontend redirects the browser to that URL.
		startMCPFlow: builder.mutation<MCPFlowStartResponse, string>({
			// GET in spirit (no body) but defined as mutation so it can be
			// triggered imperatively on button click rather than auto-fetched.
			query: (flowId) => ({ url: `/oauth/per-user/flows/${flowId}/start`, method: "GET" }),
		}),
	}),
});

export const {
	useGetMCPSessionsQuery,
	useReauthMCPSessionMutation,
	useRevokeMCPSessionMutation,
	useGetMCPFlowDetailQuery,
	useStartMCPFlowMutation,
} = mcpSessionsApi;