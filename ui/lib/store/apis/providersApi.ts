import { AddProviderRequest, ListProvidersResponse, ModelProvider, ModelProviderName } from "@/lib/types/config";
import { DBKey } from "@/lib/types/governance";
import { baseApi } from "./baseApi";

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
			query: (provider) => `/providers/${provider}`,
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
				url: `/providers/${provider.name}`,
				method: "PUT",
				body: provider,
			}),
			invalidatesTags: (result, error, provider) => ["Providers", { type: "Providers", id: provider.name }],
		}),

		// Delete provider
		deleteProvider: builder.mutation<ModelProviderName, string>({
			query: (provider) => ({
				url: `/providers/${provider}`,
				method: "DELETE",
			}),
			invalidatesTags: ["Providers"],
		}),

		// Get all available keys from all providers for governance selection
		getAllKeys: builder.query<DBKey[], void>({
			query: () => "/keys",
			providesTags: ["DBKeys"],
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
	useLazyGetProvidersQuery,
	useLazyGetProviderQuery,
	useLazyGetAllKeysQuery,
} = providersApi;
