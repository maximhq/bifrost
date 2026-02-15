import { ChatMessage, ContentBlock } from "@/lib/types/logs"
import { cleanJson, isJson } from "@/lib/utils/validation"
import AudioPlayer from "./audioPlayer"
import { CodeEditor } from "./codeEditor"
import CollapsibleBox from "./collapsibleBox"

interface LogChatMessageViewProps {
	message: ChatMessage
	audioFormat?: string // Optional audio format from request params
}

function ContentBlockView({ block, index }: { block: ContentBlock; index: number }) {
	const blockType = block.type.replaceAll("_", " ")

	// Handle text content
	if (block.text) {
		if (isJson(block.text)) {
			const jsonContent = JSON.stringify(cleanJson(block.text), null, 2)
			return (
				<CollapsibleBox title={blockType} onCopy={() => jsonContent} collapsedHeight={100}>
					<CodeEditor
						className="z-0 w-full"
						shouldAdjustInitialHeight={true}
						maxHeight={200}
						wrap={true}
						code={jsonContent}
						lang="json"
						readonly={true}
						options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
					/>
				</CollapsibleBox>
			)
		}
		return (
			<CollapsibleBox title={blockType} onCopy={() => block.text || ""} collapsedHeight={100}>
				<div className="custom-scrollbar max-h-[400px] overflow-y-auto px-6 py-2 font-mono text-xs break-words whitespace-pre-wrap">{block.text}</div>
			</CollapsibleBox>
		)
	}

	// Handle image content
	if (block.image_url) {
		const jsonContent = JSON.stringify(block.image_url, null, 2)
		return (
			<CollapsibleBox title={blockType} onCopy={() => jsonContent} collapsedHeight={100}>
				<CodeEditor
					className="z-0 w-full"
					shouldAdjustInitialHeight={true}
					maxHeight={150}
					wrap={true}
					code={jsonContent}
					lang="json"
					readonly={true}
					options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
				/>
			</CollapsibleBox>
		)
	}

	// Handle audio content
	if (block.input_audio) {
		const jsonContent = JSON.stringify(block.input_audio, null, 2)
		return (
			<CollapsibleBox title={blockType} onCopy={() => jsonContent} collapsedHeight={100}>
				<CodeEditor
					className="z-0 w-full"
					shouldAdjustInitialHeight={true}
					maxHeight={150}
					wrap={true}
					code={jsonContent}
					lang="json"
					readonly={true}
					options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
				/>
			</CollapsibleBox>
		)
	}

	return null
}

export default function LogChatMessageView({ message, audioFormat }: LogChatMessageViewProps) {
	return (
		<div className="flex w-full flex-col gap-2">
			{/* Role header */}
			<div className="flex items-center gap-2">
				<span className="text-sm font-medium capitalize">{message.role}</span>
				{message.tool_call_id && <span className="text-muted-foreground text-xs">Tool Call ID: {message.tool_call_id}</span>}
			</div>

			{/* Handle reasoning content */}
			{message.reasoning && (
				<>
					{isJson(message.reasoning) ? (
						<CollapsibleBox title="Reasoning" onCopy={() => JSON.stringify(cleanJson(message.reasoning), null, 2)} collapsedHeight={100}>
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
						</CollapsibleBox>
					) : (
						<CollapsibleBox title="Reasoning" onCopy={() => message.reasoning || ""} collapsedHeight={100}>
							<div className="custom-scrollbar text-muted-foreground max-h-[400px] overflow-y-auto px-6 py-2 font-mono text-xs break-words whitespace-pre-wrap italic">
								{message.reasoning}
							</div>
						</CollapsibleBox>
					)}
				</>
			)}

			{/* Handle refusal content */}
			{message.refusal && (
				<>
					{isJson(message.refusal) ? (
						<CollapsibleBox title="Refusal" onCopy={() => JSON.stringify(cleanJson(message.refusal), null, 2)} collapsedHeight={100}>
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
						</CollapsibleBox>
					) : (
						<CollapsibleBox title="Refusal" onCopy={() => message.refusal || ""} collapsedHeight={100}>
							<div className="custom-scrollbar max-h-[400px] overflow-y-auto px-6 py-2 font-mono text-xs break-words whitespace-pre-wrap text-red-800">{message.refusal}</div>
						</CollapsibleBox>
					)}
				</>
			)}

			{/* Handle content */}
			{message.content && (
				<>
					{typeof message.content === "string" ? (
						<>
							{isJson(message.content) ? (
								<CollapsibleBox title="Content" onCopy={() => JSON.stringify(cleanJson(message.content as string), null, 2)} collapsedHeight={100}>
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
								</CollapsibleBox>
							) : (
								<CollapsibleBox title="Content" onCopy={() => (message.content as string) || ""} collapsedHeight={100}>
									<div className="custom-scrollbar max-h-[400px] overflow-y-auto px-6 py-2 font-mono text-xs break-words whitespace-pre-wrap">{message.content}</div>
								</CollapsibleBox>
							)}
						</>
					) : (
						Array.isArray(message.content) &&
						message.content.map((block, blockIndex) => <ContentBlockView key={blockIndex} block={block} index={blockIndex} />)
					)}
				</>
			)}

			{/* Handle tool calls */}
			{message.tool_calls && message.tool_calls.length > 0 && (
				<>
					{message.tool_calls.map((toolCall, index) => {
						const jsonContent = JSON.stringify(toolCall, null, 2)
						return (
							<CollapsibleBox key={index} title={`Tool Call #${index + 1}`} onCopy={() => jsonContent} collapsedHeight={100}>
								<CodeEditor
									className="z-0 w-full"
									shouldAdjustInitialHeight={true}
									maxHeight={400}
									wrap={true}
									code={jsonContent}
									lang="json"
									readonly={true}
									options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
								/>
							</CollapsibleBox>
						)
					})}
				</>
			)}

			{/* Handle annotations */}
			{message.annotations && message.annotations.length > 0 && (
				<CollapsibleBox title="Annotations" onCopy={() => JSON.stringify(message.annotations, null, 2)} collapsedHeight={100}>
					<CodeEditor
						className="z-0 w-full"
						shouldAdjustInitialHeight={true}
						maxHeight={400}
						wrap={true}
						code={JSON.stringify(message.annotations, null, 2)}
						lang="json"
						readonly={true}
						options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
					/>
				</CollapsibleBox>
			)}

			{/* Handle audio output */}
			{message.audio && (
				<CollapsibleBox title="Audio Output" collapsedHeight={150}>
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
				</CollapsibleBox>
			)}
		</div>
	)
}
