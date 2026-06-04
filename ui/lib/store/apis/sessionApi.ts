import { baseApi, clearAuthStorage } from "./baseApi";

export interface LoginRequest {
	username: string;
	password: string;
}

export interface LoginResponse {
	message: string;
}

export interface IsAuthEnabledResponse {
	is_auth_enabled: boolean;
	has_valid_token: boolean;
	auth_type?: "sso" | "password" | "none";
}

export interface LogoutResponse {
	message: string;
}

export const sessionApi = baseApi.injectEndpoints({
	overrideExisting: false,
	endpoints: (builder) => ({
		// Check if auth is enabled
		isAuthEnabled: builder.query<IsAuthEnabledResponse, void>({
			query: () => ({
				url: "/session/is-auth-enabled",
				method: "GET",
			}),
			providesTags: ["Sessions"],
		}),
		// Login endpoint
		login: builder.mutation<LoginResponse, LoginRequest>({
			query: (credentials) => ({
				url: "/session/login",
				method: "POST",
				body: credentials,
			}),
			invalidatesTags: ["Sessions"],
		}),

		// Logout endpoint
		logout: builder.mutation<LogoutResponse, void>({
			async queryFn(_arg, _api, _extraOptions, baseQuery) {
				const passwordLogout = await baseQuery({
					url: "/session/logout",
					method: "POST",
				});

				const oauthLogout = await baseQuery({
					url: "/scim/oauth/logout",
					method: "POST",
				});

				if (passwordLogout.error && oauthLogout.error) {
					return { error: oauthLogout.error };
				}

				return { data: { message: "Logout successful" } };
			},
			// After logout, clear token and all cached data
			async onQueryStarted(arg, { dispatch, queryFulfilled }) {
				try {
					await queryFulfilled;
				} catch {
				} finally {
					clearAuthStorage();
					dispatch(baseApi.util.resetApiState());
				}
			},
			invalidatesTags: ["Sessions", "Config", "Providers", "Logs", "VirtualKeys", "Teams", "Customers", "Budgets", "RateLimits"],
		}),
	}),
});

export const { useIsAuthEnabledQuery, useLoginMutation, useLogoutMutation } = sessionApi;