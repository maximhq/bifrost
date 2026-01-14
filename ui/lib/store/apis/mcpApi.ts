import { CreateMCPClientRequest, MCPClient, UpdateMCPClientRequest } from "@/lib/types/mcp";
import { baseApi } from "./baseApi";

export const mcpApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		// Get all MCP clients
		getMCPClients: builder.query<MCPClient[], void>({
			query: () => "/mcp/clients",
			providesTags: ["MCPClients"],
		}),

		// Create new MCP client
		createMCPClient: builder.mutation<null, CreateMCPClientRequest>({
			query: (data) => ({
				url: "/mcp/client",
				method: "POST",
				body: data,
			}),
			invalidatesTags: ["MCPClients"],
		}),

		// Update existing MCP client
		updateMCPClient: builder.mutation<null, { id: string; data: UpdateMCPClientRequest }>({
			query: ({ id, data }) => ({
				url: `/mcp/client/${id}`,
				method: "PUT",
				body: data,
			}),
			invalidatesTags: ["MCPClients"],
		}),

		// Delete MCP client
		deleteMCPClient: builder.mutation<null, string>({
			query: (id) => ({
				url: `/mcp/client/${id}`,
				method: "DELETE",
			}),
			invalidatesTags: ["MCPClients"],
		}),

		// Reconnect MCP client
		reconnectMCPClient: builder.mutation<null, string>({
			query: (id) => ({
				url: `/mcp/client/${id}/reconnect`,
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
} = mcpApi;
