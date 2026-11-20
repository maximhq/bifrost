import { acceptsOptionShortcuts, confirmArgumentRows, humanizeToolName, isInteractiveTarget } from "./warpQuestion.utils";
import { Button } from "@/components/ui/button";
import { isTypingInto, type WarpConfirmAction, type WarpQuestion, type WarpQuestionOption } from "@/components/warp/warpStream.utils";
import { cn } from "@/lib/utils";
import { MessageCircleQuestion, ShieldCheck, TriangleAlert } from "lucide-react";
import { useCallback, useContext, useEffect, useRef, useState, type CSSProperties } from "react";
import { useTranslation } from "react-i18next";
import { WarpTrayActiveContext } from "./warpTrayContext";

interface WarpQuestionCardProps {
	question: WarpQuestion;
	/** Sends the chosen answer. `label` is what was read, `answer` is what Warp receives. */
	onAnswer: (answer: string, label?: string) => void;
	onSkip: () => void;
}

const OPTION_KEYS = ["A", "B", "C", "D", "E", "F", "G", "H"];

export default function WarpQuestionCard({ question, onAnswer, onSkip }: WarpQuestionCardProps) {
	const { t } = useTranslation("shell");
	// Which option is selected. Without it the letter keys and arrows work but
	// look like they do nothing, so the card reads as click-only and the whole
	// keyboard affordance goes unused.
	const [highlighted, setHighlighted] = useState(0);
	const shortcuts = acceptsOptionShortcuts(question);
	// Bound on the document: the card opens while the composer has focus, so card-level keys would need a click.
	const ownsKeys = !!question.action && confirmOptions(question) !== null;
	const active = useContext(WarpTrayActiveContext);
	useEffect(() => {
		// The confirm card handles its own keys so Esc plays its exit like a click does.
		// A card left on screen for the tray's fold-back is already answered, so its keys must not answer again.
		if (ownsKeys || !active) return;
		const onKeyDown = (event: KeyboardEvent) => {
			// First, even for Escape: otherwise clearing a draft in the composer skips the question.
			if (isTypingInto(event.target as HTMLTextAreaElement | null)) return;
			// With Skip focused, Enter must press Skip rather than pick the highlighted option.
			if (isInteractiveTarget(event.target as HTMLElement | null)) return;

			if (event.key === "Escape") {
				event.preventDefault();
				// On a confirmation card Esc is Cancel, so the model hears a plain no rather than "use your judgement".
				const cancel = confirmOptions(question)?.cancel;
				if (cancel) onAnswer(cancel.hint || cancel.label, cancel.label);
				else onSkip();
				return;
			}
			if (event.metaKey || event.ctrlKey || event.altKey) return;
			// A confirmation card runs a write: only a press on its own buttons picks Confirm or Cancel.
			if (!acceptsOptionShortcuts(question)) return;
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
	}, [question, highlighted, onAnswer, onSkip, ownsKeys, active]);

	const confirm = confirmOptions(question);
	if (confirm && question.action) {
		return <WarpConfirmCard action={question.action} confirm={confirm.confirm} cancel={confirm.cancel} onAnswer={onAnswer} />;
	}

	return (
		// Sits in the composer's tray, which supplies the surface and the divider above the input.
		<div className="warp-reveal" data-testid="warp-question">
			<div className="space-y-3 p-3">
				<div className="text-muted-foreground flex items-center gap-2 text-xs font-normal">
					<MessageCircleQuestion className="size-3.5 shrink-0" />
					<span>{t("warp.question.label")}</span>
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
							{shortcuts && (
								<kbd
									className={cn(
										"flex size-5 shrink-0 items-center justify-center rounded border font-mono text-[10px]",
										index === highlighted ? "border-primary/40 text-primary bg-background" : "bg-background text-muted-foreground",
									)}
								>
									{OPTION_KEYS[index]}
								</kbd>
							)}
							<span className="min-w-0 flex-1 truncate">{option.label}</span>
						</button>
					))}
				</div>

				<div className="flex items-center justify-end gap-2">
					<Button type="button" variant="ghost" size="sm" onClick={onSkip} data-testid="warp-question-skip" className="font-normal">
						<span className="flex items-center gap-1.5">
							<span>{t("warp.question.skip")}</span>
							<kbd className="text-muted-foreground font-mono text-[10px] leading-none">Esc</kbd>
						</span>
					</Button>
				</div>

				{question.allow_other && <p className="text-muted-foreground text-[11px]">{t("warp.question.orType")}</p>}
			</div>
		</div>
	);
}
/** The Confirm and Cancel options of a confirmation card, or null for any other question. */
function confirmOptions(question: WarpQuestion): { confirm: WarpQuestionOption; cancel: WarpQuestionOption } | null {
	if (question.kind !== "confirm") return null;
	const confirm = question.options.find((option) => option.hint?.startsWith("confirm:"));
	const cancel = question.options.find((option) => option.hint === "cancel");
	return confirm && cancel ? { confirm, cancel } : null;
}

interface WarpConfirmCardProps {
	action: WarpConfirmAction;
	confirm: WarpQuestionOption;
	cancel: WarpQuestionOption;
	onAnswer: (answer: string, label?: string) => void;
}

// Confirm holds the answer until its check has drawn; the tray then folds the card back into the input.
const CONFIRM_EXIT_MS = 220;

function prefersReducedMotion(): boolean {
	return typeof window !== "undefined" && !!window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
}

function WarpConfirmCard({ action, confirm, cancel, onAnswer }: WarpConfirmCardProps) {
	const { t } = useTranslation("shell");
	const rows = confirmArgumentRows(action.arguments);
	const destructive = !!action.destructive;
	const Icon = destructive ? TriangleAlert : ShieldCheck;
	// Set on the first answer; it locks both buttons so a double click cannot send twice.
	const [leaving, setLeaving] = useState<"confirm" | "cancel" | null>(null);
	const timer = useRef<ReturnType<typeof setTimeout>>(undefined);
	useEffect(() => () => clearTimeout(timer.current), []);
	// The question went away before Confirm's check finished drawing, such as a message typed in the meantime:
	// the person moved on, so the pending Confirm must not reach the model after it.
	const active = useContext(WarpTrayActiveContext);
	useEffect(() => {
		if (!active) clearTimeout(timer.current);
	}, [active]);

	const answer = useCallback(
		(choice: "confirm" | "cancel") => {
			if (leaving || !active) return;
			const option = choice === "confirm" ? confirm : cancel;
			const send = () => onAnswer(option.hint || option.label, option.label);
			setLeaving(choice);
			if (choice === "cancel" || prefersReducedMotion()) return send();
			timer.current = setTimeout(send, CONFIRM_EXIT_MS);
		},
		[leaving, active, confirm, cancel, onAnswer],
	);

	// Esc cancels. Enter and letters typed elsewhere stay inert: a write runs only on a press of Confirm itself,
	// a click or Enter/Space while it has focus, so a keyboard user can still confirm.
	useEffect(() => {
		if (!active) return;
		const onKeyDown = (event: KeyboardEvent) => {
			if (event.key !== "Escape" || isTypingInto(event.target as HTMLTextAreaElement | null)) return;
			event.preventDefault();
			answer("cancel");
		};
		document.addEventListener("keydown", onKeyDown);
		return () => document.removeEventListener("keydown", onKeyDown);
	}, [answer, active]);

	return (
		<div className="warp-reveal" style={{ animationDelay: "60ms" }} data-testid="warp-question" data-kind="confirm">
			<div>
				<div className="flex items-start gap-2.5 px-3 pt-3">
					<span
						className={cn(
							"warp-pop flex size-7 shrink-0 items-center justify-center rounded-full",
							destructive ? "bg-destructive/10 text-destructive" : "bg-muted text-primary",
						)}
						style={{ animationDelay: "80ms" }}
					>
						<Icon className="size-3.5" />
					</span>
					<div className="flex min-w-0 flex-1 flex-col gap-0.5">
						<div className="flex min-w-0 items-center gap-2">
							<p className="truncate text-sm font-medium">{humanizeToolName(action.tool)}</p>
							<code className="bg-muted text-muted-foreground ml-auto shrink-0 truncate rounded-sm px-1.5 py-1 font-mono text-[10px] leading-none">
								{action.tool}
							</code>
						</div>
						<p className="text-muted-foreground text-xs">
							{destructive ? t("warp.question.confirm.destructiveSubtitle") : t("warp.question.confirm.subtitle")}
						</p>
					</div>
				</div>

				<div className="px-3 pt-3">
					{rows === null ? (
						<pre
							className="warp-row-in bg-muted/50 max-h-48 overflow-auto rounded-sm px-2.5 py-2 font-mono text-xs break-all whitespace-pre-wrap"
							data-testid="warp-confirm-arguments"
						>
							{action.arguments}
						</pre>
					) : rows.length === 0 ? (
						<p className="warp-row-in text-muted-foreground text-xs">{t("warp.question.confirm.noArguments")}</p>
					) : (
						<dl
							className="grid max-h-48 grid-cols-[minmax(0,auto)_minmax(0,1fr)] overflow-auto border-y border-dashed text-xs"
							data-testid="warp-confirm-arguments"
						>
							{rows.map((row, index) => (
								<div
									key={row.key}
									className="warp-row-in col-span-2 grid grid-cols-subgrid gap-x-6 border-dashed py-2 not-first:border-t"
									style={{ "--warp-row": index } as CSSProperties}
								>
									<dt className="text-muted-foreground truncate font-mono">{row.key}</dt>
									<dd
										className={cn(
											"min-w-0 break-words",
											(row.structured || row.empty) && "font-mono",
											row.empty && "text-muted-foreground",
										)}
									>
										{row.value}
									</dd>
								</div>
							))}
						</dl>
					)}
				</div>

				<div className="flex items-center gap-2 px-3 py-3">
					{destructive && (
						<p className="text-destructive flex min-w-0 items-center gap-1.5 text-xs">
							<TriangleAlert className="size-3 shrink-0" />
							<span className="truncate">{t("warp.question.confirm.cannotUndo")}</span>
						</p>
					)}
					<div className="ml-auto flex shrink-0 items-center gap-2">
						<Button
							type="button"
							variant="ghost"
							size="sm"
							disabled={!!leaving}
							onClick={() => answer("cancel")}
							data-testid="warp-question-option-cancel"
							className="font-normal transition-transform active:scale-[0.97] disabled:opacity-100"
						>
							<span>{t("warp.question.confirm.cancel")}</span>
							<kbd className="text-muted-foreground font-mono text-[10px] leading-none">Esc</kbd>
						</Button>
						<Button
							type="button"
							variant={destructive ? "destructive" : "default"}
							size="sm"
							disabled={!!leaving}
							onClick={() => answer("confirm")}
							data-testid="warp-question-option-confirm"
							className="relative transition-transform active:scale-[0.97] disabled:opacity-100"
						>
							{/* The label keeps its width while the check draws in its place, so the button never jumps. */}
							<span className={cn("transition-opacity duration-150", leaving === "confirm" && "opacity-0")}>
								{t("warp.question.confirm.confirm")}
							</span>
							{leaving === "confirm" && (
								<svg viewBox="0 0 16 16" fill="none" aria-hidden="true" className="absolute inset-0 m-auto size-4">
									<path
										d="M3 8.5l3.25 3.25L13 5"
										pathLength={1}
										stroke="currentColor"
										strokeWidth="1.75"
										strokeLinecap="round"
										strokeLinejoin="round"
										className="warp-check-draw"
									/>
								</svg>
							)}
						</Button>
					</div>
				</div>
			</div>
		</div>
	);
}