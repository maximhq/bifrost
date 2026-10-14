import { describe, expect, it } from "vitest";

import { LiveTranscriptLine, ResponsesMessage } from "@/lib/types/logs";

import { shortId, toolNamesByCall, visibleDelegationItems, visibleTranscript } from "./liveSessionView.utils";

const transcript: LiveTranscriptLine[] = [
	{ role: "user", text: "What is on my calendar?" },
	{ role: "assistant", text: "A design review at ten." },
];

const items: ResponsesMessage[] = [
	{ type: "reasoning", summary: [{ type: "summary_text", text: "Need the calendar." }] } as ResponsesMessage,
	{ type: "function_call", call_id: "call_1", name: "get_calendar", arguments: '{"date":"today"}' },
	{ type: "function_call_output", call_id: "call_1", output: '{"events":["Design review at 10:00"]}' },
	{ type: "message", role: "assistant", content: [{ type: "output_text", text: "You have a design review at 10." }] },
];

describe("visibleTranscript", () => {
	it("shows every line when no filter is set", () => {
		expect(visibleTranscript(transcript)).toEqual(transcript);
	});

	it("keeps only the speakers the filter names", () => {
		expect(visibleTranscript(transcript, new Set(["assistant"]))).toEqual([transcript[1]]);
		expect(visibleTranscript(transcript, new Set(["tool"]))).toEqual([]);
	});
});

describe("visibleDelegationItems", () => {
	it("shows every item when no filter is set", () => {
		expect(visibleDelegationItems(items)).toEqual(items);
	});

	it("keeps the call and its result under the tool role", () => {
		expect(visibleDelegationItems(items, new Set(["tool"])).map((item) => item.type)).toEqual(["function_call", "function_call_output"]);
	});

	it("keeps the answer and the reasoning under their own roles", () => {
		expect(visibleDelegationItems(items, new Set(["assistant"])).map((item) => item.type)).toEqual(["message"]);
		expect(visibleDelegationItems(items, new Set(["reasoning"])).map((item) => item.type)).toEqual(["reasoning"]);
	});
});

describe("shortId", () => {
	it("keeps a short id whole", () => {
		expect(shortId("resp_answer")).toBe("resp_answer");
	});

	it("keeps the head and tail of a long id", () => {
		expect(shortId("resp_08dc8d98a2c726b5006accacf2fcb087d0bffb7d74f840f426")).toBe("resp_08dc…f426");
	});
});

describe("toolNamesByCall", () => {
	it("names a result row after the call it answers", () => {
		expect(toolNamesByCall(items).get("call_1")).toBe("get_calendar");
	});

	it("ignores items that are not function calls", () => {
		expect(toolNamesByCall(items.filter((item) => item.type !== "function_call")).size).toBe(0);
	});
});