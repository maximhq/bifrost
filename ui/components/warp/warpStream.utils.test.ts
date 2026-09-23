import { describe, expect, it } from "vitest";
import {
	WARP_COMPOSER_TESTID,
	decodeTurnError,
	encodeTurnError,
	errorMessage,
	historyForRequest,
	formatWarpUsage,
	indexStatusLabel,
	isEncodedTurnError,
	isInternalWarpLink,
	isPartialAnswer,
	isPlainLeftClick,
	isTypingInto,
	isWarpQuestionFinish,
	parseWarpFrame,
	shouldDrainQueue,
	splitWarpAnswer,
	splitWarpFrames,
	turnsFromStoredMessages,
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
		expect(warpToolLabel("semantic_search_logs", true)).toBe("Performing vector search");
		expect(warpToolLabel("semantic_search_logs")).toBe("Performed vector search");
	});

	// Every tool the agent can call needs a label. A raw name like "count_logs"
	// leaking into the transcript is the symptom this guards against.
	it("labels every tool the agent exposes", () => {
		const tools = [
			"semantic_search_logs",
			"count_logs",
			"query_logs",
			"get_log_detail",
			"get_request_trace",
			"query_metrics",
			"query_usage_by",
			"query_model_performance",
			"describe_filter_space",
			"describe_virtual_key",
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

// The server marks an answer given on its last research step as "partial". The
// transcript has to show that, or a half-checked figure reads as a settled one.
describe("isPartialAnswer", () => {
	it("recognises the partial finish reason and nothing else", () => {
		expect(isPartialAnswer("partial")).toBe(true);
		expect(isPartialAnswer("stop")).toBe(false);
		expect(isPartialAnswer("question")).toBe(false);
		expect(isPartialAnswer(undefined)).toBe(false);
	});

	it("reads it off a done frame", () => {
		const event = parseWarpFrame('data: {"type":"done","finish_reason":"partial","iterations":8}');
		expect(event && isPartialAnswer(event.finish_reason)).toBe(true);
	});
});

// Warp's answers link into the dashboard with root-relative paths. Those must
// navigate in-app, keeping the tray open; anything else is a real external link.
describe("isInternalWarpLink", () => {
	it("accepts root-relative dashboard paths only", () => {
		expect(isInternalWarpLink("/workspace/logs?selected_log=abc")).toBe(true);
		expect(isInternalWarpLink("/workspace/logs")).toBe(true);
		expect(isInternalWarpLink("//evil.example/x")).toBe(false);
		expect(isInternalWarpLink("https://github.com/maximhq/bifrost/issues/new")).toBe(false);
		expect(isInternalWarpLink("javascript:alert(1)")).toBe(false);
		expect(isInternalWarpLink(undefined)).toBe(false);
	});
});

// A reopened thread must look like it did live: the same tool rows, the same
// error card, the same partial note and the same cost line.
describe("turnsFromStoredMessages", () => {
	it("maps stored messages onto transcript turns", () => {
		const turns = turnsFromStoredMessages([
			{ role: "user", content: "what did we spend?", created_at: "2026-09-05T00:00:00Z" },
			{
				role: "assistant",
				content: "About $12.",
				tool_calls: [
					{ name: "query_metrics", duration_ms: 12 },
					{ name: "count_logs", duration_ms: 3, failed: true },
				],
				finish_reason: "partial",
				total_tokens: 120,
				cost: 0.0123,
				created_at: "2026-09-05T00:00:01Z",
			},
			{ role: "assistant", content: "", error: "upstream_error:boom", created_at: "2026-09-05T00:00:02Z" },
		]);
		expect(turns).toHaveLength(3);
		expect(turns[0]).toMatchObject({ role: "user", content: "what did we spend?" });
		expect(turns[1]).toMatchObject({ role: "assistant", content: "About $12.", partial: true });
		expect(turns[1].toolCalls).toEqual([
			{ id: "stored-1-0", name: "query_metrics", durationMs: 12, failed: undefined },
			{ id: "stored-1-1", name: "count_logs", durationMs: 3, failed: true },
		]);
		expect(turns[1].usage).toEqual({ total_tokens: 120, cost: { total_cost: 0.0123 } });
		expect(turns[2]).toMatchObject({ role: "assistant", content: "", error: "upstream_error:boom" });
		expect(turns[2].usage).toBeUndefined();
		expect(turns[2].partial).toBeUndefined();
	});

	// A reopened question turn must carry the same selectable card the live
	// turn showed. The stored structured question restores the options with
	// their hints; without one, the bare content is still shown as a question
	// so the reply is not misfiled as an answer.
	it("restores a stored question's options and hints", () => {
		const turns = turnsFromStoredMessages([
			{
				role: "assistant",
				content: "Whose traffic do you mean?",
				finish_reason: "question",
				question: {
					question: "Whose traffic do you mean?",
					options: [{ label: "Platform team", hint: "team:platform" }],
					allow_other: true,
					kind: "scope",
				},
				created_at: "2026-09-05T00:00:00Z",
			},
			{ role: "assistant", content: "Which window?", finish_reason: "question", created_at: "2026-09-05T00:00:01Z" },
		]);
		expect(turns[0].question).toEqual({
			question: "Whose traffic do you mean?",
			options: [{ label: "Platform team", hint: "team:platform" }],
			allow_other: true,
			kind: "scope",
		});
		// Legacy rows without the stored question still come back as a question.
		expect(turns[1].question).toEqual({ question: "Which window?", options: [] });
	});
});

// The tray's index chip is one glance: is semantic search usable right now.
describe("indexStatusLabel", () => {
	it("names each state and shows progress while indexing", () => {
		expect(indexStatusLabel({ state: "ready", vector_store_connected: true, embedding_configured: true })).toEqual({
			label: "Index ready",
			tone: "ok",
		});
		expect(indexStatusLabel({ state: "unavailable", vector_store_connected: false, embedding_configured: true })).toEqual({
			label: "No vector store",
			tone: "error",
		});
		expect(indexStatusLabel({ state: "not_configured", vector_store_connected: true, embedding_configured: false })).toEqual({
			label: "Search not set up",
			tone: "muted",
		});
		expect(
			indexStatusLabel({
				state: "failed",
				vector_store_connected: true,
				embedding_configured: true,
				backfill: {
					id: "job-1",
					status: "failed",
					total: 5000,
					scanned: 100,
					indexed: 0,
					skipped: 0,
					failed: 100,
					last_error: "no keys found",
				},
			}),
		).toEqual({ label: "Indexing failed", tone: "error", detail: "no keys found" });
		expect(
			indexStatusLabel({
				state: "indexing",
				vector_store_connected: true,
				embedding_configured: true,
				backfill: { id: "job-2", status: "running", total: 200, scanned: 50, indexed: 40, skipped: 10, failed: 0 },
			}),
		).toEqual({ label: "Indexing 25%", tone: "busy" });
		expect(
			indexStatusLabel({
				state: "indexing",
				vector_store_connected: true,
				embedding_configured: true,
				backfill: { id: "job-3", status: "pending", total: 0, scanned: 0, indexed: 0, skipped: 0, failed: 0 },
			}),
		).toEqual({ label: "Indexing", tone: "busy" });
	});

	it("does not read the idle response as a job", () => {
		// The idle body is zeroed, not absent: total 0 and scanned 0 with no id.
		// Read as a job it renders a 0% progress chip for a run that never
		// started, and hides the fact that indexing simply has not been asked for.
		expect(
			indexStatusLabel({
				state: "indexing",
				vector_store_connected: true,
				embedding_configured: true,
				backfill: { status: "idle" },
			}),
		).toEqual({ label: "Indexing", tone: "busy" });
	});
});

// The question card's shortcuts are document-level because the composer has
// focus when the card appears. They must still work in that state - an empty
// composer is not "typing" - and must yield the moment someone starts writing
// their own answer.
describe("isTypingInto", () => {
	const composer = (value: string) => ({ tagName: "TEXTAREA", value, dataset: { testid: WARP_COMPOSER_TESTID } });

	it("treats an empty composer as not typing", () => {
		expect(isTypingInto(composer(""))).toBe(false);
		expect(isTypingInto(composer("   "))).toBe(false);
	});
	it("treats a composer with text, or any input, as typing", () => {
		expect(isTypingInto(composer("all cust"))).toBe(true);
		expect(isTypingInto({ tagName: "INPUT", value: "" })).toBe(true);
		expect(isTypingInto({ tagName: "INPUT", value: "x" })).toBe(true);
	});
	// Another textarea on the page belongs to someone else. Treating it as a
	// shortcut target let the question card swallow a character that matched an
	// option letter while they were writing into an unrelated field.
	it("treats any other textarea as typing, even when empty", () => {
		expect(isTypingInto({ tagName: "TEXTAREA", value: "" })).toBe(true);
		expect(isTypingInto({ tagName: "TEXTAREA", value: "", dataset: { testid: "some-other-field" } })).toBe(true);
	});
	it("treats anything else as not typing", () => {
		expect(isTypingInto({ tagName: "DIV" })).toBe(false);
		expect(isTypingInto(null)).toBe(false);
	});
});

// Messages typed while Warp is thinking wait their turn. One goes out per
// finished turn, never two at once.
describe("shouldDrainQueue", () => {
	it("sends only on the streaming-to-idle transition", () => {
		expect(shouldDrainQueue(true, false, 2, false)).toBe(true);
		expect(shouldDrainQueue(false, false, 2, false)).toBe(false);
		expect(shouldDrainQueue(true, true, 2, false)).toBe(false);
		expect(shouldDrainQueue(true, false, 0, false)).toBe(false);
	});

	// A queued follow-up was written expecting the turn ahead of it to have
	// actually answered. Auto-firing it the instant a *failed* turn finishes
	// showed "Thinking" directly under an error card, for a request sent
	// against a conversation whose last turn never actually completed.
	it("holds the queue back when the turn that just finished failed", () => {
		expect(shouldDrainQueue(true, false, 2, false, true)).toBe(false);
		expect(shouldDrainQueue(true, false, 0, false, true)).toBe(false);
	});
});

// isInternalWarpLink decides whether a link Warp produced is followed with the
// router (same tab) or opened as an external link. WHATWG URL parsing folds a
// backslash into a forward slash for special schemes, so "/\host" resolves the
// same way "//host" does - and treating it as internal handed the router a
// value that navigates the current tab to another origin.
describe("isInternalWarpLink", () => {
	it("accepts root-relative paths", () => {
		expect(isInternalWarpLink("/workspace/logs")).toBe(true);
		expect(isInternalWarpLink("/workspace/logs?providers=openai")).toBe(true);
	});

	it("rejects protocol-relative and backslash-folded authorities", () => {
		for (const href of ["//evil.example/x", "/\\evil.example/x", "/\\\\evil.example/x", "/\\/evil.example"]) {
			expect(isInternalWarpLink(href)).toBe(false);
		}
	});

	it("rejects absolute and empty links", () => {
		for (const href of ["https://evil.example", "http://evil.example", "javascript:alert(1)", "", undefined]) {
			expect(isInternalWarpLink(href)).toBe(false);
		}
	});
});
describe("isTypingInto contenteditable", () => {
	// A rich-text editor is a DIV, so tagName alone says nothing. This was
	// covered by the inline check the helper replaced, and losing it meant the
	// question shortcuts ate keystrokes in exactly the field where it is hardest
	// to spot.
	it("treats a contenteditable element as typing", () => {
		expect(isTypingInto({ tagName: "DIV", isContentEditable: true })).toBe(true);
	});

	it("leaves an ordinary div alone, so the shortcuts still work", () => {
		expect(isTypingInto({ tagName: "DIV" })).toBe(false);
		expect(isTypingInto({ tagName: "DIV", isContentEditable: false })).toBe(false);
	});

	// Warp's own composer keeps its exemption: empty means the shortcuts apply.
	it("keeps the composer exemption", () => {
		expect(isTypingInto({ tagName: "TEXTAREA", value: "", dataset: { testid: WARP_COMPOSER_TESTID } })).toBe(false);
		expect(isTypingInto({ tagName: "TEXTAREA", value: "draft", dataset: { testid: WARP_COMPOSER_TESTID } })).toBe(true);
	});
});
describe("isPlainLeftClick", () => {
	it("leaves modified and non-primary clicks to the browser", () => {
		// Intercepting these took away the only way to open a cited link in a new
		// tab without losing the answer being read.
		expect(isPlainLeftClick({ button: 0 })).toBe(true);
		expect(isPlainLeftClick({})).toBe(true);
		expect(isPlainLeftClick({ button: 1 })).toBe(false);
		expect(isPlainLeftClick({ button: 0, metaKey: true })).toBe(false);
		expect(isPlainLeftClick({ button: 0, ctrlKey: true })).toBe(false);
		expect(isPlainLeftClick({ button: 0, shiftKey: true })).toBe(false);
		expect(isPlainLeftClick({ button: 0, altKey: true })).toBe(false);
	});
});

describe("shouldDrainQueue with a pending question", () => {
	it("holds the queue until the clarification is resolved", () => {
		// A question ends streaming too, so without the gate the next queued
		// follow-up became the answer to a question it has nothing to do with.
		expect(shouldDrainQueue(true, false, 1)).toBe(true);
		expect(shouldDrainQueue(true, false, 1, true)).toBe(false);
		expect(shouldDrainQueue(true, false, 0, false)).toBe(false);
		expect(shouldDrainQueue(false, false, 2, false)).toBe(false);
	});
});

describe("turnsFromStoredMessages question markers", () => {
	it("marks a reopened turn that asked rather than answered", () => {
		expect(isWarpQuestionFinish("question")).toBe(true);
		expect(isWarpQuestionFinish("stop")).toBe(false);
		expect(isWarpQuestionFinish(undefined)).toBe(false);
		const turns = turnsFromStoredMessages([
			{ role: "user", content: "which provider?", created_at: "2026-09-16T09:00:00Z" },
			{ role: "assistant", content: "Which provider did you mean?", finish_reason: "question", created_at: "2026-09-16T09:00:01Z" },
		]);
		// send() serialises turn.question as `question: true`. Without the marker
		// the server reads a replayed clarification as an answer.
		expect(turns[1].question?.question).toBe("Which provider did you mean?");
		expect(turns[0].question).toBeUndefined();
	});
});