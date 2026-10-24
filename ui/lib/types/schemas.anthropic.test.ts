import { describe, expect, it } from "vitest";

import { anthropicKeyConfigSchema, anthropicWorkloadIdentityComplete, modelProviderKeySchema } from "./schemas";

const ids = {
	federation_rule_id: { value: "fdrl_01" },
	organization_id: { value: "00000000-0000-4000-8000-000000000000" },
	service_account_id: { value: "svac_01" },
};
const tokenFile = { value: "/var/run/secrets/anthropic/token" };
const token = { value: "", ref: "env.ANTHROPIC_IDENTITY_TOKEN", type: "env" as const };
const issues = (result: { success: boolean; error?: { issues: { path: PropertyKey[] }[] } }) =>
	result.success ? [] : (result.error?.issues.map((issue) => issue.path.join(".")) ?? []);
const messages = (result: { success: boolean; error?: { issues: { message: string }[] } }) =>
	result.success ? [] : (result.error?.issues.map((issue) => issue.message) ?? []);

describe("anthropicKeyConfigSchema", () => {
	it("accepts a complete block with a token file", () => {
		const result = anthropicKeyConfigSchema.safeParse({
			_auth_type: "workload_identity",
			workload_identity: { ...ids, identity_token_file: tokenFile, _token_source: "file" },
		});
		expect(result.success).toBe(true);
	});

	it("accepts a complete block with a referenced token and an optional workspace", () => {
		const result = anthropicKeyConfigSchema.safeParse({
			_auth_type: "workload_identity",
			workload_identity: { ...ids, workspace_id: { value: "wrkspc_01" }, identity_token: token, _token_source: "token" },
		});
		expect(result.success).toBe(true);
	});

	// The tab hides the API key field, so each missing ID must fail on its own input.
	it("requires the three IDs on their own fields", () => {
		const result = anthropicKeyConfigSchema.safeParse({
			_auth_type: "workload_identity",
			workload_identity: { identity_token_file: tokenFile, _token_source: "file" },
		});
		expect(issues(result)).toEqual([
			"workload_identity.federation_rule_id",
			"workload_identity.organization_id",
			"workload_identity.service_account_id",
		]);
		expect(messages(result)).toEqual(["Federation rule ID is required", "Organization ID is required", "Service account ID is required"]);
	});

	it("does not require a workspace ID", () => {
		const result = anthropicKeyConfigSchema.safeParse({
			_auth_type: "workload_identity",
			workload_identity: { ...ids, workspace_id: { value: "", ref: "" }, identity_token_file: tokenFile, _token_source: "file" },
		});
		expect(result.success).toBe(true);
	});

	it("requires the selected token source on its own field", () => {
		const file = anthropicKeyConfigSchema.safeParse({
			_auth_type: "workload_identity",
			workload_identity: { ...ids, identity_token_file: { value: "", ref: "" }, identity_token: token, _token_source: "file" },
		});
		expect(issues(file)).toEqual(["workload_identity.identity_token_file"]);
		const tok = anthropicKeyConfigSchema.safeParse({
			_auth_type: "workload_identity",
			workload_identity: { ...ids, identity_token_file: tokenFile, identity_token: { value: "", ref: "" }, _token_source: "token" },
		});
		expect(issues(tok)).toEqual(["workload_identity.identity_token"]);
	});

	// The unselected source is dropped on save, so a value left in it must not block the save.
	it("ignores the unselected token source", () => {
		const result = anthropicKeyConfigSchema.safeParse({
			_auth_type: "workload_identity",
			workload_identity: { ...ids, identity_token_file: tokenFile, identity_token: token, _token_source: "file" },
		});
		expect(result.success).toBe(true);
	});

	// A reset can leave the discriminators undefined; the server's exactly-one rule then applies.
	it("requires exactly one token source when the discriminators are absent", () => {
		expect(anthropicKeyConfigSchema.safeParse({ workload_identity: { ...ids, identity_token: token } }).success).toBe(true);
		expect(issues(anthropicKeyConfigSchema.safeParse({ workload_identity: { ...ids } }))).toEqual([
			"workload_identity.identity_token_file",
		]);
		expect(
			issues(anthropicKeyConfigSchema.safeParse({ workload_identity: { ...ids, identity_token_file: tokenFile, identity_token: token } })),
		).toEqual(["workload_identity.identity_token"]);
	});

	it("leaves an inert block alone on the API key method", () => {
		expect(anthropicKeyConfigSchema.safeParse({ _auth_type: "api_key" }).success).toBe(true);
		expect(anthropicKeyConfigSchema.safeParse({ _auth_type: "api_key", workload_identity: { _token_source: "file" } }).success).toBe(true);
	});
});

describe("anthropicWorkloadIdentityComplete", () => {
	it("is true only for the three IDs plus exactly one token source", () => {
		expect(anthropicWorkloadIdentityComplete({ ...ids, identity_token_file: tokenFile })).toBe(true);
		expect(anthropicWorkloadIdentityComplete({ ...ids, identity_token: token })).toBe(true);
		expect(anthropicWorkloadIdentityComplete({ ...ids })).toBe(false);
		expect(anthropicWorkloadIdentityComplete({ ...ids, identity_token_file: tokenFile, identity_token: token })).toBe(false);
		expect(
			anthropicWorkloadIdentityComplete({ ...ids, identity_token_file: tokenFile, identity_token: token, _token_source: "token" }),
		).toBe(true);
		expect(anthropicWorkloadIdentityComplete({ federation_rule_id: ids.federation_rule_id, identity_token_file: tokenFile })).toBe(false);
		expect(anthropicWorkloadIdentityComplete(undefined)).toBe(false);
	});
});

describe("modelProviderKeySchema with anthropic_key_config", () => {
	const base = { id: "k1", name: "wif", models: ["*"], weight: 1 };

	it("does not require a key value on the workload identity method", () => {
		const result = modelProviderKeySchema.safeParse({
			...base,
			value: { value: "" },
			anthropic_key_config: {
				_auth_type: "workload_identity",
				workload_identity: { ...ids, identity_token_file: tokenFile, _token_source: "file" },
			},
		});
		expect(result.success, JSON.stringify(result.success ? null : result.error.issues)).toBe(true);
	});

	it("infers the workload identity method from a complete block when the discriminator is absent", () => {
		const result = modelProviderKeySchema.safeParse({
			...base,
			value: { value: "" },
			anthropic_key_config: { workload_identity: { ...ids, identity_token: { value: "********" } } },
		});
		expect(result.success).toBe(true);
	});

	it("still requires a key value on the API key method", () => {
		const result = modelProviderKeySchema.safeParse({ ...base, value: { value: "" }, anthropic_key_config: { _auth_type: "api_key" } });
		expect(result.success).toBe(false);
		expect(issues(result)).toContain("value");
	});

	it("reports missing IDs under the key so the form shows them inline", () => {
		const result = modelProviderKeySchema.safeParse({
			...base,
			value: { value: "" },
			anthropic_key_config: { _auth_type: "workload_identity", workload_identity: { _token_source: "file" } },
		});
		expect(issues(result)).toEqual(
			expect.arrayContaining([
				"anthropic_key_config.workload_identity.federation_rule_id",
				"anthropic_key_config.workload_identity.organization_id",
				"anthropic_key_config.workload_identity.service_account_id",
				"anthropic_key_config.workload_identity.identity_token_file",
			]),
		);
		expect(issues(result)).not.toContain("value");
	});

	it("keeps the block through parsing so it reaches the API", () => {
		const result = modelProviderKeySchema.safeParse({
			...base,
			value: { value: "" },
			anthropic_key_config: {
				_auth_type: "workload_identity",
				workload_identity: { ...ids, identity_token_file: tokenFile, _token_source: "file" },
			},
		});
		expect(result.success).toBe(true);
		if (!result.success) return;
		expect(result.data.anthropic_key_config?.workload_identity?.identity_token_file).toEqual(tokenFile);
		expect(result.data.anthropic_key_config?.workload_identity?.service_account_id).toEqual(ids.service_account_id);
	});
});