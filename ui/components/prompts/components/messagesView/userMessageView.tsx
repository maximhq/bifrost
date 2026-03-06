import { Textarea } from "@/components/ui/textarea";
import { Message, SerializedMessage, type MessageContent } from "@/lib/message";
import { FileIcon, Mic, Paperclip, PencilIcon, XIcon } from "lucide-react";
import { Markdown } from "@/components/ui/markdown";
import { useCallback, useEffect, useRef, useState } from "react";
import MessageRoleSwitcher from "./messageRoleSwitcher";
import { fileToAttachment } from "../../utils/attachment";

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
	const content = message.content;
	const isEmpty = !content;
	const messageAttachments = message.attachments;
	const canAttach = supportsVision && !disabled;

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
			const clone = message.clone();
			clone.attachments = [...message.attachments, ...newAttachments];
			onChange(clone.serialized);
		},
		[message, onChange],
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

	return (
		<div
			className="group relative hover:border-border rounded-lg border border-transparent px-3 py-2 transition-colors"
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
				<div className="bg-background/80 border-primary absolute inset-0 z-50 flex items-center justify-center rounded-lg border-2 border-dashed backdrop-blur-sm">
					<div className="text-primary flex flex-col items-center gap-1">
						<Paperclip className="h-5 w-5" />
						<span className="text-xs font-medium">Drop files to attach</span>
					</div>
				</div>
			)}
			<div className="mb-1 flex items-center">
				<MessageRoleSwitcher role={message.role ?? ""} disabled={disabled} onRoleChange={handleRoleChange} />
				<div className="ml-auto flex items-center gap-2">
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
							<Paperclip
								className="text-muted-foreground hover:text-foreground h-3.5 w-3.5 shrink-0 cursor-pointer opacity-0 transition-opacity group-hover:opacity-100"
								onClick={() => fileInputRef.current?.click()}
							/>
						</>
					)}
					{!disabled && (
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
				{editMode ? (
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
				) : isEmpty && messageAttachments.length === 0 ? (
					<div className="text-muted-foreground min-h-[20px] text-sm italic">Enter user message...</div>
				) : (
					<Markdown content={content} className="text-muted-foreground" />
				)}
			</div>

			{messageAttachments.length > 0 && (
				<AttachmentDisplay attachments={messageAttachments} editable={canAttach} onRemoveAttachment={handleRemoveAttachment} />
			)}
		</div>
	);
}

function AttachmentDisplay({
	attachments,
	editable,
	onRemoveAttachment,
}: {
	attachments: MessageContent[];
	editable?: boolean;
	onRemoveAttachment?: (index: number) => void;
}) {
	if (attachments.length === 0) return null;

	return (
		<div className="mt-2 flex flex-wrap gap-2">
			{attachments.map((att, i) => {
				if (att.type === "image_url" && att.image_url?.url) {
					return (
						<div key={i} className="group/att relative">
							{/* eslint-disable-next-line @next/next/no-img-element */}
							<img src={att.image_url.url} alt="attached image" className="max-h-48 max-w-xs rounded-md border object-contain" />
							{editable && onRemoveAttachment && (
								<button
									onClick={() => onRemoveAttachment(i)}
									className="bg-background/80 text-muted-foreground hover:bg-destructive/20 hover:text-destructive absolute -top-1.5 -right-1.5 rounded-full border p-0.5 opacity-0 transition-opacity group-hover/att:opacity-100"
								>
									<XIcon className="h-3 w-3" />
								</button>
							)}
						</div>
					);
				}

				if (att.type === "input_audio") {
					const format = att.input_audio?.format || "wav";
					const dataUrl = `data:audio/${format};base64,${att.input_audio?.data || ""}`;
					return (
						<div key={i} className="group/att bg-muted/30 relative flex items-center gap-2 rounded-md border px-3 py-2">
							<Mic className="text-muted-foreground h-4 w-4" />
							<audio controls className="h-8 max-w-[200px]">
								<source src={dataUrl} type={`audio/${format}`} />
							</audio>
							{editable && onRemoveAttachment && (
								<button
									onClick={() => onRemoveAttachment(i)}
									className="bg-background/80 text-muted-foreground hover:bg-destructive/20 hover:text-destructive absolute -top-1.5 -right-1.5 rounded-full border p-0.5 opacity-0 transition-opacity group-hover/att:opacity-100"
								>
									<XIcon className="h-3 w-3" />
								</button>
							)}
						</div>
					);
				}

				if (att.type === "file") {
					return (
						<div key={i} className="group/att bg-muted/30 text-muted-foreground relative flex items-center gap-2 rounded-md border px-3 py-1.5 text-sm">
							<FileIcon className="h-4 w-4 shrink-0" />
							<span className="max-w-[200px] truncate">{att.file?.filename || "File"}</span>
							{att.file?.file_type && <span className="text-xs opacity-60">{att.file.file_type}</span>}
							{editable && onRemoveAttachment && (
								<button
									onClick={() => onRemoveAttachment(i)}
									className="bg-background/80 text-muted-foreground hover:bg-destructive/20 hover:text-destructive absolute -top-1.5 -right-1.5 rounded-full border p-0.5 opacity-0 transition-opacity group-hover/att:opacity-100"
								>
									<XIcon className="h-3 w-3" />
								</button>
							)}
						</div>
					);
				}

				return null;
			})}
		</div>
	);
}