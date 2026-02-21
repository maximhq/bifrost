import {
	Budget,
	CreateCustomerRequest,
	CreateModelConfigRequest,
	CreateTeamRequest,
	CreateVirtualKeyRequest,
	Customer,
	DebugStatsResponse,
	GetBudgetsResponse,
	GetCustomersResponse,
	GetModelConfigsResponse,
	GetProviderGovernanceResponse,
	GetRateLimitsResponse,
	GetTeamsResponse,
	GetUsageStatsResponse,
	GetVirtualKeysResponse,
	HealthCheckResponse,
	ModelConfig,
	ProviderGovernance,
	RateLimit,
	ResetUsageRequest,
	Team,
	UpdateBudgetRequest,
	UpdateCustomerRequest,
	UpdateModelConfigRequest,
	UpdateProviderGovernanceRequest,
	UpdateRateLimitRequest,
	UpdateTeamRequest,
	UpdateVirtualKeyRequest,
	VirtualKey,
} from "@/lib/types/governance";
import { baseApi } from "./baseApi";

export const governanceApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		// Virtual Keys
		getVirtualKeys: builder.query<GetVirtualKeysResponse, { fromMemory?: boolean } | void>({
			query: (params) => ({
				url: "/governance/virtual-keys",
				params: { from_memory: params?.fromMemory ?? false },
			}),
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
			async onQueryStarted(arg, { dispatch, queryFulfilled }) {
				try {
					const { data } = await queryFulfilled;
					const variants = [undefined, { fromMemory: true }] as const;
					for (const variant of variants) {
						dispatch(
							governanceApi.util.updateQueryData("getVirtualKeys", variant, (draft) => {
								if (!draft.virtual_keys) draft.virtual_keys = [];
								draft.virtual_keys.unshift(data.virtual_key);
								draft.count = (draft.count || 0) + 1;
							}),
						);
					}
				} catch {
					// Mutation failed, do nothing - error handling bubbled up
				}
			},
		}),

		updateVirtualKey: builder.mutation<{ message: string; virtual_key: VirtualKey }, { vkId: string; data: UpdateVirtualKeyRequest }>({
			query: ({ vkId, data }) => ({
				url: `/governance/virtual-keys/${vkId}`,
				method: "PUT",
				body: data,
			}),
			async onQueryStarted({ vkId }, { dispatch, queryFulfilled }) {
				try {
					const { data } = await queryFulfilled;
					const variants = [undefined, { fromMemory: true }] as const;
					for (const variant of variants) {
						dispatch(
							governanceApi.util.updateQueryData("getVirtualKeys", variant, (draft) => {
								if (!draft.virtual_keys) return;
								const index = draft.virtual_keys.findIndex((vk) => vk.id === vkId);
								if (index !== -1) {
									draft.virtual_keys[index] = data.virtual_key;
								}
							}),
						);
					}
					dispatch(
						governanceApi.util.updateQueryData("getVirtualKey", vkId, (draft) => {
							draft.virtual_key = data.virtual_key;
						}),
					);
				} catch {
					// Mutation failed
				}
			},
		}),

		deleteVirtualKey: builder.mutation<{ message: string }, string>({
			query: (vkId) => ({
				url: `/governance/virtual-keys/${vkId}`,
				method: "DELETE",
			}),
			async onQueryStarted(vkId, { dispatch, queryFulfilled }) {
				try {
					await queryFulfilled;
					const variants = [undefined, { fromMemory: true }] as const;
					for (const variant of variants) {
						dispatch(
							governanceApi.util.updateQueryData("getVirtualKeys", variant, (draft) => {
								if (!draft.virtual_keys) return;
								draft.virtual_keys = draft.virtual_keys.filter((vk) => vk.id !== vkId);
								draft.count = Math.max(0, (draft.count || 0) - 1);
							}),
						);
					}
				} catch {
					// Mutation failed
				}
			},
		}),

		bulkDeleteVirtualKeys: builder.mutation<{ message: string; deleted_count: number; failed_ids: string[]; total: number }, string[]>({
			query: (ids) => ({
				url: "/governance/virtual-keys",
				method: "DELETE",
				body: { ids },
			}),
			async onQueryStarted(ids, { dispatch, queryFulfilled }) {
				try {
					await queryFulfilled;
					const variants = [undefined, { fromMemory: true }] as const;
					for (const variant of variants) {
						dispatch(
							governanceApi.util.updateQueryData("getVirtualKeys", variant, (draft) => {
								if (!draft.virtual_keys) return;
								draft.virtual_keys = draft.virtual_keys.filter((vk) => !ids.includes(vk.id));
								draft.count = Math.max(0, draft.virtual_keys.length);
							}),
						);
					}
				} catch {
					// Mutation failed
				}
			},
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
			async onQueryStarted(arg, { dispatch, queryFulfilled }) {
				try {
					const { data } = await queryFulfilled;
					dispatch(
						governanceApi.util.updateQueryData("getTeams", {}, (draft) => {
							if (!draft.teams) draft.teams = [];
							draft.teams.unshift(data.team);
							draft.count = (draft.count || 0) + 1;
						}),
					);
				} catch {
					// Mutation failed
				}
			},
		}),

		updateTeam: builder.mutation<{ message: string; team: Team }, { teamId: string; data: UpdateTeamRequest }>({
			query: ({ teamId, data }) => ({
				url: `/governance/teams/${teamId}`,
				method: "PUT",
				body: data,
			}),
			async onQueryStarted({ teamId }, { dispatch, queryFulfilled }) {
				try {
					const { data } = await queryFulfilled;
					dispatch(
						governanceApi.util.updateQueryData("getTeams", {}, (draft) => {
							if (!draft.teams) return;
							const index = draft.teams.findIndex((t) => t.id === teamId);
							if (index !== -1) {
								draft.teams[index] = data.team;
							}
						}),
					);
					dispatch(
						governanceApi.util.updateQueryData("getTeam", teamId, (draft) => {
							draft.team = data.team;
						}),
					);
				} catch {
					// Mutation failed
				}
			},
		}),

		deleteTeam: builder.mutation<{ message: string }, string>({
			query: (teamId) => ({
				url: `/governance/teams/${teamId}`,
				method: "DELETE",
			}),
			async onQueryStarted(teamId, { dispatch, queryFulfilled }) {
				try {
					await queryFulfilled;
					dispatch(
						governanceApi.util.updateQueryData("getTeams", {}, (draft) => {
							if (!draft.teams) return;
							draft.teams = draft.teams.filter((t) => t.id !== teamId);
							draft.count = Math.max(0, (draft.count || 0) - 1);
						}),
					);
				} catch {
					// Mutation failed
				}
			},
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
			async onQueryStarted(arg, { dispatch, queryFulfilled }) {
				try {
					const { data } = await queryFulfilled;
					dispatch(
						governanceApi.util.updateQueryData("getCustomers", undefined, (draft) => {
							if (!draft.customers) draft.customers = [];
							draft.customers.unshift(data.customer);
							draft.count = (draft.count || 0) + 1;
						}),
					);
				} catch {
					// Mutation failed
				}
			},
		}),

		updateCustomer: builder.mutation<{ message: string; customer: Customer }, { customerId: string; data: UpdateCustomerRequest }>({
			query: ({ customerId, data }) => ({
				url: `/governance/customers/${customerId}`,
				method: "PUT",
				body: data,
			}),
			async onQueryStarted({ customerId }, { dispatch, queryFulfilled }) {
				try {
					const { data } = await queryFulfilled;
					dispatch(
						governanceApi.util.updateQueryData("getCustomers", undefined, (draft) => {
							if (!draft.customers) return;
							const index = draft.customers.findIndex((c) => c.id === customerId);
							if (index !== -1) {
								draft.customers[index] = data.customer;
							}
						}),
					);
					dispatch(
						governanceApi.util.updateQueryData("getCustomer", customerId, (draft) => {
							draft.customer = data.customer;
						}),
					);
				} catch {
					// Mutation failed
				}
			},
		}),

		deleteCustomer: builder.mutation<{ message: string }, string>({
			query: (customerId) => ({
				url: `/governance/customers/${customerId}`,
				method: "DELETE",
			}),
			async onQueryStarted(customerId, { dispatch, queryFulfilled }) {
				try {
					await queryFulfilled;
					dispatch(
						governanceApi.util.updateQueryData("getCustomers", undefined, (draft) => {
							if (!draft.customers) return;
							draft.customers = draft.customers.filter((c) => c.id !== customerId);
							draft.count = Math.max(0, (draft.count || 0) - 1);
						}),
					);
				} catch {
					// Mutation failed
				}
			},
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
			async onQueryStarted({ budgetId }, { dispatch, queryFulfilled }) {
				try {
					const { data } = await queryFulfilled;
					dispatch(
						governanceApi.util.updateQueryData("getBudgets", undefined, (draft) => {
							if (!draft.budgets) return;
							const index = draft.budgets.findIndex((b) => b.id === budgetId);
							if (index !== -1) {
								draft.budgets[index] = data.budget;
							}
						}),
					);
					dispatch(
						governanceApi.util.updateQueryData("getBudget", budgetId, (draft) => {
							draft.budget = data.budget;
						}),
					);
				} catch {
					// Mutation failed
				}
			},
		}),

		deleteBudget: builder.mutation<{ message: string }, string>({
			query: (budgetId) => ({
				url: `/governance/budgets/${budgetId}`,
				method: "DELETE",
			}),
			async onQueryStarted(budgetId, { dispatch, queryFulfilled }) {
				try {
					await queryFulfilled;
					dispatch(
						governanceApi.util.updateQueryData("getBudgets", undefined, (draft) => {
							if (!draft.budgets) return;
							draft.budgets = draft.budgets.filter((b) => b.id !== budgetId);
							draft.count = Math.max(0, (draft.count || 0) - 1);
						}),
					);
				} catch {
					// Mutation failed
				}
			},
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
			async onQueryStarted({ rateLimitId }, { dispatch, queryFulfilled }) {
				try {
					const { data } = await queryFulfilled;
					dispatch(
						governanceApi.util.updateQueryData("getRateLimits", undefined, (draft) => {
							if (!draft.rate_limits) return;
							const index = draft.rate_limits.findIndex((r) => r.id === rateLimitId);
							if (index !== -1) {
								draft.rate_limits[index] = data.rate_limit;
							}
						}),
					);
					dispatch(
						governanceApi.util.updateQueryData("getRateLimit", rateLimitId, (draft) => {
							draft.rate_limit = data.rate_limit;
						}),
					);
				} catch {
					// Mutation failed
				}
			},
		}),

		deleteRateLimit: builder.mutation<{ message: string }, string>({
			query: (rateLimitId) => ({
				url: `/governance/rate-limits/${rateLimitId}`,
				method: "DELETE",
			}),
			async onQueryStarted(rateLimitId, { dispatch, queryFulfilled }) {
				try {
					await queryFulfilled;
					dispatch(
						governanceApi.util.updateQueryData("getRateLimits", undefined, (draft) => {
							if (!draft.rate_limits) return;
							draft.rate_limits = draft.rate_limits.filter((r) => r.id !== rateLimitId);
							draft.count = Math.max(0, (draft.count || 0) - 1);
						}),
					);
				} catch {
					// Mutation failed
				}
			},
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

		// Model Configs
		getModelConfigs: builder.query<GetModelConfigsResponse, { fromMemory?: boolean } | void>({
			query: (params) => ({
				url: "/governance/model-configs",
				params: { from_memory: params?.fromMemory ?? false },
			}),
			providesTags: ["ModelConfigs"],
		}),

		getModelConfig: builder.query<{ model_config: ModelConfig }, string>({
			query: (id) => `/governance/model-configs/${id}`,
			providesTags: (result, error, id) => [{ type: "ModelConfigs", id }],
		}),

		createModelConfig: builder.mutation<{ message: string; model_config: ModelConfig }, CreateModelConfigRequest>({
			query: (data) => ({
				url: "/governance/model-configs",
				method: "POST",
				body: data,
			}),
			async onQueryStarted(arg, { dispatch, queryFulfilled }) {
				try {
					const { data } = await queryFulfilled;
					const variants = [undefined, { fromMemory: true }] as const;
					for (const variant of variants) {
						dispatch(
							governanceApi.util.updateQueryData("getModelConfigs", variant, (draft) => {
								if (!draft.model_configs) draft.model_configs = [];
								draft.model_configs.unshift(data.model_config);
								draft.count = (draft.count || 0) + 1;
							}),
						);
					}
				} catch {
					// Mutation failed, do nothing - error handling bubbled up
				}
			},
		}),

		updateModelConfig: builder.mutation<{ message: string; model_config: ModelConfig }, { id: string; data: UpdateModelConfigRequest }>({
			query: ({ id, data }) => ({
				url: `/governance/model-configs/${id}`,
				method: "PUT",
				body: data,
			}),
			async onQueryStarted({ id }, { dispatch, queryFulfilled }) {
				try {
					const { data } = await queryFulfilled;
					const variants = [undefined, { fromMemory: true }] as const;
					for (const variant of variants) {
						dispatch(
							governanceApi.util.updateQueryData("getModelConfigs", variant, (draft) => {
								if (!draft.model_configs) return;
								const index = draft.model_configs.findIndex((mc) => mc.id === id);
								if (index !== -1) {
									draft.model_configs[index] = data.model_config;
								}
							}),
						);
					}
					dispatch(
						governanceApi.util.updateQueryData("getModelConfig", id, (draft) => {
							draft.model_config = data.model_config;
						}),
					);
				} catch {
					// Mutation failed
				}
			},
		}),

		deleteModelConfig: builder.mutation<{ message: string }, string>({
			query: (id) => ({
				url: `/governance/model-configs/${id}`,
				method: "DELETE",
			}),
			async onQueryStarted(id, { dispatch, queryFulfilled }) {
				try {
					await queryFulfilled;
					const variants = [undefined, { fromMemory: true }] as const;
					for (const variant of variants) {
						dispatch(
							governanceApi.util.updateQueryData("getModelConfigs", variant, (draft) => {
								if (!draft.model_configs) return;
								draft.model_configs = draft.model_configs.filter((mc) => mc.id !== id);
								draft.count = Math.max(0, (draft.count || 0) - 1);
							}),
						);
					}
				} catch {
					// Mutation failed
				}
			},
		}),

		// Provider Governance
		getProviderGovernance: builder.query<GetProviderGovernanceResponse, { fromMemory?: boolean } | void>({
			query: (params) => ({
				url: "/governance/providers",
				params: { from_memory: params?.fromMemory ?? false },
			}),
			providesTags: ["ProviderGovernance"],
		}),

		updateProviderGovernance: builder.mutation<
			{ message: string; provider: ProviderGovernance },
			{ provider: string; data: UpdateProviderGovernanceRequest }
		>({
			query: ({ provider, data }) => ({
				url: `/governance/providers/${encodeURIComponent(provider)}`,
				method: "PUT",
				body: data,
			}),
			async onQueryStarted({ provider }, { dispatch, queryFulfilled }) {
				try {
					const { data } = await queryFulfilled;
					const variants = [undefined, { fromMemory: true }] as const;
					for (const variant of variants) {
						dispatch(
							governanceApi.util.updateQueryData("getProviderGovernance", variant, (draft) => {
								if (!draft.providers) draft.providers = [];
								const index = draft.providers.findIndex((p) => p.provider === provider);
								if (index !== -1) {
									draft.providers[index] = data.provider;
								} else {
									// New provider governance - add to list
									draft.providers.push(data.provider);
									draft.count = (draft.count || 0) + 1;
								}
							}),
						);
					}
				} catch {
					// Mutation failed
				}
			},
		}),

		deleteProviderGovernance: builder.mutation<{ message: string }, string>({
			query: (provider) => ({
				url: `/governance/providers/${encodeURIComponent(provider)}`,
				method: "DELETE",
			}),
			async onQueryStarted(provider, { dispatch, queryFulfilled }) {
				try {
					await queryFulfilled;
					const variants = [undefined, { fromMemory: true }] as const;
					for (const variant of variants) {
						dispatch(
							governanceApi.util.updateQueryData("getProviderGovernance", variant, (draft) => {
								if (!draft.providers) return;
								draft.providers = draft.providers.filter((p) => p.provider !== provider);
								draft.count = Math.max(0, (draft.count || 0) - 1);
							}),
						);
					}
				} catch {
					// Mutation failed
				}
			},
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
	useBulkDeleteVirtualKeysMutation,

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

	// Model Configs
	useGetModelConfigsQuery,
	useGetModelConfigQuery,
	useCreateModelConfigMutation,
	useUpdateModelConfigMutation,
	useDeleteModelConfigMutation,

	// Provider Governance
	useGetProviderGovernanceQuery,
	useUpdateProviderGovernanceMutation,
	useDeleteProviderGovernanceMutation,

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
	useLazyGetModelConfigsQuery,
	useLazyGetProviderGovernanceQuery,
} = governanceApi;
