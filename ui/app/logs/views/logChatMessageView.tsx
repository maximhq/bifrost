import { ChatMessage, ContentBlock } from "@/lib/types/logs";
import { CodeEditor } from "./codeEditor";

interface LogChatMessageViewProps {
	message: ChatMessage;
}

const isJson = (text: string) => {
	try {
		JSON.parse(text);
		return true;
	} catch {
		return false;
	}
};

const cleanJson = (text: unknown) => {
	try {
		if (typeof text === "string") return JSON.parse(text); // parse JSON strings
		if (Array.isArray(text)) return text; // keep arrays as-is
		if (text !== null && typeof text === "object") return text; // keep objects as-is
		if (typeof text === "number" || typeof text === "boolean") return text;
		return "Invalid payload";
	} catch {
		return text;
	}
};

const renderContentBlock = (block: ContentBlock, index: number) => {
	const getBlockTitle = (type: string) => {
		switch (type) {
			case "text":
				return "Text";
			case "image_url":
				return "Image";
			case "input_audio":
				return "Audio Input";
			case "input_text":
				return "Text Input";
			case "input_file":
				return "File Input";
			case "output_text":
				return "Text Output";
			case "refusal":
				return "Refusal";
			case "reasoning":
				return "Reasoning";
			default:
				return type.replace(/_/g, " ").replace(/\b\w/g, (l) => l.toUpperCase());
		}
	};

	return (
		<div key={index} className="border-b last:border-b-0">
			<div className="bg-muted/50 text-muted-foreground px-6 py-2 text-xs font-medium">{getBlockTitle(block.type)}</div>

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
						code={JSON.stringify(block.image_url, null, 2)}
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
		</div>
	);
};

const getRoleTitle = (role: string) => {
	switch (role) {
		case "assistant":
			return "Assistant";
		case "user":
			return "User";
		case "system":
			return "System";
		case "chatbot":
			return "Chatbot";
		case "tool":
			return "Tool";
		default:
			return role.charAt(0).toUpperCase() + role.slice(1);
	}
};

const getRoleColor = (role: string) => {
	switch (role) {
		case "assistant":
			return "bg-blue-100 text-blue-800";
		case "user":
			return "bg-green-100 text-green-800";
		case "system":
			return "bg-purple-100 text-purple-800";
		case "chatbot":
			return "bg-cyan-100 text-cyan-800";
		case "tool":
			return "bg-orange-100 text-orange-800";
		default:
			return "bg-muted text-muted-foreground";
	}
};

export default function LogChatMessageView({ message }: LogChatMessageViewProps) {
	return (
		<div className="w-full rounded-sm border">
			<div className="border-b px-6 py-2 text-sm font-medium">
				<span className={`inline-block rounded text-sm font-medium`}>{getRoleTitle(message.role)}</span>
				{message.tool_call_id && <span className="text-muted-foreground ml-2 text-xs">Tool Call ID: {message.tool_call_id}</span>}
			</div>

			{/* Handle thought content */}
			{message.thought && (
				<div className="border-b last:border-b-0">
					<div className="bg-muted/50 text-muted-foreground px-6 py-2 text-xs font-medium">Thought Process</div>
					<div className="px-6 py-2">
						{isJson(message.thought) ? (
							<CodeEditor
								className="z-0 w-full"
								shouldAdjustInitialHeight={true}
								maxHeight={200}
								wrap={true}
								code={JSON.stringify(cleanJson(message.thought), null, 2)}
								lang="json"
								readonly={true}
								options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
							/>
						) : (
							<div className="text-muted-foreground font-mono text-xs whitespace-pre-wrap italic">{message.thought}</div>
						)}
					</div>
				</div>
			)}

			{/* Handle refusal content */}
			{message.refusal && (
				<div className="border-b last:border-b-0">
					<div className="bg-muted/50 text-muted-foreground px-6 py-2 text-xs font-medium">Refusal</div>
					<div className="px-6 py-2">
						{isJson(message.refusal) ? (
							<CodeEditor
								className="z-0 w-full"
								shouldAdjustInitialHeight={true}
								maxHeight={150}
								wrap={true}
								code={JSON.stringify(cleanJson(message.refusal), null, 2)}
								lang="json"
								readonly={true}
								options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
							/>
						) : (
							<div className="font-mono text-xs text-red-800">{message.refusal}</div>
						)}
					</div>
				</div>
			)}

			{/* Handle content */}
			{message.content && (
				<div className="border-b last:border-b-0">
					{typeof message.content === "string" ? (
						<>
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
									<div className="font-mono text-xs whitespace-pre-wrap">{message.content}</div>
								)}
							</div>
						</>
					) : (
						Array.isArray(message.content) && message.content.map((block, blockIndex) => renderContentBlock(block, blockIndex))
					)}
				</div>
			)}

			{/* Handle tool calls */}
			{message.tool_calls && message.tool_calls.length > 0 && (
				<div className="space-y-4 border-b last:border-b-0">
					<div className="bg-muted/50 text-muted-foreground px-6 py-2 text-xs font-medium">Tool Calls</div>
					{message.tool_calls.map((toolCall, index) => (
						<div key={index} className="space-y-2 rounded">
							<div className="text-muted-foreground pl-6 text-xs">Tool Call #{index + 1}</div>
							<CodeEditor
								className="z-0 w-full"
								shouldAdjustInitialHeight={true}
								maxHeight={200}
								wrap={true}
								code={JSON.stringify(toolCall, null, 2)}
								lang="json"
								readonly={true}
								options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
							/>
						</div>
					))}
				</div>
			)}

			{/* Handle annotations */}
			{message.annotations && message.annotations.length > 0 && (
				<div className="border-b last:border-b-0">
					<div className="bg-muted/50 text-muted-foreground px-6 py-2 text-xs font-medium">Annotations</div>
					<div className="px-6 py-2">
						<CodeEditor
							className="z-0 w-full"
							shouldAdjustInitialHeight={true}
							maxHeight={150}
							wrap={true}
							code={JSON.stringify(message.annotations, null, 2)}
							lang="json"
							readonly={true}
							options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
						/>
					</div>
				</div>
			)}
		</div>
	);
}
