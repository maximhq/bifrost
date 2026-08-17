import { odinErrorMessage, odinToolLabel } from "@/components/odin/odinStream.utils";
import type { OdinTurn, OdinTurnToolCall } from "@/lib/contexts/odinContext";
import { cn } from "@/lib/utils";
import { AlertTriangle, Check, Loader2 } from "lucide-react";
import { lazy, Suspense } from "react";

// Shiki is heavy and most Odin answers are prose, so the renderer is loaded on
// demand. This mirrors how the prompt playground handles the same component.
const LazyMarkdown = lazy(() => import("@/components/ui/markdown").then((module) => ({ default: module.Markdown })));

/** One completed turn in the transcript. */
export function OdinMessage({ turn }: { turn: OdinTurn }) {
	if (turn.role === "user") {
		return (
			<div className="flex justify-end" data-testid="odin-message-user">
				<div className="bg-muted max-w-[85%] rounded-md px-3 py-2 text-sm whitespace-pre-wrap">{turn.content}</div>
			</div>
		);
	}

	return (
		<div className="space-y-2" data-testid="odin-message-assistant">
			{turn.toolCalls && turn.toolCalls.length > 0 && <OdinToolCallList calls={turn.toolCalls} />}
			{turn.content && (
				<Suspense fallback={<div className="text-muted-foreground text-sm">{turn.content}</div>}>
					<LazyMarkdown content={turn.content} />
				</Suspense>
			)}
			{turn.error && <OdinTurnError error={turn.error} />}
		</div>
	);
}

/**
 * The answer as it streams in.
 *
 * Rendered separately from OdinMessage because it needs isStreaming on the
 * markdown renderer for the caret, and because it must not be keyed into the
 * completed-turn list until it is actually complete.
 */
export function OdinStreamingMessage({
	text,
	toolCalls,
	isStreaming,
}: {
	text: string;
	toolCalls: OdinTurnToolCall[];
	isStreaming: boolean;
}) {
	return (
		<div className="space-y-2" data-testid="odin-message-streaming">
			{toolCalls.length > 0 && <OdinToolCallList calls={toolCalls} />}
			{text ? (
				<Suspense fallback={<div className="text-muted-foreground text-sm">{text}</div>}>
					<LazyMarkdown content={text} isStreaming={isStreaming} caret="block" />
				</Suspense>
			) : (
				isStreaming && toolCalls.length === 0 && <OdinThinking />
			)}
		</div>
	);
}

/**
 * Tool calls shown as compact rows.
 *
 * These exist so the wait is legible: without them a multi-second research pause
 * looks like the app has hung. They show what was queried and how long it took,
 * never the result - the model consumed that, and dumping rows of JSON into the
 * transcript would bury the answer.
 */
function OdinToolCallList({ calls }: { calls: OdinTurnToolCall[] }) {
	return (
		<ul className="space-y-1" data-testid="odin-tool-calls">
			{calls.map((call, index) => (
				<li
					key={`${call.id}-${index}`}
					className="text-muted-foreground flex items-center gap-2 text-xs"
					data-testid={`odin-tool-call-${call.name}`}
				>
					{call.durationMs === undefined ? (
						<Loader2 className="size-3 shrink-0 animate-spin" />
					) : call.failed ? (
						<AlertTriangle className="size-3 shrink-0 text-amber-500" />
					) : (
						<Check className="size-3 shrink-0 text-emerald-500" />
					)}
					<span className="truncate">{odinToolLabel(call.name)}</span>
					{call.durationMs !== undefined && <span className="shrink-0 tabular-nums opacity-60">{call.durationMs}ms</span>}
				</li>
			))}
		</ul>
	);
}

/** Terminal error for a turn, rendered inline rather than as a toast so it stays with the question it belongs to. */
function OdinTurnError({ error }: { error: string }) {
	const [code, ...rest] = error.split(":");
	return (
		<p className={cn("text-destructive flex items-start gap-2 text-xs")} data-testid="odin-turn-error">
			<AlertTriangle className="mt-0.5 size-3 shrink-0" />
			<span>{odinErrorMessage(code, rest.join(":").trim())}</span>
		</p>
	);
}

/** Placeholder shown between sending and the first token. */
function OdinThinking() {
	return (
		<p className="text-muted-foreground flex items-center gap-2 text-xs" data-testid="odin-thinking">
			<Loader2 className="size-3 animate-spin" />
			Thinking
		</p>
	);
}