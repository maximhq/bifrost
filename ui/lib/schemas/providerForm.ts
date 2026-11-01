import { KnownProvidersNames } from "@/lib/constants/logs";
import i18n from "@/lib/i18n";
import { msg } from "@/lib/i18n/validationMessage";
import { aliasConfigSchema, oauthKeyConfigComplete, oauthKeyConfigSchema, secretVarSchema } from "@/lib/types/schemas";
import { isValidAliases, isValidVertexAuthCredentials } from "@/lib/utils/validation";
import { z } from "zod";

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
			ctx.addIssue({ code: "custom", path: ["url"], message: i18n.t("validation.proxyUrlIsRequiredForHttpSocks5") });
		}
		const user = v.username?.trim();
		const pass = v.password?.trim();
		if ((user && !pass) || (pass && !user)) {
			ctx.addIssue({
				code: "custom",
				path: ["password"],
				message: i18n.t("validation.usernameAndPasswordMustBothBeProvided"),
			});
		}
	});

const NetworkConfigSchema = z
	.object({
		base_url: z.string().optional(),
		extra_headers: z.record(z.string(), z.string()).optional(),
		default_request_timeout_in_seconds: z.number().min(1, msg("validation.timeoutMustBeGreaterThan0Seconds")),
		max_retries: z.number().min(0, msg("validation.maxRetriesCannotBeNegative")),
		retry_backoff_initial: z.number(),
		retry_backoff_max: z.number(),
		insecure_skip_verify: z.boolean().optional(),
		ca_cert_pem: z.union([z.string(), secretVarSchema]).optional(),
		stream_idle_timeout_in_seconds: z.number().int().min(5).max(3600).optional(),
		keep_alive_timeout_in_seconds: z.number().int().min(1).max(3600).optional(),
		max_conns_per_host: z.number().int().min(1).max(10000).optional(),
		enforce_http2: z.boolean().optional(),
		http2_ping_interval_in_seconds: z.number().int().min(0).max(3600).optional(),
	})
	.refine((v) => v.retry_backoff_initial <= v.retry_backoff_max, {
		...msg("validation.initialBackoffMustBeLessThanOrEqualToMax"),
		path: ["retry_backoff_initial"],
	});

const ConcurrencyAndBufferSizeSchema = z
	.object({
		concurrency: z.number().min(1, msg("validation.concurrencyMustBeGreaterThan0")),
		buffer_size: z.number().min(1, msg("validation.bufferSizeMustBeGreaterThan0")),
	})
	.refine((data) => data.concurrency <= data.buffer_size, {
		...msg("validation.concurrencyMustBeLessThanOrEqualToBufferSize"),
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
	endpoint: z.string().min(1, msg("validation.endpointIsRequiredForAzureKeys")),
	client_id: z.string().optional(),
	client_secret: z.string().optional(),
	tenant_id: z.string().optional(),
	use_speech_endpoints: z.boolean().optional(),
	use_voice_live_endpoints: z.boolean().optional(),
});

// AWS → GCP Workload Identity Federation block; see VertexAWSWorkloadIdentityConfig.
const VertexAWSWorkloadIdentitySchema = z.object({
	audience: z.string().min(1, msg("validation.workloadIdentityPoolProviderAudienceIsRequired")),
	service_account_email: z.string().optional(),
	token_lifetime_seconds: z.number().int().min(600).max(43200).optional(),
	aws_region: z.string().optional(),
	aws_role_arn: z.string().optional(),
});

const VertexKeyConfigSchema = z.object({
	project_id: z.string().min(1, msg("validation.projectIdIsRequiredForVertexKeys")),
	project_number: z.string().optional(),
	region: z.string().min(1, msg("validation.regionIsRequiredForVertexKeys")),
	auth_credentials: z
		.string()
		.optional()
		.refine((value) => !value || isValidVertexAuthCredentials(value), {
			...msg("validation.authCredentialsMustBeValidJsonOrEnvVar"),
		}),
	force_single_region: z.boolean().optional(),
	use_session_routing: z.boolean().optional(),
	aws_workload_identity: VertexAWSWorkloadIdentitySchema.optional(),
});

// S3 bucket configuration for Bedrock batch operations
const S3BucketConfigSchema = z.object({
	bucket_name: z.string().min(1, msg("validation.bucketNameIsRequired")),
	prefix: z.string().optional(),
	is_default: z.boolean().optional(),
});

const BatchS3ConfigSchema = z.object({
	buckets: z.array(S3BucketConfigSchema).optional(),
});

// A VPC endpoint value must be a DNS name, so it always contains a dot. The check exists to
// catch a pasted endpoint ID, which resolves to nothing: AWS appends a random string to the ID
// that the DNS name carries and the ID does not.
const VPCEndpointHostSchema = z
	.string()
	.refine((v) => v.trim() === "" || v.includes("."), msg("validation.enterTheVpcEndpointDnsName"))
	.optional();

const BedrockEndpointsSchema = z.object({
	runtime: VPCEndpointHostSchema,
	control_plane: VPCEndpointHostSchema,
	mantle: VPCEndpointHostSchema,
	agent_runtime: VPCEndpointHostSchema,
	s3: VPCEndpointHostSchema,
});

const BedrockKeyConfigSchema = z
	.object({
		access_key: z.string(),
		secret_key: z.string(),
		session_token: z.string().optional(),
		region: z.string().min(1, msg("validation.regionIsRequiredForBedrockKeys")),
		role_arn: z.string().optional(),
		external_id: z.string().optional(),
		session_name: z.string().optional(),
		batch_role_arn: z.string().optional(),
		arn: z.string().optional(),
		project_id: z.string().optional(),
		batch_s3_config: BatchS3ConfigSchema.optional(),
		endpoints: BedrockEndpointsSchema.optional(),
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
			...msg("validation.bedrockAccessKeyPair"),
			path: ["access_key"],
		},
	);

const BedrockMantleKeyConfigSchema = z
	.object({
		access_key: z.string(),
		secret_key: z.string(),
		session_token: z.string().optional(),
		region: z.string().min(1, msg("validation.regionIsRequiredForBedrockMantleKeys")),
		role_arn: z.string().optional(),
		external_id: z.string().optional(),
		session_name: z.string().optional(),
		project_id: z.string().optional(),
		endpoints: BedrockEndpointsSchema.optional(),
	})
	.refine(
		(data) => {
			const accessKey = data.access_key?.trim() || "";
			const secretKey = data.secret_key?.trim() || "";
			const bothEmpty = accessKey === "" && secretKey === "";
			const bothProvided = accessKey !== "" && secretKey !== "";
			// A session token alone cannot sign SigV4 requests; reject it without both keys.
			const sessionToken = data.session_token?.trim() || "";
			if (bothEmpty && sessionToken !== "") {
				return false;
			}
			// Either both empty (IAM role auth) or both provided (explicit credentials)
			return bothEmpty || bothProvided;
		},
		{
			...msg("validation.bedrockMantleAccessKeyPair"),
			path: ["access_key"],
		},
	);

const SageMakerKeyConfigSchema = z
	.object({
		access_key: z.string(),
		secret_key: z.string(),
		session_token: z.string().optional(),
		region: z.string().min(1, msg("validation.regionIsRequiredForSagemakerKeys")),
		role_arn: z.string().optional(),
		external_id: z.string().optional(),
		session_name: z.string().optional(),
		endpoints: z.object({ runtime: VPCEndpointHostSchema }).optional(),
	})
	.refine(
		(data) => {
			const accessKey = data.access_key?.trim() || "";
			const secretKey = data.secret_key?.trim() || "";
			const bothEmpty = accessKey === "" && secretKey === "";
			// A session token alone cannot mint a bearer token; reject it without both keys.
			if (bothEmpty && (data.session_token?.trim() || "") !== "") {
				return false;
			}
			return bothEmpty || (accessKey !== "" && secretKey !== "");
		},
		{
			...msg("validation.sagemakerAccessKeyPair"),
			path: ["access_key"],
		},
	);

// Optional for the same reason as replicateKeyConfigSchema in lib/types/schemas.ts:
// the registered-but-untouched switch leaves an empty object behind.
const ReplicateKeyConfigSchema = z.object({
	use_deployments_endpoint: z.boolean().optional(),
});

const KeySchema = z.object({
	id: z.string(),
	name: z.string().min(1, msg("validation.keyNameIsRequired")),
	value: z.string(),
	models: z.array(z.string()),
	weight: z.number().min(0.1, msg("validation.keyWeightsMustBeBetween01And1")).max(1, msg("validation.keyWeightsMustBeBetween01And1")),
	aliases: z
		.record(z.string(), aliasConfigSchema)
		.optional()
		.refine((value) => !value || isValidAliases(value), msg("validation.deploymentsMustHaveAModelIdOnEveryRow")),
	azure_key_config: AzureKeyConfigSchema.optional(),
	vertex_key_config: VertexKeyConfigSchema.optional(),
	bedrock_key_config: BedrockKeyConfigSchema.optional(),
	bedrock_mantle_key_config: BedrockMantleKeyConfigSchema.optional(),
	sagemaker_key_config: SageMakerKeyConfigSchema.optional(),
	replicate_key_config: ReplicateKeyConfigSchema.optional(),
	oauth_key_config: oauthKeyConfigSchema.optional(),
	use_for_batch_api: z.boolean().optional(),
});

// Main provider form schema
export const ProviderFormSchema = z
	.object({
		selectedProvider: z.string().min(1, msg("validation.pleaseSelectAProvider")),
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
					message: i18n.t("validation.customProviderNameIsRequired"),
					path: ["customProviderName"],
				});
			}

			if (!/^[a-z0-9_-]+$/.test(data.customProviderName?.trim() || "")) {
				ctx.addIssue({
					code: z.ZodIssueCode.custom,
					message: i18n.t("validation.customProviderNameFormat"),
					path: ["customProviderName"],
				});
			}

			if (!data.baseProviderType) {
				ctx.addIssue({
					code: z.ZodIssueCode.custom,
					message: i18n.t("validation.baseProviderTypeIsRequiredForCustomProviders"),
					path: ["baseProviderType"],
				});
			}

			if (KnownProvidersNames.includes(data.customProviderName?.trim() as (typeof KnownProvidersNames)[number])) {
				ctx.addIssue({
					code: z.ZodIssueCode.custom,
					message: i18n.t("validation.customProviderNameCannotMatchAStandardProvider"),
					path: ["customProviderName"],
				});
			}
		}

		// Base URL validation for specific providers
		const baseURLRequired = isCustomProvider;
		if (baseURLRequired) {
			if (!data.networkConfig?.base_url) {
				ctx.addIssue({
					code: z.ZodIssueCode.custom,
					message: i18n.t("validation.baseUrlIsRequiredForThisProvider"),
					path: ["networkConfig", "base_url"],
				});
			}

			if (data.networkConfig?.base_url && !/^https?:\/\/.+/.test(data.networkConfig.base_url)) {
				ctx.addIssue({
					code: z.ZodIssueCode.custom,
					message: i18n.t("validation.baseUrlMustStartWithHttpOrHttps"),
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
					message: i18n.t("validation.atLeastOneApiKeyIsRequired"),
					path: ["keys"],
				});
			}

			// Validate individual key values based on provider type
			const effectiveProviderType = data.baseProviderType || data.selectedProvider;
			data.keys.forEach((key, index) => {
				if (
					effectiveProviderType !== "vertex" &&
					effectiveProviderType !== "bedrock" &&
					!key.value.trim() &&
					!oauthKeyConfigComplete(key.oauth_key_config)
				) {
					ctx.addIssue({
						code: z.ZodIssueCode.custom,
						message: i18n.t("validation.apiKeyValueCannotBeEmpty"),
						path: ["keys", index, "value"],
					});
				}
			});
		}
	});

export type ProviderFormData = z.infer<typeof ProviderFormSchema>;