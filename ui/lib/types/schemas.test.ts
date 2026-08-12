import { describe, expect, it } from "vitest";
import { bedrockDNSSuffixSchema, bedrockEndpointsSchema } from "./schemas";

describe("bedrockDNSSuffixSchema", () => {
	it.each(["amazonaws.com", "c2s.ic.gov", ".amazonaws.com.", " .c2s.ic.gov. "])("accepts valid dotted suffix %s", (value) => {
		expect(bedrockDNSSuffixSchema.safeParse(value).success).toBe(true);
	});

	it("accepts an omitted or empty suffix", () => {
		expect(bedrockDNSSuffixSchema.safeParse(undefined).success).toBe(true);
		expect(bedrockDNSSuffixSchema.safeParse("").success).toBe(true);
	});

	it("rejects a malformed suffix", () => {
		const result = bedrockDNSSuffixSchema.safeParse("bad suffix");
		expect(result.success).toBe(false);
		if (!result.success) {
			expect(result.error.issues[0]?.message).toContain("valid DNS suffix");
		}
	});

	it("rejects a loopback-reserved suffix", () => {
		const result = bedrockDNSSuffixSchema.safeParse("svc.localhost");
		expect(result.success).toBe(false);
		if (!result.success) {
			expect(result.error.issues[0]?.message).toContain("loopback-reserved");
		}
	});
});

describe("bedrockEndpointsSchema", () => {
	it("reuses the shared suffix schema", () => {
		expect(bedrockEndpointsSchema.safeParse({ dns_suffix: "c2s.ic.gov" }).success).toBe(true);
		expect(bedrockEndpointsSchema.safeParse({ dns_suffix: "bad suffix" }).success).toBe(false);
		expect(bedrockEndpointsSchema.safeParse({ dns_suffix: "svc.localhost" }).success).toBe(false);
	});
});