import { describe, expect, it } from "vitest";
import { relayOpenAPIImportWarningText, type RelayOpenAPIImportWarning } from "./relays";

// The gateway names each import warning by code; the dashboard words it in the operator's language
// and falls back to the gateway's English for a code it does not know.
describe("relayOpenAPIImportWarningText", () => {
	const text = (w: Partial<RelayOpenAPIImportWarning> & Pick<RelayOpenAPIImportWarning, "code">) =>
		relayOpenAPIImportWarningText({ message: "gateway text", ...w });

	it("fills the scheme and detail the warning names", () => {
		expect(text({ code: "scheme_undeclared", name: "ApiKey" })).toContain("ApiKey");
		expect(text({ code: "api_key_location", name: "sid", detail: "cookie" })).toMatch(/sid.*cookie/);
		expect(text({ code: "api_key_location", name: "sid" })).toContain("an unsupported location");
		expect(text({ code: "http_scheme", name: "d", detail: "digest" })).toContain("HTTP digest");
		expect(text({ code: "scheme_type", name: "o", detail: "oauth2" })).toContain("oauth2");
		expect(text({ code: "server_query", detail: "?version=2" })).toContain("?version=2");
	});

	it("pluralizes the left-out path items", () => {
		expect(text({ code: "ref_paths", paths: ["/b"] })).toMatch(/^\/b is defined/);
		expect(text({ code: "ref_paths", paths: ["/b", "/c"] })).toMatch(/^\/b, \/c are defined/);
	});

	it("words every code without parameters", () => {
		for (const code of ["auth_optional", "no_single_scheme", "several_schemes", "different_schemes", "no_methods"] as const) {
			expect(text({ code }), code).not.toBe("gateway text");
		}
	});

	it("shows the gateway's English for an unknown code", () => {
		expect(text({ code: "something_new" as RelayOpenAPIImportWarning["code"] })).toBe("gateway text");
	});
});