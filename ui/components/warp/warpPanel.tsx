import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { WarpIcon } from "@/components/ui/icons";
import { ScrollArea } from "@/components/ui/scrollArea";
import { useWarpAutoScroll } from "@/components/warp/useWarpAutoScroll";
import { useWarpStream } from "@/components/warp/useWarpStream";
import WarpComposer from "@/components/warp/warpComposer";
import WarpHistory from "@/components/warp/warpHistory";
import { WarpMessage, WarpStreamingMessage } from "@/components/warp/warpMessage";
import WarpQuestionCard from "@/components/warp/warpQuestion";
import { indexStatusLabel, pendingWarpQuestion, shouldDrainQueue, turnsFromStoredMessages } from "@/components/warp/warpStream.utils";
import { useWarp } from "@/lib/contexts/warpContext";
import { useGetWarpConfigQuery, useGetWarpLogIndexStatusQuery, useLazyGetWarpConversationQuery } from "@/lib/store/apis/warpApi";
import type { WarpConversation } from "@/lib/types/warp";
import { cn } from "@/lib/utils";
import { Link } from "@tanstack/react-router";
import { ArrowDown, Database, History, Loader2, SquarePen, X } from "lucide-react";
import { useEffect, useLayoutEffect, useRef, useState } from "react";

const STARTERS = [
	"What did I spend on each provider in the last 7 days?",
	"Which model had the worst p99 latency yesterday?",
	"Who are my top 5 users by cost this week?",
	"Show me failed requests in the last 24 hours",
];

export default function WarpPanel() {
	// Node in state, not a ref: the controls mount after the loading branch, so a [] effect would miss them.
	const [controlsNode, setControlsNode] = useState<HTMLDivElement | null>(null);
	const [controlsHeight, setControlsHeight] = useState(112);
	useLayoutEffect(() => {
		if (!controlsNode || typeof ResizeObserver === "undefined") return;
		const observer = new ResizeObserver(() => setControlsHeight(controlsNode.offsetHeight));
		observer.observe(controlsNode);
		setControlsHeight(controlsNode.offsetHeight);
		return () => observer.disconnect();
	}, [controlsNode]);

	const warp = useWarp();
	const {
		data: config,
		isLoading: isConfigLoading,
		// Saving settings refetches config; without isFetching a stale `configured: false` shows "not set up".
		isFetching: isConfigFetching,
		isError: isConfigError,
	} = useGetWarpConfigQuery();

	// The launcher unmounts when the dock opens, so focus would otherwise fall back to the body.
	const closeRef = useRef<HTMLButtonElement>(null);
	useEffect(() => {
		if (warp?.isOpen) closeRef.current?.focus();
	}, [warp?.isOpen]);

	const {
		streamingText,
		streamingToolCalls,
		isStreaming,
		error,
		question,
		clearQuestion,
		send,
		stop,
		discard,
		resetConversation,
		openConversation,
	} = useWarpStream();

	const { containerRef, contentRef, isPinned, scrollToBottom } = useWarpAutoScroll();

	const [showHistory, setShowHistory] = useState(false);
	const activeConversationId = warp?.conversationId || undefined;
	const [loadConversation] = useLazyGetWarpConversationQuery();

	const isConfigured = config?.configured ?? false;

	const [indexPollMs, setIndexPollMs] = useState(30000);
	const { data: indexStatus, isError: isIndexStatusError } = useGetWarpLogIndexStatusQuery(undefined, {
		skip: !isConfigured,
		pollingInterval: indexPollMs,
	});
	useEffect(() => {
		setIndexPollMs(indexStatus?.state === "indexing" ? 5000 : 30000);
	}, [indexStatus?.state]);
	const indexChip = indexStatus;
	// A missing chip reads as "all fine", so a failed status request shows an explicit notice instead.
	const indexStatusUnavailable = !indexChip && isIndexStatusError;

	const [queue, setQueue] = useState<string[]>([]);
	// Blocks the drain during a stored load, or a follow-up is filed under the thread being left.
	const [isOpeningStored, setIsOpeningStored] = useState(false);
	// ask closes over the transcript, so the drain effect reads it via a ref to avoid rerunning every render.
	const askRef = useRef<((text: string) => void) | null>(null);
	const wasStreaming = useRef(isStreaming);
	// Bumped by any action that moves the panel on, so a late stored load cannot overwrite newer state.
	const openStoredEpoch = useRef(0);
	// Epoch bump and flag clear must happen together, or the queue stays blocked or drains too early.
	const abandonStoredLoad = () => {
		openStoredEpoch.current++;
		setIsOpeningStored(false);
	};
	useEffect(() => {
		// Return before updating wasStreaming so the streaming-to-idle transition survives the load.
		if (isOpeningStored) return;
		const drain = shouldDrainQueue(wasStreaming.current, isStreaming, queue.length, question !== null, !!error);
		wasStreaming.current = isStreaming;
		if (!drain) return;
		const [next, ...rest] = queue;
		setQueue(rest);
		askRef.current?.(next);
	}, [isStreaming, queue, question, error, isOpeningStored]);

	if (!warp) return null;

	const openStored = async (conversation: WarpConversation) => {
		// Clear the queue only after a successful load, so a failed open keeps queued messages.
		setIsOpeningStored(true);
		const epoch = ++openStoredEpoch.current;
		let detail;
		try {
			detail = await loadConversation(conversation.id).unwrap();
		} finally {
			// Only the load that still owns the epoch may unblock the queue.
			if (epoch === openStoredEpoch.current) setIsOpeningStored(false);
		}
		if (epoch !== openStoredEpoch.current) return;
		setQueue([]);
		// discard, not stop: stop() keeps the in-flight turn, which would land in the opened thread.
		discard();
		const stored = turnsFromStoredMessages(detail.messages);
		warp.replaceTurns(stored);
		openConversation(detail.id);
		// After openConversation, which clears the previous thread's question.
		warp.setQuestion(pendingWarpQuestion(stored));
		setShowHistory(false);
	};
	// "Turned off" and "never set up" both report configured:false but need different fixes.
	const isDisabledButComplete = !!config && !config.enabled && !!config.provider && !!config.model;

	const ask = (text: string, label?: string) => {
		abandonStoredLoad();
		const history = warp.turns;
		warp.appendTurn({
			role: "user",
			content: text,
			displayContent: label,
			answeredQuestion: question?.question,
		});
		clearQuestion();
		// Sending re-pins the transcript, or the new message lands off-screen.
		scrollToBottom();
		void send(history, text);
	};
	askRef.current = ask;

	// The queue does not auto-drain after an error; this is the manual way to resume it.
	const resumeQueue = () => {
		const [next, ...rest] = queue;
		if (next === undefined) return;
		setQueue(rest);
		askRef.current?.(next);
	};

	return (
		<div className="flex h-full min-h-0 w-full flex-col" data-testid="warp-panel">
			<header className="flex h-13 shrink-0 items-center justify-between gap-2 border-b px-4">
				<div className="flex min-w-0 items-center gap-2">
					{/* -mt-0.5 corrects the glyph's optical centre, which sits below its box centre. */}
					<WarpIcon className="text-muted-foreground -mt-0.5 size-5 shrink-0" />
					<h2 className="truncate text-sm font-semibold">Warp</h2>
					<Badge variant="secondary" className="shrink-0 text-[10px] font-normal">
						Preview
					</Badge>
					{indexChip && <WarpIndexChip status={indexChip} />}
					{indexStatusUnavailable && (
						<span
							className="text-muted-foreground border-border rounded-full border px-2 py-0.5 text-[11px]"
							data-testid="warp-index-status-unavailable"
							title="Warp could not read its index status. Semantic search may or may not be available."
						>
							Index status unavailable
						</span>
					)}
				</div>
				<div className="flex shrink-0 items-center gap-1">
					{isConfigured && (
						<button
							type="button"
							aria-label={showHistory ? "Back to chat" : "Chat history"}
							aria-pressed={showHistory}
							data-testid="warp-history-btn"
							onClick={() => {
								abandonStoredLoad();
								setShowHistory((current) => !current);
							}}
							className={cn(
								"text-muted-foreground hover:bg-accent hover:text-accent-foreground flex size-7 cursor-pointer items-center justify-center rounded-md transition-colors",
								showHistory && "bg-accent text-accent-foreground",
							)}
						>
							<History className="size-3.5" />
						</button>
					)}
					{warp.turns.length > 0 && (
						<button
							type="button"
							aria-label="New chat"
							data-testid="warp-new-chat-btn"
							onClick={() => {
								abandonStoredLoad();
								resetConversation();
								setShowHistory(false);
								setQueue([]);
								warp.clear();
							}}
							className="text-muted-foreground hover:bg-accent hover:text-accent-foreground flex size-7 cursor-pointer items-center justify-center rounded-md transition-colors"
						>
							<SquarePen className="size-3.5" />
						</button>
					)}
					<button
						ref={closeRef}
						type="button"
						aria-label="Close Warp"
						data-testid="warp-close-btn"
						onClick={warp.close}
						className="text-muted-foreground hover:bg-accent hover:text-accent-foreground flex size-7 cursor-pointer items-center justify-center rounded-md transition-colors"
					>
						<X className="size-4" />
					</button>
				</div>
			</header>

			{isConfigLoading || isConfigFetching ? (
				// An undefined config reads as unconfigured, so without this the setup prompt flashes on open.
				<div className="flex min-h-0 flex-1 items-center justify-center px-6" data-testid="warp-config-loading">
					<p className="text-muted-foreground text-xs">Loading Warp...</p>
				</div>
			) : isConfigError ? (
				<div className="flex min-h-0 flex-1 items-center justify-center px-6 text-center" data-testid="warp-config-error">
					<p className="text-destructive text-xs" role="alert">
						Could not load Warp&apos;s configuration. Reload the page to try again.
					</p>
				</div>
			) : isConfigured && showHistory ? (
				<div className="min-h-0 flex-1" data-testid="warp-history-view">
					<WarpHistory
						activeConversationId={activeConversationId}
						onOpen={openStored}
						onDeleted={(id) => {
							if (id !== activeConversationId) return;
							discard();
							setQueue([]);
							warp.clear();
							// Forget the id too, or the next message recreates the deleted thread.
							resetConversation();
						}}
					/>
				</div>
			) : !isConfigured ? (
				<div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-2 px-6 text-center" data-testid="warp-unconfigured">
					<span className="bg-muted text-muted-foreground flex size-9 items-center justify-center rounded-full">
						<WarpIcon className="size-5" />
					</span>
					<p className="text-sm font-medium">{isDisabledButComplete ? "Warp is turned off" : "Warp isn't set up yet"}</p>
					<p className="text-muted-foreground text-xs">
						{isDisabledButComplete ? "Switch Enable Warp on to start asking questions." : "Choose a model for Warp to run on."}
					</p>
					<Button asChild size="sm" className="mt-1" data-testid="warp-configure-link">
						<Link to="/workspace/config/warp">{isDisabledButComplete ? "Open Warp settings" : "Configure Warp"}</Link>
					</Button>
				</div>
			) : (
				<div className="relative flex min-h-0 flex-1 flex-col">
					{/* no-table stops a wide markdown table from stretching the Radix viewport past the panel. */}
					<div className="relative min-h-0 flex-1" ref={containerRef}>
						<ScrollArea className="h-full" viewportClassName="no-table">
							{/* Bottom padding tracks the floating controls, which grow when a question card shows. */}
							<div className="min-w-0 space-y-5 p-4" style={{ paddingBottom: controlsHeight + 16 }} ref={contentRef}>
								{warp.turns.length === 0 && !isStreaming ? (
									<div className="space-y-3 pt-6" data-testid="warp-empty-state">
										<p className="text-sm font-medium">Ask about your Bifrost data</p>
										<div className="space-y-1.5">
											{STARTERS.map((starter) => (
												<button
													key={starter}
													type="button"
													onClick={() => ask(starter)}
													className="hover:bg-accent text-muted-foreground hover:text-foreground w-full cursor-pointer rounded-md border px-3 py-2 text-left text-xs transition-colors"
												>
													{starter}
												</button>
											))}
										</div>
									</div>
								) : (
									warp.turns.map((turn, index) => <WarpMessage key={index} turn={turn} isLatest={index === warp.turns.length - 1} />)
								)}
								{isStreaming && <WarpStreamingMessage text={streamingText} toolCalls={streamingToolCalls} isStreaming={isStreaming} />}
								{queue.length > 0 && (
									<div className="space-y-2">
										{error && (
											<Button type="button" size="sm" variant="outline" data-testid="warp-resume-queue" onClick={resumeQueue}>
												Resume queued messages
											</Button>
										)}
										<ul className="space-y-2" data-testid="warp-queued">
											{queue.map((text, index) => (
												<li
													key={`${index}-${text}`}
													className="flex items-start gap-2 rounded-md border border-dashed px-3 py-2 text-sm"
													data-testid="warp-queued-item"
												>
													<span className="text-muted-foreground min-w-0 flex-1 whitespace-pre-wrap">{text}</span>
													<span className="text-muted-foreground shrink-0 text-[10px] tracking-wide uppercase">Queued</span>
													<button
														type="button"
														aria-label="Remove queued message"
														data-testid="warp-queued-remove"
														onClick={() => setQueue((current) => current.filter((_, position) => position !== index))}
														className="text-muted-foreground hover:text-foreground shrink-0 cursor-pointer"
													>
														<X className="size-3" />
													</button>
												</li>
											))}
										</ul>
									</div>
								)}
							</div>
						</ScrollArea>

						{!isPinned && (
							<Button
								type="button"
								size="icon"
								variant="secondary"
								onClick={scrollToBottom}
								aria-label="Jump to latest"
								data-testid="warp-jump-to-latest"
								// Measured offset: a pending question makes the controls taller than any fixed value.
								style={{ bottom: controlsHeight + 16 }}
								className="absolute left-1/2 size-7 -translate-x-1/2 rounded-full shadow-md"
							>
								<ArrowDown className="size-3.5" />
							</Button>
						)}
					</div>

					<div className="absolute inset-x-0 bottom-0 bg-card" ref={setControlsNode}>
						{question && !isStreaming && (
							<div className="px-3 pt-3 pb-2">
								<WarpQuestionCard
									question={question}
									onAnswer={ask}
									onSkip={() => {
										ask("Use your best judgement and say what you assumed.");
									}}
								/>
							</div>
						)}
						<WarpComposer
							isStreaming={isStreaming}
							attached={!!question && !isStreaming}
							onCommand={(command) => {
								if (command.id === "clear") {
									stop();
									clearQuestion();
									resetConversation();
									setQueue([]);
									warp.clear();
								}
							}}
							provider={config?.provider}
							model={config?.model}
							onSend={ask}
							onQueue={(text) => setQueue((current) => [...current, text])}
							// Stopping also drops queued follow-ups so they don't fire right after the cut.
							onStop={() => {
								setQueue([]);
								stop();
							}}
						/>
					</div>
				</div>
			)}
		</div>
	);
}
function WarpIndexChip({ status }: { status: Parameters<typeof indexStatusLabel>[0] }) {
	const { label, shortLabel, tone, detail } = indexStatusLabel(status);
	return (
		<Badge
			variant={tone === "error" ? "destructive" : "secondary"}
			title={detail ?? label}
			data-testid="warp-index-chip"
			data-state={status.state}
			className={cn("flex shrink-0 items-center gap-1 text-[10px] font-normal", tone === "muted" && "text-muted-foreground")}
		>
			{tone === "busy" ? <Loader2 className="size-2.5 animate-spin" /> : <Database className="size-2.5" />}
			{shortLabel ? (
				<>
					<span className="sm:hidden">{shortLabel}</span>
					<span className="hidden sm:inline">{label}</span>
				</>
			) : (
				label
			)}
		</Badge>
	);
}