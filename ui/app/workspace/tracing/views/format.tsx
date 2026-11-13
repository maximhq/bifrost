import type { TraceSource, TraceSpanKind } from "@/lib/types/traces";
import { formatCompactNumber } from "@/lib/utils/numbers";
import {
	Bot,
	Boxes,
	CircleDot,
	CircleHelp,
	FlaskConical,
	FunctionSquare,
	Globe,
	ListOrdered,
	Link2,
	type LucideIcon,
	Search,
	Sparkles,
	ShieldCheck,
	Sigma,
	Star,
	Wrench,
} from "lucide-react";

// The agent trace's palette (agent-logs/views/agentTraceView.tsx kindStyles), so a span reads the
// same in Traces as in Agent Logs: agents emerald, model calls blue, tools orange, guardrails violet.
const TILE = {
	agent: "bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-400",
	llm: "bg-blue-50 text-blue-700 dark:bg-blue-950/40 dark:text-blue-400",
	tool: "bg-orange-50 text-orange-700 dark:bg-orange-950/40 dark:text-orange-400",
	guard: "bg-violet-50 text-violet-700 dark:bg-violet-950/40 dark:text-violet-400",
	retriever: "bg-cyan-50 text-cyan-700 dark:bg-cyan-950/40 dark:text-cyan-400",
	eval: "bg-rose-50 text-rose-700 dark:bg-rose-950/40 dark:text-rose-400",
	plain: "bg-muted text-foreground",
} as const;

/** Icon, tile and timeline bar of each span kind. */
export const SPAN_KINDS: Record<TraceSpanKind, { icon: LucideIcon; tile: string; text: string; bar: string }> = {
	llm: { icon: Sparkles, tile: TILE.llm, text: "text-blue-700 dark:text-blue-400", bar: "bg-blue-500" },
	embedding: { icon: Sigma, tile: TILE.llm, text: "text-blue-700 dark:text-blue-400", bar: "bg-blue-500" },
	reranker: { icon: ListOrdered, tile: TILE.llm, text: "text-blue-700 dark:text-blue-400", bar: "bg-blue-500" },
	agent: { icon: Bot, tile: TILE.agent, text: "text-emerald-700 dark:text-emerald-400", bar: "bg-emerald-500" },
	tool: { icon: Wrench, tile: TILE.tool, text: "text-orange-700 dark:text-orange-400", bar: "bg-orange-500" },
	guardrail: { icon: ShieldCheck, tile: TILE.guard, text: "text-violet-700 dark:text-violet-400", bar: "bg-violet-500" },
	retriever: { icon: Search, tile: TILE.retriever, text: "text-cyan-700 dark:text-cyan-400", bar: "bg-cyan-500" },
	score: { icon: Star, tile: TILE.eval, text: "text-rose-700 dark:text-rose-400", bar: "bg-rose-500" },
	eval: { icon: FlaskConical, tile: TILE.eval, text: "text-rose-700 dark:text-rose-400", bar: "bg-rose-500" },
	chain: { icon: Link2, tile: TILE.plain, text: "text-foreground", bar: "bg-zinc-400" },
	task: { icon: CircleDot, tile: TILE.plain, text: "text-foreground", bar: "bg-zinc-400" },
	function: { icon: FunctionSquare, tile: TILE.plain, text: "text-foreground", bar: "bg-zinc-400" },
	event: { icon: Boxes, tile: TILE.plain, text: "text-foreground", bar: "bg-zinc-400" },
	http: { icon: Globe, tile: TILE.plain, text: "text-muted-foreground", bar: "bg-zinc-400" },
	unknown: { icon: CircleHelp, tile: TILE.plain, text: "text-muted-foreground", bar: "bg-zinc-400" },
};

export function spanKindMeta(kind: string) {
	return SPAN_KINDS[kind as TraceSpanKind] ?? SPAN_KINDS.unknown;
}

export const SOURCE_LABELS: Record<TraceSource, string> = {
	braintrust: "Braintrust",
	langfuse: "Langfuse",
	arize: "Arize",
	otel: "OpenTelemetry",
};

export function formatDuration(ms?: number | null): string {
	if (ms === undefined || ms === null || Number.isNaN(ms)) return "-";
	if (ms < 1) return "<1ms";
	if (ms < 1000) return `${Math.round(ms)}ms`;
	if (ms < 60_000) return `${(ms / 1000).toFixed(ms < 10_000 ? 2 : 1)}s`;
	const minutes = Math.floor(ms / 60_000);
	return `${minutes}m ${Math.round((ms % 60_000) / 1000)}s`;
}

export function formatCost(cost?: number | null): string {
	if (cost === undefined || cost === null) return "-";
	if (cost === 0) return "$0";
	if (cost < 0.0001) return "<$0.0001";
	return `$${cost < 1 ? cost.toFixed(4) : cost.toFixed(2)}`;
}

export function formatTokens(tokens?: number | null): string {
	if (!tokens) return "-";
	return formatCompactNumber(tokens);
}

/** Pretty-prints a stored span value: JSON when it is JSON, the text as it is otherwise. */
export function prettyValue(value?: string): string {
	if (!value) return "";
	const trimmed = value.trim();
	if (trimmed.startsWith("{") || trimmed.startsWith("[")) {
		try {
			return JSON.stringify(JSON.parse(trimmed), null, 2);
		} catch {
			return value;
		}
	}
	return value;
}

/** One line of a stored span value, for a table cell. */
export function previewValue(value?: string, max = 120): string {
	if (!value) return "";
	const flat = value.replace(/\s+/g, " ").trim();
	return flat.length > max ? `${flat.slice(0, max)}…` : flat;
}