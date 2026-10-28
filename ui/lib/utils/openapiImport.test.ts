import { describe, expect, it } from "vitest";
import {
	authFromSecuritySchemes,
	baseUrlFromServers,
	importOpenAPI,
	parseOpenAPIText,
	templateToGlob,
	templateToRoute,
} from "./openapiImport";

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
			{ method: "DELETE", path: "/pets/*" },
			{ method: "GET", path: "/pets/*" },
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
		).toEqual({ type: "header", header_name: "Authorization" });
		expect(authFromSecuritySchemes({ securityDefinitions: { s: { type: "basic" } } }).auth).toEqual({ type: "basic" });
		expect(authFromSecuritySchemes({ components: { securitySchemes: { o: { type: "oauth2" } } } }).warning).toContain("oauth2");
	});

	it("templateToGlob decodes percent-encoded literal text, since requests match decoded", () => {
		expect(templateToGlob("/files/hello%20world/{id}")).toBe("/files/hello world/*");
		expect(templateToGlob("/caf%C3%A9/menu")).toBe("/café/menu");
		expect(templateToGlob("/café/menu")).toBe("/café/menu");
		// Decoded glob characters are still escaped, and malformed encoding is kept as typed.
		expect(templateToGlob("/a%2A/b")).toBe("/a\\*/b");
		expect(templateToGlob("/bad%ZZ/x")).toBe("/bad%ZZ/x");
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

	it("turns AWS X-Amz-Target path keys into header-matched routes", () => {
		expect(templateToRoute("/#X-Amz-Target=Textract.AnalyzeDocument")).toEqual({
			path: "/",
			headers: { "X-Amz-Target": "Textract.AnalyzeDocument" },
		});
		expect(templateToRoute("/v1/jobs/{id}")).toEqual({ path: "/v1/jobs/*" });
		const result = importOpenAPI({
			openapi: "3.0.0",
			paths: { "/#X-Amz-Target=Textract.GetDocumentAnalysis": { post: {} }, "/#X-Amz-Target=Textract.StartDocumentAnalysis": { post: {} } },
		});
		expect(result.routes).toEqual([
			{ method: "POST", path: "/", headers: { "X-Amz-Target": "Textract.GetDocumentAnalysis" } },
			{ method: "POST", path: "/", headers: { "X-Amz-Target": "Textract.StartDocumentAnalysis" } },
		]);
	});

	it("turns adjacent parameters into one wildcard", () => {
		expect(templateToGlob("/files/{name}{ext}/metadata")).toBe("/files/*/metadata");
		expect(templateToGlob("/files/{name}{ext}")).toBe("/files/*");
		expect(templateToGlob("/a/x{one}{two}y")).toBe("/a/x*y");
		expect(templateToGlob("/a/*{id}"), "a literal star before a parameter stays escaped").toBe("/a/\\**");
	});

	it("parses flow-style YAML that starts with a brace", async () => {
		expect((await parseOpenAPIText("{openapi: 3.0.0, paths: {}}")).openapi).toBe("3.0.0");
		await expect(parseOpenAPIText("{not: [valid")).rejects.toThrow();
	});

	it("orders literal paths before the parameter globs that cover them", () => {
		const doc = { paths: { "/files/{id}": { get: {} }, "/files/latest": { get: {} }, "/files": { post: {} } } };
		expect(importOpenAPI(doc).routes.map((r) => r.path)).toEqual(["/files", "/files/latest", "/files/*"]);
		// A total order: /models/*:predict still lands before /models/* with an unrelated glob between them.
		const mixed = { paths: { "/models/{m}": { post: {} }, "/a/b/{c}": { post: {} }, "/models/{m}:predict": { post: {} } } };
		const order = importOpenAPI(mixed).routes.map((r) => r.path);
		expect(order.indexOf("/models/*:predict")).toBeLessThan(order.indexOf("/models/*"));
		const vertex = { paths: { "/models/{model}": { post: {} }, "/models/{model}:predict": { post: {} } } };
		expect(importOpenAPI(vertex).routes.map((r) => r.path)).toEqual(["/models/*:predict", "/models/*"]);
	});

	it("maps the scheme the document requires, and leaves auth alone when it cannot tell", () => {
		const two = {
			components: { securitySchemes: { b: { type: "http", scheme: "bearer" }, k: { type: "apiKey", in: "header", name: "X-Key" } } },
		};
		expect(authFromSecuritySchemes({ ...two, security: [{ k: [] }] }).auth).toEqual({ type: "header", header_name: "X-Key" });
		expect(
			authFromSecuritySchemes({
				components: { securitySchemes: { o: { type: "oauth2" }, k: { type: "apiKey", in: "header", name: "X-Key" } } },
				security: [{ o: [] }, { k: [] }],
			}).auth,
		).toEqual({ type: "header", header_name: "X-Key" });
		expect(authFromSecuritySchemes({ ...two, security: [{ b: [], k: [] }] }).auth, "jointly required schemes").toBeUndefined();
		const unclear = authFromSecuritySchemes(two);
		expect(unclear.auth).toBeUndefined();
		expect(unclear.warning).toContain("several security schemes");
	});

	it("reads each operation's own security, and leaves auth alone when the operations disagree", () => {
		const schemes = {
			components: { securitySchemes: { b: { type: "http", scheme: "bearer" }, k: { type: "apiKey", in: "header", name: "X-Api-Key" } } },
			security: [{ b: [] }],
		};
		const mixed = importOpenAPI({ ...schemes, paths: { "/a": { get: {} }, "/b": { post: { security: [{ k: [] }] } } } });
		expect(mixed.auth, "bearer by default, X-Api-Key on /b").toBeUndefined();
		expect(mixed.warnings.join(" ")).toContain("different security schemes");
		const overridden = importOpenAPI({
			...schemes,
			paths: { "/a": { get: { security: [{ k: [] }] } }, "/health": { get: { security: [] } } },
		});
		expect(overridden.auth, "every keyed operation uses X-Api-Key; the public one needs none").toEqual({
			type: "header",
			header_name: "X-Api-Key",
		});
		expect(importOpenAPI({ ...schemes, paths: { "/a": { get: {} } } }).auth).toEqual({ type: "bearer" });
	});

	it("infers no auth when every imported operation is public", () => {
		const doc = {
			components: { securitySchemes: { b: { type: "http", scheme: "bearer" } } },
			security: [{ b: [] }],
			paths: { "/a": { get: { security: [] } }, "/b": { post: { security: [] } } },
		};
		const result = importOpenAPI(doc);
		expect(result.auth).toBeUndefined();
		expect(result.warnings.join(" ")).not.toContain("auth type by hand");
		expect(importOpenAPI({ ...doc, security: [] }).warnings.join(" "), "a fully public API is not told to set auth").not.toContain(
			"auth type by hand",
		);
	});

	it("treats an empty security alternative as optional auth, not a required key", () => {
		const schemes = { components: { securitySchemes: { k: { type: "apiKey", in: "header", name: "X-Api-Key" } } } };
		const optional = authFromSecuritySchemes({ ...schemes, security: [{}, { k: [] }] });
		expect(optional.auth).toBeUndefined();
		expect(optional.warning).toContain("optional");
		const imported = importOpenAPI({ ...schemes, security: [{}, { k: [] }], paths: { "/a": { get: {} } } });
		expect(imported.auth, "an operation that works without a key does not make the endpoint require one").toBeUndefined();
		const mixed = importOpenAPI({
			...schemes,
			paths: { "/a": { get: { security: [{}, { k: [] }] } }, "/b": { post: { security: [{ k: [] }] } } },
		});
		expect(mixed.auth, "the operation that requires the key still sets it").toEqual({ type: "header", header_name: "X-Api-Key" });
	});

	it("flags path items behind $ref so they can be added by hand", () => {
		const result = importOpenAPI({ paths: { "/a": { get: {} }, "/b": { $ref: "#/components/pathItems/B" } } });
		expect(result.routes.map((r) => r.path)).toEqual(["/a"]);
		expect(result.partial).toBe(true);
		expect(result.warnings.join(" ")).toContain("/b");
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