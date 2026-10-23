import { describe, expect, it } from "vitest";
import { authFromSecuritySchemes, baseUrlFromServers, importOpenAPI, parseOpenAPIText, templateToGlob } from "./openapiImport";

const petstore = {
	openapi: "3.0.3",
	info: { title: "Petstore", version: "1.0.0" },
	servers: [{ url: "https://{region}.petstore.example.com/v1" }, { url: "https://petstore.example.com/v1/" }],
	components: { securitySchemes: { ApiKey: { type: "apiKey", in: "header", name: "X-Api-Key" } } },
	paths: {
		"/pets": { get: {}, post: {}, options: {} },
		"/pets/{petId}": { get: {}, delete: {}, parameters: [] },
		"/pets/{petId}/photo": { post: {} },
	},
};

describe("openapiImport", () => {
	it("turns operations into routes with templated segments as globs", () => {
		const result = importOpenAPI(petstore);
		expect(result.title).toBe("Petstore");
		expect(result.routes).toEqual([
			{ method: "GET", path: "/pets" },
			{ method: "POST", path: "/pets" },
			{ method: "GET", path: "/pets/*" },
			{ method: "DELETE", path: "/pets/*" },
			{ method: "POST", path: "/pets/*/photo" },
		]);
		expect(result.warnings).toEqual([]);
	});

	it("skips templated servers and strips a trailing slash", () => {
		expect(baseUrlFromServers(petstore)).toBe("https://petstore.example.com/v1");
		expect(baseUrlFromServers({ host: "api.example.com", basePath: "/v2", schemes: ["http"] })).toBe("http://api.example.com/v2");
		expect(baseUrlFromServers({ paths: {} })).toBeUndefined();
	});

	it("maps security schemes onto relay auth types", () => {
		expect(authFromSecuritySchemes(petstore).auth).toEqual({ type: "header", header_name: "X-Api-Key" });
		expect(authFromSecuritySchemes({ components: { securitySchemes: { b: { type: "http", scheme: "bearer" } } } }).auth).toEqual({
			type: "bearer",
		});
		expect(authFromSecuritySchemes({ components: { securitySchemes: { b: { type: "http", scheme: "basic" } } } }).auth).toEqual({
			type: "basic",
		});
		expect(
			authFromSecuritySchemes({ components: { securitySchemes: { q: { type: "apiKey", in: "query", name: "api_key" } } } }).auth,
		).toEqual({ type: "query", query_param: "api_key" });
		expect(
			authFromSecuritySchemes({ components: { securitySchemes: { a: { type: "apiKey", in: "header", name: "Authorization" } } } }).auth,
		).toEqual({ type: "bearer" });
		expect(authFromSecuritySchemes({ securityDefinitions: { s: { type: "basic" } } }).auth).toEqual({ type: "basic" });
		expect(authFromSecuritySchemes({ components: { securitySchemes: { o: { type: "oauth2" } } } }).warning).toContain("oauth2");
	});

	it("templateToGlob rewrites each parameter segment", () => {
		expect(templateToGlob("/users/{id}/posts/{postId}")).toBe("/users/*/posts/*");
		expect(templateToGlob("users")).toBe("/users");
		expect(templateToGlob("/")).toBe("/");
	});

	it("templateToGlob handles parameters inside a segment and escapes glob characters", () => {
		expect(templateToGlob("/files/{id}.json")).toBe("/files/*.json");
		expect(templateToGlob("/v1/models/{model}:predict")).toBe("/v1/models/*:predict");
		expect(templateToGlob("/{owner}-{repo}/issues")).toBe("/*-*/issues");
		expect(templateToGlob("/items[0]/what?")).toBe("/items\\[0\\]/what\\?");
		expect(templateToGlob("/a*b")).toBe("/a\\*b");
	});

	it("parses JSON and YAML documents", async () => {
		expect((await parseOpenAPIText(JSON.stringify(petstore))).openapi).toBe("3.0.3");
		expect((await parseOpenAPIText("openapi: 3.0.0\npaths:\n  /x:\n    get: {}\n")).openapi).toBe("3.0.0");
		await expect(parseOpenAPIText("   ")).rejects.toThrow("empty");
		await expect(parseOpenAPIText("[1,2]")).rejects.toThrow();
	});

	it("warns about documents without usable operations", () => {
		expect(importOpenAPI({ paths: {} }).warnings[0]).toContain("No operation");
		expect(importOpenAPI({}).warnings[0]).toContain("no paths");
	});
});