import { KnownProvidersNames } from "@/lib/constants/logs";
import { z } from "zod";

// Base Zod schemas matching the TypeScript types

// Known provider schema
export const knownProviderSchema = z.enum(KnownProvidersNames as unknown as [string, ...string[]]);

// Custom provider name schema (branded type simulation)
export const customProviderNameSchema = z.string().min(1, "Custom provider name is required");

// Model provider name schema (union of known and custom providers)
export const modelProviderNameSchema = z.union([knownProviderSchema, customProviderNameSchema]);

// Azure key config schema
export const azureKeyConfigSchema = z.object({
	endpoint: z.url("Must be a valid URL"),
	deployments: z.union([z.record(z.string(), z.string()), z.string()]).optional(),
	api_version: z.string().optional(),
});

// Vertex key config schema
export const vertexKeyConfigSchema = z.object({
	project_id: z.string().min(1, "Project ID is required"),
	region: z.string().min(1, "Region is required"),
	auth_credentials: z.string().min(1, "Auth credentials are required"),
});

// Bedrock key config schema
export const bedrockKeyConfigSchema = z.object({
	access_key: z.string().min(1, "Access key is required").optional(),
	secret_key: z.string().min(1, "Secret key is required").optional(),
	session_token: z.string().optional(),
	region: z.string().min(1, "Region is required"),
	arn: z.string().optional(),
	deployments: z.union([z.record(z.string(), z.string()), z.string()]).optional(),
});

// Model provider key schema
export const modelProviderKeySchema = z
	.object({
		id: z.string().min(1, "Id is required"),
		value: z.string().optional(),
		models: z.array(z.string()).default([]).optional(),
		weight: z.number().min(0.1, "Weight must be greater than 0.1").max(1, "Weight must be less than 1"),
		azure_key_config: azureKeyConfigSchema.optional(),
		vertex_key_config: vertexKeyConfigSchema.optional(),
		bedrock_key_config: bedrockKeyConfigSchema.optional(),
	})
	.refine(
		(data) => {
			// If bedrock_key_config or azure_key_config is present, value is not required
			if (data.bedrock_key_config || data.azure_key_config || data.vertex_key_config) {
				return true;
			}
			// Otherwise, value is required
			return data.value && data.value.length > 0;
		},
		{
			message: "Value is required",
			path: ["value"],
		},
	);

// Network config schema
export const networkConfigSchema = z
	.object({
		base_url: z.union([z.string().url("Must be a valid URL"), z.string().length(0)]).optional(),
		extra_headers: z.record(z.string(), z.string()).optional(),
		default_request_timeout_in_seconds: z
			.number()
			.min(1, "Timeout must be greater than 0 seconds")
			.max(300, "Timeout must be less than 300 seconds"),
		max_retries: z.number().min(0, "Max retries must be greater than 0").max(10, "Max retries must be less than 10"),
		retry_backoff_initial: z.number().min(100),
		retry_backoff_max: z.number().min(1000),
	})
	.refine((d) => d.retry_backoff_initial <= d.retry_backoff_max, {
		message: "retry_backoff_initial must be <= retry_backoff_max",
		path: ["retry_backoff_initial"],
	});

// Network form schema - more lenient for form inputs
export const networkFormConfigSchema = z.object({
	base_url: z
		.url("Must be a valid URL")
		.refine((url) => url.startsWith("https://"), {
			message: "Only HTTPS URLs are supported",
		})
		.optional(),
	extra_headers: z.record(z.string(), z.string()).optional(),
	default_request_timeout_in_seconds: z.coerce
		.number("Timeout must be a number")
		.min(1, "Timeout must be greater than 0 seconds")
		.max(300, "Timeout must be less than 300 seconds"),
	max_retries: z.coerce
		.number("Max retries must be a number")
		.min(0, "Max retries must be greater than 0")
		.max(10, "Max retries must be less than 10"),
	retry_backoff_initial: z.coerce
		.number("Retry backoff initial must be a number")
		.min(0, "Retry backoff initial must be greater than 0"),
	retry_backoff_max: z.coerce
		.number("Retry backoff max must be a number")
		.min(0, "Retry backoff max must be greater than 0"),
})
.refine((d) => d.retry_backoff_initial <= d.retry_backoff_max, {
	message: "Initial backoff must be less than or equal to max backoff",
	path: ["retry_backoff_initial"],
});

// Concurrency and buffer size schema
export const concurrencyAndBufferSizeSchema = z.object({
	concurrency: z.number().min(1, "Concurrency must be greater than 0").max(100, "Concurrency must be less than 100"),
	buffer_size: z.number().min(1, "Buffer size must be greater than 0").max(1000, "Buffer size must be less than 1000"),
});

// Proxy type schema
export const proxyTypeSchema = z.enum(["none", "http", "socks5", "environment"]);

// Proxy config schema
export const proxyConfigSchema = z
	.object({
		type: proxyTypeSchema,
		url: z.url("Must be a valid URL"),
		username: z.string().optional(),
		password: z.string().optional(),
	})
	.refine((data) => !(data.type === "http" || data.type === "socks5") || (data.url && data.url.trim().length > 0), {
		message: "Proxy URL is required when using HTTP or SOCKS5 proxy",
		path: ["url"],
	})
	.refine(
		(data) => {
			if ((data.type === "http" || data.type === "socks5") && data.url?.trim()) {
				try {
					new URL(data.url);
					return true;
				} catch {
					return false;
				}
			}
			return true;
		},
		{ message: "Must be a valid URL (e.g., http://proxy.example.com:8080)", path: ["url"] },
	);

// Proxy form schema - more lenient for form inputs with conditional validation
export const proxyFormConfigSchema = z
	.object({
		type: proxyTypeSchema,
		url: z.string().optional(),
		username: z.string().optional(),
		password: z.string().optional(),
	})
	.refine(
		(data) => {
			if (data.type === "none") {
				return true;
			}
			// URL is required when proxy type is http or socks5
			if (data.type === "http" || data.type === "socks5") {
				// Check for URL existence, non-empty, and valid format
				if (!data.url || data.url.trim().length === 0) return false;
			}
			return true;
		},
		{
			message: "Proxy URL is required when using HTTP or SOCKS5 proxy",
			path: ["url"],
		},
	)
	.refine(
		(data) => {
			// URL must be valid format when provided and proxy type requires it
			if ((data.type === "http" || data.type === "socks5") && data.url && data.url.trim().length > 0) {
				try {
					new URL(data.url);
					return true;
				} catch {
					return false;
				}
			}
			return true;
		},
		{
			message: "Must be a valid URL (e.g., http://proxy.example.com:8080)",
			path: ["url"],
		},
	);

// Allowed requests schema
export const allowedRequestsSchema = z.object({
	text_completion: z.boolean(),
	chat_completion: z.boolean(),
	chat_completion_stream: z.boolean(),
	embedding: z.boolean(),
	speech: z.boolean(),
	speech_stream: z.boolean(),
	transcription: z.boolean(),
	transcription_stream: z.boolean(),
});

// Custom provider config schema
export const customProviderConfigSchema = z.object({
	base_provider_type: knownProviderSchema,
	allowed_requests: allowedRequestsSchema.optional(),
});

// Form-specific custom provider config schema
export const formCustomProviderConfigSchema = z.object({
	base_provider_type: z.string().min(1, "Base provider type is required"),
	allowed_requests: allowedRequestsSchema.optional(),
});

// Full model provider config schema
export const modelProviderConfigSchema = z.object({
	keys: z.array(modelProviderKeySchema).min(1, "At least one key is required"),
	network_config: networkConfigSchema.optional(),
	concurrency_and_buffer_size: concurrencyAndBufferSizeSchema.optional(),
	proxy_config: proxyConfigSchema.optional(),
	send_back_raw_response: z.boolean().optional(),
	custom_provider_config: customProviderConfigSchema.optional(),
});

// Model provider schema
export const modelProviderSchema = modelProviderConfigSchema.extend({
	name: modelProviderNameSchema,
});

// Form-specific model provider config schema
export const formModelProviderConfigSchema = z.object({
	keys: z.array(modelProviderKeySchema).min(1, "At least one key is required"),
	network_config: networkConfigSchema.optional(),
	concurrency_and_buffer_size: concurrencyAndBufferSizeSchema.optional(),
	proxy_config: proxyConfigSchema.optional(),
	send_back_raw_response: z.boolean().optional(),
	custom_provider_config: formCustomProviderConfigSchema.optional(),
});

// Flexible model provider schema for form data - allows any string for name
export const formModelProviderSchema = formModelProviderConfigSchema.extend({
	name: z.string().min(1, "Provider name is required"),
});

// Add provider request schema
export const addProviderRequestSchema = z.object({
	provider: modelProviderNameSchema,
	keys: z.array(modelProviderKeySchema).min(1, "At least one key is required"),
	network_config: networkConfigSchema.optional(),
	concurrency_and_buffer_size: concurrencyAndBufferSizeSchema.optional(),
	proxy_config: proxyConfigSchema.optional(),
	send_back_raw_response: z.boolean().optional(),
	custom_provider_config: customProviderConfigSchema.optional(),
});

// Update provider request schema
export const updateProviderRequestSchema = z.object({
	keys: z.array(modelProviderKeySchema).min(1, "At least one key is required"),
	network_config: networkConfigSchema,
	concurrency_and_buffer_size: concurrencyAndBufferSizeSchema,
	proxy_config: proxyConfigSchema,
	send_back_raw_response: z.boolean().optional(),
	custom_provider_config: customProviderConfigSchema.optional(),
});

// Cache config schema
export const cacheConfigSchema = z.object({
	provider: modelProviderNameSchema,
	keys: z.array(modelProviderKeySchema).min(1, "At least one key is required"),
	embedding_model: z.string().min(1, "Embedding model is required"),
	ttl_seconds: z.number().min(1).default(3600),
	threshold: z.number().min(0).max(1).default(0.8),
	conversation_history_threshold: z.number().min(0).max(1).optional(),
	exclude_system_prompt: z.boolean().optional(),
	cache_by_model: z.boolean().default(false),
	cache_by_provider: z.boolean().default(false),
	created_at: z.string().optional(),
	updated_at: z.string().optional(),
});

// Core config schema
export const coreConfigSchema = z.object({
	drop_excess_requests: z.boolean().default(false),
	initial_pool_size: z.number().min(1).default(10),
	prometheus_labels: z.array(z.string()).default([]),
	enable_logging: z.boolean().default(true),
	enable_governance: z.boolean().default(false),
	enforce_governance_header: z.boolean().default(false),
	allow_direct_keys: z.boolean().default(false),
	allowed_origins: z.array(z.string()).default(["*"]),
	max_request_body_size_mb: z.number().min(1).default(100),
});

// Bifrost config schema
export const bifrostConfigSchema = z.object({
	client_config: coreConfigSchema,
	is_db_connected: z.boolean(),
	is_cache_connected: z.boolean(),
	is_logs_connected: z.boolean(),
});

// Network and proxy form schema - combined for the NetworkFormFragment
export const networkAndProxyFormSchema = z.object({
	network_config: networkFormConfigSchema.optional(),
	proxy_config: proxyFormConfigSchema.optional(),
});

// Proxy-only form schema for the ProxyFormFragment
export const proxyOnlyFormSchema = z.object({
	proxy_config: proxyFormConfigSchema,
});

// Network-only form schema for the NetworkFormFragment
export const networkOnlyFormSchema = z.object({
	network_config: networkFormConfigSchema,
});

// Performance form schema for the PerformanceFormFragment
export const performanceFormSchema = z.object({
	concurrency_and_buffer_size: z.object({
		concurrency: z.coerce
			.number("Concurrency must be a number")
			.min(1, "Concurrency must be greater than 0")
			.max(100000, "Concurrency must be less than 100000"),
		buffer_size: z.coerce
			.number("Buffer size must be a number")
			.min(1, "Buffer size must be greater than 0")
			.max(100000, "Buffer size must be less than 100000"),
	}),
	send_back_raw_response: z.boolean(),
});

// Export type inference helpers

export type ModelProviderKeySchema = z.infer<typeof modelProviderKeySchema>;
export type NetworkConfigSchema = z.infer<typeof networkConfigSchema>;
export type NetworkFormConfigSchema = z.infer<typeof networkFormConfigSchema>;
export type ProxyFormConfigSchema = z.infer<typeof proxyFormConfigSchema>;
export type NetworkAndProxyFormSchema = z.infer<typeof networkAndProxyFormSchema>;
export type ProxyOnlyFormSchema = z.infer<typeof proxyOnlyFormSchema>;
export type NetworkOnlyFormSchema = z.infer<typeof networkOnlyFormSchema>;
export type PerformanceFormSchema = z.infer<typeof performanceFormSchema>;
export type CustomProviderConfigSchema = z.infer<typeof customProviderConfigSchema>;
