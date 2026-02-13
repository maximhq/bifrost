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
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { DottedSeparator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { ProviderIconType, RenderProviderIcon, RoutingEngineUsedIcons } from "@/lib/constants/icons";
import {
	RequestTypeColors,
	RequestTypeLabels,
	RoutingEngineUsedColors,
	RoutingEngineUsedLabels,
	Status,
	StatusColors,
} from "@/lib/constants/logs";
import { LogEntry } from "@/lib/types/logs";
import { DollarSign, FileText, MoreVertical, Timer, Trash2 } from "lucide-react";
import moment from "moment";
import { toast } from "sonner";

import BlockHeader from "../views/blockHeader";
import { CodeEditor } from "../views/codeEditor";
import CollapsibleBox from "../views/collapsibleBox";
import ImageView from "../views/imageView";
import LogChatMessageView from "../views/logChatMessageView";
import LogEntryDetailsView from "../views/logEntryDetailsView";
import LogResponsesMessageView from "../views/logResponsesMessageView";
import SpeechView from "../views/speechView";
import TranscriptionView from "../views/transcriptionView";

interface LogDetailSheetProps {
	log: LogEntry | null;
	open: boolean;
	onOpenChange: (open: boolean) => void;
	handleDelete: (log: LogEntry) => void;
}

// Helper to detect container operations (for hiding irrelevant fields like Model/Tokens)
const isContainerOperation = (object: string) => {
	const containerTypes = [
		"container_create",
		"container_list",
		"container_retrieve",
		"container_delete",
		"container_file_create",
		"container_file_list",
		"container_file_retrieve",
		"container_file_content",
		"container_file_delete",
	];
	return containerTypes.includes(object?.toLowerCase());
};

export function LogDetailSheet({ log, open, onOpenChange, handleDelete }: LogDetailSheetProps) {
	if (!log) return null;

	const isContainer = isContainerOperation(log.object);

	// Taking out tool call
	let toolsParameter = null;
	if (log.params?.tools) {
		try {
			toolsParameter = JSON.stringify(log.params.tools, null, 2);
		} catch (ignored) { }
	}

	// Extract audio format from request params
	// Format can be in params.audio?.format or params.extra_params?.audio?.format
	const audioFormat = (log.params as any)?.audio?.format || (log.params as any)?.extra_params?.audio?.format || undefined;

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="dark:bg-card flex w-full flex-col gap-4 overflow-x-hidden bg-white p-8 sm:max-w-[60%]">
				<SheetHeader className="flex flex-row items-center px-0">
					<div className="flex w-full items-center justify-between">
						<SheetTitle className="flex w-fit items-center gap-2 font-medium">
							{log.id && (
								<p className="text-md max-w-full truncate">
									Request ID:{" "}
									<code className="text-normal cursor-pointer" onClick={() => {
										navigator.clipboard.writeText(log.id).then(() => toast.success("Request ID copied")).catch(() => toast.error("Failed to copy"));
									}}>
										{log.id}
									</code>
								</p>
							)}
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
				<div className="-mt-6 space-y-4 rounded-sm border px-6 py-4">
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
										className={`${RequestTypeColors[log.object as keyof typeof RequestTypeColors] ?? "bg-gray-100 text-gray-800"
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
							{log.routing_engines_used && log.routing_engines_used.length > 0 && (
								<LogEntryDetailsView className="w-full" label="Routing Engines Used" value={
									<div className="flex flex-wrap gap-2">
										{log.routing_engines_used.map((engine) => (
											<Badge key={engine} className={RoutingEngineUsedColors[engine as keyof typeof RoutingEngineUsedColors] ?? "bg-gray-100 text-gray-800"}>
												<div className="flex items-center gap-2">
													{RoutingEngineUsedIcons[engine as keyof typeof RoutingEngineUsedIcons]?.()}
													<span>{RoutingEngineUsedLabels[engine as keyof typeof RoutingEngineUsedLabels] ?? engine}</span>
												</div>
											</Badge>
										))}
									</div>
								} />
							)}
							{log.routing_rule && <LogEntryDetailsView className="w-full" label="Routing Rule" value={log.routing_rule.name} />}

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
					<CollapsibleBox title={`Tools (${log.params?.tools?.length || 0})`} onCopy={() => toolsParameter}>
						<CodeEditor
							className="z-0 w-full"
							shouldAdjustInitialHeight={true}
							maxHeight={450}
							wrap={true}
							code={toolsParameter}
							lang="json"
							readonly={true}
							options={{ scrollBeyondLastLine: false, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
						/>
					</CollapsibleBox>
				)}
				{log.params?.instructions && (
					<CollapsibleBox title="Instructions" onCopy={() => log.params?.instructions || ""}>
						<div className="custom-scrollbar max-h-[400px] overflow-y-auto px-6 py-2 font-mono text-xs break-words whitespace-pre-wrap">
							{log.params.instructions}
						</div>
					</CollapsibleBox>
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
					<ImageView imageInput={log.image_generation_input} imageOutput={log.image_generation_output} requestType={log.object} />
				)}

				{log.list_models_output && (
					<CollapsibleBox
						title={`List Models Output (${log.list_models_output.length})`}
						onCopy={() => JSON.stringify(log.list_models_output, null, 2)}
					>
						<CodeEditor
							className="z-0 w-full"
							shouldAdjustInitialHeight={true}
							maxHeight={450}
							wrap={true}
							code={JSON.stringify(log.list_models_output, null, 2)}
							lang="json"
							readonly={true}
							options={{ scrollBeyondLastLine: false, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
						/>
					</CollapsibleBox>
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
								<CollapsibleBox
									title="Raw Request"
									onCopy={() => {
										try {
											return JSON.stringify(JSON.parse(log.raw_request || ""), null, 2);
										} catch {
											return log.raw_request || "";
										}
									}}
								>
									<CodeEditor
										className="z-0 w-full"
										shouldAdjustInitialHeight={true}
										maxHeight={450}
										wrap={true}
										code={(() => {
											try {
												return JSON.stringify(JSON.parse(log.raw_request || ""), null, 2);
											} catch {
												return log.raw_request || "";
											}
										})()}
										lang="json"
										readonly={true}
										options={{ scrollBeyondLastLine: false, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
									/>
								</CollapsibleBox>
							</>
						)}
						{log.raw_response && (
							<>
								<div className="mt-4 w-full text-left text-sm font-medium">
									Raw Response from <span className="font-medium capitalize">{log.provider}</span>
								</div>
								<CollapsibleBox
									title="Raw Response"
									onCopy={() => {
										try {
											return JSON.stringify(JSON.parse(log.raw_response || ""), null, 2);
										} catch {
											return log.raw_response || "";
										}
									}}
								>
									<CodeEditor
										className="z-0 w-full"
										shouldAdjustInitialHeight={true}
										maxHeight={450}
										wrap={true}
										code={(() => {
											try {
												return JSON.stringify(JSON.parse(log.raw_response || ""), null, 2);
											} catch {
												return log.raw_response || "";
											}
										})()}
										lang="json"
										readonly={true}
										options={{ scrollBeyondLastLine: false, lineNumbers: "off", alwaysConsumeMouseWheel: false }}
									/>
								</CollapsibleBox>
							</>
						)}
						{log.error_details?.error.message && (
							<>
								<div className="mt-4 w-full text-left text-sm font-medium">Error</div>
								<CollapsibleBox title="Error" onCopy={() => log.error_details?.error.message || ""}>
									<div className="custom-scrollbar max-h-[400px] overflow-y-auto px-6 py-2 font-mono text-xs break-words whitespace-pre-wrap">
										{log.error_details.error.message}
									</div>
								</CollapsibleBox>
							</>
						)}
						{log.error_details?.error.error && (
							<>
								<div className="mt-4 w-full text-left text-sm font-medium">Error Details</div>
								<CollapsibleBox
									title="Details"
									onCopy={() =>
										typeof log.error_details?.error.error === "string"
											? log.error_details.error.error
											: JSON.stringify(log.error_details?.error.error, null, 2)
									}
								>
									<div className="custom-scrollbar max-h-[400px] overflow-y-auto px-6 py-2 font-mono text-xs break-words whitespace-pre-wrap">
										{typeof log.error_details?.error.error === "string"
											? log.error_details.error.error
											: JSON.stringify(log.error_details?.error.error, null, 2)}
									</div>
								</CollapsibleBox>
							</>
						)}
					</>
				)}
			</SheetContent>
		</Sheet>
	);
}
