import { baseApi } from "@/lib/store/apis/baseApi";
import type {
	ListRunEntriesParams,
	ListRunsParams,
	ListRunsResponse,
	Run,
	RunCheck,
	RunComparison,
	RunEntriesPage,
	RunEntry,
	RunEvaluatorRef,
	RunInput,
	RunResult,
} from "@/lib/types/evalRuns";

const base = "/evals/runs";

function queryString(params: Record<string, string | number | boolean | undefined>): string {
	const search = new URLSearchParams();
	for (const [key, value] of Object.entries(params)) {
		if (value !== undefined && value !== "") search.set(key, String(value));
	}
	const query = search.toString();
	return query ? `?${query}` : "";
}

// A change to a run changes the list, the run itself and its entries.
const runTags = (id: string) => ["EvalRuns" as const, { type: "EvalRuns" as const, id }, { type: "EvalRunEntries" as const, id }];

// Inject test run endpoints into baseApi
export const evalRunsApi = baseApi.injectEndpoints({
	overrideExisting: true,
	endpoints: (builder) => ({
		getEvalRuns: builder.query<ListRunsResponse, ListRunsParams | void>({
			query: (params) => `${base}${queryString({ ...params })}`,
			providesTags: ["EvalRuns"],
		}),

		getEvalRun: builder.query<{ run: Run }, string>({
			query: (id) => `${base}/${id}`,
			providesTags: (result, error, id) => [{ type: "EvalRuns", id }],
		}),

		// A dry check: what would stop this run from starting.
		validateEvalRun: builder.mutation<RunCheck, RunInput>({
			query: (data) => ({ url: `${base}/validate`, method: "POST", body: data }),
		}),

		// Starting a run can save the dataset's draft as a version, so datasets refresh too.
		createEvalRun: builder.mutation<{ run: Run }, RunInput>({
			query: (data) => ({ url: base, method: "POST", body: data }),
			invalidatesTags: ["EvalRuns", "EvalDatasets", "EvalDatasetVersions"],
		}),

		updateEvalRun: builder.mutation<{ run: Run }, { id: string; name: string; tags?: string[] }>({
			query: ({ id, name, tags }) => ({ url: `${base}/${id}`, method: "PUT", body: { name, tags } }),
			invalidatesTags: (result, error, { id }) => ["EvalRuns", { type: "EvalRuns", id }],
		}),

		deleteEvalRun: builder.mutation<{ deleted: boolean }, string>({
			query: (id) => ({ url: `${base}/${id}`, method: "DELETE" }),
			invalidatesTags: ["EvalRuns"],
		}),

		stopEvalRun: builder.mutation<{ run: Run }, string>({
			query: (id) => ({ url: `${base}/${id}/stop`, method: "POST" }),
			invalidatesTags: (result, error, id) => runTags(id),
		}),

		rerunEvalRun: builder.mutation<{ run: Run }, { id: string; scope?: "failed" | "all"; entryIds?: string[] }>({
			query: ({ id, scope, entryIds }) => ({ url: `${base}/${id}/rerun`, method: "POST", body: { scope, entry_ids: entryIds } }),
			invalidatesTags: (result, error, { id }) => runTags(id),
		}),

		addEvalRunEvaluators: builder.mutation<{ run: Run }, { id: string; evaluators: RunEvaluatorRef[] }>({
			query: ({ id, evaluators }) => ({ url: `${base}/${id}/evaluators`, method: "POST", body: { evaluators } }),
			invalidatesTags: (result, error, { id }) => runTags(id),
		}),

		getEvalRunEntries: builder.query<RunEntriesPage, ListRunEntriesParams>({
			query: ({ id, ...params }) => `${base}/${id}/entries${queryString(params)}`,
			providesTags: (result, error, { id }) => [{ type: "EvalRunEntries", id }],
		}),

		getEvalRunEntry: builder.query<{ entry: RunEntry }, { id: string; entryId: string }>({
			query: ({ id, entryId }) => `${base}/${id}/entries/${entryId}`,
			providesTags: (result, error, { id }) => [{ type: "EvalRunEntries", id }],
		}),

		// A reviewer's score for a human evaluator on one entry.
		reviewEvalRunResult: builder.mutation<
			{ result: RunResult },
			{ id: string; entryId: string; evaluatorId: string; score: boolean | number | string; reasoning?: string }
		>({
			query: ({ id, entryId, evaluatorId, score, reasoning }) => ({
				url: `${base}/${id}/entries/${entryId}/results/${evaluatorId}`,
				method: "PUT",
				body: { score, reasoning },
			}),
			invalidatesTags: (result, error, { id }) => runTags(id),
		}),

		compareEvalRuns: builder.query<RunComparison, { ids: string[]; offset?: number; limit?: number }>({
			query: ({ ids, offset, limit }) => `${base}/compare${queryString({ ids: ids.join(","), offset, limit })}`,
			providesTags: ["EvalRuns"],
		}),
	}),
});

export const {
	useGetEvalRunsQuery,
	useGetEvalRunQuery,
	useValidateEvalRunMutation,
	useCreateEvalRunMutation,
	useUpdateEvalRunMutation,
	useDeleteEvalRunMutation,
	useStopEvalRunMutation,
	useRerunEvalRunMutation,
	useAddEvalRunEvaluatorsMutation,
	useGetEvalRunEntriesQuery,
	useLazyGetEvalRunEntriesQuery,
	useGetEvalRunEntryQuery,
	useReviewEvalRunResultMutation,
	useCompareEvalRunsQuery,
} = evalRunsApi;