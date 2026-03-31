import { Message, MessageType, SerializedMessage, extractVariablesFromMessages, mergeVariables } from "@/lib/message";
import { useCallback, useEffect, useRef } from "react";
import { usePromptContext } from "../../context";
import { SystemMessageView } from "./systemMessageView";
import { UserMessageView } from "./userMessageView";
import { AssistantMessageView } from "./assistantMessageView";
import ToolResultMessageView from "./toolCallResultView";
import ToolCallMessageView from "./toolCallView";
import ErrorMessageView from "./errorMessageView";

/**
 * Render and manage the chat messages list, mapping each message to its appropriate view, handling edits, removals, variable recomputation, and automatic scrolling during streaming.
 *
 * @returns A React element that renders the messages list and provides handlers for message changes, removals, tool submissions, and variable updates.
 */
export function MessagesView() {
	const { messages, setMessages: onUpdateMessages, setVariables, isStreaming, supportsVision, handleSubmitToolResult } = usePromptContext();
	const messagesEndRef = useRef<HTMLDivElement>(null);
	const prevLengthRef = useRef(messages.length);
	const prevLastIdRef = useRef(messages[messages.length - 1]?.id);

	useEffect(() => {
		const lastId = messages[messages.length - 1]?.id;
		const grew = messages.length > prevLengthRef.current;
		const lastChanged = lastId !== prevLastIdRef.current;
		const shouldScroll = grew || (lastChanged && messages.length >= prevLengthRef.current);
		prevLengthRef.current = messages.length;
		prevLastIdRef.current = lastId;
		if (shouldScroll) {
			messagesEndRef.current?.scrollIntoView({ behavior: "smooth" });
		}
	}, [messages, isStreaming]);

	const recomputeVariables = useCallback(
		(msgs: Message[]) => {
			const varNames = extractVariablesFromMessages(msgs);
			setVariables((prev) => mergeVariables(prev, varNames));
		},
		[setVariables],
	);

	const handleMessageChange = useCallback(
		(index: number, serialized: SerializedMessage) => {
			const newMessages = [...messages];
			newMessages[index] = Message.deserialize(serialized);
			onUpdateMessages(newMessages);
			recomputeVariables(newMessages);
		},
		[messages, onUpdateMessages, recomputeVariables],
	);

	const handleRemoveMessage = useCallback(
		(index: number) => {
			const newMessages = messages.filter((_, i) => i !== index);
			const result = newMessages.length > 0 ? newMessages : [Message.system("")];
			onUpdateMessages(result);
			recomputeVariables(result);
		},
		[messages, onUpdateMessages, recomputeVariables],
	);

	const lastMessage = messages[messages.length - 1];
	const isLastMessageStreaming = isStreaming && lastMessage?.type === MessageType.CompletionResult;

	return (
		<div className="space-y-1 px-1 py-4">
			{messages.map((msg, index) => {
				const isStreamingMsg = isLastMessageStreaming && index === messages.length - 1;
				const canRemove = index > 0;

				switch (msg.type) {
					case MessageType.CompletionError:
						return (
							<ErrorMessageView
								key={msg.id}
								message={msg}
								disabled={isStreaming}
								onRemove={canRemove ? () => handleRemoveMessage(index) : undefined}
							/>
						);
					case MessageType.ToolResult:
						return (
							<ToolResultMessageView
								key={msg.id}
								message={msg}
								disabled={isStreaming}
								onChange={(s) => handleMessageChange(index, s)}
								onRemove={canRemove ? () => handleRemoveMessage(index) : undefined}
							/>
						);
					case MessageType.CompletionResult:
						if (msg.toolCalls) {
							const respondedIds = new Set<string>();
							for (let i = index + 1; i < messages.length; i++) {
								const m = messages[i];
								if (m.type === MessageType.ToolResult && m.toolCallId) {
									respondedIds.add(m.toolCallId);
								} else {
									break;
								}
							}
							return (
								<ToolCallMessageView
									key={msg.id}
									message={msg}
									disabled={isStreaming}
									onChange={(s) => handleMessageChange(index, s)}
									onRemove={canRemove ? () => handleRemoveMessage(index) : undefined}
									onSubmitToolResult={(toolCallId, content) => handleSubmitToolResult(index, toolCallId, content)}
									respondedToolCallIds={respondedIds}
								/>
							);
						}
						return (
							<AssistantMessageView
								key={msg.id}
								message={msg}
								disabled={isStreaming}
								isStreaming={isStreamingMsg}
								onChange={(s) => handleMessageChange(index, s)}
								onRemove={canRemove ? () => handleRemoveMessage(index) : undefined}
							/>
						);
					default: {
						const role = msg.role;
						if (role === "system") {
							return (
								<SystemMessageView
									key={msg.id}
									message={msg}
									disabled={isStreaming}
									onChange={(s) => handleMessageChange(index, s)}
									onRemove={canRemove ? () => handleRemoveMessage(index) : undefined}
								/>
							);
						}
						if (role === "user") {
							return (
								<UserMessageView
									key={msg.id}
									message={msg}
									disabled={isStreaming}
									supportsVision={supportsVision}
									onChange={(s) => handleMessageChange(index, s)}
									onRemove={canRemove ? () => handleRemoveMessage(index) : undefined}
								/>
							);
						}
						return (
							<AssistantMessageView
								key={msg.id}
								message={msg}
								disabled={isStreaming}
								isStreaming={isStreamingMsg}
								onChange={(s) => handleMessageChange(index, s)}
								onRemove={canRemove ? () => handleRemoveMessage(index) : undefined}
							/>
						);
					}
				}
			})}
			<div ref={messagesEndRef} />
		</div>
	);
}
