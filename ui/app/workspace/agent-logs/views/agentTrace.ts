import type { AgentLogOperation } from "@/lib/types/agentLogs";
import type { GuardrailMatch, LogEntry, MCPToolLogEntry } from "@/lib/types/logs";
import { collectA2AArtifacts, type A2AArtifactRecord } from "./a2aArtifacts";

// The agent trace model: pure, so the tree can be tested without rendering.

export type TraceNodeKind = "session" | "agent" | "llm" | "guard" | "mcp" | "artifact";

export interface TraceNode {
	id: string;
	kind: TraceNodeKind;
	parentId: string | null;
	children: string[];
	name: string;
	start: number; // epoch ms
	end: number; // epoch ms
	status: string;
	tokens: number; // this node plus every LLM call below it
	cost: number;
	errors: number;
	spanCount: number; // descendants, artifacts excluded
	agent?: AgentLogOperation;
	followUps?: number; // more operations on the same A2A task (GetTask polls, resubscribes)
	reply?: string; // the task's answer from its newest operation that has one (a GetTask poll can finish what SendMessage started)
	llm?: LogEntry;
	mcp?: MCPToolLogEntry;
	artifact?: A2AArtifactRecord;
	producer?: string;
	turn?: number; // session mode: the top-level turn this agent started, 1-based in time order
	guard?: GuardrailCheck;
}

// One guardrail rule checked on an LLM call, from the call's guardrail_debug.
export interface GuardrailCheck {
	phase: string; // input / output
	outcome: string; // blocked, or the rule's action (redact, detect, allow...), or "monitored" for an unenforced match
	ruleName?: string;
	guardrailName?: string;
	provider?: string;
	reason?: string;
	judgeModel?: string;
	assessments?: string[];
}

export interface TraceData {
	operations: AgentLogOperation[];
	llm: { taskId: string; entry: LogEntry }[];
	mcp: { taskId: string; entry: MCPToolLogEntry }[];
	loading: boolean;
	partial: boolean; // a linked query failed; the tree shows what loaded
}

function toMs(timestamp?: string): number {
	const value = timestamp ? new Date(timestamp).getTime() : NaN;
	return Number.isFinite(value) ? value : 0;
}

export interface BuildTraceOptions {
	/** Session mode: root the tree at this A2A context, with every agent whose caller is not loaded as a turn. */
	sessionId?: string;
}

/**
 * Builds the span tree: agents nest under the agent whose task delegated them; calls and artifacts under their
 * agent. A trace roots at the agent operation rootId belongs to; a session roots at a synthetic session node.
 */
export function buildTrace(
	rootId: string,
	data: TraceData,
	options: BuildTraceOptions = {},
): { nodes: Map<string, TraceNode>; rootKey: string | null; rootNodeId: string | null } {
	const sessionNodeId = options.sessionId ? `session:${options.sessionId}` : null;
	const nodes = new Map<string, TraceNode>();
	const groups = new Map<string, AgentLogOperation[]>();
	for (const operation of data.operations) {
		if (!operation.task_id) continue;
		groups.set(operation.task_id, [...(groups.get(operation.task_id) ?? []), operation]);
	}
	// A task-less operation (the caller's GetAgentCard lookup before delegating) joins the task the same caller
	// opened on the same agent, so it never shows as an empty agent node; with no such task it stands alone.
	for (const operation of data.operations) {
		if (operation.task_id) continue;
		const sibling = [...groups.entries()].find(([, operations]) =>
			operations.some(
				(other) => other.agent_name === operation.agent_name && other.caller_correlation_id === operation.caller_correlation_id,
			),
		);
		const key = sibling?.[0] ?? operation.id;
		groups.set(key, [...(groups.get(key) ?? []), operation]);
	}
	let rootKey: string | null = null;
	for (const [key, operations] of groups) {
		const sorted = [...operations].sort((a, b) => toMs(a.timestamp) - toMs(b.timestamp));
		const primary = sorted.find((operation) => operation.id === rootId) ?? sorted.find((operation) => operation.task_id) ?? sorted[0];
		if (operations.some((operation) => operation.id === rootId)) rootKey = key;
		const start = Math.min(...sorted.map((operation) => toMs(operation.timestamp)));
		const end = Math.max(...sorted.map((operation) => toMs(operation.timestamp) + (operation.latency ?? 0)));
		const status = sorted.some((operation) => operation.status === "error")
			? "error"
			: sorted.some((operation) => operation.status === "processing")
				? "processing"
				: "success";
		nodes.set(`agent:${key}`, {
			id: `agent:${key}`,
			kind: "agent",
			parentId: null,
			children: [],
			name: primary.agent_name,
			start,
			end,
			status,
			tokens: 0,
			cost: 0,
			errors: 0,
			spanCount: 0,
			agent: primary,
			followUps: sorted.length - 1,
			reply: sorted.reduceRight<string | undefined>((found, operation) => found ?? operationReply(operation), undefined),
		});
		// Operations are in time order, so a later snapshot of an artifact (the GetTask poll's finished copy)
		// replaces an earlier one (a streamed partial) under the same key.
		const latestArtifacts = new Map<string, A2AArtifactRecord>();
		for (const operation of sorted) {
			for (const record of collectA2AArtifacts(operation, operation.events)) latestArtifacts.set(record.key, record);
		}
		for (const record of latestArtifacts.values()) {
			const at = toMs(record.timestamp) || end;
			nodes.set(`artifact:${key}:${record.key}`, {
				id: `artifact:${key}:${record.key}`,
				kind: "artifact",
				parentId: `agent:${key}`,
				children: [],
				name: record.name || record.key,
				start: at,
				end: at,
				status: "success",
				tokens: 0,
				cost: 0,
				errors: 0,
				spanCount: 0,
				artifact: record,
				producer: primary.agent_name,
			});
		}
	}
	// An agent whose caller was not loaded is a turn of the session, or in a trace hangs off the root.
	const fallbackParent = sessionNodeId ?? (rootKey ? `agent:${rootKey}` : null);
	const turns: TraceNode[] = [];
	for (const [key, operations] of groups) {
		const node = nodes.get(`agent:${key}`)!;
		const caller = operations.find((operation) => operation.caller_correlation_id)?.caller_correlation_id;
		let callerNodeId = caller && caller !== key && nodes.has(`agent:${caller}`) ? `agent:${caller}` : null;
		// Caller headers are stored as sent, so two tasks can name each other. Parents are assigned one at a time,
		// so refusing a link whose ancestor chain already reaches this node keeps every parent walk finite.
		for (let ancestor = callerNodeId; ancestor; ancestor = nodes.get(ancestor)?.parentId ?? null) {
			if (ancestor === node.id) {
				callerNodeId = null;
				break;
			}
		}
		if (!sessionNodeId && key === rootKey) continue;
		node.parentId = callerNodeId ?? fallbackParent;
		if (sessionNodeId && !callerNodeId) turns.push(node);
	}
	if (sessionNodeId) {
		turns.sort((a, b) => a.start - b.start).forEach((node, index) => (node.turn = index + 1));
		nodes.set(sessionNodeId, {
			id: sessionNodeId,
			kind: "session",
			parentId: null,
			children: [],
			name: options.sessionId!,
			start: turns.length ? Math.min(...turns.map((node) => node.start)) : 0,
			end: turns.length ? Math.max(...turns.map((node) => node.end)) : 0,
			status: "success",
			tokens: 0,
			cost: 0,
			errors: 0,
			spanCount: 0,
		});
	}
	for (const { taskId, entry } of data.llm) {
		const start = toMs(entry.timestamp);
		nodes.set(`llm:${entry.id}`, {
			id: `llm:${entry.id}`,
			kind: "llm",
			parentId: `agent:${taskId}`,
			children: [],
			name: entry.model,
			start,
			end: start + (entry.latency ?? 0),
			status: entry.status,
			tokens: entry.token_usage?.total_tokens ?? 0,
			cost: entry.cost ?? 0,
			errors: 0,
			spanCount: 0,
			llm: entry,
		});
	}
	for (const { entry } of data.llm) {
		for (const [index, check] of guardrailChecks(entry).entries()) {
			const llmNode = nodes.get(`llm:${entry.id}`)!;
			const at = /output|post|response/i.test(check.phase) ? llmNode.end : llmNode.start;
			nodes.set(`guard:${entry.id}:${index}`, {
				id: `guard:${entry.id}:${index}`,
				kind: "guard",
				parentId: llmNode.id,
				children: [],
				name: check.guardrailName || check.ruleName || check.provider || "guardrail",
				start: at,
				end: at,
				status: check.outcome === "blocked" ? "error" : "success",
				tokens: 0, // judge tokens are already in the LLM call's usage and cost
				cost: 0,
				errors: 0,
				spanCount: 0,
				guard: check,
			});
		}
	}
	for (const { taskId, entry } of data.mcp) {
		const start = toMs(entry.timestamp);
		nodes.set(`mcp:${entry.id}`, {
			id: `mcp:${entry.id}`,
			kind: "mcp",
			parentId: `agent:${taskId}`,
			children: [],
			name: entry.tool_name,
			start,
			end: start + (entry.latency ?? 0),
			status: entry.status,
			tokens: 0,
			cost: entry.cost ?? 0,
			errors: 0,
			spanCount: 0,
			mcp: entry,
		});
	}
	for (const node of nodes.values()) {
		if (node.parentId) nodes.get(node.parentId)?.children.push(node.id);
	}
	for (const node of nodes.values()) node.children.sort((a, b) => nodes.get(a)!.start - nodes.get(b)!.start);
	// Roll tokens, cost, errors and span counts up from the leaves.
	const roll = (id: string): TraceNode => {
		const node = nodes.get(id)!;
		node.errors = node.status === "error" ? 1 : 0;
		for (const childId of node.children) {
			const child = roll(childId);
			node.tokens += child.tokens;
			node.cost += child.cost;
			node.errors += child.errors;
			node.spanCount += child.spanCount + (child.kind === "artifact" ? 0 : 1);
		}
		return node;
	};
	const rootNodeId = sessionNodeId ?? (rootKey ? `agent:${rootKey}` : null);
	if (rootNodeId) roll(rootNodeId);
	return { nodes, rootKey, rootNodeId };
}
function findMessageText(value: unknown): string | undefined {
	if (!value || typeof value !== "object") return undefined;
	const record = value as Record<string, unknown>;
	const message = record.message as Record<string, unknown> | undefined;
	if (message && Array.isArray(message.parts)) {
		const text = message.parts
			.map((part) => (part && typeof part === "object" ? (part as Record<string, unknown>).text : undefined))
			.find((t) => typeof t === "string");
		if (typeof text === "string") return text;
	}
	for (const key of ["params", "result", "request"]) {
		const found = findMessageText(record[key]);
		if (found) return found;
	}
	return undefined;
}

/**
 * The user's words from an operation's stored input: the first text part of the A2A message. The list stores a
 * bounded preview that may stop mid-JSON, so a cut-off body falls back to reading the first "text" string.
 */
export function operationPrompt(input?: string): string | undefined {
	if (!input) return undefined;
	const trimmed = input.trim();
	if (!trimmed.startsWith("{")) return trimmed;
	try {
		return findMessageText(JSON.parse(trimmed)) ?? undefined;
	} catch {
		const match = /"text"\s*:\s*"((?:[^"\\]|\\.)*)"/.exec(trimmed);
		if (!match) return undefined;
		try {
			return JSON.parse(`"${match[1]}"`) as string;
		} catch {
			return match[1];
		}
	}
}

/**
 * The guardrail rules checked on an LLM call, one per phase and rule. An intervention (the call was blocked) wins
 * over a detection, which wins over a judge call that only recorded an evaluation.
 */
export function guardrailChecks(entry: LogEntry): GuardrailCheck[] {
	const debug = entry.guardrail_debug;
	if (!debug) return [];
	const byKey = new Map<string, { check: GuardrailCheck; rank: number }>();
	const put = (match: Omit<GuardrailMatch, "action"> & { judge_model?: string }, outcome: string, rank: number) => {
		const key = `${match.phase ?? ""}|${match.rule_id ?? match.rule_name ?? match.guardrail_name ?? ""}`;
		const previous = byKey.get(key);
		if (previous && previous.rank > rank) return;
		byKey.set(key, {
			rank,
			check: {
				phase: match.phase ?? previous?.check.phase ?? "",
				outcome,
				ruleName: match.rule_name ?? previous?.check.ruleName,
				guardrailName: match.guardrail_name ?? previous?.check.guardrailName,
				provider: match.guardrail_provider ?? previous?.check.provider,
				reason: match.reason ?? previous?.check.reason,
				judgeModel: match.judge_model ?? previous?.check.judgeModel,
				assessments: match.assessments ?? previous?.check.assessments,
			},
		});
	};
	for (const call of debug.judge_calls ?? []) put(call, call.action || "evaluated", 0);
	for (const match of debug.detections ?? []) put(match, match.enforced === false ? "monitored" : match.action || "detect", 1);
	for (const match of debug.interventions ?? []) put(match, "blocked", 2);
	return [...byKey.values()].map(({ check }) => check);
}
/**
 * Adds operations to the loaded set. A hydrated operation (with its events and bodies) replaces the metadata-only
 * summary the skeleton loaded first; a summary never replaces anything already loaded.
 */
export function mergeOperations(
	existing: Map<string, AgentLogOperation>,
	incoming: AgentLogOperation[],
	hydrated: boolean,
): Map<string, AgentLogOperation> {
	const next = new Map(existing);
	for (const operation of incoming) {
		if (!hydrated && next.has(operation.id)) continue;
		next.set(operation.id, hydrated ? operation : { ...operation, events: operation.events ?? [] });
	}
	return next;
}

/** createLimiter caps how many tasks run at once; the rest wait in order, and a failure frees its slot. */
export function createLimiter(max: number): <T>(task: () => Promise<T>) => Promise<T> {
	let active = 0;
	const queue: (() => void)[] = [];
	const release = () => {
		active--;
		queue.shift()?.();
	};
	return <T>(task: () => Promise<T>) =>
		new Promise<T>((resolve, reject) => {
			const start = () => {
				active++;
				task().then(resolve, reject).finally(release);
			};
			if (active < max) start();
			else queue.push(start);
		});
}

export type ArtifactKind = { kind: "file"; mediaType: string } | { kind: "data" } | { kind: "text" };

/**
 * What an artifact holds: a file part's media type wins (the PDF a report carries), then data, then text, whatever
 * order the parts arrive in. The view translates "data" and "text"; a media type is shown as-is.
 */
export function artifactType(artifact: Record<string, unknown>): ArtifactKind {
	const parts = (Array.isArray(artifact.parts) ? artifact.parts : []).filter(
		(part): part is Record<string, unknown> => Boolean(part) && typeof part === "object",
	);
	for (const part of parts) {
		const file = (part.file && typeof part.file === "object" ? part.file : part) as Record<string, unknown>;
		const media = file.mimeType ?? file.mediaType ?? file.media_type;
		if (typeof media === "string" && media && (part.file !== undefined || part.url !== undefined || part.raw !== undefined)) {
			return { kind: "file", mediaType: media };
		}
	}
	return parts.some((part) => part.data !== undefined) ? { kind: "data" } : { kind: "text" };
}

function partsText(parts: unknown): string | undefined {
	if (!Array.isArray(parts)) return undefined;
	const text = parts
		.map((part) => (part && typeof part === "object" ? (part as Record<string, unknown>).text : undefined))
		.filter((value): value is string => typeof value === "string" && value.trim() !== "")
		.join("\n");
	return text || undefined;
}

function parseRecord(body?: string): Record<string, unknown> | undefined {
	if (!body) return undefined;
	try {
		const value = JSON.parse(body);
		return value && typeof value === "object" ? (value as Record<string, unknown>) : undefined;
	} catch {
		return undefined;
	}
}

function unwrapResult(root: Record<string, unknown> | undefined): Record<string, unknown> | undefined {
	let current = root;
	for (let depth = 0; current && depth < 3; depth++) {
		const next = (current.result ?? current.params) as Record<string, unknown> | undefined;
		if (!next || typeof next !== "object") break;
		current = next;
	}
	return current;
}

const PROGRESS_STATES = new Set(["TASK_STATE_WORKING", "TASK_STATE_SUBMITTED", "working", "submitted"]);

/**
 * The agent's answer for one operation, as a person would read it in a chat: the message on the task's final
 * status, else the text of the last artifact, else the final Task in a non-streaming response. Progress updates
 * ("open /mcp", "working") are never the answer.
 */
export function operationReply(operation: AgentLogOperation): string | undefined {
	const events = [...(operation.events ?? [])].sort((a, b) => (a.event_sequence ?? 0) - (b.event_sequence ?? 0));
	let statusText: string | undefined;
	for (const event of events) {
		if (event.event_type !== "status_update") continue;
		const root = unwrapResult(parseRecord(event.event_body));
		if (!root) continue;
		const update = (root.statusUpdate ?? root) as Record<string, unknown>;
		const status = update.status as Record<string, unknown> | undefined;
		const state = typeof status?.state === "string" ? status.state : "";
		const text = partsText((status?.message as Record<string, unknown> | undefined)?.parts);
		if (text && !PROGRESS_STATES.has(state)) statusText = text;
	}
	// The streamed artifacts, with append=true chunks already joined; the last one with text is the answer.
	let artifactText: string | undefined;
	for (const record of collectA2AArtifacts(undefined, events)) artifactText = partsText(record.artifact.parts) ?? artifactText;
	if (statusText || artifactText) return statusText ?? artifactText;
	const response = unwrapResult(parseRecord(operation.response_body));
	if (!response) return undefined;
	const task = (response.task ?? response) as Record<string, unknown>;
	const status = task.status as Record<string, unknown> | undefined;
	const state = typeof status?.state === "string" ? status.state : "";
	if (PROGRESS_STATES.has(state)) return undefined; // a task still working has no answer yet
	const fromStatus = partsText((status?.message as Record<string, unknown> | undefined)?.parts);
	if (fromStatus) return fromStatus;
	const artifacts = Array.isArray(task.artifacts) ? (task.artifacts as Record<string, unknown>[]) : [];
	for (let index = artifacts.length - 1; index >= 0; index--) {
		const text = partsText(artifacts[index]?.parts);
		if (text) return text;
	}
	return partsText((response.message as Record<string, unknown> | undefined)?.parts);
}