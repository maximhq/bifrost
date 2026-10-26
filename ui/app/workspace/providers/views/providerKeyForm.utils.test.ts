import { describe, expect, it } from "vitest";

import { applyAuthMethod, stripDatabricksAuthDiscriminator, stripOAuthAuthDiscriminator } from "./providerKeyForm.utils";

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

describe("stripOAuthAuthDiscriminator", () => {
	const tokenUrl = { value: "https://idp.example.com/oauth2/token" };
	const cc = {
		grant_type: "client_credentials" as const,
		token_url: tokenUrl,
		client_id: { value: "id" },
		client_secret: { value: "secret" },
	};

	it("drops the whole block on the API key method", () => {
		expect(stripOAuthAuthDiscriminator({ _auth_type: "api_key", ...cc })).toBeUndefined();
	});

	it("never persists the UI-only discriminator", () => {
		const out = stripOAuthAuthDiscriminator({ _auth_type: "oauth", ...cc });
		expect(out).not.toHaveProperty("_auth_type");
		expect(out?.client_secret).toEqual({ value: "secret" });
	});

	// The discriminator is only seeded on mount and after the key resolves; an edit whose
	// discriminator went missing must not silently drop a valid OAuth key.
	it("keeps a complete block when the discriminator is absent", () => {
		expect(stripOAuthAuthDiscriminator(cc)?.client_id).toEqual({ value: "id" });
	});

	it("drops an incomplete block when the discriminator is absent", () => {
		expect(stripOAuthAuthDiscriminator({ grant_type: "client_credentials", token_url: tokenUrl })).toBeUndefined();
	});

	it("sends only the fields of the chosen grant", () => {
		const out = stripOAuthAuthDiscriminator({
			_auth_type: "oauth",
			...cc,
			private_key: { value: "pem" },
			issuer: "i",
			signing_algorithm: "RS256",
			assertion_lifetime_seconds: 300,
			scopes: [],
			audience: "",
		});
		expect(out).toEqual({
			grant_type: "client_credentials",
			token_url: tokenUrl,
			client_id: { value: "id" },
			client_secret: { value: "secret" },
		});

		const jwt = stripOAuthAuthDiscriminator({
			_auth_type: "oauth",
			grant_type: "jwt_bearer",
			token_url: tokenUrl,
			private_key: { value: "pem" },
			issuer: "i",
			audience: "a",
			client_id: { value: "id" },
			auth_style: "body",
		});
		expect(jwt).toEqual({ grant_type: "jwt_bearer", token_url: tokenUrl, private_key: { value: "pem" }, issuer: "i", audience: "a" });
	});
});

// The update API keeps every field the payload omits. Switching auth methods therefore has to
// say explicitly what it is clearing: an empty value on the OAuth path, a null section on the
// API-key path. Otherwise a stored static key survives the switch and, since resolveKey prefers
// it, keeps being sent until it expires.
describe("applyAuthMethod", () => {
	const tokenUrl = { value: "https://idp.example.com/oauth2/token" };
	const oauth = {
		_auth_type: "oauth" as const,
		grant_type: "client_credentials" as const,
		token_url: tokenUrl,
		client_id: { value: "id" },
		client_secret: { value: "secret" },
	};

	it("sends an explicit empty value on the OAuth path", () => {
		const out = applyAuthMethod({ name: "k", value: undefined, oauth_key_config: oauth }, true);
		expect(out.value).toEqual({ value: "" });
		expect(out.oauth_key_config).toEqual({
			grant_type: "client_credentials",
			token_url: tokenUrl,
			client_id: { value: "id" },
			client_secret: { value: "secret" },
		});
	});

	it("sends an explicit null section on the API-key path when editing", () => {
		const out = applyAuthMethod({ name: "k", value: { value: "sk" }, oauth_key_config: { ...oauth, _auth_type: "api_key" } }, true);
		expect(out.value).toEqual({ value: "sk" });
		expect(out.oauth_key_config).toBeNull();
	});

	it("omits the section on the API-key path when creating", () => {
		const out = applyAuthMethod({ name: "k", value: { value: "sk" }, oauth_key_config: { _auth_type: "api_key" } }, false);
		expect(out).not.toHaveProperty("oauth_key_config");
	});

	it("leaves a key without the section alone", () => {
		const out = applyAuthMethod({ name: "k", value: { value: "sk" } }, true);
		expect(out).toEqual({ name: "k", value: { value: "sk" } });
	});
});
// Every auth tab that does not use the static key has the same problem the OAuth tab solves
// above: the update API keeps an omitted value, so the tab switch has to send an explicit empty
// one or the stored token survives and the server keeps preferring it.
describe("applyAuthMethod clears the static key for the other providers' tabs", () => {
	const endpoint = { value: "https://res.openai.azure.com" };
	const cases: [string, string, Record<string, unknown>][] = [
		[
			"azure entra id",
			"azure_key_config",
			{ _auth_type: "entra_id", endpoint, client_id: { value: "c" }, client_secret: { value: "s" }, tenant_id: { value: "t" } },
		],
		["azure default credential", "azure_key_config", { _auth_type: "default_credential", endpoint }],
		["databricks oauth m2m", "databricks_key_config", { _auth_type: "oauth_m2m", workspace_url, ...pair }],
		["bedrock iam role", "bedrock_key_config", { _auth_type: "iam_role", region: { value: "us-east-1" } }],
		["bedrock explicit", "bedrock_key_config", { _auth_type: "explicit", access_key: { value: "a" }, secret_key: { value: "s" } }],
		["bedrock mantle iam role", "bedrock_mantle_key_config", { _auth_type: "iam_role", region: { value: "us-east-1" } }],
		["vertex service account", "vertex_key_config", { _auth_type: "service_account", project_id: { value: "p" } }],
	];

	it.each(cases)("sends an explicit empty value on the %s tab", (_name, section, block) => {
		const out = applyAuthMethod({ name: "k", value: undefined, [section]: block }, true);
		expect(out.value).toEqual({ value: "" });
		expect(out[section]).not.toHaveProperty("_auth_type");
	});

	it("keeps the value on the static-key tabs", () => {
		const azure = applyAuthMethod({ name: "k", value: { value: "sk" }, azure_key_config: { _auth_type: "api_key", endpoint } }, true);
		expect(azure.value).toEqual({ value: "sk" });
		expect(azure.azure_key_config).toEqual({ endpoint });
		const databricks = applyAuthMethod(
			{ name: "k", value: { value: "dapi" }, databricks_key_config: { _auth_type: "pat", workspace_url, ...pair } },
			true,
		);
		expect(databricks.value).toEqual({ value: "dapi" });
		expect(databricks.databricks_key_config).toEqual({ workspace_url });
	});

	// The discriminator is only seeded on mount and after the key resolves; with none, the form
	// has said nothing about the method and must not clear a key it did not touch.
	it("leaves the value alone when no method was chosen", () => {
		const out = applyAuthMethod({ name: "k", value: undefined, azure_key_config: { endpoint } }, true);
		expect(out.value).toBeUndefined();
	});
});