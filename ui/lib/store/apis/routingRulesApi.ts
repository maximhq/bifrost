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
			invalidatesTags: ["RoutingRules"],
		}),

		// Update an existing routing rule
		updateRoutingRule: builder.mutation<RoutingRule, { id: string; data: UpdateRoutingRuleRequest }>({
			query: ({ id, data }) => ({
				url: `/governance/routing-rules/${id}`,
				method: "PUT",
				body: data,
			}),
			transformResponse: (response: { rule: RoutingRule }) => response.rule,
			invalidatesTags: (result, error, arg) => ["RoutingRules", { type: "RoutingRules", id: arg.id }],
		}),

		// Delete a routing rule
		deleteRoutingRule: builder.mutation<void, string>({
			query: (id) => ({
				url: `/governance/routing-rules/${id}`,
				method: "DELETE",
			}),
			invalidatesTags: (result, error, id) => [{ type: "RoutingRules", id }, { type: "RoutingRules", id: "LIST" }, "RoutingRules"],
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
