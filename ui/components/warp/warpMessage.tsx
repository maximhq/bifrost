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

// Lazy: Shiki and recharts are heavy and most answers are plain prose.
const LazyMarkdown = lazy(() => import("@/components/ui/markdown").then((module) => ({ default: module.Markdown })));
const LazyWarpChart = lazy(() => import("@/components/warp/warpChart"));

const renderChartLink = (href: string, children: ReactNode) => <WarpAnswerLink href={href}>{children}</WarpAnswerLink>;

/** Memoized: WarpPanel re-renders per streamed token, which would re-parse every past turn. */
export const WarpMessage = memo(function WarpMessage({ turn, isLatest }: { turn: WarpTurn; isLatest?: boolean }) {
	// Only the newest animates, or reopening the panel flies every message in at once.
	const enter = isLatest ? "warp-message-in" : undefined;

	if (turn.role === "user") {
		return (
			<div className={cn("space-y-1", enter)} data-testid="warp-message-user">
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

	return (
		// min-w-0 plus its own scroller so a wide table or code line cannot widen the row.
		<div
			className={cn("min-w-0 space-y-2 overflow-x-auto [&_pre]:overflow-x-auto [&_table]:w-full [&_table]:min-w-full", enter)}
			data-testid="warp-message-assistant"
		>
			{turn.partial && <WarpPartialNote />}
			{(turn.content || (turn.toolCalls?.length ?? 0) > 0) && <WarpAnswer content={turn.content} toolCalls={turn.toolCalls} />}
			{/* Warp's own calls are kept out of the logs, so this is the only place its spend shows. */}
			{usage && (
				<p className="text-muted-foreground text-right text-[11px] tabular-nums" data-testid="warp-usage">
					{usage}
				</p>
			)}
			{turn.error && <WarpTurnError error={turn.error} />}
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
			className="min-w-0 space-y-2 overflow-x-auto [&_pre]:overflow-x-auto [&_table]:w-full [&_table]:min-w-full"
			data-testid="warp-message-streaming"
		>
			{/* Not split for provenance: a half-written fence would fold the answer's ending away. */}
			<WarpTimeline content={text} toolCalls={toolCalls} isStreaming={isStreaming} />
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
					return <WarpToolCallList key={`tools-${item.calls[0].id}`} calls={item.calls} />;
				}
				const isLast = index === items.length - 1;
				const segments = splitWarpCharts(item.text, isStreaming && isLast);
				return segments.map((segment, segmentIndex) => {
					// Keyed by position: a text-derived key would remount on every streamed change.
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

function WarpChartPlaceholder() {
	return <div className="bg-muted/40 my-3 h-[240px] animate-pulse rounded-sm border" data-testid="warp-chart-pending" />;
}

/** Shows what was queried and how long it took, never the result, which would bury the answer. */
function WarpToolCallList({ calls }: { calls: WarpTurnToolCall[] }) {
	return (
		<ul className="space-y-1" data-testid="warp-tool-calls">
			{calls.map((call, index) => (
				<WarpToolCallRow key={`${call.id}-${index}`} call={call} />
			))}
		</ul>
	);
}

/** Memoized: tool_call_end replaces only the finished call object, so other rows keep their reference. */
const WarpToolCallRow = memo(function WarpToolCallRow({ call }: { call: WarpTurnToolCall }) {
	const [expanded, setExpanded] = useState(false);
	const canExpand = !!call.failed && !!call.error;

	const summary = (
		<>
			{/* Icons carry state only by shape and color, so the sr-only label says it for screen readers. */}
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
	// Not a bare split: a message with no colon is a message, not a code.
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
					{detail.raw && detail.raw !== detail.summary && (
						<p className="bg-muted/60 rounded px-2 py-1 font-mono break-words">{detail.raw}</p>
					)}
				</div>
			)}
		</div>
	);
}

function WarpThinking() {
	return (
		<p className="text-muted-foreground flex items-center gap-1.5 text-xs" data-testid="warp-thinking">
			<Brain className="size-3.5 shrink-0" />
			<span className="warp-shimmer">Thinking</span>
		</p>
	);
}