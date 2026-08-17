import { Button } from "@/components/ui/button";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { warpModelLabel } from "./warpComposer.utils";
import { ArrowUp, Square } from "lucide-react";
import { useState } from "react";
import TextareaAutosize from "react-textarea-autosize";

interface WarpComposerProps {
	isStreaming: boolean;
	disabled?: boolean;
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
export default function WarpComposer({ isStreaming, disabled, provider, model, onSend, onStop }: WarpComposerProps) {
	const [value, setValue] = useState("");

	const submit = () => {
		const question = value.trim();
		if (!question || isStreaming || disabled) return;
		onSend(question);
		setValue("");
	};

	return (
		<div className="shrink-0 p-3">
			<div className="focus-within:border-ring bg-background flex flex-col gap-2 rounded-lg border p-2 transition-colors">
				<TextareaAutosize
					value={value}
					onChange={(event) => setValue(event.target.value)}
					// Enter sends, Shift+Enter breaks the line. Questions here are usually
					// one line, so making the common case require a modifier would be the
					// wrong default.
					onKeyDown={(event) => {
						// An IME commits its candidate with Enter, so submitting on that
						// keypress sends a half-composed question and swallows the commit.
						// nativeEvent.isComposing is the reliable signal; event.keyCode 229
						// is the older equivalent some browsers still report.
						if (event.nativeEvent.isComposing || event.keyCode === 229) return;
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
					<span className="text-muted-foreground flex min-w-0 items-center gap-1.5 px-1 text-xs" data-testid="warp-composer-model">
						{provider && <RenderProviderIcon provider={provider as ProviderIconType} size="xs" className="size-3.5 shrink-0" />}
						{/* The label names the provider in text whenever there is no mark
						    for it. RenderProviderIcon returns null silently for a custom or
						    self-hosted provider, which left this row showing a model name
						    with nothing to say who was answering it. */}
						<span className="truncate">{warpModelLabel(provider, model)}</span>
					</span>
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