import { Textarea } from "@/components/ui/textarea";
import { Message, MessageRole, SerializedMessage } from "@/lib/message";
import { PencilIcon, XIcon } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import MessageRoleSwitcher from "./messageRoleSwitcher";

export default function ToolResultMessageView({
	message,
	disabled,
	onChange,
	onRemove,
}: {
	message: Message;
	disabled?: boolean;
	onChange: (serialized: SerializedMessage) => void;
	onRemove?: () => void;
}) {
	const [editMode, setEditMode] = useState(false);
	const containerRef = useRef<HTMLDivElement>(null);
	const content = message.content;

	useEffect(() => {
		const handleClick = (e: MouseEvent) => {
			if (!containerRef.current?.contains(e.target as Node)) {
				setEditMode(false);
			}
		};
		document.addEventListener("mousedown", handleClick);
		return () => document.removeEventListener("mousedown", handleClick);
	}, []);

	const handleRoleChange = (role: string) => {
		const clone = message.clone();
		clone.role = role as any;
		onChange(clone.serialized);
	};

	return (
		<div className="group hover:border-border focus-within:border-border rounded-lg border border-transparent px-3 py-2 transition-colors" ref={containerRef}>
			<div className="mb-1 flex items-center">
				<MessageRoleSwitcher role={message.role ?? MessageRole.ASSISTANT} disabled={disabled} onRoleChange={handleRoleChange} />
				<div className="ml-auto flex items-center gap-0.5 overflow-x-auto max-w-1/2 h-5">
					{message.toolCallId && (
						<span className="text-muted-foreground ml-4 truncate font-mono text-xs">{message.toolCallId}</span>
					)}
					{!disabled && (
						<button type="button" aria-label="Edit message" onClick={() => setEditMode(true)} className="rounded-sm p-1 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100 hover:bg-muted focus:bg-muted focus:opacity-100">
							<PencilIcon className="text-muted-foreground hover:text-foreground h-3.5 w-3.5 shrink-0 cursor-pointer" />
						</button>
					)}
					{!disabled && onRemove && (
						<button type="button" aria-label="Delete message" onClick={onRemove} className="rounded-sm p-1 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100 hover:bg-muted focus:bg-muted focus:opacity-100">
							<XIcon className="text-muted-foreground hover:text-foreground h-4 w-4 shrink-0 cursor-pointer" />
						</button>
					)}
				</div>
			</div>
			<div>
				{editMode ? (
					<Textarea
						autoFocus
						value={content}
						className="text-muted-foreground min-h-[20px] resize-none rounded-none border-0 bg-transparent p-0 font-mono text-sm shadow-none focus-visible:ring-0 focus-visible:ring-offset-0 dark:bg-transparent"
						disabled={disabled}
						onChange={(e) => {
							const clone = message.clone();
							clone.content = e.target.value;
							onChange(clone.serialized);
						}}
						onBlur={() => setEditMode(false)}
					/>
				) : (
					<div className="text-muted-foreground min-h-[20px] font-mono text-sm whitespace-pre-wrap">
						{content || <span className="text-muted-foreground italic">Enter tool result...</span>}
					</div>
				)}
			</div>
		</div>
	);
}
