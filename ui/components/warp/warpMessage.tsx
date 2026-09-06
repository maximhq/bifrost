import { errorMessage, warpToolLabel } from "@/components/warp/warpStream.utils";
import type { WarpTurn, WarpTurnToolCall } from "@/lib/contexts/warpContext";
import { cn } from "@/lib/utils";
import { AlertTriangle, Check, Loader2 } from "lucide-react";
import { lazy, Suspense } from "react";

// Shiki is heavy and most Warp answers are prose, so the renderer is loaded on
// demand. This mirrors how the prompt playground handles the same component.
const LazyMarkdown = lazy(() => import("@/components/ui/markdown").then((module) => ({ default: module.Markdown })));

/** One completed turn in the transcript. */
export function WarpMessage({ turn }: { turn: WarpTurn }) {
	if (turn.role === "user") {
		return (
			<div className="flex justify-end" data-testid="warp-message-user">
				{/* break-words so an unbroken token - a url, an id - wraps instead of
			    widening the bubble past the panel. */}
				<div className="bg-muted max-w-[85%] rounded-md px-3 py-2 text-sm break-words whitespace-pre-wrap">{turn.content}</div>
			</div>
		);
	}

	return (
		// min-w-0 so a wide child cannot stretch this row, and wide content is
		// given its own horizontal scroller. A markdown table or a long code line
		// is the one thing in a chat transcript with no natural width limit, and
		// letting it set the row's width breaks the padding for every message.
		<div
			className="min-w-0 space-y-2 [&_pre]:overflow-x-auto [&_table]:block [&_table]:max-w-full [&_table]:overflow-x-auto"
			data-testid="warp-message-assistant"
		>
			{turn.toolCalls && turn.toolCalls.length > 0 && <WarpToolCallList calls={turn.toolCalls} />}
			{turn.content && (
				<Suspense fallback={<div className="text-muted-foreground text-sm">{turn.content}</div>}>
					<LazyMarkdown content={turn.content} />
				</Suspense>
			)}
			{turn.error && <WarpTurnError error={turn.error} />}
		</div>
	);
}

/**
 * The answer as it streams in.
 *
 * Rendered separately from WarpMessage because it needs isStreaming on the
 * markdown renderer for the caret, and because it must not be keyed into the
 * completed-turn list until it is actually complete.
 */
export function WarpStreamingMessage({
	text,
	toolCalls,
	isStreaming,
}: {
	text: string;
	toolCalls: WarpTurnToolCall[];
	isStreaming: boolean;
}) {
	return (
		<div
			className="min-w-0 space-y-2 [&_pre]:overflow-x-auto [&_table]:block [&_table]:max-w-full [&_table]:overflow-x-auto"
			data-testid="warp-message-streaming"
		>
			{toolCalls.length > 0 && <WarpToolCallList calls={toolCalls} />}
			{text ? (
				<Suspense fallback={<div className="text-muted-foreground text-sm">{text}</div>}>
					<LazyMarkdown content={text} isStreaming={isStreaming} caret="block" />
				</Suspense>
			) : (
				isStreaming && toolCalls.length === 0 && <WarpThinking />
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
function WarpToolCallList({ calls }: { calls: WarpTurnToolCall[] }) {
	return (
		<ul className="space-y-1" data-testid="warp-tool-calls">
			{calls.map((call, index) => (
				<li
					key={`${call.id}-${index}`}
					className="text-muted-foreground flex items-center gap-2 text-xs"
					data-testid={`warp-tool-call-${call.name}`}
				>
					{call.durationMs === undefined ? (
						<Loader2 className="size-3 shrink-0 animate-spin" />
					) : call.failed ? (
						<AlertTriangle className="size-3 shrink-0 text-amber-500" />
					) : (
						<Check className="size-3 shrink-0 text-emerald-500" />
					)}
					<span className="truncate">{warpToolLabel(call.name)}</span>
					{call.durationMs !== undefined && <span className="shrink-0 tabular-nums opacity-60">{call.durationMs}ms</span>}
				</li>
			))}
		</ul>
	);
}

/** Terminal error for a turn, rendered inline rather than as a toast so it stays with the question it belongs to. */
function WarpTurnError({ error }: { error: string }) {
	const [code, ...rest] = error.split(":");
	return (
		<p className={cn("text-destructive flex items-start gap-2 text-xs")} data-testid="warp-turn-error">
			<AlertTriangle className="mt-0.5 size-3 shrink-0" />
			<span>{errorMessage(code, rest.join(":").trim())}</span>
		</p>
	);
}

/** Placeholder shown between sending and the first token. */
function WarpThinking() {
	return (
		<p className="text-muted-foreground flex items-center gap-2 text-xs" data-testid="warp-thinking">
			<Loader2 className="size-3 animate-spin" />
			Thinking
		</p>
	);
}