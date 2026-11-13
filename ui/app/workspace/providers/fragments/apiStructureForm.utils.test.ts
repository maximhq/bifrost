import { describe, expect, it } from "vitest";

import type { FormCustomProviderConfig } from "@/lib/types/config";
import { withToolsFlagDefaults } from "./apiStructureForm.utils";

const stored = (allowed_requests?: Record<string, unknown>): FormCustomProviderConfig =>
	({
		base_provider_type: "deepseek",
		...(allowed_requests ? { allowed_requests } : {}),
	}) as FormCustomProviderConfig;

describe("withToolsFlagDefaults", () => {
	// form.reset swaps in whatever it is handed, so the reset must carry the defaults the
	// form's defaultValues apply; otherwise the next save sends the block without them and the
	// gateway reads the omitted booleans as false.
	it("fills the tools flags a config saved before them lacks", () => {
		const result = withToolsFlagDefaults(stored({ text_completion: true }));
		expect(result?.allowed_requests?.search).toBe(true);
		expect(result?.allowed_requests?.fetch).toBe(true);
	});

	it("keeps an explicit false rather than turning it back on", () => {
		const result = withToolsFlagDefaults(stored({ search: false, fetch: false }));
		expect(result?.allowed_requests?.search).toBe(false);
		expect(result?.allowed_requests?.fetch).toBe(false);
	});

	it("leaves the rest of the block exactly as stored", () => {
		const allowed_requests = { text_completion: true, embedding: false, rerank: true };
		const result = withToolsFlagDefaults(stored(allowed_requests));
		expect(result?.allowed_requests).toEqual({ ...allowed_requests, search: true, fetch: true });
	});

	// A nil block means every operation is allowed, so materializing one here would change the
	// meaning of the stored config.
	it("keeps an absent block absent", () => {
		expect(withToolsFlagDefaults(stored())).not.toHaveProperty("allowed_requests");
		expect(withToolsFlagDefaults(undefined)).toBeUndefined();
	});
});
