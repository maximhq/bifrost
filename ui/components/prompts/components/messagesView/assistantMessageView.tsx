import { Textarea } from "@/components/ui/textarea";
import { Message, SerializedMessage } from "@/lib/message";
import { PencilIcon, XIcon } from "lucide-react";
import { Markdown } from "@/components/ui/markdown";
import { useEffect, useRef, useState } from "react";
import MessageRoleSwitcher from "./messageRoleSwitcher";

export function AssistantMessageView({
	message,
	disabled,
	isStreaming,
	onChange,
	onRemove,
}: {
	message: Message;
	disabled?: boolean;
	isStreaming?: boolean;
	onChange: (serialized: SerializedMessage) => void;
	onRemove?: () => void;
}) {
	const [editMode, setEditMode] = useState(false);
	const containerRef = useRef<HTMLDivElement>(null);
	const content = message.content;
	const isEmpty = !content;

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
		<div className="group hover:border-border rounded-lg border border-transparent px-3 py-2 transition-colors" ref={containerRef}>
			<div className="mb-1 flex items-center">
				<MessageRoleSwitcher role={message.role ?? ""} disabled={disabled} onRoleChange={handleRoleChange} />
				<div className="ml-auto flex items-center gap-2">
					{!disabled && !isStreaming && (
						<PencilIcon
							className="text-muted-foreground hover:text-foreground h-3.5 w-3.5 shrink-0 cursor-pointer opacity-0 transition-opacity group-hover:opacity-100"
							onClick={() => setEditMode(true)}
						/>
					)}
					{!disabled && onRemove && (
						<XIcon
							className="text-muted-foreground hover:text-foreground h-4 w-4 shrink-0 cursor-pointer opacity-0 transition-opacity group-hover:opacity-100"
							onClick={onRemove}
						/>
					)}
				</div>
			</div>

			<div>
				{isStreaming && isEmpty ? (
					<div className="flex items-center gap-1 py-1">
						<span className="bg-muted-foreground h-2 w-2 animate-bounce rounded-full opacity-60" style={{ animationDelay: "0ms" }} />
						<span className="bg-muted-foreground h-2 w-2 animate-bounce rounded-full opacity-60" style={{ animationDelay: "150ms" }} />
						<span className="bg-muted-foreground h-2 w-2 animate-bounce rounded-full opacity-60" style={{ animationDelay: "300ms" }} />
					</div>
				) : editMode ? (
					<Textarea
						autoFocus
						value={content}
						className="text-muted-foreground dark:bg-transparent min-h-[20px] resize-none rounded-none border-0 bg-transparent p-0 text-sm shadow-none focus-visible:ring-0 focus-visible:ring-offset-0"
						disabled={disabled}
						onChange={(e) => {
							const clone = message.clone();
							clone.content = e.target.value;
							onChange(clone.serialized);
						}}
						onBlur={() => {
							if (content.trim().length > 0) setEditMode(false);
						}}
					/>
				) : isEmpty ? (
					<div className="text-muted-foreground min-h-[20px] text-sm italic">Enter assistant message...</div>
				) : (
					<Markdown content={content} isStreaming={isStreaming} className="text-muted-foreground" />
				)}
			</div>
		</div>
	);
}
