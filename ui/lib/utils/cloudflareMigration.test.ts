import { describe, expect, it } from "vitest";

import { ModelProvider, ModelProviderKey } from "@/lib/types/config";
import {
	buildCloudflareMigrationPlan,
	deriveCloudflareAccount,
	isCustomCloudflareProvider,
	isMigratableCloudflareProvider,
	MigrationPlan,
	planNeedsInput,
	runCloudflareMigration,
	toCloudflareKeyPayload,
} from "./cloudflareMigration";
import { MigrationApi } from "./databricksMigration";

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

	it("rejects AI Gateway, other paths and garbage", () => {
		for (const url of [
			`https://gateway.ai.cloudflare.com/v1/${ACCOUNT}/my-gateway/workers-ai`,
			`https://gateway.ai.cloudflare.com/v1/${ACCOUNT}/my-gateway/compat`,
			`https://api.cloudflare.com/client/v4/accounts/${ACCOUNT}/ai/run`,
			"https://api.cloudflare.com/client/v4",
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
	it("synthesizes one key from the Authorization header for a keyless source", () => {
		const plan = buildCloudflareMigrationPlan(custom("cloudflare"), []);
		expect(plan.nameClash).toBe(true);
		expect(plan.accountId).toBe(ACCOUNT);
		expect(plan.keys).toHaveLength(1);
		expect(plan.keys[0]).toMatchObject({ name: "cloudflare-token", fromHeader: true, needsValue: false, models: ["*"] });
		expect(plan.keys[0].value?.value).toBe("cf-token-1234567890");
		expect(planNeedsInput(plan)).toBe(false);
	});

	it("drops the base url and Authorization header from provider settings", () => {
		const plan = buildCloudflareMigrationPlan(
			custom("cloudflare", {
				network_config: { ...custom("x").network_config!, extra_headers: { Authorization: "Bearer t", "X-Team": "ml" } },
			}),
			[],
		);
		expect(plan.providerSettings.network_config.base_url).toBeUndefined();
		expect(plan.providerSettings.network_config.extra_headers).toEqual({ "X-Team": "ml" });
	});

	it("maps keyed sources, requiring re-entry for masked values and keeping env refs and aliases", () => {
		const plan = buildCloudflareMigrationPlan(custom("cloudflare", { custom_provider_config: { base_provider_type: "openai" } }), [
			key("k1", "prod"),
			key("k2", "backup", {
				value: { value: "", ref: "env.CLOUDFLARE_API_TOKEN", type: "env" },
				aliases: { clef: "@cf/cloudflare/clef" } as unknown as ModelProviderKey["aliases"],
			}),
		]);
		expect(plan.keys.map((k) => [k.name, k.needsValue])).toEqual([
			["prod", true],
			["backup", false],
		]);
		expect(plan.keys[1].aliases).toEqual({ clef: { model_id: "@cf/cloudflare/clef" } });
		expect(planNeedsInput(plan)).toBe(true);
	});

	it("asks for the account ID when it cannot be read, and needs it before migrating", () => {
		const plan = buildCloudflareMigrationPlan(
			custom("cloudflare", { network_config: { ...custom("x").network_config!, base_url: "" } }),
			[],
		);
		expect(plan.accountId).toBe("");
		expect(plan.warnings.join(" ")).toContain("account ID could not be read");
		expect(planNeedsInput(plan)).toBe(true);
		expect(planNeedsInput({ ...plan, accountId: ACCOUNT })).toBe(false);
	});

	it("builds a cloudflare key payload with the account ID on every key", () => {
		const plan = buildCloudflareMigrationPlan(custom("cloudflare"), []);
		const payload = toCloudflareKeyPayload({ ...plan, accountId: ` ${ACCOUNT} ` }, plan.keys[0]);
		expect(payload).toMatchObject({
			id: "",
			name: "cloudflare-token",
			models: ["*"],
			cloudflare_key_config: { account_id: { value: ACCOUNT, ref: "" } },
		});
	});
});

// ---------------------------------------------------------------------------
// Orchestrator
// ---------------------------------------------------------------------------

type Call = [string, ...unknown[]];

const fakeApi = (failOn?: (call: Call) => Error | undefined) => {
	const calls: Call[] = [];
	const keysByProvider = new Map<string, ModelProviderKey[]>();
	let nextId = 1;
	const record = async <T>(call: Call, result: () => T): Promise<T> => {
		calls.push(call);
		const err = failOn?.(call);
		if (err) throw err;
		return result();
	};
	const api: MigrationApi = {
		getProvider: (name) => record(["getProvider", name], () => ({ name, provider_status: "active" }) as ModelProvider),
		getProviderKeys: (name) => record(["getProviderKeys", name], () => keysByProvider.get(name) ?? []),
		createProvider: (body) =>
			record(["createProvider", body.provider], () => ({ name: body.provider, provider_status: "active" }) as ModelProvider),
		updateProvider: (name) => record(["updateProvider", name], () => ({ name, provider_status: "active" }) as ModelProvider),
		deleteProvider: (name) => record(["deleteProvider", name], () => undefined),
		createProviderKey: (provider, k) =>
			record(["createProviderKey", provider, k.name, k.cloudflare_key_config?.account_id.value], () => {
				const created = { ...k, id: `id-${nextId++}` };
				keysByProvider.set(provider, [...(keysByProvider.get(provider) ?? []), created]);
				return created;
			}),
		updateProviderKey: (provider, keyId, k) => record(["updateProviderKey", provider, keyId, k.name], () => ({ ...k, id: keyId })),
		deleteProviderKey: (provider, keyId) => record(["deleteProviderKey", provider, keyId], () => undefined),
		refreshModels: (provider) => record(["refreshModels", provider], () => undefined),
	};
	return { api, calls, names: () => calls.map((c) => c[0]) };
};

const clashPlan = (): MigrationPlan =>
	buildCloudflareMigrationPlan(custom("cloudflare", { custom_provider_config: { base_provider_type: "openai" } }), [
		key("k1", "prod", { value: { value: "", ref: "env.A", type: "env" } }),
	]);

describe("runCloudflareMigration", () => {
	it("deletes the custom provider first, then creates the official one with the account ID on its keys", async () => {
		const { api, names, calls } = fakeApi();
		const result = await runCloudflareMigration(clashPlan(), api, () => {});
		expect(result).toMatchObject({ ok: true, partial: false });
		expect(names()).toEqual([
			"deleteProvider",
			"createProvider",
			"updateProvider",
			"createProviderKey",
			"getProvider",
			"getProviderKeys",
			"refreshModels",
		]);
		expect(calls.find((c) => c[0] === "createProviderKey")).toEqual(["createProviderKey", "cloudflare", "prod", ACCOUNT]);
	});

	it("restores the custom provider from the snapshot when creating the official one fails", async () => {
		const { api, calls } = fakeApi((c) =>
			c[0] === "createProvider" && calls.filter((x) => x[0] === "createProvider").length === 1 ? new Error("down") : undefined,
		);
		const result = await runCloudflareMigration(clashPlan(), api, () => {});
		expect(result.ok).toBe(false);
		expect(calls.map((c) => c[0])).toEqual(["deleteProvider", "createProvider", "createProvider", "createProviderKey"]);
		// The restored key is the source key, without a Cloudflare key config.
		expect(calls.at(-1)).toEqual(["createProviderKey", "cloudflare", "prod", undefined]);
	});

	it("rolls back the created provider and restores the source when a key fails", async () => {
		const { api, names } = fakeApi((c) => (c[0] === "createProviderKey" && c[3] === ACCOUNT ? new Error("boom") : undefined));
		const result = await runCloudflareMigration(clashPlan(), api, () => {});
		expect(result.ok).toBe(false);
		expect(names()).toEqual([
			"deleteProvider",
			"createProvider",
			"updateProvider",
			"createProviderKey",
			"deleteProvider",
			"createProvider",
			"createProviderKey",
		]);
	});
});