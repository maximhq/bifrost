import { SecretVar } from "./schemas";

// Wire shapes for the /api/webhooks admin API.

export type WebhookEvent = "async_job.completed" | "async_job.failed";

export const WEBHOOK_EVENTS: { value: WebhookEvent; label: string; description: string }[] = [
	{ value: "async_job.completed", label: "Async job completed", description: "An async inference job finished successfully." },
	{ value: "async_job.failed", label: "Async job failed", description: "An async inference job reached a terminal failure." },
];

export interface WebhookEndpoint {
	id: string;
	name: string;
	url: string;
	events: WebhookEvent[];
	headers?: Record<string, SecretVar>;
	include_response: boolean;
	allow_private_network: boolean;
	disabled: boolean;
	// Delivery tuning knobs; 0 or absent means the delivery worker default
	// (see WEBHOOK_TUNING_DEFAULTS).
	max_retries?: number;
	retry_backoff_initial_seconds?: number;
	retry_backoff_max_seconds?: number;
	attempt_timeout_seconds?: number;
	max_response_payload_kbs?: number;
	max_concurrent_deliveries?: number;
	consecutive_failures: number;
	last_success_at?: string;
	last_failure_at?: string;
	created_at: string;
	updated_at: string;
}

// PUT replaces the endpoint's full caller-editable state — omitted fields are
// cleared server-side, so every field is required here.
export interface WebhookEndpointRequest {
	name: string;
	url: string;
	events: WebhookEvent[];
	headers: Record<string, SecretVar>;
	include_response: boolean;
	allow_private_network: boolean;
	disabled: boolean;
	max_retries: number;
	retry_backoff_initial_seconds: number;
	retry_backoff_max_seconds: number;
	attempt_timeout_seconds: number;
	max_response_payload_kbs: number;
	max_concurrent_deliveries: number;
}

export interface GetWebhookEndpointsParams {
	search?: string;
	events?: WebhookEvent[]; // subscribed to any of these, OR semantics
	disabled?: boolean;
	limit?: number;
	offset?: number;
}

export interface GetWebhookEndpointsResponse {
	endpoints: WebhookEndpoint[];
	count: number;
	total_count: number;
	limit: number;
	offset: number;
}

// Returned by create and rotate-secret; the signing secret appears exactly
// once in these responses and can never be read back afterwards.
export interface WebhookSecretResponse {
	endpoint: WebhookEndpoint;
	secret: string;
}

export interface TestWebhookEndpointResponse {
	delivered: boolean;
	receiver_status_code?: number;
	error?: string;
}

export type WebhookDeliveryOutcome = "delivered" | "retryable_failure" | "permanent_failure" | "exhausted";

export interface WebhookDelivery {
	id: string;
	webhook_id: string;
	endpoint_id: string;
	async_job_id: string;
	request_id?: string;
	event: WebhookEvent;
	attempt_no: number;
	outcome: WebhookDeliveryOutcome;
	status_code?: number;
	error?: string;
	created_at: string;
	expires_at?: string;
}

export interface GetWebhookDeliveriesParams {
	endpointId: string;
	limit?: number;
	offset?: number;
}

export interface GetWebhookDeliveriesResponse {
	deliveries: WebhookDelivery[];
	pagination: {
		limit: number;
		offset: number;
		total_count: number;
	};
}

export interface RedeliverWebhookResponse {
	status: string;
	webhook_id: string;
}

// Soft tints for event badges, matching the delivery-outcome color language
// (green = completed/delivered, red = failed).
export const WEBHOOK_EVENT_COLORS: Record<WebhookEvent, string> = {
	"async_job.completed": "bg-green-100 text-green-800",
	"async_job.failed": "bg-red-100 text-red-800",
};

// Delivery worker defaults, shown as placeholders for unset (0) knobs.
export const WEBHOOK_TUNING_DEFAULTS = {
	max_retries: 4,
	retry_backoff_initial_seconds: 30,
	retry_backoff_max_seconds: 1800,
	attempt_timeout_seconds: 10,
	max_response_payload_kbs: 256,
	max_concurrent_deliveries: 10,
} as const;