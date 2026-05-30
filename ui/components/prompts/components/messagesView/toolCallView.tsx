import { Button } from "@/components/ui/button";
import { CodeEditor } from "@/components/ui/codeEditor";
import { Textarea } from "@/components/ui/textarea";
import { Message, MessageRole, SerializedMessage, type ToolCall } from "@/lib/message";
import { cn } from "@/lib/utils";
import { isJson } from "@/lib/utils/validation";
import { Check, ExternalLink, Loader2, PencilLine, Play, RefreshCw, Send, ShieldAlert, Wrench, XIcon } from "lucide-react";
import { useRef, useState } from "react";
import { MCPAuthRequiredError } from "../../utils/executor";
import MessageRoleSwitcher from "./messageRoleSwitcher";

export default function ToolCallMessageView({
	message,
	disabled,
	onChange,
	onRemove,
	onSubmitToolResult,
	onExecuteToolCall,
	onSubmitAllToolResults,
	onExecuteAllToolCalls,
	fetchToolResult,
	respondedToolCallIds,
}: {
	message: Message;
	disabled?: boolean;
	onChange: (serialized: SerializedMessage) => void;
	onRemove?: () => void;
	onSubmitToolResult?: (toolCallId: string, content: string) => void;
	onExecuteToolCall?: (toolCall: ToolCall) => Promise<void>;
	onSubmitAllToolResults?: (results: { toolCallId: string; content: string }[]) => Promise<void>;
	onExecuteAllToolCalls?: (toolCalls: ToolCall[]) => Promise<{ toolCallId: string; content: string }[] | undefined>;
	fetchToolResult?: (toolCall: ToolCall) => Promise<string>;
	respondedToolCallIds?: Set<string>;
}) {
	const toolCalls = message.toolCalls ?? [];
	const [responses, setResponses] = useState<Record<string, string>>({});
	const [executingIds, setExecutingIds] = useState<Set<string>>(new Set());
	const [resolvedIds, setResolvedIds] = useState<Set<string>>(new Set());
	const [manualEntryIds, setManualEntryIds] = useState<Set<string>>(new Set());
	const [authErrors, setAuthErrors] = useState<Record<string, MCPAuthRequiredError>>({});
	const [isExecutingAll, setIsExecutingAll] = useState(false);
	const [isSubmittingAll, setIsSubmittingAll] = useState(false);
	const messageRef = useRef(message);
	messageRef.current = message;
	const jsonBufferRef = useRef<Record<string, string>>({});

	const pendingToolCalls = toolCalls.filter((tc) => !respondedToolCallIds?.has(tc.id));
	const isMultiple = pendingToolCalls.length > 1;
	const isBusy = isExecutingAll || isSubmittingAll || executingIds.size > 0;

	const resolvedCount = pendingToolCalls.filter(
		(tc) => resolvedIds.has(tc.id) || (manualEntryIds.has(tc.id) && responses[tc.id]?.trim()),
	).length;
	const allResolved = pendingToolCalls.length > 0 && resolvedCount === pendingToolCalls.length;

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

	const flushAllJsonBuffers = () => {
		const keys = Object.keys(jsonBufferRef.current);
		if (keys.length === 0) return;
		const clone = messageRef.current.clone();
		let changed = false;
		for (const toolCallId of keys) {
			const tc = clone.toolCalls?.find((t) => t.id === toolCallId);
			if (tc) {
				tc.function.arguments = jsonBufferRef.current[toolCallId];
				changed = true;
			}
		}
		jsonBufferRef.current = {};
		if (changed) onChange(clone.serialized);
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

	// Single mode: submit one result and continue conversation immediately
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

	// Single mode: execute and immediately continue conversation
	const handleExecuteSingle = async (tc: ToolCall) => {
		if (!onExecuteToolCall) return;
		flushJsonBuffer(tc.id);
		const latestTc = messageRef.current.toolCalls?.find((t) => t.id === tc.id) ?? tc;
		setExecutingIds((prev) => new Set(prev).add(tc.id));
		try {
			await onExecuteToolCall(latestTc);
		} catch (err) {
			if (err instanceof MCPAuthRequiredError) {
				setAuthErrors((prev) => ({ ...prev, [tc.id]: err }));
			}
		} finally {
			setExecutingIds((prev) => {
				const next = new Set(prev);
				next.delete(tc.id);
				return next;
			});
		}
	};

	// Multi mode: execute one tool, store result locally (don't submit yet)
	const handleExecuteOne = async (tc: ToolCall) => {
		if (!fetchToolResult) return;
		flushJsonBuffer(tc.id);
		const latestTc = messageRef.current.toolCalls?.find((t) => t.id === tc.id) ?? tc;
		setExecutingIds((prev) => new Set(prev).add(tc.id));
		try {
			const content = await fetchToolResult(latestTc);
			setResponses((prev) => ({ ...prev, [tc.id]: content }));
			setResolvedIds((prev) => new Set(prev).add(tc.id));
		} catch (err) {
			if (err instanceof MCPAuthRequiredError) {
				setAuthErrors((prev) => ({ ...prev, [tc.id]: err }));
			}
		} finally {
			setExecutingIds((prev) => {
				const next = new Set(prev);
				next.delete(tc.id);
				return next;
			});
		}
	};

	// Multi mode: execute all pending tools in parallel, store results locally
	const handleExecuteAll = async () => {
		if (!onExecuteAllToolCalls) return;
		flushAllJsonBuffers();
		const latestCalls = pendingToolCalls.map(
			(tc) => messageRef.current.toolCalls?.find((t) => t.id === tc.id) ?? tc,
		);
		setAuthErrors({});
		setIsExecutingAll(true);
		try {
			const partialResults = await onExecuteAllToolCalls(latestCalls);
			if (partialResults) {
				const newResponses: Record<string, string> = {};
				const newResolved = new Set<string>();
				for (const r of partialResults) {
					newResponses[r.toolCallId] = r.content;
					newResolved.add(r.toolCallId);
				}
				setResponses((prev) => ({ ...prev, ...newResponses }));
				setResolvedIds((prev) => {
					const next = new Set(prev);
					for (const id of newResolved) next.add(id);
					return next;
				});
			}
		} finally {
			setIsExecutingAll(false);
		}
	};

	// Multi mode: submit all collected results at once
	const handleSubmitAll = async () => {
		if (!onSubmitAllToolResults) return;
		const results: { toolCallId: string; content: string }[] = [];
		for (const tc of pendingToolCalls) {
			const content = responses[tc.id]?.trim();
			if (!content) return;
			results.push({ toolCallId: tc.id, content });
		}
		setIsSubmittingAll(true);
		try {
			await onSubmitAllToolResults(results);
			setResponses({});
			setManualEntryIds(new Set());
			setResolvedIds(new Set());
		} finally {
			setIsSubmittingAll(false);
		}
	};

	const showManualEntry = (toolCallId: string) => {
		setManualEntryIds((prev) => new Set(prev).add(toolCallId));
		setResolvedIds((prev) => {
			const next = new Set(prev);
			next.delete(toolCallId);
			return next;
		});
	};

	const hideManualEntry = (toolCallId: string) => {
		setManualEntryIds((prev) => {
			const next = new Set(prev);
			next.delete(toolCallId);
			return next;
		});
		setResponses((prev) => {
			const next = { ...prev };
			delete next[toolCallId];
			return next;
		});
		setResolvedIds((prev) => {
			const next = new Set(prev);
			next.delete(toolCallId);
			return next;
		});
	};

	const tcHasResult = (tcId: string) => resolvedIds.has(tcId) || (manualEntryIds.has(tcId) && !!responses[tcId]?.trim());

	const handleRetry = (tc: ToolCall) => {
		setAuthErrors((prev) => {
			const next = { ...prev };
			delete next[tc.id];
			return next;
		});
		if (isMultiple) {
			handleExecuteOne(tc);
		} else {
			handleExecuteSingle(tc);
		}
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

					const isExecuting = executingIds.has(tc.id) || isExecutingAll;
					const isResponded = respondedToolCallIds?.has(tc.id);
					const isManualEntryOpen = manualEntryIds.has(tc.id);
					const hasResult = tcHasResult(tc.id);
					const isResolved = resolvedIds.has(tc.id);

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
							{/* Header */}
							<div className="flex items-start gap-2 bg-muted/40 px-3 py-2">
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
										{!isResponded && isMultiple && hasResult && (
											<span className="animate-in fade-in-0 zoom-in-90 duration-200 motion-reduce:animate-none shrink-0 rounded-full bg-blue-500/10 px-1.5 py-0.5 text-[9px] font-medium text-blue-600 dark:text-blue-400 flex items-center gap-0.5">
												<Check className="size-2.5" />
												Ready
											</span>
										)}
									</div>

									<div className="mt-0.5 truncate font-mono text-[10px] text-muted-foreground">
										{tc.id}
									</div>
								</div>
							</div>

							{/* Arguments */}
							{formattedArgs && (
								<div className="px-3 py-2 border-t">
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

							{/* Manual entry / executed result textarea */}
							{!disabled && (isManualEntryOpen || isResolved) && !isResponded && (
								<div className="animate-in fade-in-0 duration-150 motion-reduce:animate-none border-t bg-muted/20 px-3 py-2 space-y-2">
									<div className="flex items-center gap-2">
										<div className="text-xs font-medium text-foreground">
											{isResolved && !isManualEntryOpen ? "Executed result" : "Tool result"}
										</div>
										{isMultiple && (
											<Button
												variant="ghost"
												size="sm"
												className="ml-auto h-7 px-2 text-xs text-muted-foreground"
												onClick={() => hideManualEntry(tc.id)}
												disabled={isBusy}
											>
												Clear
											</Button>
										)}
										{!isMultiple && (
											<Button
												variant="ghost"
												size="sm"
												className="ml-auto h-7 px-2 text-xs text-muted-foreground"
												data-testid="tool-call-response-cancel"
												onClick={() => hideManualEntry(tc.id)}
												disabled={isBusy}
											>
												Cancel
											</Button>
										)}
									</div>
									<Textarea
										autoFocus={isManualEntryOpen && !isResolved}
										placeholder="Paste tool result..."
										value={responses[tc.id] ?? ""}
										onChange={(e) => handleResponseChange(tc.id, e.target.value)}
										data-testid="tool-call-response-textarea"
										className="max-h-[200px] min-h-[84px] resize-none rounded-md bg-background font-mono text-xs"
										rows={3}
										disabled={isBusy}
									/>
									{/* Single mode: inline submit */}
									{!isMultiple && (
										<div className="flex justify-end">
											<Button
												variant="secondary"
												size="sm"
												className="h-8 active:scale-[0.97] transition-transform"
												data-testid="tool-call-response-submit"
												disabled={!responses[tc.id]?.trim() || isBusy}
												onClick={() => handleSubmitResponse(tc.id)}
											>
												<Send className="size-3.5" />
												Submit result
											</Button>
										</div>
									)}
								</div>
							)}

							{/* Auth error inline banner */}
							{!disabled && authErrors[tc.id] && !isResponded && (
								<div className="animate-in fade-in-0 duration-150 motion-reduce:animate-none border-t border-amber-500/30 bg-amber-500/5 px-3 py-2.5">
									<div className="flex flex-wrap items-center gap-2">
										<div className="flex min-w-0 items-center gap-2">
											<div className="rounded-md bg-amber-500/10 p-1 shrink-0">
												<ShieldAlert className="size-3.5 text-amber-600 dark:text-amber-400" />
											</div>
											<div className="min-w-0">
												<div className="text-xs font-medium text-foreground">
													Authentication required for {authErrors[tc.id].mcpClientName}
												</div>
												<div className="text-[10px] text-muted-foreground">
													Connect your account to execute this tool.
												</div>
											</div>
										</div>
										<div className="ml-auto flex items-center gap-1.5">
											{authErrors[tc.id].authorizeUrl && (
												<Button
													size="sm"
													className="h-8 active:scale-[0.97] transition-transform bg-primary text-primary-foreground hover:bg-primary/90"
													onClick={() => window.open(authErrors[tc.id].authorizeUrl, "_blank", "noopener,noreferrer")}
												>
													<ExternalLink className="size-3.5" />
													Authenticate
												</Button>
											)}
											<Button
												variant="secondary"
												size="sm"
												className="h-8 active:scale-[0.97] transition-transform"
												onClick={() => handleRetry(tc)}
												disabled={isBusy}
											>
												<RefreshCw className="size-3.5" />
												Retry
											</Button>
										</div>
									</div>
								</div>
							)}

							{/* Per-card action bar (single mode: full actions, multi mode: per-card execute/manual) */}
							{!disabled && onSubmitToolResult && !isResponded && !hasResult && !isManualEntryOpen && !authErrors[tc.id] && (
								<div className="animate-in fade-in-0 slide-in-from-bottom-1 duration-150 motion-reduce:animate-none border-t bg-muted/20 px-3 py-2">
									<div className="flex flex-wrap items-center gap-2">
										{!isMultiple && (
											<div className="min-w-0">
												<div className="text-xs font-medium text-foreground">Awaiting tool result</div>
												<div className="text-[10px] text-muted-foreground">
													Execute the call or add the result manually.
												</div>
											</div>
										)}
										<div className={cn("flex items-center gap-1.5", !isMultiple && "ml-auto")}>
											{(isMultiple ? fetchToolResult : onExecuteToolCall) && (
												<Button
													variant="secondary"
													size="sm"
													className="h-8 active:scale-[0.97] transition-transform"
													data-testid="tool-call-execute"
													disabled={isBusy}
													onClick={() => isMultiple ? handleExecuteOne(tc) : handleExecuteSingle(tc)}
												>
													{executingIds.has(tc.id) ? (
														<Loader2 className="size-3.5 animate-spin" />
													) : (
														<Play className="size-3.5" />
													)}
													{executingIds.has(tc.id) ? "Executing" : "Execute"}
												</Button>
											)}
											<Button
												variant="ghost"
												size="sm"
												className="h-8 active:scale-[0.97] transition-transform"
												data-testid="tool-call-response-add-manually"
												disabled={isBusy}
												onClick={() => showManualEntry(tc.id)}
											>
												<PencilLine className="size-3.5" />
												Add manually
											</Button>
										</div>
									</div>
								</div>
							)}
						</div>
					);
				})}
			</div>

			{/* Multi-tool: unified bottom bar with progress and submit */}
			{!disabled && onSubmitToolResult && isMultiple && pendingToolCalls.length > 0 && (
				<div className="sticky bottom-0 py-1.5 bg-card mt-3">
					<div className="animate-in fade-in-0 slide-in-from-bottom-1 duration-150 motion-reduce:animate-none rounded-lg border bg-muted/20 px-3 py-2.5">
						<div className="flex flex-wrap items-center gap-2">
							<div className="min-w-0">
								<div className="text-xs font-medium text-foreground">
									{allResolved
										? `All ${pendingToolCalls.length} results ready`
										: `${resolvedCount} of ${pendingToolCalls.length} results collected`}
								</div>
								<div className="text-[10px] text-muted-foreground">
									{allResolved
										? "Submit all results to continue the conversation."
										: "Execute or fill each tool call above, then submit together."}
								</div>
							</div>
							<div className="ml-auto flex items-center gap-1.5">
								{onExecuteAllToolCalls && resolvedCount === 0 && (
									<Button
										size="sm"
										className="h-8 active:scale-[0.97] transition-transform bg-primary text-primary-foreground hover:bg-primary/90"
										data-testid="tool-call-execute-all"
										disabled={isBusy}
										onClick={handleExecuteAll}
									>
										{isExecutingAll ? (
											<Loader2 className="size-3.5 animate-spin" />
										) : (
											<Play className="size-3.5" />
										)}
										{isExecutingAll ? "Executing all" : "Execute all"}
									</Button>
								)}
								<Button
									size="sm"
									className={cn(
										"h-8 active:scale-[0.97] transition-all",
										allResolved
											? "bg-primary text-primary-foreground hover:bg-primary/90"
											: "bg-secondary text-secondary-foreground hover:bg-secondary/80",
									)}
									data-testid="tool-call-submit-all"
									disabled={!allResolved || isBusy || isSubmittingAll}
									onClick={handleSubmitAll}
								>
									{isSubmittingAll ? (
										<Loader2 className="size-3.5 animate-spin" />
									) : (
										<Send className="size-3.5" />
									)}
									{isSubmittingAll ? "Submitting" : "Submit all results"}
								</Button>
							</div>
						</div>
					</div>
				</div>
			)}
		</div>
	);
}
