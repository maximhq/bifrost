import WarpComposer from "@/components/warp/warpComposer";
import WarpHistory from "@/components/warp/warpHistory";
import { WarpMessage, WarpStreamingMessage } from "@/components/warp/warpMessage";
import WarpQuestionCard from "@/components/warp/warpQuestion";
import { useWarpStream } from "@/components/warp/useWarpStream";
import { indexStatusLabel, pendingWarpQuestion, shouldDrainQueue, turnsFromStoredMessages } from "@/components/warp/warpStream.utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { WarpIcon } from "@/components/ui/icons";
import { ScrollArea } from "@/components/ui/scrollArea";
import { useWarp, type WarpTurn } from "@/lib/contexts/warpContext";
import { useGetWarpConfigQuery, useGetWarpLogIndexStatusQuery, useLazyGetWarpConversationQuery } from "@/lib/store/apis/warpApi";
import type { WarpConversation } from "@/lib/types/warp";
import { cn } from "@/lib/utils";
import { Link } from "@tanstack/react-router";
import { useWarpAutoScroll } from "@/components/warp/useWarpAutoScroll";
import { ArrowDown, Database, History, Loader2, SquarePen, X } from "lucide-react";
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

/** The dock's contents: header, transcript, composer. */
export default function WarpPanel() {
	const { t } = useTranslation("shell");
	const starters = [t("warp.starters.spend"), t("warp.starters.latency"), t("warp.starters.users"), t("warp.starters.failed")];
	// Measured so the transcript reserves exactly what the floating controls
	// occupy. Falls back to the old fixed reserve where ResizeObserver is absent,
	// which keeps the panel usable rather than letting the last answer hide.
	// A callback ref, not a plain one. The panel renders a loading branch first
	// while the config query has no cached result, so the controls wrapper does
	// not exist when a [] layout effect runs - the observer was never attached
	// and the reserve stayed at its 112px fallback, which a question card
	// exceeds. Tracking the node in state reruns the effect when it appears.
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
		// isFetching as well: isLoading is true only when nothing is cached, and
		// saving the settings invalidates WarpConfig - so an open panel holding a
		// cached `configured: false` rendered "Warp isn't set up yet" for the whole
		// refetch, right after the operator had finished setting it up.
		isFetching: isConfigFetching,
		isError: isConfigError,
	} = useGetWarpConfigQuery();

	// The launcher unmounts when the dock opens, so without this hand-off the
	// focused element disappears and focus falls back to the body - a keyboard
	// user then tabs through the whole topbar to reach the panel they just
	// opened. The close button is the panel's last stable control, so focus
	// lands on the way back out.
	const closeRef = useRef<HTMLButtonElement>(null);
	useEffect(() => {
		if (warp?.isOpen) closeRef.current?.focus();
	}, [warp?.isOpen]);

	// appendTurn is stable from the context, so the callback identity below stays
	// stable too and the stream hook is not rebuilt on every render.
	const appendTurn = warp?.appendTurn;
	const onTurnComplete = useCallback(
		(turn: WarpTurn) => {
			// A turn with neither text nor an error is an empty answer; recording it
			// would leave a blank bubble in the transcript with nothing to say.
			if (turn.content || turn.error) appendTurn?.(turn);
		},
		[appendTurn],
	);

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
	} = useWarpStream({ onTurnComplete });

	const { containerRef, contentRef, isPinned, scrollToBottom } = useWarpAutoScroll();

	// History is a view over the same column, not a second panel: the transcript
	// steps aside while a thread is being picked and comes back with it loaded.
	const [showHistory, setShowHistory] = useState(false);
	// The thread identity is the stream's, not a second copy. A local copy was
	// only ever written by openStored, so a thread this chat had just created was
	// not recognised as active - deleting it cleared nothing and the next message
	// was filed under the id that had just been deleted.
	const activeConversationId = warp?.conversationId || undefined;
	const [loadConversation] = useLazyGetWarpConversationQuery();

	const isConfigured = config?.configured ?? false;

	// The index chip. Polled faster while a backfill is running, because that is
	// the only time the number moves; otherwise a slow heartbeat is enough to
	// notice a vector store going away. One subscription with a state-driven
	// interval, rather than two overlapping subscriptions relying on RTK Query
	// coalescing them down to the faster of the two - same effective polling,
	// without a reader having to know that coalescing rule to see why.
	const [indexPollMs, setIndexPollMs] = useState(30000);
	const { data: indexStatus, isError: isIndexStatusError } = useGetWarpLogIndexStatusQuery(undefined, {
		skip: !isConfigured,
		pollingInterval: indexPollMs,
	});
	useEffect(() => {
		setIndexPollMs(indexStatus?.state === "indexing" ? 5000 : 30000);
	}, [indexStatus?.state]);
	const indexChip = indexStatus;
	// The chip is the only place that says whether semantic search is usable, so
	// a failed status request must not simply remove it - a silently absent chip
	// reads as "everything is fine". The settings view does not cover this
	// either; it polls the backfill status through a different query.
	const indexStatusUnavailable = !indexChip && isIndexStatusError;

	// Messages typed while an answer is streaming. Sent one per finished turn,
	// in order, so a follow-up asked mid-answer is neither dropped nor fired
	// into the middle of the request it follows up on.
	const [queue, setQueue] = useState<string[]>([]);
	// True while a stored conversation is being fetched. The drain must not run
	// then: if the active stream finishes before loadConversation resolves, the
	// follow-up goes out against the conversation being left, is persisted under
	// it, and then vanishes when openStored replaces the transcript.
	const [isOpeningStored, setIsOpeningStored] = useState(false);
	// ask is redefined every render because it closes over the transcript. The
	// drain effect reaches it through a ref so it always sends with the history
	// as of the turn that just finished, without re-running on every render.
	const askRef = useRef<((text: string) => void) | null>(null);
	const wasStreaming = useRef(isStreaming);
	// Invalidates a stored-conversation load that is still in flight. The
	// history list stays interactive while openStored awaits, so the person can
	// leave history, start a new chat or ask something - and the load finishing
	// afterwards must not replace that newer state with the stored thread.
	// Bumped by every action that moves the panel on; openStored compares its
	// own epoch after the await and drops a result nobody is waiting for.
	const openStoredEpoch = useRef(0);
	// The single invalidation point: every action that abandons a stored load
	// bumps the epoch AND clears the loading flag together. Bumping alone left
	// isOpeningStored true until the stale request settled, so the queue stayed
	// blocked; clearing alone let a superseded load's finally free the queue
	// while a newer load was still pending, and a queued follow-up could drain
	// into the conversation being left.
	const abandonStoredLoad = () => {
		openStoredEpoch.current++;
		setIsOpeningStored(false);
	};
	useEffect(() => {
		// Before wasStreaming is updated, so the streaming-to-idle transition is
		// still pending when the load finishes and the queue drains then instead of
		// being swallowed.
		if (isOpeningStored) return;
		const drain = shouldDrainQueue(wasStreaming.current, isStreaming, queue.length, question !== null, !!error);
		wasStreaming.current = isStreaming;
		if (!drain) return;
		const [next, ...rest] = queue;
		setQueue(rest);
		askRef.current?.(next);
		// question is a dependency because it gates the drain: the queue has to be
		// re-examined when a clarification is answered, not only when a turn ends.
		// error gates it for the same reason. isOpeningStored is a dependency so the
		// queue is re-examined once the load settles, either way.
	}, [isStreaming, queue, question, error, isOpeningStored]);

	if (!warp) return null;

	const openStored = async (conversation: WarpConversation) => {
		// Loaded first, cleared second. A queued follow-up was typed into the thread
		// being left and must not drain into the one being opened - but clearing
		// before the await threw the queue away on a failed load too, when nothing
		// had changed and the messages were still wanted.
		//
		// Nothing between the await and the clear can drain it: the queue drains on
		// the streaming-to-idle transition, and discard() below is what causes that
		// transition, so it happens after both.
		setIsOpeningStored(true);
		const epoch = ++openStoredEpoch.current;
		let detail;
		try {
			detail = await loadConversation(conversation.id).unwrap();
		} finally {
			// Cleared on the failure path too, or a failed open would freeze the
			// queue for the rest of the session - but only by the load that still
			// owns the epoch. A superseded load clearing the flag would unblock
			// the queue while the newer load is still pending.
			if (epoch === openStoredEpoch.current) setIsOpeningStored(false);
		}
		// Superseded while loading: the person left history, started a new chat
		// or asked something. Applying the stored thread now would replace that
		// newer state with an older one they already moved past.
		if (epoch !== openStoredEpoch.current) return;
		setQueue([]);
		// discard, not stop: the in-flight turn belongs to the thread being left,
		// and stop() keeps it - so it would be filed under the one being opened.
		discard();
		const stored = turnsFromStoredMessages(detail.messages);
		warp.replaceTurns(stored);
		openConversation(detail.id);
		// After openConversation, which clears the question of the thread being
		// left. A thread that ended on a question gets its card back, so reopening
		// it is the same as never having left - and its answer is filed under the
		// conversation id set just above.
		warp.setQuestion(pendingWarpQuestion(stored));
		setShowHistory(false);
	};
	// "Turned off" and "never set up" both report configured:false, but they are
	// different situations with different fixes. Telling someone who has already
	// filled the form in that Warp "isn't set up yet" sends them back to a page
	// that looks complete, and the one control that actually matters goes
	// unnoticed.
	const isDisabledButComplete = !!config && !config.enabled && !!config.provider && !!config.model;

	const ask = (text: string, label?: string) => {
		// A question moves the panel on, so a stored load still in flight is for
		// a thread the person is no longer waiting to open.
		abandonStoredLoad();
		const history = warp.turns;
		// Carrying the question onto the user's turn is what makes a bare "-7d" in
		// the transcript legible later: on its own it reads as a non sequitur.
		warp.appendTurn({
			role: "user",
			content: text,
			// Warp receives the hint; the transcript shows what was actually chosen.
			displayContent: label,
			answeredQuestion: question?.question,
		});
		clearQuestion();
		// Your own message always gets shown. Following is a mode the reader can
		// leave by scrolling up, but submitting a question is them asking to be
		// brought back - without this the message they just typed lands off-screen.
		scrollToBottom();
		void send(history, text);
	};
	askRef.current = ask;

	// shouldDrainQueue deliberately refuses to auto-fire a queued follow-up
	// right behind a failed turn - see its own comment - which otherwise leaves
	// a queue that fails once stuck forever, since nothing else sends it. This
	// is the explicit way out: the same "send the next one" step the effect
	// above takes automatically, offered as a button once an error is showing.
	const resumeQueue = () => {
		const [next, ...rest] = queue;
		if (next === undefined) return;
		setQueue(rest);
		askRef.current?.(next);
	};

	return (
		// No chrome of its own: WarpDock supplies the surface, so the panel is just
		// a column that fills it.
		<div className="flex h-full min-h-0 w-full flex-col" data-testid="warp-panel">
			{/* h-13 matches the topbar, so the panel header lines up with the page
			    title across the divider. */}
			<header className="flex h-13 shrink-0 items-center justify-between gap-2 border-b px-4">
				<div className="flex min-w-0 items-center gap-2">
					{/* -mt-0.5 because the glyph's optical centre sits below its box
					    centre - the helmet's wings reach the top edge while the chin
					    stops short - so a box centred against the title still reads low
					    beside it. */}
					<WarpIcon className="text-muted-foreground -mt-0.5 size-5 shrink-0" />
					<h2 className="truncate text-sm font-semibold">Warp</h2>
					{/* Warp answers questions people will act on, so its maturity belongs
					    next to its name rather than buried in a tooltip. */}
					<Badge variant="secondary" className="shrink-0 text-[10px] font-normal">
						{t("warp.panel.preview")}
					</Badge>
					{indexChip && <WarpIndexChip status={indexChip} />}
					{indexStatusUnavailable && (
						<span
							className="text-muted-foreground border-border rounded-full border px-2 py-0.5 text-[11px]"
							data-testid="warp-index-status-unavailable"
							title={t("warp.panel.indexStatusTitle")}
						>
							Index status unavailable
						</span>
					)}
				</div>
				<div className="flex shrink-0 items-center gap-1">
					{isConfigured && (
						<button
							type="button"
							aria-label={showHistory ? t("warp.panel.backToChat") : t("warp.panel.chatHistory")}
							aria-pressed={showHistory}
							data-testid="warp-history-btn"
							onClick={() => {
								// Toggling away from history abandons any stored load still
								// in flight; toggling into it starts fresh anyway.
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
							aria-label={t("warp.panel.newChat")}
							data-testid="warp-new-chat-btn"
							onClick={() => {
								// Dropping the thread id as well as the transcript, or the next
								// question would be filed under the chat that was just cleared.
								// The epoch bump abandons any stored load still in flight, so
								// it cannot repopulate the chat that was just cleared.
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
						aria-label={t("warp.panel.close")}
						data-testid="warp-close-btn"
						onClick={warp.close}
						className="text-muted-foreground hover:bg-accent hover:text-accent-foreground flex size-7 cursor-pointer items-center justify-center rounded-md transition-colors"
					>
						<X className="size-4" />
					</button>
				</div>
			</header>

			{isConfigLoading || isConfigFetching ? (
				// Without this branch the first fetch renders "Warp isn't set up yet",
				// because an undefined config reads as unconfigured - so every open of
				// the dock flashed a false setup prompt at people who had set it up.
				<div className="flex min-h-0 flex-1 items-center justify-center px-6" data-testid="warp-config-loading">
					<p className="text-muted-foreground text-xs">{t("warp.panel.loading")}</p>
				</div>
			) : isConfigError ? (
				<div className="flex min-h-0 flex-1 items-center justify-center px-6 text-center" data-testid="warp-config-error">
					<p className="text-destructive text-xs" role="alert">
						{t("warp.panel.loadFailed")}
					</p>
				</div>
			) : isConfigured && showHistory ? (
				<div className="min-h-0 flex-1" data-testid="warp-history-view">
					<WarpHistory
						activeConversationId={activeConversationId}
						onOpen={openStored}
						onDeleted={(id) => {
							// Only the thread on screen. Deleting some other row leaves the
							// open conversation alone.
							if (id !== activeConversationId) return;
							discard();
							setQueue([]);
							warp.clear();
							// Forget the id too, or the next message reopens the thread
							// that was just deleted by writing to its id.
							resetConversation();
						}}
					/>
				</div>
			) : !isConfigured ? (
				// An unconfigured Warp is fixable by the operator, so the panel points
				// at the fix rather than hiding or showing a bare error.
				<div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-2 px-6 text-center" data-testid="warp-unconfigured">
					<span className="bg-muted text-muted-foreground flex size-9 items-center justify-center rounded-full">
						<WarpIcon className="size-5" />
					</span>
					<p className="text-sm font-medium">{isDisabledButComplete ? t("warp.panel.turnedOff") : t("warp.panel.notSetUp")}</p>
					<p className="text-muted-foreground text-xs">
						{isDisabledButComplete ? t("warp.panel.enableHint") : t("warp.panel.chooseModel")}
					</p>
					<Button asChild size="sm" className="mt-1" data-testid="warp-configure-link">
						<Link to="/workspace/config/warp">{isDisabledButComplete ? t("warp.panel.openSettings") : t("warp.panel.configure")}</Link>
					</Button>
				</div>
			) : (
				// The composer floats over the transcript rather than sitting in the
				// column beside it. Stacked, it needed a strip of its own above the
				// text, and that strip is dead space in the one place the panel can
				// least afford it. Overlaid, the transcript runs the full height and
				// the last line slides under frosted glass instead of stopping at a
				// hard edge.
				<div className="relative flex min-h-0 flex-1 flex-col">
					{/* no-table flips the Radix viewport's inner wrapper from display:table
						    back to block. As a table it sizes to its widest child, so one wide
						    markdown table stretches the whole transcript, eats the padding and
						    pushes every line of prose past the right edge. globals.css already
						    carries this rule for the dashboard's scroll area. */}
					<div className="relative min-h-0 flex-1" ref={containerRef}>
						<ScrollArea className="h-full" viewportClassName="no-table">
							{/* space-y-5 between turns, while the assistant block keeps its own
							    space-y-2 internally - so tool rows stay tight against the answer
							    they belong to and exchanges separate from each other. Uniform
							    spacing makes a question and its answer look as unrelated as two
							    different questions. */}
							{/* The bottom padding is measured, not guessed. The controls float
							    over the transcript, and a fixed reserve only fits one of their
							    shapes: a question card can stand taller than the composer alone,
							    and then the last answer sits permanently under the glass - the
							    failure mode of every floating composer, arrived at by rounding.
							    The extra 16px keeps a little air between the two. */}
							<div className="min-w-0 space-y-5 p-4" style={{ paddingBottom: controlsHeight + 16 }} ref={contentRef}>
								{warp.turns.length === 0 && !isStreaming ? (
									<div className="space-y-3 pt-6" data-testid="warp-empty-state">
										<p className="text-sm font-medium">{t("warp.panel.emptyTitle")}</p>
										<div className="space-y-1.5">
											{starters.map((starter) => (
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
										{/* A failed turn stops the queue from auto-draining (see
										    shouldDrainQueue) so a follow-up cannot fire straight into
										    another broken request. Without this, that queue has no
										    other way to move again - each item's own control only
										    removes it. */}
										{error && (
											<Button type="button" size="sm" variant="outline" data-testid="warp-resume-queue" onClick={resumeQueue}>
												{t("warp.panel.resumeQueue")}
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
													<span className="text-muted-foreground shrink-0 text-[10px] tracking-wide uppercase">
														{t("warp.panel.queued")}
													</span>
													<button
														type="button"
														aria-label={t("warp.panel.removeQueued")}
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

						{/* Only offered once following has stopped. A jump-to-bottom button
							    that is always there is noise, and one that appears while the
							    transcript is already pinned suggests something is missing when
							    nothing is. */}
						{!isPinned && (
							<Button
								type="button"
								size="icon"
								variant="secondary"
								onClick={scrollToBottom}
								aria-label={t("warp.panel.jumpToLatest")}
								data-testid="warp-jump-to-latest"
								// Offset from the measured controls, not a fixed bottom-32. A
								// pending question makes the overlay taller than 128px, and the
								// button then sat underneath the thing it is meant to float above.
								style={{ bottom: controlsHeight + 16 }}
								className="absolute left-1/2 size-7 -translate-x-1/2 rounded-full shadow-md"
							>
								<ArrowDown className="size-3.5" />
							</Button>
						)}
					</div>

					{/* No fill of its own. --background is a shade off --card, so painting
						    it here drew a grey band across the panel's white surface - the
						    seam this was meant to remove, just moved. The composer's own card
						    is the only thing that should read as a surface; the strip around
						    it stays transparent so the transcript runs under it unbroken.
						    The wrapper carries no padding either - each child brings its own,
						    so an absent question card costs nothing. */}
					<div className="absolute inset-x-0 bottom-0" ref={setControlsNode}>
						{/* The question sits on top of the composer, not inside the scrolling
						    transcript: it is about what you are going to say next, so it stays
						    put while you scroll back to read the answer that prompted it. */}
						{question && !isStreaming && (
							<div className="px-3 pt-3 pb-2">
								<WarpQuestionCard
									question={question}
									onAnswer={ask}
									onSkip={() => {
										// Skipping is a real answer. Saying so lets Warp proceed on its
										// own judgement and state what it assumed, rather than asking
										// the same thing again.
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
							// Stopping is a decision about the whole exchange: a follow-up
							// queued behind the answer you just cut off should not fire on
							// its own the moment the cut lands.
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
/**
 * Whether semantic search is usable, as one small chip beside the title.
 *
 * Kept in the header rather than the settings page because it answers the
 * question people have while asking: "why did Warp not find that
 * conversation?". A backfill in progress shows its percentage; a failure shows
 * the cause on hover.
 */
function WarpIndexChip({ status }: { status: Parameters<typeof indexStatusLabel>[0] }) {
	const { label, tone, detail } = indexStatusLabel(status);
	return (
		<Badge
			variant={tone === "error" ? "destructive" : "secondary"}
			title={detail ?? label}
			data-testid="warp-index-chip"
			data-state={status.state}
			className={cn("flex shrink-0 items-center gap-1 text-[10px] font-normal", tone === "muted" && "text-muted-foreground")}
		>
			{tone === "busy" ? <Loader2 className="size-2.5 animate-spin" /> : <Database className="size-2.5" />}
			{label}
		</Badge>
	);
}