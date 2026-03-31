import { RedactedDBKey, VirtualKey } from "@/lib/types/governance";
import {
	CostHistogramResponse,
	LatencyHistogramResponse,
	LogEntry,
	LogFilters,
	LogsHistogramResponse,
	LogStats,
	ModelHistogramResponse,
	ModelRankingsResponse,
	Pagination,
	ProviderCostHistogramResponse,
	ProviderLatencyHistogramResponse,
	ProviderTokenHistogramResponse,
	RecalculateCostResponse,
	TokenHistogramResponse,
} from "@/lib/types/logs";
import { baseApi } from "./baseApi";
import { RoutingRule } from "@/lib/types/routingRules";

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
	if (filters.routing_rule_ids && filters.routing_rule_ids.length > 0) {
		params.routing_rule_ids = filters.routing_rule_ids.join(",");
	}
	if (filters.routing_engine_used && filters.routing_engine_used.length > 0) {
		params.routing_engine_used = filters.routing_engine_used.join(",");
	}
	if (filters.start_time) params.start_time = filters.start_time;
	if (filters.end_time) params.end_time = filters.end_time;
	if (filters.min_latency !== undefined) params.min_latency = filters.min_latency;
	if (filters.max_latency !== undefined) params.max_latency = filters.max_latency;
	if (filters.min_tokens !== undefined) params.min_tokens = filters.min_tokens;
	if (filters.max_tokens !== undefined) params.max_tokens = filters.max_tokens;
	if (filters.missing_cost_only) params.missing_cost_only = "true";
	if (filters.content_search) params.content_search = filters.content_search;
	if (filters.metadata_filters) {
		for (const [key, value] of Object.entries(filters.metadata_filters)) {
			params[`metadata_${key}`] = value;
		}
	}

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
				if (filters.routing_rule_ids && filters.routing_rule_ids.length > 0) {
					params.routing_rule_ids = filters.routing_rule_ids.join(",");
				}
				if (filters.routing_engine_used && filters.routing_engine_used.length > 0) {
					params.routing_engine_used = filters.routing_engine_used.join(",");
				}
				if (filters.start_time) params.start_time = filters.start_time;
				if (filters.end_time) params.end_time = filters.end_time;
				if (filters.min_latency !== undefined) params.min_latency = filters.min_latency;
				if (filters.max_latency !== undefined) params.max_latency = filters.max_latency;
				if (filters.min_tokens !== undefined) params.min_tokens = filters.min_tokens;
				if (filters.max_tokens !== undefined) params.max_tokens = filters.max_tokens;
				if (filters.missing_cost_only) params.missing_cost_only = "true";
				if (filters.content_search) params.content_search = filters.content_search;
				if (filters.metadata_filters) {
					for (const [key, value] of Object.entries(filters.metadata_filters)) {
						params[`metadata_${key}`] = value;
					}
				}

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
				if (filters.routing_rule_ids && filters.routing_rule_ids.length > 0) {
					params.routing_rule_ids = filters.routing_rule_ids.join(",");
				}
				if (filters.routing_engine_used && filters.routing_engine_used.length > 0) {
					params.routing_engine_used = filters.routing_engine_used.join(",");
				}
				if (filters.start_time) params.start_time = filters.start_time;
				if (filters.end_time) params.end_time = filters.end_time;
				if (filters.min_latency !== undefined) params.min_latency = filters.min_latency;
				if (filters.max_latency !== undefined) params.max_latency = filters.max_latency;
				if (filters.min_tokens !== undefined) params.min_tokens = filters.min_tokens;
				if (filters.max_tokens !== undefined) params.max_tokens = filters.max_tokens;
				if (filters.missing_cost_only) params.missing_cost_only = "true";
				if (filters.content_search) params.content_search = filters.content_search;
				if (filters.metadata_filters) {
					for (const [key, value] of Object.entries(filters.metadata_filters)) {
						params[`metadata_${key}`] = value;
					}
				}

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

		// Get latency histogram with percentiles
		getLogsLatencyHistogram: builder.query<
			LatencyHistogramResponse,
			{
				filters: LogFilters;
			}
		>({
			query: ({ filters }) => ({
				url: "/logs/histogram/latency",
				params: buildFilterParams(filters),
			}),
			providesTags: ["Logs"],
		}),

		// Get provider cost histogram with provider breakdown
		getLogsProviderCostHistogram: builder.query<
			ProviderCostHistogramResponse,
			{
				filters: LogFilters;
			}
		>({
			query: ({ filters }) => ({
				url: "/logs/histogram/cost/by-provider",
				params: buildFilterParams(filters),
			}),
			providesTags: ["Logs"],
		}),

		// Get provider token histogram with provider breakdown
		getLogsProviderTokenHistogram: builder.query<
			ProviderTokenHistogramResponse,
			{
				filters: LogFilters;
			}
		>({
			query: ({ filters }) => ({
				url: "/logs/histogram/tokens/by-provider",
				params: buildFilterParams(filters),
			}),
			providesTags: ["Logs"],
		}),

		// Get provider latency histogram with provider breakdown
		getLogsProviderLatencyHistogram: builder.query<
			ProviderLatencyHistogramResponse,
			{
				filters: LogFilters;
			}
		>({
			query: ({ filters }) => ({
				url: "/logs/histogram/latency/by-provider",
				params: buildFilterParams(filters),
			}),
			providesTags: ["Logs"],
		}),

		// Get model rankings with trends
		getModelRankings: builder.query<
			ModelRankingsResponse,
			{
				filters: LogFilters;
			}
		>({
			query: ({ filters }) => ({
				url: "/logs/rankings",
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
		getAvailableFilterData: builder.query<
			{
				models: string[];
				selected_keys: RedactedDBKey[];
				virtual_keys: VirtualKey[];
				routing_rules: RoutingRule[];
				routing_engines: string[];
				metadata_keys: Record<string, string[]>;
			},
			void
		>({
			query: () => "/logs/filterdata",
			providesTags: ["Logs"],
		}),

		// Delete logs by their IDs or by current filters
		deleteLogs: builder.mutation<{ message: string; deleted_count: number }, { ids?: string[]; filters?: LogFilters }>({
			query: ({ ids, filters }) => ({
				url: "/logs",
				method: "DELETE",
				body: {
					...(ids ? { ids } : {}),
					...(filters ? { filters } : {}),
				},
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

		// Get a single log entry by ID (includes raw_request and raw_response)
		getLogById: builder.query<LogEntry, string>({
			query: (id) => `/logs/${encodeURIComponent(id)}`,
			providesTags: (result, error, id) => [{ type: "Logs", id }],
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
	useGetLogsLatencyHistogramQuery,
	useGetLogsProviderCostHistogramQuery,
	useGetLogsProviderTokenHistogramQuery,
	useGetLogsProviderLatencyHistogramQuery,
	useGetDroppedRequestsQuery,
	useGetAvailableFilterDataQuery,
	useLazyGetLogsQuery,
	useLazyGetLogsStatsQuery,
	useLazyGetLogsHistogramQuery,
	useLazyGetLogsTokenHistogramQuery,
	useLazyGetLogsCostHistogramQuery,
	useLazyGetLogsModelHistogramQuery,
	useLazyGetLogsLatencyHistogramQuery,
	useLazyGetLogsProviderCostHistogramQuery,
	useLazyGetLogsProviderTokenHistogramQuery,
	useLazyGetLogsProviderLatencyHistogramQuery,
	useLazyGetModelRankingsQuery,
	useLazyGetDroppedRequestsQuery,
	useLazyGetAvailableFilterDataQuery,
	useDeleteLogsMutation,
	useRecalculateLogCostsMutation,
	useLazyGetLogByIdQuery,
	useGetLogByIdQuery,
} = logsApi;
