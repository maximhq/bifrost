import { Button } from "@/components/ui/button";
import { matchWarpCommands, resolveWarpCommand, type WarpCommand } from "@/components/warp/warpCommands";
import { cn } from "@/lib/utils";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { Link } from "@tanstack/react-router";
import { ArrowUp, Settings2, Square } from "lucide-react";
import { useState } from "react";
import TextareaAutosize from "react-textarea-autosize";

interface WarpComposerProps {
	/** Runs a slash command. Returns true when the input should be cleared. */
	onCommand?: (command: WarpCommand) => void;
	isStreaming: boolean;
	disabled?: boolean;
	/**
	 * Set when a question card is showing above. It only drops this control's own
	 * top padding, since the card already supplies the gap - the two stay
	 * separate cards rather than merging into one.
	 */
	attached?: boolean;
	/** Shown with the provider's mark on the control row, so it is obvious what is answering. */
	provider?: string;
	model?: string;
	onSend: (question: string) => void;
	onStop: () => void;
}

/**
 * The question input at the foot of the dock.
 *
 * Two rows: the textarea owns the full width, and the controls sit on their own
 * line beneath it. A single row has to reserve horizontal space for the send
 * button, which crowds the text at the panel's narrow width and leaves the
 * button pressed against the rounded border. Stacking also gives the control row
 * somewhere to name the model that is answering.
 */
export default function WarpComposer({ isStreaming, disabled, attached, provider, model, onCommand, onSend, onStop }: WarpComposerProps) {
	const [value, setValue] = useState("");
	// Which command the arrow keys have landed on. Reset whenever the list
	// changes, so a shrinking list cannot leave the highlight past its end.
	const [highlighted, setHighlighted] = useState(0);

	const commands = onCommand ? matchWarpCommands(value) : [];
	const menuOpen = commands.length > 0;

	const runCommand = (command: WarpCommand) => {
		onCommand?.(command);
		setValue("");
		setHighlighted(0);
	};

	const submit = () => {
		const text = value.trim();
		if (!text || isStreaming || disabled) return;

		// A command is resolved before anything is sent, so "/clear" never reaches
		// the model as a question.
		const command = resolveWarpCommand(text);
		if (command) {
			runCommand(command);
			return;
		}
		onSend(text);
		setValue("");
	};

	return (
		<div className={cn("shrink-0 px-3 pb-3", attached ? "pt-0" : "pt-3")}>
			{menuOpen && (
				<div className="bg-popover mb-2 overflow-hidden rounded-md border shadow-sm" data-testid="warp-command-menu">
					{commands.map((command, index) => (
						<button
							key={command.id}
							type="button"
							// Mouse and keyboard share one highlight, so moving the pointer
							// does not leave two things looking selected.
							onMouseEnter={() => setHighlighted(index)}
							onClick={() => runCommand(command)}
							data-testid={`warp-command-${command.id}`}
							className={cn(
								"flex w-full cursor-pointer items-baseline gap-2 px-3 py-2 text-left text-sm transition-colors",
								index === highlighted ? "bg-accent" : "hover:bg-accent/50",
							)}
						>
							<span className="font-mono text-xs">/{command.name}</span>
							<span className="text-muted-foreground truncate text-xs font-normal">{command.description}</span>
						</button>
					))}
				</div>
			)}
			<div className="focus-within:border-ring bg-background flex flex-col gap-2 rounded-lg border p-2 transition-colors">
				<TextareaAutosize
					value={value}
					onChange={(event) => setValue(event.target.value)}
					// Enter sends, Shift+Enter breaks the line. Questions here are usually
					// one line, so making the common case require a modifier would be the
					// wrong default.
					onKeyDown={(event) => {
						// The menu owns the arrow keys and Enter only while it is open, so
						// normal typing is untouched the rest of the time.
						if (menuOpen) {
							if (event.key === "ArrowDown") {
								event.preventDefault();
								setHighlighted((current) => (current + 1) % commands.length);
								return;
							}
							if (event.key === "ArrowUp") {
								event.preventDefault();
								setHighlighted((current) => (current - 1 + commands.length) % commands.length);
								return;
							}
							if (event.key === "Escape") {
								event.preventDefault();
								setValue("");
								return;
							}
							if (event.key === "Enter" && !event.shiftKey) {
								event.preventDefault();
								runCommand(commands[Math.min(highlighted, commands.length - 1)]);
								return;
							}
						}
						if (event.key === "Enter" && !event.shiftKey) {
							event.preventDefault();
							submit();
						}
					}}
					placeholder="Ask about your Bifrost data..."
					disabled={disabled}
					minRows={1}
					maxRows={8}
					data-testid="warp-composer-input"
					className="placeholder:text-muted-foreground max-h-48 w-full resize-none bg-transparent px-1 text-sm outline-none disabled:opacity-50"
				/>
				<div className="flex items-center justify-between gap-2">
					{/* Which model is answering. Warp runs on a model chosen separately
					    from the traffic Bifrost serves, so naming it here is the
					    difference between an answer you can weigh and one that arrived
					    from nowhere.

					    min-w-0 + truncate so a long model name shortens rather than
					    pushing the send button out of the row. */}
					{/* The model label is also the way into its settings. Someone reading
					    "which model is this?" is one step from "and how do I change it?".
					    The gear is always visible rather than appearing on hover - an
					    affordance you have to discover by accident is not one. */}
					<Link
						to="/workspace/config/warp"
						className="text-muted-foreground hover:text-foreground hover:bg-accent flex min-w-0 items-center gap-1.5 rounded px-1 py-0.5 text-xs transition-colors"
						data-testid="warp-composer-model"
					>
						{provider && <RenderProviderIcon provider={provider as ProviderIconType} size="xs" className="size-3.5 shrink-0" />}
						<span className="truncate">{model ?? ""}</span>
						<Settings2 className="size-3 shrink-0 opacity-60" />
					</Link>
					{isStreaming ? (
						<Button
							type="button"
							size="icon"
							variant="secondary"
							onClick={onStop}
							aria-label="Stop"
							data-testid="warp-stop-btn"
							className="size-7 shrink-0 rounded-full"
						>
							<Square className="size-3" />
						</Button>
					) : (
						<Button
							type="button"
							size="icon"
							onClick={submit}
							disabled={!value.trim() || disabled}
							aria-label="Send"
							data-testid="warp-send-btn"
							className="size-7 shrink-0 rounded-full"
						>
							<ArrowUp className="size-3.5" />
						</Button>
					)}
				</div>
			</div>
		</div>
	);
}