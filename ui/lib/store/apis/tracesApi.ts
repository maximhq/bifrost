import type {
	TraceComment,
	TraceConnectInfo,
	TraceDetail,
	TraceExperiment,
	TraceFilterData,
	TraceFilters,
	TraceHistogramResponse,
	TracePagination,
	TraceProject,
	TraceScore,
	TraceSearchResponse,
	TraceSessionSearchResponse,
	TraceSpanSearchResponse,
	TraceStatsResponse,
} from "@/lib/types/traces";
import { buildTraceQuery } from "@/lib/utils/traceQuery";
import { baseApi } from "./baseApi";

type ListArgs = { filters: TraceFilters; pagination: TracePagination };

export const tracesApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		getTraces: builder.query<TraceSearchResponse, ListArgs>({
			query: ({ filters, pagination }) => ({ url: `/traces?${buildTraceQuery(filters, pagination)}` }),
			providesTags: ["Traces"],
		}),
		getTraceSpans: builder.query<TraceSpanSearchResponse, ListArgs>({
			query: ({ filters, pagination }) => ({ url: `/traces/spans?${buildTraceQuery(filters, pagination)}` }),
			providesTags: ["Traces"],
		}),
		getTraceSessions: builder.query<TraceSessionSearchResponse, ListArgs>({
			query: ({ filters, pagination }) => ({ url: `/traces/sessions?${buildTraceQuery(filters, pagination)}` }),
			providesTags: ["Traces"],
		}),
		getTraceStats: builder.query<TraceStatsResponse, TraceFilters>({
			query: (filters) => ({ url: `/traces/stats?${buildTraceQuery(filters)}` }),
			providesTags: ["Traces"],
		}),
		getTraceHistogram: builder.query<TraceHistogramResponse, { filters: TraceFilters; bucketSizeSeconds: number }>({
			query: ({ filters, bucketSizeSeconds }) => ({
				url: `/traces/histogram?${buildTraceQuery(filters, undefined, { bucket_size_seconds: bucketSizeSeconds })}`,
			}),
			providesTags: ["Traces"],
		}),
		getTrace: builder.query<TraceDetail, { projectId: string; traceId: string }>({
			query: ({ projectId, traceId }) => ({ url: `/traces/trace/${encodeURIComponent(projectId)}/${encodeURIComponent(traceId)}` }),
			providesTags: (_r, _e, { projectId, traceId }) => [{ type: "Traces", id: `${projectId}/${traceId}` }],
		}),
		// A file an SDK uploaded beside its spans (a Braintrust attachment, Langfuse media), as an object
		// URL the viewer can show. Fetched through the API client so it carries the dashboard auth.
		getTraceUploadUrl: builder.query<string, string>({
			query: (id) => ({
				url: `/traces/uploads/${encodeURIComponent(id)}`,
				responseHandler: async (response: Response) => (response.ok ? URL.createObjectURL(await response.blob()) : response.text()),
			}),
			keepUnusedDataFor: 300,
		}),
		// Review: a reviewer's scores and comments on a trace or on one of its spans.
		saveTraceScore: builder.mutation<
			{ score: TraceScore },
			{
				projectId: string;
				traceId: string;
				data: { id?: string; span_id?: string; name: string; value?: number; string_value?: string; comment?: string };
			}
		>({
			query: ({ projectId, traceId, data }) => ({
				url: `/traces/trace/${encodeURIComponent(projectId)}/${encodeURIComponent(traceId)}/scores`,
				method: "POST",
				body: data,
			}),
			invalidatesTags: (_r, _e, { projectId, traceId }) => [{ type: "Traces", id: `${projectId}/${traceId}` }, "Traces"],
		}),
		deleteTraceScore: builder.mutation<void, { projectId: string; traceId: string; id: string }>({
			query: ({ projectId, traceId, id }) => ({
				url: `/traces/trace/${encodeURIComponent(projectId)}/${encodeURIComponent(traceId)}/scores/${encodeURIComponent(id)}`,
				method: "DELETE",
			}),
			invalidatesTags: (_r, _e, { projectId, traceId }) => [{ type: "Traces", id: `${projectId}/${traceId}` }, "Traces"],
		}),
		addTraceComment: builder.mutation<
			{ comment: TraceComment },
			{ projectId: string; traceId: string; data: { span_id?: string; text: string } }
		>({
			query: ({ projectId, traceId, data }) => ({
				url: `/traces/trace/${encodeURIComponent(projectId)}/${encodeURIComponent(traceId)}/comments`,
				method: "POST",
				body: data,
			}),
			invalidatesTags: (_r, _e, { projectId, traceId }) => [{ type: "Traces", id: `${projectId}/${traceId}` }],
		}),
		deleteTraceComment: builder.mutation<void, { projectId: string; traceId: string; id: string }>({
			query: ({ projectId, traceId, id }) => ({
				url: `/traces/trace/${encodeURIComponent(projectId)}/${encodeURIComponent(traceId)}/comments/${encodeURIComponent(id)}`,
				method: "DELETE",
			}),
			invalidatesTags: (_r, _e, { projectId, traceId }) => [{ type: "Traces", id: `${projectId}/${traceId}` }],
		}),
		deleteTrace: builder.mutation<void, { projectId: string; traceId: string }>({
			query: ({ projectId, traceId }) => ({
				url: `/traces/trace/${encodeURIComponent(projectId)}/${encodeURIComponent(traceId)}`,
				method: "DELETE",
			}),
			invalidatesTags: ["Traces"],
		}),
		getTraceExperiments: builder.query<{ experiments: TraceExperiment[] | null }, { projectIds?: string[] }>({
			query: ({ projectIds }) => ({
				url: `/traces/experiments${projectIds && projectIds.length > 0 ? `?project_ids=${projectIds.map(encodeURIComponent).join(",")}` : ""}`,
			}),
			providesTags: ["Traces"],
		}),
		getTraceConnectInfo: builder.query<TraceConnectInfo, void>({
			query: () => ({ url: "/traces/connect" }),
		}),
		getTraceProjects: builder.query<{ projects: TraceProject[] | null }, void>({
			query: () => ({ url: "/traces/projects" }),
			providesTags: ["TraceProjects"],
		}),
		createTraceProject: builder.mutation<{ project: TraceProject }, Partial<TraceProject>>({
			query: (body) => ({ url: "/traces/projects", method: "POST", body }),
			invalidatesTags: ["TraceProjects"],
		}),
		updateTraceProject: builder.mutation<{ project: TraceProject }, { id: string; data: Partial<TraceProject> }>({
			query: ({ id, data }) => ({ url: `/traces/projects/${encodeURIComponent(id)}`, method: "PUT", body: data }),
			invalidatesTags: ["TraceProjects"],
		}),
		deleteTraceProject: builder.mutation<void, string>({
			query: (id) => ({ url: `/traces/projects/${encodeURIComponent(id)}`, method: "DELETE" }),
			// Deleting a project removes its traces and unbinds its keys.
			invalidatesTags: ["TraceProjects", "Traces"],
		}),
		getTraceFilterData: builder.query<TraceFilterData, { projectIds?: string[] }>({
			query: ({ projectIds }) => ({
				url: `/traces/filterdata${projectIds && projectIds.length > 0 ? `?project_ids=${projectIds.map(encodeURIComponent).join(",")}` : ""}`,
			}),
			providesTags: ["Traces"],
		}),
	}),
});

export const {
	useGetTraceUploadUrlQuery,
	useGetTracesQuery,
	useGetTraceSpansQuery,
	useGetTraceSessionsQuery,
	useGetTraceStatsQuery,
	useGetTraceHistogramQuery,
	useGetTraceQuery,
	useGetTraceConnectInfoQuery,
	useGetTraceExperimentsQuery,
	useDeleteTraceMutation,
	useSaveTraceScoreMutation,
	useDeleteTraceScoreMutation,
	useAddTraceCommentMutation,
	useDeleteTraceCommentMutation,
	useGetTraceProjectsQuery,
	useCreateTraceProjectMutation,
	useUpdateTraceProjectMutation,
	useDeleteTraceProjectMutation,
	useGetTraceFilterDataQuery,
} = tracesApi;