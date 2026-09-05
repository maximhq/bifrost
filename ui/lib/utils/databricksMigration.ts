import {
	AddProviderRequest,
	AliasConfig,
	DatabricksKeyConfig,
	ModelProvider,
	ModelProviderKey,
	ModelProviderName,
	NetworkConfig,
	ProxyConfig,
	UpdateProviderRequest,
} from "@/lib/types/config";
import { DefaultNetworkConfig, DefaultPerformanceConfig } from "@/lib/constants/config";
import { SecretVar } from "@/lib/types/schemas";
import { toSecretVarFormValue } from "@/lib/utils/secretVarForm";
import { isRedacted } from "@/lib/utils/validation";

/**
 * Frontend-only migration of a custom Databricks provider (created before Bifrost shipped
 * first-party Databricks support) to the official `databricks` provider.
 *
 * This module is deliberately free of React and RTK so it can be unit tested. The dialog
 * adapts RTK mutations into a `MigrationApi` and hands it to `runDatabricksMigration`.
 */

export const DATABRICKS_PROVIDER: ModelProviderName = "databricks";

export type DatabricksApiFormat = NonNullable<DatabricksKeyConfig["api_format"]>;

const MODEL_SERVING_PATH = "/serving-endpoints";
const AI_GATEWAY_PATH = "/ai-gateway";
const MIGRATING_SUFFIX = "-migrating";

// ---------------------------------------------------------------------------
// Detection
// ---------------------------------------------------------------------------

const parseUrl = (raw: string | undefined): URL | undefined => {
	const trimmed = (raw ?? "").trim();
	if (!trimmed) return undefined;
	const withScheme = /^[a-z][a-z0-9+.-]*:\/\//i.test(trimmed) ? trimmed : `https://${trimmed}`;
	try {
		return new URL(withScheme);
	} catch {
		return undefined;
	}
};

/**
 * True for a custom provider named exactly "databricks" (case-insensitive). Only the exact name
 * qualifies: a provider such as "databricks-bc" that points at a Databricks workspace is left
 * alone, since it may be intentionally kept as a custom provider.
 */
export const isCustomDatabricksProvider = (provider: ModelProvider): boolean =>
	Boolean(provider.custom_provider_config) && provider.name.trim().toLowerCase() === DATABRICKS_PROVIDER;

// ---------------------------------------------------------------------------
// Derivation helpers
// ---------------------------------------------------------------------------

export interface DerivedWorkspace {
	workspaceUrl: string;
	apiFormat: DatabricksApiFormat;
	warnings: string[];
}

/**
 * Splits a custom provider base URL into the workspace origin the first-party provider wants
 * and the inference surface implied by its path.
 */
export const deriveDatabricksWorkspace = (baseUrl: string | undefined): DerivedWorkspace => {
	const warnings: string[] = [];
	const url = parseUrl(baseUrl);
	if (!url) {
		return { workspaceUrl: "", apiFormat: "auto", warnings };
	}
	if (url.protocol === "http:") {
		warnings.push(`Base URL used http://; the workspace URL will use https:// (${url.host}).`);
	}
	const workspaceUrl = `https://${url.host}`;
	const path = url.pathname.replace(/\/+$/, "");
	let apiFormat: DatabricksApiFormat = "auto";
	if (path === MODEL_SERVING_PATH) {
		apiFormat = "model_serving";
	} else if (path === AI_GATEWAY_PATH || path.startsWith(`${AI_GATEWAY_PATH}/`)) {
		apiFormat = "ai_gateway";
	} else if (path !== "") {
		warnings.push(`Unrecognized base URL path "${path}"; inference surface set to auto-detect.`);
	}
	return { workspaceUrl, apiFormat, warnings };
};

export type AuthHeaderProblem = "missing" | "not_bearer";

export interface ExtractedAuth {
	token?: SecretVar;
	problem?: AuthHeaderProblem;
}

const isSecretRef = (value: string): boolean => value.startsWith("env.") || value.startsWith("vault.");

/** Pulls the personal access token out of an `Authorization: Bearer <token>` extra header. */
export const extractAuthFromHeaders = (headers: Record<string, string> | undefined): ExtractedAuth => {
	const entry = Object.entries(headers ?? {}).find(([key]) => key.trim().toLowerCase() === "authorization");
	if (!entry) return { problem: "missing" };
	const raw = entry[1].trim();
	if (!raw) return { problem: "missing" };
	// A whole-header secret reference (env.X / vault.X) is assumed to resolve to the raw token.
	if (isSecretRef(raw)) return { token: toSecretVarFormValue(raw) };
	const match = raw.match(/^bearer\s+(.+)$/i);
	if (!match) return { problem: "not_bearer" };
	const token = match[1].trim();
	if (!token) return { problem: "not_bearer" };
	return { token: toSecretVarFormValue(token) };
};

/** Masks a literal secret for display; secret references are shown as-is. */
export const maskSecret = (secret: SecretVar | undefined): string => {
	if (!secret) return "";
	if (secret.ref?.trim()) return secret.ref.trim();
	const value = (secret.value ?? "").trim();
	if (!value) return "";
	if (isRedacted(value)) return value;
	if (value.length <= 8) return "****";
	return `${value.slice(0, 4)}****${value.slice(-4)}`;
};

export const isSecretVarSet = (secret: SecretVar | undefined): boolean => Boolean(secret?.value?.trim() || secret?.ref?.trim());

/** True when a secret came back from the API as a masked placeholder and cannot be re-sent. */
const isMaskedLiteral = (secret: SecretVar | undefined): boolean => {
	if (!secret) return false;
	if (secret.ref?.trim()) return false;
	const value = (secret.value ?? "").trim();
	return value !== "" && isRedacted(value);
};

const normalizeAliases = (aliases: ModelProviderKey["aliases"] | undefined): Record<string, AliasConfig> | undefined => {
	if (!aliases) return undefined;
	const entries = Object.entries(aliases as Record<string, AliasConfig | string>);
	if (entries.length === 0) return undefined;
	return Object.fromEntries(entries.map(([alias, cfg]) => [alias, typeof cfg === "string" ? { model_id: cfg } : cfg]));
};

// ---------------------------------------------------------------------------
// Plan
// ---------------------------------------------------------------------------

export interface PlannedKey {
	tempId: string;
	/** Name the key should end up with. */
	name: string;
	/** Name used when the key is created; may carry a temporary suffix while the source still exists. */
	createName: string;
	/** True when the key was synthesized from the Authorization header (keyless source). */
	fromHeader: boolean;
	value?: SecretVar;
	/** The token could not be read (masked or absent); the user must enter it. */
	needsValue: boolean;
	models: string[];
	blacklisted_models: string[];
	weight: number;
	enabled: boolean;
	aliases?: Record<string, AliasConfig>;
}

export interface MigrationPlan {
	source: {
		name: string;
		isKeyless: boolean;
		fromConfigFile: boolean;
	};
	/** The custom provider is literally named "databricks", so it must be deleted before the first-party one can be created. */
	nameClash: boolean;
	/** A first-party Databricks provider is already configured; keys are merged into it. */
	targetExists: boolean;
	workspaceUrl: string;
	apiFormat: DatabricksApiFormat;
	providerSettings: UpdateProviderRequest;
	keys: PlannedKey[];
	warnings: string[];
	/** Snapshot of the source, used to restore it if the name-clash path fails midway. */
	snapshot: { provider: ModelProvider; keys: ModelProviderKey[] };
}

export interface ExistingTarget {
	provider: ModelProvider;
	keys: ModelProviderKey[];
}

const stripAuthorizationHeader = (headers: Record<string, string> | undefined): Record<string, string> | undefined => {
	if (!headers) return undefined;
	const rest = Object.fromEntries(Object.entries(headers).filter(([key]) => key.trim().toLowerCase() !== "authorization"));
	return Object.keys(rest).length > 0 ? rest : undefined;
};

const portableSecret = (secret: SecretVar | undefined, label: string, warnings: string[]): SecretVar | undefined => {
	if (!secret || !isSecretVarSet(secret)) return undefined;
	if (isMaskedLiteral(secret)) {
		warnings.push(`${label} is stored as a literal secret and cannot be copied; re-enter it on the Databricks provider after migrating.`);
		return undefined;
	}
	return secret;
};

const buildProviderSettings = (source: ModelProvider, warnings: string[]): UpdateProviderRequest => {
	const net = source.network_config;
	const network_config: NetworkConfig = {
		default_request_timeout_in_seconds: net?.default_request_timeout_in_seconds ?? DefaultNetworkConfig.default_request_timeout_in_seconds,
		max_retries: net?.max_retries ?? DefaultNetworkConfig.max_retries,
		retry_backoff_initial: net?.retry_backoff_initial ?? DefaultNetworkConfig.retry_backoff_initial,
		retry_backoff_max: net?.retry_backoff_max ?? DefaultNetworkConfig.retry_backoff_max,
	};
	if (net) {
		const extra_headers = stripAuthorizationHeader(net.extra_headers);
		if (extra_headers) network_config.extra_headers = extra_headers;
		if (net.insecure_skip_verify !== undefined) network_config.insecure_skip_verify = net.insecure_skip_verify;
		if (net.stream_idle_timeout_in_seconds !== undefined) network_config.stream_idle_timeout_in_seconds = net.stream_idle_timeout_in_seconds;
		if (net.keep_alive_timeout_in_seconds !== undefined) network_config.keep_alive_timeout_in_seconds = net.keep_alive_timeout_in_seconds;
		if (net.max_conns_per_host !== undefined) network_config.max_conns_per_host = net.max_conns_per_host;
		if (net.enforce_http2 !== undefined) network_config.enforce_http2 = net.enforce_http2;
		if (net.http2_ping_interval_in_seconds !== undefined) network_config.http2_ping_interval_in_seconds = net.http2_ping_interval_in_seconds;
		if (net.beta_header_overrides !== undefined) network_config.beta_header_overrides = net.beta_header_overrides;
		if (net.allow_private_network !== undefined) network_config.allow_private_network = net.allow_private_network;
		const caCert = portableSecret(net.ca_cert_pem, "The provider CA certificate", warnings);
		if (caCert) network_config.ca_cert_pem = caCert;
	}

	let proxy_config: ProxyConfig | undefined;
	if (source.proxy_config && source.proxy_config.type && source.proxy_config.type !== "none") {
		proxy_config = { type: source.proxy_config.type };
		const url = portableSecret(source.proxy_config.url, "The proxy URL", warnings);
		if (url) proxy_config.url = url;
		const username = portableSecret(source.proxy_config.username, "The proxy username", warnings);
		if (username) proxy_config.username = username;
		const password = portableSecret(source.proxy_config.password, "The proxy password", warnings);
		if (password) proxy_config.password = password;
		const caCert = portableSecret(source.proxy_config.ca_cert_pem, "The proxy CA certificate", warnings);
		if (caCert) proxy_config.ca_cert_pem = caCert;
	}

	const settings: UpdateProviderRequest = {
		network_config,
		concurrency_and_buffer_size: source.concurrency_and_buffer_size ?? DefaultPerformanceConfig,
	};
	if (proxy_config) settings.proxy_config = proxy_config;
	if (source.send_back_raw_request !== undefined) settings.send_back_raw_request = source.send_back_raw_request;
	if (source.send_back_raw_response !== undefined) settings.send_back_raw_response = source.send_back_raw_response;
	if (source.store_raw_request_response !== undefined) settings.store_raw_request_response = source.store_raw_request_response;
	return settings;
};

const hasRestrictedRequests = (source: ModelProvider): boolean => {
	const allowed = source.custom_provider_config?.allowed_requests;
	if (!allowed) return false;
	return Object.values(allowed as unknown as Record<string, boolean | undefined>).some((v) => v === false);
};

/**
 * Builds the migration plan from the custom provider, its (redacted) keys and, when the
 * first-party provider already exists, that provider and its keys.
 */
export const buildDatabricksMigrationPlan = (
	source: ModelProvider,
	sourceKeys: ModelProviderKey[],
	existingTarget?: ExistingTarget,
): MigrationPlan => {
	const warnings: string[] = [];
	const derived = deriveDatabricksWorkspace(source.network_config?.base_url);
	warnings.push(...derived.warnings);
	if (!derived.workspaceUrl) {
		warnings.push("The base URL could not be parsed; enter the workspace URL below.");
	}

	const isKeyless = Boolean(source.custom_provider_config?.is_key_less) || sourceKeys.length === 0;
	const nameClash = source.name.trim().toLowerCase() === DATABRICKS_PROVIDER;
	const targetExists = Boolean(existingTarget);
	const takenNames = new Set<string>();
	if (!nameClash) sourceKeys.forEach((k) => takenNames.add(k.name));
	existingTarget?.keys.forEach((k) => takenNames.add(k.name));

	const auth = extractAuthFromHeaders(source.network_config?.extra_headers);
	const keys: PlannedKey[] = [];

	const pickCreateName = (name: string, permanent: boolean): string => {
		if (!takenNames.has(name)) return name;
		const suffixed = `${name}${MIGRATING_SUFFIX}`;
		if (permanent) {
			warnings.push(`A key named "${name}" already exists on the Databricks provider; the migrated key will be created as "${suffixed}".`);
		}
		return suffixed;
	};

	if (isKeyless) {
		const name = `${source.name}-token`;
		const clashesWithTarget = existingTarget?.keys.some((k) => k.name === name) ?? false;
		const createName = pickCreateName(name, clashesWithTarget);
		if (auth.problem === "missing") {
			warnings.push("No Authorization header was found on the custom provider; enter the personal access token below.");
		} else if (auth.problem === "not_bearer") {
			warnings.push("The Authorization header is not a Bearer token; enter the personal access token below.");
		}
		keys.push({
			tempId: "header",
			name: clashesWithTarget ? createName : name,
			createName,
			fromHeader: true,
			value: auth.token,
			needsValue: !auth.token,
			models: ["*"],
			blacklisted_models: [],
			weight: 1,
			enabled: true,
		});
	} else {
		if (auth.token) {
			warnings.push("The custom provider also has an Authorization header; it will be dropped because the provider keys take precedence.");
		}
		for (const key of sourceKeys) {
			const clashesWithTarget = existingTarget?.keys.some((k) => k.name === key.name) ?? false;
			const createName = pickCreateName(key.name, clashesWithTarget);
			const value = key.value && isSecretVarSet(key.value) && !isMaskedLiteral(key.value) ? toSecretVarFormValue(key.value) : undefined;
			keys.push({
				tempId: key.id,
				name: clashesWithTarget ? createName : key.name,
				createName,
				fromHeader: false,
				value,
				needsValue: !value,
				models: key.models ?? ["*"],
				blacklisted_models: key.blacklisted_models ?? [],
				weight: key.weight ?? 1,
				enabled: key.enabled ?? true,
				aliases: normalizeAliases(key.aliases),
			});
		}
	}

	if (hasRestrictedRequests(source)) {
		warnings.push("Allowed request restrictions are specific to custom providers and will not be carried over.");
	}
	if (Object.keys(source.custom_provider_config?.request_path_overrides ?? {}).length > 0) {
		warnings.push("Request path overrides are specific to custom providers and will not be carried over.");
	}
	if (source.config_hash) {
		warnings.push("This provider is synced from config.json. Remove it there as well, or it may be re-added on the next restart.");
	}
	if (targetExists) {
		warnings.push("A Databricks provider is already configured. Its settings will be kept and the migrated keys will be added to it.");
	}
	if (nameClash) {
		warnings.push(
			'The custom provider is named "databricks", so it has to be deleted before the official provider can be created. If a later step fails, it will be restored from a snapshot; key secrets that are stored as literals cannot be restored and would need to be re-entered.',
		);
	}

	const providerSettings = buildProviderSettings(source, warnings);

	return {
		source: {
			name: source.name,
			isKeyless,
			fromConfigFile: Boolean(source.config_hash),
		},
		nameClash,
		targetExists,
		workspaceUrl: derived.workspaceUrl,
		apiFormat: derived.apiFormat,
		providerSettings,
		keys,
		warnings,
		snapshot: { provider: source, keys: sourceKeys },
	};
};

/**
 * Reduces any workspace URL a user may paste (with a scheme, path, query or trailing slash) to
 * the bare origin the first-party provider expects. Returns "" when it cannot be parsed.
 */
export const normalizeWorkspaceUrl = (raw: string | undefined): string => {
	const url = parseUrl(raw);
	return url ? `https://${url.host}` : "";
};

/** True while the preview still lacks a usable workspace URL or a token for any key. */
export const planNeedsInput = (plan: MigrationPlan): boolean =>
	!normalizeWorkspaceUrl(plan.workspaceUrl) || plan.keys.some((k) => !isSecretVarSet(k.value));

/** Builds the key payload sent to POST /api/providers/databricks/keys. */
export const toDatabricksKeyPayload = (plan: MigrationPlan, key: PlannedKey): ModelProviderKey => ({
	id: "",
	name: key.createName,
	value: key.value,
	models: key.models,
	blacklisted_models: key.blacklisted_models,
	weight: key.weight,
	enabled: key.enabled,
	aliases: key.aliases,
	databricks_key_config: {
		workspace_url: { value: normalizeWorkspaceUrl(plan.workspaceUrl), ref: "" },
		api_format: plan.apiFormat,
		forward_gateway_tags: false,
	},
});

// ---------------------------------------------------------------------------
// Orchestration
// ---------------------------------------------------------------------------

export interface MigrationApi {
	getProvider(name: string): Promise<ModelProvider>;
	getProviderKeys(name: string): Promise<ModelProviderKey[]>;
	createProvider(body: AddProviderRequest): Promise<ModelProvider>;
	updateProvider(name: string, body: UpdateProviderRequest): Promise<ModelProvider>;
	deleteProvider(name: string): Promise<unknown>;
	createProviderKey(provider: string, key: ModelProviderKey): Promise<ModelProviderKey>;
	updateProviderKey(provider: string, keyId: string, key: ModelProviderKey): Promise<ModelProviderKey>;
	deleteProviderKey(provider: string, keyId: string): Promise<unknown>;
	refreshModels(provider: string): Promise<unknown>;
}

export type MigrationStepStatus = "pending" | "running" | "done" | "skipped" | "failed";

export interface MigrationStep {
	id: string;
	label: string;
	status: MigrationStepStatus;
	detail?: string;
}

export interface MigrationResult {
	ok: boolean;
	/** The new provider is in place but cleanup did not fully complete. */
	partial: boolean;
	message: string;
}

export const getErrorStatus = (err: unknown): number | undefined => {
	if (err && typeof err === "object" && "status" in err && typeof (err as { status: unknown }).status === "number") {
		return (err as { status: number }).status;
	}
	return undefined;
};

const errorText = (err: unknown): string => {
	if (err instanceof Error) return err.message;
	if (typeof err === "string") return err;
	return "Unknown error";
};

const STEP_IDS = {
	snapshot: "snapshot",
	deleteSource: "delete-source",
	createProvider: "create-provider",
	applySettings: "apply-settings",
	createKeys: "create-keys",
	verify: "verify",
	renameKeys: "rename-keys",
	refreshModels: "refresh-models",
} as const;

export const buildMigrationSteps = (plan: MigrationPlan): MigrationStep[] => {
	const label = plan.source.name;
	const keyCount = plan.keys.length;
	const keysLabel = `Create ${keyCount} key${keyCount === 1 ? "" : "s"} on the Databricks provider`;
	if (plan.nameClash) {
		return [
			{
				id: STEP_IDS.snapshot,
				label: `Snapshot custom provider "${label}"`,
				status: "pending",
			},
			{
				id: STEP_IDS.deleteSource,
				label: `Delete custom provider "${label}"`,
				status: "pending",
			},
			{
				id: STEP_IDS.createProvider,
				label: "Create the Databricks provider",
				status: "pending",
			},
			{
				id: STEP_IDS.applySettings,
				label: "Apply network and performance settings",
				status: "pending",
			},
			{ id: STEP_IDS.createKeys, label: keysLabel, status: "pending" },
			{
				id: STEP_IDS.verify,
				label: "Verify the Databricks provider",
				status: "pending",
			},
			{
				id: STEP_IDS.refreshModels,
				label: "Refresh models",
				status: "pending",
			},
		];
	}
	const steps: MigrationStep[] = [
		{
			id: STEP_IDS.createProvider,
			label: "Create the Databricks provider",
			status: "pending",
		},
		{
			id: STEP_IDS.applySettings,
			label: "Apply network and performance settings",
			status: "pending",
		},
		{ id: STEP_IDS.createKeys, label: keysLabel, status: "pending" },
		{
			id: STEP_IDS.verify,
			label: "Verify the Databricks provider",
			status: "pending",
		},
		{
			id: STEP_IDS.deleteSource,
			label: `Delete custom provider "${label}"`,
			status: "pending",
		},
	];
	if (plan.keys.some((k) => k.createName !== k.name)) {
		steps.push({
			id: STEP_IDS.renameKeys,
			label: "Restore original key names",
			status: "pending",
		});
	}
	steps.push({
		id: STEP_IDS.refreshModels,
		label: "Refresh models",
		status: "pending",
	});
	return steps;
};

class MigrationAbort extends Error {}

/**
 * Runs the migration. The normal path only deletes the custom provider once the new one is
 * created and verified; the name-clash path deletes first and restores from a snapshot on
 * failure. Every rollback call is best-effort and reported in the failed step's detail.
 */
export const runDatabricksMigration = async (
	plan: MigrationPlan,
	api: MigrationApi,
	onProgress: (steps: MigrationStep[]) => void,
): Promise<MigrationResult> => {
	const steps = buildMigrationSteps(plan);
	const emit = () => onProgress(steps.map((s) => ({ ...s })));
	const step = (id: string) => steps.find((s) => s.id === id)!;
	const start = (id: string) => {
		step(id).status = "running";
		emit();
	};
	const done = (id: string, detail?: string) => {
		const s = step(id);
		s.status = "done";
		if (detail) s.detail = detail;
		emit();
	};
	const skip = (id: string, detail: string) => {
		const s = step(id);
		s.status = "skipped";
		s.detail = detail;
		emit();
	};
	const fail = (id: string, detail: string) => {
		const s = step(id);
		s.status = "failed";
		s.detail = detail;
		emit();
	};

	const target = DATABRICKS_PROVIDER;
	let createdProvider = false;
	const createdKeys: ModelProviderKey[] = [];
	let sourceDeleted = false;

	const rollbackNotes: string[] = [];
	const attempt = async (what: string, fn: () => Promise<unknown>) => {
		try {
			await fn();
			rollbackNotes.push(`${what}: ok`);
		} catch (err) {
			rollbackNotes.push(`${what}: failed (${errorText(err)})`);
		}
	};

	const rollbackTarget = async () => {
		for (const key of createdKeys) {
			await attempt(`Removed key "${key.name}"`, () => api.deleteProviderKey(target, key.id));
		}
		if (createdProvider) {
			await attempt("Removed the Databricks provider", () => api.deleteProvider(target));
		}
	};

	const restoreSnapshot = async () => {
		if (!sourceDeleted) return;
		const { provider, keys } = plan.snapshot;
		await attempt(`Restored custom provider "${provider.name}"`, () =>
			api.createProvider({
				provider: provider.name,
				network_config: provider.network_config,
				concurrency_and_buffer_size: provider.concurrency_and_buffer_size,
				proxy_config: provider.proxy_config,
				send_back_raw_request: provider.send_back_raw_request,
				send_back_raw_response: provider.send_back_raw_response,
				store_raw_request_response: provider.store_raw_request_response,
				custom_provider_config: provider.custom_provider_config,
			}),
		);
		for (const key of keys) {
			// Prefer what the user entered in the preview; a secret reference round-trips as-is.
			const planned = plan.keys.find((k) => k.tempId === key.id);
			const fromPlan = planned?.value && isSecretVarSet(planned.value) ? planned.value : undefined;
			const fromSource = key.value && isSecretVarSet(key.value) && !isMaskedLiteral(key.value) ? key.value : undefined;
			const value = fromPlan ?? fromSource;
			if (!value) {
				rollbackNotes.push(`Key "${key.name}" could not be restored: its secret is masked. Re-create it manually.`);
				continue;
			}
			await attempt(`Restored key "${key.name}"`, () => api.createProviderKey(provider.name, { ...key, id: "", value }));
		}
	};

	const abort = async (id: string, err: unknown, rollback: () => Promise<void>): Promise<never> => {
		await rollback();
		const detail = [errorText(err), ...rollbackNotes].join("\n");
		fail(id, detail);
		throw new MigrationAbort(errorText(err));
	};

	try {
		if (plan.nameClash) {
			start(STEP_IDS.snapshot);
			done(STEP_IDS.snapshot, `${plan.snapshot.keys.length} key(s) captured`);

			start(STEP_IDS.deleteSource);
			try {
				await api.deleteProvider(plan.source.name);
				sourceDeleted = true;
				done(STEP_IDS.deleteSource);
			} catch (err) {
				await abort(STEP_IDS.deleteSource, err, async () => {});
			}
		}

		// Create provider
		start(STEP_IDS.createProvider);
		if (plan.targetExists && !plan.nameClash) {
			skip(STEP_IDS.createProvider, "Already configured");
		} else {
			try {
				await api.createProvider({
					provider: target,
					...plan.providerSettings,
				});
				createdProvider = true;
				done(STEP_IDS.createProvider);
			} catch (err) {
				if (getErrorStatus(err) === 409 && !plan.nameClash) {
					skip(STEP_IDS.createProvider, "Already configured");
				} else {
					await abort(STEP_IDS.createProvider, err, restoreSnapshot);
				}
			}
		}

		// Apply settings
		start(STEP_IDS.applySettings);
		if (!createdProvider) {
			skip(STEP_IDS.applySettings, "Existing Databricks settings kept");
		} else {
			try {
				await api.updateProvider(target, plan.providerSettings);
				done(STEP_IDS.applySettings);
			} catch (err) {
				await abort(STEP_IDS.applySettings, err, async () => {
					await rollbackTarget();
					await restoreSnapshot();
				});
			}
		}

		// Create keys
		start(STEP_IDS.createKeys);
		for (const planned of plan.keys) {
			try {
				const created = await api.createProviderKey(target, toDatabricksKeyPayload(plan, planned));
				createdKeys.push(created);
			} catch (err) {
				await abort(STEP_IDS.createKeys, new Error(`Failed to create key "${planned.createName}": ${errorText(err)}`), async () => {
					await rollbackTarget();
					await restoreSnapshot();
				});
			}
		}
		done(STEP_IDS.createKeys);

		// Verify
		start(STEP_IDS.verify);
		try {
			const provider = await api.getProvider(target);
			if (provider.provider_status !== "active") {
				throw new Error(`Provider status is "${provider.provider_status}"`);
			}
			const keys = await api.getProviderKeys(target);
			const missing = createdKeys.filter((created) => !keys.some((k) => k.id === created.id));
			if (missing.length > 0) {
				throw new Error(`${missing.length} migrated key(s) are missing on the provider`);
			}
			done(STEP_IDS.verify, `Provider active with ${keys.length} key(s)`);
		} catch (err) {
			await abort(STEP_IDS.verify, err, async () => {
				await rollbackTarget();
				await restoreSnapshot();
			});
		}

		let partial = false;
		const partialNotes: string[] = [];

		if (!plan.nameClash) {
			start(STEP_IDS.deleteSource);
			try {
				await api.deleteProvider(plan.source.name);
				sourceDeleted = true;
				done(STEP_IDS.deleteSource);
			} catch (err) {
				partial = true;
				partialNotes.push(`Could not delete custom provider "${plan.source.name}": ${errorText(err)}. Delete it manually.`);
				fail(STEP_IDS.deleteSource, `${errorText(err)}. The Databricks provider was kept; delete "${plan.source.name}" manually.`);
			}

			const renames = plan.keys.filter((k) => k.createName !== k.name);
			if (renames.length > 0) {
				start(STEP_IDS.renameKeys);
				if (!sourceDeleted) {
					skip(STEP_IDS.renameKeys, "Custom provider still exists; key names keep the -migrating suffix");
				} else {
					const failures: string[] = [];
					for (const planned of renames) {
						const created = createdKeys.find((k) => k.name === planned.createName);
						if (!created) continue;
						try {
							await api.updateProviderKey(target, created.id, {
								...created,
								name: planned.name,
							});
						} catch (err) {
							failures.push(`"${planned.createName}" (${errorText(err)})`);
						}
					}
					if (failures.length > 0) {
						partial = true;
						partialNotes.push(`Some keys kept their temporary name: ${failures.join(", ")}.`);
						fail(STEP_IDS.renameKeys, `Could not rename: ${failures.join(", ")}`);
					} else {
						done(STEP_IDS.renameKeys);
					}
				}
			}
		}

		start(STEP_IDS.refreshModels);
		try {
			await api.refreshModels(target);
			done(STEP_IDS.refreshModels);
		} catch (err) {
			done(STEP_IDS.refreshModels, getErrorStatus(err) === 409 ? "A refresh is already running" : `Skipped: ${errorText(err)}`);
		}

		return {
			ok: true,
			partial,
			message: partial
				? `Databricks provider created with ${createdKeys.length} key(s), but cleanup did not fully complete. ${partialNotes.join(" ")}`
				: `Migrated "${plan.source.name}" to the Databricks provider with ${createdKeys.length} key(s).`,
		};
	} catch (err) {
		if (err instanceof MigrationAbort) {
			return { ok: false, partial: false, message: err.message };
		}
		return { ok: false, partial: false, message: errorText(err) };
	}
};
