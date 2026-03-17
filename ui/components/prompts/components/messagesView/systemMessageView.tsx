import { Textarea } from "@/components/ui/textarea";
import { Message, SerializedMessage } from "@/lib/message";
import { PencilIcon, XIcon } from "lucide-react";
import { Markdown } from "@/components/ui/markdown";
import { useEffect, useMemo, useRef, useState } from "react";
import { isJson } from "@/lib/utils/validation";
import { CodeEditor } from "@/components/ui/codeEditor";
import MessageRoleSwitcher from "./messageRoleSwitcher";
import { RichTextarea } from "@/components/ui/custom/richTextarea";
import { JINJA_VAR_HIGHLIGHT_PATTERNS, JINJA_VAR_REGEX } from "@/lib/message/constant";

export function SystemMessageView({
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
	const pendingCursorRef = useRef<number | null>(null);
	const content = message.content;
	const isEmpty = !content;
	const hasVariables = JINJA_VAR_REGEX.test(content);
	JINJA_VAR_REGEX.lastIndex = 0;
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

	const handleReadOnlyClick = (e: React.MouseEvent<HTMLTextAreaElement>) => {
		if (disabled) return;
		const target = e.target as HTMLTextAreaElement;
		pendingCursorRef.current = target.selectionStart ?? 0;
		setEditMode(true);
	};

	const handleEditFocus = (e: React.FocusEvent<HTMLTextAreaElement>) => {
		const pos = pendingCursorRef.current;
		pendingCursorRef.current = null;
		const target = e.target;
		requestAnimationFrame(() => {
			const cursorPos = pos ?? target.value.length;
			target.selectionStart = cursorPos;
			target.selectionEnd = cursorPos;
		});
	};

	return (
		<div className="group hover:border-border focus-within:border-border rounded-sm border border-transparent px-3 py-2 transition-colors" ref={containerRef}>
			<div className="mb-1 flex items-center">
				<MessageRoleSwitcher role={message.role ?? ""} disabled={disabled} onRoleChange={handleRoleChange} />
				<div className="ml-auto flex items-center gap-0.5 h-5">
					{!disabled && (
						<button type="button" aria-label="Edit message" data-testid="system-msg-edit" onClick={() => setEditMode(true)} className="rounded-sm p-1 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100 hover:bg-muted focus:bg-muted focus:opacity-100">
							<PencilIcon className="text-muted-foreground hover:text-foreground h-3 w-3 shrink-0 cursor-pointer" />
						</button>
					)}
					{!disabled && onRemove && (
						<button type="button" aria-label="Delete message" data-testid="system-msg-delete" onClick={onRemove} className="rounded-sm p-1 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100 hover:bg-muted focus:bg-muted focus:opacity-100">
							<XIcon className="text-muted-foreground hover:text-foreground h-3 w-3 shrink-0 cursor-pointer" />
						</button>
					)}
				</div>
			</div>

			<div onClick={() => { if (!disabled && !editMode) setEditMode(true); }} className={!disabled && !editMode ? "cursor-text" : ""}>
				{editMode ? (
					<RichTextarea
						autoFocus
						value={content}
						className="text-muted-foreground dark:bg-transparent min-h-[20px] resize-none rounded-none border-0 bg-transparent p-0 text-sm shadow-none focus-visible:ring-0 focus-visible:ring-offset-0"
						textAreaClassName="rounded-none p-0 border-none"
						disabled={disabled}
						onChange={(e) => {
							const clone = message.clone();
							clone.content = e.target.value;
							onChange(clone.serialized);
						}}
						onFocus={handleEditFocus}
						onBlur={() => {
							if (content.trim().length > 0) setEditMode(false);
						}}
						highlightPatterns={JINJA_VAR_HIGHLIGHT_PATTERNS}
					/>
				) : isEmpty ? (
					<div className="text-muted-foreground min-h-[20px] text-sm italic">Enter system message...</div>
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
				) : hasVariables ? (
					<RichTextarea
						readOnly
						value={content}
						className="text-muted-foreground dark:bg-transparent min-h-[20px] resize-none rounded-none border-0 bg-transparent p-0 text-sm shadow-none focus-visible:ring-0 focus-visible:ring-offset-0"
						textAreaClassName="rounded-none p-0 border-none cursor-text"
						onClick={handleReadOnlyClick}
						highlightPatterns={JINJA_VAR_HIGHLIGHT_PATTERNS}
					/>
				) : (
					<div
						className={!disabled ? "cursor-text" : undefined}
						onClick={() => {
							if (!disabled) setEditMode(true);
						}}
					>
						<Markdown content={content} className="text-muted-foreground" />
					</div>
				)}
			</div>
		</div>
	);
}
