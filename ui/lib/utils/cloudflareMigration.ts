import {
	AddProviderRequest,
	AliasConfig,
	ModelProvider,
	ModelProviderKey,
	ModelProviderName,
	NetworkConfig,
	ProxyConfig,
	UpdateProviderRequest,
} from "@/lib/types/config";
import { DefaultNetworkConfig, DefaultPerformanceConfig } from "@/lib/constants/config";
import { SecretVar } from "@/lib/types/schemas";
import {
	ExistingTarget,
	extractAuthFromHeaders,
	getErrorStatus,
	isSecretVarSet,
	MigrationApi,
	MigrationResult,
	MigrationStep,
	PlannedKey,
} from "@/lib/utils/databricksMigration";
import { toSecretVarFormValue } from "@/lib/utils/secretVarForm";
import { isRedacted } from "@/lib/utils/validation";

/**
 * Frontend-only migration of a custom provider named "cloudflare" that points at Workers AI
 * (created before Bifrost shipped first-party Cloudflare support) to the official `cloudflare`
 * provider. Once "cloudflare" became a standard provider name, the backend rejects any update
 * to a custom provider holding it, so the custom one has to move off the name.
 *
 * Mirrors databricksMigration.ts and reuses its provider-neutral helpers and types. Kept free
 * of React and RTK so it can be unit tested.
 */

export const CLOUDFLARE_PROVIDER: ModelProviderName = "cloudflare";

const WORKERS_AI_HOST = "api.cloudflare.com";
// Workers AI's OpenAI-compatible base: /client/v4/accounts/{account_id}/ai, optionally with /v1.
const WORKERS_AI_PATH = /^\/client\/v4\/accounts\/([^/]+)\/ai(?:\/v1)?$/;
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

/** True for a custom provider named exactly "cloudflare" (case-insensitive), wherever it points. */
export const isCustomCloudflareProvider = (provider: ModelProvider): boolean =>
	Boolean(provider.custom_provider_config) && provider.name.trim().toLowerCase() === CLOUDFLARE_PROVIDER;

export interface DerivedAccount {
	/** The base URL is a Workers AI URL, so the first-party provider can serve the same traffic. */
	isWorkersAI: boolean;
	accountId: string;
}

/** Reads the account ID out of a Workers AI base URL. Any other URL (AI Gateway included) is not Workers AI. */
export const deriveCloudflareAccount = (baseUrl: string | undefined): DerivedAccount => {
	const url = parseUrl(baseUrl);
	if (!url || url.hostname.toLowerCase() !== WORKERS_AI_HOST) return { isWorkersAI: false, accountId: "" };
	const match = url.pathname.replace(/\/+$/, "").match(WORKERS_AI_PATH);
	if (!match) return { isWorkersAI: false, accountId: "" };
	return { isWorkersAI: true, accountId: decodeURIComponent(match[1]) };
};

/** A custom "cloudflare" provider that points at Workers AI and can be migrated to the first-party provider. */
export const isMigratableCloudflareProvider = (provider: ModelProvider): boolean =>
	isCustomCloudflareProvider(provider) && deriveCloudflareAccount(provider.network_config?.base_url).isWorkersAI;

// ---------------------------------------------------------------------------
// Derivation helpers
// ---------------------------------------------------------------------------

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

export interface MigrationPlan {
	source: {
		name: string;
		isKeyless: boolean;
		fromConfigFile: boolean;
	};
	/** The custom provider is literally named "cloudflare", so it must be deleted before the first-party one can be created. */
	nameClash: boolean;
	/** A first-party Cloudflare provider is already configured; keys are merged into it. */
	targetExists: boolean;
	accountId: string;
	providerSettings: UpdateProviderRequest;
	keys: PlannedKey[];
	warnings: string[];
	/** Snapshot of the source, used to restore it if the name-clash path fails midway. */
	snapshot: { provider: ModelProvider; keys: ModelProviderKey[] };
}

const stripAuthorizationHeader = (headers: Record<string, string> | undefined): Record<string, string> | undefined => {
	if (!headers) return undefined;
	const rest = Object.fromEntries(Object.entries(headers).filter(([key]) => key.trim().toLowerCase() !== "authorization"));
	return Object.keys(rest).length > 0 ? rest : undefined;
};

const portableSecret = (secret: SecretVar | undefined, label: string, warnings: string[]): SecretVar | undefined => {
	if (!secret || !isSecretVarSet(secret)) return undefined;
	if (isMaskedLiteral(secret)) {
		warnings.push(`${label} is stored as a literal secret and cannot be copied; re-enter it on the Cloudflare provider after migrating.`);
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
		if (net.stream_idle_timeout_in_seconds !== undefined)
			network_config.stream_idle_timeout_in_seconds = net.stream_idle_timeout_in_seconds;
		if (net.keep_alive_timeout_in_seconds !== undefined) network_config.keep_alive_timeout_in_seconds = net.keep_alive_timeout_in_seconds;
		if (net.max_conns_per_host !== undefined) network_config.max_conns_per_host = net.max_conns_per_host;
		if (net.enforce_http2 !== undefined) network_config.enforce_http2 = net.enforce_http2;
		if (net.http2_ping_interval_in_seconds !== undefined)
			network_config.http2_ping_interval_in_seconds = net.http2_ping_interval_in_seconds;
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
	if (source.ignore_provider_cost !== undefined) settings.ignore_provider_cost = source.ignore_provider_cost;
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
export const buildCloudflareMigrationPlan = (
	source: ModelProvider,
	sourceKeys: ModelProviderKey[],
	existingTarget?: ExistingTarget,
): MigrationPlan => {
	const warnings: string[] = [];
	const { accountId } = deriveCloudflareAccount(source.network_config?.base_url);
	if (!accountId) {
		warnings.push("The account ID could not be read from the base URL; enter it below.");
	}

	const isKeyless = Boolean(source.custom_provider_config?.is_key_less) || sourceKeys.length === 0;
	const nameClash = source.name.trim().toLowerCase() === CLOUDFLARE_PROVIDER;
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
			warnings.push(`A key named "${name}" already exists on the Cloudflare provider; the migrated key will be created as "${suffixed}".`);
		}
		return suffixed;
	};

	if (isKeyless) {
		const name = `${source.name}-token`;
		const clashesWithTarget = existingTarget?.keys.some((k) => k.name === name) ?? false;
		const createName = pickCreateName(name, clashesWithTarget);
		if (auth.problem === "missing") {
			warnings.push("No Authorization header was found on the custom provider; enter the API token below.");
		} else if (auth.problem === "not_bearer") {
			warnings.push("The Authorization header is not a Bearer token; enter the API token below.");
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
		warnings.push("A Cloudflare provider is already configured. Its settings will be kept and the migrated keys will be added to it.");
	}
	if (nameClash) {
		warnings.push(
			'The custom provider is named "cloudflare", so it has to be deleted before the official provider can be created. If a later step fails, it will be restored from a snapshot; key secrets that are stored as literals cannot be restored and would need to be re-entered.',
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
		accountId,
		providerSettings,
		keys,
		warnings,
		snapshot: { provider: source, keys: sourceKeys },
	};
};

/** True while the preview still lacks an account ID or a token for any key. */
export const planNeedsInput = (plan: MigrationPlan): boolean => !plan.accountId.trim() || plan.keys.some((k) => !isSecretVarSet(k.value));

/** Builds the key payload sent to POST /api/providers/cloudflare/keys. */
export const toCloudflareKeyPayload = (plan: MigrationPlan, key: PlannedKey): ModelProviderKey => ({
	id: "",
	name: key.createName,
	value: key.value,
	models: key.models,
	blacklisted_models: key.blacklisted_models,
	weight: key.weight,
	enabled: key.enabled,
	aliases: key.aliases,
	cloudflare_key_config: {
		account_id: { value: plan.accountId.trim(), ref: "" },
	},
});

// ---------------------------------------------------------------------------
// Orchestration
// ---------------------------------------------------------------------------

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
	const keysLabel = `Create ${keyCount} key${keyCount === 1 ? "" : "s"} on the Cloudflare provider`;
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
				label: "Create the Cloudflare provider",
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
				label: "Verify the Cloudflare provider",
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
			label: "Create the Cloudflare provider",
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
			label: "Verify the Cloudflare provider",
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
export const runCloudflareMigration = async (
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

	const target = CLOUDFLARE_PROVIDER;
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
			await attempt("Removed the Cloudflare provider", () => api.deleteProvider(target));
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
				ignore_provider_cost: provider.ignore_provider_cost,
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
			skip(STEP_IDS.applySettings, "Existing Cloudflare settings kept");
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
				const created = await api.createProviderKey(target, toCloudflareKeyPayload(plan, planned));
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
				fail(STEP_IDS.deleteSource, `${errorText(err)}. The Cloudflare provider was kept; delete "${plan.source.name}" manually.`);
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
				? `Cloudflare provider created with ${createdKeys.length} key(s), but cleanup did not fully complete. ${partialNotes.join(" ")}`
				: `Migrated "${plan.source.name}" to the Cloudflare provider with ${createdKeys.length} key(s).`,
		};
	} catch (err) {
		if (err instanceof MigrationAbort) {
			return { ok: false, partial: false, message: err.message };
		}
		return { ok: false, partial: false, message: errorText(err) };
	}
};