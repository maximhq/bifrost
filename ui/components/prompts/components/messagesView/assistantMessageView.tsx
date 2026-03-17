import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { Message, SerializedMessage } from "@/lib/message";
import { InfoIcon, PencilIcon, XIcon } from "lucide-react";
import { Markdown } from "@/components/ui/markdown";
import { useEffect, useMemo, useRef, useState } from "react";
import MessageRoleSwitcher from "./messageRoleSwitcher";
import { isJson } from "@/lib/utils/validation";
import { CodeEditor } from "@/components/ui/codeEditor";

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
	const usage = message.usage;
	const jsonBufferRef = useRef<string | null>(null);
	const contentIsJson = useMemo(() => !isEmpty && !isStreaming && isJson(content), [content, isEmpty, isStreaming]);
	const formattedJson = useMemo(() => {
		if (!contentIsJson) return "";
		try {
			return JSON.stringify(JSON.parse(content), null, 2);
		} catch {
			return content;
		}
	}, [content, contentIsJson]);

	const flushJsonBuffer = () => {
		if (jsonBufferRef.current !== null) {
			const clone = message.clone();
			clone.content = jsonBufferRef.current;
			onChange(clone.serialized);
			jsonBufferRef.current = null;
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
		const clone = message.clone();
		clone.role = role as any;
		onChange(clone.serialized);
	};

	return (
		<div className="group hover:border-border focus-within:border-border rounded-sm border border-transparent px-3 py-2 transition-colors" ref={containerRef}>
			<div className="mb-1 flex items-center">
				<MessageRoleSwitcher role={message.role ?? ""} disabled={disabled} onRoleChange={handleRoleChange} />
				<div className="ml-auto flex items-center gap-0.5 h-5">
					{usage && (
						<Tooltip>
							<TooltipTrigger className="p-1 hover:bg-muted focus:bg-muted focus:opacity-100 rounded-sm">
								<InfoIcon className="text-muted-foreground hover:text-foreground h-3.5 w-3.5 shrink-0 cursor-pointer opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100 " />
							</TooltipTrigger>
							<TooltipContent side="bottom">
								<div className="flex flex-col gap-0.5 text-xs tabular-nums">
									<span><span className="w-12 inline-block">Input:</span> {usage.prompt_tokens} tokens</span>
									<span><span className="w-12 inline-block">Output:</span> {usage.completion_tokens} tokens</span>
									<span><span className="w-12 inline-block">Total:</span> {usage.total_tokens} tokens</span>
								</div>
							</TooltipContent>
						</Tooltip>
					)}
					{!disabled && !isStreaming && (
						<button type="button" aria-label="Edit message" data-testid="assistant-msg-edit" onClick={() => setEditMode(true)} className="rounded-sm p-1 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100 hover:bg-muted focus:bg-muted focus:opacity-100">
							<PencilIcon className="text-muted-foreground hover:text-foreground h-3 w-3 shrink-0 cursor-pointer" />
						</button>
					)}
					{!disabled && onRemove && (
						<button type="button" aria-label="Delete message" data-testid="assistant-msg-delete" onClick={onRemove} className="rounded-sm p-1 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100 hover:bg-muted focus:bg-muted focus:opacity-100">
							<XIcon className="text-muted-foreground hover:text-foreground h-3 w-3 shrink-0 cursor-pointer" />
						</button>
					)}
				</div>
			</div>

			<div>
				{isStreaming && isEmpty ? (
					<div className="flex items-center gap-1 py-1">
						<span className="bg-muted-foreground h-1.5 w-1.5 animate-bounce rounded-full opacity-60" style={{ animationDelay: "0ms" }} />
						<span className="bg-muted-foreground h-1.5 w-1.5 animate-bounce rounded-full opacity-60" style={{ animationDelay: "150ms" }} />
						<span className="bg-muted-foreground h-1.5 w-1.5 animate-bounce rounded-full opacity-60" style={{ animationDelay: "300ms" }} />
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
						onFocus={(e) => {
							const target = e.target;
							requestAnimationFrame(() => {
								target.selectionStart = target.value.length;
								target.selectionEnd = target.value.length;
							});
						}}
						onBlur={() => {
							if (content.trim().length > 0) setEditMode(false);
						}}
					/>
				) : isEmpty ? (
					<div className="text-muted-foreground min-h-[20px] text-sm italic">Enter assistant message...</div>
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
						onBlur={flushJsonBuffer}
						options={{
							showIndentLines: false,
							disableHover: true,
						}}
					/>
				) : (
					<div
						className={!disabled && !isStreaming ? "cursor-text" : undefined}
						onClick={() => {
							if (!disabled && !isStreaming) setEditMode(true);
						}}
					>
						<Markdown content={content} isStreaming={isStreaming} className="text-muted-foreground" />
					</div>
				)}
			</div>
		</div>
	);
}
