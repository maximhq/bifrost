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

export const { useGetCoreConfigQuery, useUpdateCoreConfigMutation, useLazyGetCoreConfigQuery } = configApi;
