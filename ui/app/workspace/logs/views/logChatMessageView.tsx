import { ChatMessage, ContentBlock } from "@/lib/types/logs";
import { CodeEditor } from "./codeEditor";
import { isJson, cleanJson } from "@/lib/utils/validation";
import AudioPlayer from "./audioPlayer";

interface LogChatMessageViewProps {
	message: ChatMessage;
	audioFormat?: string; // Optional audio format from request params
}

const renderContentBlock = (block: ContentBlock, index: number) => {
	return (
		<div key={index} className="border-b last:border-b-0">
			<div className="bg-muted/50 text-muted-foreground px-6 py-2 text-xs font-medium capitalize">{block.type.replaceAll("_", " ")}</div>

			{/* Handle text content */}
			{block.text && (
				<>
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
						<div className="px-6 py-2 font-mono text-xs break-words whitespace-pre-wrap">{block.text}</div>
					)}
				</>
			)}

			{/* Handle image content */}
			{block.image_url && (
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
			)}

			{/* Handle audio content */}
			{block.input_audio && (
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
			)}
		</div>
	);
};

export default function LogChatMessageView({ message, audioFormat }: LogChatMessageViewProps) {
	return (
		<div className="w-full rounded-sm border">
			<div className="border-b px-6 py-2 text-sm font-medium">
				<span className={`inline-block rounded text-sm font-medium capitalize`}>{message.role}</span>
				{message.tool_call_id && <span className="text-muted-foreground ml-2 text-xs">Tool Call ID: {message.tool_call_id}</span>}
			</div>

			{/* Handle reasoning content */}
			{message.reasoning && (
				<div className="border-b last:border-b-0">
					<div className="bg-muted/50 text-muted-foreground px-6 py-2 text-xs font-medium">Reasoning</div>
					{isJson(message.reasoning) ? (
						<CodeEditor
							className="z-0 w-full"
							shouldAdjustInitialHeight={true}
							maxHeight={200}
							wrap={true}
							code={JSON.stringify(cleanJson(message.reasoning), null, 2)}
							lang="json"
							readonly={true}
							options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
						/>
					) : (
						<div className="text-muted-foreground px-6 py-2 font-mono text-xs break-words whitespace-pre-wrap italic">
							{message.reasoning}
						</div>
					)}
				</div>
			)}

			{/* Handle refusal content */}
			{message.refusal && (
				<div className="border-b last:border-b-0">
					<div className="bg-muted/50 text-muted-foreground px-6 py-2 text-xs font-medium">Refusal</div>
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
						<div className="px-6 py-2 font-mono text-xs break-words whitespace-pre-wrap text-red-800">{message.refusal}</div>
					)}
				</div>
			)}

			{/* Handle content */}
			{message.content && (
				<div className="border-b break-words last:border-b-0">
					{typeof message.content === "string" ? (
						<>
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
								<div className="px-6 py-2 font-mono text-xs break-words whitespace-pre-wrap">{message.content}</div>
							)}
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
			)}

			{/* Handle audio output */}
			{message.audio && (
				<div className="border-b last:border-b-0">
					<div className="bg-muted/50 text-muted-foreground px-6 py-2 text-xs font-medium">Audio Output</div>
					<div className="space-y-4 px-6 py-4">
						{message.audio.transcript && (
							<div className="space-y-2">
								<div className="text-muted-foreground text-xs font-medium">Transcript:</div>
								<div className="font-mono text-xs break-words whitespace-pre-wrap">{message.audio.transcript}</div>
							</div>
						)}
						{message.audio.data && (
							<div className="space-y-2">
								<div className="text-muted-foreground text-xs font-medium">Audio:</div>
								<AudioPlayer src={message.audio.data} format={audioFormat} />
							</div>
						)}
						{message.audio.id && (
							<div className="text-muted-foreground text-xs">
								ID: {message.audio.id} | Expires:{" "}
								{message.audio.expires_at && Number.isFinite(message.audio.expires_at)
									? new Date(message.audio.expires_at * 1000).toLocaleString()
									: "N/A"}
							</div>
						)}
					</div>
				</div>
			)}
		</div>
	);
}
