"use client";

import { Badge } from "@/components/ui/badge";
import { DottedSeparator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { RequestTypeColors, RequestTypeLabels, Status, StatusColors } from "@/lib/constants/logs";
import { LogEntry } from "@/lib/types/logs";
import { DollarSign, FileText, Timer } from "lucide-react";
import moment from "moment";
import { CodeEditor } from "./codeEditor";
import LogEntryDetailsView from "./logEntryDetailsView";
import LogChatMessageView from "./logChatMessageView";
import SpeechView from "./speechView";
import TranscriptionView from "./transcriptionView";
import LogResponsesOutputView from "./logResponsesOutputView";

interface LogDetailSheetProps {
	log: LogEntry | null;
	open: boolean;
	onOpenChange: (open: boolean) => void;
}

export function LogDetailSheet({ log, open, onOpenChange }: LogDetailSheetProps) {
	if (!log) return null;

	// Taking out tool call
	let toolsParameter = null;
	if (log.params?.tools) {
		try {
			toolsParameter = JSON.stringify(log.params.tools, null, 2);
		} catch (ignored) {}
	}

	let toolChoice = null;
	if (log.params?.tool_choice) {
		try {
			toolChoice = JSON.stringify(log.params.tool_choice, null, 2);
		} catch (ignored) {}
	}

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="dark:bg-card flex w-full flex-col overflow-x-hidden bg-white p-8 sm:max-w-2xl">
				<SheetHeader className="px-0">
					<SheetTitle className="flex w-fit items-center gap-2 font-medium">
						{log.status === "success" && <p className="text-md max-w-full truncate">Request ID: {log.id}</p>}
						<Badge variant="outline" className={StatusColors[log.status as Status]}>
							{log.status}
						</Badge>
					</SheetTitle>
				</SheetHeader>
				<div className="space-y-4 rounded-sm border px-6 py-4">
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
							<LogEntryDetailsView className="w-full" label="Model" value={log.model} />
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

							{log.params &&
								Object.keys(log.params).length > 0 &&
								Object.entries(log.params)
									.filter(([key]) => key !== "tools")
									.filter(([_, value]) => typeof value === "boolean" || typeof value === "number" || typeof value === "string")
									.map(([key, value]) => <LogEntryDetailsView key={key} className="w-full" label={key} value={value} />)}
						</div>
					</div>
					{log.status === "success" && (
						<>
							<DottedSeparator />
							<div className="space-y-4">
								<BlockHeader title="Tokens" icon={<DollarSign className="h-5 w-5 text-gray-600" />} />
								<div className="grid w-full grid-cols-3 items-center justify-between gap-4">
									<LogEntryDetailsView className="w-full" label="Prompt Tokens" value={log.token_usage?.prompt_tokens || "-"} />
									<LogEntryDetailsView className="w-full" label="Completion Tokens" value={log.token_usage?.completion_tokens || "-"} />
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
						<div className="border-b px-6 py-2 text-sm font-medium">Tools</div>
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

				{/* Speech and Transcription Views */}
				{(log.speech_input || log.speech_output) && (
					<>
						<SpeechView speechInput={log.speech_input} speechOutput={log.speech_output} isStreaming={log.stream} />
					</>
				)}

				{(log.transcription_input || log.transcription_output) && (
					<>
						<div className="mt-4 w-full text-center text-sm font-medium">Transcription</div>
						<TranscriptionView
							transcriptionInput={log.transcription_input}
							transcriptionOutput={log.transcription_output}
							isStreaming={log.stream}
						/>
					</>
				)}

				{/* Show conversation history for chat/text completions */}
				{log.input_history && log.input_history.length > 1 && (
					<>
						<div className="mt-4 w-full text-left text-sm font-medium">Conversation History</div>
						{log.input_history.slice(0, -1).map((message, index) => (
							<LogChatMessageView key={index} message={message} />
						))}
					</>
				)}

				{/* Show input for chat/text completions */}
				{log.input_history && log.input_history.length > 0 && (
					<>
						<div className="mt-4 w-full text-left text-sm font-medium">Input</div>
						<LogChatMessageView message={log.input_history[log.input_history.length - 1]} />
					</>
				)}

				{log.status !== "processing" && (
					<>
						{log.output_message && !log.error_details?.error.message && (
							<>
								<div className="mt-4 flex w-full items-center gap-2">
									<div className="text-sm font-medium">Response</div>
								</div>
								<LogChatMessageView message={log.output_message} />
							</>
						)}
						{log.responses_output && log.responses_output.length > 0 && !log.error_details?.error.message && (
							<>
								<div className="mt-4 w-full text-left text-sm font-medium">Response</div>
								<LogResponsesOutputView messages={log.responses_output} />
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
