import { isInteractiveTarget } from "./warpQuestion.utils";
import { Button } from "@/components/ui/button";
import { isTypingInto, type WarpQuestion } from "@/components/warp/warpStream.utils";
import { cn } from "@/lib/utils";
import { MessageCircleQuestion } from "lucide-react";
import { useEffect, useState } from "react";

interface WarpQuestionCardProps {
	question: WarpQuestion;
	/** Sends the chosen answer. `label` is what was read, `answer` is what Warp receives. */
	onAnswer: (answer: string, label?: string) => void;
	onSkip: () => void;
}

const OPTION_KEYS = ["A", "B", "C", "D", "E", "F", "G", "H"];

export default function WarpQuestionCard({ question, onAnswer, onSkip }: WarpQuestionCardProps) {
	const [highlighted, setHighlighted] = useState(0);
	// Bound on the document: the card opens while the composer has focus, so card-level keys would need a click.
	useEffect(() => {
		const onKeyDown = (event: KeyboardEvent) => {
			// First, even for Escape: otherwise clearing a draft in the composer skips the question.
			if (isTypingInto(event.target as HTMLTextAreaElement | null)) return;
			// With Skip focused, Enter must press Skip rather than pick the highlighted option.
			if (isInteractiveTarget(event.target as HTMLElement | null)) return;

			if (event.key === "Escape") {
				event.preventDefault();
				onSkip();
				return;
			}
			if (event.metaKey || event.ctrlKey || event.altKey) return;
			// Empty options would make the modulo below NaN and crash the panel on the next Enter.
			if (question.options.length === 0) return;

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
				// The options list may have shrunk since highlighted was set.
				const option = question.options[highlighted];
				if (!option) return;
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
		// Opaque base under the 3% tint, or the transcript shows through the floating card.
		<div className="bg-background dark:bg-card rounded-md" data-testid="warp-question">
			<div className="border-primary/25 bg-primary/3 space-y-3 rounded-md border p-3">
				<div className="text-muted-foreground flex items-center gap-2 text-xs font-normal">
					<MessageCircleQuestion className="size-3.5 shrink-0" />
					<span>Question</span>
				</div>

				<p className="text-sm font-medium">{question.question}</p>

				<div className="space-y-1">
					{question.options.map((option, index) => (
						<button
							key={`${option.label}-${index}`}
							type="button"
							// Pointer and keyboard share one highlight so two options never look selected.
							onMouseEnter={() => setHighlighted(index)}
							onClick={() => onAnswer(option.hint || option.label, option.label)}
							data-testid={`warp-question-option-${index}`}
							data-highlighted={index === highlighted ? "" : undefined}
							// Primary wash, not bg-accent: accent is invisible against the card's primary tint.
							className={cn(
								"flex w-full cursor-pointer items-center gap-2.5 rounded-md px-2 py-1.5 text-left text-sm font-normal transition-colors",
								index === highlighted ? "bg-primary/15 text-foreground" : "hover:bg-primary/8",
							)}
						>
							<kbd
								className={cn(
									"flex size-5 shrink-0 items-center justify-center rounded border font-mono text-[10px]",
									index === highlighted ? "border-primary/40 text-primary bg-background" : "bg-background text-muted-foreground",
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