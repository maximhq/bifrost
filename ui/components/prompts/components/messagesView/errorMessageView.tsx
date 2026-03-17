import { Message } from "@/lib/message";
import { AlertCircle, XIcon } from "lucide-react";

/**
 * Render a styled error message block with an optional delete control.
 *
 * @param message - The message object whose `content` is displayed inside the error block.
 * @param disabled - When true, the remove button is not rendered.
 * @param onRemove - Callback invoked when the delete button is clicked.
 * @returns The React element that displays the error message view.
 */
export default function ErrorMessageView({ message, disabled, onRemove }: { message: Message; disabled?: boolean; onRemove?: () => void }) {
	return (
		<div className="group hover:border-destructive/30 focus-within:border-destructive/30 rounded-sm border border-transparent px-3 py-2 transition-colors">
			<div className="mb-1 flex items-center h-5">
				<span className="text-destructive flex items-center gap-1 py-0.5 text-xs font-medium uppercase">
					<AlertCircle className="size-3" />
					Error
				</span>
				<div className="ml-auto">
					{!disabled && onRemove && (
						<button type="button" aria-label="Delete message" data-testid="error-msg-delete" onClick={onRemove} className="rounded-sm p-1 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100 hover:bg-muted focus:bg-muted focus:opacity-100">
							<XIcon className="text-muted-foreground hover:text-foreground size-3 shrink-0 cursor-pointer" />
						</button>
					)}
				</div>
			</div>
			<div className="bg-destructive/10 rounded-sm px-2.5 py-1.5">
				<p className="text-muted-foreground text-sm whitespace-pre-wrap">{message.content}</p>
			</div>
		</div>
	);
}
