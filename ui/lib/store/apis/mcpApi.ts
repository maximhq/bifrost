import { CreateMCPClientRequest, MCPClient, OAuthFlowResponse, OAuthStatusResponse, UpdateMCPClientRequest } from "@/lib/types/mcp";
import { baseApi } from "./baseApi";

type CreateMCPClientResponse = { status: "success"; message: string } | OAuthFlowResponse;

export const mcpApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		// Get all MCP clients
		getMCPClients: builder.query<MCPClient[], void>({
			query: () => "/mcp/clients",
			providesTags: ["MCPClients"],
		}),

		// Create new MCP client
		createMCPClient: builder.mutation<CreateMCPClientResponse, CreateMCPClientRequest>({
			query: (data) => ({
				url: "/mcp/client",
				method: "POST",
				body: data,
			}),
			invalidatesTags: ["MCPClients"],
		}),

		// Update existing MCP client
		updateMCPClient: builder.mutation<any, { id: string; data: UpdateMCPClientRequest }>({
			query: ({ id, data }) => ({
				url: `/mcp/client/${id}`,
				method: "PUT",
				body: data,
			}),
			invalidatesTags: ["MCPClients"],
		}),

		// Delete MCP client
		deleteMCPClient: builder.mutation<any, string>({
			query: (id) => ({
				url: `/mcp/client/${id}`,
				method: "DELETE",
			}),
			invalidatesTags: ["MCPClients"],
		}),

		// Reconnect MCP client
		reconnectMCPClient: builder.mutation<any, string>({
			query: (id) => ({
				url: `/mcp/client/${id}/reconnect`,
				method: "POST",
			}),
			invalidatesTags: ["MCPClients"],
		}),

		// Get OAuth config status (for polling)
		getOAuthConfigStatus: builder.query<OAuthStatusResponse, string>({
			query: (oauthConfigId) => `/oauth/config/${oauthConfigId}/status`,
			providesTags: (result, error, id) => [{ type: "OAuth2Config", id }],
		}),

		// Complete OAuth flow for MCP client
		completeOAuthFlow: builder.mutation<{ status: string; message: string }, string>({
			query: (oauthConfigId) => ({
				url: `/mcp/client/${oauthConfigId}/complete-oauth`,
				method: "POST",
			}),
			invalidatesTags: ["MCPClients"],
		}),
	}),
});

export const {
	useGetMCPClientsQuery,
	useCreateMCPClientMutation,
	useUpdateMCPClientMutation,
	useDeleteMCPClientMutation,
	useReconnectMCPClientMutation,
	useLazyGetMCPClientsQuery,
	useLazyGetOAuthConfigStatusQuery,
	useCompleteOAuthFlowMutation,
} = mcpApi;
