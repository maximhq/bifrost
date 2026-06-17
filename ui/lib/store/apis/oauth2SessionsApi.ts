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
}

export const oauth2SessionsApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		getOAuth2Grants: builder.query<OAuth2GrantsListResponse, void>({
			query: () => ({ url: "/oauth2/sessions" }),
			providesTags: ["OAuth2Grants"],
		}),
		revokeOAuth2Grant: builder.mutation<void, string>({
			query: (id) => ({ url: `/oauth2/sessions/${id}`, method: "DELETE" }),
			invalidatesTags: ["OAuth2Grants"],
		}),
	}),
});

export const { useGetOAuth2GrantsQuery, useRevokeOAuth2GrantMutation } = oauth2SessionsApi;
