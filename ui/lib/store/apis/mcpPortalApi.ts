import type {
	MCPPortalAuthenticateRequest,
	MCPPortalAuthenticateResponse,
	MCPPortalParams,
	MCPPortalResponse,
} from "@/lib/types/mcpPortal";
import { baseApi } from "./baseApi";

export const mcpPortalApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		getMCPPortal: builder.query<MCPPortalResponse, MCPPortalParams>({
			query: (params) => ({ url: "/mcp/portal", params }),
			providesTags: (_result, _error, { virtual_key_id }) => [{ type: "MCPPortal", id: virtual_key_id ? `vk:${virtual_key_id}` : "user" }],
		}),
		authenticateMCPPortalServer: builder.mutation<MCPPortalAuthenticateResponse, MCPPortalAuthenticateRequest>({
			query: ({ id, ...body }) => ({
				url: `/mcp/portal/${encodeURIComponent(id)}/authenticate`,
				method: "POST",
				body,
			}),
			invalidatesTags: (_result, _error, { virtual_key_id }) => [
				{ type: "MCPPortal", id: virtual_key_id ? `vk:${virtual_key_id}` : "user" },
			],
		}),
	}),
});

export const { useGetMCPPortalQuery, useAuthenticateMCPPortalServerMutation } = mcpPortalApi;