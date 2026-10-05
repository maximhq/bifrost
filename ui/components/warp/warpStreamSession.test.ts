import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WarpTurn } from "@/lib/contexts/warpContext";
import { WarpStreamSession, type WarpStreamSink } from "./warpStreamSession";

/** One SSE frame as the server writes it. */
function frame(event: Record<string, unknown>): string {
	return `event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`;
}

/**
 * A fetch whose body the test feeds by hand, so a turn can be left in flight
 * while the test unsubscribes, stops or discards - the situations the session
 * exists to get right. Honours the abort signal the way the real fetch does:
 * the pending read rejects with an AbortError.
 */
function controlledFetch() {
	const encoder = new TextEncoder();
	let controller!: ReadableStreamDefaultController<Uint8Array>;
	const calls: { signal: AbortSignal; body: unknown }[] = [];
	const doFetch = vi.fn(async (_url: string, init?: RequestInit) => {
		const stream = new ReadableStream<Uint8Array>({
			start(c) {
				controller = c;
			},
		});
		const signal = init?.signal as AbortSignal;
		signal.addEventListener("abort", () => {
			try {
				controller.error(new DOMException("The operation was aborted.", "AbortError"));
			} catch {
				// Already closed.
			}
		});
		calls.push({ signal, body: JSON.parse(String(init?.body)) });
		return new Response(stream, { status: 200 });
	});
	return {
		fetch: doFetch as unknown as typeof fetch,
		calls,
		// Enqueued on a microtask so a caller can `await` the reader picking it up.
		push: async (event: Record<string, unknown>) => {
			controller.enqueue(encoder.encode(frame(event)));
			await settle();
		},
		close: async () => {
			controller.close();
			await settle();
		},
	};
}

/** Lets the session's read loop and finally block run. */
async function settle() {
	for (let i = 0; i < 10; i++) await new Promise((resolve) => setTimeout(resolve, 0));
}

describe("WarpStreamSession", () => {
	let committed: WarpTurn[];
	let conversationIds: string[];
	let sink: WarpStreamSink;
	let net: ReturnType<typeof controlledFetch>;
	let session: WarpStreamSession;

	beforeEach(() => {
		committed = [];
		conversationIds = [];
		net = controlledFetch();
		sink = {
			commitTurn: (turn) => committed.push(turn),
			setConversationId: (id) => conversationIds.push(id),
			setQuestion: vi.fn(),
			fetch: net.fetch,
			baseUrl: () => "http://warp.test/api",
		};
		session = new WarpStreamSession(sink);
	});

	// The case that used to lose the answer: the panel unmounts while a turn is
	// in flight. Nothing is subscribed any more, and the turn still lands.
	it("finishes and files a turn after its last subscriber has gone", async () => {
		const unsubscribe = session.subscribe(() => {});
		const sending = session.send([], "why did the requests fail?");
		await settle();
		await net.push({ type: "delta", delta: "Two requests " });
		unsubscribe();

		await net.push({ type: "delta", delta: "hit a 429." });
		await net.push({ type: "done", finish_reason: "stop", conversation_id: "conv-1" });
		await net.close();
		await sending;

		expect(net.calls[0].signal.aborted).toBe(false);
		expect(committed).toEqual([{ role: "assistant", content: "Two requests hit a 429.", usage: undefined }]);
		expect(session.getSnapshot()).toEqual({ text: "", toolCalls: [], isStreaming: false, error: null });
		// The thread id the server minted reaches both the session and the provider.
		expect(session.conversationId).toBe("conv-1");
		expect(conversationIds).toEqual(["conv-1"]);
	});

	it("notifies subscribers per event and hands a late subscriber the in-flight state", async () => {
		const seen: string[] = [];
		session.subscribe(() => seen.push(session.getSnapshot().text));
		const sending = session.send([], "q");
		await settle();
		await net.push({ type: "delta", delta: "a" });
		await net.push({ type: "delta", delta: "b" });

		// A panel reopened mid-answer reads the answer so far, not a blank.
		expect(session.getSnapshot()).toMatchObject({ text: "ab", isStreaming: true });
		expect(seen).toContain("ab");

		await net.push({ type: "done", finish_reason: "stop" });
		await net.close();
		await sending;
		expect(committed[0].content).toBe("ab");
	});

	// stop is the user pressing the stop button: what has streamed is kept.
	it("stop keeps the partial answer", async () => {
		const sending = session.send([], "q");
		await settle();
		await net.push({ type: "delta", delta: "partial" });
		session.stop();
		await sending;

		expect(net.calls[0].signal.aborted).toBe(true);
		expect(committed).toEqual([{ role: "assistant", content: "partial", usage: undefined }]);
		expect(session.getSnapshot().isStreaming).toBe(false);
	});

	// discard is leaving the thread: the partial answer must not follow into the next one.
	it("discard drops the partial answer without filing it", async () => {
		const sending = session.send([], "q");
		await settle();
		await net.push({ type: "delta", delta: "partial" });
		session.discard();
		await sending;

		expect(committed).toEqual([]);
		expect(session.getSnapshot()).toEqual({ text: "", toolCalls: [], isStreaming: false, error: null });
	});

	it("files an error frame as the turn's error and keeps it in the snapshot", async () => {
		const sending = session.send([], "q");
		await settle();
		await net.push({ type: "error", code: "cancelled", message: "request cancelled" });
		await net.close();
		await sending;

		expect(committed).toHaveLength(1);
		expect(committed[0].error).toContain("cancelled");
		expect(session.getSnapshot().error).toBe(committed[0].error);
	});

	it("sends the history and files under the current thread", async () => {
		session.setConversationId("conv-9");
		const sending = session.send([{ role: "user", content: "earlier" }], "now");
		await settle();
		expect(net.calls[0].body).toMatchObject({
			conversation_id: "conv-9",
			messages: [
				{ role: "user", content: "earlier" },
				{ role: "user", content: "now" },
			],
		});
		await net.push({ type: "done", finish_reason: "stop" });
		await net.close();
		await sending;
	});

	// Which model answers is the panel's choice among what the operator exposed.
	// Unnamed, the server's default applies, so nothing is sent for it.
	it("names the picked model on the request, and nothing when none is picked", async () => {
		let sending = session.send([], "on the default");
		await settle();
		expect(net.calls[0].body).not.toHaveProperty("provider");
		expect(net.calls[0].body).not.toHaveProperty("model");
		await net.push({ type: "done", finish_reason: "stop" });
		await net.close();
		await sending;

		sending = session.send([], "on another", { provider: "anthropic", model: "claude-sonnet-5" });
		await settle();
		expect(net.calls[1].body).toMatchObject({ provider: "anthropic", model: "claude-sonnet-5" });
		await net.push({ type: "done", finish_reason: "stop" });
		await net.close();
		await sending;
	});

	// A refusal the server worded - a model that is no longer exposed, say - has
	// to reach the reader as written rather than as a bare status code.
	it("files a refused request under the server's own message", async () => {
		const refuse = vi.fn(
			async () =>
				new Response(JSON.stringify({ error: { message: "model is not available for warp: openai/gpt-5" } }), {
					status: 400,
					headers: { "Content-Type": "application/json" },
				}),
		);
		const refused = new WarpStreamSession({ ...sink, fetch: refuse as unknown as typeof fetch });
		await refused.send([], "hello", { provider: "openai", model: "gpt-5" });
		expect(committed).toHaveLength(1);
		expect(committed[0].error).toBe(":model is not available for warp: openai/gpt-5");
	});

	it("falls back to the status when a refusal carries no message", async () => {
		const refuse = vi.fn(async () => new Response("", { status: 500 }));
		const refused = new WarpStreamSession({ ...sink, fetch: refuse as unknown as typeof fetch });
		await refused.send([], "hello");
		expect(committed[0].error).toBe(":Warp request failed (500)");
	});
});
