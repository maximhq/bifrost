import OdinComposer from "@/components/odin/odinComposer";
import { OdinMessage, OdinStreamingMessage } from "@/components/odin/odinMessage";
import { useOdinStream } from "@/components/odin/useOdinStream";
import { OdinIcon } from "@/components/ui/icons";
import { ScrollArea } from "@/components/ui/scrollArea";
import { useOdin, type OdinTurn } from "@/lib/contexts/odinContext";
import { useGetOdinConfigQuery } from "@/lib/store/apis/odinApi";
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
export default function OdinPanel() {
	const odin = useOdin();
	const { data: config } = useGetOdinConfigQuery();

	// appendTurn is stable from the context, so the callback identity below stays
	// stable too and the stream hook is not rebuilt on every render.
	const appendTurn = odin?.appendTurn;
	const onTurnComplete = useCallback(
		(turn: OdinTurn) => {
			// A turn with neither text nor an error is an empty answer; recording it
			// would leave a blank bubble in the transcript with nothing to say.
			if (turn.content || turn.error) appendTurn?.(turn);
		},
		[appendTurn],
	);

	const { streamingText, streamingToolCalls, isStreaming, send, stop } = useOdinStream({ onTurnComplete });

	// Auto-scroll via a sentinel at the foot of the transcript rather than a ref
	// on the scroll viewport: the shared ScrollArea does not expose its viewport
	// node, and adding a prop to it would put a component every page uses into
	// this diff for one panel's benefit.
	const bottomRef = useRef<HTMLDivElement>(null);
	useEffect(() => {
		// Keyed on the streamed text so it follows the answer as it grows, not just
		// when a turn completes.
		bottomRef.current?.scrollIntoView({ block: "end" });
	}, [odin?.turns.length, streamingText, streamingToolCalls.length]);

	if (!odin) return null;

	const isConfigured = config?.configured ?? false;

	const ask = (question: string) => {
		const history = odin.turns;
		odin.appendTurn({ role: "user", content: question });
		void send(history, question);
	};

	return (
		// No chrome of its own: OdinDock supplies the surface, so the panel is just
		// a column that fills it.
		<div className="flex h-full min-h-0 w-full flex-col" data-testid="odin-panel">
			{/* h-13 matches the topbar, so the panel header lines up with the page
			    title across the divider. */}
			<header className="flex h-13 shrink-0 items-center justify-between gap-2 border-b px-4">
				<div className="flex min-w-0 items-center gap-2">
					<OdinIcon className="text-muted-foreground size-4 shrink-0" />
					<h2 className="truncate text-sm font-semibold">Odin</h2>
				</div>
				<div className="flex shrink-0 items-center gap-1">
					{odin.turns.length > 0 && (
						<button
							type="button"
							aria-label="New chat"
							data-testid="odin-new-chat-btn"
							onClick={() => {
								stop();
								odin.clear();
							}}
							className="text-muted-foreground hover:bg-accent hover:text-accent-foreground flex size-7 cursor-pointer items-center justify-center rounded-md transition-colors"
						>
							<SquarePen className="size-3.5" />
						</button>
					)}
					<button
						type="button"
						aria-label="Close Odin"
						data-testid="odin-close-btn"
						onClick={odin.close}
						className="text-muted-foreground hover:bg-accent hover:text-accent-foreground flex size-7 cursor-pointer items-center justify-center rounded-md transition-colors"
					>
						<X className="size-4" />
					</button>
				</div>
			</header>

			{!isConfigured ? (
				// An unconfigured Odin is fixable by the operator, so the panel points
				// at the fix rather than hiding or showing a bare error.
				<div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-2 px-6 text-center" data-testid="odin-unconfigured">
					<span className="bg-muted text-muted-foreground flex size-9 items-center justify-center rounded-full">
						<OdinIcon className="size-4" />
					</span>
					<p className="text-sm font-medium">Odin isn&apos;t set up yet</p>
					<p className="text-muted-foreground text-xs">Choose a model for Odin to run on.</p>
					<Link
						to="/workspace/config/odin"
						className="text-primary mt-1 text-sm font-medium hover:underline"
						data-testid="odin-configure-link"
					>
						Configure Odin
					</Link>
				</div>
			) : (
				<>
					<ScrollArea className="min-h-0 flex-1">
						<div className="space-y-4 p-4">
							{odin.turns.length === 0 && !isStreaming ? (
								<div className="space-y-3 pt-6" data-testid="odin-empty-state">
									<p className="text-sm font-medium">Ask about your gateway data</p>
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
								odin.turns.map((turn, index) => <OdinMessage key={index} turn={turn} />)
							)}
							{isStreaming && <OdinStreamingMessage text={streamingText} toolCalls={streamingToolCalls} isStreaming={isStreaming} />}
							<div ref={bottomRef} />
						</div>
					</ScrollArea>

					<OdinComposer isStreaming={isStreaming} onSend={ask} onStop={stop} />
				</>
			)}
		</div>
	);
}