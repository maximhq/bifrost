import {
	CreateMCPClientRequest,
	GetMCPClientsParams,
	GetMCPClientsResponse,
	MCPClient,
	OAuthFlowResponse,
	OAuthStatusResponse,
	UpdateMCPClientRequest,
} from "@/lib/types/mcp";
import { baseApi } from "./baseApi";

type CreateMCPClientResponse = { status: "success"; message: string } | OAuthFlowResponse;

export const mcpApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		// Get MCP clients with pagination
		getMCPClients: builder.query<GetMCPClientsResponse, GetMCPClientsParams | void>({
			query: (params) => ({
				url: "/mcp/clients",
				params: {
					...(params?.limit && { limit: params.limit }),
					...(params?.offset !== undefined && { offset: params.offset }),
					...(params?.search && { search: params.search }),
				},
			}),
			providesTags: ["MCPClients"],
		}),

		// Create new MCP client
		createMCPClient: builder.mutation<CreateMCPClientResponse, CreateMCPClientRequest>({
			query: (data) => ({
				url: "/mcp/client",
				method: "POST",
				body: data,
			}),
			async onQueryStarted(arg, { dispatch, getState, queryFulfilled }) {
				try {
					await queryFulfilled;
					// MCP create may return an OAuth flow response, so we can't optimistically
					// add the client — just invalidate to refetch
					const queries = (getState() as any).api.queries;
					for (const entry of Object.values(queries) as any[]) {
						if (entry?.endpointName !== "getMCPClients" || entry?.status !== "fulfilled") continue;
						dispatch(mcpApi.util.invalidateTags(["MCPClients"]));
						break;
					}
				} catch {}
			},
		}),

		// Update existing MCP client
		updateMCPClient: builder.mutation<any, { id: string; data: UpdateMCPClientRequest }>({
			query: ({ id, data }) => ({
				url: `/mcp/client/${id}`,
				method: "PUT",
				body: data,
			}),
			async onQueryStarted({ id, data }, { dispatch, getState, queryFulfilled }) {
				try {
					await queryFulfilled;
					const queries = (getState() as any).api.queries;
					for (const entry of Object.values(queries) as any[]) {
						if (entry?.endpointName !== "getMCPClients" || entry?.status !== "fulfilled") continue;
						dispatch(
							mcpApi.util.updateQueryData("getMCPClients", entry.originalArgs, (draft) => {
								if (!draft.clients) return;
								const index = draft.clients.findIndex((c) => c.config.client_id === id);
								if (index !== -1) {
									// Merge the updated fields into the existing client
									if (data.name !== undefined) draft.clients[index].config.name = data.name;
									if (data.is_code_mode_client !== undefined) draft.clients[index].config.is_code_mode_client = data.is_code_mode_client;
									if (data.headers !== undefined) draft.clients[index].config.headers = data.headers;
									if (data.tools_to_execute !== undefined) draft.clients[index].config.tools_to_execute = data.tools_to_execute;
									if (data.tools_to_auto_execute !== undefined)
										draft.clients[index].config.tools_to_auto_execute = data.tools_to_auto_execute;
									if (data.is_ping_available !== undefined) draft.clients[index].config.is_ping_available = data.is_ping_available;
									if (data.tool_pricing !== undefined) draft.clients[index].config.tool_pricing = data.tool_pricing;
									if (data.tool_sync_interval !== undefined) draft.clients[index].config.tool_sync_interval = data.tool_sync_interval;
								}
							}),
						);
					}
				} catch {}
			},
		}),

		// Delete MCP client
		deleteMCPClient: builder.mutation<any, string>({
			query: (id) => ({
				url: `/mcp/client/${id}`,
				method: "DELETE",
			}),
			async onQueryStarted(id, { dispatch, getState, queryFulfilled }) {
				try {
					await queryFulfilled;
					const queries = (getState() as any).api.queries;
					for (const entry of Object.values(queries) as any[]) {
						if (entry?.endpointName !== "getMCPClients" || entry?.status !== "fulfilled") continue;
						dispatch(
							mcpApi.util.updateQueryData("getMCPClients", entry.originalArgs, (draft) => {
								if (!draft.clients) return;
								const before = draft.clients.length;
								draft.clients = draft.clients.filter((c) => c.config.client_id !== id);
								if (draft.clients.length < before) {
									draft.count = Math.max(0, (draft.count || 0) - 1);
									draft.total_count = Math.max(0, (draft.total_count || 0) - 1);
								}
							}),
						);
					}
				} catch {}
			},
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