"use client";

import type { ReactNode } from "react";
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
import { CodeEditor } from "@/components/ui/codeEditor";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "@/components/ui/dropdownMenu";
import { DottedSeparator } from "@/components/ui/separator";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
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
import { Clipboard, Loader2, MoreVertical, Trash2 } from "lucide-react";
import moment from "moment";
import { toast } from "sonner";
import BlockHeader from "../views/blockHeader";
import CollapsibleBox from "../views/collapsibleBox";
import ImageView from "../views/imageView";
import LogChatMessageView from "../views/logChatMessageView";
import LogEntryDetailsView from "../views/logEntryDetailsView";
import LogResponsesMessageView from "../views/logResponsesMessageView";
import PluginLogsView from "../views/pluginLogsView";
import SpeechView from "../views/speechView";
import TranscriptionView from "../views/transcriptionView";
import VideoView from "../views/videoView";

const formatJsonSafe = (str: string | undefined): string => {
	try {
		return JSON.stringify(JSON.parse(str || ""), null, 2);
	} catch {
		return str || "";
	}
};

// Helper to detect passthrough operations
const isPassthroughOperation = (object: string) => object === "passthrough" || object === "passthrough_stream";

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

interface LogDetailViewProps {
	log: LogEntry | null;
	loading?: boolean;
	handleDelete?: (log: LogEntry) => void;
	onClose?: () => void;
	headerAction?: ReactNode;
	onFilterByParentRequestId?: (parentRequestId: string) => void;
}

export function LogDetailView({ log, loading = false, handleDelete, onClose, headerAction, onFilterByParentRequestId }: LogDetailViewProps) {
	if (!log) return null;

	const isContainer = isContainerOperation(log.object);
	const isPassthrough = isPassthroughOperation(log.object);
	const passthroughParams = isPassthrough
		? (log.params as {
				method?: string;
				path?: string;
				raw_query?: string;
				status_code?: number;
			})
		: null;

	let toolsParameter = null;
	if (log.params?.tools) {
		try {
			toolsParameter = JSON.stringify(log.params.tools, null, 2);
		} catch {}
	}

	const audioFormat = (log.params as any)?.audio?.format || (log.params as any)?.extra_params?.audio?.format || undefined;
	const rawRequest = log.raw_request;
	const rawResponse = log.raw_response;
	const passthroughRequestBody = log.passthrough_request_body;
	const passthroughResponseBody = log.passthrough_response_body;
	const videoOutput = log.video_generation_output || log.video_retrieve_output || log.video_download_output;
	const videoListOutput = log.video_list_output;

	return loading ? (
		<div className="flex h-full items-center justify-center">
			<Loader2 className="text-muted-foreground h-6 w-6 animate-spin" />
		</div>
	) : (
		<>
			<div className="flex flex-row items-center px-0">
				<div className="flex w-full items-center justify-between gap-3">
					<div className="flex items-center gap-3">
						{headerAction}
						<div className="flex w-fit items-center gap-2 font-medium">
							{log.id && (
								<p className="text-md max-w-full truncate">
									Request ID:{" "}
									<code
										className="font-normal cursor-pointer"
										onClick={() => {
											navigator.clipboard
												.writeText(log.id)
												.then(() => toast.success("Request ID copied"))
												.catch(() => toast.error("Failed to copy"));
										}}
									>
										{log.id}
									</code>
								</p>
							)}
							<Badge variant="outline" className={`${StatusColors[log.status as Status]} uppercase`}>
								{log.status}
							</Badge>
							{log.metadata?.isAsyncRequest ? (
								<Badge variant="outline" className="bg-teal-100 text-teal-800 uppercase dark:bg-teal-900 dark:text-teal-200">
									Async
								</Badge>
							) : null}
							{(log.is_large_payload_request || log.is_large_payload_response) && (
								<Badge
									variant="outline"
									className="border-amber-300 bg-amber-50 text-amber-700 dark:border-amber-600 dark:bg-amber-950 dark:text-amber-400"
								>
									Large Payload
								</Badge>
							)}
						</div>
					</div>
					{handleDelete && onClose ? (
						<AlertDialog>
							<DropdownMenu>
								<DropdownMenuTrigger asChild>
									<Button variant="ghost" size="icon">
										<MoreVertical className="h-3 w-3" />
									</Button>
								</DropdownMenuTrigger>
								<DropdownMenuContent align="end">
									<DropdownMenuItem onClick={() => copyRequestBody(log)} data-testid="logdetails-copy-request-body-button">
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
									<AlertDialogDescription>
										This action cannot be undone. This will permanently delete the log entry.
									</AlertDialogDescription>
								</AlertDialogHeader>
								<AlertDialogFooter>
									<AlertDialogCancel>Cancel</AlertDialogCancel>
									<AlertDialogAction
										onClick={() => {
											handleDelete(log);
											onClose();
										}}
									>
										Delete
									</AlertDialogAction>
								</AlertDialogFooter>
							</AlertDialogContent>
						</AlertDialog>
					) : null}
				</div>
			</div>
			<div className="space-y-4 rounded-sm border px-6 py-4">
				<div className="space-y-4">
					<BlockHeader title="Timings" />
					<div className="grid w-full grid-cols-3 items-center justify-between gap-4">
						<LogEntryDetailsView className="w-full" label="Start Timestamp" value={moment(log.timestamp).format("YYYY-MM-DD HH:mm:ss A")} />
						<LogEntryDetailsView
							className="w-full"
							label="End Timestamp"
							value={moment(log.timestamp).add(log.latency || 0, "ms").format("YYYY-MM-DD HH:mm:ss A")}
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
					<BlockHeader title="Request Details" />
					<div className="grid w-full grid-cols-3 items-start justify-between gap-4">
						<LogEntryDetailsView
							className="w-full"
							label="Provider"
							value={
								<Badge variant="secondary" className="uppercase">
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
								<div className={`${RequestTypeColors[log.object as keyof typeof RequestTypeColors] ?? "bg-gray-100 text-gray-800"} rounded-sm px-3 py-1`}>
									{RequestTypeLabels[log.object as keyof typeof RequestTypeLabels] ?? log.object ?? "unknown"}
								</div>
							}
						/>
						{log.parent_request_id && (
							<LogEntryDetailsView
								className="w-full"
								label="Parent Request ID"
								value={
									onFilterByParentRequestId ? (
										<Tooltip>
											<TooltipTrigger asChild>
												<code
													className="text-primary hover:text-primary/80 block min-w-0 cursor-pointer break-all font-normal underline-offset-2 hover:underline"
													onClick={() => onFilterByParentRequestId(log.parent_request_id as string)}
												>
													{log.parent_request_id}
												</code>
											</TooltipTrigger>
											<TooltipContent sideOffset={6}>Filter this session</TooltipContent>
										</Tooltip>
									) : (
										<code className="block min-w-0 break-all font-normal">{log.parent_request_id}</code>
									)
								}
							/>
						)}
						{log.selected_key && <LogEntryDetailsView className="w-full" label="Selected Key" value={log.selected_key.name} />}
						{log.number_of_retries > 0 && <LogEntryDetailsView className="w-full" label="Number of Retries" value={log.number_of_retries} />}
						{log.fallback_index > 0 && <LogEntryDetailsView className="w-full" label="Fallback Index" value={log.fallback_index} />}
						{log.virtual_key && <LogEntryDetailsView className="w-full" label="Virtual Key" value={log.virtual_key.name} />}
						{log.routing_engines_used && log.routing_engines_used.length > 0 && (
							<LogEntryDetailsView
								className="w-full"
								label="Routing Engines Used"
								value={
									<div className="flex flex-wrap gap-2">
										{log.routing_engines_used.map((engine) => (
											<Badge
												key={engine}
												className={RoutingEngineUsedColors[engine as keyof typeof RoutingEngineUsedColors] ?? "bg-gray-100 text-gray-800"}
											>
												<div className="flex items-center gap-2">
													{RoutingEngineUsedIcons[engine as keyof typeof RoutingEngineUsedIcons]?.()}
													<span>{RoutingEngineUsedLabels[engine as keyof typeof RoutingEngineUsedLabels] ?? engine}</span>
												</div>
											</Badge>
										))}
									</div>
								}
							/>
						)}
						{log.routing_rule && <LogEntryDetailsView className="w-full" label="Routing Rule" value={log.routing_rule.name} />}

						{(log.params as any)?.audio && (
							<>
								{(log.params as any).audio.format && <LogEntryDetailsView className="w-full" label="Audio Format" value={(log.params as any).audio.format} />}
								{(log.params as any).audio.voice && <LogEntryDetailsView className="w-full" label="Audio Voice" value={(log.params as any).audio.voice} />}
							</>
						)}

						{passthroughParams && (
							<>
								{passthroughParams.method && <LogEntryDetailsView className="w-full" label="Method" value={passthroughParams.method} />}
								{passthroughParams.path && <LogEntryDetailsView className="w-full" label="Path" value={passthroughParams.path} />}
								{passthroughParams.raw_query && <LogEntryDetailsView className="w-full" label="Query" value={passthroughParams.raw_query} />}
								{(passthroughParams.status_code ?? 0) !== 0 && (
									<LogEntryDetailsView className="w-full" label="Status Code" value={passthroughParams.status_code} />
								)}
							</>
						)}

						{log.params &&
							Object.keys(log.params).length > 0 &&
							Object.entries(log.params)
								.filter(([key]) => {
									const passthroughKeys = ["method", "path", "raw_query", "status_code"];
									return key !== "tools" && key !== "instructions" && key !== "audio" && !(isPassthrough && passthroughKeys.includes(key));
								})
								.filter(([_, value]) => typeof value === "boolean" || typeof value === "number" || typeof value === "string")
								.map(([key, value]) => <LogEntryDetailsView key={key} className="w-full" label={key} value={value} />)}
					</div>
				</div>
				{log.status === "success" && !isContainer && !isPassthrough && (
					<>
						<DottedSeparator />
						<div className="space-y-4">
							<BlockHeader title="Tokens" />
							<div className="grid w-full grid-cols-3 items-center justify-between gap-4">
								<LogEntryDetailsView className="w-full" label="Input Tokens" value={log.token_usage?.prompt_tokens || "-"} />
								<LogEntryDetailsView className="w-full" label="Output Tokens" value={log.token_usage?.completion_tokens || "-"} />
								<LogEntryDetailsView className="w-full" label="Total Tokens" value={log.token_usage?.total_tokens || "-"} />
								<LogEntryDetailsView className="w-full" label="Cost" value={log.cost != null ? `$${parseFloat(log.cost.toFixed(6))}` : "-"} />
								{log.token_usage?.prompt_tokens_details && (
									<>
										{log.token_usage.prompt_tokens_details.cached_read_tokens && (
											<LogEntryDetailsView className="w-full" label="Cache Read Tokens" value={log.token_usage.prompt_tokens_details.cached_read_tokens ?? 0} />
										)}
										{log.token_usage.prompt_tokens_details.cached_write_tokens && (
											<LogEntryDetailsView className="w-full" label="Cache Write Tokens" value={log.token_usage.prompt_tokens_details.cached_write_tokens ?? 0} />
										)}
										{log.token_usage.prompt_tokens_details.audio_tokens && (
											<LogEntryDetailsView className="w-full" label="Input Audio Tokens" value={log.token_usage.prompt_tokens_details.audio_tokens || "-"} />
										)}
									</>
								)}
								{log.token_usage?.completion_tokens_details && (
									<>
										{log.token_usage.completion_tokens_details.reasoning_tokens && (
											<LogEntryDetailsView className="w-full" label="Reasoning Tokens" value={log.token_usage.completion_tokens_details.reasoning_tokens || "-"} />
										)}
										{log.token_usage.completion_tokens_details.audio_tokens && (
											<LogEntryDetailsView className="w-full" label="Output Audio Tokens" value={log.token_usage.completion_tokens_details.audio_tokens || "-"} />
										)}
										{log.token_usage.completion_tokens_details.accepted_prediction_tokens && (
											<LogEntryDetailsView className="w-full" label="Accepted Prediction Tokens" value={log.token_usage.completion_tokens_details.accepted_prediction_tokens || "-"} />
										)}
										{log.token_usage.completion_tokens_details.rejected_prediction_tokens && (
											<LogEntryDetailsView className="w-full" label="Rejected Prediction Tokens" value={log.token_usage.completion_tokens_details.rejected_prediction_tokens || "-"} />
										)}
									</>
								)}
							</div>
						</div>
						{log.metadata && Object.keys(log.metadata).filter((k) => k !== "isAsyncRequest").length > 0 && (
							<>
								<DottedSeparator />
								<div className="space-y-4">
									<BlockHeader title="Metadata" />
									<div className="grid w-full grid-cols-3 items-start justify-between gap-4">
										{Object.entries(log.metadata)
											.filter(([key]) => key !== "isAsyncRequest")
											.map(([key, value]) => (
												<LogEntryDetailsView key={key} className="w-full" label={key} value={String(value)} />
											))}
									</div>
								</div>
							</>
						)}
					</>
				)}
			</div>
			{log.routing_engine_logs && (
				<CollapsibleBox title="Routing Decision Logs" onCopy={() => log.routing_engine_logs || ""}>
					<div className="custom-scrollbar max-h-[400px] overflow-y-auto px-6 py-2 font-mono text-xs break-words whitespace-pre-wrap">
						{log.routing_engine_logs}
					</div>
				</CollapsibleBox>
			)}
			{log.plugin_logs && <PluginLogsView pluginLogs={log.plugin_logs} />}
			{toolsParameter && (
				<CollapsibleBox title={`Tools (${log.params?.tools?.length || 0})`} onCopy={() => toolsParameter}>
					<CodeEditor className="z-0 w-full" shouldAdjustInitialHeight={true} maxHeight={450} wrap={true} code={toolsParameter} lang="json" readonly={true} options={{ scrollBeyondLastLine: false, lineNumbers: "off", alwaysConsumeMouseWheel: false }} />
				</CollapsibleBox>
			)}
			{log.params?.instructions && (
				<CollapsibleBox title="Instructions" onCopy={() => log.params?.instructions || ""}>
					<div className="custom-scrollbar max-h-[400px] overflow-y-auto px-6 py-2 font-mono text-xs break-words whitespace-pre-wrap">
						{log.params.instructions}
					</div>
				</CollapsibleBox>
			)}
			{(log.speech_input || log.speech_output) && <SpeechView speechInput={log.speech_input} speechOutput={log.speech_output} isStreaming={log.stream} />}
			{(log.transcription_input || log.transcription_output) && (
				<TranscriptionView transcriptionInput={log.transcription_input} transcriptionOutput={log.transcription_output} isStreaming={log.stream} />
			)}
			{(log.image_generation_input || log.image_generation_output) && (
				<ImageView imageInput={log.image_generation_input} imageOutput={log.image_generation_output} requestType={log.object} />
			)}
			{(log.video_generation_input || videoOutput || videoListOutput) && (
				<VideoView videoInput={log.video_generation_input} videoOutput={videoOutput} videoListOutput={videoListOutput} requestType={log.object} />
			)}
			{log.list_models_output && (
				<CollapsibleBox title={`List Models Output (${log.list_models_output.length})`} onCopy={() => JSON.stringify(log.list_models_output, null, 2)}>
					<CodeEditor className="z-0 w-full" shouldAdjustInitialHeight={true} maxHeight={450} wrap={true} code={JSON.stringify(log.list_models_output, null, 2)} lang="json" readonly={true} options={{ scrollBeyondLastLine: false, lineNumbers: "off", alwaysConsumeMouseWheel: false }} />
				</CollapsibleBox>
			)}
			{isPassthrough &&
				passthroughRequestBody && (
					<CollapsibleBox
						title="Request Body"
						onCopy={() => {
							try {
								return JSON.stringify(JSON.parse(passthroughRequestBody || ""), null, 2);
							} catch {
								return passthroughRequestBody || "";
							}
						}}
					>
						<CodeEditor className="z-0 w-full" shouldAdjustInitialHeight={true} maxHeight={450} wrap={true} code={(() => {
							try {
								return JSON.stringify(JSON.parse(passthroughRequestBody || ""), null, 2);
							} catch {
								return passthroughRequestBody || "";
							}
						})()} lang="json" readonly={true} options={{ scrollBeyondLastLine: false, lineNumbers: "off", alwaysConsumeMouseWheel: false }} />
					</CollapsibleBox>
				)}
			{log.input_history && log.input_history.length > 1 && (
				<>
					<div className="mt-4 w-full text-left text-sm font-medium">Conversation History</div>
					{log.input_history.slice(0, -1).map((message, index) => (
						<LogChatMessageView key={index} message={message} audioFormat={audioFormat} />
					))}
				</>
			)}
			{log.input_history && log.input_history.length > 0 && (
				<>
					<div className="mt-4 w-full text-left text-sm font-medium">Input</div>
					<LogChatMessageView message={log.input_history[log.input_history.length - 1]} audioFormat={audioFormat} />
				</>
			)}
			{log.responses_input_history && log.responses_input_history.length > 0 && (
				<>
					<div className="mt-4 w-full text-left text-sm font-medium">Input</div>
					<LogResponsesMessageView messages={log.responses_input_history} />
				</>
			)}
			{log.is_large_payload_request && !log.input_history?.length && !log.responses_input_history?.length && (
				<div className="mt-4 rounded-md border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-800 dark:bg-amber-950/50 dark:text-amber-300">
					Large payload request — input content was streamed directly to the provider and is not available for display.
					{log.raw_request && " A truncated preview is available in the Raw Request section below."}
				</div>
			)}
			{log.is_large_payload_response && !log.output_message && !log.responses_output?.length && log.status !== "processing" && (
				<div className="mt-4 rounded-md border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-800 dark:bg-amber-950/50 dark:text-amber-300">
					Large payload response — response content was streamed directly to the client and is not available for display.
					{log.raw_response && " A truncated preview is available in the Raw Response section below."}
				</div>
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
					{isPassthrough && passthroughResponseBody && (
						<CollapsibleBox
							title="Response Body"
							onCopy={() => {
								try {
									return JSON.stringify(JSON.parse(passthroughResponseBody || ""), null, 2);
								} catch {
									return passthroughResponseBody || "";
								}
							}}
						>
							<CodeEditor className="z-0 w-full" shouldAdjustInitialHeight={true} maxHeight={450} wrap={true} code={(() => {
								try {
									return JSON.stringify(JSON.parse(passthroughResponseBody || ""), null, 2);
								} catch {
									return passthroughResponseBody || "";
								}
							})()} lang="json" readonly={true} options={{ scrollBeyondLastLine: false, lineNumbers: "off", alwaysConsumeMouseWheel: false }} />
						</CollapsibleBox>
					)}
					{rawRequest && (
						<>
							<div className="mt-4 w-full text-left text-sm font-medium">
								Raw Request sent to <span className="font-medium capitalize">{log.provider}</span>
								{log.is_large_payload_request && <span className="ml-2 text-xs font-normal text-amber-600 dark:text-amber-400">(truncated preview)</span>}
							</div>
							<CollapsibleBox title={log.is_large_payload_request ? "Raw Request (Truncated)" : "Raw Request"} onCopy={() => formatJsonSafe(rawRequest)}>
								<CodeEditor className="z-0 w-full" shouldAdjustInitialHeight={true} maxHeight={450} wrap={true} code={formatJsonSafe(rawRequest)} lang="json" readonly={true} options={{ scrollBeyondLastLine: false, lineNumbers: "off", alwaysConsumeMouseWheel: false }} />
							</CollapsibleBox>
						</>
					)}
					{rawResponse && (
						<>
							<div className="mt-4 w-full text-left text-sm font-medium">
								Raw Response from <span className="font-medium capitalize">{log.provider}</span>
								{log.is_large_payload_response && <span className="ml-2 text-xs font-normal text-amber-600 dark:text-amber-400">(truncated preview)</span>}
							</div>
							<CollapsibleBox title={log.is_large_payload_response ? "Raw Response (Truncated)" : "Raw Response"} onCopy={() => formatJsonSafe(rawResponse)}>
								<CodeEditor className="z-0 w-full" shouldAdjustInitialHeight={true} maxHeight={450} wrap={true} code={formatJsonSafe(rawResponse)} lang="json" readonly={true} options={{ scrollBeyondLastLine: false, lineNumbers: "off", alwaysConsumeMouseWheel: false }} />
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
				</>
			)}
		</>
	);
}

const copyRequestBody = (log: LogEntry) => {
	try {
		const isChat = log.object === "chat.completion" || log.object === "chat.completion.chunk";
		const isResponses = log.object === "response" || log.object === "response.completion.chunk" || log.object === "realtime.turn";
		const isSpeech = log.object === "audio.speech" || log.object === "audio.speech.chunk";
		const isTextCompletion = log.object === "text.completion" || log.object === "text.completion.chunk";
		const isEmbedding = log.object === "list";

		const extractTextFromMessage = (message: any): string => {
			if (!message || !message.content) {
				return "";
			}
			if (typeof message.content === "string") {
				return message.content;
			}
			if (Array.isArray(message.content)) {
				return message.content.filter((block: any) => block && block.type === "text" && block.text).map((block: any) => block.text).join("\n");
			}
			return "";
		};

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

		const requestBody: any = {
			model: log.provider && log.model ? `${log.provider}/${log.model}` : log.model || "",
		};

		if (isChat && log.input_history && log.input_history.length > 0) {
			requestBody.messages = log.input_history;
		} else if (isResponses && log.responses_input_history && log.responses_input_history.length > 0) {
			requestBody.input = log.responses_input_history;
		} else if (isSpeech && log.speech_input) {
			requestBody.input = log.speech_input.input;
		} else if (isTextCompletion && log.input_history && log.input_history.length > 0) {
			const firstMessage = log.input_history[0];
			const prompt = extractTextFromMessage(firstMessage);
			if (prompt) {
				requestBody.prompt = prompt;
			}
		} else if (isEmbedding && log.input_history && log.input_history.length > 0) {
			const texts: string[] = [];
			for (const message of log.input_history) {
				const messageTexts = extractTextsFromMessage(message);
				texts.push(...messageTexts);
			}
			if (texts.length > 0) {
				requestBody.input = texts.length === 1 ? texts[0] : texts;
			}
		}

		if (log.params) {
			const paramsCopy = { ...log.params };
			delete paramsCopy.tools;
			delete paramsCopy.instructions;
			Object.assign(requestBody, paramsCopy);
		}

		if ((isChat || isResponses) && log.params?.tools && Array.isArray(log.params.tools) && log.params.tools.length > 0) {
			requestBody.tools = log.params.tools;
		}
		if (isResponses && log.params?.instructions) {
			requestBody.instructions = log.params.instructions;
		}

		const requestBodyJson = JSON.stringify(requestBody, null, 2);
		navigator.clipboard
			.writeText(requestBodyJson)
			.then(() => {
				toast.success("Request body copied to clipboard");
			})
			.catch(() => {
				toast.error("Failed to copy request body");
			});
	} catch {
		toast.error("Failed to copy request body");
	}
};
