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
	MessageSquare,
	Search,
	ShieldCheck,
	Sigma,
	Star,
	Wrench,
} from "lucide-react";

/** Icon and colour of each span kind. The colour marks the kind in the tree and on the timeline. */
export const SPAN_KINDS: Record<TraceSpanKind, { icon: LucideIcon; text: string; bar: string }> = {
	llm: { icon: MessageSquare, text: "text-violet-600 dark:text-violet-400", bar: "bg-violet-500" },
	tool: { icon: Wrench, text: "text-amber-600 dark:text-amber-400", bar: "bg-amber-500" },
	task: { icon: CircleDot, text: "text-sky-600 dark:text-sky-400", bar: "bg-sky-500" },
	function: { icon: FunctionSquare, text: "text-cyan-600 dark:text-cyan-400", bar: "bg-cyan-500" },
	agent: { icon: Bot, text: "text-emerald-600 dark:text-emerald-400", bar: "bg-emerald-500" },
	chain: { icon: Link2, text: "text-teal-600 dark:text-teal-400", bar: "bg-teal-500" },
	retriever: { icon: Search, text: "text-orange-600 dark:text-orange-400", bar: "bg-orange-500" },
	embedding: { icon: Sigma, text: "text-fuchsia-600 dark:text-fuchsia-400", bar: "bg-fuchsia-500" },
	reranker: { icon: ListOrdered, text: "text-rose-600 dark:text-rose-400", bar: "bg-rose-500" },
	guardrail: { icon: ShieldCheck, text: "text-lime-600 dark:text-lime-400", bar: "bg-lime-500" },
	score: { icon: Star, text: "text-yellow-600 dark:text-yellow-400", bar: "bg-yellow-500" },
	eval: { icon: FlaskConical, text: "text-indigo-600 dark:text-indigo-400", bar: "bg-indigo-500" },
	event: { icon: Boxes, text: "text-slate-600 dark:text-slate-400", bar: "bg-slate-500" },
	http: { icon: Globe, text: "text-zinc-500", bar: "bg-zinc-400" },
	unknown: { icon: CircleHelp, text: "text-zinc-500", bar: "bg-zinc-400" },
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