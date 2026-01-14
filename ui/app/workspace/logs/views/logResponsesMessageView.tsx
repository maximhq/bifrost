import { ResponsesMessage, ResponsesMessageContentBlock } from "@/lib/types/logs";
import { CodeEditor } from "./codeEditor";
import { isJson, cleanJson } from "@/lib/utils/validation";

interface LogResponsesMessageViewProps {
	messages: ResponsesMessage[];
}

const renderContentBlock = (block: ResponsesMessageContentBlock, index: number) => {
	const getBlockTitle = (type: string) => {
		switch (type) {
			case "input_text":
				return "Input Text";
			case "input_image":
				return "Input Image";
			case "input_file":
				return "Input File";
			case "input_audio":
				return "Input Audio";
			case "output_text":
				return "Output Text";
			case "reasoning_text":
				return "Reasoning Text";
			case "refusal":
				return "Refusal";
			default:
				return type.replace(/_/g, " ").replace(/\b\w/g, (l) => l.toUpperCase());
		}
	};

	return (
		<div key={index} className="border-b last:border-b-0">
			{!block.text && <div className="bg-muted/50 text-muted-foreground px-6 py-2 text-xs font-medium">{getBlockTitle(block.type)}</div>}

			{/* Handle text content */}
			{block.text && (
				<div className="px-6 py-2">
					{isJson(block.text) ? (
						<CodeEditor
							className="z-0 w-full"
							shouldAdjustInitialHeight={true}
							maxHeight={200}
							wrap={true}
							code={JSON.stringify(cleanJson(block.text), null, 2)}
							lang="json"
							readonly={true}
							options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
						/>
					) : (
						<div className="font-mono text-xs whitespace-pre-wrap">{block.text}</div>
					)}
				</div>
			)}

			{/* Handle image content */}
			{block.image_url && (
				<div className="px-6 py-2">
					<CodeEditor
						className="z-0 w-full"
						shouldAdjustInitialHeight={true}
						maxHeight={150}
						wrap={true}
						code={JSON.stringify(
							{
								image_url: block.image_url,
								...(block.detail && { detail: block.detail }),
							},
							null,
							2,
						)}
						lang="json"
						readonly={true}
						options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
					/>
				</div>
			)}

			{/* Handle file content */}
			{(block.file_id || block.file_data || block.file_url) && (
				<div className="px-6 py-2">
					<CodeEditor
						className="z-0 w-full"
						shouldAdjustInitialHeight={true}
						maxHeight={150}
						wrap={true}
						code={JSON.stringify(
							{
								...(block.filename && { filename: block.filename }),
								...(block.file_id && { file_id: block.file_id }),
								...(block.file_url && { file_url: block.file_url }),
								...(block.file_data && { file_data: "[Base64 encoded data]" }),
							},
							null,
							2,
						)}
						lang="json"
						readonly={true}
						options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
					/>
				</div>
			)}

			{/* Handle audio content */}
			{block.input_audio && (
				<div className="px-6 py-2">
					<CodeEditor
						className="z-0 w-full"
						shouldAdjustInitialHeight={true}
						maxHeight={150}
						wrap={true}
						code={JSON.stringify(block.input_audio, null, 2)}
						lang="json"
						readonly={true}
						options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
					/>
				</div>
			)}

			{/* Handle refusal content */}
			{block.refusal && (
				<div className="px-6 py-2">
					<div className="font-mono text-xs text-red-800">{block.refusal}</div>
				</div>
			)}

			{/* Handle annotations */}
			{block.annotations && block.annotations.length > 0 && (
				<div className="border-t px-6 py-2">
					<div className="text-muted-foreground mb-2 text-xs">Annotations:</div>
					<CodeEditor
						className="z-0 w-full"
						shouldAdjustInitialHeight={true}
						maxHeight={150}
						wrap={true}
						code={JSON.stringify(block.annotations, null, 2)}
						lang="json"
						readonly={true}
						options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
					/>
				</div>
			)}

			{/* Handle log probabilities */}
			{block.logprobs && block.logprobs.length > 0 && (
				<div className="border-t px-6 py-2">
					<div className="text-muted-foreground mb-2 text-xs">Log Probabilities:</div>
					<CodeEditor
						className="z-0 w-full"
						shouldAdjustInitialHeight={true}
						maxHeight={150}
						wrap={true}
						code={JSON.stringify(block.logprobs, null, 2)}
						lang="json"
						readonly={true}
						options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
					/>
				</div>
			)}
		</div>
	);
};

const renderMessage = (message: ResponsesMessage, index: number) => {
	const getMessageTitle = () => {
		if (message.type) {
			switch (message.type) {
				case "reasoning":
					return "Reasoning";
				case "message":
					return message.role ? `${message.role.charAt(0).toUpperCase() + message.role.slice(1)} Message` : "Message";
				case "function_call":
					return `Function Call: ${message.name || "Unknown"}`;
				case "function_call_output":
					return "Function Call Output";
				case "file_search_call":
					return "File Search";
				case "web_search_call":
					return "Web Search";
				case "computer_call":
					return "Computer Action";
				case "computer_call_output":
					return "Computer Action Output";
				case "code_interpreter_call":
					return "Code Interpreter";
				case "mcp_call":
					return "MCP Tool Call";
				case "custom_tool_call":
					return "Custom Tool Call";
				case "custom_tool_call_output":
					return "Custom Tool Output";
				case "image_generation_call":
					return "Image Generation";
				case "refusal":
					return "Refusal";
				default:
					return message.type.replace(/_/g, " ").replace(/\b\w/g, (l) => l.toUpperCase());
			}
		}
		return message.role ? `${message.role.charAt(0).toUpperCase() + message.role.slice(1)}` : "Message";
	};

	if (message.type == "reasoning" && (!message.summary || message.summary.length === 0) && !message.encrypted_content && !message.content) {
		return null;
	}

	return (
		<div key={`message-${index}`} className="mb-4 w-full rounded-sm border">
			<div className="border-b px-6 py-2 text-sm font-medium">
				{getMessageTitle()}
				{/* {message.status && <span className="ml-2 rounded-full bg-gray-200 px-2 py-1 text-xs">{message.status}</span>} */}
			</div>

			{/* Handle reasoning content */}
			{message.type === "reasoning" && message.summary && message.summary.length > 0 && (
				<div className="space-y-4 border-b last:border-b-0">
					{message.summary.every((item) => item.type === "summary_text") ? (
						// Display as readable text when all items are summary_text
						message.summary.map((reasoningContent, idx) => (
							<div key={idx} className="space-y-2 pt-2">
								<div className="text-muted-foreground pl-6 text-xs">Summary #{idx + 1}</div>
								<div className="px-6 pb-2">
									<div className="font-mono text-xs whitespace-pre-wrap">{reasoningContent.text}</div>
								</div>
							</div>
						))
					) : (
						// Fallback to JSON display for mixed or non-text types
						<div className="px-6 pb-2">
							<CodeEditor
								className="z-0 w-full"
								shouldAdjustInitialHeight={true}
								maxHeight={300}
								wrap={true}
								code={JSON.stringify(message.summary, null, 2)}
								lang="json"
								readonly={true}
								options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
							/>
						</div>
					)}
				</div>
			)}

			{/* Handle encrypted reasoning content */}
			{message.type === "reasoning" && message.encrypted_content && (
				<div className="border-b last:border-b-0">
					<div className="bg-muted/50 text-muted-foreground px-6 py-2 text-xs font-medium">Encrypted Reasoning Content</div>
					<div className="px-6 py-2">
						<div className="font-mono text-xs break-words whitespace-pre-wrap">{message.encrypted_content}</div>
					</div>
				</div>
			)}

			{/* Handle regular content */}
			{message.content && (
				<div className="border-b last:border-b-0">
					{typeof message.content === "string" ? (
						<>
							<div className="bg-muted/50 text-muted-foreground px-6 py-2 text-xs font-medium">Content</div>
							<div className="px-6 py-2">
								{isJson(message.content) ? (
									<CodeEditor
										className="z-0 w-full"
										shouldAdjustInitialHeight={true}
										maxHeight={250}
										wrap={true}
										code={JSON.stringify(cleanJson(message.content), null, 2)}
										lang="json"
										readonly={true}
										options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
									/>
								) : (
									<div className="font-mono text-xs break-words whitespace-pre-wrap">{message.content}</div>
								)}
							</div>
						</>
					) : (
						Array.isArray(message.content) && message.content.map((block, blockIndex) => renderContentBlock(block, blockIndex))
					)}
				</div>
			)}

			{/* Handle tool call specific fields */}
			{(message.call_id || message.name || message.arguments) && (
				<div className="border-b last:border-b-0">
					<div className="bg-muted/50 text-muted-foreground px-6 py-2 text-xs font-medium">Tool Details</div>
					<CodeEditor
						className="z-0 w-full"
						shouldAdjustInitialHeight={true}
						maxHeight={200}
						wrap={true}
						code={JSON.stringify(
							{
								...(message.call_id && { call_id: message.call_id }),
								...(message.name && { name: message.name }),
								...(message.arguments && { arguments: isJson(message.arguments) ? cleanJson(message.arguments) : message.arguments }),
							},
							null,
							2,
						)}
						lang="json"
						readonly={true}
						options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
					/>
				</div>
			)}

			{/* Handle additional tool-specific fields */}
			{Object.keys(message).some(
				(key) => !["id", "type", "status", "role", "content", "call_id", "name", "arguments", "summary", "encrypted_content"].includes(key),
			) && (
				<div className="border-b last:border-b-0">
					<div className="bg-muted/50 text-muted-foreground px-6 py-2 text-xs font-medium">Additional Fields</div>
					<CodeEditor
						className="z-0 w-full"
						shouldAdjustInitialHeight={true}
						maxHeight={200}
						wrap={true}
						code={JSON.stringify(
							Object.fromEntries(
								Object.entries(message).filter(
									([key]) =>
										!["id", "type", "status", "role", "content", "call_id", "name", "arguments", "summary", "encrypted_content"].includes(
											key,
										),
								),
							),
							null,
							2,
						)}
						lang="json"
						readonly={true}
						options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
					/>
				</div>
			)}
		</div>
	);
};

export default function LogResponsesMessageView({ messages }: LogResponsesMessageViewProps) {
	if (!messages || messages.length === 0) {
		return (
			<div className="w-full rounded-sm border">
				<div className="text-muted-foreground px-6 py-4 text-center text-sm">No responses messages available</div>
			</div>
		);
	}

	return <div className="space-y-4">{messages.map((message, index) => renderMessage(message, index))}</div>;
}
