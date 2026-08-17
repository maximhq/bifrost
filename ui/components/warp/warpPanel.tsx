import WarpComposer from "@/components/warp/warpComposer";
import { WarpMessage, WarpStreamingMessage } from "@/components/warp/warpMessage";
import WarpQuestionCard from "@/components/warp/warpQuestion";
import { useWarpStream } from "@/components/warp/useWarpStream";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { WarpIcon } from "@/components/ui/icons";
import { ScrollArea } from "@/components/ui/scrollArea";
import { useWarp, type WarpTurn } from "@/lib/contexts/warpContext";
import { useGetWarpConfigQuery } from "@/lib/store/apis/warpApi";
import { Link } from "@tanstack/react-router";
import { useWarpAutoScroll } from "@/components/warp/useWarpAutoScroll";
import { ArrowDown, SquarePen, X } from "lucide-react";
import { useCallback } from "react";

/** Starter questions, shown on an empty conversation. */
const STARTERS = [
	"What did I spend on each provider in the last 7 days?",
	"Which model had the worst p99 latency yesterday?",
	"Who are my top 5 users by cost this week?",
	"Show me failed requests in the last 24 hours",
];

/** The dock's contents: header, transcript, composer. */
export default function WarpPanel() {
	const warp = useWarp();
	const { data: config } = useGetWarpConfigQuery();

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

	const { streamingText, streamingToolCalls, isStreaming, question, clearQuestion, send, stop, resetConversation } = useWarpStream({
		onTurnComplete,
	});

	const { containerRef, contentRef, isPinned, scrollToBottom } = useWarpAutoScroll();

	if (!warp) return null;

	const isConfigured = config?.configured ?? false;
	// "Turned off" and "never set up" both report configured:false, but they are
	// different situations with different fixes. Telling someone who has already
	// filled the form in that Warp "isn't set up yet" sends them back to a page
	// that looks complete, and the one control that actually matters goes
	// unnoticed.
	const isDisabledButComplete = !!config && !config.enabled && !!config.provider && !!config.model;

	const ask = (text: string, label?: string) => {
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
					<Badge variant="secondary" className="shrink-0 text-[10px]">
						ALPHA
					</Badge>
				</div>
				<div className="flex shrink-0 items-center gap-1">
					{warp.turns.length > 0 && (
						<button
							type="button"
							aria-label="New chat"
							data-testid="warp-new-chat-btn"
							onClick={() => {
								stop();
								// Dropping the thread id as well as the transcript, or the next
								// question would be filed under the chat that was just cleared.
								resetConversation();
								warp.clear();
							}}
							className="text-muted-foreground hover:bg-accent hover:text-accent-foreground flex size-7 cursor-pointer items-center justify-center rounded-md transition-colors"
						>
							<SquarePen className="size-3.5" />
						</button>
					)}
					<button
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

			{!isConfigured ? (
				// An unconfigured Warp is fixable by the operator, so the panel points
				// at the fix rather than hiding or showing a bare error.
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
							{/* pb-28 reserves roughly the composer's height, so the last answer
							    can still be scrolled clear of the glass. Without it the final
							    line is permanently half-covered, which is the failure mode of
							    every floating composer. */}
							<div className="min-w-0 space-y-5 p-4 pb-28" ref={contentRef}>
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
								aria-label="Jump to latest"
								data-testid="warp-jump-to-latest"
								className="absolute bottom-32 left-1/2 size-7 -translate-x-1/2 rounded-full shadow-md"
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
					<div className="absolute inset-x-0 bottom-0">
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
									warp.clear();
								}
							}}
							provider={config?.provider}
							model={config?.model}
							onSend={ask}
							onStop={stop}
						/>
					</div>
				</div>
			)}
		</div>
	);
}