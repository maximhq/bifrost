import { Button } from "@/components/ui/button";
import { CodeEditor } from "@/components/ui/codeEditor";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";
import { Message, MessageRole, SerializedMessage, type ToolCall } from "@/lib/message";
import { isJson } from "@/lib/utils/validation";
import { Loader2, PencilLine, Play, Send, Wrench, XIcon } from "lucide-react";
import { useRef, useState } from "react";
import MessageRoleSwitcher from "./messageRoleSwitcher";

/**
 * Renders a UI for viewing and editing tool-call entries on a message, including optional argument editing and submitting tool responses.
 *
 * The component displays each tool call's name, id, and arguments (JSON arguments open in an editable code editor). JSON edits are buffered locally and only committed to `onChange` when the editor loses focus or when the message role changes. The component also exposes controls for switching the message role, deleting the message, and entering/submitting a response for individual tool calls.
 *
 * @param message - Message instance containing zero or more toolCalls to render; edits are serialized via `onChange`.
 * @param disabled - When true, disables interactive controls and makes editors read-only.
 * @param onChange - Called with the message's serialized form after committed edits (e.g., buffered JSON arguments flushed or role changed).
 * @param onRemove - If provided, called when the delete button is clicked.
 * @param onSubmitToolResult - If provided, called with (toolCallId, content) when a user submits a response for a tool call.
 * @param respondedToolCallIds - Optional set of toolCall ids that have already received responses; tool calls in this set hide the response UI.
 *
 * @returns The rendered React element for the tool-call message view.
 */
export default function ToolCallMessageView({
	message,
	disabled,
	onChange,
	onRemove,
	onSubmitToolResult,
	onExecuteToolCall,
	respondedToolCallIds,
}: {
	message: Message;
	disabled?: boolean;
	onChange: (serialized: SerializedMessage) => void;
	onRemove?: () => void;
	onSubmitToolResult?: (toolCallId: string, content: string) => void;
	onExecuteToolCall?: (toolCall: ToolCall) => Promise<void>;
	respondedToolCallIds?: Set<string>;
}) {
	const toolCalls = message.toolCalls ?? [];
	const [responses, setResponses] = useState<Record<string, string>>({});
	const [executingIds, setExecutingIds] = useState<Set<string>>(new Set());
	const [manualEntryIds, setManualEntryIds] = useState<Set<string>>(new Set());
	const messageRef = useRef(message);
	messageRef.current = message;
	const jsonBufferRef = useRef<Record<string, string>>({});

	const applyPendingJsonBuffers = (msg: Message): Message => {
		const keys = Object.keys(jsonBufferRef.current);
		if (keys.length === 0) return msg;
		const clone = msg.clone();
		for (const toolCallId of keys) {
			const tc = clone.toolCalls?.find((t) => t.id === toolCallId);
			if (tc) {
				tc.function.arguments = jsonBufferRef.current[toolCallId];
			}
		}
		jsonBufferRef.current = {};
		return clone;
	};

	const flushJsonBuffer = (toolCallId: string) => {
		if (jsonBufferRef.current[toolCallId] !== undefined) {
			const clone = messageRef.current.clone();
			const tc = clone.toolCalls?.find((t) => t.id === toolCallId);
			if (tc) {
				tc.function.arguments = jsonBufferRef.current[toolCallId];
				onChange(clone.serialized);
			}
			delete jsonBufferRef.current[toolCallId];
		}
	};

	const handleRoleChange = (role: string) => {
		const latest = applyPendingJsonBuffers(messageRef.current);
		const clone = latest.clone();
		clone.role = role as MessageRole;
		onChange(clone.serialized);
	};

	const handleResponseChange = (toolCallId: string, value: string) => {
		setResponses((prev) => ({ ...prev, [toolCallId]: value }));
	};

	const handleSubmitResponse = (toolCallId: string) => {
		const content = responses[toolCallId]?.trim();
		if (!content || !onSubmitToolResult) return;
		onSubmitToolResult(toolCallId, content);
		setResponses((prev) => {
			const next = { ...prev };
			delete next[toolCallId];
			return next;
		});
		setManualEntryIds((prev) => {
			const next = new Set(prev);
			next.delete(toolCallId);
			return next;
		});
	};

	const handleExecute = async (tc: ToolCall) => {
		if (!onExecuteToolCall) return;
		flushJsonBuffer(tc.id);
		const latestTc = messageRef.current.toolCalls?.find((t) => t.id === tc.id) ?? tc;
		setExecutingIds((prev) => new Set(prev).add(tc.id));
		try {
			await onExecuteToolCall(latestTc);
		} finally {
			setExecutingIds((prev) => {
				const next = new Set(prev);
				next.delete(tc.id);
				return next;
			});
		}
	};

	const showManualEntry = (toolCallId: string) => {
		setManualEntryIds((prev) => new Set(prev).add(toolCallId));
	};

	const hideManualEntry = (toolCallId: string) => {
		setManualEntryIds((prev) => {
			const next = new Set(prev);
			next.delete(toolCallId);
			return next;
		});
	};

	return (
		<div className="group rounded-lg border border-transparent px-3 py-2 transition-colors hover:border-border/80 focus-within:border-border/80">
			<div className="mb-2 flex items-center gap-1">
				<MessageRoleSwitcher role={message.role ?? ""} disabled={disabled} onRoleChange={handleRoleChange} />

				{toolCalls.length > 0 && (
					<span className="animate-in fade-in-0 zoom-in-95 duration-200 rounded-full bg-muted px-2 py-0.5 text-[10px] font-medium text-muted-foreground motion-reduce:animate-none">
						{toolCalls.length} tool call{toolCalls.length > 1 ? "s" : ""}
					</span>
				)}

				<div className="ml-auto h-6">
					{!disabled && onRemove && (
						<button
							type="button"
							aria-label="Delete message"
							data-testid="tool-call-msg-delete"
							onClick={onRemove}
							className="rounded-md p-1 opacity-0 transition hover:bg-destructive/10 focus:bg-destructive/10 focus:opacity-100 group-focus-within:opacity-100 group-hover:opacity-100"
						>
							<XIcon className="size-3.5 shrink-0 cursor-pointer text-muted-foreground transition-colors hover:text-destructive" />
						</button>
					)}
				</div>
			</div>

			<div className="space-y-2.5">
				{toolCalls.map((tc, i) => {
					const argsIsJson = isJson(tc.function.arguments);
					let formattedArgs = tc.function.arguments;

					if (argsIsJson) {
						try {
							formattedArgs = JSON.stringify(JSON.parse(tc.function.arguments), null, 2);
						} catch {
							// keep raw string
						}
					}

					const isExecuting = executingIds.has(tc.id);
					const isResponded = respondedToolCallIds?.has(tc.id);
					const isManualEntryOpen = manualEntryIds.has(tc.id);

					return (
						<div
							key={tc.id}
							className={cn(
								"animate-in fade-in-0 slide-in-from-bottom-1 duration-200 fill-mode-both motion-reduce:animate-none overflow-hidden rounded-lg border bg-card transition-[border-color,box-shadow]",
								isExecuting
									? "border-primary/40 shadow-[0_0_0_1px_var(--color-primary)/0.1]"
									: "hover:border-border",
							)}
							style={{ animationDelay: `${i * 75}ms` }}
						>
							<div className="flex items-start gap-2 border-b bg-muted/40 px-3 py-2">
								<div className="min-w-0 flex-1 items-center flex justify-between">
									<div className="flex min-w-0 items-center gap-2">
										<div className={cn(
											"rounded-md bg-background p-1 shrink-0 transition-colors duration-150",
											isExecuting && "bg-primary/10",
										)}>
											<Wrench className={cn(
												"size-3.5 shrink-0 transition-colors duration-150",
												isExecuting ? "text-primary" : "text-muted-foreground",
											)} />
										</div>
										<span className="truncate font-mono text-xs font-semibold text-foreground">
											{tc.function.name}
										</span>

										{isResponded && (
											<span className="animate-in fade-in-0 zoom-in-90 duration-200 motion-reduce:animate-none shrink-0 rounded-full bg-emerald-500/10 px-1.5 py-0.5 text-[9px] font-medium text-emerald-600 dark:text-emerald-400">
												Responded
											</span>
										)}
									</div>

									<div className="mt-0.5 truncate font-mono text-[10px] text-muted-foreground">
										{tc.id}
									</div>
								</div>
							</div>

							{formattedArgs && (
								<div className="px-3 py-2">
									<div className="mb-1.5 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
										Arguments
									</div>

									{argsIsJson ? (
										<div className="overflow-hidden rounded-md border bg-background">
											<CodeEditor
												wrap
												code={formattedArgs}
												lang="json"
												readonly={disabled}
												autoResize
												maxHeight={400}
												onChange={(value) => {
													jsonBufferRef.current[tc.id] = value ?? "";
												}}
												options={{
													showIndentLines: false,
													disableHover: true,
												}}
												onBlur={() => flushJsonBuffer(tc.id)}
											/>
										</div>
									) : (
										<pre className="max-h-56 overflow-auto rounded-md border bg-muted/40 p-2 font-mono text-xs leading-relaxed text-muted-foreground">
											{formattedArgs}
										</pre>
									)}
								</div>
							)}

							{!disabled && onSubmitToolResult && !isResponded && (
								<div className="animate-in fade-in-0 slide-in-from-bottom-1 duration-150 motion-reduce:animate-none border-t bg-muted/20 px-3 py-2">
									{isManualEntryOpen ? (
										<div className="animate-in fade-in-0 duration-150 motion-reduce:animate-none space-y-2">
											<div className="flex items-center gap-2">
												<div>
													<div className="text-xs font-medium text-foreground">Tool result</div>
													<div className="text-[10px] text-muted-foreground">
														Paste the result returned by this tool call.
													</div>
												</div>

												<Button
													variant="ghost"
													size="sm"
													className="ml-auto h-7 px-2 text-xs text-muted-foreground"
													data-testid="tool-call-response-cancel"
													onClick={() => hideManualEntry(tc.id)}
													disabled={isExecuting}
												>
													Cancel
												</Button>
											</div>

											<Textarea
												autoFocus
												placeholder="Paste tool result..."
												value={responses[tc.id] ?? ""}
												onChange={(e) => handleResponseChange(tc.id, e.target.value)}
												data-testid="tool-call-response-textarea"
												className="min-h-[84px] resize-none rounded-md bg-background font-mono text-xs"
												rows={4}
												disabled={isExecuting}
											/>

											<div className="flex justify-end">
												<Button
													variant="secondary"
													size="sm"
													className="h-8 active:scale-[0.97] transition-transform"
													data-testid="tool-call-response-submit"
													disabled={!responses[tc.id]?.trim() || isExecuting}
													onClick={() => handleSubmitResponse(tc.id)}
												>
													<Send className="size-3.5" />
													Submit result
												</Button>
											</div>
										</div>
									) : (
										<div className="flex flex-wrap items-center gap-2">
											<div className="min-w-0">
												<div className="text-xs font-medium text-foreground">Awaiting tool result</div>
												<div className="text-[10px] text-muted-foreground">
													Execute the call or add the result manually.
												</div>
											</div>

											<div className="ml-auto flex items-center gap-1.5">
												{onExecuteToolCall && (
													<Button
														variant="secondary"
														size="sm"
														className="h-8 active:scale-[0.97] transition-transform"
														data-testid="tool-call-execute"
														disabled={isExecuting}
														onClick={() => handleExecute(tc)}
													>
														{isExecuting ? (
															<Loader2 className="size-3.5 animate-spin" />
														) : (
															<Play className="size-3.5" />
														)}
														{isExecuting ? "Executing" : "Execute"}
													</Button>
												)}

												<Button
													variant="ghost"
													size="sm"
													className="h-8 active:scale-[0.97] transition-transform"
													data-testid="tool-call-response-add-manually"
													disabled={isExecuting}
													onClick={() => showManualEntry(tc.id)}
												>
													<PencilLine className="size-3.5" />
													Add manually
												</Button>
											</div>
										</div>
									)}
								</div>
							)}
						</div>
					);
				})}
			</div>
		</div>
	);
}