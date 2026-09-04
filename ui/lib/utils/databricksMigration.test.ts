import { describe, expect, it } from "vitest";

import { ModelProvider, ModelProviderKey } from "@/lib/types/config";
import {
	buildDatabricksMigrationPlan,
	buildMigrationSteps,
	deriveDatabricksWorkspace,
	isCustomDatabricksProvider,
	extractAuthFromHeaders,
	maskSecret,
	MigrationApi,
	MigrationPlan,
	planNeedsInput,
	runDatabricksMigration,
	toDatabricksKeyPayload,
} from "./databricksMigration";

const REDACTED = "dapi************************abcd";

const custom = (name: string, overrides: Partial<ModelProvider> = {}): ModelProvider =>
	({
		name,
		provider_status: "active",
		custom_provider_config: { base_provider_type: "openai", is_key_less: true },
		network_config: {
			base_url: "https://adb-123.4.azuredatabricks.net/serving-endpoints",
			extra_headers: { Authorization: "Bearer dapi1234567890secret" },
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
	models: ["a", "b"],
	blacklisted_models: [],
	weight: 0.5,
	enabled: true,
	...overrides,
});

describe("isCustomDatabricksProvider", () => {
	it("matches only a custom provider named exactly databricks", () => {
		expect(isCustomDatabricksProvider(custom("databricks"))).toBe(true);
		expect(isCustomDatabricksProvider(custom("Databricks "))).toBe(true);
		expect(isCustomDatabricksProvider(custom("databricks-bc"))).toBe(false);
		expect(isCustomDatabricksProvider(custom("my-dbx"))).toBe(false);
		expect(isCustomDatabricksProvider({ ...custom("databricks"), custom_provider_config: undefined })).toBe(false);
	});
});

describe("deriveDatabricksWorkspace", () => {
	it("derives the surface from the path", () => {
		expect(deriveDatabricksWorkspace("https://h.cloud.databricks.com/serving-endpoints/")).toMatchObject({
			workspaceUrl: "https://h.cloud.databricks.com",
			apiFormat: "model_serving",
		});
		expect(deriveDatabricksWorkspace("https://h.cloud.databricks.com/ai-gateway/mlflow/v1").apiFormat).toBe("ai_gateway");
		expect(deriveDatabricksWorkspace("https://h.cloud.databricks.com").apiFormat).toBe("auto");
	});

	it("warns on unknown paths and http, and keeps ports", () => {
		const unknown = deriveDatabricksWorkspace("http://h.cloud.databricks.com:8443/v1");
		expect(unknown.workspaceUrl).toBe("https://h.cloud.databricks.com:8443");
		expect(unknown.apiFormat).toBe("auto");
		expect(unknown.warnings).toHaveLength(2);
	});

	it("accepts a bare host and rejects garbage", () => {
		expect(deriveDatabricksWorkspace("h.cloud.databricks.com/serving-endpoints").apiFormat).toBe("model_serving");
		expect(deriveDatabricksWorkspace("").workspaceUrl).toBe("");
	});
});

describe("extractAuthFromHeaders", () => {
	it("reads bearer tokens case-insensitively", () => {
		expect(extractAuthFromHeaders({ authorization: "bearer  tok" }).token).toEqual({ value: "tok", ref: "", type: undefined });
		expect(extractAuthFromHeaders({ Authorization: "Bearer env.DBX" }).token).toMatchObject({ ref: "env.DBX", type: "env" });
		expect(extractAuthFromHeaders({ Authorization: "vault.secret/dbx" }).token).toMatchObject({ ref: "vault.secret/dbx", type: "vault" });
	});

	it("reports missing or non-bearer headers", () => {
		expect(extractAuthFromHeaders(undefined).problem).toBe("missing");
		expect(extractAuthFromHeaders({ "X-Api-Key": "x" }).problem).toBe("missing");
		expect(extractAuthFromHeaders({ Authorization: "Basic abc" }).problem).toBe("not_bearer");
	});
});

describe("maskSecret", () => {
	it("masks literals and shows refs", () => {
		expect(maskSecret({ value: "dapi1234567890secret", ref: "" })).toBe("dapi****cret");
		expect(maskSecret({ value: "short", ref: "" })).toBe("****");
		expect(maskSecret({ value: "", ref: "env.X" })).toBe("env.X");
	});
});

describe("buildDatabricksMigrationPlan", () => {
	it("synthesizes one key from the header for a keyless source", () => {
		const plan = buildDatabricksMigrationPlan(custom("my-dbx"), []);
		expect(plan.keys).toHaveLength(1);
		expect(plan.keys[0]).toMatchObject({
			name: "my-dbx-token",
			createName: "my-dbx-token",
			fromHeader: true,
			needsValue: false,
			models: ["*"],
		});
		expect(plan.keys[0].value).toMatchObject({ value: "dapi1234567890secret" });
		expect(plan.workspaceUrl).toBe("https://adb-123.4.azuredatabricks.net");
		expect(plan.apiFormat).toBe("model_serving");
		expect(plan.nameClash).toBe(false);
		expect(planNeedsInput(plan)).toBe(false);
	});

	it("strips the Authorization header and base url from provider settings", () => {
		const source = custom("my-dbx", {
			network_config: {
				...custom("x").network_config!,
				extra_headers: { Authorization: "Bearer t", "X-Trace": "1" },
			},
			proxy_config: {
				type: "http",
				url: { value: "", ref: "env.PROXY" },
				password: { value: "<REDACTED>", ref: "" },
			},
		});
		const plan = buildDatabricksMigrationPlan(source, []);
		expect(plan.providerSettings.network_config.extra_headers).toEqual({
			"X-Trace": "1",
		});
		expect(plan.providerSettings.network_config.base_url).toBeUndefined();
		expect(plan.providerSettings.network_config.default_request_timeout_in_seconds).toBe(60);
		expect(plan.providerSettings.concurrency_and_buffer_size).toEqual({
			concurrency: 10,
			buffer_size: 20,
		});
		expect(plan.providerSettings.proxy_config).toEqual({
			type: "http",
			url: { value: "", ref: "env.PROXY" },
		});
		expect(plan.warnings.some((w) => w.includes("proxy password"))).toBe(true);
	});

	it("maps keyed sources, requiring re-entry for masked values and keeping refs", () => {
		const keys = [
			key("k1", "prod", { aliases: { alias: "real-model" } as never }),
			key("k2", "ref", { value: { value: "", ref: "env.DBX", type: "env" } }),
		];
		const plan = buildDatabricksMigrationPlan(
			custom("my-dbx", {
				custom_provider_config: { base_provider_type: "openai" },
			}),
			keys,
		);
		expect(plan.source.isKeyless).toBe(false);
		expect(plan.keys.map((k) => k.createName)).toEqual(["prod-migrating", "ref-migrating"]);
		expect(plan.keys[0].needsValue).toBe(true);
		expect(plan.keys[0].aliases).toEqual({ alias: { model_id: "real-model" } });
		expect(plan.keys[1].needsValue).toBe(false);
		expect(plan.keys[1].value).toMatchObject({ ref: "env.DBX", type: "env" });
		expect(plan.warnings.some((w) => w.includes("Authorization header"))).toBe(true);
		expect(planNeedsInput(plan)).toBe(true);
	});

	it("flags a name clash and does not suffix keys on that path", () => {
		const plan = buildDatabricksMigrationPlan(
			custom("databricks", {
				custom_provider_config: { base_provider_type: "openai" },
			}),
			[key("k1", "prod")],
		);
		expect(plan.nameClash).toBe(true);
		expect(plan.keys[0].createName).toBe("prod");
		expect(buildMigrationSteps(plan).map((s) => s.id)[1]).toBe("delete-source");
	});

	it("keeps a permanent suffix when the existing target already has the key name", () => {
		const target = {
			provider: custom("databricks", { custom_provider_config: undefined }),
			keys: [key("t1", "prod")],
		};
		const plan = buildDatabricksMigrationPlan(
			custom("my-dbx", {
				custom_provider_config: { base_provider_type: "openai" },
			}),
			[key("k1", "prod")],
			target,
		);
		expect(plan.targetExists).toBe(true);
		expect(plan.keys[0]).toMatchObject({
			name: "prod-migrating",
			createName: "prod-migrating",
		});
		expect(buildMigrationSteps(plan).some((s) => s.id === "rename-keys")).toBe(false);
	});

	it("warns about non-portable custom settings and config.json sources", () => {
		const plan = buildDatabricksMigrationPlan(
			custom("my-dbx", {
				config_hash: "abc",
				custom_provider_config: {
					base_provider_type: "openai",
					is_key_less: true,
					allowed_requests: {
						chat_completion: true,
						embedding: false,
					} as never,
					request_path_overrides: { chat_completion: "/x" },
				},
			}),
			[],
		);
		expect(plan.warnings.filter((w) => /Allowed request|path overrides|config\.json/.test(w))).toHaveLength(3);
	});

	it("enables migration once a masked key gets a value, and strips paths from an edited workspace url", () => {
		const plan = buildDatabricksMigrationPlan(
			custom("my-dbx", { custom_provider_config: { base_provider_type: "openai" } }),
			[key("k1", "prod")],
		);
		expect(planNeedsInput(plan)).toBe(true);
		const filled: MigrationPlan = {
			...plan,
			workspaceUrl: "https://dbc-ba56e0d8-0771.cloud.databricks.com/some_path/?x=1",
			keys: plan.keys.map((k) => ({ ...k, value: { value: "dapi-new-token-value", ref: "" } })),
		};
		expect(planNeedsInput(filled)).toBe(false);
		expect(toDatabricksKeyPayload(filled, filled.keys[0]).databricks_key_config?.workspace_url.value).toBe(
			"https://dbc-ba56e0d8-0771.cloud.databricks.com",
		);
		expect(planNeedsInput({ ...filled, workspaceUrl: "not a url" })).toBe(true);
	});

	it("builds a databricks key payload with the workspace on every key", () => {
		const plan = buildDatabricksMigrationPlan(custom("my-dbx"), []);
		expect(toDatabricksKeyPayload(plan, plan.keys[0])).toMatchObject({
			name: "my-dbx-token",
			databricks_key_config: {
				workspace_url: {
					value: "https://adb-123.4.azuredatabricks.net",
					ref: "",
				},
				api_format: "model_serving",
			},
		});
	});
});

// ---------------------------------------------------------------------------
// Orchestrator
// ---------------------------------------------------------------------------

type Call = [string, ...unknown[]];

interface FakeOptions {
	failOn?: (call: Call) => Error | undefined;
	providerStatus?: string;
}

const fakeApi = (opts: FakeOptions = {}) => {
	const calls: Call[] = [];
	const keysByProvider = new Map<string, ModelProviderKey[]>();
	let nextId = 1;
	const record = async <T>(call: Call, result: () => T): Promise<T> => {
		calls.push(call);
		const err = opts.failOn?.(call);
		if (err) throw err;
		return result();
	};
	const api: MigrationApi = {
		getProvider: (name) =>
			record(
				["getProvider", name],
				() =>
					({
						name,
						provider_status: opts.providerStatus ?? "active",
					}) as ModelProvider,
			),
		getProviderKeys: (name) => record(["getProviderKeys", name], () => keysByProvider.get(name) ?? []),
		createProvider: (body) => record(["createProvider", body.provider], () => ({ name: body.provider, provider_status: "active" }) as ModelProvider),
		updateProvider: (name) => record(["updateProvider", name], () => ({ name, provider_status: "active" }) as ModelProvider),
		deleteProvider: (name) => record(["deleteProvider", name], () => undefined),
		createProviderKey: (provider, k) =>
			record(["createProviderKey", provider, k.name], () => {
				const created = { ...k, id: `id-${nextId++}` };
				keysByProvider.set(provider, [...(keysByProvider.get(provider) ?? []), created]);
				return created;
			}),
		updateProviderKey: (provider, keyId, k) =>
			record(["updateProviderKey", provider, keyId, k.name], () => ({
				...k,
				id: keyId,
			})),
		deleteProviderKey: (provider, keyId) => record(["deleteProviderKey", provider, keyId], () => undefined),
		refreshModels: (provider) => record(["refreshModels", provider], () => undefined),
	};
	return { api, calls, names: () => calls.map((c) => c[0]) };
};

const withStatus = (message: string, status: number) => Object.assign(new Error(message), { status });

const keyedPlan = (name = "my-dbx", existing?: Parameters<typeof buildDatabricksMigrationPlan>[2]): MigrationPlan =>
	buildDatabricksMigrationPlan(
		custom(name, { custom_provider_config: { base_provider_type: "openai" } }),
		[
			key("k1", "prod", { value: { value: "", ref: "env.A", type: "env" } }),
			key("k2", "backup", { value: { value: "", ref: "env.B", type: "env" } }),
		],
		existing,
	);

describe("runDatabricksMigration", () => {
	it("runs the normal path in order and only deletes the source after verification", async () => {
		const { api, names, calls } = fakeApi();
		const result = await runDatabricksMigration(keyedPlan(), api, () => {});
		expect(result).toMatchObject({ ok: true, partial: false });
		expect(names()).toEqual([
			"createProvider",
			"updateProvider",
			"createProviderKey",
			"createProviderKey",
			"getProvider",
			"getProviderKeys",
			"deleteProvider",
			"updateProviderKey",
			"updateProviderKey",
			"refreshModels",
		]);
		expect(calls.find((c) => c[0] === "deleteProvider")?.[1]).toBe("my-dbx");
		expect(calls.filter((c) => c[0] === "updateProviderKey").map((c) => c[3])).toEqual(["prod", "backup"]);
	});

	it("rolls back created keys and the provider when a key fails, leaving the source untouched", async () => {
		const { api, names, calls } = fakeApi({
			failOn: (c) => (c[0] === "createProviderKey" && c[2] === "backup-migrating" ? new Error("boom") : undefined),
		});
		const steps: string[] = [];
		const result = await runDatabricksMigration(keyedPlan(), api, (s) => steps.push(s.map((x) => x.status).join(",")));
		expect(result.ok).toBe(false);
		expect(names()).toEqual(["createProvider", "updateProvider", "createProviderKey", "createProviderKey", "deleteProviderKey", "deleteProvider"]);
		expect(calls.find((c) => c[0] === "deleteProvider")?.[1]).toBe("databricks");
		expect(steps.at(-1)).toContain("failed");
	});

	it("skips create and settings when the target already exists", async () => {
		const target = {
			provider: custom("databricks", { custom_provider_config: undefined }),
			keys: [],
		};
		const { api, names } = fakeApi();
		const result = await runDatabricksMigration(keyedPlan("my-dbx", target), api, () => {});
		expect(result.ok).toBe(true);
		expect(names()).not.toContain("createProvider");
		expect(names()).not.toContain("updateProvider");
	});

	it("treats a 409 on create as an existing target", async () => {
		const { api, names } = fakeApi({
			failOn: (c) => (c[0] === "createProvider" ? withStatus("exists", 409) : undefined),
		});
		const result = await runDatabricksMigration(keyedPlan(), api, () => {});
		expect(result.ok).toBe(true);
		expect(names()).not.toContain("updateProvider");
	});

	it("deletes the source first on the name-clash path and restores it if create fails", async () => {
		const { api, names, calls } = fakeApi({
			failOn: (c) => (c[0] === "createProvider" && c[1] === "databricks" ? new Error("down") : undefined),
		});
		const result = await runDatabricksMigration(keyedPlan("databricks"), api, () => {});
		expect(result.ok).toBe(false);
		expect(names().slice(0, 2)).toEqual(["deleteProvider", "createProvider"]);
		const restores = calls.filter((c) => c[0] === "createProvider" || c[0] === "createProviderKey");
		expect(restores.map((c) => c[1])).toEqual(["databricks", "databricks", "databricks", "databricks"]);
		expect(calls.filter((c) => c[0] === "createProviderKey").map((c) => c[2])).toEqual(["prod", "backup"]);
	});

	it("reports partial success when the source cannot be deleted", async () => {
		const { api, names } = fakeApi({
			failOn: (c) => (c[0] === "deleteProvider" ? new Error("locked") : undefined),
		});
		const result = await runDatabricksMigration(keyedPlan(), api, () => {});
		expect(result).toMatchObject({ ok: true, partial: true });
		expect(result.message).toContain("Delete it manually");
		expect(names()).not.toContain("updateProviderKey");
	});

	it("ignores refresh-models failures", async () => {
		const { api } = fakeApi({
			failOn: (c) => (c[0] === "refreshModels" ? withStatus("busy", 409) : undefined),
		});
		const result = await runDatabricksMigration(keyedPlan(), api, () => {});
		expect(result).toMatchObject({ ok: true, partial: false });
	});

	it("fails verification when the provider is not active", async () => {
		const { api, names } = fakeApi({ providerStatus: "error" });
		const result = await runDatabricksMigration(keyedPlan(), api, () => {});
		expect(result.ok).toBe(false);
		expect(names()).toContain("deleteProviderKey");
		expect(names().filter((n) => n === "deleteProvider")).toHaveLength(1);
	});
});
