import { BifrostConfig, GlobalProxyConfig, LatestReleaseResponse } from "@/lib/types/config";
import axios from "axios";
import { baseApi } from "./baseApi";

const isPlainObject = (value: unknown): value is Record<string, unknown> =>
	typeof value === "object" && value !== null && !Array.isArray(value);

const applyMetadataPatch = (metadata: BifrostConfig["metadata"] | undefined, patch: Record<string, unknown>): Record<string, unknown> => {
	const next = { ...(metadata ?? {}) };
	Object.entries(patch).forEach(([key, value]) => {
		if (value === null) {
			delete next[key];
			return;
		}
		const currentValue = next[key];
		next[key] = isPlainObject(value) && isPlainObject(currentValue) ? applyMetadataPatch(currentValue, value) : value;
	});
	return next;
};

export const configApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		getUserAgentMappings: builder.query<{ mappings: UserAgentMapping[] }, void>({
			query: () => ({
				url: "/logs/user-agent-mappings",
			}),
			providesTags: ["UserAgentMappings"],
		}),
		createUserAgentMapping: builder.mutation<UserAgentMapping, UserAgentMappingPayload>({
			query: (data) => ({
				url: "/logs/user-agent-mappings",
				method: "POST",
				body: data,
			}),
			invalidatesTags: ["UserAgentMappings"],
		}),
		updateUserAgentMapping: builder.mutation<UserAgentMapping, { id: string; data: UserAgentMappingPayload }>({
			query: ({ id, data }) => ({
				url: `/logs/user-agent-mappings/${id}`,
				method: "PUT",
				body: data,
			}),
			invalidatesTags: ["UserAgentMappings"],
		}),
		deleteUserAgentMapping: builder.mutation<{ success: boolean }, string>({
			query: (id) => ({
				url: `/logs/user-agent-mappings/${id}`,
				method: "DELETE",
			}),
			invalidatesTags: ["UserAgentMappings"],
		}),

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
				body: data,
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

		// Merge-patch the ClientConfig.metadata UI/admin preferences blob.
		// Pass {key: null} to remove a key.
		updateClientMetadata: builder.mutation<{ success: boolean }, Record<string, unknown>>({
			query: (patch) => ({
				url: "/config/metadata",
				method: "POST",
				body: patch,
			}),
			async onQueryStarted(patch, { dispatch, queryFulfilled }) {
				const patchResults = [
					dispatch(
						configApi.util.updateQueryData("getCoreConfig", {}, (draft) => {
							draft.metadata = applyMetadataPatch(draft.metadata, patch);
						}),
					),
					dispatch(
						configApi.util.updateQueryData("getCoreConfig", { fromDB: true }, (draft) => {
							draft.metadata = applyMetadataPatch(draft.metadata, patch);
						}),
					),
				];
				try {
					await queryFulfilled;
				} catch {
					patchResults.forEach((patchResult) => patchResult.undo());
				}
			},
		}),
	}),
});

export type UserAgentMappingMatchType = "contains" | "starts_with" | "exact" | "regex";

export interface UserAgentMapping {
	id: string;
	pattern: string;
	match_type: UserAgentMappingMatchType;
	app: string;
	logo?: string;
	logo_mime?: string | null;
	is_active: boolean;
	created_at: string;
	updated_at: string;
}

export interface UserAgentMappingPayload {
	pattern: string;
	match_type: UserAgentMappingMatchType;
	app: string;
	logo?: string;
	logo_mime?: string | null;
	is_active: boolean;
}

export const {
	useGetVersionQuery,
	useGetCoreConfigQuery,
	useUpdateCoreConfigMutation,
	useUpdateProxyConfigMutation,
	useForcePricingSyncMutation,
	useUpdateClientMetadataMutation,
	useLazyGetCoreConfigQuery,
	useGetLatestReleaseQuery,
	useLazyGetLatestReleaseQuery,
	useGetUserAgentMappingsQuery,
	useCreateUserAgentMappingMutation,
	useUpdateUserAgentMappingMutation,
	useDeleteUserAgentMappingMutation,
} = configApi;
