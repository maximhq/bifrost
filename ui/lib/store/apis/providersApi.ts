import { AddProviderRequest, ListProvidersResponse, ModelProvider, ModelProviderName } from "@/lib/types/config";
import { DBKey } from "@/lib/types/governance";
import { baseApi } from "./baseApi";

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
			transformResponse: (response: ListProvidersResponse): ModelProvider[] => response.providers ?? [],
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
			invalidatesTags: ["Providers"],
		}),

		// Update existing provider
		updateProvider: builder.mutation<ModelProvider, ModelProvider>({
			query: (provider) => ({
				url: `/providers/${encodeURIComponent(provider.name)}`,
				method: "PUT",
				body: provider,
			}),
			invalidatesTags: (result, error, provider) => ["Providers", { type: "Providers", id: provider.name }],
		}),

		// Delete provider
		deleteProvider: builder.mutation<ModelProviderName, string>({
			query: (provider) => ({
				url: `/providers/${encodeURIComponent(provider)}`,
				method: "DELETE",
			}),
			invalidatesTags: ["Providers"],
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
