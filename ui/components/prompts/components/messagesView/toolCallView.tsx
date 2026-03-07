import { Textarea } from "@/components/ui/textarea";
import { Button } from "@/components/ui/button";
import { Message, SerializedMessage } from "@/lib/message";
import { Wrench, XIcon } from "lucide-react";
import { useState } from "react";
import MessageRoleSwitcher from "./messageRoleSwitcher";

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

	const handleRoleChange = (role: string) => {
		const clone = message.clone();
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
		<div className="group hover:border-border rounded-lg border border-transparent px-3 py-2 transition-colors">
			<div className="mb-1 flex items-center">
				<MessageRoleSwitcher role={message.role ?? ""} disabled={disabled} onRoleChange={handleRoleChange} />
				<div className="ml-auto">
					{!disabled && onRemove && (
						<XIcon
							className="text-muted-foreground hover:text-foreground h-4 w-4 shrink-0 cursor-pointer opacity-0 transition-opacity group-hover:opacity-100"
							onClick={onRemove}
						/>
					)}
				</div>
			</div>
			<div className="space-y-2">
				{toolCalls.map((tc) => {
					let formattedArgs = tc.function.arguments;
					try {
						formattedArgs = JSON.stringify(JSON.parse(tc.function.arguments), null, 2);
					} catch {
						// keep raw string if not valid JSON
					}
					return (
						<div key={tc.id} className="bg-muted/50 rounded-md border px-3 py-2">
							<div className="flex items-center gap-2">
								<Wrench className="text-muted-foreground h-3 w-3 shrink-0" />
								<span className="font-mono text-xs font-medium shrink-0 mr-4">{tc.function.name}</span>
								<span className="text-muted-foreground ml-auto font-mono text-[10px] truncate">{tc.id}</span>
							</div>
							{formattedArgs && (
								<pre className="text-muted-foreground mt-2 overflow-x-auto rounded bg-black/5 p-2 text-xs leading-relaxed dark:bg-white/5">{formattedArgs}</pre>
							)}
							{!disabled && onSubmitToolResult && !respondedToolCallIds?.has(tc.id) && (
								<div className="mt-2 border-t pt-2">
									<div className="text-muted-foreground mb-1 text-[10px] font-semibold uppercase tracking-wide">Response</div>
									<div className="flex items-end gap-2">
										<Textarea
											placeholder="Enter tool response..."
											value={responses[tc.id] ?? ""}
											onChange={(e) => handleResponseChange(tc.id, e.target.value)}
											className="min-h-[36px] resize-none font-mono text-xs"
											rows={2}
										/>
										<Button
											variant="secondary"
											size="sm"
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
