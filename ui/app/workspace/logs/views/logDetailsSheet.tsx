"use client";

import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
	AlertDialogTrigger,
} from "@/components/ui/alertDialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "@/components/ui/dropdownMenu";
import { DottedSeparator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { RequestTypeColors, RequestTypeLabels, Status, StatusColors } from "@/lib/constants/logs";
import { LogEntry } from "@/lib/types/logs";
import { Clipboard, DollarSign, FileText, MoreVertical, Timer, Trash2 } from "lucide-react";
import moment from "moment";
import { toast } from "sonner";
import { CodeEditor } from "./codeEditor";
import ImageView from "./imageView";
import LogChatMessageView from "./logChatMessageView";
import LogEntryDetailsView from "./logEntryDetailsView";
import LogResponsesMessageView from "./logResponsesMessageView";
import SpeechView from "./speechView";
import TranscriptionView from "./transcriptionView";

interface LogDetailSheetProps {
	log: LogEntry | null;
	open: boolean;
	onOpenChange: (open: boolean) => void;
	handleDelete: (log: LogEntry) => void;
}

// Helper to detect container operations (for hiding irrelevant fields like Model/Tokens)
const isContainerOperation = (object: string) => {
	const containerTypes = [
		'container_create', 'container_list', 'container_retrieve', 'container_delete',
		'container_file_create', 'container_file_list', 'container_file_retrieve',
		'container_file_content', 'container_file_delete'
	]
	return containerTypes.includes(object?.toLowerCase())
}

export function LogDetailSheet({ log, open, onOpenChange, handleDelete }: LogDetailSheetProps) {
	if (!log) return null;

	const isContainer = isContainerOperation(log.object)

	// Taking out tool call
	let toolsParameter = null;
	if (log.params?.tools) {
		try {
			toolsParameter = JSON.stringify(log.params.tools, null, 2);
		} catch (ignored) {}
	}

	const copyRequestBody = async () => {
		try {
			// Check if request is for responses, chat, speech, text completion, or embedding (exclude transcriptions)
			const object = log.object?.toLowerCase() || "";
			const isChat = object === "chat_completion" || object === "chat_completion_stream";
			const isResponses = object === "responses" || object === "responses_stream";
			const isSpeech = object === "speech" || object === "speech_stream";
			const isTextCompletion = object === "text_completion" || object === "text_completion_stream";
			const isEmbedding = object === "embedding";
			const isTranscription = object === "transcription" || object === "transcription_stream";

			// Skip if transcription
			if (isTranscription) {
				toast.error("Copy request body is not available for transcription requests");
				return;
			}

			// Skip if not a supported request type
			if (!isChat && !isResponses && !isSpeech && !isTextCompletion && !isEmbedding) {
				toast.error("Copy request body is only available for chat, responses, speech, text completion, and embedding requests");
				return;
			}

			// Helper function to extract text content from ChatMessage
			const extractTextFromMessage = (message: any): string => {
				if (!message || !message.content) {
					return "";
				}
				if (typeof message.content === "string") {
					return message.content;
				}
				if (Array.isArray(message.content)) {
					return message.content
						.filter((block: any) => block && block.type === "text" && block.text)
						.map((block: any) => block.text || "")
						.join("");
				}
				return "";
			};

			// Helper function to extract texts from ChatMessage content blocks (for embeddings)
			const extractTextsFromMessage = (message: any): string[] => {
				if (!message || !message.content) {
					return [];
				}
				if (typeof message.content === "string") {
					return message.content ? [message.content] : [];
				}
				if (Array.isArray(message.content)) {
					return message.content.filter((block: any) => block && block.type === "text" && block.text).map((block: any) => block.text);
				}
				return [];
			};

			// Build request body following OpenAI schema
			const requestBody: any = {
				model: log.provider && log.model ? `${log.provider}/${log.model}` : log.model || "",
			};

			// Add messages/input/prompt based on request type
			if (isChat && log.input_history && log.input_history.length > 0) {
				requestBody.messages = log.input_history;
			} else if (isResponses && log.responses_input_history && log.responses_input_history.length > 0) {
				requestBody.input = log.responses_input_history;
			} else if (isSpeech && log.speech_input) {
				requestBody.input = log.speech_input.input;
			} else if (isTextCompletion && log.input_history && log.input_history.length > 0) {
				// For text completions, extract prompt from input_history
				const firstMessage = log.input_history[0];
				const prompt = extractTextFromMessage(firstMessage);
				if (prompt) {
					requestBody.prompt = prompt;
				}
			} else if (isEmbedding && log.input_history && log.input_history.length > 0) {
				// For embeddings, extract all texts from input_history
				const texts: string[] = [];
				for (const message of log.input_history) {
					const messageTexts = extractTextsFromMessage(message);
					texts.push(...messageTexts);
				}
				if (texts.length > 0) {
					// Use single string if only one text, otherwise use array
					requestBody.input = texts.length === 1 ? texts[0] : texts;
				}
			}

			// Add params (excluding tools and instructions as they're handled separately in OpenAI schema)
			if (log.params) {
				const paramsCopy = { ...log.params };
				// Remove tools and instructions from params as they're typically top-level in OpenAI schema
				// Keep all other params (temperature, max_tokens, voice, etc.)
				delete paramsCopy.tools;
				delete paramsCopy.instructions;

				// Merge remaining params into request body
				Object.assign(requestBody, paramsCopy);
			}

			// Add tools if they exist (for chat and responses) - OpenAI schema has tools at top level
			if ((isChat || isResponses) && log.params?.tools && Array.isArray(log.params.tools) && log.params.tools.length > 0) {
				requestBody.tools = log.params.tools;
			}

			// Add instructions if they exist (for responses) - OpenAI schema has instructions at top level
			if (isResponses && log.params?.instructions) {
				requestBody.instructions = log.params.instructions;
			}

			const requestBodyJson = JSON.stringify(requestBody, null, 2);
			navigator.clipboard
				.writeText(requestBodyJson)
				.then(() => {
					toast.success("Request body copied to clipboard");
				})
				.catch((error) => {
					toast.error("Failed to copy request body");
				});
		} catch (error) {
			toast.error("Failed to copy request body");
		}
	};
	// Extract audio format from request params
	// Format can be in params.audio?.format or params.extra_params?.audio?.format
	const audioFormat = (log.params as any)?.audio?.format || (log.params as any)?.extra_params?.audio?.format || undefined;

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="dark:bg-card flex w-full flex-col gap-4 overflow-x-hidden bg-white p-8">
				<SheetHeader className="flex flex-row items-center px-0">
					<div className="flex w-full items-center justify-between">
						<SheetTitle className="flex w-fit items-center gap-2 font-medium">
							{log.id && <p className="text-md max-w-full truncate">Request ID: {log.id}</p>}
							<Badge variant="outline" className={`${StatusColors[log.status as Status]} uppercase`}>
								{log.status}
							</Badge>
						</SheetTitle>
					</div>
					<AlertDialog>
						<DropdownMenu>
							<DropdownMenuTrigger asChild>
								<Button variant="ghost" size="icon">
									<MoreVertical className="h-3 w-3" />
								</Button>
							</DropdownMenuTrigger>
							<DropdownMenuContent align="end">
								<DropdownMenuItem onClick={copyRequestBody}>
									<Clipboard className="h-4 w-4" />
									Copy request body
								</DropdownMenuItem>
								<DropdownMenuSeparator />
								<AlertDialogTrigger asChild>
									<DropdownMenuItem variant="destructive">
										<Trash2 className="h-4 w-4" />
										Delete log
									</DropdownMenuItem>
								</AlertDialogTrigger>
							</DropdownMenuContent>
						</DropdownMenu>
						<AlertDialogContent>
							<AlertDialogHeader>
								<AlertDialogTitle>Are you sure you want to delete this log?</AlertDialogTitle>
								<AlertDialogDescription>This action cannot be undone. This will permanently delete the log entry.</AlertDialogDescription>
							</AlertDialogHeader>
							<AlertDialogFooter>
								<AlertDialogCancel>Cancel</AlertDialogCancel>
								<AlertDialogAction
									onClick={() => {
										handleDelete(log);
										onOpenChange(false);
									}}
								>
									Delete
								</AlertDialogAction>
							</AlertDialogFooter>
						</AlertDialogContent>
					</AlertDialog>
				</SheetHeader>
				<div className="-mt-4 space-y-4 rounded-sm border px-6 py-4">
					<div className="space-y-4">
						<BlockHeader title="Timings" icon={<Timer className="h-5 w-5 text-gray-600" />} />
						<div className="grid w-full grid-cols-3 items-center justify-between gap-4">
							<LogEntryDetailsView
								className="w-full"
								label="Start Timestamp"
								value={moment(log.timestamp).format("YYYY-MM-DD HH:mm:ss A")}
							/>
							<LogEntryDetailsView
								className="w-full"
								label="End Timestamp"
								value={moment(log.timestamp)
									.add(log.latency || 0, "ms")
									.format("YYYY-MM-DD HH:mm:ss A")}
							/>
							<LogEntryDetailsView
								className="w-full"
								label="Latency"
								value={isNaN(log.latency || 0) ? "NA" : <div>{(log.latency || 0)?.toFixed(2)}ms</div>}
							/>
						</div>
					</div>
					<DottedSeparator />
					<div className="space-y-4">
						<BlockHeader title="Request Details" icon={<FileText className="h-5 w-5 text-gray-600" />} />
						<div className="grid w-full grid-cols-3 items-start justify-between gap-4">
							<LogEntryDetailsView
								className="w-full"
								label="Provider"
								value={
									<Badge variant="secondary" className={`uppercase`}>
										<RenderProviderIcon provider={log.provider as ProviderIconType} size="sm" />
										{log.provider}
									</Badge>
								}
							/>
							{!isContainer && <LogEntryDetailsView className="w-full" label="Model" value={log.model} />}
							<LogEntryDetailsView
								className="w-full"
								label="Type"
								value={
									<div
										className={`${
											RequestTypeColors[log.object as keyof typeof RequestTypeColors] ?? "bg-gray-100 text-gray-800"
										} rounded-sm px-3 py-1`}
									>
										{RequestTypeLabels[log.object as keyof typeof RequestTypeLabels] ?? log.object ?? "unknown"}
									</div>
								}
							/>
							{log.selected_key && <LogEntryDetailsView className="w-full" label="Selected Key" value={log.selected_key.name} />}
							{log.number_of_retries > 0 && (
								<LogEntryDetailsView className="w-full" label="Number of Retries" value={log.number_of_retries} />
							)}
							{log.fallback_index > 0 && <LogEntryDetailsView className="w-full" label="Fallback Index" value={log.fallback_index} />}
							{log.virtual_key && <LogEntryDetailsView className="w-full" label="Virtual Key" value={log.virtual_key.name} />}

							{/* Display audio params if present */}
							{(log.params as any)?.audio && (
								<>
									{(log.params as any).audio.format && (
										<LogEntryDetailsView className="w-full" label="Audio Format" value={(log.params as any).audio.format} />
									)}
									{(log.params as any).audio.voice && (
										<LogEntryDetailsView className="w-full" label="Audio Voice" value={(log.params as any).audio.voice} />
									)}
								</>
							)}

							{log.params &&
								Object.keys(log.params).length > 0 &&
								Object.entries(log.params)
									.filter(([key]) => key !== "tools" && key !== "instructions" && key !== "audio")
									.filter(([_, value]) => typeof value === "boolean" || typeof value === "number" || typeof value === "string")
									.map(([key, value]) => <LogEntryDetailsView key={key} className="w-full" label={key} value={value} />)}
						</div>
					</div>
					{log.status === "success" && !isContainer && (
						<>
							<DottedSeparator />
							<div className="space-y-4">
								<BlockHeader title="Tokens" icon={<DollarSign className="h-5 w-5 text-gray-600" />} />
								<div className="grid w-full grid-cols-3 items-center justify-between gap-4">
									<LogEntryDetailsView className="w-full" label="Input Tokens" value={log.token_usage?.prompt_tokens || "-"} />
									<LogEntryDetailsView className="w-full" label="Output Tokens" value={log.token_usage?.completion_tokens || "-"} />
									<LogEntryDetailsView className="w-full" label="Total Tokens" value={log.token_usage?.total_tokens || "-"} />
									{log.token_usage?.prompt_tokens_details && (
										<>
											{log.token_usage.prompt_tokens_details.cached_tokens && (
												<LogEntryDetailsView
													className="w-full"
													label="Cached Tokens"
													value={log.token_usage.prompt_tokens_details.cached_tokens || "-"}
												/>
											)}
											{log.token_usage.prompt_tokens_details.audio_tokens && (
												<LogEntryDetailsView
													className="w-full"
													label="Input Audio Tokens"
													value={log.token_usage.prompt_tokens_details.audio_tokens || "-"}
												/>
											)}
										</>
									)}
									{log.token_usage?.completion_tokens_details && (
										<>
											{log.token_usage.completion_tokens_details.reasoning_tokens && (
												<LogEntryDetailsView
													className="w-full"
													label="Reasoning Tokens"
													value={log.token_usage.completion_tokens_details.reasoning_tokens || "-"}
												/>
											)}
											{log.token_usage.completion_tokens_details.audio_tokens && (
												<LogEntryDetailsView
													className="w-full"
													label="Output Audio Tokens"
													value={log.token_usage.completion_tokens_details.audio_tokens || "-"}
												/>
											)}
											{log.token_usage.completion_tokens_details.accepted_prediction_tokens && (
												<LogEntryDetailsView
													className="w-full"
													label="Accepted Prediction Tokens"
													value={log.token_usage.completion_tokens_details.accepted_prediction_tokens || "-"}
												/>
											)}
											{log.token_usage.completion_tokens_details.rejected_prediction_tokens && (
												<LogEntryDetailsView
													className="w-full"
													label="Rejected Prediction Tokens"
													value={log.token_usage.completion_tokens_details.rejected_prediction_tokens || "-"}
												/>
											)}
										</>
									)}
								</div>
							</div>
							{(() => {
								const params = log.params as any;
								const reasoning = params?.reasoning;
								if (!reasoning || typeof reasoning !== "object" || Object.keys(reasoning).length === 0) {
									return null;
								}
								return (
									<>
										<DottedSeparator />
										<div className="space-y-4">
											<BlockHeader title="Reasoning Parameters" icon={<FileText className="h-5 w-5 text-gray-600" />} />
											<div className="grid w-full grid-cols-3 items-center justify-between gap-4">
												{reasoning.effort && (
													<LogEntryDetailsView
														className="w-full"
														label="Effort"
														value={
															<Badge variant="secondary" className="uppercase">
																{reasoning.effort}
															</Badge>
														}
													/>
												)}
												{reasoning.summary && (
													<LogEntryDetailsView
														className="w-full"
														label="Summary"
														value={
															<Badge variant="secondary" className="uppercase">
																{reasoning.summary}
															</Badge>
														}
													/>
												)}
												{reasoning.generate_summary && (
													<LogEntryDetailsView
														className="w-full"
														label="Generate Summary"
														value={
															<Badge variant="secondary" className="uppercase">
																{reasoning.generate_summary}
															</Badge>
														}
													/>
												)}
												{reasoning.max_tokens && <LogEntryDetailsView className="w-full" label="Max Tokens" value={reasoning.max_tokens} />}
											</div>
										</div>
									</>
								);
							})()}
							{log.cache_debug && (
								<>
									<DottedSeparator />
									<div className="space-y-4">
										<BlockHeader
											title={`Caching Details (${log.cache_debug.cache_hit ? "Hit" : "Miss"})`}
											icon={<DollarSign className="h-5 w-5 text-gray-600" />}
										/>
										<div className="grid w-full grid-cols-3 items-center justify-between gap-4">
											{log.cache_debug.cache_hit ? (
												<>
													<LogEntryDetailsView
														className="w-full"
														label="Cache Type"
														value={
															<Badge variant="secondary" className={`uppercase`}>
																{log.cache_debug.hit_type}
															</Badge>
														}
													/>
													{/* <LogEntryDetailsView className="w-full" label="Cache ID" value={log.cache_debug.cache_id} /> */}
													{log.cache_debug.hit_type === "semantic" && (
														<>
															{log.cache_debug.provider_used && (
																<LogEntryDetailsView
																	className="w-full"
																	label="Embedding Provider"
																	value={
																		<Badge variant="secondary" className={`uppercase`}>
																			{log.cache_debug.provider_used}
																		</Badge>
																	}
																/>
															)}
															{log.cache_debug.model_used && (
																<LogEntryDetailsView className="w-full" label="Embedding Model" value={log.cache_debug.model_used} />
															)}
															{log.cache_debug.threshold && (
																<LogEntryDetailsView className="w-full" label="Threshold" value={log.cache_debug.threshold || "-"} />
															)}
															{log.cache_debug.similarity && (
																<LogEntryDetailsView
																	className="w-full"
																	label="Similarity Score"
																	value={log.cache_debug.similarity?.toFixed(2) || "-"}
																/>
															)}
															{log.cache_debug.input_tokens && (
																<LogEntryDetailsView
																	className="w-full"
																	label="Embedding Input Tokens"
																	value={log.cache_debug.input_tokens}
																/>
															)}
														</>
													)}
												</>
											) : (
												<>
													{log.cache_debug.provider_used && (
														<LogEntryDetailsView
															className="w-full"
															label="Embedding Provider"
															value={
																<Badge variant="secondary" className={`uppercase`}>
																	{log.cache_debug.provider_used}
																</Badge>
															}
														/>
													)}
													{log.cache_debug.model_used && (
														<LogEntryDetailsView className="w-full" label="Embedding Model" value={log.cache_debug.model_used} />
													)}
													{log.cache_debug.input_tokens && (
														<LogEntryDetailsView className="w-full" label="Embedding Input Tokens" value={log.cache_debug.input_tokens} />
													)}
												</>
											)}
										</div>
									</div>
								</>
							)}
						</>
					)}
				</div>
				{toolsParameter && (
					<div className="w-full rounded-sm border">
						<div className="border-b px-6 py-2 text-sm font-medium">Tools ({log.params?.tools?.length || 0})</div>
						<CodeEditor
							className="z-0 w-full"
							shouldAdjustInitialHeight={true}
							maxHeight={450}
							wrap={true}
							code={toolsParameter}
							lang="json"
							readonly={true}
							options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
						/>
					</div>
				)}
				{log.params?.instructions && (
					<div className="w-full rounded-sm border">
						<div className="border-b px-6 py-2 text-sm font-medium">Instructions</div>
						<div className="px-6 py-2 font-mono text-xs break-words whitespace-pre-wrap">{log.params.instructions}</div>
					</div>
				)}

				{/* Speech and Transcription Views */}
				{(log.speech_input || log.speech_output) && (
					<SpeechView speechInput={log.speech_input} speechOutput={log.speech_output} isStreaming={log.stream} />
				)}

				{(log.transcription_input || log.transcription_output) && (
					<TranscriptionView
						transcriptionInput={log.transcription_input}
						transcriptionOutput={log.transcription_output}
						isStreaming={log.stream}
					/>
				)}

				{(log.image_generation_input || log.image_generation_output) && (
					<ImageView imageInput={log.image_generation_input} imageOutput={log.image_generation_output} />
				)}

				{/* Show conversation history for chat/text completions */}
				{log.input_history && log.input_history.length > 1 && (
					<>
						<div className="mt-4 w-full text-left text-sm font-medium">Conversation History</div>
						{log.input_history.slice(0, -1).map((message, index) => (
							<LogChatMessageView key={index} message={message} audioFormat={audioFormat} />
						))}
					</>
				)}

				{/* Show input for chat/text completions */}
				{log.input_history && log.input_history.length > 0 && (
					<>
						<div className="mt-4 w-full text-left text-sm font-medium">Input</div>
						<LogChatMessageView message={log.input_history[log.input_history.length - 1]} audioFormat={audioFormat} />
					</>
				)}

				{/* Show input history for responses API */}
				{log.responses_input_history && log.responses_input_history.length > 0 && (
					<>
						<div className="mt-4 w-full text-left text-sm font-medium">Input</div>
						<LogResponsesMessageView messages={log.responses_input_history} />
					</>
				)}

				{log.status !== "processing" && (
					<>
						{log.output_message && !log.error_details?.error.message && (
							<>
								<div className="mt-4 flex w-full items-center gap-2">
									<div className="text-sm font-medium">Response</div>
								</div>
								<LogChatMessageView message={log.output_message} audioFormat={audioFormat} />
							</>
						)}
						{log.responses_output && log.responses_output.length > 0 && !log.error_details?.error.message && (
							<>
								<div className="mt-4 w-full text-left text-sm font-medium">Response</div>
								<LogResponsesMessageView messages={log.responses_output} />
							</>
						)}
						{log.embedding_output && log.embedding_output.length > 0 && !log.error_details?.error.message && (
							<>
								<div className="mt-4 w-full text-left text-sm font-medium">Embedding</div>
								<LogChatMessageView
									message={{
										role: "assistant",
										content: JSON.stringify(
											log.embedding_output.map((embedding) => embedding.embedding),
											null,
											2,
										),
									}}
								/>
							</>
						)}
						{log.raw_request && (
							<>
								<div className="mt-4 w-full text-left text-sm font-medium">
									Raw Request sent to <span className="font-medium capitalize">{log.provider}</span>
								</div>
								<div className="w-full rounded-sm border">
									<CodeEditor
										className="z-0 w-full"
										shouldAdjustInitialHeight={true}
										maxHeight={250}
										wrap={true}
										code={(() => {
											try {
												return JSON.stringify(JSON.parse(log.raw_request), null, 2);
											} catch {
												return log.raw_request; // Fallback to raw string if parsing fails
											}
										})()}
										lang="json"
										readonly={true}
										options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
									/>
								</div>
							</>
						)}
						{log.raw_response && (
							<>
								<div className="mt-4 w-full text-left text-sm font-medium">
									Raw Response from <span className="font-medium capitalize">{log.provider}</span>
								</div>
								<div className="w-full rounded-sm border">
									<CodeEditor
										className="z-0 w-full"
										shouldAdjustInitialHeight={true}
										maxHeight={250}
										wrap={true}
										code={(() => {
											try {
												return JSON.stringify(JSON.parse(log.raw_response), null, 2);
											} catch {
												return log.raw_response; // Fallback to raw string if parsing fails
											}
										})()}
										lang="json"
										readonly={true}
										options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
									/>
								</div>
							</>
						)}
						{log.error_details?.error.message && (
							<>
								<div className="mt-4 w-full text-left text-sm font-medium">Error</div>
								<div className="w-full rounded-sm border">
									<div className="border-b px-6 py-2 text-sm font-medium">Error</div>
									<div className="px-6 py-2 font-mono text-xs">{log.error_details.error.message}</div>
								</div>
							</>
						)}
						{log.error_details?.error.error && (
							<>
								<div className="mt-4 w-full text-left text-sm font-medium">Error Details</div>
								<div className="w-full rounded-sm border">
									<div className="border-b px-6 py-2 text-sm font-medium">Details</div>
									<div className="px-6 py-2 font-mono text-xs break-words whitespace-pre-wrap">
										{typeof log.error_details?.error.error === "string"
											? log.error_details.error.error
											: JSON.stringify(log.error_details?.error.error, null, 2)}
									</div>
								</div>
							</>
						)}
					</>
				)}
			</SheetContent>
		</Sheet>
	);
}

const BlockHeader = ({ title, icon }: { title: string; icon: React.ReactNode }) => {
	return (
		<div className="flex items-center gap-2">
			{/* {icon} */}
			<div className="text-sm font-medium">{title}</div>
		</div>
	);
};
