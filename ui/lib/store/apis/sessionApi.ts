import type { AuthMethodsResponse } from "@/lib/types/sso";
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
		}),
		// Get available auth methods (SSO, password, etc.)
		authMethods: builder.query<AuthMethodsResponse, void>({
			query: () => ({
				url: "/session/is-auth-enabled",
				method: "GET",
			}),
		}),
		// Login endpoint
		login: builder.mutation<LoginResponse, LoginRequest>({
			query: (credentials) => ({
				url: "/session/login",
				method: "POST",
				body: credentials,
			}),
			invalidatesTags: [],
		}),

		// Logout endpoint
		logout: builder.mutation<LogoutResponse, void>({
			query: () => ({
				url: "/session/logout",
				method: "POST",
			}),
			// After logout, clear token and all cached data
			async onQueryStarted(arg, { queryFulfilled }) {
				try {
					await queryFulfilled;
				} catch (error) {
				} finally {
					clearAuthStorage();
				}
			},
			invalidatesTags: ["Config", "Providers", "Logs", "VirtualKeys", "Teams", "Customers", "Budgets", "RateLimits"],
		}),
	}),
});

export const { useIsAuthEnabledQuery, useAuthMethodsQuery, useLoginMutation, useLogoutMutation } = sessionApi;
