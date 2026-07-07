import { baseApi } from "./baseApi";

export interface OAuth2GrantRow {
	id: string;
	client_id: string;
	client_name?: string;
	bf_mode: "user" | "vk" | "session";
	bf_sub: string;
	bf_sub_display?: string; // human-readable: VK name for vk mode
	scope: string;
	created_at: string;
	last_used_at?: string | null;
}

export interface OAuth2GrantsListResponse {
	sessions: OAuth2GrantRow[];
	count?: number; // rows on this page
	total_count?: number; // total across all pages, after filters
	limit?: number;
	offset?: number;
}

export interface OAuth2GrantsQueryParams {
	q?: string;
	bf_mode?: string[];
	limit?: number;
	offset?: number;
}

// buildOAuth2GrantsListParams shapes the filter+page params for the URL.
// bf_mode is csv-joined (matches the backend's parseCommaSeparated) and sorted
// so selection order doesn't fragment the RTK Query cache key. Empty values
// are dropped so the key doesn't fragment on "" vs unset.
function buildOAuth2GrantsListParams(params?: OAuth2GrantsQueryParams) {
	if (!params) return {};
	const out: Record<string, string | number> = {};
	if (params.q) out.q = params.q;
	if (params.bf_mode?.length) out.bf_mode = [...params.bf_mode].sort().join(",");
	if (params.limit !== undefined) out.limit = params.limit;
	if (params.offset !== undefined) out.offset = params.offset;
	return out;
}

export const oauth2SessionsApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		// Each filter+page combo gets its own cache entry (RTK Query auto-derives
		// the key from the params object).
		getOAuth2Grants: builder.query<OAuth2GrantsListResponse, OAuth2GrantsQueryParams | void>({
			query: (params) => ({
				url: "/oauth2/sessions",
				params: buildOAuth2GrantsListParams(params ?? undefined),
			}),
			providesTags: ["OAuth2Grants"],
		}),
		revokeOAuth2Grant: builder.mutation<void, string>({
			query: (id) => ({ url: `/oauth2/sessions/${id}`, method: "DELETE" }),
			invalidatesTags: ["OAuth2Grants"],
		}),
	}),
});

export const { useGetOAuth2GrantsQuery, useRevokeOAuth2GrantMutation } = oauth2SessionsApi;
