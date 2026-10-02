import type {
	AgentHistogramResponse,
	AgentLogDetail,
	AgentLogFilters,
	AgentLogHistoryResponse,
	AgentLogStats,
	AgentPagination,
} from "@/lib/types/agentLogs";
import { getRangeForPeriod } from "@/lib/utils/timeRange";
import { baseApi } from "./baseApi";

const Agent_TEXT_FILTER_KEYS = ["request_id", "trace_id", "task_id", "context_id", "push_config_id", "delivery_id", "attempt_id"] as const;

const Agent_MULTI_FILTER_KEYS = [
	"agent_name",
	"operation",
	"user_id",
	"virtual_key_id",
	"team_id",
	"customer_id",
	"business_unit_id",
	"project_id",
	"status",
	"record_kind",
	"event_type",
	"task_state",
] as const;

// Shared by the list, stats and histogram queries so all three narrow to the
// same rows. Pagination only applies to the list.
function buildAgentHistoryQuery(filters: AgentLogFilters, pagination?: AgentPagination): string {
	const params = new URLSearchParams();
	if (pagination) {
		params.set("limit", String(pagination.limit));
		params.set("offset", String(pagination.offset));
		params.set("sort_by", pagination.sort_by);
		params.set("order", pagination.order);
	}
	for (const key of Agent_TEXT_FILTER_KEYS) {
		const value = filters[key];
		if (value) params.set(key, value);
	}
	if (filters.search) params.set("search", filters.search);
	// Multi-value filters go out as repeated params, one entry each.
	for (const key of Agent_MULTI_FILTER_KEYS) {
		for (const value of filters[key] ?? []) {
			if (value) params.append(key, value);
		}
	}
	// The backend has no relative-period parameter, so a period is expanded to
	// a fresh RFC3339 window at request time. Polling therefore always queries
	// a rolling window, matching how the other log pages behave.
	if (filters.period) {
		const { from, to } = getRangeForPeriod(filters.period);
		params.set("start_time", from.toISOString());
		params.set("end_time", to.toISOString());
	} else if (filters.start_time && filters.end_time) {
		params.set("start_time", filters.start_time);
		params.set("end_time", filters.end_time);
	}
	return params.toString();
}

export interface AgentFilterData {
	users?: { id: string; name: string }[];
	virtual_keys?: { id: string; name: string }[];
	teams?: { id: string; name: string }[];
	customers?: { id: string; name: string }[];
	business_units?: { id: string; name: string }[];
	projects?: { id: string; name: string }[];
}

export const agentLogsApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		// List Agent history rows (metadata only) with filters and pagination.
		getAgentLogs: builder.query<AgentLogHistoryResponse, { filters: AgentLogFilters; pagination: AgentPagination; selectedId?: string }>({
			query: ({ filters, pagination, selectedId }) => {
				const query = new URLSearchParams(buildAgentHistoryQuery(filters, pagination));
				if (selectedId) query.set("selected_id", selectedId);
				return `/agents/history?${query}`;
			},
			providesTags: ["AgentLogs"],
		}),

		// Overview totals for the current filters. Shares the AgentLogs cache tag so
		// Refresh and Live update the cards, the chart and the table together.
		getAgentLogStats: builder.query<AgentLogStats, { filters: AgentLogFilters }>({
			query: ({ filters }) => `/agents/history/stats?${buildAgentHistoryQuery(filters)}`,
			providesTags: ["AgentLogs"],
		}),

		// Time-bucketed volume for the collapsible chart.
		getAgentHistogram: builder.query<AgentHistogramResponse, { filters: AgentLogFilters }>({
			query: ({ filters }) => `/agents/history/histogram?${buildAgentHistoryQuery(filters)}`,
			providesTags: ["AgentLogs"],
		}),

		// Hydrate one history entry with request/response/event bodies.
		getAgentLogById: builder.query<AgentLogDetail, string>({
			query: (id) => `/agents/history/${encodeURIComponent(id)}`,
			providesTags: (result, error, id) => [{ type: "AgentLogs", id }],
		}),

		getAgentFilterData: builder.query<AgentFilterData, { dimensions?: string[]; q?: string } | void>({
			query: (arg) => {
				const params = new URLSearchParams();
				if (arg?.dimensions?.length) params.set("dimensions", [...arg.dimensions].sort().join(","));
				if (arg?.q) params.set("q", arg.q);
				const query = params.toString();
				return query ? `/agents/history/filterdata?${query}` : "/agents/history/filterdata";
			},
			providesTags: ["AgentLogs"],
		}),

		// Delete agent log entries by their IDs. The backend also removes the
		// correlated stream-event rows sharing the deleted entries' request IDs.
		deleteAgentLogs: builder.mutation<void, { ids: string[] }>({
			query: ({ ids }) => ({
				url: "/agents/history",
				method: "DELETE",
				body: { ids },
			}),
			invalidatesTags: ["AgentLogs"],
		}),
	}),
});

export const {
	useGetAgentLogsQuery,
	useLazyGetAgentLogsQuery,
	useGetAgentLogStatsQuery,
	useGetAgentHistogramQuery,
	useGetAgentLogByIdQuery,
	useLazyGetAgentLogByIdQuery,
	useGetAgentFilterDataQuery,
	useDeleteAgentLogsMutation,
} = agentLogsApi;