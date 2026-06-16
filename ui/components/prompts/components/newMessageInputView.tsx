import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { Message, type MessageContent } from "@/lib/message";
import { Loader2, Paperclip, Play, Plus } from "lucide-react";
import { useCallback, useRef, useState } from "react";
import { usePromptContext } from "../context";
import { fileToAttachment } from "../utils/attachment";
import { AttachmentBadge } from "./messagesView/attachmentViews";
import MessageRoleSwitcher from "./messagesView/messageRoleSwitcher";

export function NewMessageInputView() {
	const {
		messages,
		setMessages: onUpdateMessages,
		handleSendMessage: onSendMessage,
		isStreaming,
		supportsVision,
		provider,
		model,
	} = usePromptContext();
	const [userInput, setUserInput] = useState("");
	const [inputRole, setInputRole] = useState<string>("user");
	const [attachments, setAttachments] = useState<MessageContent[]>([]);
	const fileInputRef = useRef<HTMLInputElement>(null);
	const userInputRef = useRef<HTMLTextAreaElement>(null);

	const handleAddMessage = useCallback(() => {
		if (isStreaming) return;
		const input = userInput.trim();
		const currentAttachments = attachments.length > 0 ? [...attachments] : undefined;
		if (!input && !currentAttachments) return;
		setUserInput("");
		setAttachments([]);
		let msg: Message;
		if (inputRole === "user") {
			msg = Message.request(input, 0, currentAttachments);
		} else if (inputRole === "system") {
			msg = Message.system(input);
		} else {
			msg = Message.response(input);
		}
		onUpdateMessages([...messages, msg]);
	}, [userInput, attachments, isStreaming, inputRole, onUpdateMessages, messages]);

	const canRun = !!(provider && model);

	const handleRun = useCallback(async () => {
		if (isStreaming || !provider || !model) return;
		const input = userInput.trim();
		const currentAttachments = attachments.length > 0 ? [...attachments] : undefined;
		if (input || currentAttachments) {
			setUserInput("");
			setAttachments([]);
		}
		let pendingMessage: Message | undefined;
		if (input || currentAttachments) {
			if (inputRole === "system") {
				pendingMessage = Message.system(input);
			} else if (inputRole === "user") {
				pendingMessage = Message.request(input, 0, currentAttachments);
			} else {
				pendingMessage = Message.response(input);
			}
		}
		await onSendMessage(pendingMessage);
		setTimeout(() => {
			userInputRef.current?.focus();
		}, 100);
	}, [userInput, attachments, isStreaming, inputRole, onSendMessage, provider, model]);

	const handleKeyDown = useCallback(
		(e: React.KeyboardEvent) => {
			if (e.key === "Enter" && !e.shiftKey) {
				e.preventDefault();
				handleRun();
			}
		},
		[handleAddMessage, handleRun],
	);

	const handleFileSelect = useCallback(async (e: React.ChangeEvent<HTMLInputElement>) => {
		const files = e.target.files;
		if (!files) return;

		for (const file of Array.from(files)) {
			const attachment = await fileToAttachment(file);
			if (attachment) {
				setAttachments((prev) => [...prev, attachment]);
			}
		}

		// Reset input so re-selecting the same file triggers onChange
		e.target.value = "";
	}, []);

	const handleRemoveAttachment = useCallback((index: number) => {
		setAttachments((prev) => prev.filter((_, i) => i !== index));
	}, []);

	const handlePaste = useCallback(
		async (e: React.ClipboardEvent) => {
			if (!supportsVision) return;
			const items = e.clipboardData?.items;
			if (!items) return;

			for (const item of Array.from(items)) {
				if (item.type.startsWith("image/")) {
					e.preventDefault();
					const file = item.getAsFile();
					if (file) {
						const attachment = await fileToAttachment(file);
						if (attachment) {
							setAttachments((prev) => [...prev, attachment]);
						}
					}
				}
			}
		},
		[supportsVision],
	);

	const [isDragging, setIsDragging] = useState(false);
	const dragCounterRef = useRef(0);

	const handleDragEnter = useCallback((e: React.DragEvent) => {
		e.preventDefault();
		e.stopPropagation();
		dragCounterRef.current++;
		if (e.dataTransfer.types.includes("Files")) {
			setIsDragging(true);
		}
	}, []);

	const handleDragLeave = useCallback((e: React.DragEvent) => {
		e.preventDefault();
		e.stopPropagation();
		dragCounterRef.current--;
		if (dragCounterRef.current === 0) {
			setIsDragging(false);
		}
	}, []);

	const handleDragOver = useCallback((e: React.DragEvent) => {
		e.preventDefault();
		e.stopPropagation();
	}, []);

	const handleDrop = useCallback(async (e: React.DragEvent) => {
		e.preventDefault();
		e.stopPropagation();
		dragCounterRef.current = 0;
		setIsDragging(false);

		const files = e.dataTransfer.files;
		if (!files || files.length === 0) return;

		for (const file of Array.from(files)) {
			const attachment = await fileToAttachment(file);
			if (attachment) {
				setAttachments((prev) => [...prev, attachment]);
			}
		}
	}, []);

	return (
		<div
			className="group relative max-h-[500px] shrink-0 overflow-y-auto border-t px-4 py-2"
			{...(supportsVision
				? {
						onDragEnter: handleDragEnter,
						onDragLeave: handleDragLeave,
						onDragOver: handleDragOver,
						onDrop: handleDrop,
					}
				: {})}
		>
			{supportsVision && isDragging && (
				<div className="bg-background/80 border-primary absolute inset-0 z-50 flex items-center justify-center rounded-sm border-2 border-dashed backdrop-blur-sm">
					<div className="text-primary flex flex-col items-center gap-1">
						<Paperclip className="h-5 w-5" />
						<span className="text-xs font-medium">Drop files to attach</span>
					</div>
				</div>
			)}
			<div className="mb-1 flex items-center">
				<MessageRoleSwitcher
					role={inputRole}
					disabled={isStreaming}
					onRoleChange={(role) => {
						setInputRole(role);
						if (role !== "user") setAttachments([]);
					}}
					restrictedRoles={["system", "tool"]}
				/>
				{supportsVision && inputRole === "user" && (
					<div className="ml-auto">
						<input
							ref={fileInputRef}
							type="file"
							multiple
							accept="image/*,audio/*,.pdf,.txt,.csv,.json,.xml,.doc,.docx"
							className="hidden"
							onChange={handleFileSelect}
						/>
						<button
							type="button"
							aria-label="Attach file"
							data-testid="new-message-attach-file"
							onClick={() => fileInputRef.current?.click()}
							className="hover:bg-muted focus:bg-muted rounded-sm p-1"
						>
							<Paperclip className="text-muted-foreground hover:text-foreground h-3.5 w-3.5 shrink-0 cursor-pointer" />
						</button>
					</div>
				)}
			</div>
			{attachments.length > 0 && (
				<div className="mb-2 flex flex-wrap gap-2">
					{attachments.map((att, index) => (
						<AttachmentBadge key={index} attachment={att} onRemove={() => handleRemoveAttachment(index)} />
					))}
				</div>
			)}
			<div className="relative">
				<Textarea
					placeholder="Type a message..."
					value={userInput}
					ref={userInputRef}
					onChange={(e) => setUserInput(e.target.value)}
					onKeyDown={handleKeyDown}
					onPaste={handlePaste}
					data-testid="new-message-textarea"
					className="text-muted-foreground min-h-[60px] resize-none rounded-none border-0 bg-transparent p-0 pr-16 text-sm shadow-none focus-visible:ring-0 focus-visible:ring-offset-0 dark:bg-transparent"
					disabled={isStreaming}
				/>
				<div className="absolute right-0 bottom-0 flex items-center gap-1">
					<Button
						onClick={handleAddMessage}
						disabled={isStreaming}
						variant={"ghost"}
						data-testid="new-message-add"
						className="text-muted-foreground hover:text-foreground flex items-center gap-1 rounded px-1.5 py-1 text-xs disabled:pointer-events-none disabled:opacity-50"
					>
						<Plus className="h-3.5 w-3.5" />
						Add
					</Button>
					<Tooltip>
						<TooltipTrigger asChild>
							<Button
								onClick={handleRun}
								disabled={isStreaming || !canRun}
								variant={"ghost"}
								data-testid="new-message-run"
								className="text-muted-foreground hover:text-foreground flex items-center gap-1 rounded px-1.5 py-1 text-xs disabled:pointer-events-none disabled:opacity-50"
							>
								{isStreaming ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Play className="h-3.5 w-3.5" />}
								Run
							</Button>
						</TooltipTrigger>
						<TooltipContent side="top">
							{!canRun ? <span>Select a provider and model to run</span> : <span>Run prompt</span>}
							<kbd className="bg-primary-foreground/20 ml-1.5 rounded px-1 py-0.5 font-mono text-[10px]">↵</kbd>
						</TooltipContent>
					</Tooltip>
				</div>
			</div>
		</div>
	);
}