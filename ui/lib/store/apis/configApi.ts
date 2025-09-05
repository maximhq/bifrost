import { BifrostConfig, CoreConfig } from "@/lib/types/config";
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
	}),
});

export const { useGetVersionQuery, useGetCoreConfigQuery, useUpdateCoreConfigMutation, useLazyGetCoreConfigQuery } = configApi;
