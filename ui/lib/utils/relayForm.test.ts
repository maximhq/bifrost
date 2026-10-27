import { describe, expect, it } from "vitest";
import type { Relay } from "@/lib/types/relays";
import { relayAuthSchema, relayFormSchema, relayNetworkFormSchema, relayUsageBaseSchema } from "@/lib/types/schemas";
import {
	cleanJob,
	cleanUsage,
	describePricing,
	emptyNetworkForm,
	emptyRelayForm,
	emptyUsageForm,
	mergeUsage,
	toRelayConfig,
	toRelayForm,
} from "./relayForm";

const stored: Relay = {
	name: "tavily",
	base_url: "https://search.example.com",
	auth: { type: "bearer" },
	credential: { value: { value: "tvly-*******6789" } },
	extra_headers: { "X-Tenant": { value: "acme" } },
	usage: { cost_per_request: 0.001, units_path: "usage.credits", cost_per_unit: 0.008 },
	routes: [
		{ method: "POST", path: "/search" },
		{ method: "POST", path: "/extract", usage: { cost_per_request: 0, cost_per_unit: 0.016 } },
		{ method: "GET", path: "/jobs/*", billable: true, enabled: false, headers: { "X-Kind": "poll" } },
	],
};

describe("relay form", () => {
	it("round-trips an endpoint, keeping route overrides and the redacted secret", () => {
		const back = toRelayConfig(toRelayForm(stored));
		expect(back.credential?.value?.value).toBe("tvly-*******6789");
		expect(back.extra_headers?.["X-Tenant"].value).toBe("acme");
		expect(back.routes?.[1].usage).toEqual({ cost_per_request: 0, cost_per_unit: 0.016 });
		expect(back.routes?.[2].billable).toBe(true);
		expect(back.routes?.[2].headers).toEqual({ "X-Kind": "poll" });
		expect(back.routes?.[0].billable).toBeUndefined();
		expect(back.routes?.[2].enabled).toBe(false);
		expect(back.routes?.[0].enabled).toBeUndefined();
	});

	it("keeps settings the form does not edit, and tells an explicit direct proxy from inheriting", () => {
		const aws: Relay = {
			...stored,
			auth: { type: "aws_sigv4", aws: { service: "textract" } },
			credential: {
				aws: { region: { value: "us-east-1" }, role_arn: { value: "arn:aws:iam::1:role/r" }, session_name: { value: "bifrost" } },
			},
			proxy_config: { type: "http", url: { value: "http://proxy:8080" }, ca_cert_pem: { value: "<REDACTED>" } },
		};
		const back = toRelayConfig(toRelayForm(aws));
		expect(back.credential?.aws?.session_name?.value).toBe("bifrost");
		expect(back.proxy_config?.ca_cert_pem?.value).toBe("<REDACTED>");

		const direct = toRelayConfig(toRelayForm({ ...stored, proxy_config: { type: "none" } }));
		expect(direct.proxy_config).toEqual({ type: "none" });
		const inherits = toRelayConfig(toRelayForm({ ...stored, proxy_config: undefined }));
		expect(inherits.proxy_config).toBeUndefined();
	});

	it("treats an empty auth type as bearer, and refuses duplicate extra headers and dot names", () => {
		const form = toRelayForm({ ...stored, auth: { type: "" as "bearer" } });
		expect(form.auth.type).toBe("bearer");
		expect(relayFormSchema.safeParse(form).success).toBe(true);
		const dup = {
			...form,
			extra_headers: [
				{ name: "X-Tenant", value: { value: "a" } },
				{ name: "x-tenant", value: { value: "b" } },
			],
		};
		expect(relayFormSchema.safeParse(dup).error?.issues.some((i) => i.path.join(".") === "extra_headers.1.name")).toBe(true);
		expect(relayFormSchema.safeParse({ ...form, name: ".." }).success).toBe(false);
	});

	it("refuses a half-filled extra header row and skips a blank one", () => {
		const base = toRelayForm(stored);
		const issues = (rows: { name: string; value: { value: string } }[]) =>
			relayFormSchema.safeParse({ ...base, extra_headers: rows }).error?.issues.map((i) => i.path.join(".")) ?? [];
		expect(issues([{ name: "X-Tenant", value: { value: "" } }])).toContain("extra_headers.0.value");
		expect(issues([{ name: "", value: { value: "acme" } }])).toContain("extra_headers.0.name");
		expect(issues([{ name: "", value: { value: "" } }])).toEqual([]);
	});

	it("loads a lowercase route method as uppercase so the form validates", () => {
		const form = toRelayForm({ ...stored, routes: [{ method: "post" as "POST", path: "/search" }] });
		expect(form.routes[0].method).toBe("POST");
		expect(relayFormSchema.safeParse(form).success).toBe(true);
	});

	it("reports a bad streaming path on the paths field, skipping blank lines", () => {
		const form = {
			...toRelayForm(stored),
			streaming: { paths: ["/ok/**", "", "no-slash"] },
		};
		const parsed = relayFormSchema.safeParse(form);
		expect(parsed.success).toBe(false);
		const issue = parsed.error?.issues.find((i) => i.path.join(".") === "streaming.paths");
		expect(issue?.message).toContain("Line 3");
	});

	it("stores only the job fields that were set", () => {
		const job = cleanJob({
			id_path: " JobId ",
			poll: { method: "POST", path: "/", headers: {}, body: "", content_type: "" },
			status_path: "JobStatus",
			succeeded: ["SUCCEEDED"],
			failed: [],
			interval_seconds: 30,
			timeout_seconds: undefined,
			usage: { units_path: "DocumentMetadata.Pages", cost_per_unit: 0.0015, cost_per_request: undefined },
		});
		expect(job).toEqual({
			id_path: "JobId",
			poll: { method: "POST", path: "/" },
			status_path: "JobStatus",
			succeeded: ["SUCCEEDED"],
			interval_seconds: 30,
			usage: { units_path: "DocumentMetadata.Pages", cost_per_unit: 0.0015 },
		});
	});

	it("leaves the reported cost out of the price when the relay ignores it", () => {
		expect(describePricing({ cost_per_request: 0.001, cost_path: "usage.cost" })).toContain("reported cost");
		expect(describePricing({ cost_per_request: 0.001, cost_path: "usage.cost" }, true)).not.toContain("reported cost");
	});

	it("loads relay-level defaults as unset so the form shows placeholders", () => {
		const form = toRelayForm({ ...stored, usage: { cost_per_unit: 0.008, units_minimum: 0, units_block_size: 0 } });
		expect(form.usage).toEqual({ cost_per_unit: 0.008 });
		expect(cleanUsage({ units_minimum: 0, units_block_size: 1 }, true)).toEqual({ units_minimum: 0, units_block_size: 1 });
	});

	it("keeps an explicit block size at the relay level, since it changes the price", () => {
		expect(cleanUsage({ units_block_size: 1 }, false)).toEqual({ units_block_size: 1 });
		expect(cleanUsage({ units_block_size: 0.5 }, false)).toEqual({ units_block_size: 0.5 });
		expect(cleanUsage({ units_block_size: 0 }, false)).toBeUndefined();
		const form = toRelayForm({ ...stored, usage: { units_block_size: 0.5 } });
		expect(form.usage).toEqual({ units_block_size: 0.5 });
	});

	it("keeps a zero on a route and drops it at the endpoint level", () => {
		expect(cleanUsage({ cost_per_request: 0 }, true)).toEqual({ cost_per_request: 0 });
		expect(cleanUsage({ cost_per_request: 0 }, false)).toBeUndefined();
	});

	it("merges a route override over the endpoint mapping", () => {
		expect(mergeUsage(stored.usage, stored.routes?.[1].usage)).toEqual({
			cost_per_request: 0,
			units_path: "usage.credits",
			cost_per_unit: 0.016,
		});
	});

	it("describes the pricing formula", () => {
		expect(describePricing({ cost_per_request: 0.002, cost_per_unit: 0.0001, units_block_size: 1000, units_minimum: 1 })).toBe(
			"$0.002 / call + $0.0001 per 1000 units, min 1",
		);
		expect(describePricing(undefined)).toBe("Free");
	});

	it("requires the credential the auth type needs", () => {
		const form = { ...emptyRelayForm(), name: "x", base_url: "https://x.example" };
		expect(relayFormSchema.safeParse(form).success).toBe(false);
		expect(relayFormSchema.safeParse({ ...form, credential_value: { value: "k" } }).success).toBe(true);
		expect(relayFormSchema.safeParse({ ...form, auth: { type: "none" } }).success).toBe(true);
	});
});

describe("usage form defaults", () => {
	it("carry every usage field the sheet registers, so a reset after a save reads clean", () => {
		const keys = Object.keys(relayUsageBaseSchema.shape).sort();
		expect(Object.keys(emptyUsageForm()).sort()).toEqual(keys);
		expect(Object.keys(emptyRelayForm().usage ?? {}).sort()).toEqual(keys);
		const stored: Relay = { name: "r", base_url: "https://x", auth: { type: "bearer" }, usage: { cost_per_request: 0.01 } };
		const form = toRelayForm(stored);
		expect(Object.keys(form.usage ?? {}).sort()).toEqual(keys);
		expect(form.usage?.cost_per_request).toBe(0.01);
		expect(toRelayConfig(form).usage).toEqual({ cost_per_request: 0.01 });
	});

	it("do the same for the network and auth sections", () => {
		const networkKeys = Object.keys(relayNetworkFormSchema.shape).sort();
		const authKeys = Object.keys(relayAuthSchema.shape).sort();
		expect(Object.keys(emptyNetworkForm()).sort()).toEqual(networkKeys);
		expect(Object.keys(emptyRelayForm().network).sort()).toEqual(networkKeys);
		expect(Object.keys(emptyRelayForm().auth).sort()).toEqual(authKeys);
		const stored: Relay = {
			name: "r",
			base_url: "https://x",
			auth: { type: "header", header_name: "X-Key" },
			network: { timeout_seconds: 30 },
		};
		const form = toRelayForm(stored);
		expect(Object.keys(form.network).sort()).toEqual(networkKeys);
		expect(Object.keys(form.auth).sort()).toEqual(authKeys);
		expect(form.network.timeout_seconds).toBe(30);
		expect(form.auth.header_name).toBe("X-Key");
		const back = toRelayConfig(form);
		expect(back.network).toEqual({ timeout_seconds: 30 });
		expect(back.auth).toEqual({ type: "header", header_name: "X-Key" });
	});
});