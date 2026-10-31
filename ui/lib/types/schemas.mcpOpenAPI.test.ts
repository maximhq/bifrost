import { describe, expect, it } from "vitest";

import { mcpClientUpdateSchema, mcpOpenAPIConfigSchema } from "./schemas";

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