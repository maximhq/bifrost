import { describe, expect, it } from "vitest";
import type { MCPOpenAPIPreviewResponse, MCPOpenAPISecurityScheme } from "@/lib/types/mcp";
import {
	applyParsedPreview,
	openAPIBaseUrlRequired,
	autoParseStep,
	buildOpenAPIConfigPayload,
	needsHeadersVerification,
	collapseToolSelection,
	detectSpecLanguage,
	expandToolSelection,
	formatBytes,
	isSpecTooLarge,
	mergePreviewRows,
	pruneCredentials,
	readUploadedSpec,
	reconcileToolSelection,
	selectableServers,
	securitySchemeToInputs,
	specFingerprint,
	summarizeSpecSource,
	supportedToolNames,
	applyUploadedSpec,
	openAPIParseControls,
	visibleUploadError,
	defaultSchemeHeader,
	perUserSchemeHeaders,
	mergePerUserHeaderKeys,
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
describe("applyParsedPreview", () => {
	const twoServers: MCPOpenAPIPreviewResponse = {
		...preview,
		servers: ["https://petstore.example.com/v1", "https://staging.petstore.example.com/v1"],
		base_url: "https://petstore.example.com/v1",
	};
	it("keeps a server the user picked from the list when the re-parsed document still lists it", () => {
		const next = applyParsedPreview(
			{ selection: ["listPets"], baseUrl: "https://staging.petstore.example.com/v1", baseUrlChoice: "server", credentials: {} },
			twoServers,
			twoServers,
		);
		expect(next.baseUrl).toBe("https://staging.petstore.example.com/v1");
		expect(next.baseUrlChoice).toBe("server");
	});
	it("falls back to the document default when the picked server is gone", () => {
		const next = applyParsedPreview(
			{ selection: [], baseUrl: "https://removed.example.com", baseUrlChoice: "server", credentials: {} },
			null,
			twoServers,
		);
		expect(next.baseUrl).toBe("https://petstore.example.com/v1");
	});
	it("keeps a custom base URL and reconciles the latest selection", () => {
		const next = applyParsedPreview(
			{
				selection: ["listPets"],
				baseUrl: "https://mine.example.com",
				baseUrlChoice: "custom",
				credentials: { ApiKeyAuth: { value: { value: "k" } }, Gone: { value: { value: "x" } } },
			},
			preview,
			preview,
		);
		expect(next.baseUrl).toBe("https://mine.example.com");
		expect(next.selection).toEqual(["listPets"]);
		expect(next.credentials).toEqual({ ApiKeyAuth: { value: { value: "k" } } });
	});
	it("keeps the credential of a scheme named __proto__ across a re-parse", () => {
		const protoPreview: MCPOpenAPIPreviewResponse = {
			...preview,
			security_schemes: [{ name: "__proto__", type: "apiKey", in: "header", param_name: "X-Key", supported: true }],
		};
		const credentials = JSON.parse('{"__proto__":{"value":{"value":"k"}}}');
		const next = applyParsedPreview({ selection: [], baseUrl: "", baseUrlChoice: "server", credentials }, protoPreview, protoPreview);
		expect(Object.keys(next.credentials)).toEqual(["__proto__"]);
		expect(Object.getOwnPropertyDescriptor(next.credentials, "__proto__")?.value).toEqual({ value: { value: "k" } });
	});
});

describe("readUploadedSpec", () => {
	const file = (size: number, text: () => Promise<string>) => ({ size, text });
	it("reads a document", async () => {
		expect(await readUploadedSpec(file(10, async () => "openapi: 3.0.0"))).toEqual({ text: "openapi: 3.0.0", error: null });
	});
	it("refuses an oversized file without reading it", async () => {
		const result = await readUploadedSpec(
			file(6 * 1024 * 1024, async () => {
				throw new Error("must not read");
			}),
		);
		expect(result.error).toContain("exceeds");
	});
	it("says an empty file is empty, not too large", async () => {
		const result = await readUploadedSpec(file(0, async () => ""));
		expect(result.error).toContain("empty");
		expect(result.error).not.toContain("exceeds");
	});
	it("reports a failed read instead of rejecting", async () => {
		const result = await readUploadedSpec(
			file(10, async () => {
				throw new Error("permission denied");
			}),
		);
		expect(result.text).toBe("");
		expect(result.error).toContain("permission denied");
	});
});
describe("applyUploadedSpec and visibleUploadError", () => {
	it("accepts a file that was read", () => {
		expect(applyUploadedSpec("old.json", { text: "openapi: 3.0.0", error: null })).toEqual({ accept: true, error: null });
	});
	it("keeps the current document when a file is rejected, and reports why against the file still shown", () => {
		const outcome = applyUploadedSpec("old.json", { text: "", error: "The file is empty." });
		expect(outcome.accept).toBe(false);
		expect(visibleUploadError(outcome.error, "old.json")).toBe("The file is empty.");
	});
	it("shows a rejection even when no file was loaded before", () => {
		const outcome = applyUploadedSpec("", { text: "", error: "The file is empty." });
		expect(visibleUploadError(outcome.error, "")).toBe("The file is empty.");
	});
	it("hides a rejection once the shown file changes (another file, or the form was reset)", () => {
		const outcome = applyUploadedSpec("old.json", { text: "", error: "The file is empty." });
		expect(visibleUploadError(outcome.error, "")).toBeNull();
		expect(visibleUploadError(outcome.error, "new.json")).toBeNull();
	});
});

describe("openAPIParseControls", () => {
	const base = { isCurrent: false, hasError: false, canParse: true, isParsing: false };
	it("parses a pasted or uploaded document on its own and offers no button while that works", () => {
		for (const mode of ["paste", "upload"] as const) {
			const pending = openAPIParseControls({ ...base, mode });
			expect(pending).toMatchObject({ autoParse: true, showButton: false, showPending: true });
			const parsed = openAPIParseControls({ ...base, mode, isCurrent: true });
			expect(parsed).toMatchObject({ autoParse: true, showButton: false, showPending: false });
		}
	});
	it("offers the button again after a failed parse, and stops auto-parsing the same source", () => {
		const failed = openAPIParseControls({ ...base, mode: "paste", hasError: true });
		expect(failed).toMatchObject({ showButton: true, showPending: false, buttonLabel: "Parse again" });
	});
	it("shows a parse in flight as pending", () => {
		expect(openAPIParseControls({ ...base, mode: "paste", isParsing: true, hasError: true }).showPending).toBe(true);
	});
	it("fetches a URL only on request, and hides the button once the fetched document is current", () => {
		expect(openAPIParseControls({ ...base, mode: "url" })).toMatchObject({
			autoParse: false,
			showButton: true,
			showPending: false,
			buttonLabel: "Fetch spec",
		});
		expect(openAPIParseControls({ ...base, mode: "url", isCurrent: true }).showButton).toBe(false);
		expect(openAPIParseControls({ ...base, mode: "url", isCurrent: true, hasError: true }).showButton).toBe(true);
	});
	it("does not show pending for an empty or oversized source", () => {
		expect(openAPIParseControls({ ...base, mode: "paste", canParse: false }).showPending).toBe(false);
	});
});

describe("per-user scheme headers", () => {
	const schemes: MCPOpenAPIPreviewResponse = {
		...preview,
		security_schemes: [
			{ name: "ApiKeyAuth", type: "apiKey", in: "header", param_name: "api_key", supported: true },
			{ name: "Bearer", type: "http", scheme: "bearer", supported: true },
			{ name: "QueryKey", type: "apiKey", in: "query", param_name: "key", supported: true },
			{ name: "OAuth", type: "oauth2", supported: false },
		],
	};
	it("suggests the header a scheme travels in, and none for query, cookie or unsupported schemes", () => {
		const [apiKey, bearer, query, oauth] = schemes.security_schemes!;
		expect(defaultSchemeHeader(apiKey)).toBe("api_key");
		expect(defaultSchemeHeader(bearer)).toBe("Authorization");
		expect(defaultSchemeHeader(query)).toBeNull();
		expect(defaultSchemeHeader(oauth)).toBeNull();
	});
	it("uses the typed header name over the suggestion, and drops a cleared one", () => {
		expect(perUserSchemeHeaders(schemes, { ApiKeyAuth: "X-Petstore-Key" })).toEqual({
			ApiKeyAuth: "X-Petstore-Key",
			Bearer: "Authorization",
		});
		expect(perUserSchemeHeaders(schemes, { Bearer: "  " })).toEqual({ ApiKeyAuth: "api_key" });
	});
	it("adds scheme headers to the required headers without duplicates", () => {
		expect(mergePerUserHeaderKeys(["X-Tenant", "authorization"], ["api_key", "Authorization"])).toEqual([
			"X-Tenant",
			"authorization",
			"api_key",
		]);
	});
	it("leaves out the stored credential of a scheme each caller supplies", () => {
		const out = buildOpenAPIConfigPayload(
			{ spec: "x", security_credentials: { ApiKeyAuth: { value: { value: "stored" } }, QueryKey: { value: { value: "q" } } } },
			"paste",
			schemes,
			"server",
			["ApiKeyAuth"],
		);
		expect(Object.keys(out.security_credentials ?? {})).toEqual(["QueryKey"]);
	});
});

describe("document and operation servers", () => {
	const opServers: MCPOpenAPIPreviewResponse = { ...preview, servers: [], base_url: undefined, operation_servers: true };
	it("needs no base URL when every operation declares its own server", () => {
		expect(openAPIBaseUrlRequired("server", opServers)).toBe(false);
		expect(openAPIBaseUrlRequired("server", { ...preview, servers: [] })).toBe(true);
		expect(openAPIBaseUrlRequired("server", preview)).toBe(false);
	});
	it("does not require a base URL for an operation-servers document, even after one was typed and cleared", () => {
		expect(openAPIBaseUrlRequired("custom", opServers)).toBe(false);
		expect(openAPIBaseUrlRequired("custom", preview)).toBe(true);
	});
	it("drops a credential whose security scheme changed shape in the re-parsed document", () => {
		const header: MCPOpenAPISecurityScheme = { name: "Key", type: "apiKey", in: "header", param_name: "X-API-Key", supported: true };
		const before: MCPOpenAPIPreviewResponse = { ...preview, security_schemes: [header] };
		const sameShape: MCPOpenAPIPreviewResponse = { ...preview, security_schemes: [{ ...header }] };
		const moved: MCPOpenAPIPreviewResponse = { ...preview, security_schemes: [{ ...header, in: "query", param_name: "key" }] };
		const current = { selection: [], baseUrl: "", baseUrlChoice: "server" as const, credentials: { Key: { value: { value: "secret" } } } };
		expect(applyParsedPreview(current, before, sameShape).credentials).toEqual({ Key: { value: { value: "secret" } } });
		expect(applyParsedPreview(current, before, moved).credentials).toEqual({});
	});
	it("clears a picked document server that the re-parsed document no longer lists", () => {
		const before: MCPOpenAPIPreviewResponse = { ...preview, servers: ["https://old.example.com"], base_url: "https://old.example.com" };
		const next = applyParsedPreview(
			{ selection: [], baseUrl: "https://old.example.com", baseUrlChoice: "server", credentials: {} },
			before,
			opServers,
		);
		expect(next.baseUrl).toBe("");
		expect(next.baseUrlChoice).toBe("server");
	});
	it("keeps a base URL typed for an operation-servers document across a re-parse", () => {
		const next = applyParsedPreview(
			{ selection: [], baseUrl: "https://gateway.example.com", baseUrlChoice: "server", credentials: {} },
			opServers,
			opServers,
		);
		expect(next.baseUrl).toBe("https://gateway.example.com");
		expect(next.baseUrlChoice).toBe("custom");
	});
	it("keeps a selected operation whose shortened tool name changed after a rename", () => {
		const before: MCPOpenAPIPreviewResponse = {
			...preview,
			tools: [
				{ name: "an_operation_with_a_long_name_cut_one_way", method: "GET", path: "/long" },
				{ name: "listPets", method: "GET", path: "/pets" },
			],
		};
		const after: MCPOpenAPIPreviewResponse = {
			...preview,
			tools: [
				{ name: "an_operation_with_a_long_name_cut", method: "GET", path: "/long" },
				{ name: "listPets", method: "GET", path: "/pets" },
			],
		};
		const next = applyParsedPreview(
			{ selection: ["an_operation_with_a_long_name_cut_one_way"], baseUrl: "", baseUrlChoice: "server", credentials: {} },
			before,
			after,
		);
		expect(next.selection).toEqual(["an_operation_with_a_long_name_cut"]);
	});
	it("keeps the document-servers choice after parsing an operation-servers document", () => {
		const next = applyParsedPreview({ selection: [], baseUrl: "", baseUrlChoice: "server", credentials: {} }, null, opServers);
		expect(next.baseUrlChoice).toBe("server");
		expect(next.baseUrl).toBe("");
	});
	it("does not pin the document's default server as an explicit base URL", () => {
		const payload = buildOpenAPIConfigPayload({ spec: "x", base_url: "https://petstore.example.com/v1" }, "paste", preview);
		expect(payload.base_url).toBe("");
		const custom = buildOpenAPIConfigPayload({ spec: "x", base_url: "https://staging.example.com" }, "paste", preview);
		expect(custom.base_url).toBe("https://staging.example.com");
	});
	it("matches document servers regardless of a trailing slash", () => {
		const slashed: MCPOpenAPIPreviewResponse = { ...preview, servers: ["https://api.x/v1/"], base_url: "https://api.x/v1" };
		const fresh = applyParsedPreview({ selection: [], baseUrl: "", baseUrlChoice: "server", credentials: {} }, null, slashed);
		expect(fresh.baseUrlChoice).toBe("server");
		expect(fresh.baseUrl).toBe("https://api.x/v1/");
		const picked = applyParsedPreview(
			{ selection: [], baseUrl: "https://api.x/v1", baseUrlChoice: "server", credentials: {} },
			slashed,
			slashed,
		);
		expect(picked.baseUrlChoice).toBe("server");
		expect(picked.baseUrl).toBe("https://api.x/v1/");
	});
	it("keeps a custom URL that equals the document's first server, so it still overrides operation servers", () => {
		const def = "https://petstore.example.com/v1";
		expect(buildOpenAPIConfigPayload({ spec: "x", base_url: def }, "paste", preview, "custom").base_url).toBe(def);
		expect(buildOpenAPIConfigPayload({ spec: "x", base_url: def }, "paste", preview, "server").base_url).toBe("");
	});
	it("offers only absolute servers, and needs a base URL when the document has no absolute one", () => {
		const relativeOnly: MCPOpenAPIPreviewResponse = { ...preview, servers: ["/v1"], base_url: undefined, operation_servers: false };
		expect(selectableServers({ ...preview, servers: ["/v1", "https://api.example.com"] })).toEqual(["https://api.example.com"]);
		expect(openAPIBaseUrlRequired("server", relativeOnly)).toBe(true);
		const typed = applyParsedPreview(
			{ selection: [], baseUrl: "https://gw.example.com", baseUrlChoice: "server", credentials: {} },
			relativeOnly,
			relativeOnly,
		);
		expect(typed.baseUrl).toBe("https://gw.example.com");
	});
	it("keeps a chosen server that is not the document's first, even after a re-parse echoes it back", () => {
		const reparsed: MCPOpenAPIPreviewResponse = {
			...preview,
			servers: ["https://prod.example.com", "https://staging.example.com"],
			base_url: "https://staging.example.com",
		};
		expect(buildOpenAPIConfigPayload({ spec: "x", base_url: "https://staging.example.com" }, "paste", reparsed).base_url).toBe(
			"https://staging.example.com",
		);
		expect(buildOpenAPIConfigPayload({ spec: "x", base_url: "https://prod.example.com/" }, "paste", reparsed).base_url).toBe("");
	});
});
describe("autoParseStep", () => {
	it("parses a settled source once, and again after the user leaves it and comes back", () => {
		let remembered: string | null = null;
		const run = (key: string, should: boolean) => {
			const step = autoParseStep(remembered, key, should);
			remembered = step.remembered;
			return step.parse;
		};
		expect(run("paste:A", true)).toBe(true);
		// A failed parse is not retried on its own while the source is unchanged.
		expect(run("paste:A", true)).toBe(false);
		// The URL tab fetches only on request.
		expect(run("url:https://x/openapi.json", false)).toBe(false);
		// Back on Paste, the URL's preview is current, not the pasted one's: parse again.
		expect(run("paste:A", true)).toBe(true);
	});
});

describe("needsHeadersVerification", () => {
	it("runs the sample-values check for per-user headers, except on OpenAPI servers, which the server never verifies", () => {
		expect(needsHeadersVerification({ auth_type: "per_user_headers", connection_type: "http" })).toBe(true);
		expect(needsHeadersVerification({ auth_type: "per_user_headers", connection_type: "openapi" })).toBe(false);
		expect(needsHeadersVerification({ auth_type: "headers", connection_type: "http" })).toBe(false);
	});
});