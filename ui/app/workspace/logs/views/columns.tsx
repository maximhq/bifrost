import {
  formatCost,
  formatLatency,
  formatTokens,
} from "@/app/workspace/dashboard/utils/chartUtils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import {
  getProviderLabel,
  ProviderName,
  RequestTypeColors,
  RequestTypeLabels,
  Status,
  StatusBarColors,
} from "@/lib/constants/logs";
import {
  ChatMessageContent,
  LogEntry,
  ResponsesMessageContentBlock,
} from "@/lib/types/logs";
import { cn } from "@/lib/utils";
import { ColumnDef } from "@tanstack/react-table";
import { format, formatDistanceToNow } from "date-fns";
import { ArrowUpDown, Trash2 } from "lucide-react";

function getAssistantToolCallSummary(log?: LogEntry): string {
  const toolCalls = log?.output_message?.tool_calls || [];
  return toolCalls
    .map((toolCall) => {
      const name = toolCall?.function?.name;
      if (!name) {
        return "";
      }
      const argumentsText = toolCall?.function?.arguments?.trim();
      return argumentsText ? `${name}(${argumentsText})` : name;
    })
    .filter(Boolean)
    .join("\n");
}

function getMessageFromContent(content?: ChatMessageContent): string {
  if (content == undefined) {
    return "";
  }
  if (typeof content === "string") {
    return content;
  }
  let lastTextContentBlock = "";
  for (const block of content) {
    if (
      (block.type === "text" ||
        block.type === "input_text" ||
        block.type === "output_text") &&
      block.text
    ) {
      lastTextContentBlock = block.text;
    }
  }
  return lastTextContentBlock;
}

export function getRealtimeTurnMessages(log?: LogEntry): {
  tool?: string;
  user?: string;
  assistant?: string;
  assistantToolCall?: string;
} {
  const toolMessages =
    log?.input_history?.filter((message) => message.role === "tool") || [];
  const userMessages =
    log?.input_history?.filter((message) => message.role === "user") || [];
  return {
    tool:
      toolMessages
        .map((m) => getMessageFromContent(m.content))
        .filter(Boolean)
        .join("\n") || "",
    user:
      userMessages
        .map((m) => getMessageFromContent(m.content))
        .filter(Boolean)
        .join("\n") || "",
    assistant: log?.output_message
      ? getMessageFromContent(log.output_message.content)
      : "",
    assistantToolCall: getAssistantToolCallSummary(log),
  };
}

export function getMessage(log?: LogEntry) {
  if (log?.object === "list_models") {
    return "N/A";
  }
  if (log?.object === "realtime.turn") {
    const messages = getRealtimeTurnMessages(log);
    const parts = [
      messages.tool ? `Tool Result: ${messages.tool}` : "",
      messages.user ? `User: ${messages.user}` : "",
      messages.assistantToolCall
        ? `Assistant Tool Call: ${messages.assistantToolCall}`
        : "",
      messages.assistant ? `Assistant: ${messages.assistant}` : "",
    ].filter(Boolean);
    if (parts.length > 0) {
      return parts.join("\n");
    }
    return "";
  }
  if (log?.input_history && log.input_history.length > 0) {
    return getMessageFromContent(
      log.input_history[log.input_history.length - 1].content,
    );
  } else if (
    log?.responses_input_history &&
    log.responses_input_history.length > 0
  ) {
    let lastMessage =
      log.responses_input_history[log.responses_input_history.length - 1];
    let lastMessageContent = lastMessage.content;
    if (typeof lastMessageContent === "string") {
      return lastMessageContent;
    }
    let lastTextContentBlock = "";
    for (const block of (lastMessageContent ??
      []) as ResponsesMessageContentBlock[]) {
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
      } else if (
        lastMessage.output.type &&
        lastMessage.output.type === "computer_screenshot"
      ) {
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
  if (
    obj === "image_edit" ||
    obj === "image_edit_stream" ||
    obj === "image_variation"
  ) {
    return "Image file";
  }
  if (log?.content_summary) {
    return log.content_summary;
  }
  return "";
}

export function LogMessageCell({
  log,
  maxWidth = "max-w-[400px]",
}: {
  log: LogEntry;
  maxWidth?: string;
}) {
  const input = getMessage(log);
  const isLargePayload =
    log.is_large_payload_request || log.is_large_payload_response;
  const realtimeMessages =
    log.object === "realtime.turn" ? getRealtimeTurnMessages(log) : null;

  return (
    <div className="flex items-center gap-1.5">
      {isLargePayload && (
        <span
          className="shrink-0 rounded bg-amber-100 px-1.5 py-0.5 text-[10px] font-medium text-amber-700 dark:bg-amber-900/50 dark:text-amber-400"
          title="Large payload - streamed directly to provider"
        >
          LP
        </span>
      )}
      {realtimeMessages &&
      (realtimeMessages.tool ||
        realtimeMessages.user ||
        realtimeMessages.assistantToolCall ||
        realtimeMessages.assistant) ? (
        <div
          className={cn(maxWidth, "font-mono text-sm font-normal leading-5")}
          title={input || "-"}
        >
          {realtimeMessages.tool ? (
            <div className="truncate">Tool Result: {realtimeMessages.tool}</div>
          ) : null}
          {realtimeMessages.user ? (
            <div className="truncate">User: {realtimeMessages.user}</div>
          ) : null}
          {realtimeMessages.assistantToolCall ? (
            <div className="truncate">
              Assistant Tool Call: {realtimeMessages.assistantToolCall}
            </div>
          ) : null}
          {realtimeMessages.assistant ? (
            <div className="truncate">
              Assistant: {realtimeMessages.assistant}
            </div>
          ) : null}
        </div>
      ) : (
        <div
          className={cn(maxWidth, "truncate font-mono text-sm font-normal")}
          title={input || "-"}
        >
          {input ||
            (isLargePayload
              ? `Large payload ${log.is_large_payload_request && log.is_large_payload_response ? "request & response" : log.is_large_payload_request ? "request" : "response"}`
              : "-")}
        </div>
      )}
    </div>
  );
}

export const createColumns = (
  onDelete: (log: LogEntry) => void,
  hasDeleteAccess = true,
  metadataKeys: string[] = [],
): ColumnDef<LogEntry>[] => {
  const baseColumns: ColumnDef<LogEntry>[] = [
    {
      accessorKey: "status",
      header: "",
      size: 8,
      maxSize: 8,
      cell: ({ row }) => {
        const status = row.original.status as Status;
        return (
          <div
            className={`h-full min-h-[24px] w-1 rounded-sm ${StatusBarColors[status]}`}
          />
        );
      },
    },
    {
      accessorKey: "timestamp",
      header: ({ column }) => (
        <Button
          variant="ghost"
          data-testid="logs-time-sort-btn"
          onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}
        >
          Time
          <ArrowUpDown className="ml-2 h-4 w-4" />
        </Button>
      ),
      size: 180,
      cell: ({ row }) => {
        const timestamp = row.original.timestamp;
        const date = timestamp ? new Date(timestamp) : null;
        const isValid = date && date.toString() !== "Invalid Date";
        if (!isValid) {
          return <div className="truncate text-xs">N/A</div>;
        }
        return (
          <div className="flex flex-col leading-tight">
            <span className="font-mono text-xs tabular-nums">
              {format(date, "MMM dd  HH:mm:ss")}
            </span>
            <span className="text-muted-foreground text-[10.5px] tabular-nums">
              {formatDistanceToNow(date, { addSuffix: true })}
            </span>
          </div>
        );
      },
    },
    {
      id: "request_type",
      header: "Type",
      size: 180,
      cell: ({ row }) => {
        return (
          <Badge
            variant="outline"
            className={cn(
              "font-mono text-[11px] py-0.5 px-1.5 uppercase",
              RequestTypeColors[
                row.original.object as keyof typeof RequestTypeColors
              ],
            )}
          >
            {
              RequestTypeLabels[
                row.original.object as keyof typeof RequestTypeLabels
              ]
            }
          </Badge>
        );
      },
    },
    {
      accessorKey: "input",
      header: "Message",
      size: 250,
      cell: ({ row }) => <LogMessageCell log={row.original} />,
    },
    {
      accessorKey: "model",
      header: "Model",
      size: 220,
      cell: ({ row }) => {
        const provider = row.original.provider as ProviderName | undefined;
        const model = row.original.model;
        return (
          <div className="flex min-w-0 items-center gap-2">
            {provider ? (
              <RenderProviderIcon
                provider={provider as ProviderIconType}
                size="xs"
              />
            ) : null}
            <div className="flex min-w-0 flex-col leading-tight">
              <span className="truncate font-mono text-[12px]">
                {model || "N/A"}
              </span>
              <span className="text-muted-foreground truncate text-[10.5px]">
                {provider ? getProviderLabel(provider) : "N/A"}
              </span>
            </div>
          </div>
        );
      },
    },
    {
      accessorKey: "latency",
      header: ({ column }) => (
        <Button
          variant="ghost"
          data-testid="logs-latency-sort-btn"
          onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}
        >
          Latency
          <ArrowUpDown className="ml-2 h-4 w-4" />
        </Button>
      ),
      size: 170,
      cell: ({ row }) => {
        const latency = row.original.latency;
        if (latency === undefined || latency === null) {
          return <div className="pl-4 font-mono text-xs">N/A</div>;
        }
        const tone =
          latency >= 5000
            ? "bg-red-500"
            : latency >= 2000
              ? "bg-amber-500"
              : "bg-emerald-500";
        const pct = Math.min(100, (latency / 5000) * 100);
        return (
          <div className="flex items-center gap-2 pl-4">
            <span className="font-mono text-[12px] tabular-nums">
              {formatLatency(latency)}
            </span>
            <div className="relative h-1.5 w-[56px] overflow-hidden rounded-sm bg-zinc-200 dark:bg-zinc-700">
              <div
                className={cn(
                  "absolute inset-y-0 left-0 rounded-sm opacity-85",
                  tone,
                )}
                style={{ width: `${pct}%` }}
              />
            </div>
          </div>
        );
      },
    },
    {
      accessorKey: "tokens",
      header: ({ column }) => (
        <Button
          variant="ghost"
          data-testid="logs-tokens-sort-btn"
          onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}
        >
          Tokens
          <ArrowUpDown className="ml-2 h-4 w-4" />
        </Button>
      ),
      size: 190,
      cell: ({ row }) => {
        const tokenUsage = row.original.token_usage;
        if (!tokenUsage) {
          return <div className="pl-4 font-mono text-xs">N/A</div>;
        }
        const prompt = tokenUsage.prompt_tokens ?? 0;
        const completion = tokenUsage.completion_tokens ?? 0;
        const total = tokenUsage.total_tokens ?? 0;
        const hasSplit =
          tokenUsage.completion_tokens != null &&
          tokenUsage.prompt_tokens != null;
        const splitBase = prompt + completion || 1;
        const inPct = (prompt / splitBase) * 100;
        return (
          <div className="flex flex-col items-start gap-0.5 pl-4 leading-tight">
            <div className="flex items-center gap-2">
              <span className="font-mono text-[12px] tabular-nums">
                {formatTokens(total)}
              </span>
              {hasSplit && (
                <div className="flex h-1.5 w-[64px] overflow-hidden rounded-sm">
                  <div className="bg-blue-400" style={{ width: `${inPct}%` }} />
                  <div className="flex-1 bg-violet-400" />
                </div>
              )}
            </div>
            {hasSplit && (
              <div className="text-muted-foreground font-mono text-[10.5px] tabular-nums">
                <span className="text-blue-500">{formatTokens(prompt)}</span>
                <span> / </span>
                <span className="text-violet-500">
                  {formatTokens(completion)}
                </span>
              </div>
            )}
          </div>
        );
      },
    },
    {
      accessorKey: "cost",
      header: ({ column }) => (
        <Button
          variant="ghost"
          data-testid="logs-cost-sort-btn"
          onClick={() => column.toggleSorting(column.getIsSorted() === "asc")}
        >
          Cost
          <ArrowUpDown className="ml-2 h-4 w-4" />
        </Button>
      ),
      size: 120,
      cell: ({ row }) => {
        if (row.original.cost == null) {
          return <div className="pl-4 font-mono text-[12px]">N/A</div>;
        }
        return (
          <div className="pl-4 font-mono text-sm tabular-nums">
            {formatCost(row.original.cost)}
          </div>
        );
      },
    },
  ];

  const metadataColumns: ColumnDef<LogEntry>[] = metadataKeys.map((key) => ({
    id: `metadata_${key}`,
    header: key.charAt(0).toUpperCase() + key.slice(1),
    size: 126,
    cell: ({ row }) => {
      const value = row.original.metadata?.[key];
      return (
        <div className="max-w-[150px] truncate font-mono text-xs">
          {value ?? "-"}
        </div>
      );
    },
  }));

  const actionsColumn: ColumnDef<LogEntry> = {
    id: "actions",
    size: 72,
    cell: ({ row }) => {
      const log = row.original;
      return (
        <Button
          variant="outline"
          size="icon"
          data-testid="log-delete-btn"
          aria-label="Delete log"
          className="text-secondary-foreground/30 hover:bg-destructive/10 hover:text-destructive border-destructive/10"
          onClick={() => onDelete(log)}
          disabled={!hasDeleteAccess}
        >
          <Trash2 strokeWidth={1.5} />
        </Button>
      );
    },
  };

  return [...baseColumns, ...metadataColumns, actionsColumn];
};
