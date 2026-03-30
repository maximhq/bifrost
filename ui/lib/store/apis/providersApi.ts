import {
	AddProviderRequest,
	CreateProviderKeyRequest,
	ListProviderKeysResponse,
	ListProvidersResponse,
	ModelProvider,
	ModelProviderKey,
	ModelProviderName,
	UpdateProviderRequest,
	UpdateProviderKeyRequest,
} from "@/lib/types/config";
import { DBKey } from "@/lib/types/governance";
import { baseApi } from "./baseApi";

function sortProviders(a: ModelProvider, b: ModelProvider) {
	const aIsCustom = !!a.custom_provider_config;
	const bIsCustom = !!b.custom_provider_config;
	if (aIsCustom !== bIsCustom) return aIsCustom ? 1 : -1;
	return a.name.localeCompare(b.name);
}

// Types for models API
export interface ModelResponse {
	name: string;
	provider: string;
	accessible_by_keys?: string[];
}

export interface ListModelsResponse {
	models: ModelResponse[];
	total: number;
}

export interface GetModelsRequest {
	query?: string;
	provider?: string;
	keys?: string[];
	vks?: string[];
	limit?: number;
	unfiltered?: boolean;
}

export interface GetBaseModelsRequest {
	query?: string;
	limit?: number;
}

export interface ModelDatasheetParameter {
	id: string;
	label: string;
	helpText?: string;
	type: string;
	accesorKey?: string;
	default?: any;
	multiple?: boolean;
	range?: { min: number; max: number; step?: number };
	array?: { type: string; maxElements?: number; minElements?: number };
	options?: { label: string; value: string; subFields?: ModelDatasheetParameter[] }[];
}

export interface ModelDatasheetResponse {
	model_parameters?: ModelDatasheetParameter[];
	max_input_tokens?: number;
	max_output_tokens?: number;
	max_tokens?: number;
	mode?: string;
	provider?: string;
	base_model?: string;
	supports_vision?: boolean;
	[key: string]: any;
}

export interface ListBaseModelsResponse {
	models: string[];
	total: number;
}

type UpdateProviderMutationArg = UpdateProviderRequest & { name: ModelProviderName };

const DEFAULT_MODEL_PARAMETERS: ModelDatasheetResponse = {
	mode: "chat",
	base_model: "default",
	model_parameters: [
		{
			id: "temperature",
			label: "Temperature",
			helpText:
				"What sampling temperature to use, between 0 and 2. Higher values like 0.8 will make the output more random, while lower values like 0.2 will make it more focused and deterministic.",
			type: "number",
			range: { min: 0, max: 2, step: 0.01 },
		},
		{
			id: "max_tokens",
			label: "Max Tokens",
			helpText: "The maximum number of tokens that can be generated in the Result.",
			type: "number",
			range: { min: 1, max: 8192, step: 1 },
		},
		{
			id: "stream",
			label: "Stream",
			helpText:
				"The stream parameter in the API controls whether the response is sent in incremental updates, like tokenized data as it's generated, or as a complete result in one go.",
			type: "boolean",
		},
	],
};

export const providersApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		// Get all providers
		getProviders: builder.query<ModelProvider[], void>({
			query: () => "/providers",
			transformResponse: (response: ListProvidersResponse): ModelProvider[] => (response.providers ?? []).sort(sortProviders),
			providesTags: ["Providers"],
		}),

		// Get single provider
		getProvider: builder.query<ModelProvider, string>({
			query: (provider) => `/providers/${encodeURIComponent(provider)}`,
			providesTags: (result, error, provider) => [{ type: "Providers", id: provider }],
		}),

		getProviderKeys: builder.query<ModelProviderKey[], string>({
			query: (provider) => `/providers/${encodeURIComponent(provider)}/keys`,
			transformResponse: (response: ListProviderKeysResponse) => response.keys ?? [],
			providesTags: (result, error, provider) => [{ type: "ProviderKeys", id: provider }],
		}),

		getProviderKey: builder.query<ModelProviderKey, { provider: string; keyId: string }>({
			query: ({ provider, keyId }) => `/providers/${encodeURIComponent(provider)}/keys/${encodeURIComponent(keyId)}`,
			providesTags: (result, error, { provider }) => [{ type: "ProviderKeys", id: provider }],
		}),

		// Create new provider
		createProvider: builder.mutation<ModelProvider, AddProviderRequest>({
			query: (data) => ({
				url: "/providers",
				method: "POST",
				body: data,
			}),
			async onQueryStarted(arg, { dispatch, queryFulfilled }) {
				try {
					const { data: newProvider } = await queryFulfilled;
					dispatch(
						providersApi.util.updateQueryData("getProviders", undefined, (draft) => {
							draft.push(newProvider);
							draft.sort(sortProviders);
						}),
					);
				} catch {}
			},
		}),

		// Update existing provider
		updateProvider: builder.mutation<ModelProvider, UpdateProviderMutationArg>({
			query: ({ name, ...body }) => ({
				url: `/providers/${encodeURIComponent(name)}`,
				method: "PUT",
				body,
			}),
			invalidatesTags: (result, error, arg) => [{ type: "ProviderKeys", id: arg.name }, "DBKeys"],
			async onQueryStarted(arg, { dispatch, queryFulfilled }) {
				try {
					const { data: updatedProvider } = await queryFulfilled;
					dispatch(
						providersApi.util.updateQueryData("getProviders", undefined, (draft) => {
							const index = draft.findIndex((p) => p.name === arg.name);
							if (index !== -1) {
								draft[index] = updatedProvider;
							}
						}),
					);
					dispatch(providersApi.util.updateQueryData("getProvider", arg.name, () => updatedProvider));
				} catch {}
			},
		}),

		createProviderKey: builder.mutation<ModelProviderKey, { provider: string; key: CreateProviderKeyRequest }>({
			query: ({ provider, key }) => ({
				url: `/providers/${encodeURIComponent(provider)}/keys`,
				method: "POST",
				body: key,
			}),
			async onQueryStarted({ provider }, { dispatch, queryFulfilled }) {
				try {
					const { data: newKey } = await queryFulfilled;
					dispatch(
						providersApi.util.updateQueryData("getProviderKeys", provider, (draft) => {
							const exists = draft.some((k) => k.id === newKey.id);
							if (!exists) {
								draft.push(newKey);
							}
						}),
					);
					dispatch(
						providersApi.util.updateQueryData("getAllKeys", undefined, (draft) => {
							const exists = draft.some((k) => k.key_id === newKey.id);
							if (!exists) {
								draft.push({
									key_id: newKey.id,
									name: newKey.name,
									provider_id: "",
									models: newKey.models ?? [],
									provider: provider as ModelProviderName,
								});
							}
						}),
					);
				} catch {}
			},
		}),

		updateProviderKey: builder.mutation<ModelProviderKey, { provider: string; keyId: string; key: UpdateProviderKeyRequest }>({
			query: ({ provider, keyId, key }) => ({
				url: `/providers/${encodeURIComponent(provider)}/keys/${encodeURIComponent(keyId)}`,
				method: "PUT",
				body: key,
			}),
			async onQueryStarted({ provider, keyId }, { dispatch, queryFulfilled }) {
				try {
					const { data: updatedKey } = await queryFulfilled;
					dispatch(
						providersApi.util.updateQueryData("getProviderKeys", provider, (draft) => {
							const index = draft.findIndex((key) => key.id === keyId);
							if (index !== -1) {
								draft[index] = updatedKey;
							}
						}),
					);
					dispatch(
						providersApi.util.updateQueryData("getProviderKey", { provider, keyId }, () => updatedKey),
					);
					dispatch(
						providersApi.util.updateQueryData("getAllKeys", undefined, (draft) => {
							const index = draft.findIndex((k) => k.key_id === keyId);
							if (index !== -1) {
								draft[index] = { ...draft[index], name: updatedKey.name, models: updatedKey.models ?? [] };
							}
						}),
					);
				} catch {}
			},
		}),

		deleteProviderKey: builder.mutation<ModelProviderKey, { provider: string; keyId: string }>({
			query: ({ provider, keyId }) => ({
				url: `/providers/${encodeURIComponent(provider)}/keys/${encodeURIComponent(keyId)}`,
				method: "DELETE",
			}),
			async onQueryStarted({ provider, keyId }, { dispatch, queryFulfilled }) {
				try {
					await queryFulfilled;
					dispatch(
						providersApi.util.updateQueryData("getProviderKeys", provider, (draft) => {
							const index = draft.findIndex((key) => key.id === keyId);
							if (index !== -1) {
								draft.splice(index, 1);
							}
						}),
					);
					dispatch(
						providersApi.util.updateQueryData("getAllKeys", undefined, (draft) => {
							const index = draft.findIndex((k) => k.key_id === keyId);
							if (index !== -1) {
								draft.splice(index, 1);
							}
						}),
					);
				} catch {}
			},
		}),

		// Delete provider
		deleteProvider: builder.mutation<ModelProviderName, string>({
			query: (provider) => ({
				url: `/providers/${encodeURIComponent(provider)}`,
				method: "DELETE",
			}),
			async onQueryStarted(providerName, { dispatch, queryFulfilled }) {
				try {
					await queryFulfilled;
					dispatch(
						providersApi.util.updateQueryData("getProviders", undefined, (draft) => {
							const index = draft.findIndex((p) => p.name === providerName);
							if (index !== -1) {
								draft.splice(index, 1);
							}
						}),
					);
					dispatch(
						providersApi.util.updateQueryData("getProviderKeys", providerName, (draft) => {
							draft.splice(0, draft.length);
						}),
					);
					dispatch(
						providersApi.util.updateQueryData("getAllKeys", undefined, (draft) =>
							draft.filter((key) => key.provider !== providerName),
						),
					);
				} catch {}
			},
		}),

		// Get all available keys from all providers for governance selection
		getAllKeys: builder.query<DBKey[], void>({
			query: () => "/keys",
			providesTags: ["DBKeys"],
		}),

		// Get models with optional filtering
		getModels: builder.query<ListModelsResponse, GetModelsRequest>({
			query: ({ query, provider, keys, vks, limit, unfiltered }) => {
				const params = new URLSearchParams();
				if (query) params.append("query", query);
				if (provider) params.append("provider", provider);
				if (keys && keys.length > 0) params.append("keys", keys.join(","));
				if (vks && vks.length > 0) params.append("vks", vks.join(","));
				if (limit !== undefined) params.append("limit", limit.toString());
				if (unfiltered !== undefined) params.append("unfiltered", unfiltered.toString());
				return `/models?${params.toString()}`;
			},
			providesTags: ["Models"],
		}),

		// Get distinct base model names from the catalog
		getBaseModels: builder.query<ListBaseModelsResponse, GetBaseModelsRequest>({
			query: ({ query, limit }) => {
				const params = new URLSearchParams();
				if (query) params.append("query", query);
				if (limit !== undefined) params.append("limit", limit.toString());
				return `/models/base?${params.toString()}`;
			},
			providesTags: ["BaseModels"],
		}),

		// Get model parameters (parameters, capabilities) from local API
		// Falls back to default parameters if the API returns an error (e.g. model not found)
		getModelParameters: builder.query<ModelDatasheetResponse, string>({
			queryFn: async (model, _queryApi, _extraOptions, baseQuery) => {
				const result = await baseQuery(`/models/parameters?model=${encodeURIComponent(model)}`);
				if (result.error) {
					// If the model is not found, return the default parameters
					if ((result.error as any)?.status === 404) {
						return { data: DEFAULT_MODEL_PARAMETERS };
					}
					return { error: result.error };
				}
				return { data: result.data as ModelDatasheetResponse };
			},
		}),
	}),
});

export const {
	useGetProvidersQuery,
	useGetProviderQuery,
	useGetProviderKeysQuery,
	useGetProviderKeyQuery,
	useCreateProviderMutation,
	useUpdateProviderMutation,
	useCreateProviderKeyMutation,
	useUpdateProviderKeyMutation,
	useDeleteProviderKeyMutation,
	useDeleteProviderMutation,
	useGetAllKeysQuery,
	useGetModelsQuery,
	useGetBaseModelsQuery,
	useLazyGetProvidersQuery,
	useLazyGetProviderQuery,
	useLazyGetProviderKeysQuery,
	useLazyGetProviderKeyQuery,
	useLazyGetAllKeysQuery,
	useLazyGetModelsQuery,
	useLazyGetBaseModelsQuery,
	useGetModelParametersQuery,
	useLazyGetModelParametersQuery,
} = providersApi;
