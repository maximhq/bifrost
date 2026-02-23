import { AddProviderRequest, ListProvidersResponse, ModelProvider, ModelProviderName } from "@/lib/types/config";
import { DBKey } from "@/lib/types/governance";
import { baseApi } from "./baseApi";

function sortProviders (a: ModelProvider, b: ModelProvider) {
	const aIsCustom = !!a.custom_provider_config
	const bIsCustom = !!b.custom_provider_config
	if (aIsCustom !== bIsCustom) return aIsCustom ? 1 : -1
	return a.name.localeCompare(b.name)
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
	limit?: number;
}

export interface GetBaseModelsRequest {
	query?: string;
	limit?: number;
}

export interface ListBaseModelsResponse {
	models: string[];
	total: number;
}

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
						})
					);
				} catch {}
			},
		}),

		// Update existing provider
		updateProvider: builder.mutation<ModelProvider, ModelProvider>({
			query: (provider) => ({
				url: `/providers/${encodeURIComponent(provider.name)}`,
				method: "PUT",
				body: provider,
			}),
			async onQueryStarted(arg, { dispatch, queryFulfilled }) {
				try {
					const { data: updatedProvider } = await queryFulfilled;
					dispatch(
						providersApi.util.updateQueryData("getProviders", undefined, (draft) => {
							const index = draft.findIndex((p) => p.name === arg.name);
							if (index !== -1) {
								draft[index] = updatedProvider;
							}
						})
					);
					dispatch(providersApi.util.updateQueryData("getProvider", arg.name, () => updatedProvider));
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
						})
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
			query: ({ query, provider, keys, limit }) => {
				const params = new URLSearchParams();
				if (query) params.append("query", query);
				if (provider) params.append("provider", provider);
				if (keys && keys.length > 0) params.append("keys", keys.join(","));
				if (limit !== undefined) params.append("limit", limit.toString());
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
	}),
});

export const {
	useGetProvidersQuery,
	useGetProviderQuery,
	useCreateProviderMutation,
	useUpdateProviderMutation,
	useDeleteProviderMutation,
	useGetAllKeysQuery,
	useGetModelsQuery,
	useGetBaseModelsQuery,
	useLazyGetProvidersQuery,
	useLazyGetProviderQuery,
	useLazyGetAllKeysQuery,
	useLazyGetModelsQuery,
	useLazyGetBaseModelsQuery,
} = providersApi;
