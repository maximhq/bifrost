import WarpComposer from "@/components/warp/warpComposer";
import { WarpMessage, WarpStreamingMessage } from "@/components/warp/warpMessage";
import { useWarpStream } from "@/components/warp/useWarpStream";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { WarpIcon } from "@/components/ui/icons";
import { ScrollArea } from "@/components/ui/scrollArea";
import { useWarp, type WarpTurn } from "@/lib/contexts/warpContext";
import { useGetWarpConfigQuery } from "@/lib/store/apis/warpApi";
import { Link } from "@tanstack/react-router";
import { SquarePen, X } from "lucide-react";
import { useCallback, useEffect, useRef } from "react";

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

	const { streamingText, streamingToolCalls, isStreaming, send, stop } = useWarpStream({ onTurnComplete });

	// Auto-scroll via a sentinel at the foot of the transcript rather than a ref
	// on the scroll viewport: the shared ScrollArea does not expose its viewport
	// node, and adding a prop to it would put a component every page uses into
	// this diff for one panel's benefit.
	const bottomRef = useRef<HTMLDivElement>(null);
	useEffect(() => {
		// Keyed on the streamed text so it follows the answer as it grows, not just
		// when a turn completes.
		bottomRef.current?.scrollIntoView({ block: "end" });
	}, [warp?.turns.length, streamingText, streamingToolCalls.length]);

	if (!warp) return null;

	const isConfigured = config?.configured ?? false;
	// "Turned off" and "never set up" both report configured:false, but they are
	// different situations with different fixes. Telling someone who has already
	// filled the form in that Warp "isn't set up yet" sends them back to a page
	// that looks complete, and the one control that actually matters goes
	// unnoticed.
	const isDisabledButComplete = !!config && !config.enabled && !!config.provider && !!config.model;

	const ask = (question: string) => {
		const history = warp.turns;
		warp.appendTurn({ role: "user", content: question });
		void send(history, question);
	};

	return (
		// No chrome of its own: WarpDock supplies the surface, so the panel is just
		// a column that fills it.
		<div className="flex h-full min-h-0 w-full flex-col" data-testid="warp-panel">
			{/* h-13 matches the topbar, so the panel header lines up with the page
			    title across the divider. */}
			<header className="flex h-13 shrink-0 items-center justify-between gap-2 border-b px-4">
				<div className="flex min-w-0 items-center gap-2">
					<WarpIcon className="text-muted-foreground size-4 shrink-0" />
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
						<WarpIcon className="size-4" />
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
				<>
					{/* no-table flips the Radix viewport's inner wrapper from display:table
						    back to block. As a table it sizes to its widest child, so one wide
						    markdown table stretches the whole transcript, eats the padding and
						    pushes every line of prose past the right edge. globals.css already
						    carries this rule for the dashboard's scroll area. */}
					<ScrollArea className="min-h-0 flex-1" viewportClassName="no-table">
						<div className="min-w-0 space-y-4 p-4">
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
								warp.turns.map((turn, index) => <WarpMessage key={index} turn={turn} />)
							)}
							{isStreaming && <WarpStreamingMessage text={streamingText} toolCalls={streamingToolCalls} isStreaming={isStreaming} />}
							<div ref={bottomRef} />
						</div>
					</ScrollArea>

					<WarpComposer isStreaming={isStreaming} provider={config?.provider} model={config?.model} onSend={ask} onStop={stop} />
				</>
			)}
		</div>
	);
}