import { Button } from "@/components/ui/button";
import { ArrowUp, Square } from "lucide-react";
import { useState } from "react";
import TextareaAutosize from "react-textarea-autosize";

interface WarpComposerProps {
	isStreaming: boolean;
	disabled?: boolean;
	onSend: (question: string) => void;
	onStop: () => void;
}

/** The question input at the foot of the dock. */
export default function WarpComposer({ isStreaming, disabled, onSend, onStop }: WarpComposerProps) {
	const [value, setValue] = useState("");

	const submit = () => {
		const question = value.trim();
		if (!question || isStreaming || disabled) return;
		onSend(question);
		setValue("");
	};

	return (
		<div className="shrink-0 border-t p-3">
			<div className="focus-within:ring-ring flex items-end gap-2 rounded-md border px-2 py-1.5 focus-within:ring-1">
				<TextareaAutosize
					value={value}
					onChange={(event) => setValue(event.target.value)}
					// Enter sends, Shift+Enter breaks the line. Questions here are
					// usually one line, so making the common case require a modifier
					// would be the wrong default.
					onKeyDown={(event) => {
						if (event.key === "Enter" && !event.shiftKey) {
							event.preventDefault();
							submit();
						}
					}}
					placeholder="Ask about your gateway data..."
					disabled={disabled}
					minRows={1}
					maxRows={8}
					data-testid="warp-composer-input"
					className="placeholder:text-muted-foreground max-h-48 flex-1 resize-none bg-transparent text-sm outline-none disabled:opacity-50"
				/>
				{isStreaming ? (
					<Button
						type="button"
						size="icon"
						variant="ghost"
						onClick={onStop}
						aria-label="Stop"
						data-testid="warp-stop-btn"
						className="size-7"
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
						className="size-7"
					>
						<ArrowUp className="size-3.5" />
					</Button>
				)}
			</div>
		</div>
	);
}