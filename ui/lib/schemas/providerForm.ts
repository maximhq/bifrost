import { KnownProvidersNames } from "@/lib/constants/logs";
import { isValidDeployments, isValidVertexAuthCredentials, isValidEnvVar } from "@/lib/utils/validation";
import { z } from "zod";

// EnvVar schema for provider config fields that support env.VAR_NAME format
const EnvVarSchema = z.object({
	value: z.string().optional(),
	env_var: z.string().optional(),
	from_env: z.boolean().optional(),
});

// Base schemas for reusable types
const ProxyTypeSchema = z.enum(["none", "http", "socks5", "environment"]);

const ProxyConfigSchema = z
	.object({
		type: ProxyTypeSchema,
		url: z.string().optional(),
		username: z.string().optional(),
		password: z.string().optional(),
	})
	.superRefine((v, ctx) => {
		const needsUrl = v.type === "http" || v.type === "socks5";
		if (needsUrl && !(v.url && v.url.trim())) {
			ctx.addIssue({ code: "custom", path: ["url"], message: "Proxy URL is required for http/socks5" });
		}
		const user = v.username?.trim();
		const pass = v.password?.trim();
		if ((user && !pass) || (pass && !user)) {
			ctx.addIssue({
				code: "custom",
				path: ["password"],
				message: "Username and password must both be provided",
			});
		}
	});

const NetworkConfigSchema = z
	.object({
		base_url: z.string().optional(),
		extra_headers: z.record(z.string(), z.string()).optional(),
		default_request_timeout_in_seconds: z.number().min(1, "Timeout must be greater than 0 seconds"),
		max_retries: z.number().min(0, "Max retries cannot be negative"),
		retry_backoff_initial: z.number(),
		retry_backoff_max: z.number(),
	})
	.refine((v) => v.retry_backoff_initial <= v.retry_backoff_max, {
		message: "Initial backoff must be <= max backoff",
		path: ["retry_backoff_initial"],
	});

const ConcurrencyAndBufferSizeSchema = z
	.object({
		concurrency: z.number().min(1, "Concurrency must be greater than 0"),
		buffer_size: z.number().min(1, "Buffer size must be greater than 0"),
	})
	.refine((data) => data.concurrency <= data.buffer_size, {
		message: "Concurrency must be less than or equal to buffer size",
		path: ["concurrency"],
	});

const AllowedRequestsSchema = z.object({
	text_completion: z.boolean(),
	chat_completion: z.boolean(),
	chat_completion_stream: z.boolean(),
	responses: z.boolean(),
	responses_stream: z.boolean(),
	embedding: z.boolean(),
	speech: z.boolean(),
	speech_stream: z.boolean(),
	transcription: z.boolean(),
	transcription_stream: z.boolean(),
});

// Key configuration schemas
const AzureKeyConfigSchema = z.object({
	endpoint: z.string().min(1, "Endpoint is required for Azure keys"),
	deployments: z
		.union([z.record(z.string(), z.string()), z.string()])
		.optional()
		.refine((value) => !value || isValidDeployments(value), { message: "Valid Deployments (JSON object) are required for Azure keys" }),
	api_version: z.string().optional(),
	client_id: z.string().optional(),
	client_secret: z.string().optional(),
	tenant_id: z.string().optional(),
});

const VertexKeyConfigSchema = z.object({
	project_id: z.string().min(1, "Project ID is required for Vertex AI keys"),
	project_number: z.string().optional(),
	region: z.string().min(1, "Region is required for Vertex AI keys"),
	auth_credentials: z
		.string()
		.optional()
		.refine((value) => !value || isValidVertexAuthCredentials(value), {
			message: "Auth Credentials must be a valid JSON object or env.VAR format when provided",
		}),
	deployments: z
		.union([z.record(z.string(), z.string()), z.string()])
		.optional()
		.refine((value) => !value || isValidDeployments(value), { message: "Valid Deployments (JSON object) are required for Vertex AI keys" }),
});

// S3 bucket configuration for Bedrock batch operations
const S3BucketConfigSchema = z.object({
	bucket_name: z.string().min(1, "Bucket name is required"),
	prefix: z.string().optional(),
	is_default: z.boolean().optional(),
});

const BatchS3ConfigSchema = z.object({
	buckets: z.array(S3BucketConfigSchema).optional(),
});

const BedrockKeyConfigSchema = z
	.object({
		access_key: z.string(),
		secret_key: z.string(),
		session_token: z.string().optional(),
		region: z.string().min(1, "Region is required for Bedrock keys"),
		arn: z.string().optional(),
		deployments: z
			.union([z.record(z.string(), z.string()), z.string()])
			.optional()
			.refine((value) => !value || Object.keys(value).length === 0 || isValidDeployments(value), {
				message: "Valid Deployments (JSON object) are required for Bedrock keys",
			}),
		batch_s3_config: BatchS3ConfigSchema.optional(),
	})
	.refine(
		(data) => {
			const accessKey = data.access_key?.trim() || "";
			const secretKey = data.secret_key?.trim() || "";
			const bothEmpty = accessKey === "" && secretKey === "";
			const bothProvided = accessKey !== "" && secretKey !== "";

			// Either both empty (IAM role auth) or both provided (explicit credentials)
			if (!bothEmpty && !bothProvided) {
				return false;
			}

			// Check for session token when using IAM role path (both keys empty)
			const sessionToken = data.session_token?.trim() || "";
			if (bothEmpty && sessionToken !== "") {
				return false;
			}

			return true;
		},
		{
			message: "For Bedrock: either provide both Access Key and Secret Key, or leave both empty for IAM role authentication",
			path: ["access_key"],
		},
	);

const ReplicateKeyConfigSchema = z.object({
	deployments: z
		.union([z.record(z.string(), z.string()), z.string()])
		.optional()
		.refine((value) => !value || isValidDeployments(value), { message: "Valid Deployments (JSON object) are required for Replicate keys" }),
});

const SAPAICoreKeyConfigSchema = z.object({
	client_id: EnvVarSchema.refine((value) => isValidEnvVar(value), { message: "Client ID is required for SAP AI Core keys" }),
	client_secret: EnvVarSchema.refine((value) => isValidEnvVar(value), { message: "Client Secret is required for SAP AI Core keys" }),
	auth_url: EnvVarSchema.refine((value) => isValidEnvVar(value), { message: "Auth URL is required for SAP AI Core keys" }),
	base_url: EnvVarSchema.refine((value) => isValidEnvVar(value), { message: "Base URL is required for SAP AI Core keys" }),
	resource_group: EnvVarSchema.refine((value) => isValidEnvVar(value), { message: "Resource Group is required for SAP AI Core keys" }),
	deployments: z
		.union([z.record(z.string(), z.string()), z.string()])
		.optional()
		.refine((value) => !value || isValidDeployments(value), { message: "Valid Deployments (JSON object) are required for SAP AI Core keys" }),
});

const KeySchema = z.object({
	id: z.string(),
	name: z.string().min(1, "Name is required for the key"),
	value: z.string(),
	models: z.array(z.string()),
	weight: z.number().min(0.1, "Key weights must be between 0.1 and 1").max(1, "Key weights must be between 0.1 and 1"),
	azure_key_config: AzureKeyConfigSchema.optional(),
	vertex_key_config: VertexKeyConfigSchema.optional(),
	bedrock_key_config: BedrockKeyConfigSchema.optional(),
	replicate_key_config: ReplicateKeyConfigSchema.optional(),
	sapaicore_key_config: SAPAICoreKeyConfigSchema.optional(),
	use_for_batch_api: z.boolean().optional(),
});

// Main provider form schema
export const ProviderFormSchema = z
	.object({
		selectedProvider: z.string().min(1, "Please select a provider"),
		customProviderName: z.string().optional(),
		baseProviderType: z.enum([...KnownProvidersNames, ""]).optional(),
		keys: z.array(KeySchema),
		networkConfig: NetworkConfigSchema.optional(),
		performanceConfig: ConcurrencyAndBufferSizeSchema.optional(),
		proxyConfig: ProxyConfigSchema.optional(),
		sendBackRawResponse: z.boolean().default(false),
		allowedRequests: AllowedRequestsSchema.optional(),
		isDirty: z.boolean(),
	})
	.superRefine((data, ctx) => {
		// Custom provider validation
		const isCustomProvider =
			data.selectedProvider === "custom" ||
			!!data.customProviderName ||
			!!data.baseProviderType ||
			!KnownProvidersNames.includes(data.selectedProvider as (typeof KnownProvidersNames)[number]);

		if (isCustomProvider) {
			if (!data.customProviderName?.trim()) {
				ctx.addIssue({
					code: z.ZodIssueCode.custom,
					message: "Custom provider name is required",
					path: ["customProviderName"],
				});
			}

			if (!/^[a-z0-9_-]+$/.test(data.customProviderName?.trim() || "")) {
				ctx.addIssue({
					code: z.ZodIssueCode.custom,
					message: "Custom provider name must be lowercase alphanumeric and may include '-' or '_' (no spaces)",
					path: ["customProviderName"],
				});
			}

			if (!data.baseProviderType) {
				ctx.addIssue({
					code: z.ZodIssueCode.custom,
					message: "Base provider type is required for custom providers",
					path: ["baseProviderType"],
				});
			}

			if (KnownProvidersNames.includes(data.customProviderName?.trim() as (typeof KnownProvidersNames)[number])) {
				ctx.addIssue({
					code: z.ZodIssueCode.custom,
					message: "Custom provider name cannot be the same as a standard provider name",
					path: ["customProviderName"],
				});
			}
		}

		// Base URL validation for specific providers
		const baseURLRequired = data.selectedProvider === "ollama" || data.selectedProvider === "sgl" || isCustomProvider;
		if (baseURLRequired) {
			if (!data.networkConfig?.base_url) {
				ctx.addIssue({
					code: z.ZodIssueCode.custom,
					message: "Base URL is required for this provider",
					path: ["networkConfig", "base_url"],
				});
			}

			if (data.networkConfig?.base_url && !/^https?:\/\/.+/.test(data.networkConfig.base_url)) {
				ctx.addIssue({
					code: z.ZodIssueCode.custom,
					message: "Base URL must start with http:// or https://",
					path: ["networkConfig", "base_url"],
				});
			}
		}

		// Keys validation
		const keysRequired = data.selectedProvider === "custom" || !["ollama", "sgl"].includes(data.selectedProvider);
		if (keysRequired) {
			if (data.keys.length < 1) {
				ctx.addIssue({
					code: z.ZodIssueCode.custom,
					message: "At least one API key is required",
					path: ["keys"],
				});
			}

			// Validate individual key values based on provider type
			const effectiveProviderType = data.baseProviderType || data.selectedProvider;
			// Providers that don't use the standard API key field
			const noApiKeyProviders = ["vertex", "bedrock", "sapaicore"];
			data.keys.forEach((key, index) => {
				if (!noApiKeyProviders.includes(effectiveProviderType) && !key.value.trim()) {
					ctx.addIssue({
						code: z.ZodIssueCode.custom,
						message: "API key value cannot be empty",
						path: ["keys", index, "value"],
					});
				}
			});
		}
	});

export type ProviderFormData = z.infer<typeof ProviderFormSchema>;
