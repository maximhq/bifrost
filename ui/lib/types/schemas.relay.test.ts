import { describe, expect, it } from "vitest";
import {
	relayFormSchema,
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

	it("accepts the gateway's segment globs and rejects ** before the last segment", () => {
		expect(route("/v1/*/status").success).toBe(true);
		expect(route("/jobs/**").success).toBe(true);
		expect(route("/**").success).toBe(true);
		const bad = route("/jobs/**/status");
		expect(bad.success).toBe(false);
		expect(bad.error?.issues[0]?.message).toBe("** is only allowed as the last segment");
		expect(route("jobs").success).toBe(false);
	});

	it("applies the same rule to streaming paths, naming the line", () => {
		const res = relayFormSchema.safeParse({
			...emptyRelayForm(),
			base_url: "https://api.example.com",
			credential_value: { value: "k" },
			streaming: { paths: ["/v1/stream/**", "/a/**/b"] },
		});
		expect(res.success).toBe(false);
		expect(res.error?.issues.find((i) => i.path[0] === "streaming")?.message).toBe("Line 2: ** is only allowed as the last segment");
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

	it("accepts only an absolute http(s) URL as the OpenAPI import's URL source", () => {
		for (const bad of ["", "   ", "/openapi.json", "openapi.json", "ftp://example.com/spec.json", "http://[::1", "https://"]) {
			expect(relayOpenAPISpecUrlSchema.safeParse(bad).success, bad).toBe(false);
		}
		expect(relayOpenAPISpecUrlSchema.safeParse(" https://exa.ai/docs/exa-spec.json ").data).toBe("https://exa.ai/docs/exa-spec.json");
		expect(relayOpenAPISpecUrlSchema.safeParse("http://127.0.0.1:8080/openapi.yaml").success).toBe(true);
		expect(relayOpenAPISpecUrlSchema.safeParse("http://[::1]/spec.json").success).toBe(true);
	});
});