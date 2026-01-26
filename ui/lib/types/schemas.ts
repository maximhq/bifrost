import { KnownProvidersNames } from "@/lib/constants/logs";
import { z } from "zod";

// Base Zod schemas matching the TypeScript types

// Known provider schema
export const knownProviderSchema = z.enum(KnownProvidersNames as unknown as [string, ...string[]]);

// Custom provider name schema (branded type simulation)
export const customProviderNameSchema = z.string().min(1, "Custom provider name is required");

// Model provider name schema (union of known and custom providers)
export const modelProviderNameSchema = z.union([knownProviderSchema, customProviderNameSchema]);

// EnvVar schema - matches the Go EnvVar type from schemas/env.go
export const envVarSchema = z.object({
	value: z.string().optional(),
	env_var: z.string().optional(),
	from_env: z.boolean().optional(),
});

// Azure key config schema
export const azureKeyConfigSchema = z
	.object({
		endpoint: envVarSchema,
		deployments: z.union([z.record(z.string(), z.string()), z.string()]).optional(),
		api_version: envVarSchema.optional(),
		client_id: envVarSchema.optional(),
		client_secret: envVarSchema.optional(),
		tenant_id: envVarSchema.optional(),
	})
	.refine(
		(data) => {
			// If deployments is not provided, it's valid
			if (!data.deployments) return true;
			// If it's already an object, it's valid
			if (typeof data.deployments === "object") return true;
			// If it's a string, check if it's valid JSON or an env variable
			if (typeof data.deployments === "string") {
				const trimmed = data.deployments.trim();
				// Allow empty string
				if (trimmed === "") return true;
				// Allow env variables
				if (trimmed.startsWith("env.")) return true;
				// Validate JSON format
				try {
					const parsed = JSON.parse(trimmed);
					return typeof parsed === "object" && parsed !== null && !Array.isArray(parsed);
				} catch {
					return false;
				}
			}
			return false;
		},
		{
			message: "Deployments must be a valid JSON object or an environment variable reference",
			path: ["deployments"],
		},
	);

// Vertex key config schema
export const vertexKeyConfigSchema = z
	.object({
		project_id: envVarSchema,
		project_number: envVarSchema.optional(),
		region: envVarSchema,
		auth_credentials: envVarSchema.optional(),
		deployments: z.union([z.record(z.string(), z.string()), z.string()]).optional(),
	})
	.refine(
		(data) => {
			// If deployments is not provided, it's valid
			if (!data.deployments) return true;
			// If it's already an object, it's valid
			if (typeof data.deployments === "object") return true;
			// If it's a string, check if it's valid JSON or an env variable
			if (typeof data.deployments === "string") {
				const trimmed = data.deployments.trim();
				// Allow empty string
				if (trimmed === "") return true;
				// Allow env variables
				if (trimmed.startsWith("env.")) return true;
				// Validate JSON format
				try {
					const parsed = JSON.parse(trimmed);
					return typeof parsed === "object" && parsed !== null && !Array.isArray(parsed);
				} catch {
					return false;
				}
			}
			return false;
		},
		{
			message: "Deployments must be a valid JSON object or an environment variable reference",
			path: ["deployments"],
		},
	);

// S3 bucket configuration for Bedrock batch operations
export const s3BucketConfigSchema = z.object({
	bucket_name: z.string().min(1, "Bucket name is required"),
	prefix: z.string().optional(),
	is_default: z.boolean().optional(),
});

export const batchS3ConfigSchema = z.object({
	buckets: z.array(s3BucketConfigSchema).optional(),
});

// Bedrock key config schema
export const bedrockKeyConfigSchema = z
	.object({
		access_key: envVarSchema.optional(),
		secret_key: envVarSchema.optional(),
		session_token: envVarSchema.optional(),
		region: envVarSchema.optional(),
		arn: envVarSchema.optional(),
		deployments: z.union([z.record(z.string(), z.string()), z.string()]).optional(),
		batch_s3_config: batchS3ConfigSchema.optional(),
	})
	.refine(
		(data) => {
			// If deployments is not provided, it's valid
			if (!data.deployments) return true;
			// If it's already an object, it's valid
			if (typeof data.deployments === "object") return true;
			// If it's a string, check if it's valid JSON or an env variable
			if (typeof data.deployments === "string") {
				const trimmed = data.deployments.trim();
				// Allow empty string
				if (trimmed === "") return true;
				// Allow env variables
				if (trimmed.startsWith("env.")) return true;
				// Validate JSON format
				try {
					const parsed = JSON.parse(trimmed);
					return typeof parsed === "object" && parsed !== null && !Array.isArray(parsed);
				} catch {
					return false;
				}
			}
			return false;
		},
		{
			message: "Deployments must be a valid JSON object or an environment variable reference",
			path: ["deployments"],
		},
	);

// Model provider key schema
export const modelProviderKeySchema = z
	.object({
		id: z.string().min(1, "Id is required"),
		name: z.string().min(1, "Name is required"),
		value: envVarSchema.optional(),
		models: z.array(z.string()).default([]).optional(),
		weight: z.union([
			z.number().min(0, "Weight must be equal to or greater than 0").max(1, "Weight must be equal to or less than 1"),
			z
				.string()
				.transform((val) => {
					if (val === "") return 1.0;
					const num = parseFloat(val);
					if (isNaN(num)) {
						throw new z.ZodError([
							{
								code: "custom",
								message: "Weight must be a valid number",
								path: ["weight"],
							},
						]);
					}
					return num;
				})
				.pipe(z.number().min(0, "Weight must be equal to or greater than 0").max(1, "Weight must be equal to or less than 1")),
		]),
		azure_key_config: azureKeyConfigSchema.optional(),
		vertex_key_config: vertexKeyConfigSchema.optional(),
		bedrock_key_config: bedrockKeyConfigSchema.optional(),
		use_for_batch_api: z.boolean().optional(),
	})
	.refine(
		(data) => {
			// If bedrock_key_config or azure_key_config is present, value is not required
			if (data.bedrock_key_config || data.azure_key_config || data.vertex_key_config) {
				return true;
			}
			// Otherwise, value is required
			return data.value?.value && data.value?.value?.length > 0;
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
			.max(3600, "Timeout must be less than 3600 seconds"),
		max_retries: z.number().min(0, "Max retries must be greater than 0").max(10, "Max retries must be less than 10"),
		retry_backoff_initial: z.number().min(100),
		retry_backoff_max: z.number().min(1000),
	})
	.refine((d) => d.retry_backoff_initial <= d.retry_backoff_max, {
		message: "retry_backoff_initial must be <= retry_backoff_max",
		path: ["retry_backoff_initial"],
	});

// Network form schema - more lenient for form inputs
export const networkFormConfigSchema = z
	.object({
		base_url: z
			.union([
				z
					.string()
					.url("Must be a valid URL")
					.refine((url) => url.startsWith("https://") || url.startsWith("http://"), {
						message: "Must be a valid HTTP or HTTPS URL",
					}),
				z.string().length(0),
			])
			.optional(),
		extra_headers: z.record(z.string(), z.string()).optional(),
		default_request_timeout_in_seconds: z.coerce
			.number("Timeout must be a number")
			.min(1, "Timeout must be greater than 0 seconds")
			.max(172800, "Timeout must be less than 172800 seconds i.e. 48 hours"),
		max_retries: z.coerce
			.number("Max retries must be a number")
			.min(0, "Max retries must be greater than 0")
			.max(10, "Max retries must be less than 10"),
		retry_backoff_initial: z.coerce
			.number("Retry backoff initial must be a number")
			.min(100, "Retry backoff initial must be at least 100ms")
			.max(1000000, "Retry backoff initial must be at most 1000000ms"),
		retry_backoff_max: z.coerce
			.number("Retry backoff max must be a number")
			.min(100, "Retry backoff max must be at least 100ms")
			.max(1000000, "Retry backoff max must be at most 1000000ms"),
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
		ca_cert_pem: z.string().optional(),
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
		ca_cert_pem: z.string().optional(),
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
	text_completion_stream: z.boolean(),
	chat_completion: z.boolean(),
	chat_completion_stream: z.boolean(),
	responses: z.boolean(),
	responses_stream: z.boolean(),
	embedding: z.boolean(),
	speech: z.boolean(),
	speech_stream: z.boolean(),
	transcription: z.boolean(),
	transcription_stream: z.boolean(),
	image_generation: z.boolean(),
	image_generation_stream: z.boolean(),
	count_tokens: z.boolean(),
	list_models: z.boolean(),
});

// Custom provider config schema
export const customProviderConfigSchema = z
	.object({
		base_provider_type: knownProviderSchema,
		is_key_less: z.boolean().optional(),
		allowed_requests: allowedRequestsSchema.optional(),
		request_path_overrides: z.record(z.string(), z.string().optional()).optional(),
	})
	.refine(
		(data) => {
			if (data.base_provider_type === "bedrock") {
				return !data.is_key_less;
			}
			return true;
		},
		{
			message: "Is keyless is not allowed for Bedrock",
			path: ["is_key_less"],
		},
	);

// Form-specific custom provider config schema
export const formCustomProviderConfigSchema = z
	.object({
		base_provider_type: z.string().min(1, "Base provider type is required"),
		is_key_less: z.boolean().optional(),
		allowed_requests: allowedRequestsSchema.optional(),
		request_path_overrides: z.record(z.string(), z.string().optional()).optional(),
	})
	.refine(
		(data) => {
			if (data.base_provider_type === "bedrock") {
				return !data.is_key_less;
			}
			return true;
		},
		{
			message: "Is keyless is not allowed for Bedrock",
			path: ["is_key_less"],
		},
	);
// Full model provider config schema
export const modelProviderConfigSchema = z.object({
	keys: z.array(modelProviderKeySchema).min(1, "At least one key is required"),
	network_config: networkConfigSchema.optional(),
	concurrency_and_buffer_size: concurrencyAndBufferSizeSchema.optional(),
	proxy_config: proxyConfigSchema.optional(),
	send_back_raw_request: z.boolean().optional(),
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
	send_back_raw_request: z.boolean().optional(),
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
	send_back_raw_request: z.boolean().optional(),
	send_back_raw_response: z.boolean().optional(),
	custom_provider_config: customProviderConfigSchema.optional(),
});

// Update provider request schema
export const updateProviderRequestSchema = z.object({
	keys: z.array(modelProviderKeySchema).min(1, "At least one key is required"),
	network_config: networkConfigSchema,
	concurrency_and_buffer_size: concurrencyAndBufferSizeSchema,
	proxy_config: proxyConfigSchema,
	send_back_raw_request: z.boolean().optional(),
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
	disable_content_logging: z.boolean().default(false),
	enable_governance: z.boolean().default(false),
	enforce_governance_header: z.boolean().default(false),
	allow_direct_keys: z.boolean().default(false),
	allowed_origins: z.array(z.string()).default(["*"]),
	max_request_body_size_mb: z.number().min(1).default(100),
	mcp_agent_depth: z.number().min(1).default(10),
	mcp_tool_execution_timeout: z.number().min(1).default(30),
	mcp_code_mode_binding_level: z.enum(["server", "tool"]).default("server"),
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
	proxy_config: proxyFormConfigSchema.optional(),
});

// Network-only form schema for the NetworkFormFragment
export const networkOnlyFormSchema = z.object({
	network_config: networkFormConfigSchema.optional(),
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
	send_back_raw_request: z.boolean(),
	send_back_raw_response: z.boolean(),
});

// OTEL Configuration Schema
export const otelConfigSchema = z
	.object({
		service_name: z.string().optional(),
		collector_url: z.string().min(1, "Collector address is required"),
		trace_type: z
			.enum(["otel", "genai_extension", "vercel", "arize_otel"], {
				message: "Please select a trace type",
			})
			.default("otel"),
		headers: z.record(z.string(), z.string()).optional(),
		protocol: z
			.enum(["http", "grpc"], {
				message: "Please select a protocol",
			})
			.default("http"),
	})
	.superRefine((data, ctx) => {
		const value = (data.collector_url || "").trim();
		if (!value) {
			ctx.addIssue({
				code: "custom",
				path: ["collector_url"],
				message: "Collector address is required",
			});
			return;
		}

		if (data.protocol === "http") {
			try {
				const u = new URL(value);
				if (!(u.protocol === "http:" || u.protocol === "https:")) {
					ctx.addIssue({
						code: "custom",
						path: ["collector_url"],
						message: "Must be a valid HTTP or HTTPS URL",
					});
				}
			} catch {
				ctx.addIssue({
					code: "custom",
					path: ["collector_url"],
					message: "Must be a valid HTTP or HTTPS URL",
				});
			}
			return;
		}

		if (data.protocol === "grpc") {
			// Only allow host:port format, reject HTTP URLs
			const hostPortRegex = /^(?!https?:\/\/)([a-zA-Z0-9.-]+|\[[0-9a-fA-F:]+\]|\d{1,3}(?:\.\d{1,3}){3}):(\d{1,5})$/;
			const match = value.match(hostPortRegex);
			if (!match) {
				ctx.addIssue({
					code: "custom",
					path: ["collector_url"],
					message: "Must be in the format <host>:<port> for gRPC (e.g. otel-collector:4317)",
				});
				return;
			}
			const port = Number(match[2]);
			if (!(port >= 1 && port <= 65535)) {
				ctx.addIssue({
					code: "custom",
					path: ["collector_url"],
					message: "Port must be between 1 and 65535",
				});
			}
		}
	});

// OTEL form schema for the OtelFormFragment
export const otelFormSchema = z.object({
	enabled: z.boolean().default(false),
	otel_config: otelConfigSchema,
});

// Maxim Configuration Schema
export const maximConfigSchema = z.object({
	api_key: z
		.string()
		.min(1, "API key is required")
		.refine((key) => key.startsWith("sk_mx_"), {
			message: "API key must start with 'sk_mx_'",
		}),
	log_repo_id: z.string().optional(),
});

// Maxim form schema for the MaximFormFragment
export const maximFormSchema = z.object({
	enabled: z.boolean().default(false),
	maxim_config: maximConfigSchema,
});

// MCP Client update schema
export const mcpClientUpdateSchema = z.object({
	is_code_mode_client: z.boolean().optional(),
	is_ping_available: z.boolean().optional(),
	name: z
		.string()
		.min(1, "Name is required")
		.refine((val) => !val.includes("-"), { message: "Client name cannot contain hyphens" })
		.refine((val) => !val.includes(" "), { message: "Client name cannot contain spaces" })
		.refine((val) => !/^[0-9]/.test(val), { message: "Client name cannot start with a number" }),
	headers: z.record(z.string(), envVarSchema).optional(),
	tools_to_execute: z
		.array(z.string())
		.optional()
		.refine(
			(tools) => {
				if (!tools || tools.length === 0) return true;
				const hasWildcard = tools.includes("*");
				return !hasWildcard || tools.length === 1;
			},
			{ message: "Wildcard '*' cannot be combined with other tool names" },
		)
		.refine(
			(tools) => {
				if (!tools) return true;
				return tools.length === new Set(tools).size;
			},
			{ message: "Duplicate tool names are not allowed" },
		),
	tools_to_auto_execute: z
		.array(z.string())
		.optional()
		.refine(
			(tools) => {
				if (!tools || tools.length === 0) return true;
				const hasWildcard = tools.includes("*");
				return !hasWildcard || tools.length === 1;
			},
			{ message: "Wildcard '*' cannot be combined with other tool names" },
		)
		.refine(
			(tools) => {
				if (!tools) return true;
				return tools.length === new Set(tools).size;
			},
			{ message: "Duplicate tool names are not allowed" },
		),
});

// Global proxy type schema
export const globalProxyTypeSchema = z.enum(["http", "socks5", "tcp"]);

// Global proxy configuration schema
export const globalProxyConfigSchema = z
	.object({
		enabled: z.boolean(),
		type: globalProxyTypeSchema,
		url: z.string(),
		username: z.string().optional(),
		password: z.string().optional(),
		ca_cert_pem: z.string().optional(),
		no_proxy: z.string().optional(),
		timeout: z.number().min(0).optional(),
		skip_tls_verify: z.boolean().optional(),
		enable_for_scim: z.boolean(),
		enable_for_inference: z.boolean(),
		enable_for_api: z.boolean(),
	})
	.refine(
		(data) => {
			// URL is required when proxy is enabled
			if (data.enabled && (!data.url || data.url.trim().length === 0)) {
				return false;
			}
			return true;
		},
		{
			message: "Proxy URL is required when proxy is enabled",
			path: ["url"],
		},
	)
	.refine(
		(data) => {
			// Validate URL format when provided and enabled
			if (data.enabled && data.url && data.url.trim().length > 0) {
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

// Global proxy form schema for the ProxyView
export const globalProxyFormSchema = z.object({
	proxy_config: globalProxyConfigSchema,
});

// Global header filter configuration schema
// Controls which headers with the x-bf-eh-* prefix are forwarded to LLM providers
export const globalHeaderFilterConfigSchema = z.object({
	allowlist: z.array(z.string()).optional(), // If non-empty, only these headers are allowed
	denylist: z.array(z.string()).optional(), // Headers to always block
});

// Global header filter form schema for the HeaderFilterView
export const globalHeaderFilterFormSchema = z.object({
	header_filter_config: globalHeaderFilterConfigSchema,
});

// Export type inference helpers
export type EnvVar = z.infer<typeof envVarSchema>;
export type MCPClientUpdateSchema = z.infer<typeof mcpClientUpdateSchema>;
export type ModelProviderKeySchema = z.infer<typeof modelProviderKeySchema>;
export type NetworkConfigSchema = z.infer<typeof networkConfigSchema>;
export type NetworkFormConfigSchema = z.infer<typeof networkFormConfigSchema>;
export type ProxyFormConfigSchema = z.infer<typeof proxyFormConfigSchema>;
export type NetworkAndProxyFormSchema = z.infer<typeof networkAndProxyFormSchema>;
export type ProxyOnlyFormSchema = z.infer<typeof proxyOnlyFormSchema>;
export type OtelConfigSchema = z.infer<typeof otelConfigSchema>;
export type OtelFormSchema = z.infer<typeof otelFormSchema>;
export type MaximConfigSchema = z.infer<typeof maximConfigSchema>;
export type MaximFormSchema = z.infer<typeof maximFormSchema>;
export type NetworkOnlyFormSchema = z.infer<typeof networkOnlyFormSchema>;
export type PerformanceFormSchema = z.infer<typeof performanceFormSchema>;
export type CustomProviderConfigSchema = z.infer<typeof customProviderConfigSchema>;
export type GlobalProxyConfigSchema = z.infer<typeof globalProxyConfigSchema>;
export type GlobalProxyFormSchema = z.infer<typeof globalProxyFormSchema>;
export type GlobalHeaderFilterConfigSchema = z.infer<typeof globalHeaderFilterConfigSchema>;
export type GlobalHeaderFilterFormSchema = z.infer<typeof globalHeaderFilterFormSchema>;
