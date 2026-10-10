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
} from "./mcpClientOpenApi.utils";

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
		expect(specFingerprint("paste", "a", "u")).toBe("paste:a");
		expect(specFingerprint("upload", "a", "u")).toBe("upload:a");
		expect(specFingerprint("url", "a", " https://x ")).toBe("url:https://x");
		expect(specFingerprint("url", "changed text", "https://x")).toBe(specFingerprint("url", "a", "https://x"));
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
		expect(paste).toEqual({ spec: "openapi: 3.0.0", base_url: "https://api.example.com" });

		const url = buildOpenAPIConfigPayload(
			{ spec: "", spec_url: "https://x/openapi.json", include_deprecated: true, max_response_bytes: 10 },
			"url",
			{
				...preview,
				spec: "fetched text",
			},
		);
		expect(url).toEqual({ spec: "fetched text", spec_url: "https://x/openapi.json", include_deprecated: true, max_response_bytes: 10 });

		const withCreds = buildOpenAPIConfigPayload(
			{ spec: "x", security_credentials: { ApiKeyAuth: { value: { value: "k" } }, OAuth: { value: { value: "nope" } } } },
			"upload",
			preview,
		);
		expect(withCreds.security_credentials).toEqual({ ApiKeyAuth: { value: { value: "k" } } });
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