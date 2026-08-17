import { formatWarpUsage, warpErrorDetail, warpToolLabel, splitWarpAnswer } from "@/components/warp/warpStream.utils";
import type { WarpTurn, WarpTurnToolCall } from "@/lib/contexts/warpContext";
import { cn } from "@/lib/utils";
import { AlertTriangle, Brain, Check, ChevronDown, Info, Loader2 } from "lucide-react";
import { lazy, Suspense, useState } from "react";

// Shiki is heavy and most Warp answers are prose, so the renderer is loaded on
// demand. This mirrors how the prompt playground handles the same component.
const LazyMarkdown = lazy(() => import("@/components/ui/markdown").then((module) => ({ default: module.Markdown })));

/**
 * One completed turn in the transcript.
 *
 * Both roles run the full width of the panel: the question is a bordered block,
 * the answer is plain prose beneath it. Alternating left/right bubbles cost
 * horizontal room the panel does not have - at 400px a right-aligned bubble
 * capped at 85% wraps a one-line question onto three - and the alternation was
 * carrying information the border already carries.
 */
export function WarpMessage({ turn, isLatest }: { turn: WarpTurn; isLatest?: boolean }) {
	// Only the newest turn animates. Turns are keyed by index, so appending never
	// remounts the ones above - but reopening the panel mounts them all at once,
	// and a transcript where every message flies in at the same time reads as a
	// glitch rather than an arrival.
	const enter = isLatest ? "warp-message-in" : undefined;

	if (turn.role === "user") {
		return (
			<div className={cn("space-y-1", enter)} data-testid="warp-message-user">
				{/* The question this answers, so a bare "-7d" in the transcript stays
				    legible. On its own it reads as a non sequitur once the card that
				    prompted it is gone. */}
				{turn.answeredQuestion && (
					<p className="text-muted-foreground truncate text-[11px]" data-testid="warp-answered-question">
						{turn.answeredQuestion}
					</p>
				)}
				{/* break-words so an unbroken token - a url, an id - wraps instead of
				    widening the block past the panel. */}
				<div className="bg-muted/40 rounded-md border px-3 py-2 text-sm break-words whitespace-pre-wrap">
					{turn.displayContent ?? turn.content}
				</div>
			</div>
		);
	}

	const usage = formatWarpUsage(turn.usage);

	return (
		// min-w-0 so a wide child cannot stretch this row, and wide content is
		// given its own horizontal scroller. A markdown table or a long code line
		// is the one thing in a chat transcript with no natural width limit, and
		// letting it set the row's width breaks the padding for every message.
		<div
			className={cn("min-w-0 space-y-2 [&_pre]:overflow-x-auto [&_table]:block [&_table]:max-w-full [&_table]:overflow-x-auto", enter)}
			data-testid="warp-message-assistant"
		>
			{turn.toolCalls && turn.toolCalls.length > 0 && <WarpToolCallList calls={turn.toolCalls} />}
			{turn.content && <WarpAnswer content={turn.content} />}
			{/* What this answer cost. Warp's own calls never appear in the logs it
			    reads - by design, so it does not corrupt the numbers it reports - so
			    this line is the only place its spend is visible at all. */}
			{usage && (
				<p className="text-muted-foreground text-right text-[11px] tabular-nums" data-testid="warp-usage">
					{usage}
				</p>
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
					{/* Streamed text is rendered whole: the provenance fence may be
					    half-written, and folding a partial block away would make the
					    answer appear to lose its ending mid-stream. */}
					<LazyMarkdown content={text} isStreaming={isStreaming} caret="block" />
				</Suspense>
			) : (
				// Shown whenever a turn is in flight with nothing written yet, tool
				// calls or not. It used to be suppressed once any tool had run, so the
				// gap between a step finishing and the first token arriving - the
				// longest silence in a turn, since that is where the model is actually
				// thinking - had nothing moving in it and read as hung.
				isStreaming && <WarpThinking />
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
				<WarpToolCallRow key={`${call.id}-${index}`} call={call} />
			))}
		</ul>
	);
}

/**
 * One step, expandable when it failed.
 *
 * A failed step with no account of itself makes a retry look like the same
 * query running four times for no reason. The message says which - a result
 * that was too large reads very differently from a filter that did not exist,
 * and only one of them is worth changing the question over.
 */
function WarpToolCallRow({ call }: { call: WarpTurnToolCall }) {
	const [expanded, setExpanded] = useState(false);
	const canExpand = !!call.failed && !!call.error;

	return (
		<li className="text-muted-foreground text-xs" data-testid={`warp-tool-call-${call.name}`}>
			<div
				className={cn("flex items-center gap-2", canExpand && "hover:text-foreground cursor-pointer transition-colors")}
				onClick={canExpand ? () => setExpanded((current) => !current) : undefined}
			>
				{call.durationMs === undefined ? (
					<Loader2 className="size-3 shrink-0 animate-spin" />
				) : call.failed ? (
					<AlertTriangle className="size-3 shrink-0 text-amber-500" />
				) : (
					<Check className="size-3 shrink-0 text-emerald-500" />
				)}
				<span className={cn("truncate", call.durationMs === undefined && "warp-shimmer")}>
					{warpToolLabel(call.name, call.durationMs === undefined)}
				</span>
				{call.durationMs !== undefined && <span className="shrink-0 tabular-nums opacity-60">{call.durationMs}ms</span>}
				{canExpand && <ChevronDown className={cn("size-3 shrink-0 transition-transform", expanded && "rotate-180")} />}
			</div>
			{expanded && call.error && (
				<pre
					className="bg-muted/60 mt-1 ml-5 overflow-x-auto rounded px-2 py-1 text-[11px] whitespace-pre-wrap"
					data-testid="warp-tool-call-error"
				>
					{call.error}
				</pre>
			)}
		</li>
	);
}

/**
 * An answer, with its provenance folded away.
 *
 * The window, scope and filters are what make a number checkable, so they must
 * be available - but they are reference material, not the answer. Inline they
 * push the next question off screen and get re-read on every scroll. Collapsed,
 * they are one click away for the one time someone doubts a figure.
 */
function WarpAnswer({ content }: { content: string }) {
	const [expanded, setExpanded] = useState(false);
	const { answer, provenance } = splitWarpAnswer(content);

	return (
		<div className="space-y-2">
			<Suspense fallback={<div className="text-muted-foreground text-sm">{answer}</div>}>
				<LazyMarkdown content={answer} />
			</Suspense>

			{provenance && (
				<div className="text-muted-foreground">
					<button
						type="button"
						onClick={() => setExpanded((current) => !current)}
						aria-expanded={expanded}
						data-testid="warp-provenance-toggle"
						className="hover:text-foreground flex cursor-pointer items-center gap-1 text-[11px] transition-colors"
					>
						<Info className="size-3 shrink-0" />
						<span>What this covers</span>
						<ChevronDown className={cn("size-3 shrink-0 transition-transform", expanded && "rotate-180")} />
					</button>
					{expanded && (
						<pre
							className="bg-muted/50 mt-1.5 overflow-x-auto rounded px-2 py-1.5 text-[11px] whitespace-pre-wrap"
							data-testid="warp-provenance"
						>
							{provenance}
						</pre>
					)}
				</div>
			)}
		</div>
	);
}

/**
 * A failed turn, expandable.
 *
 * The summary alone tells someone it failed but not what to do, so the only
 * move left is retyping the same question and hoping. Expanding gives the cause
 * and the specific things that change the outcome - and it stays collapsed by
 * default because most failures are self-explanatory in one line.
 */
function WarpTurnError({ error }: { error: string }) {
	const [expanded, setExpanded] = useState(false);
	const [code, ...rest] = error.split(":");
	const detail = warpErrorDetail(code, rest.join(":").trim());

	return (
		<div className="border-destructive/30 bg-destructive/5 space-y-2 rounded-md border p-2.5" data-testid="warp-turn-error">
			<button
				type="button"
				onClick={() => setExpanded((current) => !current)}
				aria-expanded={expanded}
				data-testid="warp-turn-error-toggle"
				className="flex w-full cursor-pointer items-start gap-2 text-left"
			>
				<AlertTriangle className="text-destructive mt-0.5 size-3.5 shrink-0" />
				<span className="text-destructive min-w-0 flex-1 text-xs font-normal">{detail.summary}</span>
				<ChevronDown className={cn("text-muted-foreground mt-0.5 size-3.5 shrink-0 transition-transform", expanded && "rotate-180")} />
			</button>

			{expanded && (
				<div className="text-muted-foreground space-y-2 pl-5.5 text-xs" data-testid="warp-turn-error-detail">
					<p>{detail.cause}</p>
					{detail.suggestions.length > 0 && (
						<div className="space-y-1">
							<p className="text-foreground">What to try:</p>
							<ul className="list-disc space-y-0.5 pl-4">
								{detail.suggestions.map((suggestion) => (
									<li key={suggestion}>{suggestion}</li>
								))}
							</ul>
						</div>
					)}
					{/* The server's own words, kept verbatim and last. It is the only part
					    worth pasting into a bug report, and paraphrasing it would lose the
					    detail that makes it useful. */}
					{detail.raw && detail.raw !== detail.summary && (
						<p className="bg-muted/60 rounded px-2 py-1 font-mono break-words">{detail.raw}</p>
					)}
				</div>
			)}
		</div>
	);
}

/** Placeholder shown between sending and the first token. */
function WarpThinking() {
	return (
		<p className="text-muted-foreground flex items-center gap-1.5 text-xs" data-testid="warp-thinking">
			<Brain className="size-3.5 shrink-0" />
			<span className="warp-shimmer">Thinking</span>
		</p>
	);
}