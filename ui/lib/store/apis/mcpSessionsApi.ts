// RTK Query endpoints for the MCP Auth Sessions tab + auth landing flow.
// Mirrors the backend handler in transports/bifrost-http/handlers/mcp_sessions.go.

import {
	MCPFlowDetail,
	MCPFlowStartResponse,
	MCPSessionReauthResponse,
	MCPSessionsListResponse,
} from "@/lib/types/mcpSessions";
import { baseApi } from "./baseApi";

export const mcpSessionsApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		// List token rows + pending flow rows visible to the caller's identity.
		getMCPSessions: builder.query<MCPSessionsListResponse, void>({
			query: () => ({ url: "/mcp/sessions" }),
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

		// Best-effort upstream revoke + hard-delete the row.
		// Optimistic patch over invalidatesTags to avoid the cross-replica
		// stale-list window in clustered deployments — drop the row from the
		// cached list immediately and roll back if the server rejects.
		revokeMCPSession: builder.mutation<void, string>({
			query: (rowId) => ({ url: `/mcp/sessions/${rowId}`, method: "DELETE" }),
			async onQueryStarted(rowId, { dispatch, queryFulfilled }) {
				const patchResult = dispatch(
					mcpSessionsApi.util.updateQueryData("getMCPSessions", undefined, (draft) => {
						draft.sessions = draft.sessions.filter((s) => s.id !== rowId);
					}),
				);
				try {
					await queryFulfilled;
				} catch {
					patchResult.undo();
				}
			},
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
