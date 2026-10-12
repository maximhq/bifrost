import { ModelProvider, ModelProviderKey, ModelProviderName, NetworkConfig, UpdateProviderRequest } from "@/lib/types/config";
import { DefaultNetworkConfig, DefaultPerformanceConfig } from "@/lib/constants/config";
import { SecretVar } from "@/lib/types/schemas";
import { extractAuthFromHeaders, isSecretVarSet, MigrationApi, MigrationResult, MigrationStep } from "@/lib/utils/databricksMigration";

/**
 * Frontend migration of a custom provider named "cloudflare" that points at Workers AI (created
 * before Bifrost shipped first-party Cloudflare support) to the official `cloudflare` provider.
 * Once "cloudflare" became a standard provider name, the backend rejects any update to a custom
 * provider holding it, except the one that drops custom_provider_config.
 *
 * The provider is converted in place rather than deleted and re-created: its keys keep their IDs,
 * and the virtual key grants, budgets, rate limits and model limits that reference the provider
 * by name are kept. Reuses databricksMigration.ts's provider-neutral helpers and types, and is
 * kept free of React and RTK so it can be unit tested.
 */

export const CLOUDFLARE_PROVIDER: ModelProviderName = "cloudflare";

const WORKERS_AI_HOST = "api.cloudflare.com";
// Workers AI's OpenAI-compatible base: /client/v4/accounts/{account_id}/ai, optionally with /v1.
const WORKERS_AI_PATH = /^\/client\/v4\/accounts\/([^/]+)\/ai(?:\/v1)?$/;

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

const NOT_WORKERS_AI: DerivedAccount = { isWorkersAI: false, accountId: "" };

/** Reads the account ID out of a Workers AI base URL. Any other URL (AI Gateway included) is not Workers AI. */
export const deriveCloudflareAccount = (baseUrl: string | undefined): DerivedAccount => {
	const url = parseUrl(baseUrl);
	if (!url || url.hostname.toLowerCase() !== WORKERS_AI_HOST) return NOT_WORKERS_AI;
	const match = url.pathname.replace(/\/+$/, "").match(WORKERS_AI_PATH);
	if (!match) return NOT_WORKERS_AI;
	// A malformed percent escape would make decodeURIComponent throw during render.
	try {
		return { isWorkersAI: true, accountId: decodeURIComponent(match[1]) };
	} catch {
		return NOT_WORKERS_AI;
	}
};

/** A custom "cloudflare" provider that points at Workers AI and can be migrated to the first-party provider. */
export const isMigratableCloudflareProvider = (provider: ModelProvider): boolean =>
	isCustomCloudflareProvider(provider) && deriveCloudflareAccount(provider.network_config?.base_url).isWorkersAI;

// ---------------------------------------------------------------------------
// Plan
// ---------------------------------------------------------------------------

/** UpdateProviderRequest with custom_provider_config sent as null, which the update endpoint reads as removing it. */
export type ConvertProviderRequest = Omit<UpdateProviderRequest, "custom_provider_config"> & { custom_provider_config: null };

export interface MigrationPlan {
	providerName: string;
	accountId: string;
	/** Settings applied by the conversion; its null custom_provider_config is what converts the provider. */
	providerSettings: ConvertProviderRequest;
	/** Existing keys, updated in place with the account ID. Empty for a keyless source. */
	keys: ModelProviderKey[];
	/** For a keyless source, the API token read from the Authorization header, created as a key after conversion. */
	headerToken?: { name: string; value?: SecretVar; needsValue: boolean };
	warnings: string[];
}

/**
 * Settings for the converted provider: the source's own, minus what was specific to calling
 * Workers AI as a custom provider (the base URL and the Authorization header). Masked secrets
 * are sent back as they are; the update endpoint restores the stored values for them.
 */
const buildProviderSettings = (source: ModelProvider): ConvertProviderRequest => {
	const sourceNetwork: NetworkConfig = source.network_config ?? DefaultNetworkConfig;
	const { base_url: _baseUrl, extra_headers, ...network } = sourceNetwork;
	const headers = Object.fromEntries(Object.entries(extra_headers ?? {}).filter(([key]) => key.trim().toLowerCase() !== "authorization"));
	const settings: ConvertProviderRequest = {
		// Sent as null, not omitted: the update endpoint keeps fields the body leaves out.
		custom_provider_config: null,
		network_config: Object.keys(headers).length > 0 ? { ...network, extra_headers: headers } : network,
		concurrency_and_buffer_size: source.concurrency_and_buffer_size ?? DefaultPerformanceConfig,
	};
	if (source.proxy_config) settings.proxy_config = source.proxy_config;
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

/** Builds the migration plan from the custom provider and its (redacted) keys. */
export const buildCloudflareMigrationPlan = (source: ModelProvider, sourceKeys: ModelProviderKey[]): MigrationPlan => {
	const warnings: string[] = [];
	const { accountId } = deriveCloudflareAccount(source.network_config?.base_url);
	if (!accountId) {
		warnings.push("The account ID could not be read from the base URL; enter it below.");
	}

	const isKeyless = Boolean(source.custom_provider_config?.is_key_less) || sourceKeys.length === 0;
	let headerToken: MigrationPlan["headerToken"];
	if (isKeyless) {
		const auth = extractAuthFromHeaders(source.network_config?.extra_headers);
		if (auth.problem === "missing") {
			warnings.push("No Authorization header was found on the custom provider; enter the API token below.");
		} else if (auth.problem === "not_bearer") {
			warnings.push("The Authorization header is not a Bearer token; enter the API token below.");
		}
		headerToken = { name: `${source.name}-token`, value: auth.token, needsValue: !auth.token };
	}

	if (hasRestrictedRequests(source)) {
		warnings.push("Allowed request restrictions are specific to custom providers and will not be carried over.");
	}
	if (Object.keys(source.custom_provider_config?.request_path_overrides ?? {}).length > 0) {
		warnings.push("Request path overrides are specific to custom providers and will not be carried over.");
	}
	if (source.config_hash) {
		warnings.push(
			"This provider is synced from config.json. Update it there as well, or the custom provider may be restored on the next restart.",
		);
	}

	return {
		providerName: source.name,
		accountId,
		providerSettings: buildProviderSettings(source),
		keys: isKeyless ? [] : sourceKeys,
		headerToken,
		warnings,
	};
};

/** True while the preview still lacks an account ID, or the API token of a keyless source. */
export const planNeedsInput = (plan: MigrationPlan): boolean =>
	!plan.accountId.trim() || (plan.headerToken !== undefined && !isSecretVarSet(plan.headerToken.value));

const cloudflareKeyConfig = (plan: MigrationPlan) => ({ account_id: { value: plan.accountId.trim(), ref: "" } });

/** The key payload for converting an existing key in place: the key as read, plus the account ID. */
export const toCloudflareKeyUpdate = (plan: MigrationPlan, key: ModelProviderKey): ModelProviderKey => ({
	...key,
	cloudflare_key_config: cloudflareKeyConfig(plan),
});

// ---------------------------------------------------------------------------
// Orchestration
// ---------------------------------------------------------------------------

/** The calls the migration makes. Deliberately has no delete: the provider is converted in place. */
export type CloudflareMigrationApi = Pick<
	MigrationApi,
	"getProvider" | "getProviderKeys" | "updateProviderKey" | "createProviderKey" | "refreshModels"
> & {
	updateProvider(name: string, body: ConvertProviderRequest): Promise<ModelProvider>;
};

const errorText = (err: unknown): string => {
	if (err instanceof Error) return err.message;
	if (typeof err === "string") return err;
	return "Unknown error";
};

const STEP_IDS = {
	updateKeys: "update-keys",
	convert: "convert",
	createKey: "create-key",
	verify: "verify",
	refreshModels: "refresh-models",
} as const;

export const buildMigrationSteps = (plan: MigrationPlan): MigrationStep[] => {
	const steps: MigrationStep[] = [];
	if (plan.keys.length > 0) {
		const count = plan.keys.length;
		steps.push({ id: STEP_IDS.updateKeys, label: `Add the account ID to ${count} key${count === 1 ? "" : "s"}`, status: "pending" });
	}
	steps.push({ id: STEP_IDS.convert, label: "Convert to the official Cloudflare provider", status: "pending" });
	if (plan.headerToken) {
		steps.push({ id: STEP_IDS.createKey, label: `Create key "${plan.headerToken.name}" from the Authorization header`, status: "pending" });
	}
	steps.push({ id: STEP_IDS.verify, label: "Verify the Cloudflare provider", status: "pending" });
	steps.push({ id: STEP_IDS.refreshModels, label: "Refresh models", status: "pending" });
	return steps;
};

/**
 * Runs the migration. Nothing is deleted, so there is nothing to roll back: a failure before the
 * conversion leaves the custom provider working (an account ID on its keys is ignored by the
 * OpenAI-compatible provider), and a failure after it leaves the official provider in place.
 */
export const runCloudflareMigration = async (
	plan: MigrationPlan,
	api: CloudflareMigrationApi,
	onProgress: (steps: MigrationStep[]) => void,
): Promise<MigrationResult> => {
	const steps = buildMigrationSteps(plan);
	const emit = () => onProgress(steps.map((s) => ({ ...s })));
	const set = (id: string, status: MigrationStep["status"], detail?: string) => {
		const s = steps.find((x) => x.id === id)!;
		s.status = status;
		if (detail) s.detail = detail;
		emit();
	};
	const name = plan.providerName;
	let converted = false;

	const run = async (id: string, fn: () => Promise<string | void>) => {
		set(id, "running");
		try {
			set(id, "done", (await fn()) ?? undefined);
		} catch (err) {
			set(id, "failed", errorText(err));
			throw err;
		}
	};

	try {
		if (plan.keys.length > 0) {
			await run(STEP_IDS.updateKeys, async () => {
				for (const key of plan.keys) {
					try {
						await api.updateProviderKey(name, key.id, toCloudflareKeyUpdate(plan, key));
					} catch (err) {
						throw new Error(`Failed to update key "${key.name}": ${errorText(err)}`);
					}
				}
			});
		}

		await run(STEP_IDS.convert, () => api.updateProvider(name, plan.providerSettings).then(() => undefined));
		converted = true;

		if (plan.headerToken) {
			const { name: keyName, value } = plan.headerToken;
			await run(STEP_IDS.createKey, () =>
				api
					.createProviderKey(name, {
						id: "",
						name: keyName,
						value,
						models: ["*"],
						blacklisted_models: [],
						weight: 1,
						enabled: true,
						cloudflare_key_config: cloudflareKeyConfig(plan),
					})
					.then(() => undefined),
			);
		}

		await run(STEP_IDS.verify, async () => {
			const provider = await api.getProvider(name);
			if (provider.custom_provider_config) throw new Error("The provider is still a custom provider");
			if (provider.provider_status !== "active") throw new Error(`Provider status is "${provider.provider_status}"`);
			const keys = await api.getProviderKeys(name);
			return `Provider active with ${keys.length} key(s)`;
		});
	} catch (err) {
		return {
			ok: false,
			partial: converted,
			message: converted
				? `"${name}" was converted to the official Cloudflare provider, but a later step failed: ${errorText(err)}. Nothing was deleted; finish the setup on the provider page.`
				: `Migration failed: ${errorText(err)}. Nothing was deleted, and "${name}" still works as a custom provider.`,
		};
	}

	set(STEP_IDS.refreshModels, "running");
	try {
		await api.refreshModels(name);
		set(STEP_IDS.refreshModels, "done");
	} catch (err) {
		set(STEP_IDS.refreshModels, "done", `Skipped: ${errorText(err)}`);
	}

	return {
		ok: true,
		partial: false,
		message: `Converted "${name}" to the official Cloudflare provider. Its keys, access settings, budgets and limits were kept.`,
	};
};