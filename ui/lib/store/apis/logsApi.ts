import { RedactedDBKey, VirtualKey } from "@/lib/types/governance";
import {
	CostHistogramResponse,
	LogEntry,
	LogFilters,
	LogsHistogramResponse,
	LogStats,
	ModelHistogramResponse,
	Pagination,
	RecalculateCostResponse,
	TokenHistogramResponse,
} from "@/lib/types/logs";
import { baseApi } from "./baseApi";

// Helper function to build filter params
function buildFilterParams(filters: LogFilters): Record<string, string | number> {
	const params: Record<string, string | number> = {};

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
	if (filters.missing_cost_only) params.missing_cost_only = "true";
	if (filters.content_search) params.content_search = filters.content_search;

	return params;
}

export const logsApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		// Get logs with filters and pagination
		getLogs: builder.query<
			{
				logs: LogEntry[];
				pagination: Pagination;
				stats: LogStats;
				has_logs: boolean;
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
				if (filters.missing_cost_only) params.missing_cost_only = "true";
				if (filters.content_search) params.content_search = filters.content_search;

				return {
					url: "/logs",
					params,
				};
			},
			providesTags: ["Logs"],
		}),

		// Get logs statistics with filters
		getLogsStats: builder.query<
			LogStats,
			{
				filters: LogFilters;
			}
		>({
			query: ({ filters }) => {
				const params: Record<string, string | number> = {};

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
				if (filters.missing_cost_only) params.missing_cost_only = "true";
				if (filters.content_search) params.content_search = filters.content_search;

				return {
					url: "/logs/stats",
					params,
				};
			},
			providesTags: ["Logs"],
		}),

		// Get logs histogram with filters
		getLogsHistogram: builder.query<
			LogsHistogramResponse,
			{
				filters: LogFilters;
			}
		>({
			query: ({ filters }) => ({
				url: "/logs/histogram",
				params: buildFilterParams(filters),
			}),
			providesTags: ["Logs"],
		}),

		// Get token usage histogram with filters
		getLogsTokenHistogram: builder.query<
			TokenHistogramResponse,
			{
				filters: LogFilters;
			}
		>({
			query: ({ filters }) => ({
				url: "/logs/histogram/tokens",
				params: buildFilterParams(filters),
			}),
			providesTags: ["Logs"],
		}),

		// Get cost histogram with filters and model breakdown
		getLogsCostHistogram: builder.query<
			CostHistogramResponse,
			{
				filters: LogFilters;
			}
		>({
			query: ({ filters }) => ({
				url: "/logs/histogram/cost",
				params: buildFilterParams(filters),
			}),
			providesTags: ["Logs"],
		}),

		// Get model usage histogram with filters
		getLogsModelHistogram: builder.query<
			ModelHistogramResponse,
			{
				filters: LogFilters;
			}
		>({
			query: ({ filters }) => ({
				url: "/logs/histogram/models",
				params: buildFilterParams(filters),
			}),
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

		// Delete logs by their IDs
		deleteLogs: builder.mutation<void, { ids: string[] }>({
			query: ({ ids }) => ({
				url: "/logs",
				method: "DELETE",
				body: { ids },
			}),
			invalidatesTags: ["Logs"],
		}),

		recalculateLogCosts: builder.mutation<RecalculateCostResponse, { filters: LogFilters; limit?: number }>({
			query: ({ filters, limit }) => ({
				url: "/logs/recalculate-cost",
				method: "POST",
				body: { filters, limit },
			}),
			invalidatesTags: ["Logs"],
		}),
	}),
});

export const {
	useGetLogsQuery,
	useGetLogsStatsQuery,
	useGetLogsHistogramQuery,
	useGetLogsTokenHistogramQuery,
	useGetLogsCostHistogramQuery,
	useGetLogsModelHistogramQuery,
	useGetDroppedRequestsQuery,
	useGetAvailableFilterDataQuery,
	useLazyGetLogsQuery,
	useLazyGetLogsStatsQuery,
	useLazyGetLogsHistogramQuery,
	useLazyGetLogsTokenHistogramQuery,
	useLazyGetLogsCostHistogramQuery,
	useLazyGetLogsModelHistogramQuery,
	useLazyGetDroppedRequestsQuery,
	useLazyGetAvailableFilterDataQuery,
	useDeleteLogsMutation,
	useRecalculateLogCostsMutation,
} = logsApi;
