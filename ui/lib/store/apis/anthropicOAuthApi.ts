import { baseApi } from "./baseApi";

export interface InitiateAnthropicOAuthResponse {
	authorize_url: string;
	oauth_config_id: string;
	expires_at: string;
}

export interface ExchangeAnthropicOAuthRequest {
	code: string;
	oauth_config_id: string;
}

export interface AnthropicOAuthStatusResponse {
	oauth_config_id: string;
	status: "valid" | "invalid";
}

export interface AnthropicOAuthConfigIdRequest {
	oauth_config_id: string;
}

export interface AnthropicOAuthActionResponse {
	status: "success";
}

export const anthropicOAuthApi = baseApi.injectEndpoints({
	overrideExisting: false,
	endpoints: (builder) => ({
		initiateAnthropicOAuth: builder.mutation<InitiateAnthropicOAuthResponse, void>({
			query: () => ({
				url: "/anthropic-oauth/initiate",
				method: "POST",
			}),
		}),
		exchangeAnthropicOAuthCode: builder.mutation<AnthropicOAuthActionResponse, ExchangeAnthropicOAuthRequest>({
			query: (body) => ({
				url: "/anthropic-oauth/exchange",
				method: "POST",
				body,
			}),
		}),
		getAnthropicOAuthStatus: builder.query<AnthropicOAuthStatusResponse, string>({
			query: (oauthConfigId) => ({
				url: "/anthropic-oauth/status",
				params: { oauth_config_id: oauthConfigId },
			}),
		}),
		refreshAnthropicOAuthToken: builder.mutation<AnthropicOAuthActionResponse, AnthropicOAuthConfigIdRequest>({
			query: (body) => ({
				url: "/anthropic-oauth/refresh",
				method: "POST",
				body,
			}),
		}),
		logoutAnthropicOAuth: builder.mutation<AnthropicOAuthActionResponse, AnthropicOAuthConfigIdRequest>({
			query: (body) => ({
				url: "/anthropic-oauth/logout",
				method: "POST",
				body,
			}),
		}),
	}),
});

export const {
	useInitiateAnthropicOAuthMutation,
	useExchangeAnthropicOAuthCodeMutation,
	useLazyGetAnthropicOAuthStatusQuery,
	useRefreshAnthropicOAuthTokenMutation,
	useLogoutAnthropicOAuthMutation,
} = anthropicOAuthApi;
