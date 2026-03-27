"use client";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderName, RequestTypeColors, RequestTypeLabels, Status, StatusBarColors } from "@/lib/constants/logs";
import { ChatMessageContent, LogEntry, ResponsesMessageContentBlock } from "@/lib/types/logs";
import { cn } from "@/lib/utils";
import { ColumnDef } from "@tanstack/react-table";
import { ArrowUpDown, Trash2 } from "lucide-react";
import moment from "moment";

function getMessageFromContent(content?: ChatMessageContent): string {
	if (content == undefined) {
		return "";
	}
	if (typeof content === "string") {
		return content;
	}
	let lastTextContentBlock = "";
	for (const block of content) {
		if (block.type === "text" && block.text) {
			lastTextContentBlock = block.text;
		}
	}
	return lastTextContentBlock;
}

export function getRealtimeTurnMessages(log?: LogEntry): { tool?: string; user?: string; assistant?: string } {
	const toolMessage = log?.input_history?.find((message) => message.role === "tool");
	const userMessage = log?.input_history?.find((message) => message.role === "user");
	return {
		tool: toolMessage ? getMessageFromContent(toolMessage.content) : "",
		user: userMessage ? getMessageFromContent(userMessage.content) : "",
		assistant: log?.output_message ? getMessageFromContent(log.output_message.content) : "",
	};
}

export function getMessage(log?: LogEntry) {
	if (log?.object === "list_models") {
		return "N/A";
	}
	if (log?.object === "realtime.turn") {
		const messages = getRealtimeTurnMessages(log);
		const parts = [
			messages.tool ? `Tool: ${messages.tool}` : "",
			messages.user ? `User: ${messages.user}` : "",
			messages.assistant ? `Assistant: ${messages.assistant}` : "",
		].filter(Boolean);
		if (parts.length > 0) {
			return parts.join("\n");
		}
		return "";
	}
	if (log?.input_history && log.input_history.length > 0) {
		return getMessageFromContent(log.input_history[log.input_history.length - 1].content);
	} else if (log?.responses_input_history && log.responses_input_history.length > 0) {
		let lastMessage = log.responses_input_history[log.responses_input_history.length - 1];
		let lastMessageContent = lastMessage.content;
		if (typeof lastMessageContent === "string") {
			return lastMessageContent;
		}
		let lastTextContentBlock = "";
		for (const block of (lastMessageContent ?? []) as ResponsesMessageContentBlock[]) {
			if (block.text && block.text !== "") {
				lastTextContentBlock = block.text;
			}
		}
		// If no content found in content field, check output field for Responses API
		if (!lastTextContentBlock && lastMessage.output) {
			// Handle output field - it could be a string, an array of content blocks, or a computer tool call output data
			if (typeof lastMessage.output === "string") {
				return lastMessage.output;
			} else if (Array.isArray(lastMessage.output)) {
				return lastMessage.output.map((block) => block.text).join("\n");
			} else if (lastMessage.output.type && lastMessage.output.type === "computer_screenshot") {
				return lastMessage.output.image_url;
			}
		}
		return lastTextContentBlock ?? "";
	} else if (log?.output_message) {
		return getMessageFromContent(log.output_message.content);
	} else if (log?.speech_input) {
		return log.speech_input.input;
	} else if (log?.transcription_input) {
		return "Audio file";
	} else if (log?.image_generation_input?.prompt) {
		return log.image_generation_input.prompt;
	}
	const obj = log?.object as string | undefined;
	if (obj === "image_edit" || obj === "image_edit_stream" || obj === "image_variation") {
		return "Image file";
	}
	return "";
}

export function LogMessageCell({ log, maxWidth = "max-w-[400px]" }: { log: LogEntry; maxWidth?: string }) {
	const input = getMessage(log);
	const isLargePayload = log.is_large_payload_request || log.is_large_payload_response;
	const realtimeMessages = log.object === "realtime.turn" ? getRealtimeTurnMessages(log) : null;

	return (
		<div className="flex items-center gap-1.5">
			{isLargePayload && (
				<span className="shrink-0 rounded bg-amber-100 px-1.5 py-0.5 text-[10px] font-medium text-amber-700 dark:bg-amber-900/50 dark:text-amber-400" title="Large payload - streamed directly to provider">
					LP
				</span>
			)}
			{realtimeMessages && (realtimeMessages.tool || realtimeMessages.user || realtimeMessages.assistant) ? (
				<div className={cn(maxWidth, "font-mono text-sm font-normal leading-5")} title={input || "-"}>
					{realtimeMessages.tool ? <div className="truncate">Tool: {realtimeMessages.tool}</div> : null}
					{realtimeMessages.user ? <div className="truncate">User: {realtimeMessages.user}</div> : null}
					{realtimeMessages.assistant ? <div className="truncate">Assistant: {realtimeMessages.assistant}</div> : null}
				</div>
			) : (
				<div className={cn(maxWidth, "truncate font-mono text-sm font-normal")} title={input || "-"}>
					{input || (isLargePayload
					? `Large payload ${log.is_large_payload_request && log.is_large_payload_response ? "request & response" : log.is_large_payload_request ? "request" : "response"}`
					: "-")}
				</div>
			)}
		</div>
	);
}

export const createColumns = (onDelete: (log: LogEntry) => void, hasDeleteAccess = true): ColumnDef<LogEntry>[] => {
	const baseColumns: ColumnDef<LogEntry>[] = [
	{
		accessorKey: "status",
		header: "",
		size: 8,
		maxSize: 8,
		cell: ({ row }) => {
			const status = row.original.status as Status;
			return <div className={`h-full min-h-[24px] w-1 rounded-sm ${StatusBarColors[status]}`} />;
		},
	},
	{
		accessorKey: "timestamp",
		header: ({ column }) => (
			<Button variant="ghost" onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}>
				Time
				<ArrowUpDown className="ml-2 h-4 w-4" />
			</Button>
		),
		cell: ({ row }) => {
			const timestamp = row.original.timestamp;
			return <div className="text-xs">{moment(timestamp).format("YYYY-MM-DD hh:mm:ss A (Z)")}</div>;
		},
	},
	{
		id: "request_type",
		header: "Type",
		cell: ({ row }) => {
			return (
				<Badge variant="outline" className={`${RequestTypeColors[row.original.object as keyof typeof RequestTypeColors]} text-xs`}>
					{RequestTypeLabels[row.original.object as keyof typeof RequestTypeLabels]}
				</Badge>
			);
		},
	},
	{
		accessorKey: "input",
		header: "Message",
		cell: ({ row }) => <LogMessageCell log={row.original} />,
	},
	{
		accessorKey: "provider",
		header: "Provider",
		cell: ({ row }) => {
			const provider = row.original.provider as ProviderName;
			return (
				<Badge variant="secondary" className={`font-mono text-xs uppercase`}>
					<RenderProviderIcon provider={provider as ProviderIconType} size="sm" />
					{provider}
				</Badge>
			);
		},
	},
	{
		accessorKey: "model",
		header: "Model",
		cell: ({ row }) => <div className="max-w-[120px] truncate font-mono text-xs font-normal">{row.original.model || "N/A"}</div>,
	},
	{
		accessorKey: "latency",
		header: ({ column }) => (
			<Button variant="ghost" onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}>
				Latency
				<ArrowUpDown className="ml-2 h-4 w-4" />
			</Button>
		),
		cell: ({ row }) => {
			const latency = row.original.latency;
			return (
				<div className="pl-4 font-mono text-sm">{latency === undefined || latency === null ? "N/A" : `${latency.toLocaleString()}ms`}</div>
			);
		},
	},
	{
		accessorKey: "tokens",
		header: ({ column }) => (
			<Button variant="ghost" onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}>
				Tokens
				<ArrowUpDown className="ml-2 h-4 w-4" />
			</Button>
		),
		cell: ({ row }) => {
			const tokenUsage = row.original.token_usage;
			if (!tokenUsage) {
				return <div className="pl-4 font-mono text-sm">N/A</div>;
			}

			return (
				<div className="pl-4 text-sm">
					<div className="font-mono">
						{tokenUsage.total_tokens.toLocaleString()}{" "}
						{tokenUsage.completion_tokens != null && tokenUsage.prompt_tokens != null
							? `(${tokenUsage.prompt_tokens.toLocaleString()}+${tokenUsage.completion_tokens.toLocaleString()})`
							: ""}
					</div>
				</div>
			);
		},
	},
	{
		accessorKey: "cost",
		header: ({ column }) => (
			<Button variant="ghost" onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}>
				Cost
				<ArrowUpDown className="ml-2 h-4 w-4" />
			</Button>
		),
		cell: ({ row }) => {
			if (!row.original.cost) {
				return <div className="pl-4 font-mono text-xs">N/A</div>;
			}

			return (
				<div className="pl-4 text-xs">
					<div className="font-mono">{row.original.cost?.toFixed(4)}</div>
				</div>
			);
		},
	},
	];

	const actionsColumn: ColumnDef<LogEntry> = {
		id: "actions",
		cell: ({ row }) => {
			const log = row.original;
			return (
				<Button variant="outline" size="icon" onClick={() => onDelete(log)} disabled={!hasDeleteAccess}>
					<Trash2 />
				</Button>
			);
		},
	};

	return [...baseColumns, actionsColumn];
};
