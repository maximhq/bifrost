/**
 * Routing Rules RTK Query API
 * Handles all API communication for routing rules CRUD operations
 */

import {
	RoutingRule,
	GetRoutingRulesResponse,
	GetRoutingRulesParams,
	CreateRoutingRuleRequest,
	UpdateRoutingRuleRequest,
} from "@/lib/types/routingRules";
import { baseApi } from "./baseApi";

export const routingRulesApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		// Get routing rules with pagination
		getRoutingRules: builder.query<GetRoutingRulesResponse, GetRoutingRulesParams | void>({
			query: (params) => ({
				url: "/governance/routing-rules",
				params: {
					...(params?.limit && { limit: params.limit }),
					...(params?.offset !== undefined && { offset: params.offset }),
					...(params?.search && { search: params.search }),
				},
			}),
			providesTags: ["RoutingRules"],
		}),

		// Get a single routing rule
		getRoutingRule: builder.query<RoutingRule, string>({
			query: (id) => ({
				url: `/governance/routing-rules/${id}`,
				method: "GET",
			}),
			transformResponse: (response: { rule: RoutingRule }) => response.rule,
			providesTags: (result, error, arg) => [{ type: "RoutingRules", id: arg }],
		}),

		// Create a new routing rule
		createRoutingRule: builder.mutation<RoutingRule, CreateRoutingRuleRequest>({
			query: (body) => ({
				url: `/governance/routing-rules`,
				method: "POST",
				body,
			}),
			transformResponse: (response: { rule: RoutingRule }) => response.rule,
			async onQueryStarted(arg, { dispatch, getState, queryFulfilled }) {
				try {
					const { data: newRule } = await queryFulfilled;
					const queries = (getState() as any).api.queries;
					for (const entry of Object.values(queries) as any[]) {
						if (entry?.endpointName !== "getRoutingRules" || entry?.status !== "fulfilled") continue;
						const search = entry.originalArgs?.search as string | undefined;
						if (search && !newRule.name.toLowerCase().includes(search.toLowerCase())) continue;
						dispatch(
							routingRulesApi.util.updateQueryData("getRoutingRules", entry.originalArgs, (draft) => {
								if (!draft.rules) draft.rules = [];
								draft.rules.unshift(newRule);
								draft.count = (draft.count || 0) + 1;
								draft.total_count = (draft.total_count || 0) + 1;
							}),
						);
					}
					dispatch(routingRulesApi.util.updateQueryData("getRoutingRule", newRule.id, () => newRule));
				} catch {}
			},
		}),

		// Update an existing routing rule
		updateRoutingRule: builder.mutation<RoutingRule, { id: string; data: UpdateRoutingRuleRequest }>({
			query: ({ id, data }) => ({
				url: `/governance/routing-rules/${id}`,
				method: "PUT",
				body: data,
			}),
			transformResponse: (response: { rule: RoutingRule }) => response.rule,
			async onQueryStarted({ id }, { dispatch, getState, queryFulfilled }) {
				try {
					const { data: updatedRule } = await queryFulfilled;
					const queries = (getState() as any).api.queries;
					for (const entry of Object.values(queries) as any[]) {
						if (entry?.endpointName !== "getRoutingRules" || entry?.status !== "fulfilled") continue;
						dispatch(
							routingRulesApi.util.updateQueryData("getRoutingRules", entry.originalArgs, (draft) => {
								if (!draft.rules) return;
								const index = draft.rules.findIndex((r) => r.id === id);
								if (index !== -1) {
									draft.rules[index] = updatedRule;
								}
							}),
						);
					}
					dispatch(routingRulesApi.util.updateQueryData("getRoutingRule", updatedRule.id, () => updatedRule));
				} catch {}
			},
		}),

		// Delete a routing rule
		deleteRoutingRule: builder.mutation<void, string>({
			query: (id) => ({
				url: `/governance/routing-rules/${id}`,
				method: "DELETE",
			}),
			async onQueryStarted(ruleId, { dispatch, getState, queryFulfilled }) {
				try {
					await queryFulfilled;
					const queries = (getState() as any).api.queries;
					for (const entry of Object.values(queries) as any[]) {
						if (entry?.endpointName !== "getRoutingRules" || entry?.status !== "fulfilled") continue;
						dispatch(
							routingRulesApi.util.updateQueryData("getRoutingRules", entry.originalArgs, (draft) => {
								if (!draft.rules) return;
								const before = draft.rules.length;
								draft.rules = draft.rules.filter((r) => r.id !== ruleId);
								if (draft.rules.length < before) {
									draft.count = Math.max(0, (draft.count || 0) - 1);
									draft.total_count = Math.max(0, (draft.total_count || 0) - 1);
								}
							}),
						);
					}
				} catch {}
			},
		}),
	}),
});

export const {
	useGetRoutingRulesQuery,
	useGetRoutingRuleQuery,
	useCreateRoutingRuleMutation,
	useUpdateRoutingRuleMutation,
	useDeleteRoutingRuleMutation,
	useLazyGetRoutingRulesQuery,
} = routingRulesApi;