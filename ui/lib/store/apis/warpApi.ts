import type {
	WarpBackfillInput,
	WarpBackfillStatus,
	WarpConfig,
	WarpConfigInput,
	WarpConversation,
	WarpConversationDetail,
	WarpLogIndexStatus,
} from "@/lib/types/warp";
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
		// The tray's one-glance indexing summary. Not admin-gated server-side,
		// unlike the backfill controls above.
		getWarpLogIndexStatus: builder.query<WarpLogIndexStatus, void>({
			query: () => ({ url: "/warp/log-index/status" }),
		}),
		// History. Threads are created by the chat stream, outside RTK, so the
		// list is refetched whenever the history view opens rather than relying
		// on invalidation alone.
		listWarpConversations: builder.query<WarpConversation[], { limit?: number } | void>({
			query: (arg) => ({ url: "/warp/conversations", params: arg?.limit ? { limit: arg.limit } : {} }),
			transformResponse: (response: { conversations: WarpConversation[] }) => response.conversations ?? [],
			providesTags: ["WarpConversations"],
		}),
		getWarpConversation: builder.query<WarpConversationDetail, string>({
			query: (id) => ({ url: `/warp/conversations/${encodeURIComponent(id)}` }),
		}),
		deleteWarpConversation: builder.mutation<void, string>({
			query: (id) => ({ url: `/warp/conversations/${encodeURIComponent(id)}`, method: "DELETE" }),
			invalidatesTags: ["WarpConversations"],
		}),
	}),
});

export const {
	useGetWarpConfigQuery,
	useUpdateWarpConfigMutation,
	useStartWarpBackfillMutation,
	useGetWarpBackfillStatusQuery,
	useCancelWarpBackfillMutation,
	useGetWarpLogIndexStatusQuery,
	useListWarpConversationsQuery,
	useLazyGetWarpConversationQuery,
	useDeleteWarpConversationMutation,
} = warpApi;