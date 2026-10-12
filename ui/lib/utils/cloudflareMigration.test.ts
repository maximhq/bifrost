import { describe, expect, it } from "vitest";

import { ModelProvider, ModelProviderKey } from "@/lib/types/config";
import {
	buildCloudflareMigrationPlan,
	CloudflareMigrationApi,
	deriveCloudflareAccount,
	isCustomCloudflareProvider,
	isMigratableCloudflareProvider,
	MigrationPlan,
	planNeedsInput,
	runCloudflareMigration,
	toCloudflareKeyUpdate,
} from "./cloudflareMigration";

const ACCOUNT = "023e105f4ecef8ad9ca31a8372d0c353";
const WORKERS_AI_URL = `https://api.cloudflare.com/client/v4/accounts/${ACCOUNT}/ai`;
const REDACTED = "cfut************************abcd";

const custom = (name: string, overrides: Partial<ModelProvider> = {}): ModelProvider =>
	({
		name,
		provider_status: "active",
		custom_provider_config: { base_provider_type: "openai", is_key_less: true },
		network_config: {
			base_url: WORKERS_AI_URL,
			extra_headers: { Authorization: "Bearer cf-token-1234567890" },
			default_request_timeout_in_seconds: 60,
			max_retries: 2,
			retry_backoff_initial: 500,
			retry_backoff_max: 5000,
		},
		concurrency_and_buffer_size: { concurrency: 10, buffer_size: 20 },
		...overrides,
	}) as ModelProvider;

const keyed = (overrides: Partial<ModelProvider> = {}) =>
	custom("cloudflare", { custom_provider_config: { base_provider_type: "openai" }, ...overrides });

const key = (id: string, name: string, overrides: Partial<ModelProviderKey> = {}): ModelProviderKey => ({
	id,
	name,
	value: { value: REDACTED, ref: "" },
	models: ["@cf/meta/llama-3.1-8b-instruct-fp8"],
	blacklisted_models: [],
	weight: 0.5,
	enabled: true,
	...overrides,
});

describe("deriveCloudflareAccount", () => {
	it("reads the account ID from Workers AI base URLs", () => {
		for (const url of [
			WORKERS_AI_URL,
			`${WORKERS_AI_URL}/`,
			`${WORKERS_AI_URL}/v1`,
			`${WORKERS_AI_URL}/v1/`,
			`api.cloudflare.com/client/v4/accounts/${ACCOUNT}/ai`,
		]) {
			expect(deriveCloudflareAccount(url)).toEqual({ isWorkersAI: true, accountId: ACCOUNT });
		}
	});

	it("rejects AI Gateway, other paths, garbage and malformed escapes without throwing", () => {
		for (const url of [
			`https://gateway.ai.cloudflare.com/v1/${ACCOUNT}/my-gateway/workers-ai`,
			`https://gateway.ai.cloudflare.com/v1/${ACCOUNT}/my-gateway/compat`,
			`https://api.cloudflare.com/client/v4/accounts/${ACCOUNT}/ai/run`,
			"https://api.cloudflare.com/client/v4",
			"https://api.cloudflare.com/client/v4/accounts/%E0%A4%A/ai",
			"",
			undefined,
			"::not a url::",
		]) {
			expect(deriveCloudflareAccount(url).isWorkersAI).toBe(false);
		}
	});
});

describe("isMigratableCloudflareProvider", () => {
	it("matches only a custom provider named exactly cloudflare that points at Workers AI", () => {
		expect(isMigratableCloudflareProvider(custom("cloudflare"))).toBe(true);
		expect(isMigratableCloudflareProvider(custom(" Cloudflare "))).toBe(true);
		expect(isMigratableCloudflareProvider(custom("cloudflare-workers"))).toBe(false);
		expect(isMigratableCloudflareProvider(custom("cloudflare", { custom_provider_config: undefined }))).toBe(false);
		const gateway = custom("cloudflare", {
			network_config: { ...custom("x").network_config!, base_url: `https://gateway.ai.cloudflare.com/v1/${ACCOUNT}/gw/compat` },
		});
		expect(isCustomCloudflareProvider(gateway)).toBe(true);
		expect(isMigratableCloudflareProvider(gateway)).toBe(false);
	});
});

describe("buildCloudflareMigrationPlan", () => {
	it("keeps the existing keys as they are, with nothing to re-enter", () => {
		const keys = [key("k1", "prod"), key("k2", "backup", { value: { value: "", ref: "env.CLOUDFLARE_API_TOKEN", type: "env" } })];
		const plan = buildCloudflareMigrationPlan(keyed(), keys);
		expect(plan.accountId).toBe(ACCOUNT);
		expect(plan.keys).toEqual(keys);
		expect(plan.headerToken).toBeUndefined();
		expect(planNeedsInput(plan)).toBe(false);
	});

	it("reads the API token from the Authorization header for a keyless source", () => {
		const plan = buildCloudflareMigrationPlan(custom("cloudflare"), []);
		expect(plan.keys).toEqual([]);
		expect(plan.headerToken).toMatchObject({ name: "cloudflare-token", value: { value: "cf-token-1234567890" }, needsValue: false });
		expect(planNeedsInput(plan)).toBe(false);

		const noHeader = buildCloudflareMigrationPlan(
			custom("cloudflare", { network_config: { ...custom("x").network_config!, extra_headers: {} } }),
			[],
		);
		expect(noHeader.warnings.join(" ")).toContain("No Authorization header");
		expect(planNeedsInput(noHeader)).toBe(true);
		expect(noHeader.headerToken?.needsValue).toBe(true);

		// Typing a token completes the plan, but the token still needs entering, so its input stays visible.
		const typed = { ...noHeader, headerToken: { ...noHeader.headerToken!, value: { value: "c", ref: "" } } };
		expect(planNeedsInput(typed)).toBe(false);
		expect(typed.headerToken.needsValue).toBe(true);
	});

	it("drops the base url and Authorization header and sends masked secrets back unchanged", () => {
		const proxy = { type: "http" as const, url: { value: "http://proxy:8080", ref: "" }, password: { value: "****", ref: "" } };
		const plan = buildCloudflareMigrationPlan(
			keyed({
				network_config: { ...custom("x").network_config!, extra_headers: { Authorization: "Bearer t", "X-Team": "ml" } },
				proxy_config: proxy,
			}),
			[key("k1", "prod")],
		);
		expect(plan.providerSettings.network_config.base_url).toBeUndefined();
		expect(plan.providerSettings.network_config.extra_headers).toEqual({ "X-Team": "ml" });
		expect(plan.providerSettings.proxy_config).toEqual(proxy);
		expect(plan.providerSettings.custom_provider_config).toBeNull();
	});

	it("needs the account ID when it cannot be read", () => {
		const plan = buildCloudflareMigrationPlan(keyed({ network_config: { ...custom("x").network_config!, base_url: "" } }), [
			key("k1", "prod"),
		]);
		expect(plan.warnings.join(" ")).toContain("account ID could not be read");
		expect(planNeedsInput(plan)).toBe(true);
		expect(planNeedsInput({ ...plan, accountId: ACCOUNT })).toBe(false);
	});

	it("updates a key in place with the account ID, keeping everything else", () => {
		const plan = buildCloudflareMigrationPlan(keyed(), [key("k1", "prod")]);
		expect(toCloudflareKeyUpdate({ ...plan, accountId: ` ${ACCOUNT} ` }, plan.keys[0])).toEqual({
			...key("k1", "prod"),
			cloudflare_key_config: { account_id: { value: ACCOUNT, ref: "" } },
		});
	});
});

// ---------------------------------------------------------------------------
// Orchestrator
// ---------------------------------------------------------------------------

type Call = [string, ...unknown[]];

const fakeApi = (opts: { failOn?: (call: Call) => Error | undefined; converted?: boolean } = {}) => {
	const calls: Call[] = [];
	const record = async <T>(call: Call, result: () => T): Promise<T> => {
		calls.push(call);
		const err = opts.failOn?.(call);
		if (err) throw err;
		return result();
	};
	const api: CloudflareMigrationApi = {
		getProvider: (name) =>
			record(["getProvider", name], () => {
				const converted = opts.converted ?? true;
				return {
					name,
					provider_status: "active",
					custom_provider_config: converted ? undefined : { base_provider_type: "openai" },
				} as ModelProvider;
			}),
		getProviderKeys: (name) => record(["getProviderKeys", name], () => []),
		updateProvider: (name, body) => record(["updateProvider", name, body], () => ({ name, provider_status: "active" }) as ModelProvider),
		updateProviderKey: (provider, keyId, k) =>
			record(["updateProviderKey", provider, keyId, k.cloudflare_key_config?.account_id.value], () => k),
		createProviderKey: (provider, k) =>
			record(["createProviderKey", provider, k.name, k.cloudflare_key_config?.account_id.value], () => ({ ...k, id: "new" })),
		refreshModels: (provider) => record(["refreshModels", provider], () => undefined),
	};
	return { api, calls, names: () => calls.map((c) => c[0]) };
};

const keyedPlan = (): MigrationPlan => buildCloudflareMigrationPlan(keyed(), [key("k1", "prod"), key("k2", "backup")]);

describe("runCloudflareMigration", () => {
	it("adds the account ID to each key, then converts the provider in place, deleting nothing", async () => {
		const { api, names, calls } = fakeApi();
		const result = await runCloudflareMigration(keyedPlan(), api, () => {});
		expect(result).toMatchObject({ ok: true, partial: false });
		expect(names()).toEqual([
			"updateProviderKey",
			"updateProviderKey",
			"updateProvider",
			"getProvider",
			"getProviderKeys",
			"refreshModels",
		]);
		expect(calls.filter((c) => c[0] === "updateProviderKey").map((c) => [c[2], c[3]])).toEqual([
			["k1", ACCOUNT],
			["k2", ACCOUNT],
		]);
		const body = calls.find((c) => c[0] === "updateProvider")![2] as object;
		expect((body as { custom_provider_config?: unknown }).custom_provider_config).toBeNull();
	});

	it("converts a keyless source first, then creates its key from the header token", async () => {
		const { api, names, calls } = fakeApi();
		const result = await runCloudflareMigration(buildCloudflareMigrationPlan(custom("cloudflare"), []), api, () => {});
		expect(result.ok).toBe(true);
		expect(names()).toEqual(["updateProvider", "createProviderKey", "getProvider", "getProviderKeys", "refreshModels"]);
		expect(calls.find((c) => c[0] === "createProviderKey")).toEqual(["createProviderKey", "cloudflare", "cloudflare-token", ACCOUNT]);
	});

	it("leaves the custom provider untouched when a key update fails", async () => {
		const { api, names } = fakeApi({ failOn: (c) => (c[0] === "updateProviderKey" && c[2] === "k2" ? new Error("boom") : undefined) });
		const steps: string[] = [];
		const result = await runCloudflareMigration(keyedPlan(), api, (s) => steps.push(s.map((x) => x.status).join(",")));
		expect(result).toMatchObject({ ok: false, partial: false });
		expect(result.message).toContain("still works as a custom provider");
		expect(names()).not.toContain("updateProvider");
		expect(steps.at(-1)).toContain("failed");
	});

	it("reports a partial result when verification fails after the conversion", async () => {
		const { api } = fakeApi({ converted: false });
		const result = await runCloudflareMigration(keyedPlan(), api, () => {});
		expect(result).toMatchObject({ ok: false, partial: true });
		expect(result.message).toContain("Nothing was deleted");
	});

	it("ignores refresh-models failures", async () => {
		const { api } = fakeApi({ failOn: (c) => (c[0] === "refreshModels" ? new Error("busy") : undefined) });
		const result = await runCloudflareMigration(keyedPlan(), api, () => {});
		expect(result).toMatchObject({ ok: true, partial: false });
	});
});