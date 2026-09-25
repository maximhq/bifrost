import { describe, expect, it } from "vitest";
import {
	extractProviderErrorMessage,
	hasNoToolArguments,
	isClientToolCallItem,
	nextSessionLookupStart,
	isResponsesToolCallItem,
	parseRoutingDecisionLine,
	pickNextSessionLog,
	resolveRawJsonNoticeState,
} from "./logDetailView.utils";

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
		expect(resolveRawJsonNoticeState({ ...base, providers: [{ name: "openai", store_raw_request_response: false }] })).toBe(
			"storage-disabled",
		);
	});

	it("is unknown when the provider has raw storage enabled", () => {
		expect(resolveRawJsonNoticeState({ ...base, providers: [{ name: "openai", store_raw_request_response: true }] })).toBe("unknown");
	});

	it("is unknown when the setting is absent on the provider", () => {
		expect(resolveRawJsonNoticeState({ ...base, providers: [{ name: "openai" }] })).toBe("unknown");
	});

	it("is unknown when this log's provider is not in the list", () => {
		expect(resolveRawJsonNoticeState({ ...base, providers: [{ name: "anthropic", store_raw_request_response: false }] })).toBe("unknown");
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

describe("extractProviderErrorMessage", () => {
	it("reads AWS's flat error shape, which is what left the Bedrock invoke path with no message", () => {
		expect(extractProviderErrorMessage({ message: "data retention mode 'default' is not available for this model" })).toBe(
			"data retention mode 'default' is not available for this model",
		);
	});

	it("reads the nested error envelope", () => {
		expect(extractProviderErrorMessage({ type: "error", error: { type: "invalid_request_error", message: "bad request" } })).toBe(
			"bad request",
		);
	});

	it("parses a JSON string body", () => {
		expect(extractProviderErrorMessage('{"message":"rate exceeded"}')).toBe("rate exceeded");
	});

	it("falls back to the raw text when the body is not JSON", () => {
		expect(extractProviderErrorMessage("  upstream connect error  ")).toBe("upstream connect error");
	});

	it("returns null when there is nothing readable", () => {
		expect(extractProviderErrorMessage(null)).toBeNull();
		expect(extractProviderErrorMessage("")).toBeNull();
		expect(extractProviderErrorMessage({ id: "resp_123", output: [] })).toBeNull();
		expect(extractProviderErrorMessage({ message: "   " })).toBeNull();
	});

	// JSON.parse("null") yields null, which typeof still reports as "object" - without an
	// explicit null check the property read below the guard throws and takes the whole
	// detail view down with it.
	it("returns null for a body that is literally JSON null", () => {
		expect(extractProviderErrorMessage("null")).toBeNull();
	});
});
describe("isResponsesToolCallItem", () => {
	// A function_call with `{}` arguments was labelled "Tool Result" and read as a
	// tool that returned nothing.
	it("treats *_call items as calls", () => {
		for (const type of ["function_call", "custom_tool_call", "web_search_call", "mcp_call"]) {
			expect(isResponsesToolCallItem(type)).toBe(true);
		}
	});

	it("does not treat outputs or other items as calls", () => {
		for (const type of ["function_call_output", "custom_tool_call_output", "message", "reasoning", undefined]) {
			expect(isResponsesToolCallItem(type)).toBe(false);
		}
	});
});

describe("isClientToolCallItem", () => {
	it("is true only for calls the caller executes", () => {
		expect(isClientToolCallItem("function_call")).toBe(true);
		expect(isClientToolCallItem("custom_tool_call")).toBe(true);
		// Computer use and local shell run on the caller's machine too, and come
		// back as computer_call_output / local_shell_call_output next request.
		expect(isClientToolCallItem("computer_call")).toBe(true);
		expect(isClientToolCallItem("local_shell_call")).toBe(true);
		expect(isClientToolCallItem("computer_call_output")).toBe(false);
		expect(isClientToolCallItem("web_search_call")).toBe(false);
		expect(isClientToolCallItem("function_call_output")).toBe(false);
		expect(isClientToolCallItem(undefined)).toBe(false);
	});
});

describe("hasNoToolArguments", () => {
	it("is true for empty and empty-object arguments", () => {
		expect(hasNoToolArguments("{}")).toBe(true);
		expect(hasNoToolArguments(" { } ")).toBe(true);
		expect(hasNoToolArguments("")).toBe(true);
	});

	it("is false for arguments that carry something", () => {
		expect(hasNoToolArguments('{"search":"blah"}')).toBe(false);
		expect(hasNoToolArguments("[]")).toBe(false);
		expect(hasNoToolArguments("null")).toBe(false);
		expect(hasNoToolArguments("not json")).toBe(false);
		expect(hasNoToolArguments(undefined)).toBe(false);
	});
});

describe("pickNextSessionLog", () => {
	const current = { id: "b", timestamp: "2026-09-21T14:07:16.958Z" };

	it("skips the current row and returns the first later one", () => {
		const logs = [
			{ id: "b", timestamp: "2026-09-21T14:07:16.958Z" },
			{ id: "c", timestamp: "2026-09-21T14:07:20.254Z" },
			{ id: "d", timestamp: "2026-09-21T14:07:25.000Z" },
		];
		expect(pickNextSessionLog(logs, current)?.id).toBe("c");
	});

	it("skips a different row that shares the current timestamp", () => {
		const logs = [
			{ id: "a", timestamp: "2026-09-21T14:07:16.958Z" },
			{ id: "c", timestamp: "2026-09-21T14:07:20.254Z" },
		];
		expect(pickNextSessionLog(logs, current)?.id).toBe("c");
	});

	it("returns undefined when nothing follows", () => {
		expect(pickNextSessionLog([{ ...current }], current)).toBeUndefined();
		expect(pickNextSessionLog([], current)).toBeUndefined();
	});

	// Logs carry microseconds; JavaScript dates stop at milliseconds, so a later
	// request in the same millisecond compared equal and was never picked.
	it("picks a later row within the same millisecond", () => {
		const precise = { id: "b", timestamp: "2026-09-21T14:07:16.958123Z" };
		const logs = [
			{ id: "a", timestamp: "2026-09-21T14:07:16.958100Z" },
			{ id: "c", timestamp: "2026-09-21T14:07:16.958500Z" },
		];
		expect(pickNextSessionLog(logs, precise)?.id).toBe("c");
	});

	it("compares across time zone offsets", () => {
		const logs = [{ id: "c", timestamp: "2026-09-21T19:37:16.958124+05:30" }];
		expect(pickNextSessionLog(logs, { id: "b", timestamp: "2026-09-21T14:07:16.958123Z" })?.id).toBe("c");
	});

	it("never picks a row whose timestamp it cannot parse", () => {
		expect(pickNextSessionLog([{ id: "c", timestamp: "not a time" }], current)).toBeUndefined();
		expect(pickNextSessionLog([{ id: "c", timestamp: "2026-09-21T14:07:20Z" }], { id: "b", timestamp: "not a time" })).toBeUndefined();
	});
});
// The next-request lookup started at the current row's timestamp and fetched two
// rows, so a sibling logged in the same millisecond filled the page and the link
// never appeared. The next request cannot start before this response returns, so
// the lookup starts just after it.
describe("nextSessionLookupStart", () => {
	// One nanosecond, the finest a log timestamp carries: SQLite keeps Go's full
	// nanoseconds, so a microsecond skipped a later request within it. Postgres
	// truncates the bound to its microseconds, and the current row that brings
	// back is skipped by pickNextSessionLog.
	it("starts one nanosecond after the current request", () => {
		expect(nextSessionLookupStart("2026-09-24T10:08:52.666Z")).toBe("2026-09-24T10:08:52.666000001Z");
		expect(nextSessionLookupStart("2026-09-24T10:08:52.666123Z")).toBe("2026-09-24T10:08:52.666123001Z");
		expect(nextSessionLookupStart("2026-09-24T10:08:52.666123456Z")).toBe("2026-09-24T10:08:52.666123457Z");
	});

	it("never skips a later request within the same microsecond", () => {
		const current = { id: "b", timestamp: "2026-09-24T10:08:52.666123456Z" };
		const start = nextSessionLookupStart(current.timestamp);
		const later = { id: "c", timestamp: "2026-09-24T10:08:52.666123789Z" };
		expect(later.timestamp >= start).toBe(true);
		expect(pickNextSessionLog([later], current)?.id).toBe("c");
	});

	it("carries into the next second and normalises the offset to UTC", () => {
		expect(nextSessionLookupStart("2026-09-24T10:08:52.999999999+05:30")).toBe("2026-09-24T04:38:53Z");
	});

	it("keeps a timestamp it cannot parse as it is", () => {
		expect(nextSessionLookupStart("not a time")).toBe("not a time");
	});
});