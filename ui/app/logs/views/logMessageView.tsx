import { ChatMessage } from "@/lib/types/logs";
import { CodeEditor } from "./codeEditor";

interface LogMessageViewProps {
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

export default function LogMessageView({ message }: LogMessageViewProps) {
	return (
		<div className="w-full rounded-sm border">
			<div className="border-b px-6 py-2 text-sm font-medium capitalize">{message.role}</div>
			{message.content && typeof message.content === "string" && message.content.length > 0 && !isJson(message.content) ? (
				<div className="px-6 py-2 font-mono text-xs">{message.content}</div>
			) : (
				message.content?.length > 0 && (
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
				)
			)}
			{message.tool_calls && message.tool_calls.length > 0 && (
				<div className="border-b last:border-b-0">
					<CodeEditor
						className="z-0 w-full"
						shouldAdjustInitialHeight={true}
						maxHeight={150}
						wrap={true}
						code={JSON.stringify(cleanJson(message.tool_calls), null, 2)}
						lang="json"
						readonly={true}
						options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
					/>
				</div>
			)}
		</div>
	);
}
