import { IS_ENTERPRISE } from "@/lib/constants/config";
import { BifrostConfig, GlobalProxyConfig, LatestReleaseResponse } from "@/lib/types/config";
import axios from "axios";
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

		// Get latest release from public site
		getLatestRelease: builder.query<LatestReleaseResponse, void>({
			queryFn: async (_arg, { signal }) => {
				try {
					const response = await axios.get("https://getbifrost.ai/latest-release", {
						timeout: 3000, // 3 second timeout
						signal,
						headers: {
							Accept: "application/json",
						},
						maxRedirects: 5,
						validateStatus: (status) => status >= 200 && status < 300,
					});
					const data = response.data as any;
					const normalized: LatestReleaseResponse = {
						name: data.name ?? data.tag ?? data.version ?? "",
						changelogUrl: data.changelogUrl ?? data.changelog_url ?? "",
					};
					return { data: normalized };
				} catch (error) {
					if (axios.isAxiosError(error)) {
						if (error.code === "ECONNABORTED" || error.code === "ETIMEDOUT") {
							console.warn("Latest release fetch timed out after 3s");
							return {
								error: {
									status: "TIMEOUT_ERROR",
									error: "Request timeout",
									data: { error: { message: "Request timeout" } },
								},
							};
						}
						console.error("Latest release fetch error:", error.message);
					} else {
						console.error("Latest release fetch error:", error);
					}
					return {
						error: {
							status: "FETCH_ERROR",
							error: String(error),
							data: { error: { message: "Network error" } },
						},
					};
				}
			},
			keepUnusedDataFor: 300, // Cache for 5 minutes (seconds)
		}),
		// Update core configuration
		updateCoreConfig: builder.mutation<null, BifrostConfig>({
			query: (data) => ({
				url: "/config",
				method: "PUT",
				body: IS_ENTERPRISE ? { ...data, auth_config: undefined } : data,
			}),
			invalidatesTags: ["Config"],
		}),

		// Update proxy configuration
		updateProxyConfig: builder.mutation<null, GlobalProxyConfig>({
			query: (data) => ({
				url: "/proxy-config",
				method: "PUT",
				body: data,
			}),
			invalidatesTags: ["Config"],
		}),

		// Force a pricing sync immediately
		forcePricingSync: builder.mutation<null, void>({
			query: () => ({
				url: "/pricing/force-sync",
				method: "POST",
			}),
			invalidatesTags: ["Config"],
		}),
	}),
});

export const {
	useGetVersionQuery,
	useGetCoreConfigQuery,
	useUpdateCoreConfigMutation,
	useUpdateProxyConfigMutation,
	useForcePricingSyncMutation,
	useLazyGetCoreConfigQuery,
	useGetLatestReleaseQuery,
	useLazyGetLatestReleaseQuery,
} = configApi;
