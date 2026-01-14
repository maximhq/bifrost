import {
	Budget,
	CreateCustomerRequest,
	CreateTeamRequest,
	CreateVirtualKeyRequest,
	Customer,
	DebugStatsResponse,
	GetBudgetsResponse,
	GetCustomersResponse,
	GetRateLimitsResponse,
	GetTeamsResponse,
	GetUsageStatsResponse,
	GetVirtualKeysResponse,
	HealthCheckResponse,
	RateLimit,
	ResetUsageRequest,
	Team,
	UpdateBudgetRequest,
	UpdateCustomerRequest,
	UpdateRateLimitRequest,
	UpdateTeamRequest,
	UpdateVirtualKeyRequest,
	VirtualKey,
} from "@/lib/types/governance";
import { baseApi } from "./baseApi";

export const governanceApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		// Virtual Keys
		getVirtualKeys: builder.query<GetVirtualKeysResponse, void>({
			query: () => "/governance/virtual-keys",
			providesTags: ["VirtualKeys"],
		}),

		getVirtualKey: builder.query<{ virtual_key: VirtualKey }, string>({
			query: (vkId) => `/governance/virtual-keys/${vkId}`,
			providesTags: (result, error, vkId) => [{ type: "VirtualKeys", id: vkId }],
		}),

		createVirtualKey: builder.mutation<{ message: string; virtual_key: VirtualKey }, CreateVirtualKeyRequest>({
			query: (data) => ({
				url: "/governance/virtual-keys",
				method: "POST",
				body: data,
			}),
			invalidatesTags: ["VirtualKeys"],
		}),

		updateVirtualKey: builder.mutation<{ message: string; virtual_key: VirtualKey }, { vkId: string; data: UpdateVirtualKeyRequest }>({
			query: ({ vkId, data }) => ({
				url: `/governance/virtual-keys/${vkId}`,
				method: "PUT",
				body: data,
			}),
			invalidatesTags: (result, error, { vkId }) => ["VirtualKeys", { type: "VirtualKeys", id: vkId }],
		}),

		deleteVirtualKey: builder.mutation<{ message: string }, string>({
			query: (vkId) => ({
				url: `/governance/virtual-keys/${vkId}`,
				method: "DELETE",
			}),
			invalidatesTags: ["VirtualKeys"],
		}),

		// Teams
		getTeams: builder.query<GetTeamsResponse, { customerId?: string }>({
			query: ({ customerId } = {}) => ({
				url: "/governance/teams",
				params: customerId ? { customer_id: customerId } : {},
			}),
			providesTags: ["Teams"],
		}),

		getTeam: builder.query<{ team: Team }, string>({
			query: (teamId) => `/governance/teams/${teamId}`,
			providesTags: (result, error, teamId) => [{ type: "Teams", id: teamId }],
		}),

		createTeam: builder.mutation<{ message: string; team: Team }, CreateTeamRequest>({
			query: (data) => ({
				url: "/governance/teams",
				method: "POST",
				body: data,
			}),
			invalidatesTags: ["Teams"],
		}),

		updateTeam: builder.mutation<{ message: string; team: Team }, { teamId: string; data: UpdateTeamRequest }>({
			query: ({ teamId, data }) => ({
				url: `/governance/teams/${teamId}`,
				method: "PUT",
				body: data,
			}),
			invalidatesTags: (result, error, { teamId }) => ["Teams", { type: "Teams", id: teamId }],
		}),

		deleteTeam: builder.mutation<{ message: string }, string>({
			query: (teamId) => ({
				url: `/governance/teams/${teamId}`,
				method: "DELETE",
			}),
			invalidatesTags: ["Teams"],
		}),

		// Customers
		getCustomers: builder.query<GetCustomersResponse, void>({
			query: () => "/governance/customers",
			providesTags: ["Customers"],
		}),

		getCustomer: builder.query<{ customer: Customer }, string>({
			query: (customerId) => `/governance/customers/${customerId}`,
			providesTags: (result, error, customerId) => [{ type: "Customers", id: customerId }],
		}),

		createCustomer: builder.mutation<{ message: string; customer: Customer }, CreateCustomerRequest>({
			query: (data) => ({
				url: "/governance/customers",
				method: "POST",
				body: data,
			}),
			invalidatesTags: ["Customers"],
		}),

		updateCustomer: builder.mutation<{ message: string; customer: Customer }, { customerId: string; data: UpdateCustomerRequest }>({
			query: ({ customerId, data }) => ({
				url: `/governance/customers/${customerId}`,
				method: "PUT",
				body: data,
			}),
			invalidatesTags: (result, error, { customerId }) => ["Customers", { type: "Customers", id: customerId }],
		}),

		deleteCustomer: builder.mutation<{ message: string }, string>({
			query: (customerId) => ({
				url: `/governance/customers/${customerId}`,
				method: "DELETE",
			}),
			invalidatesTags: ["Customers"],
		}),

		// Budgets
		getBudgets: builder.query<GetBudgetsResponse, void>({
			query: () => "/governance/budgets",
			providesTags: ["Budgets"],
		}),

		getBudget: builder.query<{ budget: Budget }, string>({
			query: (budgetId) => `/governance/budgets/${budgetId}`,
			providesTags: (result, error, budgetId) => [{ type: "Budgets", id: budgetId }],
		}),

		updateBudget: builder.mutation<{ message: string; budget: Budget }, { budgetId: string; data: UpdateBudgetRequest }>({
			query: ({ budgetId, data }) => ({
				url: `/governance/budgets/${budgetId}`,
				method: "PUT",
				body: data,
			}),
			invalidatesTags: (result, error, { budgetId }) => ["Budgets", { type: "Budgets", id: budgetId }],
		}),

		deleteBudget: builder.mutation<{ message: string }, string>({
			query: (budgetId) => ({
				url: `/governance/budgets/${budgetId}`,
				method: "DELETE",
			}),
			invalidatesTags: ["Budgets"],
		}),

		// Rate Limits
		getRateLimits: builder.query<GetRateLimitsResponse, void>({
			query: () => "/governance/rate-limits",
			providesTags: ["RateLimits"],
		}),

		getRateLimit: builder.query<{ rate_limit: RateLimit }, string>({
			query: (rateLimitId) => `/governance/rate-limits/${rateLimitId}`,
			providesTags: (result, error, rateLimitId) => [{ type: "RateLimits", id: rateLimitId }],
		}),

		updateRateLimit: builder.mutation<{ message: string; rate_limit: RateLimit }, { rateLimitId: string; data: UpdateRateLimitRequest }>({
			query: ({ rateLimitId, data }) => ({
				url: `/governance/rate-limits/${rateLimitId}`,
				method: "PUT",
				body: data,
			}),
			invalidatesTags: (result, error, { rateLimitId }) => ["RateLimits", { type: "RateLimits", id: rateLimitId }],
		}),

		deleteRateLimit: builder.mutation<{ message: string }, string>({
			query: (rateLimitId) => ({
				url: `/governance/rate-limits/${rateLimitId}`,
				method: "DELETE",
			}),
			invalidatesTags: ["RateLimits"],
		}),

		// Usage Stats
		getUsageStats: builder.query<GetUsageStatsResponse, { virtualKeyId?: string }>({
			query: ({ virtualKeyId } = {}) => ({
				url: "/governance/usage-stats",
				params: virtualKeyId ? { virtual_key_id: virtualKeyId } : {},
			}),
			providesTags: ["UsageStats"],
		}),

		resetUsage: builder.mutation<{ message: string }, ResetUsageRequest>({
			query: (data) => ({
				url: "/governance/usage-reset",
				method: "POST",
				body: data,
			}),
			invalidatesTags: ["UsageStats"],
		}),

		// Debug endpoints
		getGovernanceDebugStats: builder.query<DebugStatsResponse, void>({
			query: () => "/governance/debug/stats",
			providesTags: ["DebugStats"],
		}),

		getGovernanceHealth: builder.query<HealthCheckResponse, void>({
			query: () => "/governance/debug/health",
			providesTags: ["HealthCheck"],
		}),
	}),
});

export const {
	// Virtual Keys
	useGetVirtualKeysQuery,
	useGetVirtualKeyQuery,
	useCreateVirtualKeyMutation,
	useUpdateVirtualKeyMutation,
	useDeleteVirtualKeyMutation,

	// Teams
	useGetTeamsQuery,
	useGetTeamQuery,
	useCreateTeamMutation,
	useUpdateTeamMutation,
	useDeleteTeamMutation,

	// Customers
	useGetCustomersQuery,
	useGetCustomerQuery,
	useCreateCustomerMutation,
	useUpdateCustomerMutation,
	useDeleteCustomerMutation,

	// Budgets
	useGetBudgetsQuery,
	useGetBudgetQuery,
	useUpdateBudgetMutation,
	useDeleteBudgetMutation,

	// Rate Limits
	useGetRateLimitsQuery,
	useGetRateLimitQuery,
	useUpdateRateLimitMutation,
	useDeleteRateLimitMutation,

	// Usage Stats
	useGetUsageStatsQuery,
	useResetUsageMutation,

	// Debug
	useGetGovernanceDebugStatsQuery,
	useGetGovernanceHealthQuery,

	// Lazy queries
	useLazyGetVirtualKeysQuery,
	useLazyGetVirtualKeyQuery,
	useLazyGetTeamsQuery,
	useLazyGetTeamQuery,
	useLazyGetCustomersQuery,
	useLazyGetCustomerQuery,
	useLazyGetBudgetsQuery,
	useLazyGetBudgetQuery,
	useLazyGetRateLimitsQuery,
	useLazyGetRateLimitQuery,
	useLazyGetUsageStatsQuery,
	useLazyGetGovernanceDebugStatsQuery,
	useLazyGetGovernanceHealthQuery,
} = governanceApi;
