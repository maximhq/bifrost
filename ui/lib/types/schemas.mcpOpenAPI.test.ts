import { describe, expect, it } from "vitest";

import { bifrostUrlSchema, coreConfigSchema, mcpClientUpdateSchema, mcpOpenAPIConfigSchema, openAPIUrlSchema } from "./schemas";

describe("openapi_config schemas", () => {
	// An update merges onto the stored block, so it may carry only the fields
	// being changed, such as a rotated credential, with no spec source.
	it("accepts a credential-only openapi_config on update", () => {
		const result = mcpClientUpdateSchema.safeParse({
			name: "petstore",
			openapi_config: { security_credentials: { ApiKeyAuth: { value: { value: "rotated", ref: "" } } } },
		});
		expect(result.success, JSON.stringify(result.error?.issues)).toBe(true);
	});
	it("still requires a spec source on create", () => {
		const result = mcpOpenAPIConfigSchema.safeParse({ base_url: "https://api.example.com" });
		expect(result.success).toBe(false);
		expect(result.error?.issues[0]?.path).toEqual(["spec"]);
	});
	it("rejects URLs that only look like http(s)", () => {
		for (const bad of ["https://bad host", "https://", "http://a b/x"]) {
			expect(mcpOpenAPIConfigSchema.safeParse({ spec_url: bad }).success, bad).toBe(false);
			expect(mcpOpenAPIConfigSchema.safeParse({ spec: "x", base_url: bad }).success, bad).toBe(false);
		}
		expect(mcpOpenAPIConfigSchema.safeParse({ spec_url: "http://127.0.0.1:8080/openapi.json" }).success).toBe(true);
		expect(mcpOpenAPIConfigSchema.safeParse({ spec: "x", base_url: "https://api.example.com/v1" }).success).toBe(true);
	});
});

// The client settings form checks bifrost_url and openapi_url with the same rules as
// configstore.ValidatePublicBaseURL and configstore.ValidateOpenAPIURL.
describe("Bifrost MCP URL settings", () => {
	const plain = (value: string) => ({ value, ref: "", type: "plain_text" as const });

	it.each([
		["", true],
		["https://bifrost.example.com", true],
		["http://localhost:8080/bifrost", true],
		["bifrost.example.com", false],
		["ftp://bifrost.example.com", false],
		[" https://bifrost.example.com", false],
		["https://user:pass@bifrost.example.com", false],
		["https://bifrost.example.com/?x=1", false],
		["https://bifrost.example.com/?", false],
		["https://bifrost.example.com/#top", false],
		["https://bifrost.example.com/" + "a".repeat(512), false],
		["HTTPS://bifrost.example.com", true],
		["https://@bifrost.example.com", false],
		["https:bifrost.example.com", false],
		["https:///bifrost.example.com", false],
		["https:\\\\bifrost.example.com", false],
		["https://bifrost\\example.com", false],
		["https://bifrost.exa\nmple.com", false],
		["https://bifrost.example.com/" + "é".repeat(240), true],
		["https://bifrost.example.com/" + "é".repeat(250), false],
	])("bifrost_url %j accepted: %s", (value, ok) => {
		expect(bifrostUrlSchema.safeParse(plain(value)).success).toBe(ok);
	});

	it("leaves env and vault references to the server", () => {
		expect(bifrostUrlSchema.safeParse({ value: "", ref: "env.BIFROST_URL", type: "env" }).success).toBe(true);
		expect(bifrostUrlSchema.safeParse({ value: "", ref: "secret/bifrost#url", type: "vault" }).success).toBe(true);
	});

	it("is part of the client config schema", () => {
		expect(coreConfigSchema.shape.bifrost_url.safeParse(undefined).success).toBe(true);
		expect(coreConfigSchema.shape.bifrost_url.safeParse(plain("not a url")).success).toBe(false);
	});

	it.each([
		["", true],
		["  ", true],
		["https://mirror.internal/openapi.json", true],
		["specs/openapi.json", true],
		["./specs/../openapi.json", true],
		["specs/..", true],
		["/etc/bifrost/openapi.json", false],
		["../openapi.json", false],
		["specs/../../openapi.json", false],
		["..", false],
		["file:///etc/openapi.json", false],
		["https://", false],
		["specs/" + "a".repeat(2048), false],
		["https:///openapi.json", false],
		["https://@mirror.internal/openapi.json", true],
		["https://mirror\\internal/openapi.json", false],
		["https://mirror.inter\tnal/openapi.json", false],
		["specs/" + "é".repeat(1020), true],
		["specs/" + "é".repeat(1022), false],
	])("openapi_url %j accepted: %s", (value, ok) => {
		expect(openAPIUrlSchema.safeParse(value).success).toBe(ok);
	});
});