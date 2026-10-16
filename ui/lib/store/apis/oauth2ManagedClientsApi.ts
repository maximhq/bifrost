import { baseApi } from "./baseApi";

export type ManagedClientIdentity = "vk" | "user";

// A client allowed to use the jwt-bearer (ID-JAG) grant. client_secret is only
// present in the create and rotate-secret responses.
export interface OAuth2ManagedClient {
	id: string;
	client_id: string;
	name: string;
	identity: ManagedClientIdentity;
	virtual_key_id?: string;
	disabled: boolean;
	has_secret: boolean;
	client_secret?: string;
	last_used_at?: string | null;
	created_at: string;
	updated_at: string;
}

export interface OAuth2ManagedClientsListResponse {
	clients: OAuth2ManagedClient[];
	user_identity_available: boolean;
	suggested_trusted_issuer?: string;
}

export interface CreateOAuth2ManagedClientRequest {
	name?: string;
	client_id?: string;
	public?: boolean;
	identity: ManagedClientIdentity;
	virtual_key_id?: string;
}

export interface UpdateOAuth2ManagedClientRequest {
	id: string;
	name?: string;
	identity?: ManagedClientIdentity;
	virtual_key_id?: string;
	disabled?: boolean;
}

export const oauth2ManagedClientsApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		getOAuth2ManagedClients: builder.query<OAuth2ManagedClientsListResponse, void>({
			query: () => ({ url: "/oauth2/managed-clients" }),
			providesTags: ["OAuth2ManagedClients"],
		}),
		createOAuth2ManagedClient: builder.mutation<OAuth2ManagedClient, CreateOAuth2ManagedClientRequest>({
			query: (body) => ({ url: "/oauth2/managed-clients", method: "POST", body }),
			invalidatesTags: ["OAuth2ManagedClients"],
		}),
		updateOAuth2ManagedClient: builder.mutation<OAuth2ManagedClient, UpdateOAuth2ManagedClientRequest>({
			query: ({ id, ...body }) => ({ url: `/oauth2/managed-clients/${id}`, method: "PATCH", body }),
			invalidatesTags: ["OAuth2ManagedClients"],
		}),
		deleteOAuth2ManagedClient: builder.mutation<void, string>({
			query: (id) => ({ url: `/oauth2/managed-clients/${id}`, method: "DELETE" }),
			invalidatesTags: ["OAuth2ManagedClients"],
		}),
		rotateOAuth2ManagedClientSecret: builder.mutation<OAuth2ManagedClient, string>({
			query: (id) => ({ url: `/oauth2/managed-clients/${id}/rotate-secret`, method: "POST" }),
			invalidatesTags: ["OAuth2ManagedClients"],
		}),
	}),
});

export const {
	useGetOAuth2ManagedClientsQuery,
	useCreateOAuth2ManagedClientMutation,
	useUpdateOAuth2ManagedClientMutation,
	useDeleteOAuth2ManagedClientMutation,
	useRotateOAuth2ManagedClientSecretMutation,
} = oauth2ManagedClientsApi;