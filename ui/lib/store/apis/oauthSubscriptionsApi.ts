import { baseApi } from "./baseApi";

// Sign-in flows for OAuth subscription providers (antigravity, kiro). The server keeps the
// pending session in memory; the final step returns a `credential` string that goes verbatim
// into the key's value and is then saved through the normal provider key endpoints.

export interface AntigravityOAuthStartResponse {
	session_id: string;
	auth_url: string;
	redirect_uri: string;
	expires_at: string;
}

export interface AntigravityOAuthCompleteRequest {
	session_id: string;
	// Full redirected URL copied from the browser address bar, or the bare `code`.
	callback_url: string;
}

export interface AntigravityOAuthCompleteResponse {
	credential: string;
	email?: string;
	project_id?: string;
	suggested_name?: string;
}

export type KiroOAuthMethod = "builder-id" | "google" | "github";

export interface KiroOAuthStartResponse {
	session_id: string;
	user_code: string;
	verification_uri: string;
	verification_uri_complete?: string;
	expires_at: string;
	interval_seconds: number;
}

export type KiroOAuthPollStatus = "pending" | "complete" | "expired" | "error";

export interface KiroOAuthPollResponse {
	status: KiroOAuthPollStatus;
	credential?: string;
	suggested_name?: string;
	error?: string;
	interval_seconds: number;
}

export const oauthSubscriptionsApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		startAntigravityOAuth: builder.mutation<AntigravityOAuthStartResponse, void>({
			query: () => ({ url: "/oauth-subscriptions/antigravity/start", method: "POST", body: {} }),
		}),
		completeAntigravityOAuth: builder.mutation<AntigravityOAuthCompleteResponse, AntigravityOAuthCompleteRequest>({
			query: (body) => ({ url: "/oauth-subscriptions/antigravity/complete", method: "POST", body }),
		}),
		startKiroOAuth: builder.mutation<KiroOAuthStartResponse, { method: KiroOAuthMethod }>({
			query: (body) => ({ url: "/oauth-subscriptions/kiro/start", method: "POST", body }),
		}),
		pollKiroOAuth: builder.mutation<KiroOAuthPollResponse, { session_id: string }>({
			query: (body) => ({ url: "/oauth-subscriptions/kiro/poll", method: "POST", body }),
		}),
	}),
});

export const {
	useStartAntigravityOAuthMutation,
	useCompleteAntigravityOAuthMutation,
	useStartKiroOAuthMutation,
	usePollKiroOAuthMutation,
} = oauthSubscriptionsApi;
