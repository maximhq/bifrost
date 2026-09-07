import { stopWebSocket } from "@/hooks/useWebSocket";
import { beginLogout, endLogout } from "@/lib/utils/logoutState";
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
			async onQueryStarted(_arg, { queryFulfilled }) {
				await queryFulfilled;
				// A new session: 401s are meaningful again.
				endLogout();
			},
			invalidatesTags: ["Sessions"],
		}),

		// Logout endpoint
		logout: builder.mutation<LogoutResponse, void>({
			async queryFn(_arg, _api, _extraOptions, baseQuery) {
				// Latch first, so 401s from queries still in flight do not each
				// trigger a refresh + another logout, and drop the websocket so it
				// stops requesting tickets for the session being destroyed.
				beginLogout();
				stopWebSocket();

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
			// After logout, clear local auth state. The RTK cache is reset by the
			// caller (see Topbar.handleLogout) only after navigating away: resetting
			// it here, while the dashboard's polled queries are still mounted, makes
			// every one of them refetch immediately and fail with a 401.
			async onQueryStarted(_arg, { queryFulfilled }) {
				try {
					await queryFulfilled;
				} catch {
				} finally {
					clearAuthStorage();
				}
			},
			invalidatesTags: ["Sessions", "Config", "Providers", "Logs", "VirtualKeys", "Teams", "Customers", "Budgets", "RateLimits"],
		}),
	}),
});

export const { useIsAuthEnabledQuery, useLoginMutation, useLogoutMutation } = sessionApi;