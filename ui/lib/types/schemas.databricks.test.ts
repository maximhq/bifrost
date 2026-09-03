import { describe, expect, it } from "vitest";

import { databricksKeyConfigSchema } from "./schemas";

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
