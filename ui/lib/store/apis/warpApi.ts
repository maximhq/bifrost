import type { WarpConfig, WarpConfigInput } from "@/lib/types/warp";
import { baseApi } from "./baseApi";

export const warpApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		getWarpConfig: builder.query<WarpConfig, void>({
			query: () => ({ url: "/warp/config" }),
			providesTags: ["WarpConfig"],
		}),
		updateWarpConfig: builder.mutation<WarpConfig, WarpConfigInput>({
			query: (body) => ({ url: "/warp/config", method: "PUT", body }),
			invalidatesTags: ["WarpConfig"],
		}),
	}),
});

export const { useGetWarpConfigQuery, useUpdateWarpConfigMutation } = warpApi;