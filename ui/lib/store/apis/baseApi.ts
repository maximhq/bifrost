import { IS_ENTERPRISE } from "@/lib/constants/config";
import { BifrostErrorResponse } from "@/lib/types/config";
import { getApiBaseUrl } from "@/lib/utils/port";
import { createBaseQueryWithRefresh } from "@enterprise/lib/store/utils/baseQueryWithRefresh";
import { clearOAuthStorage, getAccessToken } from "@enterprise/lib/store/utils/tokenManager";
import { createApi, fetchBaseQuery } from "@reduxjs/toolkit/query/react";

// Helper function to get token from localStorage
// If enterprise, use access_token from enterprise tokenManager; otherwise use bifrost-auth-token
export const getTokenFromStorage = (): Promise<string | null> => {
	if (typeof window === "undefined") {
		return Promise.resolve(null);
	}
	try {
		if (IS_ENTERPRISE) {
			// Enterprise OAuth login - use tokenManager
			return getAccessToken();
		} else {
			// Traditional login - use bifrost-auth-token
			return Promise.resolve(localStorage.getItem("bifrost-auth-token"));
		}
	} catch (error) {
		return Promise.resolve(null);
	}
};

// Helper function to set token in localStorage (non-enterprise only)
export const setAuthToken = (token: string | null) => {
	if (typeof window === "undefined") {
		return;
	}
	try {
		if (token) {
			localStorage.setItem("bifrost-auth-token", token);
		} else {
			localStorage.removeItem("bifrost-auth-token");
		}
	} catch (error) {
		throw new Error("Error setting token in localStorage");
	}
};

// Helper function to clear all auth-related storage
export const clearAuthStorage = () => {
	if (typeof window === "undefined") {
		return;
	}
	try {
		// Clear traditional auth token
		localStorage.removeItem("bifrost-auth-token");

		// Clear enterprise OAuth tokens using tokenManager
		if (IS_ENTERPRISE) {
			clearOAuthStorage();
		}
	} catch (error) {
		console.error("Error clearing auth storage:", error);
	}
};

// Define the base query with authentication headers
const baseQuery = fetchBaseQuery({
	baseUrl: getApiBaseUrl(),
	credentials: "include",
	prepareHeaders: async (headers) => {
		headers.set("Content-Type", "application/json");
		// Automatically include token from localStorage in Authorization header
		const token = await getTokenFromStorage();
		if (token) {
			headers.set("Authorization", `Bearer ${token}`);
		}
		return headers;
	},
});

// Wrap base query with enterprise refresh logic (or passthrough for non-enterprise)
const baseQueryWithRefresh = createBaseQueryWithRefresh(baseQuery);

// Enhanced base query with error handling
const baseQueryWithErrorHandling: typeof baseQueryWithRefresh = async (args: any, api: any, extraOptions: any) => {
	// First apply refresh logic (enterprise-specific, handles 401)
	const result = await baseQueryWithRefresh(args, api, extraOptions);

	// Then handle other error types
	if (result.error) {
		const error = result.error as any;

		// Handle 401 for non-enterprise (no refresh available)
		if (error?.status === 401 && !IS_ENTERPRISE) {
			clearAuthStorage();
			if (typeof window !== "undefined" && !window.location.pathname.includes("/login")) {
				window.location.href = "/login";
			}
			return result;
		}

		// Handle specific error types
		if (error?.status === "FETCH_ERROR") {
			// Network error
			return {
				...result,
				error: {
					...error,
					data: {
						error: {
							message: "Network error: Unable to connect to the server",
						},
					},
				},
			};
		}

		// Handle other errors with proper BifrostErrorResponse format
		if (error?.data) {
			const errorData = error.data as BifrostErrorResponse;
			if (errorData.error?.message) {
				return result;
			}
		}

		// Fallback error message
		return {
			...result,
			error: {
				...error,
				data: {
					error: {
						message: "An unexpected error occurred",
					},
				},
			},
		};
	}

	return result;
};

// Create the base API
export const baseApi = createApi({
	reducerPath: "api",
	baseQuery: baseQueryWithErrorHandling,
	tagTypes: [
		"Logs",
		"Providers",
		"MCPClients",
		"Config",
		"CacheConfig",
		"VirtualKeys",
		"Teams",
		"Customers",
		"Budgets",
		"RateLimits",
		"UsageStats",
		"DebugStats",
		"HealthCheck",
		"DBKeys",
		"Models",
		"ModelConfigs",
		"ProviderGovernance",
		"Plugins",
		"SCIMProviders",
		"User",
		"Guardrails",
		"ClusterNodes",
		"Users",
		"GuardrailRules",
		"Roles",
		"Resources",
		"Operations",
		"Permissions",
		"APIKeys",
		"OAuth2Config",
	],
	endpoints: () => ({}),
});

// Helper function to extract error message from RTK Query error
export const getErrorMessage = (error: unknown): string => {
	if (error === undefined || error === null) {
		return "An unexpected error occurred";
	}
	if (error instanceof Error) {
		return error.message;
	}
	if (
		typeof error === "object" &&
		error &&
		"data" in error &&
		error.data &&
		typeof error.data === "object" &&
		"error" in error.data &&
		error.data.error &&
		typeof error.data.error === "object" &&
		"message" in error.data.error &&
		typeof error.data.error.message === "string"
	) {
		return error.data.error.message.charAt(0).toUpperCase() + error.data.error.message.slice(1);
	}
	if (typeof error === "object" && error && "message" in error && typeof error.message === "string") {
		return error.message;
	}
	return "An unexpected error occurred";
};
