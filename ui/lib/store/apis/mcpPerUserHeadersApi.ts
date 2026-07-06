// RTK Query endpoints for the MCP per-user headers credential flow.
// Mirrors the backend handler in transports/bifrost-http/handlers/mcp_per_user_headers.go.

import { MCPHeadersFlowDetail, MCPPerUserHeadersSubmitRequest, MCPPerUserHeadersSubmitResponse } from "@/lib/types/mcpPerUserHeaders";
import { baseApi } from "./baseApi";

export const mcpPerUserHeadersApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		// Flow detail: pending headers-submission flow row metadata + the
		// schema the form needs. Authorization on the backend accepts either
		// a dashboard session OR the mcp_headers_auth temp token bound to
		// {flowId} (carried in the URL fragment by the auth page).
		getMCPPerUserHeadersFlow: builder.query<MCPHeadersFlowDetail, string>({
			query: (flowID) => ({ url: `/mcp/per-user-headers/flows/${flowID}` }),
			providesTags: (_result, _err, id) => [{ type: "MCPPerUserHeaderCredentials", id }],
		}),

		// Submit caller-supplied header values for a pending flow row.
		// Backend verifies upstream + upserts the credential + consumes
		// (deletes) the flow row and its temp token. Invalidates the sessions
		// list so UI surfaces the fresh credential row immediately.
		submitMCPPerUserHeadersFlow: builder.mutation<
			MCPPerUserHeadersSubmitResponse,
			{ flowId: string; body: MCPPerUserHeadersSubmitRequest }
		>({
			query: ({ flowId, body }) => ({
				url: `/mcp/per-user-headers/flows/${flowId}`,
				method: "PUT",
				body,
			}),
			invalidatesTags: (_result, _err, arg) => ["MCPSessions", { type: "MCPPerUserHeaderCredentials", id: arg.flowId }],
		}),

		// Revoke a single credential row by primary key. The sessions tab uses
		// the unified /api/mcp/sessions/{id} DELETE which falls back to header
		// credential lookup on OAuth miss — this typed endpoint exists for
		// callers that already know they're acting on a header row.
		revokeMCPPerUserHeaders: builder.mutation<void, string>({
			query: (credentialID) => ({ url: `/mcp/per-user-headers/credential/${credentialID}`, method: "DELETE" }),
			invalidatesTags: ["MCPSessions"],
		}),
	}),
});

export const { useGetMCPPerUserHeadersFlowQuery, useSubmitMCPPerUserHeadersFlowMutation, useRevokeMCPPerUserHeadersMutation } =
	mcpPerUserHeadersApi;