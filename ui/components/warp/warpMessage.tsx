import {
	decodeTurnError,
	formatWarpUsage,
	isInternalWarpLink,
	isPlainLeftClick,
	splitWarpAnswer,
	splitWarpCharts,
	warpErrorDetail,
	warpToolLabel,
	warpToolStatusLabel,
	warpTimeline,
} from "@/components/warp/warpStream.utils";
import type { WarpTurn, WarpTurnToolCall } from "@/lib/contexts/warpContext";
import { cn } from "@/lib/utils";
import { useNavigate } from "@tanstack/react-router";
import { AlertTriangle, Brain, Check, ChevronDown, Info, Loader2 } from "lucide-react";
import { lazy, memo, Suspense, useMemo, useState, type AnchorHTMLAttributes, type ReactNode } from "react";

// Shiki is heavy and most Warp answers are prose, so the renderer is loaded on
// demand. This mirrors how the prompt playground handles the same component.
const LazyMarkdown = lazy(() => import("@/components/ui/markdown").then((module) => ({ default: module.Markdown })));
// recharts, likewise, is only loaded once an answer actually carries a chart.
const LazyWarpChart = lazy(() => import("@/components/warp/warpChart"));

const renderChartLink = (href: string, children: ReactNode) => <WarpAnswerLink href={href}>{children}</WarpAnswerLink>;

/**
 * One completed turn in the transcript.
 *
 * Both roles run the full width of the panel: the question is a bordered block,
 * the answer is plain prose beneath it. Alternating left/right bubbles cost
 * horizontal room the panel does not have - at 400px a right-aligned bubble
 * capped at 85% wraps a one-line question onto three - and the alternation was
 * carrying information the border already carries.
 *
 * Memoized because WarpPanel re-renders on every streamed token (its own
 * useWarpStream state lives at that level), and turn/isLatest are otherwise
 * unchanged for every completed message in between - without this, a long
 * transcript re-runs every past turn's markdown/regex parsing once per token
 * of the answer currently streaming in below it.
 */
export const WarpMessage = memo(function WarpMessage({ turn, isLatest }: { turn: WarpTurn; isLatest?: boolean }) {
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
			className={cn("min-w-0 space-y-2 overflow-x-auto [&_pre]:overflow-x-auto [&_table]:w-full [&_table]:min-w-full", enter)}
			data-testid="warp-message-assistant"
		>
			{turn.partial && <WarpPartialNote />}
			{(turn.content || (turn.toolCalls?.length ?? 0) > 0) && <WarpAnswer content={turn.content} toolCalls={turn.toolCalls} />}
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
});

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
		// overflow-x-auto on the container, not [&_table]:block. The Warp
		// ScrollArea renders only a vertical scrollbar, and Radix will not scroll
		// its viewport horizontally without a horizontal one - so a wide table was
		// clipped by the viewport and again by the dock's overflow-x-clip. Making
		// the table a block would scroll it but drops native table layout and
		// undoes the .no-table viewport fix; a container-level scroller keeps both.
		<div
			className="min-w-0 space-y-2 overflow-x-auto [&_pre]:overflow-x-auto [&_table]:w-full [&_table]:min-w-full"
			data-testid="warp-message-streaming"
		>
			{/* Streamed text is rendered whole: the provenance fence may be
			    half-written, and folding a partial block away would make the
			    answer appear to lose its ending mid-stream. */}
			<WarpTimeline content={text} toolCalls={toolCalls} isStreaming={isStreaming} />
		</div>
	);
}

/**
 * A turn in the order it happened: narration, the lookups it led to, more
 * narration, more lookups, then the answer.
 *
 * Shared by the live and the completed turn so the transcript does not
 * rearrange itself at the moment a turn finishes. Narration is set back - muted
 * and smaller - so the answer is still the thing the eye lands on; at the same
 * weight, five paragraphs of "let me trace those" bury it.
 */
function WarpTimeline({
	content,
	toolCalls,
	isStreaming = false,
}: {
	content: string;
	toolCalls?: WarpTurnToolCall[];
	isStreaming?: boolean;
}) {
	const items = useMemo(() => warpTimeline(content, toolCalls), [content, toolCalls]);
	const last = items[items.length - 1];
	// Waiting on the model: nothing yet, or every lookup so far has come back.
	// That gap is the longest silence in a turn, since it is where the model is
	// actually thinking, and with nothing moving in it the turn reads as hung.
	// While a lookup is still running its own spinner says so.
	const isWaitingOnModel = isStreaming && (!last || (last.kind === "tools" && last.calls.every((call) => call.durationMs !== undefined)));

	return (
		<>
			{items.map((item, index) => {
				if (item.kind === "tools") {
					return <WarpToolCallList key={`tools-${item.calls[0].id}`} calls={item.calls} />;
				}
				const isLast = index === items.length - 1;
				// Charts render as charts, not as the JSON their block carries. Only
				// the item still streaming can have a chart on its way.
				const segments = splitWarpCharts(item.text, isStreaming && isLast);
				return segments.map((segment, segmentIndex) => {
					// Keyed by position: segments never reorder, and a key taken from
					// the text would remount the block on every streamed change.
					const key = `text-${index}-${segmentIndex}`;
					const isLastSegment = isLast && segmentIndex === segments.length - 1;
					if (segment.kind === "chart") {
						return (
							<Suspense key={key} fallback={<WarpChartPlaceholder />}>
								<LazyWarpChart spec={segment.spec} renderLink={renderChartLink} />
							</Suspense>
						);
					}
					if (segment.kind === "chart-pending") return <WarpChartPlaceholder key={key} />;
					if (segment.kind === "chart-invalid") {
						return (
							<div
								key={key}
								className="text-muted-foreground my-3 rounded-sm border border-dashed p-3 text-xs"
								data-testid="warp-chart-invalid"
							>
								Chart unavailable.
							</div>
						);
					}
					return (
						<Suspense key={key} fallback={<div className="text-muted-foreground text-sm">{segment.text}</div>}>
							<LazyMarkdown
								content={segment.text}
								components={{ a: WarpAnswerLink }}
								className={item.final ? undefined : "text-muted-foreground text-[13px]"}
								isStreaming={isStreaming && isLastSegment}
								caret={isStreaming && isLastSegment ? "block" : undefined}
							/>
						</Suspense>
					);
				});
			})}
			{isWaitingOnModel && <WarpThinking />}
		</>
	);
}

/** Holds a chart's place while it loads or streams in. */
function WarpChartPlaceholder() {
	return <div className="bg-muted/40 my-3 h-[240px] animate-pulse rounded-sm border" data-testid="warp-chart-pending" />;
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
 *
 * Memoized: useWarpStream's tool_call_end handler only replaces the one call
 * object that finished (`toolCalls.map((c) => c.id === id ? {...} : c)`), so
 * every other row's `call` prop keeps its old reference on that update - this
 * is what lets memo actually skip them instead of re-rendering the whole list
 * on every tool call that finishes during a multi-tool step.
 */
const WarpToolCallRow = memo(function WarpToolCallRow({ call }: { call: WarpTurnToolCall }) {
	const [expanded, setExpanded] = useState(false);
	const canExpand = !!call.failed && !!call.error;

	// Shared so the interactive and static rows cannot drift apart.
	const summary = (
		<>
			{/* aria-hidden on the icons and an sr-only label beside them: the
			    running/failed/completed state is otherwise carried only by icon
			    shape and color, which a screen reader cannot see. */}
			{call.durationMs === undefined ? (
				<Loader2 aria-hidden="true" className="size-3 shrink-0 animate-spin motion-reduce:animate-none" />
			) : call.failed ? (
				<AlertTriangle aria-hidden="true" className="size-3 shrink-0 text-amber-500" />
			) : (
				<Check aria-hidden="true" className="size-3 shrink-0 text-emerald-500" />
			)}
			<span className="sr-only">{warpToolStatusLabel(call)}</span>
			<span className={cn("truncate", call.durationMs === undefined && "warp-shimmer")}>
				{warpToolLabel(call.name, call.durationMs === undefined)}
			</span>
			{call.durationMs !== undefined && <span className="shrink-0 tabular-nums opacity-60">{call.durationMs}ms</span>}
		</>
	);

	return (
		<li className="text-muted-foreground text-xs" data-testid={`warp-tool-call-${call.name}`}>
			{/* A button when it can expand, a plain div when it cannot. A div with
			    onClick is unreachable by keyboard and announces nothing, so the only
			    account a failed step gives of itself was mouse-only - while a row
			    with nothing to expand must not become a focus stop either. */}
			{canExpand ? (
				<button
					type="button"
					onClick={() => setExpanded((current) => !current)}
					aria-expanded={expanded}
					className="hover:text-foreground flex w-full items-center gap-2 text-left transition-colors"
					data-testid={`warp-tool-call-toggle-${call.name}`}
				>
					{summary}
					<ChevronDown className={cn("size-3 shrink-0 transition-transform", expanded && "rotate-180")} />
				</button>
			) : (
				<div className="flex items-center gap-2">{summary}</div>
			)}
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
});

/**
 * A link inside an answer.
 *
 * Warp's tools give the model root-relative links into the Logs view - a row's
 * detail sheet, or the same filters it just counted. The markdown renderer
 * opens every anchor in a new tab, which for those would mean a second copy of
 * the dashboard; they are followed with the router instead, so the Logs page
 * opens beside the tray with the conversation still there. External links keep
 * the new-tab behaviour.
 */
function WarpAnswerLink({ href, children, ...rest }: AnchorHTMLAttributes<HTMLAnchorElement> & { node?: unknown }) {
	const navigate = useNavigate();
	// `node` is the markdown AST element; it must not reach the DOM.
	const { node: _node, ...anchorProps } = rest;
	if (isInternalWarpLink(href)) {
		return (
			<a
				{...anchorProps}
				href={href}
				className="text-primary underline underline-offset-2"
				data-testid="warp-answer-link"
				onClick={(event) => {
					// A modified or middle click is the browser's to handle: it is how
					// someone opens a cited link in a new tab without losing the answer
					// they are reading.
					if (!isPlainLeftClick(event)) return;
					event.preventDefault();
					void navigate({ href: href! });
				}}
			>
				{children}
			</a>
		);
	}
	return (
		<a {...anchorProps} href={href} target="_blank" rel="noreferrer" className="text-primary underline underline-offset-2">
			{children}
		</a>
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
function WarpAnswer({ content, toolCalls }: { content: string; toolCalls?: WarpTurnToolCall[] }) {
	const [expanded, setExpanded] = useState(false);
	// The provenance block is the last thing in an answer, so folding it away
	// leaves every tool call's offset pointing where it did.
	const { answer, provenance } = splitWarpAnswer(content);

	return (
		<div className="space-y-2">
			<WarpTimeline content={answer} toolCalls={toolCalls} />

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
/**
 * Sits above an answer Warp gave on its last research step. The model was told
 * to stop querying and say what it had, so the text below is honest but may not
 * cover everything asked; the note is what keeps a reader from quoting it as
 * settled.
 */
function WarpPartialNote() {
	return (
		<div
			className="flex items-start gap-2 rounded-md border border-amber-500/30 bg-amber-500/5 p-2.5 text-xs"
			data-testid="warp-partial-answer"
		>
			<Info className="mt-0.5 size-3.5 shrink-0 text-amber-600 dark:text-amber-400" />
			<div className="space-y-0.5">
				<p className="font-medium">Partial answer</p>
				<p className="text-muted-foreground">
					Warp used all of its research steps before it finished checking. This is what it found so far, and it says what it could not
					confirm. A narrower question usually completes.
				</p>
			</div>
		</div>
	);
}

function WarpTurnError({ error }: { error: string }) {
	const [expanded, setExpanded] = useState(false);
	// decodeTurnError rather than a bare split: a message with no colon is a
	// message, not a code. Reading it as a code matched nothing and replaced the
	// status line or network error with the generic fallback.
	const { code, message } = decodeTurnError(error);
	const detail = warpErrorDetail(code, message);

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