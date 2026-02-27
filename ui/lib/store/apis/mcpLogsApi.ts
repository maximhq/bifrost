import {
	MCPToolLogEntry,
	MCPToolLogFilters,
	MCPToolLogStats,
	MCPToolLogFilterData,
	Pagination,
} from "@/lib/types/logs";
import { baseApi } from "./baseApi";

export const mcpLogsApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		// Get MCP tool logs with filters and pagination
		getMCPLogs: builder.query<
			{
				logs: MCPToolLogEntry[];
				pagination: Pagination;
				stats: MCPToolLogStats;
				has_logs: boolean;
			},
			{
				filters: MCPToolLogFilters;
				pagination: Pagination;
			}
		>({
			query: ({ filters, pagination }) => {
				const params: Record<string, string | number> = {
					limit: pagination.limit,
					offset: pagination.offset,
					sort_by: pagination.sort_by,
					order: pagination.order,
				};

				// Add filters to params if they exist
				if (filters.tool_names && filters.tool_names.length > 0) {
					params.tool_names = filters.tool_names.join(",");
				}
				if (filters.server_labels && filters.server_labels.length > 0) {
					params.server_labels = filters.server_labels.join(",");
				}
				if (filters.status && filters.status.length > 0) {
					params.status = filters.status.join(",");
				}
				if (filters.virtual_key_ids && filters.virtual_key_ids.length > 0) {
					params.virtual_key_ids = filters.virtual_key_ids.join(",");
				}
				if (filters.llm_request_ids && filters.llm_request_ids.length > 0) {
					params.llm_request_ids = filters.llm_request_ids.join(",");
				}
				if (filters.start_time) params.start_time = filters.start_time;
				if (filters.end_time) params.end_time = filters.end_time;
				if (filters.min_latency) params.min_latency = filters.min_latency;
				if (filters.max_latency) params.max_latency = filters.max_latency;
				if (filters.content_search) params.content_search = filters.content_search;

				return {
					url: "/mcp-logs",
					params,
				};
			},
			providesTags: ["MCPLogs"],
		}),

		// Get MCP tool logs statistics with filters
		getMCPLogsStats: builder.query<
			MCPToolLogStats,
			{
				filters: MCPToolLogFilters;
			}
		>({
			query: ({ filters }) => {
				const params: Record<string, string | number> = {};

				// Add filters to params if they exist
				if (filters.tool_names && filters.tool_names.length > 0) {
					params.tool_names = filters.tool_names.join(",");
				}
				if (filters.server_labels && filters.server_labels.length > 0) {
					params.server_labels = filters.server_labels.join(",");
				}
				if (filters.status && filters.status.length > 0) {
					params.status = filters.status.join(",");
				}
				if (filters.virtual_key_ids && filters.virtual_key_ids.length > 0) {
					params.virtual_key_ids = filters.virtual_key_ids.join(",");
				}
				if (filters.llm_request_ids && filters.llm_request_ids.length > 0) {
					params.llm_request_ids = filters.llm_request_ids.join(",");
				}
				if (filters.start_time) params.start_time = filters.start_time;
				if (filters.end_time) params.end_time = filters.end_time;
				if (filters.min_latency) params.min_latency = filters.min_latency;
				if (filters.max_latency) params.max_latency = filters.max_latency;
				if (filters.content_search) params.content_search = filters.content_search;

				return {
					url: "/mcp-logs/stats",
					params,
				};
			},
			providesTags: ["MCPLogs"],
		}),

		// Get available filter data (tool names, server labels)
		getMCPAvailableFilterData: builder.query<MCPToolLogFilterData, void>({
			query: () => "/mcp-logs/filterdata",
			providesTags: ["MCPLogs"],
		}),

		// Delete MCP tool logs by their IDs
		deleteMCPLogs: builder.mutation<void, { ids: string[] }>({
			query: ({ ids }) => ({
				url: "/mcp-logs",
				method: "DELETE",
				body: { ids },
			}),
			invalidatesTags: ["MCPLogs"],
		}),
	}),
});

export const {
	useGetMCPLogsQuery,
	useGetMCPLogsStatsQuery,
	useGetMCPAvailableFilterDataQuery,
	useGetMCPAvailableFilterDataQuery: useGetMCPLogsFilterDataQuery,
	useLazyGetMCPLogsQuery,
	useLazyGetMCPLogsStatsQuery,
	useLazyGetMCPAvailableFilterDataQuery,
	useDeleteMCPLogsMutation,
} = mcpLogsApi;
