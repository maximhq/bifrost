import {
	GetWebhookDeliveriesParams,
	GetWebhookDeliveriesResponse,
	GetWebhookEndpointsParams,
	GetWebhookEndpointsResponse,
	RedeliverWebhookResponse,
	TestWebhookEndpointResponse,
	WebhookEndpoint,
	WebhookEndpointRequest,
	WebhookEvent,
	WebhookSecretResponse,
} from "@/lib/types/webhooks";
import { baseApi } from "./baseApi";

// Shapes list params for the wire: empty values are dropped and the events
// filter is CSV-joined after sorting, so cache keys stay canonical regardless
// of selection order.
const buildWebhookEndpointsListParams = (params?: GetWebhookEndpointsParams): Record<string, string | number | boolean> => {
	const out: Record<string, string | number | boolean> = {};
	if (!params) return out;
	if (params.search) out.search = params.search;
	if (params.events?.length) out.event = [...params.events].sort().join(",");
	if (params.disabled !== undefined) out.disabled = params.disabled;
	if (params.limit !== undefined) out.limit = params.limit;
	if (params.offset !== undefined) out.offset = params.offset;
	return out;
};

export const webhooksApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		getWebhookEndpoints: builder.query<GetWebhookEndpointsResponse, GetWebhookEndpointsParams | void>({
			query: (params) => ({ url: "/webhooks", params: buildWebhookEndpointsListParams(params ?? undefined) }),
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