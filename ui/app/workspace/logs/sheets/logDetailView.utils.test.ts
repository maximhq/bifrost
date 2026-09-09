import { describe, expect, it } from "vitest";
import { parseRoutingDecisionLine, resolveRawJsonNoticeState } from "./logDetailView.utils";

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

describe("parseRoutingDecisionLine", () => {
	it("reads timestamp, engine, level and message from a line written with a level", () => {
		expect(
			parseRoutingDecisionLine("[1756881000842] [core] [warn] - Fallback anthropic/claude-sonnet-4-5 skipped: missing provider config"),
		).toEqual({
			timestamp: 1756881000842,
			engine: "core",
			level: "warn",
			message: "Fallback anthropic/claude-sonnet-4-5 skipped: missing provider config",
		});
	});

	it("parses a line stored before the level was recorded with a null level", () => {
		expect(parseRoutingDecisionLine("[1756881000412] [loadbalancing] - Selected provider openai for model gpt-4o-mini")).toEqual({
			timestamp: 1756881000412,
			engine: "loadbalancing",
			level: null,
			message: "Selected provider openai for model gpt-4o-mini",
		});
	});

	it("does not mistake a bracketed message prefix on an old line for a level", () => {
		expect(parseRoutingDecisionLine("[1756881000412] [governance] - [vk-prod] - allow-list applied")).toMatchObject({
			engine: "governance",
			level: null,
			message: "[vk-prod] - allow-list applied",
		});
	});

	it("keeps the message intact when it starts with a bracket on a levelled line", () => {
		expect(parseRoutingDecisionLine("[1756881000412] [governance] [info] - [vk-prod] - allow-list applied")).toMatchObject({
			level: "info",
			message: "[vk-prod] - allow-list applied",
		});
	});

	it("treats an unknown level token as no level", () => {
		expect(parseRoutingDecisionLine("[1756881000412] [core] [fatal] - boom").level).toBeNull();
	});

	it("falls back to the raw line when it does not match the trail shape", () => {
		expect(parseRoutingDecisionLine("free-form note")).toEqual({ timestamp: null, engine: null, level: null, message: "free-form note" });
	});
});
