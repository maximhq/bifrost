import type { WarpBackfillInput, WarpBackfillStatus, WarpConfig, WarpConfigInput } from "@/lib/types/warp";
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
		startWarpBackfill: builder.mutation<WarpBackfillStatus, WarpBackfillInput>({
			query: (body) => ({ url: "/warp/log-index/backfill", method: "POST", body }),
		}),
		getWarpBackfillStatus: builder.query<WarpBackfillStatus, { id?: string } | void>({
			query: (arg) => ({ url: "/warp/log-index/backfill/status", params: arg?.id ? { id: arg.id } : {} }),
		}),
		cancelWarpBackfill: builder.mutation<WarpBackfillStatus, { id?: string } | void>({
			query: (body) => ({ url: "/warp/log-index/backfill/cancel", method: "POST", body: body ?? {} }),
		}),
	}),
});

export const {
	useGetWarpConfigQuery,
	useUpdateWarpConfigMutation,
	useStartWarpBackfillMutation,
	useGetWarpBackfillStatusQuery,
	useCancelWarpBackfillMutation,
} = warpApi;