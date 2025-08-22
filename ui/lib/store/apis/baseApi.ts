import { BifrostErrorResponse } from "@/lib/types/config";
import { getApiBaseUrl } from "@/lib/utils/port";
import { createApi, fetchBaseQuery } from "@reduxjs/toolkit/query/react";

// Define the base query with error handling
const baseQuery = fetchBaseQuery({
	baseUrl: getApiBaseUrl(),
	prepareHeaders: (headers) => {
		headers.set("Content-Type", "application/json");
		return headers;
	},
});

// Enhanced base query with error handling
const baseQueryWithErrorHandling = async (args: any, api: any, extraOptions: any) => {
	const result = await baseQuery(args, api, extraOptions);

	if (result.error) {
		// Handle specific error types
		if (result.error.status === "FETCH_ERROR") {
			// Network error
			return {
				...result,
				error: {
					...result.error,
					data: {
						error: {
							message: "Network error: Unable to connect to the server",
						},
					},
				},
			};
		}

		// Handle other errors with proper BifrostErrorResponse format
		if (result.error.data) {
			const errorData = result.error.data as BifrostErrorResponse;
			if (errorData.error?.message) {
				return result;
			}
		}

		// Fallback error message
		return {
			...result,
			error: {
				...result.error,
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
		"Plugins",
	],
	endpoints: () => ({}),
});

// Helper function to extract error message from RTK Query error
export const getErrorMessage = (error: any): string => {
	if (error?.data?.error?.message) {
		return error.data.error.message;
	}
	if (error?.message) {
		return error.message;
	}
	return "An unexpected error occurred";
};
