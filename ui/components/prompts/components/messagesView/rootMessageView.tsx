import { Message, MessageType, SerializedMessage } from "@/lib/message";
import { useCallback, useEffect, useRef } from "react";
import { usePromptContext } from "../../context";
import { SystemMessageView } from "./systemMessageView";
import { UserMessageView } from "./userMessageView";
import { AssistantMessageView } from "./assistantMessageView";
import ToolResultMessageView from "./toolCallResultView";
import ToolCallMessageView from "./toolCallView";
import ErrorMessageView from "./errorMessageView";

export function MessagesView() {
	const { messages, setMessages: onUpdateMessages, isStreaming, supportsVision } = usePromptContext();
	const messagesEndRef = useRef<HTMLDivElement>(null);

	useEffect(() => {
		messagesEndRef.current?.scrollIntoView({ behavior: "smooth" });
	}, [messages, isStreaming]);

	const handleMessageChange = useCallback(
		(index: number, serialized: SerializedMessage) => {
			const newMessages = [...messages];
			newMessages[index] = Message.deserialize(serialized);
			onUpdateMessages(newMessages);
		},
		[messages, onUpdateMessages],
	);

	const handleRemoveMessage = useCallback(
		(index: number) => {
			const newMessages = messages.filter((_, i) => i !== index);
			onUpdateMessages(newMessages.length > 0 ? newMessages : [Message.system("")]);
		},
		[messages, onUpdateMessages],
	);

	const lastMessage = messages[messages.length - 1];
	const isLastMessageStreaming = isStreaming && lastMessage?.type === MessageType.CompletionResult;

	return (
		<div className="space-y-1 p-4">
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
							return (
								<ToolCallMessageView
									key={msg.id}
									message={msg}
									disabled={isStreaming}
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
