import {
	GetWebhookDeliveriesParams,
	GetWebhookDeliveriesResponse,
	GetWebhookEndpointsResponse,
	RedeliverWebhookResponse,
	TestWebhookEndpointResponse,
	WebhookEndpoint,
	WebhookEndpointRequest,
	WebhookEvent,
	WebhookSecretResponse,
} from "@/lib/types/webhooks";
import { baseApi } from "./baseApi";

export const webhooksApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		getWebhookEndpoints: builder.query<GetWebhookEndpointsResponse, void>({
			query: () => ({ url: "/webhooks" }),
			providesTags: ["WebhookEndpoints"],
		}),

		createWebhookEndpoint: builder.mutation<WebhookSecretResponse, WebhookEndpointRequest>({
			query: (data) => ({
				url: "/webhooks",
				method: "POST",
				body: data,
			}),
			invalidatesTags: ["WebhookEndpoints"],
		}),

		updateWebhookEndpoint: builder.mutation<WebhookEndpoint, { id: string; data: WebhookEndpointRequest }>({
			query: ({ id, data }) => ({
				url: `/webhooks/${id}`,
				method: "PUT",
				body: data,
			}),
			invalidatesTags: ["WebhookEndpoints"],
		}),

		deleteWebhookEndpoint: builder.mutation<{ status: string }, string>({
			query: (id) => ({
				url: `/webhooks/${id}`,
				method: "DELETE",
			}),
			invalidatesTags: ["WebhookEndpoints"],
		}),

		rotateWebhookEndpointSecret: builder.mutation<WebhookSecretResponse, string>({
			query: (id) => ({
				url: `/webhooks/${id}/rotate-secret`,
				method: "POST",
			}),
			invalidatesTags: ["WebhookEndpoints"],
		}),

		testWebhookEndpoint: builder.mutation<TestWebhookEndpointResponse, { id: string; event: WebhookEvent }>({
			query: ({ id, event }) => ({
				url: `/webhooks/${id}/test`,
				method: "POST",
				body: { event },
			}),
		}),

		getWebhookDeliveries: builder.query<GetWebhookDeliveriesResponse, GetWebhookDeliveriesParams>({
			query: ({ endpointId, limit, offset }) => ({
				url: `/webhooks/${endpointId}/deliveries`,
				params: {
					...(limit !== undefined && { limit }),
					...(offset !== undefined && { offset }),
				},
			}),
			providesTags: ["WebhookDeliveries"],
		}),

		redeliverWebhookDelivery: builder.mutation<RedeliverWebhookResponse, string>({
			query: (deliveryId) => ({
				url: `/webhooks/deliveries/${deliveryId}/redeliver`,
				method: "POST",
			}),
			invalidatesTags: ["WebhookDeliveries"],
		}),
	}),
});

export const {
	useGetWebhookEndpointsQuery,
	useCreateWebhookEndpointMutation,
	useUpdateWebhookEndpointMutation,
	useDeleteWebhookEndpointMutation,
	useRotateWebhookEndpointSecretMutation,
	useTestWebhookEndpointMutation,
	useGetWebhookDeliveriesQuery,
	useRedeliverWebhookDeliveryMutation,
} = webhooksApi;