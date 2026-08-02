import { FeatureFlagStatus, FeatureFlagsListResponse, UpdateFeatureFlagRequest } from "@/lib/types/featureFlag";
import { baseApi } from "./baseApi";

export const featureFlagsApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		// List all known feature flags (registered + any stale overrides).
		listFeatureFlags: builder.query<FeatureFlagsListResponse, void>({
			query: () => ({
				url: "/feature-flags",
			}),
			providesTags: ["FeatureFlags"],
		}),

		// Toggle a single feature flag. The backend returns the updated
		// FlagStatus so callers can optimistically update the cached list
		// without a follow-up GET, but we keep this simple by invalidating
		// the list tag and letting the next render fetch a fresh snapshot.
		updateFeatureFlag: builder.mutation<FeatureFlagStatus, UpdateFeatureFlagRequest>({
			query: ({ id, enabled }) => ({
				url: `/feature-flags/${encodeURIComponent(id)}`,
				method: "PUT",
				body: { enabled },
			}),
			invalidatesTags: ["FeatureFlags"],
		}),
	}),
});

export const { useListFeatureFlagsQuery, useUpdateFeatureFlagMutation } = featureFlagsApi;