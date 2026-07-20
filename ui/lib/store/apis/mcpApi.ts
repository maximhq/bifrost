import {
	CreateMCPClientRequest,
	CreateMCPLibraryEntryRequest,
	GetMCPClientsParams,
	GetMCPClientsResponse,
	GetMCPLibraryParams,
	GetMCPLibraryResponse,
	MCPLibraryEntry,
	MCPLibraryFilterData,
	OAuthFlowResponse,
	OAuthStatusResponse,
	UpdateMCPClientRequest,
} from "@/lib/types/mcp";
import { baseApi } from "./baseApi";

type CreateMCPClientResponse = { status: "success"; message: string } | OAuthFlowResponse;
type UpdateMCPClientResponse = { status: "success"; message: string } | OAuthFlowResponse;

export const mcpApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		// Get MCP clients with pagination
		getMCPClients: builder.query<GetMCPClientsResponse, GetMCPClientsParams | void>({
			query: (params) => ({
				url: "/mcp/clients",
				params: {
					...(params?.limit !== undefined && { limit: params.limit }),
					...(params?.offset !== undefined && { offset: params.offset }),
					...(params?.search && { search: params.search }),
					...(params?.server && { server: params.server }),
					...(params?.connection_type && { connection_type: params.connection_type }),
					...(params?.auth_type && { auth_type: params.auth_type }),
					...(params?.state && { state: params.state }),
					...(params?.virtual_keys && { virtual_keys: params.virtual_keys }),
					...(params?.code_mode !== undefined && { code_mode: params.code_mode }),
					...(params?.disabled !== undefined && { disabled: params.disabled }),
					...(params?.all_virtual_keys !== undefined && { all_virtual_keys: params.all_virtual_keys }),
				},
			}),
			providesTags: ["MCPClients"],
		}),

		// Get MCP library catalog (synced) with search/filter/sort/pagination
		getMCPLibrary: builder.query<GetMCPLibraryResponse, GetMCPLibraryParams | void>({
			query: (params) => ({
				url: "/mcp/library",
				params: {
					...(params?.search && { search: params.search }),
					...(params?.category && { category: params.category }),
					...(params?.connection_type && { connection_type: params.connection_type }),
					...(params?.auth_type && { auth_type: params.auth_type }),
					...(params?.tags && { tags: params.tags }),
					...(params?.sort_by && { sort_by: params.sort_by }),
					...(params?.order && { order: params.order }),
					...(params?.limit !== undefined && { limit: params.limit }),
					...(params?.offset !== undefined && { offset: params.offset }),
				},
			}),
			providesTags: ["MCPLibrary"],
		}),

		// Get distinct facet values for the MCP library filter sidebar
		getMCPLibraryFilterData: builder.query<MCPLibraryFilterData, void>({
			query: () => ({ url: "/mcp/library/filterdata" }),
			providesTags: ["MCPLibrary"],
		}),

		// Force an immediate MCP library catalog sync
		forceSyncMCPLibrary: builder.mutation<{ status: string; message: string }, void>({
			query: () => ({
				url: "/mcp/library/force-sync",
				method: "POST",
			}),
			invalidatesTags: ["MCPLibrary"],
		}),

		// Publish a custom (org-internal) MCP server into the library
		createMCPLibraryEntry: builder.mutation<{ status: string; message: string; entry: MCPLibraryEntry }, CreateMCPLibraryEntryRequest>({
			query: (data) => ({
				url: "/mcp/library",
				method: "POST",
				body: data,
			}),
			invalidatesTags: ["MCPLibrary"],
		}),

		// Soft-delete (hide) a library entry — remote or custom — by numeric id
		deleteMCPLibraryEntry: builder.mutation<{ status: string; message: string }, number>({
			query: (id) => ({
				url: `/mcp/library/${id}`,
				method: "DELETE",
			}),
			async onQueryStarted(id, { dispatch, getState, queryFulfilled }) {
				// Optimistically remove the row from every cached getMCPLibrary page
				// so it disappears immediately; roll back the patches if the request fails.
				const patches: { undo: () => void }[] = [];
				const queries = (getState() as any).api.queries;
				for (const entry of Object.values(queries) as any[]) {
					if (entry?.endpointName !== "getMCPLibrary" || entry?.status !== "fulfilled") continue;
					patches.push(
						dispatch(
							mcpApi.util.updateQueryData("getMCPLibrary", entry.originalArgs, (draft) => {
								if (!draft.servers) return;
								const before = draft.servers.length;
								draft.servers = draft.servers.filter((s) => s.id !== id);
								if (draft.servers.length < before) {
									draft.count = Math.max(0, (draft.count || 0) - 1);
									draft.total_count = Math.max(0, (draft.total_count || 0) - 1);
								}
							}),
						),
					);
				}
				try {
					await queryFulfilled;
				} catch {
					patches.forEach((p) => p.undo());
				}
			},
			// Keep tag invalidation as a fallback to reconcile with the server.
			invalidatesTags: ["MCPLibrary"],
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
		updateMCPClient: builder.mutation<UpdateMCPClientResponse, { id: string; data: UpdateMCPClientRequest }>({
			query: ({ id, data }) => ({
				url: `/mcp/client/${id}`,
				method: "PUT",
				body: data,
			}),
			async onQueryStarted({ id, data }, { dispatch, getState, queryFulfilled }) {
				try {
					const { data: response } = await queryFulfilled;
					if (response.status === "pending_oauth") {
						dispatch(mcpApi.util.invalidateTags(["MCPClients"]));
						return;
					}
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
									if (data.per_user_header_keys !== undefined) draft.clients[index].config.per_user_header_keys = data.per_user_header_keys;
									if (data.tools_to_execute !== undefined) draft.clients[index].config.tools_to_execute = data.tools_to_execute;
									if (data.tools_to_auto_execute !== undefined)
										draft.clients[index].config.tools_to_auto_execute = data.tools_to_auto_execute;
									if (data.is_ping_available !== undefined) draft.clients[index].config.is_ping_available = data.is_ping_available;
									if (data.tool_pricing !== undefined) draft.clients[index].config.tool_pricing = data.tool_pricing;
									// The request carries minutes, but the cached config mirrors the GET
									// response, which carries nanoseconds. Convert so the cache stays in
									// the server's unit instead of drifting until the next refetch.
									if (data.tool_sync_interval !== undefined)
										draft.clients[index].config.tool_sync_interval = data.tool_sync_interval * 6e10;
									if (data.disabled !== undefined) {
										draft.clients[index].config.disabled = data.disabled;
										if (data.disabled) {
											draft.clients[index].state = "disabled";
										}
									}
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
	useGetMCPLibraryQuery,
	useGetMCPLibraryFilterDataQuery,
	useForceSyncMCPLibraryMutation,
	useCreateMCPLibraryEntryMutation,
	useDeleteMCPLibraryEntryMutation,
	useCreateMCPClientMutation,
	useUpdateMCPClientMutation,
	useDeleteMCPClientMutation,
	useReconnectMCPClientMutation,
	useLazyGetMCPClientsQuery,
	useLazyGetOAuthConfigStatusQuery,
	useCompleteOAuthFlowMutation,
} = mcpApi;