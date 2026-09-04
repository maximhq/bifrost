import { describe, expect, it } from "vitest";
import { errorMessage, warpToolLabel, parseWarpFrame, splitWarpFrames } from "./warpStream.utils";

describe("splitWarpFrames", () => {
	it("returns complete frames and keeps the remainder", () => {
		const { frames, rest } = splitWarpFrames("event: delta\ndata: {}\n\nevent: done\ndata: {");
		expect(frames).toEqual(["event: delta\ndata: {}"]);
		expect(rest).toBe("event: done\ndata: {");
	});

	// A chunk boundary can land mid-frame. Dropping the remainder instead of
	// carrying it forward loses whatever token was being written at that moment,
	// which reads as a corrupted answer rather than an error.
	it("reassembles a frame split across two reads", () => {
		const first = splitWarpFrames('event: delta\ndata: {"type":"delta","del');
		expect(first.frames).toHaveLength(0);

		const second = splitWarpFrames(first.rest + 'ta":"hello"}\n\n');
		expect(second.frames).toHaveLength(1);
		expect(parseWarpFrame(second.frames[0])?.delta).toBe("hello");
	});

	it("ignores blank frames", () => {
		const { frames } = splitWarpFrames("\n\n\n\ndata: {}\n\n");
		expect(frames).toEqual(["data: {}"]);
	});
});

describe("parseWarpFrame", () => {
	it("parses an event from the data payload", () => {
		const event = parseWarpFrame('event: tool_call_end\ndata: {"type":"tool_call_end","tool_name":"query_metrics","duration_ms":42}');
		expect(event).toMatchObject({ type: "tool_call_end", tool_name: "query_metrics", duration_ms: 42 });
	});

	// Heartbeats keep the connection honest but carry no data. Treating one as a
	// parse failure would tear down a healthy stream.
	it("returns null for a heartbeat comment", () => {
		expect(parseWarpFrame(": heartbeat")).toBeNull();
	});

	it("returns null for malformed JSON rather than throwing", () => {
		expect(parseWarpFrame("data: {not json")).toBeNull();
	});

	it("returns null for the [DONE] sentinel", () => {
		expect(parseWarpFrame("data: [DONE]")).toBeNull();
	});

	it("returns null when the payload has no type", () => {
		expect(parseWarpFrame('data: {"delta":"orphan"}')).toBeNull();
	});
});

describe("warpToolLabel", () => {
	it("maps known tools to readable labels", () => {
		expect(warpToolLabel("query_metrics")).toBe("Queried metrics");
	});

	// A tool added server-side should still render legibly instead of blank.
	it("falls back to the raw name for unknown tools", () => {
		expect(warpToolLabel("query_something_new")).toBe("query_something_new");
	});
});

describe("errorMessage", () => {
	it("phrases max_iterations as something the user can act on", () => {
		expect(errorMessage("max_iterations", "")).toContain("narrower question");
	});

	it("phrases timeout as something the user can act on", () => {
		expect(errorMessage("timeout", "")).toContain("shorter time range");
	});

	it("falls back to the server message for unknown codes", () => {
		expect(errorMessage("something_else", "upstream exploded")).toBe("upstream exploded");
	});

	it("has a message even when the server sends nothing useful", () => {
		expect(errorMessage(undefined, undefined)).toBe("Something went wrong.");
	});
});