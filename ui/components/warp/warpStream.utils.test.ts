import { describe, expect, it } from "vitest";
import {
	decodeTurnError,
	encodeTurnError,
	errorMessage,
	historyForRequest,
	formatWarpUsage,
	isEncodedTurnError,
	parseWarpFrame,
	splitWarpAnswer,
	splitWarpFrames,
	warpErrorDetail,
	warpToolLabel,
	warpToolStatusLabel,
} from "./warpStream.utils";

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
		// tool_id is part of the payload: the agent always sends it (it is how
		// applyEvent matches the end frame to its start), and isUsableWarpEvent
		// drops a tool_call_end without one.
		const event = parseWarpFrame(
			'event: tool_call_end\ndata: {"type":"tool_call_end","tool_id":"t1","tool_name":"query_metrics","duration_ms":42}',
		);
		expect(event).toMatchObject({ type: "tool_call_end", tool_id: "t1", tool_name: "query_metrics", duration_ms: 42 });
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

	// A running row shimmers and a finished one is ticked, so the same past-tense
	// label cannot serve both: "Queried metrics" beside a spinner reads as already
	// done, which is exactly the wait these rows exist to explain.
	it("uses the present tense while a step is running", () => {
		expect(warpToolLabel("query_metrics", true)).toBe("Querying metrics");
		expect(warpToolLabel("count_logs", true)).toBe("Checking log volume");
		expect(warpToolLabel("count_logs")).toBe("Checked log volume");
	});

	// Every tool the agent can call needs a label. A raw name like "count_logs"
	// leaking into the transcript is the symptom this guards against.
	it("labels every tool the agent exposes", () => {
		const tools = [
			"count_logs",
			"query_logs",
			"get_log_detail",
			"query_metrics",
			"query_user_usage",
			"query_virtual_key_usage",
			"query_model_performance",
			"describe_filter_space",
			"describe_scope",
			"ask_user",
		];
		for (const tool of tools) {
			expect(warpToolLabel(tool), `${tool} has no label`).not.toBe(tool);
			expect(warpToolLabel(tool, true), `${tool} has no running label`).not.toBe(tool);
		}
	});

	// A tool added server-side should still render legibly instead of blank.
	it("falls back to the raw name for unknown tools", () => {
		expect(warpToolLabel("query_something_new")).toBe("query_something_new");
		expect(warpToolLabel("query_something_new", true)).toBe("query_something_new");
	});
});

describe("errorMessage", () => {
	// The advice lives in the detail rather than the summary: the summary is one
	// line in a transcript, and a line long enough to carry guidance is too long
	// to scan.
	it("offers concrete steps for max_iterations", () => {
		const detail = warpErrorDetail("max_iterations", "");
		expect(detail.summary).toContain("could not settle");
		expect(detail.cause).toContain("research steps");
		expect(detail.suggestions.join(" ")).toContain("one thing at a time");
		expect(detail.suggestions.join(" ")).toContain("Max Iterations");
	});

	it("offers concrete steps for timeout", () => {
		const detail = warpErrorDetail("timeout", "");
		expect(detail.suggestions.join(" ")).toContain("shorter time range");
		expect(detail.suggestions.join(" ")).toContain("Request Timeout");
	});

	// The server's own words are the only part worth pasting into a bug report,
	// so they must survive rather than be paraphrased away.
	it("keeps the raw server message", () => {
		expect(warpErrorDetail("upstream_error", "provider exploded").raw).toBe("provider exploded");
	});

	it("has guidance for every code it recognises", () => {
		for (const code of ["not_configured", "max_iterations", "timeout", "upstream_error", "tool_error"]) {
			const detail = warpErrorDetail(code, "");
			expect(detail.summary, code).not.toBe("");
			expect(detail.cause, code).not.toBe("");
			expect(detail.suggestions.length, code).toBeGreaterThan(0);
		}
	});

	it("falls back to the server message for unknown codes", () => {
		expect(errorMessage("something_else", "upstream exploded")).toBe("upstream exploded");
	});

	it("has a message even when the server sends nothing useful", () => {
		expect(errorMessage(undefined, undefined)).toBe("Something went wrong.");
	});
});
// The SSE spec allows CRLF line endings and makes the space after `data:`
// optional. A stream from a proxy that normalises to CRLF, or a server that
// omits the space, parsed to nothing at all - so the chat completed with an
// empty answer and no error to explain it.
describe("SSE wire tolerance", () => {
	it("splits frames delimited by CRLF", () => {
		const { frames, rest } = splitWarpFrames('event: delta\r\ndata: {"type":"delta","delta":"hi"}\r\n\r\nevent: done\r\ndata: {');
		expect(frames).toHaveLength(1);
		expect(parseWarpFrame(frames[0])?.delta).toBe("hi");
		expect(rest).toContain("event: done");
	});

	it("parses a data field with no space after the colon", () => {
		expect(parseWarpFrame('data:{"type":"delta","delta":"hi"}')?.delta).toBe("hi");
	});

	it("parses a CRLF frame whose data field has no space", () => {
		expect(parseWarpFrame('event: delta\r\ndata:{"type":"delta","delta":"hi"}')?.delta).toBe("hi");
	});

	it("still treats [DONE] as a sentinel without the space", () => {
		expect(parseWarpFrame("data:[DONE]")).toBeNull();
	});

	it("keeps multi-line data joined on newlines", () => {
		expect(parseWarpFrame('data:{"type":"delta",\r\ndata:"delta":"hi"}')?.delta).toBe("hi");
	});
});

// The turn error is an encoded `code:message` pair. Producers that emitted a
// bare message with no colon had the whole message read back as a code, which
// matched nothing and rendered the generic "Something went wrong." - losing the
// status line, the network error, and every other detail worth showing.
describe("turn error encoding", () => {
	it("round-trips a coded error", () => {
		const { code, message } = decodeTurnError(encodeTurnError("not_configured", ""));
		expect(code).toBe("not_configured");
		expect(errorMessage(code, message)).toBe("Warp is not configured yet.");
	});

	it("keeps a message that has no code", () => {
		const encoded = encodeTurnError(undefined, "Warp request failed (500)");
		const { code, message } = decodeTurnError(encoded);
		expect(code).toBe("");
		expect(message).toBe("Warp request failed (500)");
		expect(errorMessage(code, message)).toBe("Warp request failed (500)");
	});

	it("keeps colons inside the message intact", () => {
		const { code, message } = decodeTurnError(encodeTurnError(undefined, "connect: connection refused"));
		expect(code).toBe("");
		expect(message).toBe("connect: connection refused");
	});

	it("decodes a legacy bare message as a message, not a code", () => {
		const { code, message } = decodeTurnError("Warp returned no response body");
		expect(code).toBe("");
		expect(message).toBe("Warp returned no response body");
		expect(errorMessage(code, message)).toBe("Warp returned no response body");
	});
});
// A chunk boundary can land between the CR and the LF of a single CRLF.
// Normalising the trailing CR to a newline immediately makes the next chunk's
// LF look like a second newline, so the buffer reads as a frame boundary and
// the half-written event is parsed - and discarded - as though it were whole.
describe("splitWarpFrames CR boundaries", () => {
	it("holds back a trailing CR until the next read decides what it is", () => {
		// One frame whose data spans two lines, split by the network exactly
		// between the CR and the LF of the separator between them. Converting
		// that lone CR to a newline immediately makes the incoming LF look like a
		// second newline, and the single frame is torn into two - each half
		// unparseable, so the delta is dropped with no error.
		const first = splitWarpFrames('data: {"type":"delta",\r');
		expect(first.frames).toHaveLength(0);

		const second = splitWarpFrames(first.rest + '\ndata: "delta":"hello"}\r\n\r\n');
		expect(second.frames).toHaveLength(1);
		expect(parseWarpFrame(second.frames[0])?.delta).toBe("hello");
	});

	it("still splits a frame whose terminator arrives whole", () => {
		const { frames } = splitWarpFrames('data: {"type":"delta","delta":"hi"}\r\n\r\n');
		expect(frames).toHaveLength(1);
		expect(parseWarpFrame(frames[0])?.delta).toBe("hi");
	});
});

// Every producer of a turn error has to go through the same encoding, or the
// decoder reads whatever sits before the first colon as a code. A server error
// frame with a message like "connect: connection refused" and no code would
// otherwise lose the "connect:" half and render under an unknown code.
describe("isEncodedTurnError", () => {
	it("recognises what encodeTurnError produces", () => {
		expect(isEncodedTurnError(encodeTurnError(undefined, "Warp request failed (500)"))).toBe(true);
		expect(isEncodedTurnError(encodeTurnError("timeout", ""))).toBe(true);
		expect(isEncodedTurnError(encodeTurnError("upstream_error", "boom"))).toBe(true);
	});

	it("does not mistake a colon in a message for a code", () => {
		expect(isEncodedTurnError("connect: connection refused")).toBe(false);
		expect(isEncodedTurnError("TypeError: Failed to fetch")).toBe(false);
		expect(isEncodedTurnError("Warp returned no response body")).toBe(false);
	});

	it("round-trips a colon-bearing message that was encoded", () => {
		const encoded = encodeTurnError(undefined, "connect: connection refused");
		expect(isEncodedTurnError(encoded)).toBe(true);
		expect(decodeTurnError(encoded).message).toBe("connect: connection refused");
	});
});
describe("splitWarpFrames lone-CR delimiters", () => {
	it("emits a frame that a CR already terminated", () => {
		// "\r\r" and "\n\r" are both complete delimiters. Holding the final CR back
		// stranded the finished frame in `rest` until a chunk that may never come.
		expect(splitWarpFrames('data: {"type":"delta"}\r\r').frames).toEqual(['data: {"type":"delta"}']);
		expect(splitWarpFrames('data: {"type":"delta"}\n\r').frames).toEqual(['data: {"type":"delta"}']);
	});

	it("still holds back a CR that could be half of a CRLF", () => {
		// Here the CR follows ordinary text, so only the next read says whether it
		// is "\r\n" (one newline) or "\r\r" (a delimiter).
		const first = splitWarpFrames('data: {"type":"delta"}\r');
		expect(first.frames).toEqual([]);
		expect(first.rest.endsWith("\r")).toBe(true);
		// A buffer that is only a CR has no preceding character, so it stays held.
		expect(splitWarpFrames("\r").rest).toBe("\r");
		// And the pair resolves once the LF arrives.
		expect(splitWarpFrames(first.rest + '\ndata: {"type":"done"}\n\n').frames).toEqual(['data: {"type":"delta"}\ndata: {"type":"done"}']);
	});
});

describe("warpToolStatusLabel", () => {
	// The icons alone carry the tool-call state only through shape and color,
	// which a screen reader cannot see - this label is what the sr-only span
	// beside them announces.
	it("names each tool-call state", () => {
		expect(warpToolStatusLabel({})).toBe("In progress");
		expect(warpToolStatusLabel({ durationMs: undefined, failed: true })).toBe("In progress");
		expect(warpToolStatusLabel({ durationMs: 120 })).toBe("Completed");
		expect(warpToolStatusLabel({ durationMs: 120, failed: false })).toBe("Completed");
		expect(warpToolStatusLabel({ durationMs: 0, failed: true })).toBe("Failed");
	});
});

describe("historyForRequest", () => {
	it("drops turns that carry no content", () => {
		// A failed turn is stored with empty content so the transcript can show the
		// error, but the wire format is role and content only - replaying it told
		// the model it had once answered with nothing.
		expect(
			historyForRequest([
				{ role: "user", content: "what failed?" },
				{ role: "assistant", content: "" },
				{ role: "assistant", content: "   " },
				{ role: "user", content: "try again" },
			]),
		).toEqual([
			{ role: "user", content: "what failed?" },
			{ role: "user", content: "try again" },
		]);
		// Extra fields survive: the caller maps the question marker onto the turns
		// this returns, so filtering must not flatten them away.
		expect(historyForRequest([{ role: "assistant", content: "pick one", question: true }])).toEqual([
			{ role: "assistant", content: "pick one", question: true },
		]);
	});
});

describe("question events", () => {
	it("parses a structured question with options", () => {
		const event = parseWarpFrame(
			'event: question\ndata: {"type":"question","question":{"question":"Which period?","kind":"time_range","options":[{"label":"Last 7 days","hint":"-7d"},{"label":"Last 30 days","hint":"-30d"}],"allow_other":true}}',
		);
		expect(event?.type).toBe("question");
		expect(event?.question?.question).toBe("Which period?");
		expect(event?.question?.options).toHaveLength(2);
		// The hint is what goes back, so the answer needs no re-interpretation.
		expect(event?.question?.options[0].hint).toBe("-7d");
		expect(event?.question?.allow_other).toBe(true);
	});

	// A question ends the turn, so the client has to tell it apart from a
	// finished answer or it will render the thread as complete.
	it("marks the done frame that follows a question", () => {
		const event = parseWarpFrame('data: {"type":"done","finish_reason":"question","iterations":1}');
		expect(event?.finish_reason).toBe("question");
	});
});
describe("splitWarpAnswer", () => {
	it("lifts the provenance block out of the answer", () => {
		const { answer, provenance } = splitWarpAnswer(
			"gpt-4o was slowest at 5,106ms p99.\n\n```warp-scope\nWindow: 2026-08-16 00:00-2026-08-17 00:00 UTC\nScope: all users\nFilters: none\n```",
		);
		expect(answer).toBe("gpt-4o was slowest at 5,106ms p99.");
		expect(provenance).toContain("Window:");
		expect(provenance).toContain("Filters: none");
	});

	it("leaves an answer without a block untouched", () => {
		const { answer, provenance } = splitWarpAnswer("Nothing to report.");
		expect(answer).toBe("Nothing to report.");
		expect(provenance).toBeUndefined();
	});

	// A fence anywhere but the end is part of the answer. Lifting it would leave
	// the prose after it stranded with no context.
	it("only lifts a trailing block", () => {
		const content = "```warp-scope\nWindow: x\n```\n\nAnd then some prose.";
		expect(splitWarpAnswer(content).provenance).toBeUndefined();
	});

	// A partially streamed fence must not be treated as complete, or the answer
	// appears to lose its ending mid-stream.
	it("ignores an unterminated block", () => {
		const content = "Answer.\n\n```warp-scope\nWindow: 2026";
		expect(splitWarpAnswer(content).provenance).toBeUndefined();
		expect(splitWarpAnswer(content).answer).toBe(content);
	});

	it("ignores an empty block", () => {
		expect(splitWarpAnswer("Answer.\n\n```warp-scope\n```").provenance).toBeUndefined();
	});

	// Ordinary code blocks are part of the answer.
	it("leaves other fenced blocks alone", () => {
		const content = 'Here:\n\n```json\n{"a":1}\n```';
		expect(splitWarpAnswer(content).provenance).toBeUndefined();
	});
});
describe("formatWarpUsage", () => {
	it("reports tokens and cost together", () => {
		expect(formatWarpUsage({ total_tokens: 12345, cost: { total_cost: 0.42 } })).toBe("12,345 tokens · $0.42");
	});

	// Sub-cent answers are the common case. Two decimals would render "$0.00"
	// and read as free.
	it("keeps a sub-cent cost visible", () => {
		expect(formatWarpUsage({ total_tokens: 100, cost: { total_cost: 0.0012 } })).toBe("100 tokens · $0.0012");
	});

	it("falls back to summing prompt and completion tokens", () => {
		expect(formatWarpUsage({ prompt_tokens: 300, completion_tokens: 200 })).toBe("500 tokens");
	});

	// A "0 tokens" label is worse than none.
	it("returns null when there is nothing to report", () => {
		expect(formatWarpUsage(undefined)).toBeNull();
		expect(formatWarpUsage({})).toBeNull();
		expect(formatWarpUsage({ total_tokens: 0, cost: { total_cost: 0 } })).toBeNull();
	});
});
// A real but tiny cost must not print as $0.0000, which reads as free. The
// figure is the only place Warp's own spend is reported, so rounding it away is
// worse than an approximate marker.
describe("formatWarpUsage sub-cent costs", () => {
	it("marks a positive cost below the display threshold", () => {
		const label = formatWarpUsage({ total_tokens: 10, cost: { total_cost: 0.00001 } });
		expect(label).not.toContain("$0.0000");
		expect(label).toContain("<$0.0001");
	});

	it("still shows costs the format can express", () => {
		// Below a cent gets four places, at or above it gets two - unchanged.
		expect(formatWarpUsage({ total_tokens: 10, cost: { total_cost: 0.0012 } })).toContain("$0.0012");
		expect(formatWarpUsage({ total_tokens: 10, cost: { total_cost: 0.0123 } })).toContain("$0.01");
		expect(formatWarpUsage({ total_tokens: 10, cost: { total_cost: 1.5 } })).toContain("$1.50");
	});
});