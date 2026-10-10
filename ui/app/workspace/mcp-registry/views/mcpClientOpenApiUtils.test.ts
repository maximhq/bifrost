import { describe, expect, it } from "vitest";
import type { MCPOpenAPIPreviewResponse, MCPOpenAPISecurityScheme } from "@/lib/types/mcp";
import {
	buildOpenAPIConfigPayload,
	collapseToolSelection,
	detectSpecLanguage,
	expandToolSelection,
	formatBytes,
	isSpecTooLarge,
	mergePreviewRows,
	pruneCredentials,
	reconcileToolSelection,
	securitySchemeToInputs,
	specFingerprint,
	summarizeSpecSource,
	supportedToolNames,
} from "./mcpClientOpenApiUtils";

const preview: MCPOpenAPIPreviewResponse = {
	title: "Petstore",
	version: "1.0.0",
	openapi_version: "3.0.3",
	servers: ["https://petstore.example.com/v1"],
	base_url: "https://petstore.example.com/v1",
	security_schemes: [
		{ name: "ApiKeyAuth", type: "apiKey", in: "header", param_name: "X-API-Key", supported: true },
		{ name: "OAuth", type: "oauth2", supported: false, reason: "oauth2 security schemes are not supported" },
	],
	tools: [
		{ name: "listPets", method: "GET", path: "/pets", summary: "List pets" },
		{ name: "createPet", method: "POST", path: "/pets" },
		{ name: "getPetById", method: "GET", path: "/pets/{petId}", description: "Info for a pet" },
	],
	unsupported: [
		{
			operation_id: "uploadPetPhoto",
			method: "POST",
			path: "/pets/{petId}/photo",
			reason: "multipart/form-data request body is not supported",
		},
	],
	warnings: [],
	tool_count: 3,
	spec_size: 1234,
	spec_hash: "abc",
};

describe("detectSpecLanguage", () => {
	it("recognizes JSON objects and arrays, defaults to YAML", () => {
		expect(detectSpecLanguage('{"openapi":"3.0.0"}')).toBe("json");
		expect(detectSpecLanguage("  \n[1]")).toBe("json");
		expect(detectSpecLanguage("﻿{")).toBe("json");
		expect(detectSpecLanguage("openapi: 3.0.0")).toBe("yaml");
		expect(detectSpecLanguage("")).toBe("yaml");
		expect(detectSpecLanguage(undefined)).toBe("yaml");
	});
});

describe("specFingerprint", () => {
	it("tracks the text for paste/upload and only the URL for url mode", () => {
		expect(specFingerprint("paste", "a", "u")).toBe(specFingerprint("paste", "a", "other url"));
		expect(specFingerprint("paste", "a", "u")).not.toBe(specFingerprint("upload", "a", "u"));
		expect(specFingerprint("url", "a", " https://x ")).toBe(specFingerprint("url", "b", "https://x"));
		expect(specFingerprint("url", "changed text", "https://x")).toBe(specFingerprint("url", "a", "https://x"));
	});
	it("changes with the client name, which shortens long tool names", () => {
		expect(specFingerprint("paste", "a", "", "petstore")).not.toBe(specFingerprint("paste", "a", "", "zoo"));
		expect(specFingerprint("url", "a", "https://x", "petstore")).not.toBe(specFingerprint("url", "a", "https://x", "zoo"));
		expect(specFingerprint("paste", "a", "", " petstore ")).toBe(specFingerprint("paste", "a", "", "petstore"));
	});
	it("cannot be forged by a separator inside the text, URL or name", () => {
		expect(specFingerprint("paste", "a|b", "", "c")).not.toBe(specFingerprint("paste", "a", "", "b|c"));
		expect(specFingerprint("paste", "A|b", "")).not.toBe(specFingerprint("paste", "A", "", "b"));
		expect(specFingerprint("url", "", "https://x/a|b")).not.toBe(specFingerprint("url", "", "https://x/a", "b"));
	});
});

describe("mergePreviewRows", () => {
	it("lists supported tools first and unsupported operations disabled with their reason", () => {
		const rows = mergePreviewRows(preview.tools, preview.unsupported);
		expect(rows.map((r) => r.name)).toEqual(["listPets", "createPet", "getPetById", "uploadPetPhoto"]);
		expect(rows[0].supported).toBe(true);
		expect(rows[0].description).toBe("List pets");
		expect(rows[2].description).toBe("Info for a pet");
		expect(rows[3].supported).toBe(false);
		expect(rows[3].skipReason).toContain("multipart");
		expect(rows[3].key).toBe("POST /pets/{petId}/photo");
	});
	it("flags deprecated skips and names anonymous operations by method and path", () => {
		const rows = mergePreviewRows([], [{ method: "DELETE", path: "/pets/{id}", reason: "deprecated (set include_deprecated)" }]);
		expect(rows[0].name).toBe("delete /pets/{id}");
		expect(rows[0].deprecated).toBe(true);
	});
});

describe("tool selection helpers", () => {
	const supported = supportedToolNames(preview);
	it("collapses a full selection to the wildcard and keeps preview order otherwise", () => {
		expect(supported).toEqual(["listPets", "createPet", "getPetById"]);
		expect(collapseToolSelection(["getPetById", "createPet", "listPets"], supported)).toEqual(["*"]);
		expect(collapseToolSelection(["getPetById", "listPets", "ghost"], supported)).toEqual(["listPets", "getPetById"]);
		expect(collapseToolSelection([], supported)).toEqual([]);
		expect(collapseToolSelection(["a"], [])).toEqual([]);
	});
	it("expands a stored allow-list for the picker", () => {
		expect(expandToolSelection(["*"], supported)).toEqual(supported);
		expect(expandToolSelection(["listPets", "ghost"], supported)).toEqual(["listPets"]);
		expect(expandToolSelection(undefined, supported)).toEqual([]);
	});
	it("reconciles a selection across a re-parse", () => {
		const next = ["listPets", "getPetById", "deletePet"];
		expect(reconcileToolSelection(["*"], supported, next)).toEqual(next);
		expect(reconcileToolSelection(supported, supported, next)).toEqual(next);
		expect(reconcileToolSelection(["listPets", "createPet"], supported, next)).toEqual(["listPets"]);
		expect(reconcileToolSelection([], supported, next)).toEqual([]);
	});
	it("does not select an operation that only inherited a selected tool's name", () => {
		const before = [
			{ name: "fetch_pet", method: "GET", path: "/a" },
			{ name: "fetch_pet_2", method: "GET", path: "/b" },
		];
		const after = [{ name: "fetch_pet", method: "GET", path: "/b" }];
		expect(reconcileToolSelection(["fetch_pet"], ["fetch_pet", "fetch_pet_2"], ["fetch_pet"], before, after)).toEqual([]);
	});
	it("follows an operation whose tool name changed, matched by method and path", () => {
		const before = [
			{ name: "petstore_a_very_long_operation_name_cut_here", method: "GET", path: "/long" },
			{ name: "listPets", method: "GET", path: "/pets" },
			{ name: "createPet", method: "POST", path: "/pets" },
		];
		const after = [
			{ name: "petstore_a_very_long_operation_name_cut", method: "get", path: "/long" },
			{ name: "listPets", method: "GET", path: "/pets" },
			{ name: "createPet", method: "POST", path: "/pets" },
		];
		expect(
			reconcileToolSelection(
				["petstore_a_very_long_operation_name_cut_here", "listPets"],
				before.map((t) => t.name),
				after.map((t) => t.name),
				before,
				after,
			),
		).toEqual(["petstore_a_very_long_operation_name_cut", "listPets"]);
	});
});

describe("securitySchemeToInputs", () => {
	it("maps apiKey, bearer and basic schemes to inputs and leaves unsupported ones without fields", () => {
		const apiKey = securitySchemeToInputs(preview.security_schemes[0]);
		expect(apiKey.label).toBe("ApiKeyAuth · X-API-Key (header)");
		expect(apiKey.fields.map((f) => f.field)).toEqual(["value"]);
		expect(apiKey.fields[0].placeholder).toContain("env.APIKEYAUTH");

		const bearer: MCPOpenAPISecurityScheme = { name: "Bearer-Token", type: "http", scheme: "bearer", supported: true };
		expect(securitySchemeToInputs(bearer).label).toBe("Bearer-Token · Bearer token");

		const basic: MCPOpenAPISecurityScheme = { name: "Basic", type: "http", scheme: "basic", supported: true };
		expect(securitySchemeToInputs(basic).fields.map((f) => f.field)).toEqual(["username", "password"]);

		const oauth = securitySchemeToInputs(preview.security_schemes[1]);
		expect(oauth.fields).toEqual([]);
		expect(oauth.helper).toContain("not supported");
	});
});

describe("pruneCredentials and buildOpenAPIConfigPayload", () => {
	it("keeps a scheme named __proto__ like any other name", () => {
		const creds = JSON.parse('{"__proto__": {"value": {"value": "k"}}}');
		const schemes: MCPOpenAPISecurityScheme[] = [{ name: "__proto__", type: "apiKey", in: "header", param_name: "X-Key", supported: true }];
		const pruned = pruneCredentials(creds, schemes);
		expect(pruned && Object.keys(pruned)).toEqual(["__proto__"]);
	});
	it("drops empty credentials and undeclared schemes", () => {
		const pruned = pruneCredentials(
			{
				ApiKeyAuth: { value: { value: "k" } },
				Empty: { value: { value: "" } },
				Undeclared: { value: { value: "x" } },
			},
			preview.security_schemes,
		);
		expect(pruned).toEqual({ ApiKeyAuth: { value: { value: "k" } } });
		expect(pruneCredentials({ A: { value: { value: "" } } }, undefined)).toBeUndefined();
		expect(pruneCredentials(undefined, undefined)).toBeUndefined();
	});
	it("inlines the document, keeps the url as provenance in url mode, and omits untouched options", () => {
		const paste = buildOpenAPIConfigPayload(
			{ spec: "openapi: 3.0.0", spec_url: "https://ignored", base_url: " https://api.example.com " },
			"paste",
			preview,
		);
		expect(paste).toEqual({ spec: "openapi: 3.0.0", spec_url: "", base_url: "https://api.example.com" });

		const url = buildOpenAPIConfigPayload(
			{ spec: "", spec_url: "https://x/openapi.json", include_deprecated: true, max_response_bytes: 10 },
			"url",
			{
				...preview,
				spec: "fetched text",
			},
		);
		expect(url).toEqual({
			spec: "fetched text",
			spec_url: "https://x/openapi.json",
			base_url: "",
			include_deprecated: true,
			max_response_bytes: 10,
		});

		// A redirected spec_url: the fetched text is saved with the URL it was
		// read from, so its relative servers resolve where the preview showed.
		const redirected = buildOpenAPIConfigPayload({ spec: "", spec_url: "https://x/openapi.json" }, "url", {
			...preview,
			spec: "fetched text",
			spec_url: "https://x/v2/openapi.json",
		});
		expect(redirected.spec_url).toBe("https://x/v2/openapi.json");

		const withCreds = buildOpenAPIConfigPayload(
			{ spec: "x", security_credentials: { ApiKeyAuth: { value: { value: "k" } }, OAuth: { value: { value: "nope" } } } },
			"upload",
			preview,
		);
		expect(withCreds.security_credentials).toEqual({ ApiKeyAuth: { value: { value: "k" } } });
	});
	it("sends the fetched document in url mode, never stale pasted or stored text", () => {
		const url = buildOpenAPIConfigPayload({ spec: "old stored document", spec_url: "https://x/v2.json" }, "url", {
			...preview,
			spec: "new fetched document",
		});
		expect(url.spec).toBe("new fetched document");
		expect(url.spec_url).toBe("https://x/v2.json");
	});
	it("clears a stored spec_url when the document is pasted or uploaded", () => {
		for (const mode of ["paste", "upload"] as const) {
			const out = buildOpenAPIConfigPayload({ spec: "new text", spec_url: "https://old/openapi.json" }, mode, preview);
			expect(out.spec_url).toBe("");
		}
		expect(buildOpenAPIConfigPayload({ spec: "x", spec_url: "https://x/v2.json" }, "url", { ...preview, spec: "f" }).spec_url).toBe(
			"https://x/v2.json",
		);
	});
	it("sends an empty base_url when the override is cleared, so the stored one does not stay", () => {
		expect(buildOpenAPIConfigPayload({ spec: "x", base_url: "" }, "paste", preview)).toHaveProperty("base_url", "");
		expect(buildOpenAPIConfigPayload({ spec: "x" }, "paste", preview)).toHaveProperty("base_url", "");
	});
	it("drops a basic credential with a cleared field, so the server removes it instead of keeping the old secret", () => {
		const schemes: MCPOpenAPISecurityScheme[] = [{ name: "Basic", type: "http", scheme: "basic", supported: true }];
		expect(pruneCredentials({ Basic: { username: { value: "u" }, password: { value: "" } } }, schemes)).toBeUndefined();
		expect(pruneCredentials({ Basic: { username: { value: "" }, password: { value: "p" } } }, schemes)).toBeUndefined();
		expect(pruneCredentials({ Basic: { username: { value: "u" }, password: { value: "p" } } }, schemes)).toEqual({
			Basic: { username: { value: "u" }, password: { value: "p" } },
		});
		const cleared = buildOpenAPIConfigPayload(
			{ spec: "x", security_credentials: { Basic: { username: { value: "u" }, password: { value: "" } } } },
			"paste",
			{ ...preview, security_schemes: schemes },
		);
		expect(cleared.security_credentials).toEqual({});
	});
	it("sends an explicit empty credential map when every provided credential was cleared", () => {
		const cleared = buildOpenAPIConfigPayload(
			{ spec: "x", security_credentials: { ApiKeyAuth: { value: { value: "" } } } },
			"paste",
			preview,
		);
		expect(cleared.security_credentials).toEqual({});
		const untouched = buildOpenAPIConfigPayload({ spec: "x" }, "paste", preview);
		expect(untouched).not.toHaveProperty("security_credentials");
	});
});

describe("formatting helpers", () => {
	it("formats sizes and detects oversized documents", () => {
		expect(formatBytes(0)).toBe("0 B");
		expect(formatBytes(512)).toBe("512 B");
		expect(formatBytes(12 * 1024 + 410)).toBe("12.4 KB");
		expect(formatBytes(2 * 1024 * 1024)).toBe("2.0 MB");
		expect(isSpecTooLarge(5 * 1024 * 1024)).toBe(false);
		expect(isSpecTooLarge(5 * 1024 * 1024 + 1)).toBe(true);
	});
	it("summarizes a stored openapi_config", () => {
		expect(summarizeSpecSource({ spec_title: "Petstore", openapi_version: "3.0.3", spec_size: 12 * 1024 + 410, operation_count: 14 })).toBe(
			"Petstore (OpenAPI 3.0.3) · inline spec · 12.4 KB · 14 operations",
		);
		expect(summarizeSpecSource({ spec_url: "https://x/openapi.json", operation_count: 1 })).toBe("https://x/openapi.json · 1 operation");
		expect(summarizeSpecSource(undefined)).toBe("-");
	});
});