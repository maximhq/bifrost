import { baseApi } from "@/lib/store/apis/baseApi";
import {
	CatalogResponse,
	EvaluatorInput,
	EvaluatorResponse,
	EvaluatorVersionsResponse,
	ExecuteDraftRequest,
	ExecuteEvaluatorRequest,
	ExecutionResponse,
	InstallBuiltinRequest,
	ListEvalKeysResponse,
	ListEvaluatorsParams,
	ListEvaluatorsResponse,
	ValidateEvaluatorRequest,
	ValidateEvaluatorResponse,
} from "@/lib/types/evals";

// Inject evaluator endpoints into baseApi
export const evalsApi = baseApi.injectEndpoints({
	overrideExisting: true,
	endpoints: (builder) => ({
		// List evaluators, newest first
		getEvaluators: builder.query<ListEvaluatorsResponse, ListEvaluatorsParams | void>({
			query: (params) => {
				const search = new URLSearchParams();
				if (params?.types?.length) search.set("type", params.types.join(","));
				if (params?.source) search.set("source", params.source);
				if (params?.status) search.set("status", params.status);
				if (params?.search) search.set("search", params.search);
				if (params?.tag) search.set("tag", params.tag);
				if (params?.limit !== undefined) search.set("limit", String(params.limit));
				if (params?.offset !== undefined) search.set("offset", String(params.offset));
				const query = search.toString();
				return query ? `/evals/evaluators?${query}` : "/evals/evaluators";
			},
			providesTags: ["Evaluators"],
		}),

		// Get one evaluator with its current config
		getEvaluator: builder.query<EvaluatorResponse, string>({
			query: (id) => `/evals/evaluators/${id}`,
			providesTags: (result, error, id) => [{ type: "Evaluators", id }],
		}),

		// Create a custom evaluator
		createEvaluator: builder.mutation<EvaluatorResponse, EvaluatorInput>({
			query: (data) => ({
				url: "/evals/evaluators",
				method: "POST",
				body: data,
			}),
			invalidatesTags: ["Evaluators"],
		}),

		// Update an evaluator; a changed config becomes a new version
		updateEvaluator: builder.mutation<EvaluatorResponse, { id: string; data: EvaluatorInput }>({
			query: ({ id, data }) => ({
				url: `/evals/evaluators/${id}`,
				method: "PUT",
				body: data,
			}),
			invalidatesTags: (result, error, { id }) => ["Evaluators", { type: "Evaluators", id }, { type: "EvaluatorVersions", id }],
		}),

		// Delete an evaluator
		deleteEvaluator: builder.mutation<{ message: string }, string>({
			query: (id) => ({
				url: `/evals/evaluators/${id}`,
				method: "DELETE",
			}),
			invalidatesTags: ["Evaluators"],
		}),

		// Copy an evaluator under a new name
		duplicateEvaluator: builder.mutation<EvaluatorResponse, { id: string; name?: string }>({
			query: ({ id, name }) => ({
				url: `/evals/evaluators/${id}/duplicate`,
				method: "POST",
				body: { name },
			}),
			invalidatesTags: ["Evaluators"],
		}),

		// List an evaluator's versions, newest first
		getEvaluatorVersions: builder.query<EvaluatorVersionsResponse, string>({
			query: (id) => `/evals/evaluators/${id}/versions`,
			providesTags: (result, error, id) => [{ type: "EvaluatorVersions", id }],
		}),

		// Check a config without saving or running it
		validateEvaluator: builder.mutation<ValidateEvaluatorResponse, ValidateEvaluatorRequest>({
			query: (data) => ({
				url: "/evals/evaluators/validate",
				method: "POST",
				body: data,
			}),
		}),

		// Run a saved evaluator
		executeEvaluator: builder.mutation<ExecutionResponse, { id: string; data: ExecuteEvaluatorRequest }>({
			query: ({ id, data }) => ({
				url: `/evals/evaluators/${id}/execute`,
				method: "POST",
				body: data,
			}),
		}),

		// Run an unsaved evaluator, for an editor's test panel
		executeDraftEvaluator: builder.mutation<ExecutionResponse, ExecuteDraftRequest>({
			query: (data) => ({
				url: "/evals/evaluators/execute",
				method: "POST",
				body: data,
			}),
		}),

		// Read an execution, to poll one that outlasted the execute call
		getEvaluatorExecution: builder.query<ExecutionResponse, string>({
			query: (id) => `/evals/executions/${id}`,
		}),

		// List the provider keys marked "use for evaluations". Tagged as a key listing so
		// saving a provider key refreshes it.
		getEvalKeys: builder.query<ListEvalKeysResponse, void>({
			query: () => "/evals/keys",
			providesTags: ["DBKeys", "EvalKeys"],
		}),

		// List the built-in evaluators
		getEvaluatorCatalog: builder.query<CatalogResponse, void>({
			query: () => "/evals/catalog",
			providesTags: ["EvaluatorCatalog"],
		}),

		// Create an evaluator from a built-in
		installBuiltinEvaluator: builder.mutation<EvaluatorResponse, { key: string; data: InstallBuiltinRequest }>({
			query: ({ key, data }) => ({
				url: `/evals/catalog/${key}/install`,
				method: "POST",
				body: data,
			}),
			invalidatesTags: ["Evaluators"],
		}),
	}),
});

export const {
	useGetEvaluatorsQuery,
	useGetEvaluatorQuery,
	useLazyGetEvaluatorQuery,
	useCreateEvaluatorMutation,
	useUpdateEvaluatorMutation,
	useDeleteEvaluatorMutation,
	useDuplicateEvaluatorMutation,
	useGetEvaluatorVersionsQuery,
	useValidateEvaluatorMutation,
	useExecuteEvaluatorMutation,
	useExecuteDraftEvaluatorMutation,
	useLazyGetEvaluatorExecutionQuery,
	useGetEvalKeysQuery,
	useGetEvaluatorCatalogQuery,
	useInstallBuiltinEvaluatorMutation,
} = evalsApi;