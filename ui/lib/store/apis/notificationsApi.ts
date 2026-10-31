import type { NotificationListResponse } from "@/lib/types/notifications";
import { baseApi } from "./baseApi";

export const notificationsApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		getNotifications: builder.query<NotificationListResponse, { cursor?: string; limit?: number } | void>({
			query: (params) => ({ url: "/notifications", params: params || { limit: 50 } }),
			providesTags: ["Notifications"],
		}),
	}),
});

export const { useGetNotificationsQuery } = notificationsApi;