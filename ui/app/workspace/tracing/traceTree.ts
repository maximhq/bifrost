import type { TraceSpan } from "@/lib/types/traces";

/** One row of the span tree: a span, its depth, and where it sits on the trace's timeline. */
export interface TraceTreeNode {
	span: TraceSpan;
	depth: number;
	children: TraceTreeNode[];
	/** Start offset and width as fractions of the trace's duration, both 0 to 1. */
	offset: number;
	width: number;
	/** True for a span whose parent has not arrived: it is shown at the top level until it does. */
	orphan: boolean;
	/** Tokens and cost of this span and every model call below it, as an agent row in Agent Logs adds them up. */
	subtreeTokens: number;
	subtreeCost: number;
}

export interface TraceTree {
	roots: TraceTreeNode[];
	/** Every node in display order: depth first, siblings by start time. */
	rows: TraceTreeNode[];
	startMs: number;
	durationMs: number;
}

function startOf(span: TraceSpan): number {
	return new Date(span.start_time).getTime();
}

function endOf(span: TraceSpan): number {
	if (span.end_time) return new Date(span.end_time).getTime();
	if (span.duration_ms !== undefined) return startOf(span) + span.duration_ms;
	return startOf(span);
}

/**
 * Builds the span tree of one trace. HTTP client spans are the SDK's transport plumbing under a
 * model call, so they are left out unless showHTTP is set; their children move up to their parent.
 * A span that names a parent the trace does not hold yet is kept, at the top level, so a trace that
 * is still arriving shows everything received so far.
 */
export function buildTraceTree(spans: TraceSpan[], showHTTP = false): TraceTree {
	const byId = new Map(spans.map((span) => [span.span_id, span]));
	// Where a hidden span's children re-attach: the nearest ancestor that is shown.
	const shownParent = (span: TraceSpan): { id: string | undefined; missing: boolean } => {
		let parentId = span.parent_span_id;
		for (let hops = 0; parentId && hops < 64; hops++) {
			const parent = byId.get(parentId);
			if (!parent) return { id: undefined, missing: true };
			if (showHTTP || parent.kind !== "http") return { id: parentId, missing: false };
			parentId = parent.parent_span_id;
		}
		return { id: undefined, missing: false };
	};

	const visible = spans.filter((span) => showHTTP || span.kind !== "http");
	const nodes = new Map<string, TraceTreeNode>();
	for (const span of visible)
		nodes.set(span.span_id, { span, depth: 0, children: [], offset: 0, width: 0, orphan: false, subtreeTokens: 0, subtreeCost: 0 });

	const roots: TraceTreeNode[] = [];
	for (const span of visible) {
		const node = nodes.get(span.span_id)!;
		const parent = shownParent(span);
		const parentNode = parent.id && parent.id !== span.span_id ? nodes.get(parent.id) : undefined;
		if (parentNode) {
			parentNode.children.push(node);
		} else {
			node.orphan = parent.missing;
			roots.push(node);
		}
	}

	const startMs = visible.length > 0 ? Math.min(...visible.map(startOf)) : 0;
	const endMs = visible.length > 0 ? Math.max(...visible.map(endOf)) : 0;
	const durationMs = Math.max(endMs - startMs, 0);
	const byStart = (a: TraceTreeNode, b: TraceTreeNode) => startOf(a.span) - startOf(b.span) || a.span.span_id.localeCompare(b.span.span_id);

	const rows: TraceTreeNode[] = [];
	const visit = (node: TraceTreeNode, depth: number, seen: Set<string>) => {
		if (seen.has(node.span.span_id)) return; // a cycle in parent ids must not hang the viewer
		seen.add(node.span.span_id);
		node.depth = depth;
		node.offset = durationMs > 0 ? (startOf(node.span) - startMs) / durationMs : 0;
		node.width = durationMs > 0 ? Math.max(endOf(node.span) - startOf(node.span), 0) / durationMs : 1;
		rows.push(node);
		node.children.sort(byStart);
		// Only model calls carry usage of their own; a parent repeating its children's usage is not added again.
		const own = spanUsage(node.span);
		const counts = own.linked || node.span.kind === "llm" || node.span.kind === "embedding";
		node.subtreeTokens = counts ? own.tokens : 0;
		node.subtreeCost = counts ? (own.cost ?? 0) : 0;
		for (const child of node.children) {
			visit(child, depth + 1, seen);
			node.subtreeTokens += child.subtreeTokens;
			node.subtreeCost += child.subtreeCost;
		}
	};
	roots.sort(byStart);
	const seen = new Set<string>();
	for (const root of roots) visit(root, 0, seen);
	return { roots, rows, startMs, durationMs };
}

/** One message of the Thread view. */
export interface ThreadMessage {
	role: string;
	text: string;
	spanId: string;
}

function contentText(content: unknown): string {
	if (typeof content === "string") return content;
	if (Array.isArray(content)) return content.map(contentText).filter(Boolean).join("\n");
	if (content && typeof content === "object") {
		const record = content as Record<string, unknown>;
		for (const key of ["text", "content", "message_content"]) {
			if (key in record) return contentText(record[key]);
		}
	}
	return "";
}

export function messagesOf(value: unknown): { role: string; text: string }[] {
	let parsed = value;
	if (typeof value === "string") {
		try {
			parsed = JSON.parse(value);
		} catch {
			return [];
		}
	}
	if (parsed && !Array.isArray(parsed) && typeof parsed === "object") {
		const record = parsed as Record<string, unknown>;
		parsed = record.messages ?? record.input ?? record.choices ?? parsed;
	}
	if (!Array.isArray(parsed)) return [];
	const messages: { role: string; text: string }[] = [];
	for (const item of parsed) {
		if (!item || typeof item !== "object") continue;
		let record = item as Record<string, unknown>;
		if (record.message && typeof record.message === "object") record = record.message as Record<string, unknown>;
		const role = typeof record.role === "string" ? record.role : "";
		const text = contentText(record.content ?? record.contents ?? record.parts);
		if (role && text) messages.push({ role, text });
	}
	return messages;
}

/**
 * The conversation a trace holds, read from its model calls in start order. Each call's input
 * repeats the history before it, so only messages not seen yet are added, then the call's reply.
 */
export function buildThread(spans: TraceSpan[]): ThreadMessage[] {
	const calls = spans.filter((span) => span.kind === "llm").sort((a, b) => startOf(a) - startOf(b));
	const thread: ThreadMessage[] = [];
	const seen = new Set<string>();
	const add = (role: string, text: string, spanId: string) => {
		const key = `${role}\u0000${text}`;
		if (seen.has(key)) return;
		seen.add(key);
		thread.push({ role, text, spanId });
	};
	for (const call of calls) {
		for (const message of messagesOf(call.input)) add(message.role, message.text, call.span_id);
		const replies = messagesOf(call.output);
		if (replies.length > 0) {
			for (const reply of replies) add(reply.role || "assistant", reply.text, call.span_id);
		} else if (call.output && !call.output.trim().startsWith("[") && !call.output.trim().startsWith("{")) {
			add("assistant", call.output, call.span_id);
		}
	}
	return thread;
}

/** What a span counts for: the gateway's figures when it is linked, else the SDK's own. */
export function spanUsage(span: TraceSpan): { tokens: number; cost?: number; estimated: boolean; cachedShare: number; linked: boolean } {
	const linked = span.link_snapshot;
	const prompt = (linked ? linked.prompt_tokens : span.prompt_tokens) ?? 0;
	const cached = (linked ? linked.cached_read_tokens : span.cached_read_tokens) ?? 0;
	return {
		tokens: (linked ? linked.total_tokens : span.total_tokens) ?? 0,
		cost: linked ? linked.cost : span.cost,
		estimated: !linked && Boolean(span.cost_estimated),
		cachedShare: prompt > 0 ? Math.min(cached / prompt, 1) : 0,
		linked: Boolean(linked),
	};
}