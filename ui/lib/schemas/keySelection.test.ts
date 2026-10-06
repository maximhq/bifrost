import { buildProviderUpdatePayload } from "@/app/workspace/providers/views/utils";
import type { ModelProvider } from "@/lib/types/config";
import { keySelectionFormSchema } from "@/lib/types/schemas";
import { describe, expect, it } from "vitest";

describe("keySelectionFormSchema", () => {
	it("accepts every strategy with defaults", () => {
		for (const strategy of ["weighted_random", "round_robin", "least_used", "fill_first"]) {
			expect(keySelectionFormSchema.safeParse({ strategy }).success).toBe(true);
		}
	});

	it("rejects unknown strategies", () => {
		expect(keySelectionFormSchema.safeParse({ strategy: "random" }).success).toBe(false);
	});

	it("bounds sticky_limit to 1..1000", () => {
		expect(keySelectionFormSchema.safeParse({ strategy: "round_robin", sticky_limit: 0 }).success).toBe(false);
		expect(keySelectionFormSchema.safeParse({ strategy: "round_robin", sticky_limit: 1000 }).success).toBe(true);
		expect(keySelectionFormSchema.safeParse({ strategy: "round_robin", sticky_limit: 1001 }).success).toBe(false);
	});

	it("bounds cooldown_seconds to 0..86400 and allows 0 to disable", () => {
		expect(keySelectionFormSchema.safeParse({ strategy: "fill_first", cooldown_seconds: 0 }).success).toBe(true);
		expect(keySelectionFormSchema.safeParse({ strategy: "fill_first", cooldown_seconds: -1 }).success).toBe(false);
		expect(keySelectionFormSchema.safeParse({ strategy: "fill_first", cooldown_seconds: 86401 }).success).toBe(false);
	});
});

describe("buildProviderUpdatePayload key_selection", () => {
	const provider = {
		name: "kiro",
		provider_status: "active",
		key_selection: { strategy: "least_used", cooldown_seconds: 30 },
	} as ModelProvider;

	it("keeps the provider's key_selection when other tabs save", () => {
		expect(buildProviderUpdatePayload(provider, { send_back_raw_request: true }).key_selection).toEqual(provider.key_selection);
	});

	it("sends the updated key_selection", () => {
		const next = { strategy: "round_robin" as const, sticky_limit: 3, cooldown_seconds: 0 };
		expect(buildProviderUpdatePayload(provider, { key_selection: next }).key_selection).toEqual(next);
	});
});
