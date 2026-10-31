import type {
	CreateRSSFeedRequest,
	RSSFeed,
	RSSFeedListResponse,
	RSSItemsResponse,
	RSSSettings,
	UpdateRSSFeedRequest,
} from "@/lib/types/rssFeeds";
import { baseApi } from "./baseApi";

export const rssFeedsApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		getRSSFeeds: builder.query<RSSFeedListResponse, void>({
			query: () => ({ url: "/rss/feeds" }),
			providesTags: ["RSSFeeds"],
		}),
		getRSSItems: builder.query<RSSItemsResponse, { limit?: number } | void>({
			query: (params) => ({ url: "/rss/items", params: params || { limit: 50 } }),
			providesTags: ["RSSItems"],
		}),
		createRSSFeed: builder.mutation<RSSFeed, CreateRSSFeedRequest>({
			query: (body) => ({ url: "/rss/feeds", method: "POST", body }),
			invalidatesTags: ["RSSFeeds", "RSSItems"],
		}),
		updateRSSFeed: builder.mutation<RSSFeed, UpdateRSSFeedRequest>({
			query: ({ id, ...body }) => ({ url: `/rss/feeds/${id}`, method: "PUT", body }),
			// Optimistic so a toggle flips immediately; rolled back if the server refuses.
			async onQueryStarted({ id, ...patch }, { dispatch, queryFulfilled }) {
				const result = dispatch(
					rssFeedsApi.util.updateQueryData("getRSSFeeds", undefined, (draft) => {
						const feed = draft.feeds.find((f) => f.id === id);
						if (feed) Object.assign(feed, patch);
					}),
				);
				try {
					await queryFulfilled;
				} catch {
					result.undo();
				}
			},
			invalidatesTags: ["RSSFeeds", "RSSItems"],
		}),
		getRSSSettings: builder.query<RSSSettings, void>({
			query: () => ({ url: "/rss/settings" }),
			providesTags: ["RSSSettings"],
		}),
		updateRSSSettings: builder.mutation<RSSSettings, RSSSettings>({
			query: (body) => ({ url: "/rss/settings", method: "PUT", body }),
			async onQueryStarted(patch, { dispatch, queryFulfilled }) {
				const result = dispatch(rssFeedsApi.util.updateQueryData("getRSSSettings", undefined, (draft) => Object.assign(draft, patch)));
				try {
					await queryFulfilled;
				} catch {
					result.undo();
				}
			},
			invalidatesTags: ["RSSSettings", "RSSItems"],
		}),
		deleteRSSFeed: builder.mutation<{ message: string }, number>({
			query: (id) => ({ url: `/rss/feeds/${id}`, method: "DELETE" }),
			invalidatesTags: ["RSSFeeds", "RSSItems"],
		}),
	}),
});

export const {
	useGetRSSFeedsQuery,
	useGetRSSItemsQuery,
	useCreateRSSFeedMutation,
	useUpdateRSSFeedMutation,
	useDeleteRSSFeedMutation,
	useGetRSSSettingsQuery,
	useUpdateRSSSettingsMutation,
} = rssFeedsApi;