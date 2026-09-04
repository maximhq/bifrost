import { describe, expect, it } from "vitest";

import { stripDatabricksAuthDiscriminator } from "./providerKeyForm.utils";

const pair = { client_id: { value: "sp-id" }, client_secret: { value: "sp-secret" } };
const workspace_url = { value: "https://dbc-1234abcd-5678.cloud.databricks.com" };

describe("stripDatabricksAuthDiscriminator", () => {
	it("never persists the UI-only discriminator", () => {
		const out = stripDatabricksAuthDiscriminator({ workspace_url, _auth_type: "pat" });
		expect(out).not.toHaveProperty("_auth_type");
	});

	it("drops the service principal when the token method was chosen explicitly", () => {
		const out = stripDatabricksAuthDiscriminator({ workspace_url, _auth_type: "pat", ...pair });
		expect(out.client_id).toBeUndefined();
		expect(out.client_secret).toBeUndefined();
	});

	it("keeps the service principal when the OAuth M2M method was chosen", () => {
		const out = stripDatabricksAuthDiscriminator({ workspace_url, _auth_type: "oauth_m2m", ...pair });
		expect(out.client_id).toEqual(pair.client_id);
		expect(out.client_secret).toEqual(pair.client_secret);
	});

	// The discriminator is only seeded on mount and after the key resolves; an edit whose
	// discriminator went missing must not silently strip a valid M2M key's credentials.
	it("keeps a complete service principal when the discriminator is absent", () => {
		const out = stripDatabricksAuthDiscriminator({ workspace_url, ...pair });
		expect(out.client_id).toEqual(pair.client_id);
		expect(out.client_secret).toEqual(pair.client_secret);
	});

	it("drops an incomplete service principal when the discriminator is absent", () => {
		const out = stripDatabricksAuthDiscriminator({ workspace_url, client_id: pair.client_id });
		expect(out.client_id).toBeUndefined();
		expect(out.client_secret).toBeUndefined();
	});
});
