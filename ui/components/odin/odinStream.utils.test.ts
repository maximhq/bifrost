import { describe, expect, it } from "vitest";
import { odinErrorMessage, odinToolLabel, parseOdinFrame, splitOdinFrames } from "./odinStream.utils";

describe("splitOdinFrames", () => {
	it("returns complete frames and keeps the remainder", () => {
		const { frames, rest } = splitOdinFrames("event: delta\ndata: {}\n\nevent: done\ndata: {");
		expect(frames).toEqual(["event: delta\ndata: {}"]);
		expect(rest).toBe("event: done\ndata: {");
	});

	// A chunk boundary can land mid-frame. Dropping the remainder instead of
	// carrying it forward loses whatever token was being written at that moment,
	// which reads as a corrupted answer rather than an error.
	it("reassembles a frame split across two reads", () => {
		const first = splitOdinFrames('event: delta\ndata: {"type":"delta","del');
		expect(first.frames).toHaveLength(0);

		const second = splitOdinFrames(first.rest + 'ta":"hello"}\n\n');
		expect(second.frames).toHaveLength(1);
		expect(parseOdinFrame(second.frames[0])?.delta).toBe("hello");
	});

	it("ignores blank frames", () => {
		const { frames } = splitOdinFrames("\n\n\n\ndata: {}\n\n");
		expect(frames).toEqual(["data: {}"]);
	});
});

describe("parseOdinFrame", () => {
	it("parses an event from the data payload", () => {
		const event = parseOdinFrame('event: tool_call_end\ndata: {"type":"tool_call_end","tool_name":"query_metrics","duration_ms":42}');
		expect(event).toMatchObject({ type: "tool_call_end", tool_name: "query_metrics", duration_ms: 42 });
	});

	// Heartbeats keep the connection honest but carry no data. Treating one as a
	// parse failure would tear down a healthy stream.
	it("returns null for a heartbeat comment", () => {
		expect(parseOdinFrame(": heartbeat")).toBeNull();
	});

	it("returns null for malformed JSON rather than throwing", () => {
		expect(parseOdinFrame("data: {not json")).toBeNull();
	});

	it("returns null for the [DONE] sentinel", () => {
		expect(parseOdinFrame("data: [DONE]")).toBeNull();
	});

	it("returns null when the payload has no type", () => {
		expect(parseOdinFrame('data: {"delta":"orphan"}')).toBeNull();
	});
});

describe("odinToolLabel", () => {
	it("maps known tools to readable labels", () => {
		expect(odinToolLabel("query_metrics")).toBe("Queried metrics");
	});

	// A tool added server-side should still render legibly instead of blank.
	it("falls back to the raw name for unknown tools", () => {
		expect(odinToolLabel("query_something_new")).toBe("query_something_new");
	});
});

describe("odinErrorMessage", () => {
	it("phrases max_iterations as something the user can act on", () => {
		expect(odinErrorMessage("max_iterations", "")).toContain("narrower question");
	});

	it("phrases timeout as something the user can act on", () => {
		expect(odinErrorMessage("timeout", "")).toContain("shorter time range");
	});

	it("falls back to the server message for unknown codes", () => {
		expect(odinErrorMessage("something_else", "upstream exploded")).toBe("upstream exploded");
	});

	it("has a message even when the server sends nothing useful", () => {
		expect(odinErrorMessage(undefined, undefined)).toBe("Something went wrong.");
	});
});