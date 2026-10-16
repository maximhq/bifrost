import {
	decodeTurnError,
	formatWarpUsage,
	holdPartialWarpTable,
	holdWarpProvenance,
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
import { Markdown } from "@/components/ui/markdown";
import { cn } from "@/lib/utils";
import { useNavigate } from "@tanstack/react-router";
import { AlertTriangle, Brain, ChevronDown, Info } from "lucide-react";
import { lazy, memo, Suspense, useMemo, useState, type AnchorHTMLAttributes, type ReactNode } from "react";

// Markdown is not lazy: its Suspense fallback showed the raw markdown, which flashed before every answer.
// Recharts is heavy and most answers have no chart.
const LazyWarpChart = lazy(() => import("@/components/warp/warpChart"));

// A light blur clears as each streamed word lands, so the answer resolves into place instead of ticking on.
const WARP_WORD_ANIMATION = { animation: "blurIn", duration: 260, easing: "cubic-bezier(0.165, 0.84, 0.44, 1)", sep: "word" } as const;

const renderChartLink = (href: string, children: ReactNode) => <WarpAnswerLink href={href}>{children}</WarpAnswerLink>;

/** Memoized: WarpPanel re-renders per streamed token, which would re-parse every past turn. */
export const WarpMessage = memo(function WarpMessage({ turn, isLatest }: { turn: WarpTurn; isLatest?: boolean }) {
	if (turn.role === "user") {
		return (
			// Only the newest animates, or reopening the panel flies every message in at once.
			<div className={cn("space-y-1", isLatest && "warp-message-send")} data-testid="warp-message-user">
				{/* Keeps a bare "-7d" reply legible once the question card is gone. */}
				{turn.answeredQuestion && (
					<p className="text-muted-foreground truncate text-[11px]" data-testid="warp-answered-question">
						{turn.answeredQuestion}
					</p>
				)}
				<div className="bg-muted/40 rounded-md border px-3 py-2 text-sm break-words whitespace-pre-wrap">
					{turn.displayContent ?? turn.content}
				</div>
			</div>
		);
	}

	const usage = formatWarpUsage(turn.usage);
	// The answer itself has no entrance: the streaming message already brought it in, and replaying it on finish flickers.
	// The note, usage and error only exist once the turn finishes, so those fade in.
	const finishIn = isLatest ? "warp-fade-in" : undefined;

	return (
		// min-w-0 plus its own scroller so a wide table or code line cannot widen the row.
		<div
			className="min-w-0 space-y-2 overflow-x-auto [&_pre]:overflow-x-auto [&_table]:w-full [&_table]:min-w-full"
			data-testid="warp-message-assistant"
		>
			{turn.partial && <WarpPartialNote className={finishIn} />}
			{(turn.content || (turn.toolCalls?.length ?? 0) > 0) && <WarpAnswer content={turn.content} toolCalls={turn.toolCalls} />}
			{/* Warp's own calls are kept out of the logs, so this is the only place its spend shows. */}
			{usage && (
				<p className={cn("text-muted-foreground text-right text-[11px] tabular-nums", finishIn)} data-testid="warp-usage">
					{usage}
				</p>
			)}
			{turn.error && <WarpTurnError error={turn.error} className={finishIn} />}
		</div>
	);
});

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
		// Container scroller, not [&_table]:block: the ScrollArea has no horizontal bar to scroll with.
		<div
			className="warp-message-in min-w-0 space-y-2 overflow-x-auto [&_pre]:overflow-x-auto [&_table]:w-full [&_table]:min-w-full"
			data-testid="warp-message-streaming"
		>
			{/* Scope block held back: it would render as code, then jump into the toggle when the turn finishes. */}
			<WarpTimeline content={holdWarpProvenance(text)} toolCalls={toolCalls} isStreaming={isStreaming} />
		</div>
	);
}

/** Shared by live and completed turns so the transcript does not rearrange when a turn finishes. */
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
	// Nothing yet, or every lookup is back: the longest silence, which otherwise reads as hung.
	const isWaitingOnModel = isStreaming && (!last || (last.kind === "tools" && last.calls.every((call) => call.durationMs !== undefined)));

	return (
		<>
			{items.map((item, index) => {
				if (item.kind === "tools") {
					return <WarpToolCallList key={`tools-${item.calls[0].id}`} calls={item.calls} animate={isStreaming} />;
				}
				const isLast = index === items.length - 1;
				const isLiveTail = isStreaming && isLast;
				const segments = splitWarpCharts(isLiveTail ? holdPartialWarpTable(item.text) : item.text, isLiveTail);
				return segments.map((segment, segmentIndex) => {
					// Keyed by position: a text-derived key would remount on every streamed change.
					const key = `text-${index}-${segmentIndex}`;
					const isLastSegment = isLast && segmentIndex === segments.length - 1;
					if (segment.kind === "chart") {
						return (
							<Suspense key={key} fallback={<WarpChartPlaceholder />}>
								{/* Inside the boundary, so the chart reveals when it has loaded rather than behind the placeholder. */}
								<div className={isStreaming ? "warp-reveal" : undefined}>
									<LazyWarpChart spec={segment.spec} renderLink={renderChartLink} />
								</div>
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
						<Markdown
							key={key}
							content={segment.text}
							components={{ a: WarpAnswerLink }}
							className={item.final ? undefined : "text-muted-foreground text-[13px]"}
							isStreaming={isStreaming && isLastSegment}
							caret={isStreaming && isLastSegment ? "block" : undefined}
							// Only the live tail: words already on screen keep their nodes, so just new ones fade in.
							animated={isStreaming && isLastSegment ? WARP_WORD_ANIMATION : false}
						/>
					);
				});
			})}
			{isWaitingOnModel && <WarpThinking />}
		</>
	);
}

function WarpChartPlaceholder() {
	return <div className="bg-muted/40 my-3 h-[240px] animate-pulse rounded-sm border" data-testid="warp-chart-pending" />;
}

/** Shows what was queried and how long it took, never the result, which would bury the answer. */
function WarpToolCallList({ calls, animate = false }: { calls: WarpTurnToolCall[]; animate?: boolean }) {
	return (
		<ul className="space-y-1" data-testid="warp-tool-calls">
			{calls.map((call, index) => (
				<WarpToolCallRow key={`${call.id}-${index}`} call={call} animate={animate} />
			))}
		</ul>
	);
}

/**
 * Memoized: tool_call_end replaces only the finished call object, so other rows keep their reference.
 * animate is for live turns only; a finished turn remounts these rows and must not replay them.
 */
const WarpToolCallRow = memo(function WarpToolCallRow({ call, animate }: { call: WarpTurnToolCall; animate: boolean }) {
	const [expanded, setExpanded] = useState(false);
	const canExpand = !!call.failed && !!call.error;
	const isRunning = call.durationMs === undefined;

	const summary = (
		<>
			{/* Icons carry state only by shape and color, so the sr-only label says it for screen readers. */}
			{call.failed ? (
				<AlertTriangle aria-hidden="true" className={cn("size-3 shrink-0 text-amber-500", animate && "warp-pop")} />
			) : (
				<WarpToolStatusIcon isRunning={isRunning} animate={animate} />
			)}
			<span className="sr-only">{warpToolStatusLabel(call)}</span>
			<span className={cn("truncate", isRunning && "warp-shimmer")}>{warpToolLabel(call.name, isRunning)}</span>
			{!isRunning && <span className={cn("shrink-0 tabular-nums opacity-60", animate && "warp-fade-in")}>{call.durationMs}ms</span>}
		</>
	);

	return (
		<li className={cn("text-muted-foreground text-xs", animate && "warp-reveal")} data-testid={`warp-tool-call-${call.name}`}>
			{/* A real button for keyboard access; a row with nothing to expand stays out of the tab order. */}
			{canExpand ? (
				<button
					type="button"
					onClick={() => setExpanded((current) => !current)}
					aria-expanded={expanded}
					className="hover:text-foreground flex w-full items-center gap-2 text-left transition-colors"
					data-testid={`warp-tool-call-toggle-${call.name}`}
				>
					{summary}
					<WarpChevron open={expanded} className="size-3" />
				</button>
			) : (
				<div className="flex items-center gap-2">{summary}</div>
			)}
			{call.error && (
				<WarpCollapse open={expanded}>
					<pre
						className="bg-muted/60 mt-1 ml-5 overflow-x-auto rounded px-2 py-1 text-[11px] whitespace-pre-wrap"
						data-testid="warp-tool-call-error"
					>
						{call.error}
					</pre>
				</WarpCollapse>
			)}
		</li>
	);
});

/**
 * The spinner stays in the row's muted color and the check lands in green as it draws.
 * Spinner and check share one 16px grid and stroke width, so the swap reads as one mark resolving.
 */
function WarpToolStatusIcon({ isRunning, animate }: { isRunning: boolean; animate: boolean }) {
	if (isRunning) {
		return (
			<svg viewBox="0 0 16 16" fill="none" aria-hidden="true" className="size-3 shrink-0 animate-spin motion-reduce:animate-none">
				<circle cx="8" cy="8" r="6" stroke="currentColor" strokeOpacity="0.25" strokeWidth="1.75" />
				<path d="M8 2a6 6 0 0 1 6 6" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" />
			</svg>
		);
	}
	return (
		<svg viewBox="0 0 16 16" fill="none" aria-hidden="true" className="size-3 shrink-0 text-emerald-500">
			{/* pathLength 1 lets the draw-on animation use unit dash values whatever the path's real length. */}
			<path
				d="M3 8.5l3.25 3.25L13 5"
				pathLength={1}
				stroke="currentColor"
				strokeWidth="1.75"
				strokeLinecap="round"
				strokeLinejoin="round"
				className={animate ? "warp-check-draw" : undefined}
			/>
		</svg>
	);
}

/**
 * Animates open and closed by easing the grid row from 0fr to 1fr, which reaches the content's natural height without measuring it.
 * The content stays mounted so it can animate out; inert keeps it out of focus and the accessibility tree while closed.
 */
function WarpCollapse({ open, children }: { open: boolean; children: ReactNode }) {
	return (
		<div
			inert={!open}
			className={cn(
				"grid transition-[grid-template-rows,opacity] duration-200 ease-[cubic-bezier(0.645,0.045,0.355,1)] motion-reduce:transition-none",
				open ? "grid-rows-[1fr] opacity-100" : "grid-rows-[0fr] opacity-0",
			)}
		>
			<div className="min-h-0 overflow-hidden">{children}</div>
		</div>
	);
}

/** Turns with the same timing as WarpCollapse, so the chevron and the panel move as one. */
function WarpChevron({ open, className }: { open: boolean; className?: string }) {
	return (
		<ChevronDown
			className={cn(
				"shrink-0 transition-transform duration-200 ease-[cubic-bezier(0.645,0.045,0.355,1)] motion-reduce:transition-none",
				open && "rotate-180",
				className,
			)}
		/>
	);
}

/** Internal links go through the router so the conversation stays open; external ones open a new tab. */
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
					// Leave modified and middle clicks to the browser so "open in new tab" still works.
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

function WarpAnswer({ content, toolCalls }: { content: string; toolCalls?: WarpTurnToolCall[] }) {
	const [expanded, setExpanded] = useState(false);
	// Provenance is always last, so removing it leaves every tool call's offset valid.
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
						<WarpChevron open={expanded} className="size-3" />
					</button>
					<WarpCollapse open={expanded}>
						<pre
							className="bg-muted/50 mt-1.5 overflow-x-auto rounded px-2 py-1.5 text-[11px] whitespace-pre-wrap"
							data-testid="warp-provenance"
						>
							{provenance}
						</pre>
					</WarpCollapse>
				</div>
			)}
		</div>
	);
}

function WarpPartialNote({ className }: { className?: string }) {
	return (
		<div
			className={cn("flex items-start gap-2 rounded-md border border-amber-500/30 bg-amber-500/5 p-2.5 text-xs", className)}
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

function WarpTurnError({ error, className }: { error: string; className?: string }) {
	const [expanded, setExpanded] = useState(false);
	// Not a bare split: a message with no colon is a message, not a code.
	const { code, message } = decodeTurnError(error);
	const detail = warpErrorDetail(code, message);

	return (
		<div className={cn("border-destructive/30 bg-destructive/5 rounded-md border p-2.5", className)} data-testid="warp-turn-error">
			<button
				type="button"
				onClick={() => setExpanded((current) => !current)}
				aria-expanded={expanded}
				data-testid="warp-turn-error-toggle"
				className="flex w-full cursor-pointer items-start gap-2 text-left"
			>
				<AlertTriangle className="text-destructive mt-0.5 size-3.5 shrink-0" />
				<span className="text-destructive min-w-0 flex-1 text-xs font-normal">{detail.summary}</span>
				<WarpChevron open={expanded} className="text-muted-foreground mt-0.5 size-3.5" />
			</button>

			{/* Spacing sits inside the collapse, so a closed card has no gap under its summary. */}
			<WarpCollapse open={expanded}>
				<div className="text-muted-foreground space-y-2 pt-2 pl-5.5 text-xs" data-testid="warp-turn-error-detail">
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
					{detail.raw && detail.raw !== detail.summary && (
						<p className="bg-muted/60 rounded px-2 py-1 font-mono break-words">{detail.raw}</p>
					)}
				</div>
			</WarpCollapse>
		</div>
	);
}

function WarpThinking() {
	return (
		<p className="warp-thinking-in text-muted-foreground flex items-center gap-1.5 text-xs" data-testid="warp-thinking">
			<Brain className="size-3.5 shrink-0" />
			<span className="warp-shimmer">Thinking</span>
		</p>
	);
}