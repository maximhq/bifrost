import { describe, expect, it } from "vitest";

import { modelProviderKeySchema, oauthKeyConfigComplete, oauthKeyConfigSchema } from "./schemas";

const tokenUrl = { value: "https://idp.example.com/oauth2/token" };
const pair = { client_id: { value: "client-id" }, client_secret: { value: "client-secret" } };
const pem = "-----BEGIN EC PRIVATE KEY-----\nMHcCAQEEIBkz\n-----END EC PRIVATE KEY-----";
const issues = (result: { success: boolean; error?: { issues: { path: PropertyKey[] }[] } }) =>
	result.success ? [] : (result.error?.issues.map((issue) => issue.path.join(".")) ?? []);

describe("oauthKeyConfigSchema", () => {
	it("accepts a complete client credentials block", () => {
		const result = oauthKeyConfigSchema.safeParse({ _auth_type: "oauth", grant_type: "client_credentials", token_url: tokenUrl, ...pair });
		expect(result.success).toBe(true);
	});

	it("accepts a complete JWT bearer block with an EC key", () => {
		const result = oauthKeyConfigSchema.safeParse({
			_auth_type: "oauth",
			grant_type: "jwt_bearer",
			token_url: tokenUrl,
			private_key: { value: pem },
			issuer: "svc",
			audience: "https://idp.example.com/oauth2/token",
			signing_algorithm: "ES256",
		});
		expect(result.success).toBe(true);
	});

	// The OAuth tab hides the API key field, so a block saved from it must be whole or the
	// key has no credentials at all.
	it("requires the client pair once the OAuth method is selected", () => {
		const result = oauthKeyConfigSchema.safeParse({ _auth_type: "oauth", grant_type: "client_credentials", token_url: tokenUrl });
		expect(result.success).toBe(false);
		expect(issues(result)).toEqual(expect.arrayContaining(["client_id", "client_secret"]));
	});

	it("requires key, issuer and audience for JWT bearer", () => {
		const result = oauthKeyConfigSchema.safeParse({ _auth_type: "oauth", grant_type: "jwt_bearer", token_url: tokenUrl });
		expect(issues(result)).toEqual(expect.arrayContaining(["private_key", "issuer", "audience"]));
	});

	it("rejects a plain-http token URL but allows loopback", () => {
		expect(
			oauthKeyConfigSchema.safeParse({
				_auth_type: "oauth",
				grant_type: "client_credentials",
				token_url: { value: "http://idp.example/token" },
				...pair,
			}).success,
		).toBe(false);
		expect(
			oauthKeyConfigSchema.safeParse({
				_auth_type: "oauth",
				grant_type: "client_credentials",
				token_url: { value: "http://127.0.0.1:9/token" },
				...pair,
			}).success,
		).toBe(true);
	});

	// The server accepts plain http for any loopback address; the form must not be stricter, or
	// a local identity provider on 127.0.0.2 saves through the API but not through the UI.
	it("accepts plain http for every loopback address the server accepts", () => {
		for (const url of ["http://127.0.0.2/token", "http://127.1.2.3:8080/token", "http://localhost:9/token", "http://[::1]:9/token"]) {
			const result = oauthKeyConfigSchema.safeParse({
				_auth_type: "oauth",
				grant_type: "client_credentials",
				token_url: { value: url },
				...pair,
			});
			expect(result.success, url).toBe(true);
		}
		for (const url of ["http://10.0.0.5/token", "http://192.168.1.1/token", "http://idp.example/token"]) {
			expect(
				oauthKeyConfigSchema.safeParse({ _auth_type: "oauth", grant_type: "client_credentials", token_url: { value: url }, ...pair })
					.success,
				url,
			).toBe(false);
		}
	});

	it("rejects a literal private key that is not a PEM block", () => {
		const result = oauthKeyConfigSchema.safeParse({
			_auth_type: "oauth",
			grant_type: "jwt_bearer",
			token_url: tokenUrl,
			private_key: { value: "not a key" },
			issuer: "svc",
			audience: "aud",
		});
		expect(issues(result)).toContain("private_key");
	});

	it("does not shape-check references or masked values", () => {
		const result = oauthKeyConfigSchema.safeParse({
			_auth_type: "oauth",
			grant_type: "jwt_bearer",
			token_url: { value: "", ref: "env.IDP_TOKEN_URL", type: "env" },
			private_key: { value: "", ref: "env.IDP_KEY", type: "env" },
			issuer: "svc",
			audience: "aud",
		});
		expect(result.success).toBe(true);
	});

	// The lifetime field is hidden once the user leaves the JWT tab, so a value left in it must
	// not block saving: 0 means "default" (as on the backend) and the range only applies while
	// JWT bearer is the active grant.
	it("does not let a hidden lifetime block the save", () => {
		expect(oauthKeyConfigSchema.safeParse({ _auth_type: "api_key", assertion_lifetime_seconds: 0 }).success).toBe(true);
		expect(oauthKeyConfigSchema.safeParse({ _auth_type: "api_key", assertion_lifetime_seconds: 5000 }).success).toBe(true);
		expect(
			oauthKeyConfigSchema.safeParse({
				_auth_type: "oauth",
				grant_type: "client_credentials",
				token_url: tokenUrl,
				...pair,
				assertion_lifetime_seconds: 5000,
			}).success,
		).toBe(true);
		const jwt = (lifetime: number) =>
			oauthKeyConfigSchema.safeParse({
				_auth_type: "oauth",
				grant_type: "jwt_bearer",
				token_url: tokenUrl,
				private_key: { value: pem },
				issuer: "svc",
				audience: "aud",
				assertion_lifetime_seconds: lifetime,
			});
		expect(jwt(0).success).toBe(true);
		expect(jwt(300).success).toBe(true);
		expect(issues(jwt(5000))).toContain("assertion_lifetime_seconds");
	});

	it("leaves an inert block alone on the API key method", () => {
		const result = oauthKeyConfigSchema.safeParse({ _auth_type: "api_key" });
		expect(result.success).toBe(true);
	});
});

describe("oauthKeyConfigComplete", () => {
	it("is true only for a whole block of its grant", () => {
		expect(oauthKeyConfigComplete({ grant_type: "client_credentials", token_url: tokenUrl, ...pair })).toBe(true);
		expect(oauthKeyConfigComplete({ grant_type: "client_credentials", token_url: tokenUrl, client_id: pair.client_id })).toBe(false);
		expect(
			oauthKeyConfigComplete({ grant_type: "jwt_bearer", token_url: tokenUrl, private_key: { value: pem }, issuer: "i", audience: "a" }),
		).toBe(true);
		expect(oauthKeyConfigComplete({ grant_type: "jwt_bearer", token_url: tokenUrl, private_key: { value: pem }, issuer: "i" })).toBe(false);
		expect(oauthKeyConfigComplete(undefined)).toBe(false);
	});
});

describe("modelProviderKeySchema with oauth_key_config", () => {
	const base = { id: "k1", name: "oauth", models: ["*"], weight: 1 };

	it("does not require a key value on the OAuth method", () => {
		const result = modelProviderKeySchema.safeParse({
			...base,
			value: { value: "" },
			oauth_key_config: { _auth_type: "oauth", grant_type: "client_credentials", token_url: tokenUrl, ...pair },
		});
		expect(result.success, JSON.stringify(result.success ? null : result.error.issues)).toBe(true);
	});

	// The discriminator is only seeded on mount and after the key resolves; a reset can leave
	// it undefined on a perfectly valid OAuth key.
	it("infers the OAuth method from a complete block when the discriminator is absent", () => {
		const result = modelProviderKeySchema.safeParse({
			...base,
			value: { value: "" },
			oauth_key_config: { grant_type: "client_credentials", token_url: tokenUrl, ...pair },
		});
		expect(result.success).toBe(true);
	});

	it("still requires a key value on the API key method", () => {
		const result = modelProviderKeySchema.safeParse({ ...base, value: { value: "" }, oauth_key_config: { _auth_type: "api_key" } });
		expect(result.success).toBe(false);
		expect(issues(result)).toContain("value");
	});

	it("keeps the block through parsing so it reaches the API", () => {
		const result = modelProviderKeySchema.safeParse({
			...base,
			value: { value: "" },
			oauth_key_config: { _auth_type: "oauth", grant_type: "client_credentials", token_url: tokenUrl, ...pair, scopes: ["inference"] },
		});
		expect(result.success).toBe(true);
		if (!result.success) return;
		expect(result.data.oauth_key_config?.client_secret?.value).toBe("client-secret");
		expect(result.data.oauth_key_config?.scopes).toEqual(["inference"]);
	});
});