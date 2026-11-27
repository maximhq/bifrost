import { describe, expect, it } from "vitest";
import {
	hasUnsafeInteger,
	isValidRelayGlobSegment,
	relayAsyncJobFormSchema,
	relayBodyOverridesSchema,
	exactJSONNumbers,
	relayFormSchema,
	relayJobSchema,
	relayOpenAPISpecUrlSchema,
	relayRouteFormSchema,
	relayUsageBaseSchema,
	relayUsageUnitsPathError,
} from "./schemas";
import { emptyRelayForm } from "@/lib/utils/relayForm";

// The form refuses what the gateway refuses, so the error shows on the field rather than as a
// toast after a failed save.
describe("relay form validation", () => {
	const route = (path: string) => relayRouteFormSchema.safeParse({ method: "POST", path, headers: [], enabled: true });

	it("refuses body overrides the gateway would refuse", () => {
		const messages = (pairs: { name: string; value: string }[]) =>
			(relayBodyOverridesSchema.safeParse(pairs).error?.issues ?? []).map((i) => i.message);
		expect(
			messages([
				{ name: "include_usage", value: "true" },
				{ name: "options.depth", value: '"advanced"' },
				{ name: "", value: "" },
			]),
		).toEqual([]);
		expect(messages([{ name: "depth", value: "advanced" }])).toEqual([
			'Enter a JSON value, such as true, 5 or "advanced" (text needs quotes)',
		]);
		expect(messages([{ name: "depth", value: "" }])).toEqual(["An enforced field needs a JSON value"]);
		expect(messages([{ name: "", value: "true" }])).toEqual(["An enforced field needs a path"]);
		expect(messages([{ name: "items.0", value: "1" }])[0]).toContain("no key made only of digits");
		expect(messages([{ name: "items.#.id", value: "1" }])[0]).toContain("object keys separated by dots");
		expect(
			messages([
				{ name: "include_usage", value: "true" },
				{ name: "Include_Usage", value: "false" },
			]),
		).toEqual(["This field is already set (paths ignore letter case)"]);
		expect(
			messages([
				{ name: "options", value: "{}" },
				{ name: "options.depth", value: "1" },
			]),
		).toEqual(["options.depth is inside options, which is also set"]);
		expect(messages([{ name: "options.-1", value: "true" }])[0]).toContain("no key -1");
		expect(messages([{ name: "options.-x", value: "true" }])).toEqual([]);
		expect(
			messages([
				{ name: "options.depth", value: "1" },
				{ name: "Options.mode", value: "2" },
			]),
		).toEqual(["options is spelled with different letter case in another field; use one spelling"]);
		expect(messages([{ name: "limit", value: "1e400" }])[0]).toContain("too large to send");
		expect(messages([{ name: "limits", value: '{"max":[1, -1e400]}' }])[0]).toContain("too large to send");
		// A non-zero number the browser reads as 0 would be sent as 0.
		expect(messages([{ name: "eps", value: "1e-400" }])[0]).toContain("too small to send");
		expect(messages([{ name: "limits", value: '{"min":[0.5, -2.5e-999]}' }])[0]).toContain("too small to send");
		expect(messages([{ name: "zero", value: '[0, 0.0, 0e5, -0, "1e-400"]' }])).toEqual([]);
		expect(messages([{ name: "limit", value: "1e300" }])).toEqual([]);
	});

	it("refuses a relay field that clashes with a route's once they are merged", () => {
		const relay = (fields: { name: string; value: string }[], routeFields: { name: string; value: string }[], inherit?: boolean) =>
			(
				relayFormSchema.safeParse({
					...emptyRelayForm(),
					name: "r",
					base_url: "https://api.example.com",
					auth: { type: "none" },
					body_overrides: fields,
					routes: [{ method: "POST", path: "/search", headers: [], body_overrides: routeFields, inherit_body_overrides: inherit }],
				}).error?.issues ?? []
			).filter((i) => i.path[0] === "body_overrides");
		const clash = relay([{ name: "options", value: "{}" }], [{ name: "options.depth", value: "1" }]);
		expect(clash.map((i) => i.path)).toEqual([["body_overrides", 0, "name"]]);
		expect(clash[0].message).toContain("options.depth on route POST /search");
		expect(relay([{ name: "options.mode", value: "1" }], [{ name: "Options.depth", value: "1" }])).toHaveLength(1);
		expect(relay([{ name: "include_usage", value: "true" }], [{ name: "INCLUDE_USAGE", value: "true" }])).toHaveLength(1);
		expect(relay([{ name: "include_usage", value: "true" }], [{ name: "include_usage", value: "false" }]), "same path is replaced").toEqual(
			[],
		);
		expect(relay([{ name: "options", value: "{}" }], [{ name: "options.depth", value: "1" }], false), "route opted out").toEqual([]);
	});

	it("accepts the gateway's segment globs and rejects a second **", () => {
		expect(route("/v1/*/status").success).toBe(true);
		expect(route("/jobs/**").success).toBe(true);
		expect(route("/**").success).toBe(true);
		expect(route("/jobs/**/status").success).toBe(true);
		const bad = route("/jobs/**/status/**");
		expect(bad.success).toBe(false);
		expect(bad.error?.issues[0]?.message).toBe("** is only allowed once in a path");
		expect(route("jobs").success).toBe(false);
	});

	it("applies the same rule to streaming paths, naming the line", () => {
		const res = relayFormSchema.safeParse({
			...emptyRelayForm(),
			base_url: "https://api.example.com",
			credential_value: { value: "k" },
			streaming: { paths: ["/v1/stream/**", "/a/**/b/**"] },
		});
		expect(res.success).toBe(false);
		expect(res.error?.issues.find((i) => i.path[0] === "streaming")?.message).toBe("Line 2: ** is only allowed once in a path");
	});

	it("rejects a base URL with user info, as the gateway does", () => {
		const parse = (base_url: string) =>
			relayFormSchema.safeParse({ ...emptyRelayForm(), name: "r", base_url, credential_value: { value: "k" } });
		expect(parse("https://api.example.com/v1").success).toBe(true);
		const bad = parse("https://user:secret@api.example.com");
		expect(bad.success).toBe(false);
		expect(bad.error?.issues.find((i) => i.path[0] === "base_url")?.message).toBe(
			"Enter an absolute http(s) URL without a query, fragment or user info",
		);
	});

	it("mirrors the gateway's units path rule, on a route judged with the relay pricing merged in", () => {
		expect(relayUsageUnitsPathError({ cost_per_unit: 0.01 })).toBe("Units path is required unless the measure reads the whole body");
		expect(relayUsageUnitsPathError({ cost_per_unit: 0.01, units_measure: "body_bytes" })).toBeUndefined();
		expect(relayUsageUnitsPathError({ cost_per_unit: 0 })).toBeUndefined();
		// A route that sets a rate inherits the relay's path, and one that inherits the rate needs a path from somewhere.
		expect(relayUsageUnitsPathError({ cost_per_unit: 0.01 }, { units_path: "usage.credits" })).toBeUndefined();
		expect(relayUsageUnitsPathError({ cost_per_unit: 0.01 }, {})).toMatch(/neither it nor the relay names a path/);
		expect(relayUsageUnitsPathError({ units_measure: "value" }, { cost_per_unit: 0.01, units_measure: "body_bytes" })).toMatch(
			/neither it nor the relay/,
		);
		// A cleared (null) field falls back to the relay like an unset one.
		expect(relayUsageUnitsPathError({ cost_per_unit: null, units_path: null }, { cost_per_unit: 0.01, units_path: "u" })).toBeUndefined();
	});

	it("refuses a relay edit that leaves a per-unit route without a units path", () => {
		const res = relayFormSchema.safeParse({
			...emptyRelayForm(),
			base_url: "https://api.example.com",
			credential_value: { value: "k" },
			usage: { ...emptyRelayForm().usage, units_path: "" },
			routes: [{ method: "POST", path: "/search", headers: [], usage: { cost_per_unit: 0.01 } }],
		});
		expect(res.success).toBe(false);
		const issue = res.error?.issues.find((i) => i.path.join(".") === "usage.units_path");
		expect(issue?.message).toBe("Units path is required: route POST /search charges per unit and inherits it from the relay");
	});

	it("does not let a non-billable route's hidden price block a relay edit", () => {
		const res = relayFormSchema.safeParse({
			...emptyRelayForm(),
			name: "search",
			base_url: "https://api.example.com",
			credential_value: { value: "k" },
			routes: [
				{ method: "GET", path: "/status", headers: [], usage: { cost_per_unit: 0.01 } },
				{ method: "POST", path: "/free", headers: [], billable: false, usage: { cost_per_unit: 0.01 } },
			],
		});
		expect(res.error?.issues.map((i) => `${i.path.join(".")}: ${i.message}`) ?? []).toEqual([]);
	});

	it("accepts null for a cleared price or an inherited choice", () => {
		expect(
			relayUsageBaseSchema.safeParse({ cost_per_request: null, cost_per_unit: null, units_source: null, units_measure: null }).success,
		).toBe(true);
		expect(relayUsageBaseSchema.safeParse({ cost_per_unit: -1 }).success).toBe(false);
	});

	it("refuses an async job the gateway would refuse", () => {
		const job = { id_path: "id", poll: { method: "GET", path: "/jobs/{{job.id}}" }, status_path: "status", succeeded: ["done"] };
		const messages = (patch: object) => (relayJobSchema.safeParse({ ...job, ...patch }).error?.issues ?? []).map((i) => i.message);
		expect(messages({})).toEqual([]);
		expect(messages({ poll: { method: "POST", path: "/", body: '{"JobId":"{{job.id}}"}' } })).toEqual([]);
		expect(messages({ poll: { method: "POST", path: "/", headers: { "X-Job": "{{job.id}}" } } })).toEqual([]);
		expect(messages({ poll: { method: "GET", path: "/jobs/latest" } })[0]).toContain("{{job.id}}");
		// With a body the gateway sends its own Content-Type, so the id in that header row is never sent.
		const typed = { "Content-Type": "application/x-job-{{job.id}}" };
		expect(messages({ poll: { method: "POST", path: "/status", body: "{}", headers: typed } })[0]).toContain("{{job.id}}");
		expect(messages({ poll: { method: "POST", path: "/status", headers: typed } })).toEqual([]);
		expect(messages({ timeout_seconds: 604801 })[0]).toContain("7 days");
		expect(messages({ interval_seconds: 7200 })[0]).toContain("3600");
		expect(messages({ interval_seconds: 7200, timeout_seconds: 7200 })).toEqual([]);
		expect(messages({ succeeded: ["DONE"], failed: ["done"] })[0]).toContain("DONE");
		expect(messages({ succeeded: ["", "DONE"], failed: ["", "done"] })).toHaveLength(1);
		expect(messages({ succeeded: ["S"], failed: ["ſ"] })).toHaveLength(1);
		expect(messages({ succeeded: ["ß"], failed: ["SS"] })).toEqual([]);
		expect(messages({ succeeded: ["DONE"], failed: ["FAILED"] })).toEqual([]);
	});

	it("accepts the cleared values the job editor keeps", () => {
		const job = { id_path: "id", poll: { method: "GET", path: "/jobs/{{job.id}}", body: "", content_type: "" }, status_path: "s" };
		const parsed = relayJobSchema.safeParse({
			...job,
			succeeded: ["done"],
			failed: [],
			interval_seconds: null,
			timeout_seconds: null,
			reused_id_is_new_job: false,
		});
		expect(parsed.error?.issues ?? []).toEqual([]);
	});

	it("accepts only an absolute http(s) URL as the OpenAPI import's URL source", () => {
		for (const bad of ["", "   ", "/openapi.json", "openapi.json", "ftp://example.com/spec.json", "http://[::1", "https://"]) {
			expect(relayOpenAPISpecUrlSchema.safeParse(bad).success, bad).toBe(false);
		}
		expect(relayOpenAPISpecUrlSchema.safeParse(" https://exa.ai/docs/exa-spec.json ").data).toBe("https://exa.ai/docs/exa-spec.json");
		expect(relayOpenAPISpecUrlSchema.safeParse("http://127.0.0.1:8080/openapi.yaml").success).toBe(true);
		expect(relayOpenAPISpecUrlSchema.safeParse("http://[::1]/spec.json").success).toBe(true);
	});
});

describe("body override numbers", () => {
	it("refuses a whole number a save would round, and keeps safe or quoted ones", () => {
		expect(hasUnsafeInteger("9007199254740993")).toBe(true);
		expect(hasUnsafeInteger('{"n":[1,-9007199254740993]}')).toBe(true);
		expect(hasUnsafeInteger("9007199254740991")).toBe(false);
		expect(hasUnsafeInteger("1000000000000000128")).toBe(true);
		expect(hasUnsafeInteger("999999999999999999999")).toBe(true);
		expect(hasUnsafeInteger("12345678901234567890123")).toBe(true);
		expect(hasUnsafeInteger("1e300")).toBe(false);
		expect(hasUnsafeInteger("100000000000000000000")).toBe(false);
		expect(hasUnsafeInteger("-0")).toBe(false);
		expect(hasUnsafeInteger('"9007199254740993"')).toBe(false);
		expect(hasUnsafeInteger('{"a\\"9007199254740993":1}')).toBe(false);
		expect(hasUnsafeInteger("1.5e300")).toBe(false);
		// The same whole number spelled with a decimal point or an exponent rounds the same way.
		expect(hasUnsafeInteger("9007199254740993.0")).toBe(true);
		expect(hasUnsafeInteger("9007199254740993e0")).toBe(true);
		expect(hasUnsafeInteger("90071992547409930e-1")).toBe(true);
		expect(hasUnsafeInteger("9007199254740992.0")).toBe(false);
		expect(hasUnsafeInteger("1e20")).toBe(false);
		expect(hasUnsafeInteger("0.5")).toBe(false);
		// Exponents that move the point past every digit are small fractions, not whole numbers.
		expect(hasUnsafeInteger("100e-4")).toBe(false);
		expect(hasUnsafeInteger("10e-3")).toBe(false);
		expect(hasUnsafeInteger('{"rate":[1e-9, 100e-4]}')).toBe(false);
		expect(relayBodyOverridesSchema.safeParse([{ name: "rate", value: "100e-4" }]).success).toBe(true);
		// This runtime keeps exact digits (JSON.rawJSON), so the value is accepted and sent unchanged.
		expect(exactJSONNumbers).toBe(true);
		expect(relayBodyOverridesSchema.safeParse([{ name: "seed", value: "9007199254740993" }]).success).toBe(true);
		// A 400-digit token that a duplicate key overwrites is reported, not thrown on.
		expect(hasUnsafeInteger(`{"n":${"9".repeat(400)},"n":0}`)).toBe(true);
		expect(() => relayBodyOverridesSchema.safeParse([{ name: "n", value: `{"n":${"9".repeat(400)},"n":0}` }])).not.toThrow();
	});
});

describe("relayAsyncJobFormSchema poll headers", () => {
	const job = (headers: Record<string, string>) => ({
		method: "POST" as const,
		path: "/jobs",
		headers: [],
		job: { id_path: "id", poll: { method: "GET", path: "/jobs/{{job.id}}", headers }, status_path: "status", succeeded: ["done"] },
	});
	it("refuses a poll header with no value, which would make its status route match every call", () => {
		const result = relayAsyncJobFormSchema.safeParse(job({ "X-Op": " " }));
		expect(result.success).toBe(false);
		expect(result.error?.issues.some((i) => i.path.join(".") === "job.poll.headers")).toBe(true);
		expect(relayAsyncJobFormSchema.safeParse(job({ "X-Op": "Get" })).success).toBe(true);
	});
});
// The same cases as TestRelayRouteGlobSegmentsMatchTheUI in core/schemas, so the form refuses on the
// field exactly what the gateway refuses on save.
describe("isValidRelayGlobSegment", () => {
	const valid = ["search", "*", "v?", "[a-z]*", "[^x]", "a\\*b", "[\\]]", "jobs-*", ""];
	const invalid = ["[b", "[]", "[]a]", "[a-]", "[-a]", "[a", "a\\", "[^]", "[a-\\", "x[\\"];
	it.each(valid)("accepts %s", (segment) => expect(isValidRelayGlobSegment(segment)).toBe(true));
	it.each(invalid)("refuses %s", (segment) => expect(isValidRelayGlobSegment(segment)).toBe(false));
});