import { Button } from "@/components/ui/button";
import type { WarpQuestion } from "@/components/warp/warpStream.utils";
import { cn } from "@/lib/utils";
import { MessageCircleQuestion } from "lucide-react";
import { useEffect, useState } from "react";

interface WarpQuestionCardProps {
	question: WarpQuestion;
	/** Sends the chosen answer. `label` is what was read, `answer` is what Warp receives. */
	onAnswer: (answer: string, label?: string) => void;
	/** Declines to answer, letting Warp proceed with its own judgement. */
	onSkip: () => void;
}

/** Letters shown against each option, and the keys that pick them. */
const OPTION_KEYS = ["A", "B", "C", "D", "E", "F", "G", "H"];

/**
 * A structured question from Warp, rendered as a picker.
 *
 * It exists because two things decide a metric answer - which window, and whose
 * traffic - and a number computed over the wrong one is not a smaller answer, it
 * is a different one. Offering options rather than asking someone to type is
 * both faster and gives Warp an answer it can use directly instead of one it has
 * to re-interpret.
 *
 * Skip is always available. A question you cannot decline is a wall, and Warp
 * can proceed on its own judgement as long as it says what it assumed.
 */
export default function WarpQuestionCard({ question, onAnswer, onSkip }: WarpQuestionCardProps) {
	// Which option is selected. Without it the letter keys and arrows work but
	// look like they do nothing, so the card reads as click-only and the whole
	// keyboard affordance goes unused.
	const [highlighted, setHighlighted] = useState(0);
	// A/B/C to pick, Escape to skip. The card appears while the composer has
	// focus, so these are bound at the document level rather than on the card -
	// otherwise answering would require clicking into it first.
	useEffect(() => {
		const onKeyDown = (event: KeyboardEvent) => {
			if (event.key === "Escape") {
				event.preventDefault();
				onSkip();
				return;
			}
			// Never steal a keystroke that is part of typing. Someone who has
			// started writing their own answer has already declined the options.
			const target = event.target as HTMLElement | null;
			if (target && (target.tagName === "TEXTAREA" || target.tagName === "INPUT")) return;
			if (event.metaKey || event.ctrlKey || event.altKey) return;

			if (event.key === "ArrowDown") {
				event.preventDefault();
				setHighlighted((current) => (current + 1) % question.options.length);
				return;
			}
			if (event.key === "ArrowUp") {
				event.preventDefault();
				setHighlighted((current) => (current - 1 + question.options.length) % question.options.length);
				return;
			}
			if (event.key === "Enter") {
				event.preventDefault();
				const option = question.options[highlighted];
				onAnswer(option.hint || option.label, option.label);
				return;
			}

			const index = OPTION_KEYS.indexOf(event.key.toUpperCase());
			if (index >= 0 && index < question.options.length) {
				event.preventDefault();
				const option = question.options[index];
				onAnswer(option.hint || option.label, option.label);
			}
		};
		document.addEventListener("keydown", onKeyDown);
		return () => document.removeEventListener("keydown", onKeyDown);
	}, [question, highlighted, onAnswer, onSkip]);

	return (
		// An opaque base under the tint. The card floats over the transcript, and
		// bg-primary/5 is 5% opaque - so on its own the answer underneath showed
		// straight through the question and its options, leaving two overlapping
		// blocks of text. The wrapper supplies the solid ground the tint needs;
		// keeping the tint on the inner element means the card still reads as a
		// prompt rather than as another message.
		<div className="bg-background rounded-md" data-testid="warp-question">
			<div className="border-primary/40 bg-primary/5 space-y-3 rounded-md border p-3">
				<div className="text-muted-foreground flex items-center gap-2 text-xs font-normal">
					<MessageCircleQuestion className="size-3.5 shrink-0" />
					<span>Question</span>
				</div>

				{/* The only medium weight in the card. Everything else is normal, so the
			    question reads as the one thing being asked rather than competing
			    with its own options. */}
				<p className="text-sm font-medium">{question.question}</p>

				<div className="space-y-1">
					{question.options.map((option, index) => (
						<button
							key={`${option.label}-${index}`}
							type="button"
							// Pointer and keyboard share one highlight, so moving the mouse
							// never leaves two options looking selected at once.
							onMouseEnter={() => setHighlighted(index)}
							onClick={() => onAnswer(option.hint || option.label, option.label)}
							data-testid={`warp-question-option-${index}`}
							data-highlighted={index === highlighted ? "" : undefined}
							// font-normal on the button, not the label inside it: a button
							// carries its own weight from the UA stylesheet, so a span nested in
							// it never wins.
							// bg-accent is a near-white grey, which is invisible against this
							// card's own primary tint - the highlight was there the whole time
							// and simply could not be seen. The selected row uses the primary
							// colour itself, so it reads at a glance instead of on inspection.
							className={cn(
								"flex w-full cursor-pointer items-center gap-2.5 rounded-md px-2 py-1.5 text-left text-sm font-normal transition-colors",
								index === highlighted ? "bg-primary text-primary-foreground" : "hover:bg-primary/10",
							)}
						>
							<kbd
								className={cn(
									"flex size-5 shrink-0 items-center justify-center rounded border font-mono text-[10px]",
									// The key cap inverts with the row, otherwise a muted-on-primary
									// cap is the one thing left unreadable in a selected option.
									index === highlighted
										? "border-primary-foreground/40 text-primary-foreground bg-transparent"
										: "bg-background text-muted-foreground",
								)}
							>
								{OPTION_KEYS[index]}
							</kbd>
							<span className="min-w-0 flex-1 truncate">{option.label}</span>
						</button>
					))}
				</div>

				<div className="flex items-center justify-end gap-2">
					<Button type="button" variant="ghost" size="sm" onClick={onSkip} data-testid="warp-question-skip" className="font-normal">
						{/* One flex row with items-center so the label and the key cap share a
					    centre line. As bare siblings they inherited different
					    line-heights and sat a pixel apart. */}
						<span className="flex items-center gap-1.5">
							<span>Skip</span>
							<kbd className="text-muted-foreground font-mono text-[10px] leading-none">Esc</kbd>
						</span>
					</Button>
				</div>

				{question.allow_other && <p className="text-muted-foreground text-[11px]">Or type your own answer below.</p>}
			</div>
		</div>
	);
}