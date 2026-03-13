import { Textarea } from "@/components/ui/textarea";
import { Message, SerializedMessage, type MessageContent } from "@/lib/message";
import { FileIcon, Mic, Paperclip, PencilIcon, XIcon } from "lucide-react";
import { Markdown } from "@/components/ui/markdown";
import { useCallback, useEffect, useRef, useState } from "react";
import MessageRoleSwitcher from "./messageRoleSwitcher";
import { fileToAttachment } from "../../utils/attachment";
import { RichTextarea } from "@/components/ui/custom/richTextarea";
import { JINJA_VAR_HIGHLIGHT_PATTERNS, JINJA_VAR_REGEX } from "@/lib/message/constant";
import { AttachmentDisplay } from "./attachmentViews";

export function UserMessageView({
	message,
	disabled,
	supportsVision,
	onChange,
	onRemove,
}: {
	message: Message;
	disabled?: boolean;
	supportsVision?: boolean;
	onChange: (serialized: SerializedMessage) => void;
	onRemove?: () => void;
}) {
	const [editMode, setEditMode] = useState(false);
	const containerRef = useRef<HTMLDivElement>(null);
	const fileInputRef = useRef<HTMLInputElement>(null);
	const messageRef = useRef(message);
	messageRef.current = message;
	const pendingCursorRef = useRef<number | null>(null);
	const content = message.content;
	const isEmpty = !content;
	const messageAttachments = message.attachments;
	const canAttach = supportsVision && !disabled;
	const hasVariables = JINJA_VAR_REGEX.test(content);
	JINJA_VAR_REGEX.lastIndex = 0;

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

	const addAttachments = useCallback(
		(newAttachments: MessageContent[]) => {
			const latest = messageRef.current;
			const clone = latest.clone();
			clone.attachments = [...latest.attachments, ...newAttachments];
			onChange(clone.serialized);
		},
		[onChange],
	);

	const handleRemoveAttachment = useCallback(
		(index: number) => {
			const clone = message.clone();
			clone.attachments = message.attachments.filter((_, i) => i !== index);
			onChange(clone.serialized);
		},
		[message, onChange],
	);

	const handleFileSelect = useCallback(
		async (e: React.ChangeEvent<HTMLInputElement>) => {
			const files = e.target.files;
			if (!files) return;
			const attachments: MessageContent[] = [];
			for (const file of Array.from(files)) {
				const att = await fileToAttachment(file);
				if (att) attachments.push(att);
			}
			if (attachments.length > 0) addAttachments(attachments);
			e.target.value = "";
		},
		[addAttachments],
	);

	// Drag & drop state
	const [isDragging, setIsDragging] = useState(false);
	const dragCounterRef = useRef(0);

	const handleDragEnter = useCallback((e: React.DragEvent) => {
		e.preventDefault();
		e.stopPropagation();
		dragCounterRef.current++;
		if (e.dataTransfer.types.includes("Files")) setIsDragging(true);
	}, []);

	const handleDragLeave = useCallback((e: React.DragEvent) => {
		e.preventDefault();
		e.stopPropagation();
		dragCounterRef.current--;
		if (dragCounterRef.current === 0) setIsDragging(false);
	}, []);

	const handleDragOver = useCallback((e: React.DragEvent) => {
		e.preventDefault();
		e.stopPropagation();
	}, []);

	const handleDrop = useCallback(
		async (e: React.DragEvent) => {
			e.preventDefault();
			e.stopPropagation();
			dragCounterRef.current = 0;
			setIsDragging(false);
			const files = e.dataTransfer.files;
			if (!files || files.length === 0) return;
			const attachments: MessageContent[] = [];
			for (const file of Array.from(files)) {
				const att = await fileToAttachment(file);
				if (att) attachments.push(att);
			}
			if (attachments.length > 0) addAttachments(attachments);
		},
		[addAttachments],
	);

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
		<div
			className="group relative hover:border-border focus-within:border-border rounded-sm border border-transparent px-3 py-2 transition-colors"
			ref={containerRef}
			{...(canAttach
				? {
						onDragEnter: handleDragEnter,
						onDragLeave: handleDragLeave,
						onDragOver: handleDragOver,
						onDrop: handleDrop,
					}
				: {})}
		>
			{canAttach && isDragging && (
				<div className="bg-background/80 border-primary absolute inset-0 z-50 flex items-center justify-center rounded-sm border-2 border-dashed backdrop-blur-sm">
					<div className="text-primary flex flex-col items-center gap-1">
						<Paperclip className="h-5 w-5" />
						<span className="text-xs font-medium">Drop files to attach</span>
					</div>
				</div>
			)}
			<div className="mb-1 flex items-center">
				<MessageRoleSwitcher role={message.role ?? ""} disabled={disabled} onRoleChange={handleRoleChange} />
				<div className="ml-auto flex items-center gap-0.5 h-5">
					{canAttach && (
						<>
							<input
								ref={fileInputRef}
								type="file"
								multiple
								accept="image/*,audio/*,.pdf,.txt,.csv,.json,.xml,.doc,.docx"
								className="hidden"
								onChange={handleFileSelect}
							/>
							<button type="button" aria-label="Attach file" data-testid="user-msg-attach" onClick={() => fileInputRef.current?.click()} className="rounded-sm p-1 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100 hover:bg-muted focus:bg-muted focus:opacity-100">
								<Paperclip className="text-muted-foreground hover:text-foreground h-3.5 w-3.5 shrink-0 cursor-pointer" />
							</button>
						</>
					)}
					{!disabled && (
						<button type="button" aria-label="Edit message" data-testid="user-msg-edit" onClick={() => setEditMode(true)} className="rounded-sm p-1 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100 hover:bg-muted focus:bg-muted focus:opacity-100">
							<PencilIcon className="text-muted-foreground hover:text-foreground h-3.5 w-3.5 shrink-0 cursor-pointer" />
						</button>
					)}
					{!disabled && onRemove && (
						<button type="button" aria-label="Delete message" data-testid="user-msg-delete" onClick={onRemove} className="rounded-sm p-1 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100 hover:bg-muted focus:bg-muted focus:opacity-100">
							<XIcon className="text-muted-foreground hover:text-foreground h-4 w-4 shrink-0 cursor-pointer" />
						</button>
					)}
				</div>
			</div>

			<div>
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
				) : isEmpty && messageAttachments.length === 0 ? (
					<div className="text-muted-foreground min-h-[20px] text-sm italic">Enter user message...</div>
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

			{messageAttachments.length > 0 && (
				<AttachmentDisplay attachments={messageAttachments} editable={canAttach} onRemoveAttachment={handleRemoveAttachment} />
			)}
		</div>
	);
}
