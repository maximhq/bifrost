import {
	CreatePricingOverrideRequest,
	GetPricingOverridesResponse,
	PricingOverride,
	UpdatePricingOverrideRequest,
} from "@/lib/types/pricingOverrides";
import { baseApi } from "./baseApi";

export const pricingOverridesApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		getPricingOverrides: builder.query<PricingOverride[], void>({
			query: () => ({
				url: "/governance/pricing-overrides",
				method: "GET",
			}),
			transformResponse: (response: GetPricingOverridesResponse) => response.pricing_overrides || [],
			providesTags: ["PricingOverrides"],
		}),

		getPricingOverride: builder.query<PricingOverride, string>({
			query: (id) => ({
				url: `/governance/pricing-overrides/${id}`,
				method: "GET",
			}),
			transformResponse: (response: { pricing_override: PricingOverride }) => response.pricing_override,
			providesTags: (result, error, id) => [{ type: "PricingOverrides", id }],
		}),

		createPricingOverride: builder.mutation<PricingOverride, CreatePricingOverrideRequest>({
			query: (data) => ({
				url: "/governance/pricing-overrides",
				method: "POST",
				body: data,
			}),
			transformResponse: (response: { pricing_override: PricingOverride }) => response.pricing_override,
			async onQueryStarted(arg, { dispatch, queryFulfilled }) {
				try {
					const { data: created } = await queryFulfilled;
					dispatch(
						pricingOverridesApi.util.updateQueryData("getPricingOverrides", undefined, (draft) => {
							draft.unshift(created);
						}),
					);
				} catch (error) {
					// Best-effort optimistic cache update.
					void error;
				}
			},
		}),

		updatePricingOverride: builder.mutation<PricingOverride, { id: string; data: UpdatePricingOverrideRequest }>({
			query: ({ id, data }) => ({
				url: `/governance/pricing-overrides/${id}`,
				method: "PUT",
				body: data,
			}),
			transformResponse: (response: { pricing_override: PricingOverride }) => response.pricing_override,
			async onQueryStarted({ id }, { dispatch, queryFulfilled }) {
				try {
					const { data: updated } = await queryFulfilled;
					dispatch(
						pricingOverridesApi.util.updateQueryData("getPricingOverrides", undefined, (draft) => {
							const index = draft.findIndex((item) => item.id === id);
							if (index !== -1) {
								draft[index] = updated;
							}
						}),
					);
					dispatch(
						pricingOverridesApi.util.updateQueryData("getPricingOverride", id, () => updated),
					);
				} catch (error) {
					// Best-effort optimistic cache update.
					void error;
				}
			},
		}),

		deletePricingOverride: builder.mutation<void, string>({
			query: (id) => ({
				url: `/governance/pricing-overrides/${id}`,
				method: "DELETE",
			}),
			async onQueryStarted(id, { dispatch, queryFulfilled }) {
				try {
					await queryFulfilled;
					dispatch(
						pricingOverridesApi.util.updateQueryData("getPricingOverrides", undefined, (draft) => {
							const index = draft.findIndex((item) => item.id === id);
							if (index !== -1) {
								draft.splice(index, 1);
							}
						}),
					);
				} catch (error) {
					// Best-effort optimistic cache update.
					void error;
				}
			},
		}),
	}),
});

export const {
	useGetPricingOverridesQuery,
	useGetPricingOverrideQuery,
	useCreatePricingOverrideMutation,
	useUpdatePricingOverrideMutation,
	useDeletePricingOverrideMutation,
} = pricingOverridesApi;
