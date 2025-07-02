import { CodeEditor } from "./code-editor";
import { BifrostMessage } from "@/lib/types/logs";

interface LogMessageViewProps {
	message: BifrostMessage;
}

const isJson = (text: string) => {
	try {
		JSON.parse(text);
		return true;
	} catch {
		return false;
	}
};

const cleanJson = (text: any) => {
	try {
		return JSON.parse(text);
	} catch {
		return text;
	}
};

export default function LogMessageView({ message }: LogMessageViewProps) {
	return (
		<div className="w-full rounded-sm border">
			<div className="border-b px-6 py-2 text-sm font-medium capitalize">{message.role}</div>
			{typeof message.content === "string" && !isJson(message.content) ? (
				<div className="px-6 py-2 font-mono text-xs">{message.content}</div>
			) : (
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
			)}
			{message.tool_calls &&
				message.tool_calls.length > 0 &&
				message.tool_calls.map((tool_call) => (
					<div key={tool_call.id} className="border-b last:border-b-0">
						<CodeEditor
							className="z-0 w-full"
							shouldAdjustInitialHeight={true}
							maxHeight={150}
							wrap={true}
							code={JSON.stringify(tool_call.function, null, 2)}
							lang="json"
							readonly={true}
							options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
						/>
					</div>
				))}
		</div>
	);
}
