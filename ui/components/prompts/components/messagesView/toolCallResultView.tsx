import { Textarea } from "@/components/ui/textarea";
import { Message, MessageRole, SerializedMessage } from "@/lib/message";
import { isJson } from "@/lib/utils/validation";
import { CodeEditor } from "@/components/ui/codeEditor";
import { PencilIcon, XIcon } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import MessageRoleSwitcher from "./messageRoleSwitcher";

/**
 * Renders an editable view for a tool result message that supports role switching, inline text editing, JSON-aware editing, and removal.
 *
 * The component presents the message role selector, optional tool call id, edit/delete actions, and a content area that:
 * - shows a textarea for freeform editing when in edit mode,
 * - shows a JSON-aware code editor (with edits buffered and flushed on blur) when the content is valid JSON,
 * - shows a read-only monospaced display when content is plain text,
 * - shows a placeholder when content is empty.
 *
 * @param message - The Message instance to display and edit; updates emitted via `onChange` are serialized from clones of this message.
 * @param disabled - When true, disables interactive controls and makes editors read-only.
 * @param onChange - Called with the message's serialized form whenever the message is modified (role, content edits, or flushed JSON buffer).
 * @param onRemove - Optional callback invoked when the user requests deletion of the message.
 */
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
	const messageRef = useRef(message);
	messageRef.current = message;
	const content = message.content;
	const isEmpty = !content;
	const jsonBufferRef = useRef<string | null>(null);
	const contentIsJson = useMemo(() => !isEmpty && isJson(content), [content, isEmpty]);
	const formattedJson = useMemo(() => {
		if (!contentIsJson) return "";
		try {
			return JSON.stringify(JSON.parse(content), null, 2);
		} catch {
			return content;
		}
	}, [content, contentIsJson]);

	const applyPendingJsonBuffer = (msg: Message): Message => {
		if (jsonBufferRef.current !== null) {
			const clone = msg.clone();
			clone.content = jsonBufferRef.current;
			jsonBufferRef.current = null;
			return clone;
		}
		return msg;
	};

	const flushJsonBuffer = () => {
		const updated = applyPendingJsonBuffer(messageRef.current);
		if (updated !== messageRef.current) {
			onChange(updated.serialized);
		}
	};

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
		const latest = applyPendingJsonBuffer(messageRef.current);
		const clone = latest.clone();
		clone.role = role as any;
		onChange(clone.serialized);
	};

	return (
		<div
			className="group hover:border-border focus-within:border-border rounded-sm border border-transparent px-3 py-2 transition-colors"
			ref={containerRef}
		>
			<div className="mb-1 flex items-center">
				<MessageRoleSwitcher role={message.role ?? MessageRole.ASSISTANT} disabled={disabled} onRoleChange={handleRoleChange} />
				<div className="ml-auto flex h-5 items-center gap-0.5">
					{message.toolCallId && (
						<span className="text-muted-foreground ml-4 max-w-[200px] truncate font-mono text-xs">{message.toolCallId}</span>
					)}
					{!disabled && (
						<button
							type="button"
							aria-label="Edit message"
							data-testid="tool-result-msg-edit"
							onClick={() => setEditMode(true)}
							className="hover:bg-muted focus:bg-muted rounded-sm p-1 opacity-0 transition-opacity group-focus-within:opacity-100 group-hover:opacity-100 focus:opacity-100"
						>
							<PencilIcon className="text-muted-foreground hover:text-foreground size-3 shrink-0 cursor-pointer" />
						</button>
					)}
					{!disabled && onRemove && (
						<button
							type="button"
							aria-label="Delete message"
							data-testid="tool-result-msg-delete"
							onClick={onRemove}
							className="hover:bg-muted focus:bg-muted rounded-sm p-1 opacity-0 transition-opacity group-focus-within:opacity-100 group-hover:opacity-100 focus:opacity-100"
						>
							<XIcon className="text-muted-foreground hover:text-foreground size-3 shrink-0 cursor-pointer" />
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
				) : isEmpty ? (
					<div className="text-muted-foreground min-h-[20px] font-mono text-sm italic">Enter tool result...</div>
				) : contentIsJson ? (
					<CodeEditor
						wrap
						code={formattedJson}
						lang="json"
						readonly={disabled}
						autoResize
						onChange={(value) => {
							jsonBufferRef.current = value ?? "";
						}}
						options={{
							showIndentLines: false,
							disableHover: true,
						}}
						onBlur={flushJsonBuffer}
					/>
				) : (
					<div
						className={!disabled ? "cursor-text" : undefined}
						onClick={() => {
							if (!disabled) setEditMode(true);
						}}
					>
						<div className="text-muted-foreground min-h-[20px] font-mono text-sm whitespace-pre-wrap">{content}</div>
					</div>
				)}
			</div>
		</div>
	);
}