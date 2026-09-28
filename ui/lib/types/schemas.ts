import { KnownProvidersNames } from "@/lib/constants/logs";
import { MCP_MAX_PROTOCOL_VERSIONS } from "@/lib/constants/mcp";
import i18n from "@/lib/i18n";
import { isRedacted } from "@/lib/utils/validation";
import type { ParseKeys } from "i18next";
import { z } from "zod";

// Lazy validation message: resolved when a check fails, so it follows the active locale instead of the one at load time.
const msg = (key: ParseKeys<"common">) => ({ error: () => i18n.t(key) });

// Global error map - turns Zod's default messages into readable, human-friendly ones.
// Individual schemas can still override by passing their own message.
z.config({
	customError: (issue) => {
		if (issue.code === "invalid_type") {
			// Field is missing / undefined
			if (issue.input === undefined || issue.input === null) {
				return i18n.t("validation.fieldRequired");
			}
			const expected = issue.expected;
			const received = typeof issue.input;
			if (expected === "number") return i18n.t("validation.validNumber");
			if (expected === "string") return i18n.t("validation.validText");
			if (expected === "boolean") return i18n.t("validation.trueOrFalse");
			return i18n.t("validation.expectedReceived", { expected, received });
		}
		if (issue.code === "too_small") {
			if (issue.origin === "string" && issue.minimum === 1) {
				return i18n.t("validation.fieldRequired");
			}
			if (issue.origin === "number") {
				return i18n.t("validation.minNumber", { minimum: String(issue.minimum) });
			}
			if (issue.origin === "array" && issue.minimum === 1) {
				return i18n.t("validation.atLeastOneItem");
			}
		}
		if (issue.code === "too_big") {
			if (issue.origin === "number") {
				return i18n.t("validation.maxNumber", { maximum: String(issue.maximum) });
			}
			if (issue.origin === "string") {
				return i18n.t("validation.maxChars", { maximum: String(issue.maximum) });
			}
		}
		if (issue.code === "invalid_format") {
			if (issue.format === "url") return i18n.t("validation.validUrl");
			if (issue.format === "email") return i18n.t("validation.validEmail");
		}
		return undefined; // fall back to Zod default
	},
});

// Base Zod schemas matching the TypeScript types

// Known provider schema
export const knownProviderSchema = z.enum(KnownProvidersNames as unknown as [string, ...string[]]);

// Custom provider name schema (branded type simulation)
export const customProviderNameSchema = z.string().min(1, msg("validation.customProviderNameIsRequired"));

// Model provider name schema (union of known and custom providers)
export const modelProviderNameSchema = z.union([knownProviderSchema, customProviderNameSchema]);

// SecretVar schema - matches the Go SecretVar type from schemas/secretvar.go
export const _secretVarBase = z.object({
	value: z.string().optional(),
	ref: z.string().optional(),
	type: z.enum(["plain_text", "env", "vault"]).optional(),
});

// Extending the base schema
export const secretVarSchema = Object.assign(_secretVarBase, {
	required: (message: string) => _secretVarBase.refine((v) => !!v?.value?.trim() || !!v?.ref?.trim(), message),
});

// Helper to check if a secretVar field has a value or secret reference
export function isSecretVarSet(v: { value?: string; ref?: string } | undefined): boolean {
	if (!v) return false;
	return !!v.value?.trim() || !!v.ref?.trim();
}

// Message for private_key_jwt settings saved without the key they configure.
export const PRIVATE_KEY_REQUIRED_MESSAGE = "A private key is required for private_key_jwt";

// Message for a send method chosen without the secret it sends.
export const SECRET_REQUIRED_FOR_METHOD_MESSAGE = "A client secret is required to choose how it is sent";

// The id_jag upstream-authentication fields the form checks below read.
interface IDJAGUpstreamAuthFields {
	profile?: string;
	resource_auth_mode?: string;
	resource_client_secret?: { value?: string; ref?: string };
	resource_client_auth_method?: string;
	resource_private_key?: { value?: string; ref?: string };
	resource_key_id?: string;
	resource_signing_algorithm?: string;
	resource_assertion_audience?: string;
}

// hasPrivateKeySettingsWithoutKey reports an id_jag config set to private_key_jwt, or carrying a key
// ID, signing algorithm or assertion audience, without a private key. Saved as is, the payload would
// store a public client, so the forms refuse it.
export function hasPrivateKeySettingsWithoutKey(te?: IDJAGUpstreamAuthFields): boolean {
	if (te?.profile !== "id_jag" || isSecretVarSet(te.resource_private_key)) return false;
	return (
		te.resource_auth_mode === "private_key" ||
		!!(te.resource_key_id?.trim() || te.resource_signing_algorithm || te.resource_assertion_audience?.trim())
	);
}

// hasSendMethodWithoutSecret reports an id_jag config that names how to send the client secret but
// has none. The payload would drop the method and store a public client, which the server refuses.
export function hasSendMethodWithoutSecret(te?: IDJAGUpstreamAuthFields): boolean {
	return te?.profile === "id_jag" && !!te.resource_client_auth_method && !isSecretVarSet(te.resource_client_secret);
}

// Azure key config schema
export const azureKeyConfigSchema = z
	.object({
		_auth_type: z.enum(["api_key", "entra_id", "default_credential"]).optional(),
		endpoint: secretVarSchema.optional(),
		client_id: secretVarSchema.optional(),
		client_secret: secretVarSchema.optional(),
		tenant_id: secretVarSchema.optional(),
		scopes: z.array(z.string()).optional(),
		use_speech_endpoints: z.boolean().optional(),
		use_voice_live_endpoints: z.boolean().optional(),
	})
	.refine((data) => isSecretVarSet(data.endpoint), {
		error: () => i18n.t("validation.endpointIsRequired"),
		path: ["endpoint"],
	})
	.refine(
		(data) => {
			// When using Entra ID, all three fields are required
			if (data._auth_type === "entra_id") {
				return isSecretVarSet(data.client_id) && isSecretVarSet(data.client_secret) && isSecretVarSet(data.tenant_id);
			}
			// Otherwise, if any Entra ID field is set, all three must be set
			const hasClientId = isSecretVarSet(data.client_id);
			const hasClientSecret = isSecretVarSet(data.client_secret);
			const hasTenantId = isSecretVarSet(data.tenant_id);
			const anyEntraField = hasClientId || hasClientSecret || hasTenantId;
			if (!anyEntraField) return true;
			return hasClientId && hasClientSecret && hasTenantId;
		},
		{
			error: () => i18n.t("validation.clientIdClientSecretAndTenantIdAreAllRequired"),
			path: ["client_id"],
		},
	);

// Vertex AWS Workload Identity Federation block. The audience is the only mandatory field; the
// service account, region and role hop are optional refinements of the exchange.
export const vertexAWSWorkloadIdentitySchema = z.object({
	audience: secretVarSchema.optional(),
	service_account_email: secretVarSchema.optional(),
	token_lifetime_seconds: z
		.number()
		.int(msg("validation.tokenLifetimeMustBeAWholeNumberOfSeconds"))
		.min(600, msg("validation.tokenLifetimeMustBeAtLeast600Seconds"))
		.max(43200, msg("validation.tokenLifetimeMustBeAtMost43200Seconds"))
		.optional(),
	aws_region: secretVarSchema.optional(),
	aws_role_arn: secretVarSchema.optional(),
});

// Vertex key config schema
export const vertexKeyConfigSchema = z
	.object({
		_auth_type: z.enum(["service_account", "service_account_json", "api_key", "aws_workload_identity"]).optional(),
		project_id: secretVarSchema.optional(),
		project_number: secretVarSchema.optional(),
		region: secretVarSchema.optional(),
		auth_credentials: secretVarSchema.optional(),
		force_single_region: z.boolean().optional(),
		use_session_routing: z.boolean().optional(),
		aws_workload_identity: vertexAWSWorkloadIdentitySchema.optional(),
	})
	.refine((data) => isSecretVarSet(data.project_id), {
		error: () => i18n.t("validation.projectIdIsRequired"),
		path: ["project_id"],
	})
	.refine((data) => isSecretVarSet(data.region), {
		error: () => i18n.t("validation.regionIsRequired"),
		path: ["region"],
	})
	.refine(
		(data) => {
			// When using service_account_json auth, auth_credentials is required
			if (data._auth_type === "service_account_json") {
				return isSecretVarSet(data.auth_credentials);
			}
			return true;
		},
		{
			error: () => i18n.t("validation.authCredentialsIsRequiredForServiceAccountJsonAuthentication"),
			path: ["auth_credentials"],
		},
	)
	.refine(
		(data) => {
			// The federation tab hides every other credential input, so the audience is what
			// makes the key usable; without it the key would silently fall back to ADC.
			if (data._auth_type === "aws_workload_identity") {
				return isSecretVarSet(data.aws_workload_identity?.audience);
			}
			return true;
		},
		{
			error: () => i18n.t("validation.workloadIdentityPoolProviderAudienceIsRequired"),
			path: ["aws_workload_identity", "audience"],
		},
	)
	.refine(
		(data) => {
			// Federation and a credentials JSON are two different identities; never send both.
			if (isSecretVarSet(data.aws_workload_identity?.audience)) {
				return !isSecretVarSet(data.auth_credentials);
			}
			return true;
		},
		{
			error: () => i18n.t("validation.removeTheAuthCredentialsJsonWhenUsingAwsWorkloadIdentity"),
			path: ["auth_credentials"],
		},
	);

// S3 bucket configuration for Bedrock batch operations
export const s3BucketConfigSchema = z.object({
	bucket_name: z.string().min(1, msg("validation.bucketNameIsRequired")),
	prefix: z.string().optional(),
	is_default: z.boolean().optional(),
});

export const batchS3ConfigSchema = z.object({
	buckets: z.array(s3BucketConfigSchema).optional(),
});

// Interface VPC endpoint hosts, one per AWS endpoint service Bifrost dials for Bedrock.
export const bedrockEndpointsSchema = z.object({
	runtime: secretVarSchema.optional(),
	control_plane: secretVarSchema.optional(),
	mantle: secretVarSchema.optional(),
	agent_runtime: secretVarSchema.optional(),
	s3: secretVarSchema.optional(),
});

// Bedrock key config schema
export const bedrockKeyConfigSchema = z
	.object({
		_auth_type: z.enum(["iam_role", "explicit", "api_key"]).optional(),
		access_key: secretVarSchema.optional(),
		secret_key: secretVarSchema.optional(),
		session_token: secretVarSchema.optional(),
		region: secretVarSchema.optional(),
		role_arn: secretVarSchema.optional(),
		external_id: secretVarSchema.optional(),
		session_name: secretVarSchema.optional(),
		batch_role_arn: secretVarSchema.optional(),
		arn: secretVarSchema.optional(),
		project_id: secretVarSchema.optional(),
		batch_s3_config: batchS3ConfigSchema.optional(),
		endpoints: bedrockEndpointsSchema.optional(),
	})
	.refine(
		(data) => {
			// Region is required for Bedrock
			return isSecretVarSet(data.region);
		},
		{
			error: () => i18n.t("validation.regionIsRequired"),
			path: ["region"],
		},
	)
	.refine(
		(data) => {
			// When using explicit credentials, both access_key and secret_key are required
			if (data._auth_type === "explicit") {
				return isSecretVarSet(data.access_key) && isSecretVarSet(data.secret_key);
			}
			// Otherwise, if either is set both must be set
			const hasAccessKey = isSecretVarSet(data.access_key);
			const hasSecretKey = isSecretVarSet(data.secret_key);
			if (!hasAccessKey && !hasSecretKey) return true;
			return hasAccessKey && hasSecretKey;
		},
		{
			error: () => i18n.t("validation.bothAccessKeyAndSecretKeyAreRequiredForExplicit"),
			path: ["access_key"],
		},
	);

// Bedrock Mantle key config schema (SigV4 credentials; no ARN / batch S3 config)
export const bedrockMantleKeyConfigSchema = z
	.object({
		_auth_type: z.enum(["iam_role", "explicit", "api_key"]).optional(),
		access_key: secretVarSchema.optional(),
		secret_key: secretVarSchema.optional(),
		session_token: secretVarSchema.optional(),
		region: secretVarSchema.optional(),
		role_arn: secretVarSchema.optional(),
		external_id: secretVarSchema.optional(),
		session_name: secretVarSchema.optional(),
		project_id: secretVarSchema.optional(),
		endpoints: bedrockEndpointsSchema.optional(),
	})
	.refine((data) => isSecretVarSet(data.region), {
		error: () => i18n.t("validation.regionIsRequired"),
		path: ["region"],
	})
	.refine(
		(data) => {
			// Explicit auth must carry both keys; a region-only config fails at request time.
			if (data._auth_type === "explicit") {
				return isSecretVarSet(data.access_key) && isSecretVarSet(data.secret_key);
			}
			// If either access_key or secret_key is set, both must be set.
			const hasAccessKey = isSecretVarSet(data.access_key);
			const hasSecretKey = isSecretVarSet(data.secret_key);
			// IAM-role path: both keys empty is valid, but a lone session token cannot sign SigV4.
			if (!hasAccessKey && !hasSecretKey) return !isSecretVarSet(data.session_token);
			return hasAccessKey && hasSecretKey;
		},
		{
			error: () => i18n.t("validation.bothAccessKeyAndSecretKeyAreRequiredForExplicit"),
			path: ["access_key"],
		},
	);

// SageMaker key config schema: AWS credentials Bifrost mints the endpoint bearer token from,
// or a pre-minted token in the key value (api_key auth).
export const sageMakerKeyConfigSchema = z
	.object({
		_auth_type: z.enum(["iam_role", "explicit", "api_key"]).optional(),
		access_key: secretVarSchema.optional(),
		secret_key: secretVarSchema.optional(),
		session_token: secretVarSchema.optional(),
		region: secretVarSchema.optional(),
		role_arn: secretVarSchema.optional(),
		external_id: secretVarSchema.optional(),
		session_name: secretVarSchema.optional(),
		endpoints: z.object({ runtime: secretVarSchema.optional() }).optional(),
	})
	.refine((data) => isSecretVarSet(data.region), {
		message: "Region is required",
		path: ["region"],
	})
	.refine(
		(data) => {
			if (data._auth_type === "explicit") {
				return isSecretVarSet(data.access_key) && isSecretVarSet(data.secret_key);
			}
			const hasAccessKey = isSecretVarSet(data.access_key);
			const hasSecretKey = isSecretVarSet(data.secret_key);
			// Default-chain path: both keys empty is valid, but a lone session token cannot mint a token.
			if (!hasAccessKey && !hasSecretKey) return !isSecretVarSet(data.session_token);
			return hasAccessKey && hasSecretKey;
		},
		{
			message: "Both Access Key and Secret Key are required for explicit credentials",
			path: ["access_key"],
		},
	);

// VLLM key config schema
export const vllmKeyConfigSchema = z
	.object({
		url: secretVarSchema.optional(),
		model_name: z.string().trim().min(1, msg("validation.modelNameIsRequired")),
	})
	.refine((data) => isSecretVarSet(data.url), {
		error: () => i18n.t("validation.serverUrlIsRequired"),
		path: ["url"],
	});

export const replicateKeyConfigSchema = z.object({
	use_deployments_endpoint: z.boolean().optional(),
});

// A secret that resolves elsewhere cannot have its shape judged here, so format checks apply
// only to literals.
//
// Only a non-empty `ref` counts as indirect. The `type` tag alone is not enough: a payload
// like { value: "not-numeric", ref: "", type: "env" } still carries its value inline, and
// trusting the tag would let any malformed literal skip every check below.
const isSecretVarRef = (v: { value?: string; ref?: string; type?: string } | undefined): boolean => !!v?.ref?.trim();

// A stored secret comes back from the API masked ("******" or "1234****...****5678"), and the
// edit form round-trips that mask untouched so the server keeps the original. Judging the
// mask's shape would fail every edit of a key whose credentials were not all retyped.
//
// isRedacted also treats env./vault. prefixes as hidden, but here references live in `ref`.
// With no ref the server stores an "env.x" value as plain text, so it gets checked like one.
const isSecretVarMasked = (v: { value?: string; ref?: string; type?: string } | undefined): boolean => {
	const value = v?.value ?? "";
	if (value.startsWith("env.") || value.startsWith("vault.")) return false;
	return isRedacted(value);
};

// The id_jag fields resourceCredentialResendRequired compares.
interface IDJAGResourceTarget {
	audience?: string;
	resource_token_endpoint?: string;
	resource_assertion_audience?: string;
	resource_client_secret?: { value?: string; ref?: string; type?: string };
	resource_private_key?: { value?: string; ref?: string; type?: string };
}

// RESOURCE_CREDENTIAL_RESEND_MESSAGE explains why a saved secret or key must be typed again.
export const RESOURCE_CREDENTIAL_RESEND_MESSAGE =
	"Re-enter it: the audience, upstream token endpoint or assertion audience changed, so the saved value is not sent to the new target";

// resourceCredentialResendRequired mirrors the server's rule: an edit that changes where the
// upstream credential is sent (the audience it is discovered from, the token endpoint, or the
// client assertion's audience) must carry a saved literal secret or key again rather than its
// mask. It returns the field to re-enter, or undefined when the edit can be saved as is.
export function resourceCredentialResendRequired(
	saved: IDJAGResourceTarget | undefined,
	edited: IDJAGResourceTarget | undefined,
): "resource_client_secret" | "resource_private_key" | undefined {
	if (!saved || !edited) return undefined;
	const same = (a?: string, b?: string) => (a ?? "").trim() === (b ?? "").trim();
	const targetChanged =
		!same(saved.audience, edited.audience) ||
		!same(saved.resource_token_endpoint, edited.resource_token_endpoint) ||
		!same(saved.resource_assertion_audience, edited.resource_assertion_audience);
	if (!targetChanged) return undefined;
	// A credential kept as an env or vault reference is resent as the reference, which the server
	// accepts; only a saved literal, shown masked, has to be typed again.
	const savedLiteral = (v?: { value?: string; ref?: string; type?: string }) => !v?.ref?.trim() && isSecretVarMasked(v);
	if (savedLiteral(edited.resource_client_secret)) return "resource_client_secret";
	if (savedLiteral(edited.resource_private_key)) return "resource_private_key";
	return undefined;
}

// oauthKeyConfigComplete reports whether the block carries a usable credential for its grant.
// It is the check the outer key schema uses to decide whether an empty key value is fine.
export const oauthKeyConfigComplete = (data: Record<string, unknown> | undefined): boolean => {
	if (!data) return false;
	const d = data as {
		grant_type?: string;
		token_url?: { value?: string; ref?: string };
		client_id?: { value?: string; ref?: string };
		client_secret?: { value?: string; ref?: string };
		private_key?: { value?: string; ref?: string };
		issuer?: string;
		audience?: string;
	};
	if (!isSecretVarSet(d.token_url)) return false;
	if (d.grant_type === "client_credentials") return isSecretVarSet(d.client_id) && isSecretVarSet(d.client_secret);
	if (d.grant_type === "jwt_bearer") return isSecretVarSet(d.private_key) && !!d.issuer?.trim() && !!d.audience?.trim();
	return false;
};

const ASYMMETRIC_PEM_PRIVATE_KEY = /^-----BEGIN (RSA PRIVATE KEY|EC PRIVATE KEY|PRIVATE KEY)-----\s*\n([\s\S]*?)\n\s*-----END \1-----$/;
const isLiteralAsymmetricPEM = (v: { value?: string; ref?: string; type?: string } | undefined): boolean => {
	if (!isSecretVarSet(v)) return true;
	if (isSecretVarRef(v) || isSecretVarMasked(v)) return true;
	const body = (v?.value ?? "").replace(/\\n/g, "\n").trim();
	const match = ASYMMETRIC_PEM_PRIVATE_KEY.exec(body);
	return !!match && isCompleteDERSequence(match[2]);
};
// isCompleteDERSequence reports whether a PEM body decodes to exactly one DER SEQUENCE, the outer
// shape of every RSA and EC private key encoding the server accepts. A pasted key cut short keeps
// its markers and still decodes, but its declared length no longer matches, which is what the
// server's parser rejects at save. The full key structure is left to the server.
const isCompleteDERSequence = (b64: string): boolean => {
	let bytes: string;
	try {
		bytes = atob(b64.replace(/\s+/g, ""));
	} catch {
		return false;
	}
	if (bytes.length < 2 || bytes.charCodeAt(0) !== 0x30) return false;
	const first = bytes.charCodeAt(1);
	if (first < 0x80) return 2 + first === bytes.length;
	const lengthBytes = first & 0x7f;
	if (lengthBytes < 1 || lengthBytes > 3 || bytes.length < 2 + lengthBytes) return false;
	let length = 0;
	for (let i = 0; i < lengthBytes; i++) length = length * 256 + bytes.charCodeAt(2 + i);
	return 2 + lengthBytes + length === bytes.length;
};
const parseLiteralURL = (v: { value?: string; ref?: string; type?: string } | undefined): URL | undefined => {
	try {
		return new URL((v?.value ?? "").trim());
	} catch {
		return undefined;
	}
};
// Mirrors the server: the URL is shown in plain text wherever the key is read back, so it may
// not hide a credential, and a token endpoint takes its parameters through extra_params.
const literalURLCarriesForbiddenParts = (v: { value?: string; ref?: string; type?: string } | undefined): boolean => {
	if (!isSecretVarSet(v)) return false;
	if (isSecretVarRef(v) || isSecretVarMasked(v)) return false;
	const u = parseLiteralURL(v);
	return !!u && (u.username !== "" || u.password !== "" || u.search !== "" || u.hash !== "");
};
// hasHTTPSOrLoopbackScheme accepts https URLs, and plain http only on a loopback host, for local
// testing. Same loopback rule as the server (net.IP.IsLoopback): any 127.x.x.x, ::1, or localhost.
// The server re-checks every URL and refuses private targets when it connects; this only lets the
// form reject a value the server would refuse.
const hasHTTPSOrLoopbackScheme = (raw: string): boolean => {
	try {
		const u = new URL(raw.trim());
		if (u.protocol === "https:") return u.hostname !== "";
		const host = u.hostname.toLowerCase();
		return (
			u.protocol === "http:" &&
			(host === "localhost" || host === "[::1]" || host === "::1" || /^127\.\d{1,3}\.\d{1,3}\.\d{1,3}$/.test(host))
		);
	} catch {
		return false;
	}
};

// isHTTPSOrLoopbackURL is hasHTTPSOrLoopbackScheme for the id_jag URLs, which the server also
// refuses when they carry credentials or a fragment.
export const isHTTPSOrLoopbackURL = (raw: string): boolean => {
	try {
		const u = new URL(raw.trim());
		if (u.username !== "" || u.password !== "" || u.hash !== "") return false;
	} catch {
		return false;
	}
	return hasHTTPSOrLoopbackScheme(raw);
};

// isLiteralHTTPSOrLoopbackURL checks only the scheme: the OAuth token URL's credentials, query and
// fragment are reported separately, pointing at extra_params.
const isLiteralHTTPSOrLoopbackURL = (v: { value?: string; ref?: string; type?: string } | undefined): boolean => {
	if (!isSecretVarSet(v)) return true;
	if (isSecretVarRef(v) || isSecretVarMasked(v)) return true;
	return hasHTTPSOrLoopbackScheme(v?.value ?? "");
};

// OAuth key config schema. Honoured by openai-based providers: the key value may be empty
// when this block is whole for its grant. Literal values are shape-checked so a typo fails in
// the form rather than at the first inference call; references resolve elsewhere.
export const oauthKeyConfigSchema = z
	.object({
		_auth_type: z.enum(["api_key", "oauth"]).optional(),
		grant_type: z.enum(["client_credentials", "jwt_bearer"]).optional(),
		token_url: secretVarSchema.optional(),
		client_id: secretVarSchema.optional(),
		client_secret: secretVarSchema.optional(),
		private_key: secretVarSchema.optional(),
		scopes: z.array(z.string()).optional(),
		audience: z.string().optional(),
		issuer: z.string().optional(),
		subject: z.string().optional(),
		key_id: z.string().optional(),
		signing_algorithm: z.enum(["RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512"]).optional(),
		auth_style: z.enum(["header", "body"]).optional(),
		extra_params: z.record(z.string(), z.string()).optional(),
		// Range-checked in superRefine, only while JWT bearer is active: the field is hidden on
		// the other tabs, and a stale value in it must not block saving. 0 means "default".
		assertion_lifetime_seconds: z.number().int().optional(),
	})
	.superRefine((data, ctx) => {
		// The API-key tab leaves this block inert; only an OAuth block is checked.
		if (data._auth_type === "api_key") return;
		const started =
			data._auth_type === "oauth" ||
			isSecretVarSet(data.token_url) ||
			isSecretVarSet(data.client_id) ||
			isSecretVarSet(data.client_secret) ||
			isSecretVarSet(data.private_key);
		if (!started) return;
		if (!data.grant_type) {
			ctx.addIssue({ code: "custom", path: ["grant_type"], message: i18n.t("validation.chooseAGrantType") });
		}
		if (!isSecretVarSet(data.token_url)) {
			ctx.addIssue({ code: "custom", path: ["token_url"], message: i18n.t("validation.tokenUrlIsRequired") });
		} else if (!isLiteralHTTPSOrLoopbackURL(data.token_url)) {
			ctx.addIssue({ code: "custom", path: ["token_url"], message: i18n.t("validation.tokenUrlMustBeAnHttpsUrl") });
		} else if (literalURLCarriesForbiddenParts(data.token_url)) {
			ctx.addIssue({
				code: "custom",
				path: ["token_url"],
				message: i18n.t("validation.tokenUrlMustNotCarryUserinfoAQueryOrA"),
			});
		}
		if (data.grant_type === "client_credentials") {
			if (!isSecretVarSet(data.client_id)) {
				ctx.addIssue({ code: "custom", path: ["client_id"], message: i18n.t("validation.clientIdIsRequiredForClientCredentials") });
			}
			if (!isSecretVarSet(data.client_secret)) {
				ctx.addIssue({ code: "custom", path: ["client_secret"], message: i18n.t("validation.clientSecretIsRequiredForClientCredentials") });
			}
		}
		if (data.grant_type === "jwt_bearer") {
			if (!isSecretVarSet(data.private_key)) {
				ctx.addIssue({ code: "custom", path: ["private_key"], message: i18n.t("validation.privateKeyIsRequiredForJwtBearer") });
			} else if (!isLiteralAsymmetricPEM(data.private_key)) {
				ctx.addIssue({ code: "custom", path: ["private_key"], message: i18n.t("validation.privateKeyMustBeAnRsaOrEcPemBlock") });
			}
			if (!data.issuer?.trim()) {
				ctx.addIssue({ code: "custom", path: ["issuer"], message: i18n.t("validation.issuerIsRequiredForJwtBearer") });
			}
			if (!data.audience?.trim()) {
				ctx.addIssue({ code: "custom", path: ["audience"], message: i18n.t("validation.audienceIsRequiredForJwtBearer") });
			}
			const lifetime = data.assertion_lifetime_seconds;
			if (lifetime !== undefined && lifetime !== 0 && (lifetime < 1 || lifetime > 3600)) {
				ctx.addIssue({
					code: "custom",
					path: ["assertion_lifetime_seconds"],
					message: i18n.t("validation.assertionLifetimeMustBeBetween1And3600Seconds0"),
				});
			}
		}
	});

// Ollama key config schema
export const ollamaKeyConfigSchema = z
	.object({
		url: secretVarSchema.optional(),
	})
	.refine((data) => isSecretVarSet(data.url), {
		error: () => i18n.t("validation.serverUrlIsRequired"),
		path: ["url"],
	});

// SGL key config schema
export const sglKeyConfigSchema = z
	.object({
		url: secretVarSchema.optional(),
	})
	.refine((data) => isSecretVarSet(data.url), {
		error: () => i18n.t("validation.serverUrlIsRequired"),
		path: ["url"],
	});

// Cloudflare key config schema
export const cloudflareKeyConfigSchema = z
	.object({
		account_id: secretVarSchema.optional(),
	})
	.refine((data) => isSecretVarSet(data.account_id), {
		error: () => i18n.t("validation.accountIdIsRequired"),
		path: ["account_id"],
	});

// Databricks key config schema
export const databricksKeyConfigSchema = z
	.object({
		workspace_url: secretVarSchema.optional(),
		api_format: z.enum(["auto", "model_serving", "ai_gateway"]).optional(),
		client_id: secretVarSchema.optional(),
		client_secret: secretVarSchema.optional(),
		forward_gateway_tags: z.boolean().optional(),
		// UI-only discriminator, mirroring the azure/vertex/bedrock key forms.
		_auth_type: z.enum(["pat", "oauth_m2m"]).optional(),
	})
	.refine((data) => isSecretVarSet(data.workspace_url), {
		error: () => i18n.t("validation.workspaceUrlIsRequired"),
		path: ["workspace_url"],
	})
	// The OAuth M2M tab hides the token field, so a key saved from it needs the whole service
	// principal or it has no credentials at all. Mirrors the Azure Entra ID rule.
	.refine((data) => data._auth_type !== "oauth_m2m" || (isSecretVarSet(data.client_id) && isSecretVarSet(data.client_secret)), {
		error: () => i18n.t("validation.clientIdAndClientSecretAreBothRequiredForOauth"),
		path: ["client_id"],
	})
	// A service principal only authenticates as a pair; half of one is always a misconfiguration.
	.refine((data) => isSecretVarSet(data.client_id) === isSecretVarSet(data.client_secret), {
		error: () => i18n.t("validation.clientIdAndClientSecretMustBeSetTogether"),
		path: ["client_secret"],
	});

// Model family enum schema — must mirror schemas.ModelFamily in Go.
export const modelFamilySchema = z.enum([
	"anthropic",
	"openai",
	"mistral",
	"cohere",
	"gemini",
	"gemma",
	"llama",
	"imagen",
	"veo",
	"nova",
	"titan",
]);

// AliasConfig schema — mirrors schemas.AliasConfig with the embedded
// provider sub-configs flattened to top-level optional fields (matches Go's
// embedded-pointer-struct JSON output).
const aliasConfigObjectSchema = z.object({
	model_id: z.string().trim().min(1, msg("validation.modelIdIsRequired")),
	model_name: z.string().trim().optional(),
	model_family: modelFamilySchema.optional(),
	description: z.string().optional(),
	region: secretVarSchema.optional(),
	// Azure overrides
	api_version: z.string().optional(),
	anthropic_version: z.string().optional(),
	endpoint: secretVarSchema.optional(),
	use_speech_endpoints: z.boolean().optional(),
	use_voice_live_endpoints: z.boolean().optional(),
	// Shared per-alias project override (Vertex / Bedrock / Bedrock Mantle)
	project_id: secretVarSchema.optional(),
	// Vertex overrides
	project_number: secretVarSchema.optional(),
	force_single_region: z.boolean().optional(),
	vertex_endpoint: z
		.object({
			endpoint_id: z.string().regex(/^[A-Za-z0-9_-]+$/, "Enter a Vertex endpoint ID or paste a supported endpoint URL"),
			format: z.enum(["openai_chat"]),
			method: z.enum(["raw_predict", "chat_completions"]).optional(),
		})
		.optional(),
	// Bedrock overrides
	inference_profile_arn: secretVarSchema.optional(),
	// Replicate overrides
	use_deployments_endpoint: z.boolean().optional(),
	use_anthropic_endpoints: z.boolean().optional(),
	use_openai_endpoints: z.boolean().optional(),
});

// The Go server emits the legacy string wire shape (`{"my-alias": "model-id"}`)
// for aliases that only carry ModelID — see AliasConfig.MarshalJSON. Accept
// both shapes here so edit-time validation doesn't reject hydrated state.
export const aliasConfigSchema = z.preprocess(
	(value) => (typeof value === "string" ? { model_id: value } : value),
	aliasConfigObjectSchema,
);

// Model provider key schema
export const modelProviderKeySchema = z
	.object({
		id: z.string().min(1, msg("validation.idIsRequired")),
		name: z.string().min(1, msg("validation.nameIsRequired")),
		value: secretVarSchema.optional(),
		models: z.array(z.string()).optional().default(["*"]),
		blacklisted_models: z.array(z.string()).default([]).optional(),
		weight: z
			.union([z.number(), z.string()])
			.transform((val, ctx) => {
				if (typeof val === "number") return val;
				if (val.trim() === "") return 1.0;
				// Use Number() rather than parseFloat() so that strings like "0.5abc"
				// are rejected outright instead of silently parsing to 0.5.
				const num = Number(val);
				if (!Number.isFinite(num)) {
					ctx.addIssue({
						code: "custom",
						message: i18n.t("validation.weightMustBeAValidNumberBetween0And1"),
					});
					return z.NEVER;
				}
				return num;
			})
			.pipe(
				z.number().min(0, msg("validation.weightMustBeEqualToOrGreaterThan0")).max(1, msg("validation.weightMustBeEqualToOrLessThan1")),
			),
		aliases: z.record(z.string(), aliasConfigSchema).optional(),
		azure_key_config: azureKeyConfigSchema.optional(),
		vertex_key_config: vertexKeyConfigSchema.optional(),
		bedrock_key_config: bedrockKeyConfigSchema.optional(),
		bedrock_mantle_key_config: bedrockMantleKeyConfigSchema.optional(),
		sagemaker_key_config: sageMakerKeyConfigSchema.optional(),
		vllm_key_config: vllmKeyConfigSchema.optional(),
		replicate_key_config: replicateKeyConfigSchema.optional(),
		ollama_key_config: ollamaKeyConfigSchema.optional(),
		sgl_key_config: sglKeyConfigSchema.optional(),
		databricks_key_config: databricksKeyConfigSchema.optional(),
		cloudflare_key_config: cloudflareKeyConfigSchema.optional(),
		oauth_key_config: oauthKeyConfigSchema.optional(),
		use_for_batch_api: z.boolean().optional(),
		use_anthropic_endpoints: z.boolean().optional(),
		use_openai_endpoints: z.boolean().optional(),
		enabled: z.boolean().optional(),
	})
	.refine(
		(data) => {
			if (data.vllm_key_config || data.ollama_key_config || data.sgl_key_config) {
				return true;
			}
			// An OAuth-minted key needs no value. Decided from the credentials when the UI
			// discriminator is absent, for the same reset reason as Databricks below.
			if (data.oauth_key_config) {
				if (data.oauth_key_config._auth_type === "oauth") {
					return true;
				}
				if (data.oauth_key_config._auth_type === undefined && oauthKeyConfigComplete(data.oauth_key_config)) {
					return true;
				}
			}
			// Databricks authenticates with a personal access token (the key value) or with an
			// OAuth M2M service principal; only require a key value on the token path.
			// Decided from the credentials themselves rather than from _auth_type: the form
			// re-seeds that discriminator only on mount, so a reset (which the key form does
			// when the key resolves asynchronously) leaves it undefined on an M2M key that is
			// perfectly valid.
			if (data.databricks_key_config) {
				const databricks = data.databricks_key_config;
				if (databricks._auth_type === "oauth_m2m") {
					return true;
				}
				// Only infer the M2M path from the credentials when the discriminator is absent;
				// an explicit token method must still carry a token.
				if (databricks._auth_type === undefined && (isSecretVarSet(databricks.client_id) || isSecretVarSet(databricks.client_secret))) {
					return true;
				}
				return isSecretVarSet(data.value);
			}
			// Bedrock Mantle authenticates via SigV4 (its key config) or a Bearer key — only require
			// a top-level API key when the user explicitly chose the api_key auth method.
			if (data.bedrock_mantle_key_config) {
				if (data.bedrock_mantle_key_config._auth_type === "api_key") {
					return isSecretVarSet(data.value);
				}
				return true;
			}
			// SageMaker mints a bearer token from AWS credentials; a key value (a pre-minted
			// token) is only required when the user chose the api_key auth method.
			if (data.sagemaker_key_config) {
				if (data.sagemaker_key_config._auth_type === "api_key") {
					return isSecretVarSet(data.value);
				}
				return true;
			}
			// Azure requires API key only when using api_key auth
			if (data.azure_key_config) {
				if (data.azure_key_config._auth_type === "api_key") {
					return isSecretVarSet(data.value);
				}
				return true;
			}
			// Bedrock only requires API key when using api_key auth
			if (data.bedrock_key_config) {
				if (data.bedrock_key_config._auth_type === "api_key") {
					return isSecretVarSet(data.value);
				}
				return true;
			}
			// Vertex requires API key only when using api_key auth
			if (data.vertex_key_config) {
				if (data.vertex_key_config._auth_type === "api_key") {
					return isSecretVarSet(data.value);
				}
				return true;
			}
			// Otherwise, value is required
			return isSecretVarSet(data.value);
		},
		{
			error: () => i18n.t("validation.apiKeyIsRequired"),
			path: ["value"],
		},
	);

// Network config schema
export const networkConfigSchema = z
	.object({
		base_url: z.union([z.string().url(msg("validation.validUrl")), z.string().length(0)]).optional(),
		extra_headers: z.record(z.string(), z.string()).optional(),
		default_request_timeout_in_seconds: z
			.number()
			.min(1, msg("validation.timeoutMustBeGreaterThan0Seconds"))
			.max(3600, msg("validation.timeoutMustBeLessThan3600Seconds")),
		max_retries: z.number().min(0, msg("validation.maxRetriesMustBeGreaterThan0")).max(10, msg("validation.maxRetriesMustBeLessThan10")),
		retry_backoff_initial: z.number().min(100),
		retry_backoff_max: z.number().min(100),
		insecure_skip_verify: z.boolean().optional(),
		ca_cert_pem: secretVarSchema.optional(),
		stream_idle_timeout_in_seconds: z
			.number()
			.int(msg("validation.streamIdleTimeoutMustBeAWholeNumberOfSeconds"))
			.min(5, msg("validation.streamIdleTimeoutMustBeAtLeast5Seconds"))
			.max(3600, msg("validation.streamIdleTimeoutMustBeAtMost3600SecondsI"))
			.optional(),
		keep_alive_timeout_in_seconds: z
			.number()
			.int(msg("validation.keepAliveTimeoutMustBeAWholeNumberOfSeconds"))
			.min(1, msg("validation.keepAliveTimeoutMustBeAtLeast1Second"))
			.max(3600, msg("validation.keepAliveTimeoutMustBeAtMost3600SecondsI"))
			.optional(),
		max_conns_per_host: z
			.number()
			.int(msg("validation.maxConnectionsMustBeAWholeNumber"))
			.min(1, msg("validation.maxConnectionsMustBeAtLeast1"))
			.max(10000, msg("validation.maxConnectionsMustBeAtMost10000"))
			.optional(),
		enforce_http2: z.boolean().optional(),
		http2_ping_interval_in_seconds: z
			.number()
			.int(msg("validation.http2PingIntervalMustBeAWholeNumberOf"))
			.min(0, msg("validation.http2PingIntervalMustBeAtLeast0Seconds"))
			.max(3600, msg("validation.http2PingIntervalMustBeAtMost3600Seconds"))
			.optional(),
		allow_private_network: z.boolean().optional(),
	})
	.refine((d) => d.retry_backoff_initial <= d.retry_backoff_max, {
		error: () => i18n.t("validation.retryBackoffInitialMustBeRetryBackoffMax"),
		path: ["retry_backoff_initial"],
	});

// Network form schema - more lenient for form inputs
export const networkFormConfigSchema = z
	.object({
		base_url: z
			.union([
				z
					.string()
					.url(msg("validation.validUrl"))
					.refine((url) => url.startsWith("https://") || url.startsWith("http://"), {
						error: () => i18n.t("validation.mustBeAValidHttpOrHttpsUrl"),
					}),
				z.string().length(0),
			])
			.optional(),
		extra_headers: z.record(z.string(), secretVarSchema).optional(),
		default_request_timeout_in_seconds: z.coerce
			.number(msg("validation.timeoutMustBeANumber"))
			.min(1, msg("validation.timeoutMustBeGreaterThan0Seconds"))
			.max(172800, msg("validation.timeoutMustBeLessThan172800SecondsIE48")),
		max_retries: z.coerce
			.number(msg("validation.maxRetriesMustBeANumber"))
			.min(0, msg("validation.maxRetriesMustBeGreaterThan0"))
			.max(10, msg("validation.maxRetriesMustBeLessThan10")),
		retry_backoff_initial: z.coerce
			.number(msg("validation.retryBackoffInitialMustBeANumber"))
			.min(100, msg("validation.retryBackoffInitialMustBeAtLeast100ms"))
			.max(1000000, msg("validation.retryBackoffInitialMustBeAtMost1000000ms")),
		retry_backoff_max: z.coerce
			.number(msg("validation.retryBackoffMaxMustBeANumber"))
			.min(100, msg("validation.retryBackoffMaxMustBeAtLeast100ms"))
			.max(1000000, msg("validation.retryBackoffMaxMustBeAtMost1000000ms")),
		insecure_skip_verify: z.boolean().optional(),
		ca_cert_pem: secretVarSchema.optional(),
		stream_idle_timeout_in_seconds: z.coerce
			.number(msg("validation.streamIdleTimeoutMustBeANumber"))
			.int(msg("validation.streamIdleTimeoutMustBeAWholeNumberOfSeconds"))
			.min(5, msg("validation.streamIdleTimeoutMustBeAtLeast5Seconds"))
			.max(3600, msg("validation.streamIdleTimeoutMustBeAtMost3600SecondsI"))
			.optional(),
		keep_alive_timeout_in_seconds: z.coerce
			.number(msg("validation.keepAliveTimeoutMustBeANumber"))
			.int(msg("validation.keepAliveTimeoutMustBeAWholeNumberOfSeconds"))
			.min(1, msg("validation.keepAliveTimeoutMustBeAtLeast1Second"))
			.max(3600, msg("validation.keepAliveTimeoutMustBeAtMost3600SecondsI"))
			.optional(),
		max_conns_per_host: z.coerce
			.number(msg("validation.maxConnectionsMustBeANumber"))
			.int(msg("validation.maxConnectionsMustBeAWholeNumber"))
			.min(1, msg("validation.maxConnectionsMustBeAtLeast1"))
			.max(10000, msg("validation.maxConnectionsMustBeAtMost10000"))
			.optional(),
		enforce_http2: z.boolean().optional(),
		http2_ping_interval_in_seconds: z.coerce
			.number(msg("validation.http2PingIntervalMustBeANumber"))
			.int(msg("validation.http2PingIntervalMustBeAWholeNumberOf"))
			.min(0, msg("validation.http2PingIntervalMustBeAtLeast0Seconds"))
			.max(3600, msg("validation.http2PingIntervalMustBeAtMost3600Seconds"))
			.optional(),
		allow_private_network: z.boolean().optional(),
	})
	.refine((d) => d.retry_backoff_initial <= d.retry_backoff_max, {
		error: () => i18n.t("validation.initialBackoffMustBeLessThanOrEqualToMax"),
		path: ["retry_backoff_initial"],
	});

// Concurrency and buffer size schema
export const concurrencyAndBufferSizeSchema = z.object({
	concurrency: z
		.number()
		.min(1, msg("validation.concurrencyMustBeGreaterThan0"))
		.max(100, msg("validation.concurrencyMustBeLessThanOrEqualTo100")),
	buffer_size: z
		.number()
		.min(1, msg("validation.bufferSizeMustBeGreaterThan0"))
		.max(1000, msg("validation.bufferSizeMustBeLessThanOrEqualTo1000")),
});

// Proxy type schema
export const proxyTypeSchema = z.enum(["none", "http", "socks5", "environment"]);

// Proxy config schema
export const proxyConfigSchema = z
	.object({
		type: proxyTypeSchema,
		url: secretVarSchema.optional(),
		username: secretVarSchema.optional(),
		password: secretVarSchema.optional(),
		ca_cert_pem: secretVarSchema.optional(),
	})
	.refine(
		(data) =>
			!(data.type === "http" || data.type === "socks5") ||
			data.url?.type === "env" ||
			data.url?.type === "vault" ||
			(data.url?.value && data.url.value.trim().length > 0),
		{
			error: () => i18n.t("validation.proxyUrlIsRequiredWhenUsingHttpOrSocks5Proxy"),
			path: ["url"],
		},
	)
	.refine(
		(data) => {
			if ((data.type === "http" || data.type === "socks5") && data.url?.value?.trim()) {
				if (isRedacted(data.url.value)) {
					return true;
				}
				try {
					new URL(data.url.value);
					return true;
				} catch {
					return false;
				}
			}
			return true;
		},
		{
			error: () => i18n.t("validation.mustBeAValidUrlEGHttpProxyExample"),
			path: ["url"],
		},
	);

// Proxy form schema - more lenient for form inputs with conditional validation
export const proxyFormConfigSchema = z
	.object({
		type: proxyTypeSchema,
		url: secretVarSchema.optional(),
		username: secretVarSchema.optional(),
		password: secretVarSchema.optional(),
		ca_cert_pem: secretVarSchema.optional(),
	})
	.refine(
		(data) => {
			if (data.type === "none") {
				return true;
			}
			// URL is required when proxy type is http or socks5
			if (data.type === "http" || data.type === "socks5") {
				// Env-backed URLs may have empty resolved value before env resolution.
				if (!!(data.url?.type && data.url?.type !== "plain_text") || data.url?.ref) return true;
				// Literal URLs must be non-empty.
				if (!data.url?.value || data.url.value.trim().length === 0) return false;
			}
			return true;
		},
		{
			error: () => i18n.t("validation.proxyUrlIsRequiredWhenUsingHttpOrSocks5Proxy"),
			path: ["url"],
		},
	)
	.refine(
		(data) => {
			// URL must be valid format when provided and proxy type requires it
			if ((data.type === "http" || data.type === "socks5") && data.url?.value && data.url.value.trim().length > 0) {
				if (isRedacted(data.url.value)) {
					return true;
				}
				try {
					new URL(data.url.value);
					return true;
				} catch {
					return false;
				}
			}
			return true;
		},
		{
			error: () => i18n.t("validation.mustBeAValidUrlEGHttpProxyExample"),
			path: ["url"],
		},
	);

// OpenAI Config tab
export const openaiConfigFormSchema = z.object({
	disable_store: z.boolean(),
});

export type OpenAIConfigFormSchema = z.infer<typeof openaiConfigFormSchema>;

// Prompt cache tab
export const cacheControlInjectionPointSchema = z
	.object({
		location: z.literal("message"),
		role: z.enum(["system", "developer", "user", "assistant"]).optional(),
		index: z.number().int().optional(),
	})
	.refine((p) => p.role !== undefined || p.index !== undefined, {
		error: () => i18n.t("validation.setARoleAnIndexOrBothAPointWith"),
	});

export const promptCacheFormSchema = z.object({
	auto_inject: z.boolean(),
	ttl: z.string().optional(),
	cache_control_injection_points: z.array(cacheControlInjectionPointSchema).optional(),
});

export type PromptCacheFormSchema = z.infer<typeof promptCacheFormSchema>;

// INJECTED_TOOL_NAME_PATTERN mirrors config.schema.json: the function name every
// supported provider accepts.
export const INJECTED_TOOL_NAME_PATTERN = /^[a-zA-Z0-9_-]{1,64}$/;

// The saved injected_tools list, as the provider API returns and accepts it.
export const injectedToolsConfigSchema = z.array(
	z.object({
		name: z.string().regex(INJECTED_TOOL_NAME_PATTERN, msg("validation.use164LettersDigitsOr")),
		mcp_client_name: z.string().min(1, msg("validation.pickAnMcpServer")),
		tool_name: z.string().min(1, msg("validation.pickATool")),
	}),
);

// One row of the provider Tools tab. mcp_client_id drives the server picker; only the
// name, client name and tool name are saved. A row is savable only once its server's
// name has resolved and a tool is picked, so Save never writes a half-filled row.
export const injectedToolRowSchema = z.object({
	name: z.string().regex(INJECTED_TOOL_NAME_PATTERN, msg("validation.use164LettersDigitsOr")),
	mcp_client_id: z.string().min(1, msg("validation.pickAnMcpServer")),
	mcp_client_name: z.string().min(1, msg("validation.waitingForTheMcpServerToLoad")),
	tool_name: z.string().min(1, msg("validation.pickATool")),
});

// The name is what the model calls and what decides which client tool a row replaces,
// so it must be unique. The same MCP tool twice would offer the model one tool under two
// names. The server rejects both; these flag them on the row instead.
export const injectedToolsFormSchema = z
	.object({
		tools: z.array(injectedToolRowSchema),
	})
	.superRefine((data, ctx) => {
		const names = new Map<string, number>();
		const refs = new Map<string, number>();
		data.tools.forEach((tool, index) => {
			if (tool.name !== "") {
				if (names.has(tool.name)) {
					ctx.addIssue({ code: "custom", path: ["tools", index, "name"], message: i18n.t("validation.anotherToolAlreadyUsesThisName") });
				} else {
					names.set(tool.name, index);
				}
			}
			if (tool.mcp_client_name !== "" && tool.tool_name !== "") {
				const ref = `${tool.mcp_client_name}\u0000${tool.tool_name}`;
				if (refs.has(ref)) {
					ctx.addIssue({ code: "custom", path: ["tools", index, "tool_name"], message: i18n.t("validation.thisMcpToolIsAlreadyInjected") });
				} else {
					refs.set(ref, index);
				}
			}
		});
	});

export type InjectedToolRowSchema = z.infer<typeof injectedToolRowSchema>;
export type InjectedToolsFormSchema = z.infer<typeof injectedToolsFormSchema>;

// PASSTHROUGH_METHODS are the HTTP methods a passthrough route rule can name.
export const PASSTHROUGH_METHODS = ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"] as const;
export type PassthroughMethod = (typeof PASSTHROUGH_METHODS)[number];

export function isPassthroughMethod(method: string): boolean {
	return (PASSTHROUGH_METHODS as readonly string[]).includes(method);
}

// passthroughPathIssue mirrors the server's check on a rule path and returns the first
// problem, or undefined when the path is valid.
export function passthroughPathIssue(path: string): string | undefined {
	if (path === "") return i18n.t("providers.passthrough.validation.enterPath", { ns: "models" });
	if (!path.startsWith("/")) return i18n.t("providers.passthrough.validation.mustStartWithSlash", { ns: "models" });
	if (/[?#%\\[\]]/.test(path)) return i18n.t("providers.passthrough.validation.forbiddenChars", { ns: "models" });
	if (path === "/") return undefined;
	const segments = path.slice(1).split("/");
	for (let i = 0; i < segments.length; i++) {
		const segment = segments[i];
		if (segment === "" || segment === "." || segment === "..")
			return i18n.t("providers.passthrough.validation.emptySegments", { ns: "models" });
		if (segment.includes("**") && (segment !== "**" || i !== segments.length - 1))
			return i18n.t("providers.passthrough.validation.doubleStarLast", { ns: "models" });
	}
	return undefined;
}

// One saved passthrough rule: a path pattern with optional methods.
export const passthroughRouteRuleSchema = z
	.object({
		methods: z.array(z.string()).optional(),
		path: z.string().optional(),
	})
	.superRefine((rule, ctx) => {
		rule.methods?.forEach((method, index) => {
			if (!isPassthroughMethod(method)) {
				ctx.addIssue({
					code: "custom",
					path: ["methods", index],
					message: i18n.t("providers.passthrough.validation.unsupportedMethod", { ns: "models", method }),
				});
			}
		});
		const issue = passthroughPathIssue(rule.path ?? "");
		if (issue) ctx.addIssue({ code: "custom", path: ["path"], message: issue });
	});

// The saved passthrough policy, as the provider API returns and accepts it.
export const passthroughPolicySchema = z.object({
	allowed_routes: z.array(passthroughRouteRuleSchema),
});

// One row of the provider Passthrough tab's rule editor.
export const passthroughRuleRowSchema = z.object({
	methods: z.array(z.enum(PASSTHROUGH_METHODS)),
	path: z.string(),
});

// The Passthrough tab. Rules are checked only in custom mode; the other modes ignore them.
export const passthroughFormSchema = z
	.object({
		mode: z.enum(["all", "custom", "off"]),
		rules: z.array(passthroughRuleRowSchema),
	})
	.superRefine((data, ctx) => {
		if (data.mode !== "custom") return;
		if (data.rules.length === 0) {
			ctx.addIssue({ code: "custom", path: ["rules"], message: i18n.t("providers.passthrough.validation.addRule", { ns: "models" }) });
			return;
		}
		data.rules.forEach((rule, index) => {
			const issue = passthroughPathIssue(rule.path);
			if (issue) ctx.addIssue({ code: "custom", path: ["rules", index, "path"], message: issue });
		});
	});

export type PassthroughRuleRowSchema = z.infer<typeof passthroughRuleRowSchema>;
export type PassthroughFormSchema = z.infer<typeof passthroughFormSchema>;

// Allowed requests schema
export const allowedRequestsSchema = z.object({
	text_completion: z.boolean(),
	text_completion_stream: z.boolean(),
	chat_completion: z.boolean(),
	chat_completion_stream: z.boolean(),
	responses: z.boolean(),
	responses_stream: z.boolean(),
	responses_retrieve: z.boolean().optional(),
	responses_delete: z.boolean().optional(),
	responses_cancel: z.boolean().optional(),
	responses_input_items: z.boolean().optional(),
	embedding: z.boolean(),
	speech: z.boolean(),
	speech_stream: z.boolean(),
	transcription: z.boolean(),
	transcription_stream: z.boolean(),
	image_generation: z.boolean(),
	image_generation_stream: z.boolean(),
	image_edit: z.boolean(),
	image_edit_stream: z.boolean(),
	image_variation: z.boolean(),
	ocr: z.boolean().optional(),
	ocr_stream: z.boolean().optional(),
	rerank: z.boolean(),
	decisions: z.boolean().optional(),
	video_generation: z.boolean(),
	video_edit: z.boolean(),
	video_retrieve: z.boolean(),
	video_download: z.boolean(),
	video_delete: z.boolean(),
	video_list: z.boolean(),
	video_remix: z.boolean(),
	passthrough: z.boolean().optional(),
	passthrough_stream: z.boolean().optional(),
	count_tokens: z.boolean(),
	list_models: z.boolean(),
	model_retrieve: z.boolean().optional(),
	websocket_responses: z.boolean(),
	realtime: z.boolean(),
	live: z.boolean().optional(),
});

// Custom provider config schema
export const customProviderConfigSchema = z
	.object({
		base_provider_type: knownProviderSchema,
		is_key_less: z.boolean().optional(),
		does_not_send_done_marker: z.boolean().optional(),
		wait_for_usage: z.boolean().optional(),
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
			error: () => i18n.t("validation.isKeylessIsNotAllowedForBedrock"),
			path: ["is_key_less"],
		},
	);

// Form-specific custom provider config schema
export const formCustomProviderConfigSchema = z
	.object({
		base_provider_type: z.string().min(1, msg("validation.baseProviderTypeIsRequired")),
		is_key_less: z.boolean().optional(),
		does_not_send_done_marker: z.boolean().optional(),
		wait_for_usage: z.boolean().optional(),
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
			error: () => i18n.t("validation.isKeylessIsNotAllowedForBedrock"),
			path: ["is_key_less"],
		},
	);

// Full model provider config schema
export const modelProviderConfigSchema = z.object({
	keys: z.array(modelProviderKeySchema).min(1, msg("validation.atLeastOneKeyIsRequired")),
	network_config: networkConfigSchema.optional(),
	concurrency_and_buffer_size: concurrencyAndBufferSizeSchema.optional(),
	proxy_config: proxyConfigSchema.optional(),
	send_back_raw_request: z.boolean().optional(),
	send_back_raw_response: z.boolean().optional(),
	store_raw_request_response: z.boolean().optional(),
	ignore_provider_cost: z.boolean().optional(),
	custom_provider_config: customProviderConfigSchema.optional(),
});

// Model provider schema
export const modelProviderSchema = modelProviderConfigSchema.extend({
	name: modelProviderNameSchema,
});

// Form-specific model provider config schema
export const formModelProviderConfigSchema = z.object({
	keys: z.array(modelProviderKeySchema).min(1, msg("validation.atLeastOneKeyIsRequired")),
	network_config: networkConfigSchema.optional(),
	concurrency_and_buffer_size: concurrencyAndBufferSizeSchema.optional(),
	proxy_config: proxyConfigSchema.optional(),
	send_back_raw_request: z.boolean().optional(),
	send_back_raw_response: z.boolean().optional(),
	store_raw_request_response: z.boolean().optional(),
	ignore_provider_cost: z.boolean().optional(),
	custom_provider_config: formCustomProviderConfigSchema.optional(),
});

// Flexible model provider schema for form data - allows any string for name
export const formModelProviderSchema = formModelProviderConfigSchema.extend({
	name: z.string().min(1, msg("validation.providerNameIsRequired")),
});

// Add provider request schema
export const addProviderRequestSchema = z.object({
	provider: modelProviderNameSchema,
	keys: z.array(modelProviderKeySchema).min(1, msg("validation.atLeastOneKeyIsRequired")),
	network_config: networkConfigSchema.optional(),
	concurrency_and_buffer_size: concurrencyAndBufferSizeSchema.optional(),
	proxy_config: proxyConfigSchema.optional(),
	send_back_raw_request: z.boolean().optional(),
	send_back_raw_response: z.boolean().optional(),
	store_raw_request_response: z.boolean().optional(),
	ignore_provider_cost: z.boolean().optional(),
	custom_provider_config: customProviderConfigSchema.optional(),
	openai_config: openaiConfigFormSchema.optional(),
	prompt_cache: promptCacheFormSchema.optional(),
	injected_tools: injectedToolsConfigSchema.nullable().optional(),
	passthrough: passthroughPolicySchema.nullable().optional(),
});

// Update provider request schema
export const updateProviderRequestSchema = z.object({
	keys: z.array(modelProviderKeySchema).min(1, msg("validation.atLeastOneKeyIsRequired")),
	network_config: networkConfigSchema,
	concurrency_and_buffer_size: concurrencyAndBufferSizeSchema,
	proxy_config: proxyConfigSchema,
	send_back_raw_request: z.boolean().optional(),
	send_back_raw_response: z.boolean().optional(),
	store_raw_request_response: z.boolean().optional(),
	ignore_provider_cost: z.boolean().optional(),
	custom_provider_config: customProviderConfigSchema.optional(),
	openai_config: openaiConfigFormSchema.optional(),
	prompt_cache: promptCacheFormSchema.optional(),
	injected_tools: injectedToolsConfigSchema.nullable().optional(),
	passthrough: passthroughPolicySchema.nullable().optional(),
});

// Cache config schema
const baseCacheConfigSchema = z.object({
	ttl: z.number().int().min(1).default(3600),
	threshold: z.number().min(0).max(1).default(0.8),
	conversation_history_threshold: z.number().int().min(0).optional(),
	exclude_system_prompt: z.boolean().optional(),
	cache_by_model: z.boolean().default(false),
	cache_by_provider: z.boolean().default(false),
	disable_identity_scoping: z.boolean().optional(),
	vector_store_namespace: z.string().min(1).optional(),
	default_cache_key: z.string().min(1).optional(),
	created_at: z.string().optional(),
	updated_at: z.string().optional(),
});

const directCacheConfigSchema = baseCacheConfigSchema
	.extend({
		dimension: z.literal(1),
		keys: z.array(modelProviderKeySchema).optional(),
	})
	.strict();

const providerBackedCacheConfigSchema = baseCacheConfigSchema
	.extend({
		provider: modelProviderNameSchema,
		keys: z.array(modelProviderKeySchema).optional(),
		embedding_model: z.string().min(1, msg("validation.embeddingModelIsRequired")),
		dimension: z.number().int().min(2, msg("validation.dimensionMustBeGreaterThan1ForProviderBackedSemantic")),
	})
	.strict();

export const cacheConfigSchema = z.union([directCacheConfigSchema, providerBackedCacheConfigSchema]);

// Core config schema
export const coreConfigSchema = z.object({
	drop_excess_requests: z.boolean().default(false),
	initial_pool_size: z.number().min(1).default(10),
	prometheus_labels: z.array(z.string()).default([]),
	enable_logging: z.boolean().default(true),
	disable_content_logging: z.boolean().default(false),
	enforce_auth_on_inference: z.boolean().default(true),
	hide_deleted_virtual_keys_in_filters: z.boolean().default(false),
	delete_expired_virtual_keys: z.boolean().default(false),
	enforce_vk_object_ownership: z.boolean().default(true),
	skip_access_profile_user_virtual_key_creation: z.boolean().default(false),
	skip_access_profile_user_virtual_key_deletion: z.boolean().default(false),
	extra_fields_placement: z.enum(["body", "headers"]).default("body"),
	hidden_request_types: z.array(z.string()).default([]),
	allowed_origins: z.array(z.string()).default(["*"]),
	max_request_body_size_mb: z.number().min(1).default(100),
	mcp_agent_depth: z.number().min(1).default(10),
	mcp_tool_execution_timeout: z.number().min(1).default(30),
	mcp_code_mode_binding_level: z.enum(["server", "tool"]).default("server"),
	mcp_disable_auto_tool_inject: z.boolean().default(false),
	mcp_max_instructions_per_client: z.number().int().min(0).default(0),
	mcp_max_instructions_total: z.number().int().min(0).default(0),
	mcp_code_mode_limits: z
		.object({
			max_source_bytes: z.number().int().min(0).optional(),
			max_steps: z.number().int().min(0).optional(),
			max_memory_bytes: z.number().int().min(0).optional(),
			max_log_bytes: z.number().int().min(0).optional(),
			max_tool_calls: z.number().int().min(0).optional(),
			max_value_bytes: z
				.number()
				.int()
				.refine((v) => v === 0 || v >= 1024, { error: () => i18n.t("validation.maxValueBytesMustBe0OrAtLeast1024") })
				.optional(),
			max_nesting_depth: z.number().int().min(0).max(1000).optional(),
		})
		.optional(),
	mcp_enable_temp_token_auth: z.boolean().default(false),
	mcp_oauth_private_allowlist: z.array(z.string()).optional(),
	a2a_private_allowlist: z.array(z.string()).optional(),
	skills_external_client_url: secretVarSchema.optional(),
});

// Bifrost config schema
export const bifrostConfigSchema = z.object({
	client_config: coreConfigSchema,
	is_db_connected: z.boolean(),
	is_cache_connected: z.boolean(),
	is_logs_connected: z.boolean(),
	is_git_available: z.boolean().optional().default(false),
	skills_marketplace_base_url: z.string().optional(),
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

// Performance form schema for the PerformanceFormFragment (concurrency/buffer only; raw request/response are in Debugging tab)
export const performanceFormSchema = z.object({
	concurrency_and_buffer_size: z
		.object({
			concurrency: z
				.number({ error: () => i18n.t("validation.concurrencyMustBeANumber") })
				.min(1, msg("validation.concurrencyMustBeGreaterThan0"))
				.max(100000, msg("validation.concurrencyMustBeLessThan100000")),
			buffer_size: z
				.number({ error: () => i18n.t("validation.bufferSizeMustBeANumber") })
				.min(1, msg("validation.bufferSizeMustBeGreaterThan0"))
				.max(100000, msg("validation.bufferSizeMustBeLessThan100000")),
		})
		.refine((data) => data.concurrency <= data.buffer_size, {
			error: () => i18n.t("validation.concurrencyMustBeLessThanOrEqualToBufferSize"),
			path: ["concurrency"],
		}),
});

// Debugging tab (raw request/response toggles)
export const debuggingFormSchema = z.object({
	send_back_raw_request: z.boolean(),
	send_back_raw_response: z.boolean(),
	store_raw_request_response: z.boolean(),
});

export type DebuggingFormSchema = z.infer<typeof debuggingFormSchema>;

// Pricing tab (provider-reported cost handling)
export const pricingFormSchema = z.object({
	ignore_provider_cost: z.boolean(),
});

export type PricingFormSchema = z.infer<typeof pricingFormSchema>;

// Beta Headers tab
export const betaHeadersFormSchema = z.object({
	beta_header_overrides: z.record(z.string(), z.boolean()).optional(),
});

export type BetaHeadersFormSchema = z.infer<typeof betaHeadersFormSchema>;

// OTEL Configuration Schema
export const otelConfigSchema = z
	.object({
		// Per-profile enable toggle. A disabled profile exports nothing and is not validated.
		enabled: z.boolean().default(true),
		// Trace export toggle. When false the profile is metrics-only; collector_url isn't required.
		traces_enabled: z.boolean().default(true),
		service_name: z.string().optional(),
		collector_url: secretVarSchema.default({ value: "" }),
		trace_type: z
			.enum(["genai_extension", "vercel", "open_inference"], {
				error: () => i18n.t("validation.pleaseSelectATraceType"),
			})
			.default("genai_extension"),
		// Common headers go to both endpoints; per-signal headers override on collision.
		headers: z.record(z.string(), secretVarSchema).optional(),
		trace_headers: z.record(z.string(), secretVarSchema).optional(),
		metrics_headers: z.record(z.string(), secretVarSchema).optional(),
		protocol: z
			.enum(["http", "grpc"], {
				error: () => i18n.t("validation.pleaseSelectAProtocol"),
			})
			.default("http"),
		// TLS configuration
		tls_ca_cert: z.string().optional(),
		insecure: z.boolean().default(true),
		// Bounds a single trace export. gRPC exports have no other timeout, so an
		// endpoint that accepts the connection but never replies would otherwise block
		// an export goroutine indefinitely.
		export_timeout: z.number().int().min(1).max(60).default(5),
		// Metrics push configuration
		metrics_enabled: z.boolean().default(false),
		// Export per-component Bifrost overhead latency as a histogram.
		overhead_breakdown_enabled: z.boolean().default(false),
		metrics_endpoint: secretVarSchema.optional(),
		metrics_push_interval: z.number().int().min(1).max(300).default(15),
		request_headers: z.array(z.string()).default([]),
		disable_content_logging: z.boolean().default(false),
		export_raw_payloads: z.boolean().default(false),
		group_traces_by_session: z.boolean().default(false),
		disable_root_span_content: z.boolean().default(false),
	})
	.superRefine((data, ctx) => {
		// A disabled profile is not sent anywhere, so skip all validation for it.
		if (data.enabled === false) return;

		const protocol = data.protocol;
		const hostPortRegex = /^(?!https?:\/\/)([a-zA-Z0-9.-]+|\[[0-9a-fA-F:]+\]|\d{1,3}(?:\.\d{1,3}){3}):(\d{1,5})$/;

		// Helper to validate URL format
		const validateHttpUrl = (url: string, path: string[]) => {
			try {
				const u = new URL(url);
				if (!(u.protocol === "http:" || u.protocol === "https:")) {
					ctx.addIssue({
						code: "custom",
						path,
						message: i18n.t("validation.mustBeAValidHttpOrHttpsUrl"),
					});
					return false;
				}
				return true;
			} catch {
				ctx.addIssue({
					code: "custom",
					path,
					message: i18n.t("validation.mustBeAValidHttpOrHttpsUrl"),
				});
				return false;
			}
		};

		// Helper to validate host:port format
		const validateHostPort = (value: string, path: string[], example: string) => {
			const match = value.match(hostPortRegex);
			if (!match) {
				ctx.addIssue({
					code: "custom",
					path,
					message: i18n.t("validation.grpcHostPortFormat", { example }),
				});
				return false;
			}
			const port = Number(match[2]);
			if (!(port >= 1 && port <= 65535)) {
				ctx.addIssue({
					code: "custom",
					path,
					message: i18n.t("validation.portMustBeBetween1And65535"),
				});
				return false;
			}
			return true;
		};

		// collector_url is required and validated only when traces are enabled.
		if (data.traces_enabled) {
			if (!isSecretVarSet(data.collector_url)) {
				ctx.addIssue({
					code: "custom",
					path: ["collector_url"],
					message: i18n.t("validation.collectorAddressIsRequired"),
				});
			}

			// Validate collector_url format — skip format check for env var references
			const collectorUrl = (data.collector_url?.value || "").trim();
			if (collectorUrl && (data.collector_url?.type === "plain_text" || !data.collector_url?.type) && protocol === "http") {
				validateHttpUrl(collectorUrl, ["collector_url"]);
			} else if (collectorUrl && (data.collector_url?.type === "plain_text" || !data.collector_url?.type) && protocol === "grpc") {
				validateHostPort(collectorUrl, ["collector_url"], "otel-collector:4317");
			}
		}

		// Validate metrics_endpoint when metrics_enabled is true
		if (data.metrics_enabled) {
			const metricsEndpoint = (data.metrics_endpoint?.value || "").trim();
			if (!isSecretVarSet(data.metrics_endpoint)) {
				ctx.addIssue({
					code: "custom",
					path: ["metrics_endpoint"],
					message: i18n.t("validation.metricsEndpointIsRequiredWhenMetricsPushIsEnabled"),
				});
			} else if (metricsEndpoint && (data.metrics_endpoint?.type === "plain_text" || !data.metrics_endpoint?.type) && protocol === "http") {
				validateHttpUrl(metricsEndpoint, ["metrics_endpoint"]);
			} else if (metricsEndpoint && (data.metrics_endpoint?.type === "plain_text" || !data.metrics_endpoint?.type) && protocol === "grpc") {
				validateHostPort(metricsEndpoint, ["metrics_endpoint"], "otel-collector:4317");
			}
		}
	});

// OTEL form schema for the OtelFormFragment. The plugin itself is gated by `enabled`;
// it carries one or more export profiles, each independently enable-able.
export const otelFormSchema = z.object({
	enabled: z.boolean().default(true),
	profiles: z.array(otelConfigSchema).min(1, msg("validation.atLeastOneProfileIsRequired")),
});

// Maxim Configuration Schema
export const maximConfigSchema = z.object({
	api_key: z.string().default(""),
	log_repo_id: z.string().optional(),
	request_headers: z.array(z.string()).default([]),
});

// Maxim form schema for the MaximFormFragment
export const maximFormSchema = z
	.object({
		enabled: z.boolean().default(true),
		maxim_config: maximConfigSchema,
	})
	.superRefine((data, ctx) => {
		if (data.enabled) {
			const apiKey = (data.maxim_config.api_key || "").trim();
			if (!apiKey) {
				ctx.addIssue({
					code: "custom",
					path: ["maxim_config", "api_key"],
					message: i18n.t("validation.apiKeyIsRequired2"),
				});
			} else if (!apiKey.startsWith("sk_mx_")) {
				ctx.addIssue({
					code: "custom",
					path: ["maxim_config", "api_key"],
					message: i18n.t("validation.apiKeyMustStartWithSkMx"),
				});
			}
		}
	});

// Prometheus Push Gateway Configuration Schema
export const prometheusConfigSchema = z
	.object({
		push_gateway_url: secretVarSchema.optional(),
		job_name: z.string().default("bifrost"),
		instance_id: z.string().optional(),
		push_interval: z.number().min(1).max(300).default(15),
		basic_auth_username: secretVarSchema.optional(),
		basic_auth_password: secretVarSchema.optional(),
	})
	.superRefine((data, ctx) => {
		// Validate push_gateway_url format — skip for env var references
		const url = (data.push_gateway_url?.value || "").trim();
		if (url && (data.push_gateway_url?.type === "plain_text" || !data.push_gateway_url?.type)) {
			try {
				const u = new URL(url);
				if (!(u.protocol === "http:" || u.protocol === "https:")) {
					ctx.addIssue({
						code: "custom",
						path: ["push_gateway_url"],
						message: i18n.t("validation.mustBeAValidHttpOrHttpsUrl"),
					});
				}
			} catch {
				ctx.addIssue({
					code: "custom",
					path: ["push_gateway_url"],
					message: i18n.t("validation.mustBeAValidUrlEGHttpPushgateway9091"),
				});
			}
		}

		// Validate basic auth: if one credential is provided, both must be provided
		const hasUsername = isSecretVarSet(data.basic_auth_username);
		const hasPassword = isSecretVarSet(data.basic_auth_password);
		if (hasUsername && !hasPassword) {
			ctx.addIssue({
				code: "custom",
				path: ["basic_auth_password"],
				message: i18n.t("validation.passwordIsRequiredWhenUsernameIsProvided"),
			});
		}
		if (hasPassword && !hasUsername) {
			ctx.addIssue({
				code: "custom",
				path: ["basic_auth_username"],
				message: i18n.t("validation.usernameIsRequiredWhenPasswordIsProvided"),
			});
		}
	});

// Prometheus form schema for the PrometheusFormFragment.
export const prometheusFormSchema = z
	.object({
		metrics_enabled: z.boolean().default(true),
		overhead_breakdown_enabled: z.boolean().default(false),
		user_labels_enabled: z.boolean().default(false),
		push_gateway_enabled: z.boolean().default(false),
		prometheus_config: prometheusConfigSchema,
	})
	.superRefine((data, ctx) => {
		if (data.push_gateway_enabled) {
			const urlIsSet = isSecretVarSet(data.prometheus_config.push_gateway_url);
			if (!urlIsSet) {
				ctx.addIssue({
					code: "custom",
					path: ["prometheus_config", "push_gateway_url"],
					message: i18n.t("validation.pushGatewayUrlIsRequiredWhenThePushGatewayIs"),
				});
			}
		}
	});

const httpUrlOrEmpty = (message: ReturnType<typeof msg>) =>
	z
		.string()
		.optional()
		.refine((v) => !v?.trim() || isHttpUrl(v.trim()), message);

function isHttpUrl(value: string): boolean {
	try {
		const url = new URL(value);
		return (url.protocol === "http:" || url.protocol === "https:") && url.host !== "";
	} catch {
		return false;
	}
}

// Credential for one OpenAPI securitySchemes entry (apiKey/bearer: value; basic: username + password).
export const mcpOpenAPICredentialSchema = z.object({
	value: _secretVarBase.optional(),
	username: _secretVarBase.optional(),
	password: _secretVarBase.optional(),
});

// openapi_config as sent on create/update. Exactly one spec source (inline text or URL).
// The shape of openapi_config with no source requirement: an update is merged
// onto the stored block, so it may carry only the fields being changed.
export const mcpOpenAPIConfigBaseSchema = z.object({
	spec: z.string().optional(),
	spec_url: httpUrlOrEmpty(msg("validation.specUrlMustBeAValidHttpOrHttpsUrl")),
	base_url: httpUrlOrEmpty(msg("validation.baseUrlMustBeAValidHttpOrHttpsUrl")),
	security_credentials: z.record(z.string(), mcpOpenAPICredentialSchema).optional(),
	include_deprecated: z.boolean().optional(),
	max_response_bytes: z.number().int().min(0).optional(),
});

// openapi_config on create: a spec or spec URL is required.
export const mcpOpenAPIConfigSchema = mcpOpenAPIConfigBaseSchema.superRefine((v, ctx) => {
	const hasSpec = !!v.spec?.trim();
	const hasUrl = !!v.spec_url?.trim();
	if (!hasSpec && !hasUrl) {
		ctx.addIssue({ code: "custom", path: ["spec"], message: i18n.t("validation.pasteOrUploadASpecOrProvideASpecUrl") });
	}
});
export type MCPOpenAPIConfigSchema = z.infer<typeof mcpOpenAPIConfigSchema>;

// MCP Client update schema
export const mcpClientUpdateSchema = z
	.object({
		is_code_mode_client: z.boolean().optional(),
		is_ping_available: z.boolean().optional(),
		needs_session_stickiness: z.boolean().optional(),
		allow_by_default: z.boolean().optional(),
		disabled: z.boolean().optional(),
		// openapi clients only; undefined = leave the stored document alone.
		openapi_config: mcpOpenAPIConfigBaseSchema.optional(),
		name: z
			.string()
			.min(1, msg("validation.nameIsRequired"))
			.refine((val) => !val.includes("-"), {
				error: () => i18n.t("validation.clientNameCannotContainHyphens"),
			})
			.refine((val) => !val.includes(" "), {
				error: () => i18n.t("validation.clientNameCannotContainSpaces"),
			})
			.refine((val) => !/^[0-9]/.test(val), {
				error: () => i18n.t("validation.clientNameCannotStartWithANumber"),
			}),
		headers: z.record(z.string(), secretVarSchema).optional().nullable(),
		per_user_header_keys: z
			.array(z.string().trim().min(1, msg("validation.headerNameCannotBeEmpty")))
			.optional()
			.refine(
				(headers) => {
					if (!headers) return true;
					const normalized = headers.map((h) => h.trim().toLowerCase());
					return normalized.length === new Set(normalized).size;
				},
				{ error: () => i18n.t("validation.duplicateHeaderNamesAreNotAllowed") },
			),
		tools_to_execute: z
			.array(z.string())
			.optional()
			.refine(
				(tools) => {
					if (!tools || tools.length === 0) return true;
					const hasWildcard = tools.includes("*");
					return !hasWildcard || tools.length === 1;
				},
				{ error: () => i18n.t("validation.wildcardCannotBeCombinedWithOtherToolNames") },
			)
			.refine(
				(tools) => {
					if (!tools) return true;
					return tools.length === new Set(tools).size;
				},
				{ error: () => i18n.t("validation.duplicateToolNamesAreNotAllowed") },
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
				{ error: () => i18n.t("validation.wildcardCannotBeCombinedWithOtherToolNames") },
			)
			.refine(
				(tools) => {
					if (!tools) return true;
					return tools.length === new Set(tools).size;
				},
				{ error: () => i18n.t("validation.duplicateToolNamesAreNotAllowed") },
			),
		prompts_to_expose: z.array(z.string()).optional(), // ["*"] = all, [] = none
		resources_to_expose: z.array(z.string()).optional(), // ["*"] = all, [] = none
		tool_pricing: z.record(z.string(), z.number().min(0, msg("validation.costMustBeNonNegative"))).optional(),
		tool_sync_interval: z.number().min(0, msg("validation.toolSyncIntervalMustBe0OrAPositiveNumber")).optional(), // 0 = use global, >0 = custom interval in minutes
		max_protocol_version: z.union([z.literal(""), z.enum(MCP_MAX_PROTOCOL_VERSIONS)]).optional(), // "" = use global
		tool_execution_timeout: z.number().int().min(0).optional(), // 0 = use global, >0 = per-server timeout in seconds
		max_instructions_length: z.number().int().min(0).optional(), // 0 = use global, >0 = per-server instruction byte cap
		allowed_extra_headers: z
			.array(z.string())
			.optional()
			.refine(
				(headers) => {
					if (!headers || headers.length === 0) return true;
					const hasWildcard = headers.includes("*");
					return !hasWildcard || headers.length === 1;
				},
				{ error: () => i18n.t("validation.wildcardCannotBeCombinedWithSpecificHeaderNames") },
			),
		oauth_config: z
			.object({
				client_id: secretVarSchema.optional(),
				client_secret: secretVarSchema.optional(),
				authorize_url: z
					.string()
					.optional()
					.refine((val) => !val || /^https?:\/\/.+$/.test(val), { error: () => i18n.t("validation.authorizeUrlMustStartWithHttpOrHttps") }),
				token_url: z
					.string()
					.optional()
					.refine((val) => !val || /^https?:\/\/.+$/.test(val), { error: () => i18n.t("validation.tokenUrlMustStartWithHttpOrHttps") }),
				registration_url: z
					.string()
					.optional()
					.refine((val) => !val || /^https?:\/\/.+$/.test(val), {
						error: () => i18n.t("validation.registrationUrlMustStartWithHttpOrHttps"),
					}),
				scopes: z.array(z.string()).optional(),
				resource: z.string().optional(),
				use_client_metadata_document: z.boolean().optional(),
			})
			.optional(),
		token_exchange: z
			.object({
				audience: z.string().trim().min(1, msg("validation.audienceIsRequired")),
				use_idp_credentials: z.boolean().optional(),
				client_id: secretVarSchema.optional(),
				client_secret: secretVarSchema.optional(),
				authorization_server_url: z
					.string()
					.optional()
					.refine((val) => !val || /^https?:\/\/.+$/.test(val), {
						error: () => i18n.t("validation.authorizationServerUrlMustStartWithHttpOrHttps"),
					}),
				scopes: z.array(z.string()).optional(),
				profile: z.enum(["", "id_jag"]).optional(),
				resource: z.string().optional(),
				resource_token_endpoint: z.string().optional(),
				resource_client_id: secretVarSchema.optional(),
				resource_client_secret: secretVarSchema.optional(),
				resource_client_auth_method: z.enum(["", "client_secret_basic", "client_secret_post"]).optional(),
				resource_private_key: secretVarSchema.optional(),
				resource_key_id: z.string().optional(),
				resource_signing_algorithm: z.string().optional(),
				resource_auth_mode: z.enum(["secret", "private_key"]).optional(),
				resource_assertion_audience: z.string().optional(),
			})
			// The id_jag fields are hidden for the direct profile, so they are checked only
			// with it selected: a value left behind must not block Save with no visible error.
			.superRefine((te, ctx) => {
				if (te.profile !== "id_jag") return;
				const issue = (path: string, message: string) => ctx.addIssue({ code: "custom", path: [path], message });
				if (!isSecretVarSet(te.resource_client_id)) {
					issue("resource_client_id", "Client ID at the upstream server is required for Cross App Access");
				}
				if (te.resource_token_endpoint && !isHTTPSOrLoopbackURL(te.resource_token_endpoint)) {
					issue("resource_token_endpoint", "Token endpoint must be an https URL (http is allowed only for localhost)");
				}
				const audience = te.resource_assertion_audience?.trim();
				if (audience && !isHTTPSOrLoopbackURL(audience)) {
					issue("resource_assertion_audience", "Assertion audience must be an https URL (http is allowed only for localhost)");
				}
				if (isSecretVarSet(te.resource_client_secret) && isSecretVarSet(te.resource_private_key)) {
					issue("resource_private_key", "Use either a client secret or a private key at the upstream server, not both");
				}
				if (hasPrivateKeySettingsWithoutKey(te)) {
					issue("resource_private_key", PRIVATE_KEY_REQUIRED_MESSAGE);
				}
				if (hasSendMethodWithoutSecret(te)) {
					issue("resource_client_secret", SECRET_REQUIRED_FOR_METHOD_MESSAGE);
				}
			})
			.optional(),
		tls_config: z
			.object({
				insecure_skip_verify: z.boolean().optional(),
				ca_cert_pem: secretVarSchema.optional(),
			})
			.optional(),
	})
	.superRefine((data, ctx) => {
		// per_user_header_keys is only ever set on the form for per_user_headers
		// auth clients (undefined otherwise), so an empty array here means the
		// admin cleared every entry, not that the field doesn't apply.
		if (data.per_user_header_keys !== undefined && data.per_user_header_keys.length === 0) {
			ctx.addIssue({
				code: "custom",
				path: ["per_user_header_keys"],
				message: i18n.t("validation.declareAtLeastOneHeaderNameUsersMustSupply"),
			});
		}
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
			error: () => i18n.t("validation.proxyUrlIsRequiredWhenProxyIsEnabled"),
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
			error: () => i18n.t("validation.mustBeAValidUrlEGHttpProxyExample"),
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

// Routing rule creation schema
export const routingRuleSchema = z
	.object({
		name: z.string().min(1, msg("validation.ruleNameIsRequired")).max(255, msg("validation.ruleNameMustBeLessThan255Characters")),
		description: z.string().max(1000, msg("validation.descriptionMustBeLessThan1000Characters")).optional(),
		cel_expression: z.string().optional(),
		provider: z.string().min(1, msg("validation.providerIsRequired")),
		model: z.string().optional(),
		fallbacks: z.array(z.string()).optional().default([]),
		scope: z.enum(["global", "team", "customer", "virtual_key"]),
		scope_id: z.string().optional(),
		priority: z.number().min(0, msg("validation.priorityMustBe0OrGreater")).max(1000, msg("validation.priorityMustBe1000OrLess")),
		enabled: z.boolean().default(true),
		chain_rule: z.boolean().default(false),
	})
	.refine((data) => data.scope === "global" || (data.scope_id != null && data.scope_id.trim() !== ""), {
		error: () => i18n.t("validation.scopeIdIsRequiredWhenScopeIsNotGlobal"),
		path: ["scope_id"],
	});

// Budget override form schema (BudgetOverrideDialog)
export const budgetOverrideFormSchema = z
	.object({
		amount: z.number(msg("validation.additionalBudgetMustBeGreaterThan0")).positive(msg("validation.additionalBudgetMustBeGreaterThan0")),
		mode: z.enum(["cycles", "forever"]),
		cycles: z.number().optional(),
	})
	.refine((data) => data.mode !== "cycles" || (data.cycles !== undefined && Number.isSafeInteger(data.cycles) && data.cycles > 0), {
		error: () => i18n.t("validation.resetCyclesMustBeAPositiveWholeNumber"),
		path: ["cycles"],
	});

// Proof of control for an auth_config change made while dashboard auth is disabled but an
// admin account exists (SecurityView). PUT /api/config refuses such a change with 403 unless
// it carries the stored admin password or the operator's setup token, so one of the two must
// be filled in; the issue lands on current_password because that is the field shown first.
// current_password is sent exactly as typed: the server compares it against the stored hash
// byte for byte and the password policy allows spaces. The setup token is trimmed, matching
// how the server reads the configured one.
export const authProofOfControlSchema = z
	.object({
		current_password: z.string(),
		setup_token: z.string().trim(),
	})
	.superRefine((data, ctx) => {
		if (!data.current_password.trim() && !data.setup_token) {
			ctx.addIssue({
				code: "custom",
				path: ["current_password"],
				message: i18n.t("security.proofRequired", { ns: "config" }),
			});
		}
	});

// Export type inference helpers
export type SecretVar = z.infer<typeof secretVarSchema>;
export type AuthProofOfControl = z.infer<typeof authProofOfControlSchema>;
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
export type PrometheusConfigSchema = z.infer<typeof prometheusConfigSchema>;
export type PrometheusFormSchema = z.infer<typeof prometheusFormSchema>;
export type NetworkOnlyFormSchema = z.infer<typeof networkOnlyFormSchema>;
export type PerformanceFormSchema = z.infer<typeof performanceFormSchema>;
export type CustomProviderConfigSchema = z.infer<typeof customProviderConfigSchema>;
export type GlobalProxyConfigSchema = z.infer<typeof globalProxyConfigSchema>;
export type GlobalProxyFormSchema = z.infer<typeof globalProxyFormSchema>;
export type GlobalHeaderFilterConfigSchema = z.infer<typeof globalHeaderFilterConfigSchema>;
export type GlobalHeaderFilterFormSchema = z.infer<typeof globalHeaderFilterFormSchema>;
export type RoutingRuleSchema = z.infer<typeof routingRuleSchema>;
export type BudgetOverrideFormSchema = z.infer<typeof budgetOverrideFormSchema>;
// OSS setup lock: the operator's setup token entered on the login setup view.
export const setupTokenFormSchema = z.object({
	setup_token: z.string().trim().min(1, msg("validation.enterTheSetupTokenConfiguredForThisBifrostInstance")),
});

export type SetupTokenFormSchema = z.infer<typeof setupTokenFormSchema>;
// RSS feed added from the notification tray's Sources sheet. The server also
// fetches the URL once and rejects anything that is not an RSS or Atom feed.
export const rssFeedFormSchema = z.object({
	name: z.string().trim().min(1, msg("validation.giveTheFeedAName")).max(120, msg("validation.nameMustBe120CharactersOrFewer")),
	url: z
		.string()
		.trim()
		.min(1, msg("validation.enterTheFeedUrl"))
		.max(2048, msg("validation.urlIsTooLong"))
		.refine((value) => /^https?:\/\/[^\s/]+/i.test(value), msg("validation.urlMustStartWithHttpOrHttps")),
	// Optional logo: an image URL typed in, or the data: URI of an uploaded file.
	icon_url: z
		.string()
		.trim()
		.refine(
			(value) => value === "" || value.startsWith("data:image/") || /^https?:\/\/[^\s/]+/i.test(value),
			msg("validation.logoMustBeAnHttpSImageUrlOrAn"),
		),
});

export type RSSFeedFormSchema = z.infer<typeof rssFeedFormSchema>;

// ---------------------------------------------------------------------------
// Relays (/workspace/relays). Mirrors
// core/schemas/relay.go; the backend validates the same rules.
// ---------------------------------------------------------------------------

const endpointGjsonPath = z.string().trim().optional();

// A usage mapping: where usage is read (gjson paths) and how a call is priced. The endpoint-level
// mapping must name a path for a positive rate; a route's override may inherit it, which the backend
// checks, so routes use the base object.
export const relayUsageBaseSchema = z.object({
	model_path: endpointGjsonPath,
	model_source: z.enum(["body", "query"]).nullish(),
	input_tokens_path: endpointGjsonPath,
	output_tokens_path: endpointGjsonPath,
	total_tokens_path: endpointGjsonPath,
	cost_path: endpointGjsonPath,
	// Cleared fields hold null, not undefined: react-hook-form shows a field's loaded value again when
	// it is set to undefined, so an emptied price would snap back. null and "" are dropped on save.
	cost_per_request: z.number().min(0, "Cost per request must not be negative").nullish(),
	units_path: endpointGjsonPath,
	units_source: z.enum(["response", "request"]).nullish(),
	units_measure: z.enum(["value", "chars", "body_bytes", "body_chars"]).nullish(),
	cost_per_unit: z.number().min(0, "Cost per unit must not be negative").nullish(),
	units_block_size: z.number().positive("Block size must be above zero").nullish(),
	units_minimum: z.number().min(0, "Minimum units must not be negative").nullish(),
});

/** A route is billable by default only for POST, so the flag is stored only when it differs. */
export function routeBillableByDefault(method: string): boolean {
	return method.toUpperCase() === "POST";
}

export type RelayUsageForm = z.infer<typeof relayUsageBaseSchema>;

type UsageRule = {
	units_path?: string | null;
	units_measure?: string | null;
	cost_per_unit?: number | null;
};

/**
 * Mirrors RelayUsageConfig.validate in core/schemas/relay.go: a positive per-unit rate needs a units
 * path unless the measure reads the whole body. On a route, the rate, path and measure it leaves
 * empty come from the relay (base), so the check runs on the merged values as the backend's does.
 */
export function relayUsageUnitsPathError(usage: UsageRule | undefined, base?: UsageRule): string | undefined {
	const pick = <K extends keyof UsageRule>(k: K) => {
		const own = usage?.[k];
		return own === undefined || own === null || own === "" ? base?.[k] : own;
	};
	const rate = pick("cost_per_unit");
	const measure = pick("units_measure");
	const path = (pick("units_path") ?? "").toString().trim();
	if (typeof rate === "number" && rate > 0 && measure !== "body_bytes" && measure !== "body_chars" && !path) {
		return base
			? "Units path is required: this route charges per unit and neither it nor the relay names a path"
			: "Units path is required unless the measure reads the whole body";
	}
	return undefined;
}

export const relayUsageSchema = relayUsageBaseSchema.superRefine((usage, ctx) => {
	const message = relayUsageUnitsPathError(usage);
	if (message) ctx.addIssue({ code: "custom", message, path: ["units_path"] });
});

// The relay route import's URL source. The gateway fetches it, so only an absolute http(s) URL is sent;
// anything else is refused on the field rather than as a fetch error.
export const relayOpenAPISpecUrlSchema = z.string().trim().refine(isHttpUrl, "Enter an absolute http(s) URL");

// Mirrors the gateway's rule for a route or streaming path: rooted, and ** only as the last segment.
const relayGlob = z
	.string()
	.trim()
	.min(1, "Path is required")
	.refine((p) => p.startsWith("/"), "Path must start with /")
	.refine((p) => !p.slice(1).split("/").slice(0, -1).includes("**"), "** is only allowed as the last segment");

export const relayAuthSchema = z
	.object({
		type: z.enum(["bearer", "header", "query", "basic", "aws_sigv4", "none"]),
		header_name: z.string().trim().optional(),
		query_param: z.string().trim().optional(),
		value_prefix: z.string().optional(),
		aws: z.object({ service: z.string().trim().optional() }).optional(),
	})
	.superRefine((auth, ctx) => {
		if (auth.type === "header" && !auth.header_name) {
			ctx.addIssue({ code: "custom", message: "Header name is required for header auth", path: ["header_name"] });
		}
		if (auth.type === "query" && !auth.query_param) {
			ctx.addIssue({ code: "custom", message: "Query parameter is required for query auth", path: ["query_param"] });
		}
		if (auth.type === "aws_sigv4") {
			const service = auth.aws?.service ?? "";
			if (!service) ctx.addIssue({ code: "custom", message: "AWS service is required for SigV4 auth", path: ["aws", "service"] });
			else if (!/^[a-z0-9-]+$/.test(service))
				ctx.addIssue({ code: "custom", message: "AWS service must match [a-z0-9-]+", path: ["aws", "service"] });
		}
	});

// A route's header matchers are edited as name/value pairs; a fully blank row is dropped on save,
// a half-filled one is an error so a strict route is never silently widened.
export const relayHeaderPairSchema = z
	.object({
		name: z
			.string()
			.trim()
			.refine((n) => !/[\s:/]/.test(n), "Header name must not contain spaces, colons or slashes"),
		value: z.string().trim(),
	})
	.superRefine((pair, ctx) => {
		if (pair.name && !pair.value) ctx.addIssue({ code: "custom", message: "A header matcher needs a value glob", path: ["value"] });
		if (!pair.name && pair.value) ctx.addIssue({ code: "custom", message: "A header matcher needs a header name", path: ["name"] });
	});

// A route's async job block is set through config.json or the API; the form carries it untouched.
export const relayJobSchema = z.object({
	id_path: z.string().trim().min(1, "Enter where the start reply carries the job id"),
	poll: z.object({
		method: z.string().optional(),
		path: z
			.string()
			.trim()
			.min(1, "Enter the poll path")
			.refine((p) => p.startsWith("/"), "Enter a rooted path such as /jobs/{{job.id}}"),
		headers: z.record(z.string(), z.string()).optional(),
		body: z.string().optional(),
		content_type: z.string().optional(),
	}),
	status_path: z.string().trim().min(1, "Enter where the poll reply carries the status"),
	succeeded: z.array(z.string()).min(1, "Enter at least one succeeded status"),
	failed: z.array(z.string()).optional(),
	interval_seconds: z.number().int().min(1, "At least 1 second").optional(),
	timeout_seconds: z.number().int().min(1, "At least 1 second").optional(),
	usage: relayUsageBaseSchema.optional(),
});

export const relayRouteFormSchema = z.object({
	method: z.enum(["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD"]),
	path: relayGlob,
	headers: z.array(relayHeaderPairSchema).superRefine((pairs, ctx) => {
		const seen = new Set<string>();
		pairs.forEach((pair, i) => {
			const name = pair.name.trim().toLowerCase();
			if (!name) return;
			if (seen.has(name)) ctx.addIssue({ code: "custom", message: "This header is already matched on this route", path: [i, "name"] });
			seen.add(name);
		});
	}),
	enabled: z.boolean().optional(),
	billable: z.boolean().optional(),
	stream: z.boolean().optional(),
	usage: relayUsageBaseSchema.optional(),
	job: relayJobSchema.optional(),
});

// The endpoint editor form. Extra headers are edited as pairs; the credential is a SecretVar so env.
// and vault references round-trip, and a redacted value sent back keeps the stored secret.
export const relayNetworkFormSchema = z.object({
	timeout_seconds: z.number().int().min(0).optional(),
	stream_idle_timeout_in_seconds: z.number().int().min(0).optional(),
	max_conns_per_host: z.number().int().min(0).optional(),
	insecure_skip_verify: z.boolean().optional(),
	ca_cert_pem: z.string().optional(),
});

export const relayFormSchema = z
	.object({
		name: z
			.string()
			.trim()
			.regex(/^[A-Za-z0-9._-]{1,64}$/, "Use 1-64 letters, digits, dots, dashes or underscores")
			.refine((n) => n !== "." && n !== "..", "A relay cannot be named . or .."),
		description: z.string().optional(),
		base_url: z
			.string()
			.trim()
			.refine((u) => {
				try {
					const parsed = new URL(u);
					return (
						(parsed.protocol === "http:" || parsed.protocol === "https:") &&
						!parsed.search &&
						!parsed.hash &&
						!parsed.username &&
						!parsed.password
					);
				} catch {
					return false;
				}
			}, "Enter an absolute http(s) URL without a query, fragment or user info"),
		auth: relayAuthSchema,
		credential_value: _secretVarBase.optional(),
		aws: z
			.object({
				access_key: _secretVarBase.optional(),
				secret_key: _secretVarBase.optional(),
				session_token: _secretVarBase.optional(),
				region: _secretVarBase.optional(),
				role_arn: _secretVarBase.optional(),
				external_id: _secretVarBase.optional(),
				session_name: _secretVarBase.optional(),
			})
			.optional(),
		extra_headers: z.array(z.object({ name: z.string().trim(), value: _secretVarBase })).superRefine((pairs, ctx) => {
			// One value per header: a second row with the same name (any case) would replace the first.
			// A half-filled row is refused rather than silently dropped on save; a blank row is skipped.
			const seen = new Set<string>();
			pairs.forEach((pair, i) => {
				const name = pair.name.trim().toLowerCase();
				const hasValue = isSecretVarSet(pair.value);
				if (name && !hasValue) ctx.addIssue({ code: "custom", message: "Enter a value for this header", path: [i, "value"] });
				if (!name && hasValue) ctx.addIssue({ code: "custom", message: "Enter a header name", path: [i, "name"] });
				if (!name) return;
				if (seen.has(name)) ctx.addIssue({ code: "custom", message: "This header is already set", path: [i, "name"] });
				seen.add(name);
			});
		}),
		network: relayNetworkFormSchema,
		// "inherit" sends no proxy config (the global proxy applies); "none" is an explicit direct connection.
		proxy: z.object({
			type: z.enum(["inherit", "none", "http", "socks5", "environment"]),
			url: _secretVarBase.optional(),
			username: _secretVarBase.optional(),
			password: _secretVarBase.optional(),
			ca_cert_pem: _secretVarBase.optional(),
		}),
		usage: relayUsageSchema,
		streaming: z.object({
			// Validated as a whole so the message lands on streaming.paths, where the form shows it.
			// Blank lines are skipped: they are dropped on blur and on save.
			paths: z.array(z.string()).superRefine((paths, ctx) => {
				paths.forEach((p, i) => {
					if (!p.trim()) return;
					const parsed = relayGlob.safeParse(p);
					if (!parsed.success)
						ctx.addIssue({ code: "custom", message: `Line ${i + 1}: ${parsed.error.issues[0]?.message ?? "invalid path"}` });
				});
			}),
		}),
		routes: z.array(relayRouteFormSchema),
		skip_route_check: z.boolean(),
		ignore_upstream_cost: z.boolean(),
	})
	.superRefine((form, ctx) => {
		const type = form.auth.type;
		if (type === "aws_sigv4") {
			if (!isSecretVarSet(form.aws?.region))
				ctx.addIssue({ code: "custom", message: "Region is required for SigV4", path: ["aws", "region"] });
		} else if (type !== "none" && !isSecretVarSet(form.credential_value)) {
			ctx.addIssue({ code: "custom", message: "The credential is required for this auth type", path: ["credential_value"] });
		}
		if ((form.proxy.type === "http" || form.proxy.type === "socks5") && !isSecretVarSet(form.proxy.url)) {
			ctx.addIssue({ code: "custom", message: "Proxy URL is required", path: ["proxy", "url"] });
		}
		// The backend checks every route against the relay pricing it inherits, so a relay edit that
		// leaves a per-unit route without a units path is refused; say which route on the relay field.
		// A route that is not billed is never priced, and its hidden override is not saved.
		const broken = form.routes.find(
			(r) => (r.billable ?? routeBillableByDefault(r.method)) && r.usage && relayUsageUnitsPathError(r.usage, form.usage),
		);
		if (broken && !relayUsageUnitsPathError(form.usage)) {
			ctx.addIssue({
				code: "custom",
				message: `Units path is required: route ${broken.method} ${broken.path} charges per unit and inherits it from the relay`,
				path: ["usage", "units_path"],
			});
		}
	});

export type RelayFormValues = z.infer<typeof relayFormSchema>;

// The sample call the Test tab sends to the usage preview; the path mirrors the relay's own guard.
export const relayPreviewSampleSchema = z.object({
	method: z.enum(["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD"]),
	path: z
		.string()
		.trim()
		.min(1, "Path is required")
		.refine(
			(p) => p.startsWith("/") && !p.startsWith("//") && !p.includes("://") && !p.includes("\\"),
			"Enter a rooted path such as /v1/search",
		),
	status_code: z.number().int().min(100, "Status must be between 100 and 599").max(599, "Status must be between 100 and 599"),
});