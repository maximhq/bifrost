import { describe, expect, it } from "vitest";
import {
	formatOdinUsage,
	odinErrorDetail,
	odinErrorMessage,
	odinToolLabel,
	parseOdinFrame,
	splitOdinAnswer,
	splitOdinFrames,
} from "./odinStream.utils";

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

	// A running row shimmers and a finished one is ticked, so the same past-tense
	// label cannot serve both: "Queried metrics" beside a spinner reads as already
	// done, which is exactly the wait these rows exist to explain.
	it("uses the present tense while a step is running", () => {
		expect(odinToolLabel("query_metrics", true)).toBe("Querying metrics");
		expect(odinToolLabel("count_logs", true)).toBe("Checking log volume");
		expect(odinToolLabel("count_logs")).toBe("Checked log volume");
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
			expect(odinToolLabel(tool), `${tool} has no label`).not.toBe(tool);
			expect(odinToolLabel(tool, true), `${tool} has no running label`).not.toBe(tool);
		}
	});

	// A tool added server-side should still render legibly instead of blank.
	it("falls back to the raw name for unknown tools", () => {
		expect(odinToolLabel("query_something_new")).toBe("query_something_new");
		expect(odinToolLabel("query_something_new", true)).toBe("query_something_new");
	});
});

describe("odinErrorMessage", () => {
	// The advice lives in the detail rather than the summary: the summary is one
	// line in a transcript, and a line long enough to carry guidance is too long
	// to scan.
	it("offers concrete steps for max_iterations", () => {
		const detail = odinErrorDetail("max_iterations", "");
		expect(detail.summary).toContain("could not settle");
		expect(detail.cause).toContain("research steps");
		expect(detail.suggestions.join(" ")).toContain("one thing at a time");
		expect(detail.suggestions.join(" ")).toContain("Max Iterations");
	});

	it("offers concrete steps for timeout", () => {
		const detail = odinErrorDetail("timeout", "");
		expect(detail.suggestions.join(" ")).toContain("shorter time range");
		expect(detail.suggestions.join(" ")).toContain("Request Timeout");
	});

	// The server's own words are the only part worth pasting into a bug report,
	// so they must survive rather than be paraphrased away.
	it("keeps the raw server message", () => {
		expect(odinErrorDetail("upstream_error", "provider exploded").raw).toBe("provider exploded");
	});

	it("has guidance for every code it recognises", () => {
		for (const code of ["not_configured", "max_iterations", "timeout", "upstream_error", "tool_error"]) {
			const detail = odinErrorDetail(code, "");
			expect(detail.summary, code).not.toBe("");
			expect(detail.cause, code).not.toBe("");
			expect(detail.suggestions.length, code).toBeGreaterThan(0);
		}
	});

	it("falls back to the server message for unknown codes", () => {
		expect(odinErrorMessage("something_else", "upstream exploded")).toBe("upstream exploded");
	});

	it("has a message even when the server sends nothing useful", () => {
		expect(odinErrorMessage(undefined, undefined)).toBe("Something went wrong.");
	});
});
describe("question events", () => {
	it("parses a structured question with options", () => {
		const event = parseOdinFrame(
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
		const event = parseOdinFrame('data: {"type":"done","finish_reason":"question","iterations":1}');
		expect(event?.finish_reason).toBe("question");
	});
});
describe("splitOdinAnswer", () => {
	it("lifts the provenance block out of the answer", () => {
		const { answer, provenance } = splitOdinAnswer(
			"gpt-4o was slowest at 5,106ms p99.\n\n```odin-scope\nWindow: 2026-08-16 00:00-2026-08-17 00:00 UTC\nScope: all users\nFilters: none\n```",
		);
		expect(answer).toBe("gpt-4o was slowest at 5,106ms p99.");
		expect(provenance).toContain("Window:");
		expect(provenance).toContain("Filters: none");
	});

	it("leaves an answer without a block untouched", () => {
		const { answer, provenance } = splitOdinAnswer("Nothing to report.");
		expect(answer).toBe("Nothing to report.");
		expect(provenance).toBeUndefined();
	});

	// A fence anywhere but the end is part of the answer. Lifting it would leave
	// the prose after it stranded with no context.
	it("only lifts a trailing block", () => {
		const content = "```odin-scope\nWindow: x\n```\n\nAnd then some prose.";
		expect(splitOdinAnswer(content).provenance).toBeUndefined();
	});

	// A partially streamed fence must not be treated as complete, or the answer
	// appears to lose its ending mid-stream.
	it("ignores an unterminated block", () => {
		const content = "Answer.\n\n```odin-scope\nWindow: 2026";
		expect(splitOdinAnswer(content).provenance).toBeUndefined();
		expect(splitOdinAnswer(content).answer).toBe(content);
	});

	it("ignores an empty block", () => {
		expect(splitOdinAnswer("Answer.\n\n```odin-scope\n```").provenance).toBeUndefined();
	});

	// Ordinary code blocks are part of the answer.
	it("leaves other fenced blocks alone", () => {
		const content = 'Here:\n\n```json\n{"a":1}\n```';
		expect(splitOdinAnswer(content).provenance).toBeUndefined();
	});
});
describe("formatOdinUsage", () => {
	it("reports tokens and cost together", () => {
		expect(formatOdinUsage({ total_tokens: 12345, cost: { total_cost: 0.42 } })).toBe("12,345 tokens · $0.42");
	});

	// Sub-cent answers are the common case. Two decimals would render "$0.00"
	// and read as free.
	it("keeps a sub-cent cost visible", () => {
		expect(formatOdinUsage({ total_tokens: 100, cost: { total_cost: 0.0012 } })).toBe("100 tokens · $0.0012");
	});

	it("falls back to summing prompt and completion tokens", () => {
		expect(formatOdinUsage({ prompt_tokens: 300, completion_tokens: 200 })).toBe("500 tokens");
	});

	// A "0 tokens" label is worse than none.
	it("returns null when there is nothing to report", () => {
		expect(formatOdinUsage(undefined)).toBeNull();
		expect(formatOdinUsage({})).toBeNull();
		expect(formatOdinUsage({ total_tokens: 0, cost: { total_cost: 0 } })).toBeNull();
	});
});