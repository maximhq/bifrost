import { describe, expect, it } from "vitest";
import { resolveRawJsonNoticeState } from "./logDetailView.utils";

const base = {
	hasProvidersAccess: true,
	isProvidersLoading: false,
	isProvidersError: false,
	providers: undefined as { name: string; store_raw_request_response?: boolean }[] | undefined,
	provider: "openai",
};

describe("resolveRawJsonNoticeState", () => {
	it("is loading while the provider query is in flight", () => {
		expect(resolveRawJsonNoticeState({ ...base, isProvidersLoading: true })).toBe("loading");
	});

	it("is loading when the query has started but has not delivered data yet", () => {
		expect(resolveRawJsonNoticeState({ ...base, providers: undefined })).toBe("loading");
	});

	it("is unknown - never loading - when the caller cannot read providers (query is skipped)", () => {
		expect(resolveRawJsonNoticeState({ ...base, hasProvidersAccess: false })).toBe("unknown");
	});

	it("is unknown when the provider query failed, rather than loading forever", () => {
		expect(resolveRawJsonNoticeState({ ...base, isProvidersError: true })).toBe("unknown");
	});

	it("is storage-disabled when the provider explicitly disables raw storage", () => {
		expect(
			resolveRawJsonNoticeState({ ...base, providers: [{ name: "openai", store_raw_request_response: false }] }),
		).toBe("storage-disabled");
	});

	it("is unknown when the provider has raw storage enabled", () => {
		expect(
			resolveRawJsonNoticeState({ ...base, providers: [{ name: "openai", store_raw_request_response: true }] }),
		).toBe("unknown");
	});

	it("is unknown when the setting is absent on the provider", () => {
		expect(resolveRawJsonNoticeState({ ...base, providers: [{ name: "openai" }] })).toBe("unknown");
	});

	it("is unknown when this log's provider is not in the list", () => {
		expect(
			resolveRawJsonNoticeState({ ...base, providers: [{ name: "anthropic", store_raw_request_response: false }] }),
		).toBe("unknown");
	});
});
