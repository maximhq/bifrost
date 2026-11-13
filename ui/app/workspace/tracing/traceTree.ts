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
	/** The model call the message was first seen in: its model and start time. */
	model?: string;
	at: string;
	/** Images, audio and files in the message, shown under its text. */
	attachments: SpanAttachment[];
}

// Message parts that carry a file, not text: OpenAI, Anthropic, OTel GenAI, Vercel AI, OpenInference.
const BINARY_PART_TYPES = new Set([
	"blob",
	"uri",
	"image",
	"image_url",
	"input_image",
	"input_audio",
	"audio",
	"file",
	"input_file",
	"document",
]);

function contentText(content: unknown): string {
	if (typeof content === "string") return content.startsWith("data:") ? "" : content;
	if (Array.isArray(content)) return content.map(contentText).filter(Boolean).join("\n");
	if (content && typeof content === "object") {
		const record = content as Record<string, unknown>;
		if (typeof record.type === "string" && BINARY_PART_TYPES.has(record.type)) return "";
		for (const key of ["text", "content", "message_content"]) {
			if (key in record) return contentText(record[key]);
		}
	}
	return "";
}

export function messagesOf(value: unknown): { role: string; text: string; content?: unknown }[] {
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
	const messages: { role: string; text: string; content?: unknown }[] = [];
	for (const item of parsed) {
		if (!item || typeof item !== "object") continue;
		let record = item as Record<string, unknown>;
		if (record.message && typeof record.message === "object") record = record.message as Record<string, unknown>;
		const role = typeof record.role === "string" ? record.role : "";
		const content = record.content ?? record.contents ?? record.parts;
		const text = contentText(content);
		const hasFile = typeof content === "object" && content !== null && collectAttachments("", (c) => c.walk(content, "input")).length > 0;
		if (role && (text || hasFile)) messages.push({ role, text, content });
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
	const add = (role: string, text: string, call: TraceSpan, content?: unknown) => {
		const attachments = content === undefined ? [] : collectAttachments(call.project_id, (c) => c.walk(content, "input"));
		const key = `${role}\u0000${text}\u0000${attachments.map((a) => a.uploadId ?? a.src).join(",")}`;
		if (seen.has(key)) return;
		seen.add(key);
		thread.push({ role, text, spanId: call.span_id, model: call.link_snapshot?.model || call.model, at: call.start_time, attachments });
	};
	for (const call of calls) {
		for (const message of messagesOf(call.input)) add(message.role, message.text, call, message.content);
		const replies = messagesOf(call.output);
		if (replies.length > 0) {
			for (const reply of replies) add(reply.role || "assistant", reply.text, call, reply.content);
		} else if (call.output && !call.output.trim().startsWith("[") && !call.output.trim().startsWith("{")) {
			add("assistant", call.output, call);
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
/** A file a span refers to: uploaded to Bifrost, inline as base64, or at an outside URL. */
export interface SpanAttachment {
	kind: "upload" | "inline" | "url";
	field: "input" | "output" | "expected" | "metadata";
	name: string;
	contentType: string;
	/** The id under /api/traces/uploads, for kind "upload". */
	uploadId?: string;
	/** A data: URL for kind "inline", the outside URL for kind "url". */
	src?: string;
}

const LANGFUSE_MEDIA = /@@@langfuseMedia:type=([^|@]+)\|id=([^|@]+)\|source=[^@]+@@@/g;
const DATA_URL = /data:([\w.+-]+\/[\w.+-]+);base64,[A-Za-z0-9+/=]+/g;
const EXTENSION_TYPES: Record<string, string> = {
	png: "image/png",
	jpg: "image/jpeg",
	jpeg: "image/jpeg",
	gif: "image/gif",
	webp: "image/webp",
	mp3: "audio/mpeg",
	wav: "audio/wav",
	ogg: "audio/ogg",
	mp4: "video/mp4",
	webm: "video/webm",
	pdf: "application/pdf",
};

function typeFromURL(url: string): string {
	const extension = url.split(/[?#]/)[0].split(".").pop()?.toLowerCase() ?? "";
	return EXTENSION_TYPES[extension] ?? "";
}

// The upload id Bifrost gives a Braintrust attachment key: see braintrustAttachmentID on the server.
function braintrustUploadId(key: string): string {
	return "bt-" + key.replace(/[^a-zA-Z0-9-]/g, "-");
}

function decode(value: string): unknown {
	const trimmed = value.trim();
	if (!trimmed.startsWith("{") && !trimmed.startsWith("[")) return value;
	try {
		return JSON.parse(trimmed);
	} catch {
		return value;
	}
}

/**
 * Every attachment in a span's input, output, expected value and metadata, in the forms the SDKs send:
 * Braintrust attachment and external-attachment references, Langfuse media tokens, base64 data URLs
 * (OpenAI and OpenInference images), Anthropic base64 sources, and OTel GenAI blob and uri parts.
 */
export function spanAttachments(span: TraceSpan): SpanAttachment[] {
	return collectAttachments(span.project_id, (c) => {
		c.walk(span.input, "input");
		c.walk(span.output, "output");
		c.walk(span.expected, "expected");
		c.walk(span.metadata, "metadata");
	});
}

interface AttachmentCollector {
	walk: (value: unknown, field: SpanAttachment["field"]) => void;
}

/** Runs fill over a fresh collector and returns what it found, each attachment once. */
function collectAttachments(projectId: string, fill: (collector: AttachmentCollector) => void): SpanAttachment[] {
	const found: SpanAttachment[] = [];
	const seen = new Set<string>();
	const add = (attachment: SpanAttachment) => {
		const id = attachment.uploadId ?? attachment.src ?? "";
		if (!id || seen.has(id)) return;
		seen.add(id);
		found.push(attachment);
	};
	const nameFor = (field: SpanAttachment["field"], contentType: string) =>
		`${field} ${contentType.split("/")[0] || "file"} ${found.filter((a) => a.field === field).length + 1}`;

	const scanText = (text: string, field: SpanAttachment["field"]) => {
		for (const match of text.matchAll(LANGFUSE_MEDIA)) {
			// Stored per project: see langfuseUploadID on the server.
			add({ kind: "upload", field, contentType: match[1], uploadId: `lf-${projectId}-${match[2]}`, name: nameFor(field, match[1]) });
		}
		for (const match of text.matchAll(DATA_URL)) {
			add({ kind: "inline", field, contentType: match[1], src: match[0], name: nameFor(field, match[1]) });
		}
	};

	const walk = (value: unknown, field: SpanAttachment["field"], depth: number): void => {
		if (depth > 12 || value === null || value === undefined) return;
		if (typeof value === "string") {
			const decoded = decode(value);
			if (decoded !== value) walk(decoded, field, depth + 1);
			else scanText(value, field);
			return;
		}
		if (Array.isArray(value)) {
			value.forEach((item) => walk(item, field, depth + 1));
			return;
		}
		if (typeof value !== "object") return;
		const node = value as Record<string, unknown>;
		const str = (key: string) => (typeof node[key] === "string" ? (node[key] as string) : "");
		const declaredType = str("content_type") || str("mime_type") || str("media_type") || str("contentType");
		if (node.type === "braintrust_attachment" && str("key")) {
			const contentType = declaredType || "application/octet-stream";
			add({
				kind: "upload",
				field,
				contentType,
				uploadId: braintrustUploadId(str("key")),
				name: str("filename") || nameFor(field, contentType),
			});
			return;
		}
		if (node.type === "external_attachment" && str("url")) {
			const contentType = declaredType || typeFromURL(str("url"));
			add({ kind: "url", field, contentType, src: str("url"), name: str("filename") || nameFor(field, contentType) });
			return;
		}
		// OTel GenAI blob part and Anthropic base64 source: bytes inline with their type.
		const base64 = node.type === "blob" ? str("content") : node.type === "base64" ? str("data") : "";
		if (base64 && declaredType) {
			add({
				kind: "inline",
				field,
				contentType: declaredType,
				src: `data:${declaredType};base64,${base64}`,
				name: nameFor(field, declaredType),
			});
			return;
		}
		// A URL in a field that names a file: OTel uri parts, image_url, OpenInference image.url.
		for (const key of ["uri", "url"]) {
			const url = str(key);
			if (/^https?:\/\//.test(url)) {
				const contentType = declaredType || typeFromURL(url);
				if (contentType) add({ kind: "url", field, contentType, src: url, name: str("filename") || nameFor(field, contentType) });
			}
		}
		for (const child of Object.values(node)) walk(child, field, depth + 1);
	};

	fill({ walk: (value, field) => walk(value, field, 0) });
	return found;
}