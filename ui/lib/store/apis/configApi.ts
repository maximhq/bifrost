import { BifrostConfig, CoreConfig, VectorStoreConfig, LogStoreConfig } from "@/lib/types/config";
import { baseApi } from "./baseApi";

export const configApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		// Get core configuration
		getCoreConfig: builder.query<BifrostConfig, { fromDB?: boolean }>({
			query: ({ fromDB = false } = {}) => ({
				url: "/config",
				params: { from_db: fromDB },
			}),
			providesTags: ["Config"],
		}),

		// Get version information
		getVersion: builder.query<string, void>({
			query: () => ({
				url: "/version",
			}),
		}),

		// Update core configuration
		updateCoreConfig: builder.mutation<null, CoreConfig>({
			query: (data) => ({
				url: "/config",
				method: "PUT",
				body: data,
			}),
			invalidatesTags: ["Config"],
		}),

		// Get vector store configuration
		getVectorStoreConfig: builder.query<VectorStoreConfig, void>({
			query: () => ({
				url: "/config/vector-store",
			}),
			providesTags: ["VectorStoreConfig"],
		}),

		// Update vector store configuration
		updateVectorStoreConfig: builder.mutation<null, VectorStoreConfig>({
			query: (data) => ({
				url: "/config/vector-store",
				method: "PUT",
				body: data,
			}),
			invalidatesTags: ["VectorStoreConfig", "Config"],
		}),

		// Get log store configuration
		getLogStoreConfig: builder.query<LogStoreConfig, void>({
			query: () => ({
				url: "/config/log-store",
			}),
			providesTags: ["LogStoreConfig"],
		}),

		// Update log store configuration
		updateLogStoreConfig: builder.mutation<null, LogStoreConfig>({
			query: (data) => ({
				url: "/config/log-store",
				method: "PUT",
				body: data,
			}),
			invalidatesTags: ["LogStoreConfig", "Config"],
		}),
	}),
});

export const { 
	useGetVersionQuery, 
	useGetCoreConfigQuery, 
	useUpdateCoreConfigMutation, 
	useLazyGetCoreConfigQuery,
	useGetVectorStoreConfigQuery,
	useUpdateVectorStoreConfigMutation,
	useGetLogStoreConfigQuery,
	useUpdateLogStoreConfigMutation
} = configApi;
