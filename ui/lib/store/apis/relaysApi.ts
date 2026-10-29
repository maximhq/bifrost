import type {
	GetRelaysParams,
	GetRelaysResponse,
	Relay,
	RelayHistogramResponse,
	RelayLog,
	RelayLogFilterData,
	RelayLogFilters,
	RelayLogPagination,
	RelayLogSearchResponse,
	RelayLogStatsResponse,
	RelayOpenAPISpecResponse,
	UsagePreview,
	UsagePreviewRequest,
} from "@/lib/types/relays";
import { buildRelayLogQuery } from "@/lib/utils/relayLogQuery";
import { baseApi } from "./baseApi";

// The query builder is a pure helper (lib/utils/relayLogQuery.ts) so it can be unit tested without
// the store; it is re-exported here so callers keep importing it from the API module.
export { buildRelayLogQuery } from "@/lib/utils/relayLogQuery";

export const relaysApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		// Without params every relay is listed; the selector passes search and a page.
		getRelays: builder.query<GetRelaysResponse, GetRelaysParams | void>({
			query: (params) => ({ url: "/relays", params: params ?? undefined }),
			providesTags: (result) => ["Relays", ...(result?.relays ?? []).map((e) => ({ type: "Relays" as const, id: e.name }))],
		}),
		getRelay: builder.query<Relay, string>({
			query: (name) => ({ url: `/relays/${encodeURIComponent(name)}` }),
			providesTags: (_r, _e, name) => [{ type: "Relays", id: name }],
		}),
		createRelay: builder.mutation<Relay, Relay>({
			query: (body) => ({ url: "/relays", method: "POST", body }),
			invalidatesTags: ["Relays"],
		}),
		updateRelay: builder.mutation<Relay, { name: string; data: Relay }>({
			query: ({ name, data }) => ({ url: `/relays/${encodeURIComponent(name)}`, method: "PUT", body: data }),
			invalidatesTags: (_r, _e, { name }) => [{ type: "Relays", id: name }],
		}),
		deleteRelay: builder.mutation<void, string>({
			query: (name) => ({ url: `/relays/${encodeURIComponent(name)}`, method: "DELETE" }),
			// Deleting an endpoint cascades its virtual-key grants.
			invalidatesTags: ["Relays", "VirtualKeys"],
		}),
		previewRelayUsage: builder.mutation<UsagePreview, UsagePreviewRequest>({
			query: (body) => ({ url: "/relays/usage-preview", method: "POST", body }),
		}),
		fetchRelayOpenAPISpec: builder.mutation<RelayOpenAPISpecResponse, { spec_url: string }>({
			query: (body) => ({ url: "/relays/openapi-spec", method: "POST", body }),
		}),

		getRelayLogs: builder.query<RelayLogSearchResponse, { filters: RelayLogFilters; pagination: RelayLogPagination }>({
			query: ({ filters, pagination }) => ({ url: `/relay-logs?${buildRelayLogQuery(filters, pagination)}` }),
			providesTags: ["RelayLogs"],
		}),
		getRelayLog: builder.query<RelayLog, string>({
			query: (id) => ({ url: `/relay-logs/${encodeURIComponent(id)}` }),
			providesTags: (_r, _e, id) => [{ type: "RelayLogs", id }],
		}),
		// comparePrevious opts in to change-vs-previous-period, as getLogsStats does for the LLM
		// page; callers that omit it get the plain stats and a separate RTK cache entry.
		getRelayLogStats: builder.query<RelayLogStatsResponse, { filters: RelayLogFilters; comparePrevious?: boolean }>({
			query: ({ filters, comparePrevious }) => {
				const query = buildRelayLogQuery(filters);
				const compare = comparePrevious ? "compare_to_previous=true" : "";
				return { url: `/relay-logs/stats?${[query, compare].filter(Boolean).join("&")}` };
			},
			providesTags: ["RelayLogs"],
		}),
		getRelayLogHistogram: builder.query<RelayHistogramResponse, { filters: RelayLogFilters; bucketSizeSeconds?: number }>({
			query: ({ filters, bucketSizeSeconds }) => {
				const query = buildRelayLogQuery(filters);
				const bucket = bucketSizeSeconds ? `bucket_size_seconds=${bucketSizeSeconds}` : "";
				return { url: `/relay-logs/histogram?${[query, bucket].filter(Boolean).join("&")}` };
			},
			providesTags: ["RelayLogs"],
		}),
		// Filter sidebar values. Pass `dimensions` to fetch only a subset (relay_names, methods,
		// virtual_keys, users, teams, customers, business_units, projects, apps) and `q` to search
		// server-side; omit both for everything, as getMCPAvailableFilterData does.
		getRelayLogFilterData: builder.query<Partial<RelayLogFilterData>, { dimensions?: string[]; q?: string } | void>({
			query: (arg) => {
				const dims = arg && "dimensions" in arg ? arg.dimensions : undefined;
				const q = arg && "q" in arg ? arg.q : undefined;
				const params = new URLSearchParams();
				if (dims && dims.length > 0) params.set("dimensions", [...dims].sort().join(","));
				if (q) params.set("q", q);
				const qs = params.toString();
				return { url: qs ? `/relay-logs/filterdata?${qs}` : "/relay-logs/filterdata" };
			},
			providesTags: ["RelayLogs"],
		}),
		deleteRelayLogs: builder.mutation<void, string[]>({
			query: (ids) => ({ url: "/relay-logs", method: "DELETE", body: { ids } }),
			invalidatesTags: ["RelayLogs"],
		}),
	}),
});

export const {
	useGetRelaysQuery,
	useGetRelayQuery,
	useCreateRelayMutation,
	useUpdateRelayMutation,
	useDeleteRelayMutation,
	usePreviewRelayUsageMutation,
	useFetchRelayOpenAPISpecMutation,
	useGetRelayLogsQuery,
	useLazyGetRelayLogsQuery,
	useGetRelayLogQuery,
	useGetRelayLogStatsQuery,
	useGetRelayLogHistogramQuery,
	useGetRelayLogFilterDataQuery,
	useDeleteRelayLogsMutation,
} = relaysApi;