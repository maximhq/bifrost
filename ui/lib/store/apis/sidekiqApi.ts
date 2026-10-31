import type { CancelSidekiqJobResponse, SidekiqJobsResponse } from "@/lib/types/sidekiq";
import { baseApi } from "./baseApi";

export const sidekiqApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		// Active background jobs plus those finished in the last 24h. Meant to be polled.
		getSidekiqJobs: builder.query<SidekiqJobsResponse, void>({
			query: () => ({ url: "/sidekiq/jobs" }),
			providesTags: ["SidekiqJobs"],
		}),

		// Stop a pending or running job. Work it already committed is kept.
		cancelSidekiqJob: builder.mutation<CancelSidekiqJobResponse, string>({
			query: (id) => ({ url: `/sidekiq/jobs/${encodeURIComponent(id)}/cancel`, method: "POST" }),
			invalidatesTags: ["SidekiqJobs"],
		}),
	}),
});

export const { useGetSidekiqJobsQuery, useCancelSidekiqJobMutation } = sidekiqApi;