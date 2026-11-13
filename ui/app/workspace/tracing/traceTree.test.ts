import type { TraceSpan } from "@/lib/types/traces";
import { describe, expect, it } from "vitest";
import { buildThread, buildTraceTree, shouldPollTrace, spanAttachments, spanUsage } from "./traceTree";

function span(
	id: string,
	parent: string | undefined,
	startMs: number,
	endMs: number | undefined,
	extra: Partial<TraceSpan> = {},
): TraceSpan {
	const base = Date.UTC(2026, 10, 12, 10, 0, 0);
	return {
		project_id: "p1",
		trace_id: "t1",
		span_id: id,
		parent_span_id: parent,
		source: "otel",
		name: id,
		kind: "task",
		partial: false,
		start_time: new Date(base + startMs).toISOString(),
		end_time: endMs === undefined ? undefined : new Date(base + endMs).toISOString(),
		...extra,
	};
}

describe("buildTraceTree", () => {
	it("nests spans under their parents, siblings by start time", () => {
		const tree = buildTraceTree([
			span("b", "root", 500, 900),
			span("root", undefined, 0, 1000),
			span("a", "root", 100, 400),
			span("a1", "a", 150, 200),
		]);
		expect(tree.rows.map((node) => `${node.depth}:${node.span.span_id}`)).toEqual(["0:root", "1:a", "2:a1", "1:b"]);
		expect(tree.durationMs).toBe(1000);
		const a = tree.rows[1];
		expect(a.offset).toBeCloseTo(0.1);
		expect(a.width).toBeCloseTo(0.3);
	});

	it("hides HTTP client spans and lifts their children to the model call", () => {
		const spans = [
			span("llm", undefined, 0, 1000, { kind: "llm" }),
			span("http", "llm", 10, 990, { kind: "http" }),
			span("inner", "http", 20, 30),
		];
		expect(buildTraceTree(spans).rows.map((node) => `${node.depth}:${node.span.span_id}`)).toEqual(["0:llm", "1:inner"]);
		expect(buildTraceTree(spans, true).rows.map((node) => node.span.span_id)).toEqual(["llm", "http", "inner"]);
	});

	it("keeps a span whose parent has not arrived, at the top level", () => {
		const tree = buildTraceTree([span("child", "not-here-yet", 0, 100)]);
		expect(tree.rows).toHaveLength(1);
		expect(tree.rows[0].orphan).toBe(true);
	});

	it("does not hang on a cycle in parent ids", () => {
		const tree = buildTraceTree([span("a", "b", 0, 10), span("b", "a", 0, 10)]);
		expect(tree.rows.length).toBeLessThanOrEqual(2);
	});

	it("gives a span with no end a zero width instead of NaN", () => {
		const tree = buildTraceTree([span("root", undefined, 0, 1000), span("running", "root", 400, undefined)]);
		expect(tree.rows[1].width).toBe(0);
		expect(buildTraceTree([]).rows).toEqual([]);
	});
});

describe("buildThread", () => {
	it("reads the conversation once, though each call repeats the history", () => {
		const first = span("c1", undefined, 0, 100, {
			kind: "llm",
			input: JSON.stringify([
				{ role: "system", content: "be brief" },
				{ role: "user", content: "hi" },
			]),
			output: JSON.stringify([{ message: { role: "assistant", content: "hello" } }]),
		});
		const second = span("c2", undefined, 200, 300, {
			kind: "llm",
			input: JSON.stringify({
				messages: [
					{ role: "system", content: "be brief" },
					{ role: "user", content: "hi" },
					{ role: "assistant", content: "hello" },
					{ role: "user", content: [{ type: "text", text: "bye" }] },
				],
			}),
			output: "see you",
		});
		expect(buildThread([second, first]).map((m) => `${m.role}:${m.text}`)).toEqual([
			"system:be brief",
			"user:hi",
			"assistant:hello",
			"user:bye",
			"assistant:see you",
		]);
	});

	it("ignores spans that are not model calls and input that is not a message list", () => {
		expect(
			buildThread([span("task", undefined, 0, 10, { input: "plain" }), span("llm", undefined, 0, 10, { kind: "llm", input: "{broken" })]),
		).toEqual([]);
	});
});

describe("spanUsage", () => {
	it("counts a linked span with the gateway's figures, not the SDK's", () => {
		const usage = spanUsage(
			span("llm", undefined, 0, 10, {
				total_tokens: 100,
				cost: 9,
				link_snapshot: { total_tokens: 120, prompt_tokens: 100, cached_read_tokens: 25, cost: 0.004 },
			}),
		);
		expect(usage).toEqual({ tokens: 120, cost: 0.004, estimated: false, cachedShare: 0.25, linked: true });
	});

	it("marks a catalog-priced cost as estimated", () => {
		const usage = spanUsage(span("llm", undefined, 0, 10, { total_tokens: 50, cost: 0.01, cost_estimated: true }));
		expect(usage.estimated).toBe(true);
		expect(usage.linked).toBe(false);
	});
});
describe("spanAttachments", () => {
	const png = "iVBORw0KGgo=";
	it("finds every attachment form an SDK sends, once each", () => {
		const found = spanAttachments(
			span("a", undefined, 0, 1, {
				// Braintrust: an uploaded attachment and an external one.
				input: JSON.stringify({
					file: { type: "braintrust_attachment", key: "k1/report.pdf", filename: "report.pdf", content_type: "application/pdf" },
					link: { type: "external_attachment", url: "https://example.com/cat.png", filename: "cat.png", content_type: "image/png" },
					// OpenAI-style image part, Anthropic base64 image, OTel GenAI blob and uri parts.
					messages: [
						{ role: "user", content: [{ type: "image_url", image_url: { url: `data:image/png;base64,${png}` } }] },
						{ role: "user", content: [{ type: "image", source: { type: "base64", media_type: "image/jpeg", data: png } }] },
						{ role: "user", parts: [{ type: "blob", modality: "audio", mime_type: "audio/wav", content: png }] },
						{ role: "user", parts: [{ type: "uri", modality: "image", mime_type: "image/webp", uri: "https://example.com/dog.webp" }] },
					],
				}),
				// Langfuse: media tokens inside text, and the same token twice.
				output:
					"see @@@langfuseMedia:type=image/png|id=lfabc-123|source=bytes@@@ and @@@langfuseMedia:type=image/png|id=lfabc-123|source=bytes@@@",
				// OpenInference flattened image message, rebuilt into an object by the ingest mapper.
				metadata: { image: { url: "https://example.com/chart.jpg" } },
			}),
		);
		const summary = found.map((a) => [a.kind, a.field, a.contentType, a.uploadId ?? a.src?.slice(0, 22)]);
		expect(summary).toEqual([
			["upload", "input", "application/pdf", "bt-k1-report-pdf"],
			["url", "input", "image/png", "https://example.com/ca"],
			["inline", "input", "image/png", "data:image/png;base64,"],
			["inline", "input", "image/jpeg", "data:image/jpeg;base64"],
			["inline", "input", "audio/wav", "data:audio/wav;base64,"],
			["url", "input", "image/webp", "https://example.com/do"],
			["upload", "output", "image/png", "lf-p1-lfabc-123"],
			["url", "metadata", "image/jpeg", "https://example.com/ch"],
		]);
		expect(found[0].name).toBe("report.pdf");
	});

	it("finds nothing in a plain span", () => {
		expect(spanAttachments(span("b", undefined, 0, 1, { input: '{"q": "hi"}', output: "hello https://example.com" }))).toEqual([]);
	});
});
describe("buildThread attachments", () => {
	it("keeps binary parts out of the text and attaches them to the message", () => {
		const call = span("c", undefined, 0, 1, {
			kind: "llm",
			input: JSON.stringify([
				{
					role: "user",
					parts: [
						{ type: "text", content: "Weather where this photo was taken?" },
						{ type: "blob", modality: "image", mime_type: "image/png", content: "iVBORw0KGgo=" },
						{ type: "uri", modality: "image", mime_type: "image/png", uri: "https://example.com/a.png" },
					],
				},
				{ role: "user", content: [{ type: "image_url", image_url: { url: "data:image/png;base64,iVBORw0KGgo=" } }] },
			]),
			output: "Oslo",
		});
		const thread = buildThread([call]);
		expect(thread.map((m) => [m.role, m.text, m.attachments.map((a) => a.kind)])).toEqual([
			["user", "Weather where this photo was taken?", ["inline", "url"]],
			["user", "", ["inline"]],
			["assistant", "Oslo", []],
		]);
	});
});
describe("shouldPollTrace", () => {
	const now = Date.UTC(2026, 10, 12, 10, 10, 0);
	const iso = (msAgo: number) => new Date(now - msAgo).toISOString();
	it("polls a trace that is loading, still arriving, or changed in the last two minutes", () => {
		expect(shouldPollTrace(undefined, now)).toBe(true);
		expect(shouldPollTrace({ in_progress: true, updated_at: iso(3_600_000) }, now)).toBe(true);
		expect(shouldPollTrace({ in_progress: false, updated_at: iso(30_000) }, now)).toBe(true);
	});
	it("stops polling a finished trace that has settled", () => {
		// An open sheet used to refetch the whole trace, images included, every five seconds forever.
		expect(shouldPollTrace({ in_progress: false, updated_at: iso(121_000) }, now)).toBe(false);
		expect(shouldPollTrace({ in_progress: false }, now)).toBe(false);
	});
});
describe("audio, realtime and image generation payloads", () => {
	const wav = "UklGRiQAAABXQVZF";
	it("finds OpenAI audio parts, generated images and OpenInference audio attributes", () => {
		const found = spanAttachments(
			span("m", undefined, 0, 1, {
				input: JSON.stringify([{ role: "user", content: [{ type: "input_audio", input_audio: { data: wav, format: "mp3" } }] }]),
				output: JSON.stringify({
					choices: [{ message: { role: "assistant", audio: { id: "audio_1", data: wav, transcript: "It is sunny." } } }],
					data: [{ b64_json: "iVBORw0KGgo=", revised_prompt: "a fox" }],
				}),
				attributes: { audio: { url: "https://example.com/call.wav", mime_type: "audio/wav", transcript: "hello" } },
			}),
		);
		expect(found.map((a) => [a.kind, a.field, a.contentType, a.src?.slice(0, 24)])).toEqual([
			["inline", "input", "audio/mpeg", "data:audio/mpeg;base64,U"],
			["inline", "output", "audio/wav", "data:audio/wav;base64,Uk"],
			["inline", "output", "image/png", "data:image/png;base64,iV"],
			["url", "attributes", "audio/wav", "https://example.com/call"],
		]);
	});

	it("shows an audio turn in the thread as its transcript with the audio attached", () => {
		const call = span("r", undefined, 0, 1, {
			kind: "llm",
			input: JSON.stringify([{ role: "user", content: [{ type: "input_audio", input_audio: { data: wav, format: "wav" } }] }]),
			output: JSON.stringify([{ message: { role: "assistant", audio: { data: wav, transcript: "It is sunny in Oslo." } } }]),
		});
		expect(buildThread([call]).map((m) => [m.role, m.text, m.attachments.length])).toEqual([
			["user", "", 1],
			["assistant", "It is sunny in Oslo.", 1],
		]);
	});
});