/**
 * Routing Rules RTK Query API
 * Handles all API communication for routing rules CRUD operations
 */

import { RoutingRule, GetRoutingRulesResponse, CreateRoutingRuleRequest, UpdateRoutingRuleRequest } from "@/lib/types/routingRules";
import { baseApi } from "./baseApi";

export const routingRulesApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		// Get all routing rules
		getRoutingRules: builder.query<RoutingRule[], { fromMemory?: boolean } | void>({
			query: (params) => {
				const searchParams = new URLSearchParams();
				if (params?.fromMemory) {
					searchParams.append("from_memory", "true");
				}
				return {
					url: `/governance/routing-rules${searchParams.toString() ? `?${searchParams.toString()}` : ""}`,
					method: "GET",
				};
			},
			transformResponse: (response: GetRoutingRulesResponse) => response.rules || [],
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
			async onQueryStarted(arg, { dispatch, queryFulfilled }) {
				try {
					const { data: newRule } = await queryFulfilled;
					// Update the default cache variant
					dispatch(
						routingRulesApi.util.updateQueryData("getRoutingRules", undefined, (draft) => {
							draft.push(newRule);
						})
					);
					// Update the fromMemory cache variant
					dispatch(
						routingRulesApi.util.updateQueryData("getRoutingRules", { fromMemory: true }, (draft) => {
							draft.push(newRule);
						})
					);
					// Also update the individual routing rule cache
					dispatch(
						routingRulesApi.util.updateQueryData("getRoutingRule", newRule.id, () => newRule)
					);
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
			async onQueryStarted(arg, { dispatch, queryFulfilled }) {
				try {
					const { data: updatedRule } = await queryFulfilled;
					// Update the default cache variant
					dispatch(
						routingRulesApi.util.updateQueryData("getRoutingRules", undefined, (draft) => {
							const index = draft.findIndex((r) => r.id === updatedRule.id);
							if (index !== -1) {
								draft[index] = updatedRule;
							}
						})
					);
					// Update the fromMemory cache variant
					dispatch(
						routingRulesApi.util.updateQueryData("getRoutingRules", { fromMemory: true }, (draft) => {
							const index = draft.findIndex((r) => r.id === updatedRule.id);
							if (index !== -1) {
								draft[index] = updatedRule;
							}
						})
					);
					// Also update the individual routing rule cache
					dispatch(
						routingRulesApi.util.updateQueryData("getRoutingRule", updatedRule.id, () => updatedRule)
					);
				} catch {}
			},
		}),

		// Delete a routing rule
		deleteRoutingRule: builder.mutation<void, string>({
			query: (id) => ({
				url: `/governance/routing-rules/${id}`,
				method: "DELETE",
			}),
			async onQueryStarted(ruleId, { dispatch, queryFulfilled }) {
				try {
					await queryFulfilled;
					// Update the default cache variant
					dispatch(
						routingRulesApi.util.updateQueryData("getRoutingRules", undefined, (draft) => {
							const index = draft.findIndex((r) => r.id === ruleId);
							if (index !== -1) {
								draft.splice(index, 1);
							}
						})
					);
					// Update the fromMemory cache variant
					dispatch(
						routingRulesApi.util.updateQueryData("getRoutingRules", { fromMemory: true }, (draft) => {
							const index = draft.findIndex((r) => r.id === ruleId);
							if (index !== -1) {
								draft.splice(index, 1);
							}
						})
					);
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
