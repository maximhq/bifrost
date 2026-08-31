import { describe, expect, it } from "vitest";

import { databricksKeyConfigSchema, modelProviderKeySchema } from "./schemas";

const workspaceUrl = { value: "https://dbc-1234abcd-5678.cloud.databricks.com" };

describe("databricksKeyConfigSchema", () => {
	// The OAuth M2M tab hides the token field, so a key saved from it with no service
	// principal has no credentials at all. Mirror the Azure Entra rule: the chosen
	// auth method makes its own fields mandatory.
	it("requires both client credentials when the OAuth M2M method is selected", () => {
		const result = databricksKeyConfigSchema.safeParse({ workspace_url: workspaceUrl, _auth_type: "oauth_m2m" });
		expect(result.success).toBe(false);
		expect(result.error?.issues.map((issue) => issue.path.join("."))).toContain("client_id");
	});

	it("accepts the OAuth M2M method with a complete service principal", () => {
		const result = databricksKeyConfigSchema.safeParse({
			workspace_url: workspaceUrl,
			_auth_type: "oauth_m2m",
			client_id: { value: "sp-id" },
			client_secret: { value: "sp-secret" },
		});
		expect(result.success).toBe(true);
	});

	it("accepts the token method with no service principal", () => {
		const result = databricksKeyConfigSchema.safeParse({ workspace_url: workspaceUrl, _auth_type: "pat" });
		expect(result.success).toBe(true);
	});

	it("still rejects half a service principal on any method", () => {
		const result = databricksKeyConfigSchema.safeParse({ workspace_url: workspaceUrl, client_id: { value: "sp-id" } });
		expect(result.success).toBe(false);
	});
});

const base = { id: "k1", name: "dbx", models: ["*"], blacklisted_models: [], weight: 1 };
const workspace = { value: "https://dbc-1.cloud.databricks.com", ref: "" };
const secret = (value: string) => ({ value, ref: "" });

describe("modelProviderKeySchema, databricks", () => {
	it("accepts an OAuth M2M key whose _auth_type was lost to a form reset", () => {
		// The key form resets once the key resolves, wiping the UI-only discriminator.
		// A service principal is still a complete credential without it, so requiring a
		// personal access token here would block saving a valid key.
		const result = modelProviderKeySchema.safeParse({
			...base,
			databricks_key_config: {
				workspace_url: workspace,
				client_id: secret("client-id"),
				client_secret: secret("client-secret"),
			},
		});
		expect(result.success).toBe(true);
	});

	it("accepts an OAuth M2M key with the discriminator present", () => {
		const result = modelProviderKeySchema.safeParse({
			...base,
			databricks_key_config: {
				workspace_url: workspace,
				client_id: secret("client-id"),
				client_secret: secret("client-secret"),
				_auth_type: "oauth_m2m",
			},
		});
		expect(result.success).toBe(true);
	});

	it("accepts a personal access token key", () => {
		const result = modelProviderKeySchema.safeParse({
			...base,
			value: secret("dapi-token"),
			databricks_key_config: { workspace_url: workspace, _auth_type: "pat" },
		});
		expect(result.success).toBe(true);
	});

	it("rejects an explicit token method with no token even when a service principal is present", () => {
		// The credential-based inference only applies when the discriminator is absent;
		// a user who picked the token tab must still supply a token.
		const result = modelProviderKeySchema.safeParse({
			...base,
			databricks_key_config: {
				workspace_url: workspace,
				client_id: secret("client-id"),
				client_secret: secret("client-secret"),
				_auth_type: "pat",
			},
		});
		expect(result.success).toBe(false);
	});

	it("rejects a key with neither a token nor a service principal", () => {
		const result = modelProviderKeySchema.safeParse({
			...base,
			databricks_key_config: { workspace_url: workspace },
		});
		expect(result.success).toBe(false);
	});

	it("rejects half a service principal", () => {
		const result = modelProviderKeySchema.safeParse({
			...base,
			databricks_key_config: { workspace_url: workspace, client_id: secret("client-id") },
		});
		expect(result.success).toBe(false);
	});

	it("rejects a missing workspace URL", () => {
		const result = modelProviderKeySchema.safeParse({
			...base,
			value: secret("dapi-token"),
			databricks_key_config: { workspace_url: secret("") },
		});
		expect(result.success).toBe(false);
	});
});
