import { describe, expect, it } from "vitest";
import { relayFormSchema, relayRouteFormSchema } from "./schemas";
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
});