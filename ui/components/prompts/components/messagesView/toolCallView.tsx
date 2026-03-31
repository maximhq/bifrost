import { Textarea } from "@/components/ui/textarea";
import { Button } from "@/components/ui/button";
import { Message, SerializedMessage } from "@/lib/message";
import { isJson } from "@/lib/utils/validation";
import { CodeEditor } from "@/components/ui/codeEditor";
import { Wrench, XIcon } from "lucide-react";
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
	respondedToolCallIds,
}: {
	message: Message;
	disabled?: boolean;
	onChange: (serialized: SerializedMessage) => void;
	onRemove?: () => void;
	onSubmitToolResult?: (toolCallId: string, content: string) => void;
	respondedToolCallIds?: Set<string>;
}) {
	const toolCalls = message.toolCalls ?? [];
	const [responses, setResponses] = useState<Record<string, string>>({});
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
		clone.role = role as any;
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
	};

	return (
		<div className="group hover:border-border focus-within:border-border rounded-sm border border-transparent px-3 py-2 transition-colors">
			<div className="mb-1 flex items-center">
				<MessageRoleSwitcher role={message.role ?? ""} disabled={disabled} onRoleChange={handleRoleChange} />
				<div className="ml-auto h-5">
					{!disabled && onRemove && (
							<button type="button" aria-label="Delete message" data-testid="tool-call-msg-delete" onClick={onRemove} className="rounded-sm p-1 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100 hover:bg-muted focus:bg-muted focus:opacity-100">
							<XIcon className="text-muted-foreground hover:text-foreground size-3 shrink-0 cursor-pointer" />
						</button>
					)}
				</div>
			</div>
			<div className="space-y-2">
				{toolCalls.map((tc) => {
					const argsIsJson = isJson(tc.function.arguments);
					let formattedArgs = tc.function.arguments;
					if (argsIsJson) {
						try {
							formattedArgs = JSON.stringify(JSON.parse(tc.function.arguments), null, 2);
						} catch {
							// keep raw string
						}
					}
					return (
						<div key={tc.id} className="bg-muted/50 rounded-sm border px-3 py-2 mt-2">
							<div className="flex items-center gap-2">
								<Wrench className="text-muted-foreground size-3 shrink-0" />
								<span className="font-mono text-xs font-medium shrink-0 mr-4">{tc.function.name}</span>
								<span className="text-muted-foreground ml-auto font-mono text-[10px] truncate">{tc.id}</span>
							</div>
							{formattedArgs && (
								argsIsJson ? (
									<div className="mt-2">
										<CodeEditor
											wrap
											code={formattedArgs}
											lang="json"
											readonly={disabled}
											autoResize
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
									<pre className="text-muted-foreground mt-2 overflow-x-auto rounded bg-card p-2 text-xs leading-relaxed">{formattedArgs}</pre>
								)
							)}
							{!disabled && onSubmitToolResult && !respondedToolCallIds?.has(tc.id) && (
								<div className="mt-2 border-t pt-2">
									<div className="text-muted-foreground mb-1 text-[10px] font-semibold uppercase tracking-wide">Response</div>
									<div className="flex items-end gap-2">
										<Textarea
											placeholder="Enter tool response..."
											value={responses[tc.id] ?? ""}
											onChange={(e) => handleResponseChange(tc.id, e.target.value)}
											data-testid="tool-call-response-textarea"
											className="min-h-[36px] resize-none font-mono text-xs"
											rows={2}
										/>
										<Button
											variant="secondary"
											size="sm"
											data-testid="tool-call-response-submit"
											disabled={!responses[tc.id]?.trim()}
											onClick={() => handleSubmitResponse(tc.id)}
										>
											Submit
										</Button>
									</div>
								</div>
							)}
						</div>
					);
				})}
			</div>
		</div>
	);
}
