import { LogEntry, LogFilters, LogStats, Pagination } from "@/lib/types/logs";
import { baseApi } from "./baseApi";
import { DBKey, RedactedDBKey, VirtualKey } from "@/lib/types/governance";

export const logsApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		// Get logs with filters and pagination
		getLogs: builder.query<
			{
				logs: LogEntry[];
				pagination: Pagination;
				stats: LogStats;
			},
			{
				filters: LogFilters;
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
				if (filters.providers && filters.providers.length > 0) {
					params.providers = filters.providers.join(",");
				}
				if (filters.models && filters.models.length > 0) {
					params.models = filters.models.join(",");
				}
				if (filters.status && filters.status.length > 0) {
					params.status = filters.status.join(",");
				}
				if (filters.objects && filters.objects.length > 0) {
					params.objects = filters.objects.join(",");
				}
				if (filters.selected_key_ids && filters.selected_key_ids.length > 0) {
					params.selected_key_ids = filters.selected_key_ids.join(",");
				}
				if (filters.virtual_key_ids && filters.virtual_key_ids.length > 0) {
					params.virtual_key_ids = filters.virtual_key_ids.join(",");
				}
				if (filters.start_time) params.start_time = filters.start_time;
				if (filters.end_time) params.end_time = filters.end_time;
				if (filters.min_latency) params.min_latency = filters.min_latency;
				if (filters.max_latency) params.max_latency = filters.max_latency;
				if (filters.min_tokens) params.min_tokens = filters.min_tokens;
				if (filters.max_tokens) params.max_tokens = filters.max_tokens;
				if (filters.content_search) params.content_search = filters.content_search;

				return {
					url: "/logs",
					params,
				};
			},
			providesTags: ["Logs"],
		}),

		// Get dropped requests count
		getDroppedRequests: builder.query<{ dropped_requests: number }, void>({
			query: () => "/logs/dropped",
			providesTags: ["Logs"],
		}),

		// Get available models
		getAvailableFilterData: builder.query<{ models: string[]; selected_keys: RedactedDBKey[]; virtual_keys: VirtualKey[] }, void>({
			query: () => "/logs/filterdata",
			providesTags: ["Logs"],
		}),
	}),
});

export const {
	useGetLogsQuery,
	useGetDroppedRequestsQuery,
	useGetAvailableFilterDataQuery,
	useLazyGetLogsQuery,
	useLazyGetDroppedRequestsQuery,
	useLazyGetAvailableFilterDataQuery,
} = logsApi;
