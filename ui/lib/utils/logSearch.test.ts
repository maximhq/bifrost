import { describe, expect, it } from "vitest";
import { formatLogSearchInput, isLogIdSearch, parseLogSearchInput } from "./logSearch";

const UUID = "018f2c3d-4e5f-4a6b-8c9d-0e1f2a3b4c5d";

describe("parseLogSearchInput", () => {
	it("treats a bare UUID as a request ID", () => {
		expect(parseLogSearchInput(UUID)).toEqual({ request_id: UUID });
	});

	it("trims surrounding whitespace from a pasted id", () => {
		expect(parseLogSearchInput(`  ${UUID}\n`)).toEqual({ request_id: UUID });
	});

	it("accepts an uppercase UUID", () => {
		const upper = UUID.toUpperCase();
		expect(parseLogSearchInput(upper)).toEqual({ request_id: upper });
	});

	it("strips the id: prefix for non-UUID request IDs", () => {
		expect(parseLogSearchInput("id:my-custom-req-7")).toEqual({ request_id: "my-custom-req-7" });
		expect(parseLogSearchInput("ID: my-custom-req-7")).toEqual({ request_id: "my-custom-req-7" });
	});

	it("searches nothing while only the prefix has been typed", () => {
		expect(parseLogSearchInput("id:")).toEqual({});
		expect(parseLogSearchInput("")).toEqual({});
		expect(parseLogSearchInput("   ")).toEqual({});
	});

	it("falls through to content search for free text", () => {
		expect(parseLogSearchInput("summarize this")).toEqual({ content_search: "summarize this" });
		// A non-UUID id without the prefix is indistinguishable from free text.
		expect(parseLogSearchInput("my-custom-req-7")).toEqual({ content_search: "my-custom-req-7" });
	});
});

describe("parseLogSearchInput with a pinned mode", () => {
	it("looks up any string as a request ID", () => {
		expect(parseLogSearchInput("my-custom-req-7", "request_id")).toEqual({ request_id: "my-custom-req-7" });
		expect(parseLogSearchInput(` ${UUID} `, "request_id")).toEqual({ request_id: UUID });
	});

	it("searches any string as content, UUIDs included", () => {
		expect(parseLogSearchInput(UUID, "content")).toEqual({ content_search: UUID });
		expect(parseLogSearchInput("id:abc", "content")).toEqual({ content_search: "id:abc" });
	});

	it("still searches nothing for an empty input", () => {
		expect(parseLogSearchInput("  ", "request_id")).toEqual({});
		expect(parseLogSearchInput("", "content")).toEqual({});
	});
});

describe("formatLogSearchInput", () => {
	it("round-trips a UUID unchanged", () => {
		expect(formatLogSearchInput({ request_id: UUID })).toBe(UUID);
		expect(parseLogSearchInput(formatLogSearchInput({ request_id: UUID }))).toEqual({ request_id: UUID });
	});

	it("re-adds the prefix for a non-UUID id", () => {
		expect(formatLogSearchInput({ request_id: "my-custom-req-7" })).toBe("id:my-custom-req-7");
		expect(parseLogSearchInput("id:my-custom-req-7")).toEqual({ request_id: "my-custom-req-7" });
	});

	it("prefers the id over content search and defaults to empty", () => {
		expect(formatLogSearchInput({ request_id: UUID, content_search: "hello" })).toBe(UUID);
		expect(formatLogSearchInput({ content_search: "hello" })).toBe("hello");
		expect(formatLogSearchInput({})).toBe("");
	});
});

describe("formatLogSearchInput with a pinned mode", () => {
	it("shows only the field the mode targets, without the prefix", () => {
		expect(formatLogSearchInput({ request_id: "my-custom-req-7" }, "request_id")).toBe("my-custom-req-7");
		expect(formatLogSearchInput({ content_search: "hello" }, "request_id")).toBe("");
		expect(formatLogSearchInput({ request_id: UUID, content_search: "hello" }, "content")).toBe("hello");
	});
});

describe("isLogIdSearch", () => {
	it("reports the mode the input will resolve to", () => {
		expect(isLogIdSearch(UUID)).toBe(true);
		expect(isLogIdSearch("id:abc")).toBe(true);
		expect(isLogIdSearch("id:")).toBe(false);
		expect(isLogIdSearch("hello")).toBe(false);
	});

	it("follows a pinned mode over the input's shape", () => {
		expect(isLogIdSearch("hello", "request_id")).toBe(true);
		expect(isLogIdSearch(UUID, "content")).toBe(false);
	});
});