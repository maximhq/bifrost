import {
  formatCost,
  formatLatency,
  formatTokens,
} from "@/app/workspace/dashboard/utils/chartUtils";
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import {
  ProviderIconType,
  RenderProviderIcon,
  RoutingEngineUsedIcons,
} from "@/lib/constants/icons";
import {
  RequestTypeColors,
  RequestTypeLabels,
  RoutingEngineUsedColors,
  RoutingEngineUsedLabels,
  Status
} from "@/lib/constants/logs";
import { LogEntry, ResponsesMessage } from "@/lib/types/logs";
import { cn } from "@/lib/utils";
import { Link } from "@tanstack/react-router";
import { addMilliseconds, format } from "date-fns";
import {
  AlertCircle,
  ChevronDown,
  Clipboard,
  Loader2,
  MoreVertical,
  Trash2,
  Wrench,
} from "lucide-react";
import { useState, type ReactNode } from "react";
import { toast } from "sonner";
import BlockHeader from "../views/blockHeader";
import CollapsibleBox from "../views/collapsibleBox";
import ImageView from "../views/imageView";
import LogChatMessageView from "../views/logChatMessageView";
import LogEntryDetailsView from "../views/logEntryDetailsView";
import PluginLogsView from "../views/pluginLogsView";
import SpeechView from "../views/speechView";
import TranscriptionView from "../views/transcriptionView";
import VideoView from "../views/videoView";

const extractResponsesText = (msg: ResponsesMessage): string => {
  if (msg.type === "reasoning") {
    const summaryText = (msg.summary ?? [])
      .map((s) => s.text)
      .filter(Boolean)
      .join("\n")
      .trim();
    if (summaryText) return summaryText;
    if (msg.encrypted_content) return msg.encrypted_content;
  }
  if (typeof msg.content === "string") return msg.content;
  if (Array.isArray(msg.content)) {
    return msg.content
      .filter(
        (b: any) =>
          b &&
          b.text &&
          (b.type === "input_text" ||
            b.type === "output_text" ||
            b.type === "reasoning_text" ||
            b.type === "refusal"),
      )
      .map((b: any) => b.text as string)
      .join("\n");
  }
  if (typeof (msg as any).arguments === "string")
    return (msg as any).arguments as string;
  return "";
};

const getResponsesRole = (msg: ResponsesMessage): MessageRole => {
  if (msg.type === "reasoning") return "reasoning";
  if (
    msg.type &&
    (msg.type.endsWith("_call") ||
      msg.type.endsWith("_call_output") ||
      msg.type === "mcp_list_tools" ||
      msg.type === "mcp_approval_request" ||
      msg.type === "mcp_approval_responses")
  ) {
    return "tool";
  }
  const r = msg.role;
  if (r === "user") return "user";
  if (r === "assistant") return "assistant";
  if (r === "system" || r === "developer") return "system";
  return "assistant";
};

const extractMessageText = (message: any): string => {
  if (!message || message.content == null) return "";
  if (typeof message.content === "string") return message.content;
  if (Array.isArray(message.content)) {
    return message.content
      .filter(
        (block: any) =>
          block &&
          (block.type === "text" ||
            block.type === "input_text" ||
            block.type === "output_text") &&
          block.text,
      )
      .map((block: any) => block.text)
      .join("\n");
  }
  return "";
};

const formatJsonSafe = (str: string | undefined): string => {
  try {
    return JSON.stringify(JSON.parse(str || ""), null, 2);
  } catch {
    return str || "";
  }
};

const formatToolChoice = (value: unknown): string => {
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
};

// Helper to detect passthrough operations
const isPassthroughOperation = (object: string) =>
  object === "passthrough" || object === "passthrough_stream";

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

const statusPillStyles: Record<string, string> = {
  success:
    "bg-green-50 text-green-700 border-green-200 dark:bg-green-950/40 dark:text-green-400 dark:border-green-900",
  error:
    "bg-red-50 text-red-700 border-red-200 dark:bg-red-950/40 dark:text-red-400 dark:border-red-900",
  processing:
    "bg-blue-50 text-blue-700 border-blue-200 dark:bg-blue-950/40 dark:text-blue-400 dark:border-blue-900",
  cancelled:
    "bg-gray-50 text-gray-700 border-gray-200 dark:bg-gray-900/40 dark:text-gray-400 dark:border-gray-800",
};
const statusDotStyles: Record<string, string> = {
  success: "bg-green-500",
  error: "bg-red-500",
  processing: "bg-blue-500",
  cancelled: "bg-gray-400",
};

function StatusPill({ status }: { status: Status }) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-sm border px-2 py-0.5 text-[11px] font-semibold uppercase",
        statusPillStyles[status] ?? statusPillStyles.cancelled,
      )}
    >
      <span
        className={cn(
          "h-1.5 w-1.5 rounded-sm",
          statusDotStyles[status] ?? statusDotStyles.cancelled,
        )}
      />
      {status}
    </span>
  );
}

function HeroStat({
  label,
  value,
  sub,
  mono = false,
  valueClass,
  hasRightBorder = false,
}: {
  label: string;
  value: ReactNode;
  sub?: ReactNode;
  mono?: boolean;
  valueClass?: string;
  hasRightBorder?: boolean;
}) {
  return (
    <div
      className={cn(
        "border-border/70 border-b px-5 py-3 md:border-b-0",
        hasRightBorder && "md:border-r",
      )}
    >
      <div className="text-muted-foreground text-[10.5px] font-semibold tracking-wider uppercase">
        {label}
      </div>
      <div
        className={cn(
          "mt-0.5 truncate text-[18px] font-semibold tabular-nums",
          mono && "font-mono text-[15px]",
          valueClass,
        )}
      >
        {value}
      </div>
      {sub ? (
        <div className="text-muted-foreground mt-0.5 truncate text-[11px]">
          {sub}
        </div>
      ) : null}
    </div>
  );
}

function CopyInlineButton({ text }: { text: string }) {
  const { copy } = useCopyToClipboard({ successMessage: "Copied" });
  return (
    <button
      type="button"
      onClick={(e) => {
        e.stopPropagation();
        copy(text);
      }}
      className="text-muted-foreground hover:bg-muted hover:text-foreground inline-flex h-6 w-6 items-center justify-center rounded-sm transition"
      aria-label="Copy"
    >
      <Clipboard className="h-3.5 w-3.5" />
    </button>
  );
}

type MessageRole = "system" | "user" | "assistant" | "reasoning" | "tool";
const messageToneClass: Record<MessageRole, string> = {
  system: "bg-zinc-50 border-zinc-200 dark:bg-zinc-900/40 dark:border-zinc-800",
  user: "bg-blue-50/60 border-blue-200 dark:bg-blue-950/30 dark:border-blue-900",
  assistant: "bg-white border-zinc-200 dark:bg-zinc-900 dark:border-zinc-800",
  reasoning:
    "bg-violet-50/70 border-violet-200 dark:bg-violet-950/30 dark:border-violet-900",
  tool: "bg-amber-50/70 border-amber-200 dark:bg-amber-950/30 dark:border-amber-900",
};
const messageDotClass: Record<MessageRole, string> = {
  system: "bg-zinc-400",
  user: "bg-blue-500",
  assistant: "bg-zinc-900 dark:bg-zinc-100",
  reasoning: "bg-violet-500",
  tool: "bg-amber-500",
};
const messageRoleLabel: Record<MessageRole, string> = {
  system: "System",
  user: "User",
  assistant: "Assistant",
  reasoning: "Reasoning",
  tool: "Tool",
};

function CollapsibleCode({
  text,
  preview = 3,
  lang,
  mono = true,
}: {
  text: string;
  preview?: number;
  lang?: string;
  mono?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const lines = text.split("\n");
  const shown = open ? lines : lines.slice(0, preview);
  const hasMore = lines.length > preview;
  const moreCount = lines.length - preview;
  return (
    <>
      {mono ? (
        <pre className="font-mono text-[12.5px] leading-[1.6] break-words whitespace-pre-wrap">
          {shown.join("\n")}
        </pre>
      ) : (
        <div className="text-[13px] leading-relaxed break-words whitespace-pre-wrap">
          {shown.join("\n")}
        </div>
      )}
      {hasMore && (
        <div className="mt-1.5 flex items-center justify-between">
          <button
            type="button"
            onClick={() => setOpen((o) => !o)}
            className="text-primary inline-flex items-center gap-1 text-[11.5px] font-medium hover:underline"
          >
            {open ? "Show less" : `Show ${moreCount} more lines`}
            <ChevronDown
              className={cn(
                "h-3 w-3 transition-transform",
                open && "rotate-180",
              )}
            />
          </button>
          <span className="text-muted-foreground font-mono text-[10.5px]">
            {lines.length} lines{lang ? ` · ${lang}` : ""}
          </span>
        </div>
      )}
    </>
  );
}

function MessageRow({
  role,
  meta,
  children,
  last = false,
}: {
  role: MessageRole;
  meta?: string;
  children: ReactNode;
  last?: boolean;
}) {
  return (
    <div className="flex gap-3">
      <div className="flex flex-col items-center pt-1.5">
        <span className={cn("h-2 w-2 rounded-sm", messageDotClass[role])} />
        {!last && <div className="bg-border my-1 w-px flex-1" />}
      </div>
      <div className="min-w-0 flex-1 pb-4">
        <div className="mb-1 flex items-center gap-2">
          <span className="text-foreground text-[11.5px] font-semibold">
            {messageRoleLabel[role]}
          </span>
          {meta ? (
            <span className="text-muted-foreground text-[11px]">{meta}</span>
          ) : null}
        </div>
        <div
          className={cn(
            "rounded-sm border p-3 text-[13px] leading-relaxed",
            messageToneClass[role],
          )}
        >
          {children}
        </div>
      </div>
    </div>
  );
}

interface LogDetailViewProps {
  log: LogEntry | null;
  resolvedSelectedPromptName?: string; // Current prompt name from prompt-repo when `selected_prompt_id` is set; falls back to stored log name
  loading?: boolean;
  handleDelete?: (log: LogEntry) => void;
  onClose?: () => void;
  headerAction?: ReactNode;
  onFilterByParentRequestId?: (parentRequestId: string) => void;
}

export function LogDetailView({
  log,
  resolvedSelectedPromptName,
  loading = false,
  handleDelete,
  onClose,
  headerAction,
  onFilterByParentRequestId,
}: LogDetailViewProps) {
  const { copy: copyRequestId } = useCopyToClipboard({
    successMessage: "Request ID copied",
  });
  const { copy: copyBody } = useCopyToClipboard({
    successMessage: "Request body copied to clipboard",
    errorMessage: "Failed to copy request body",
  });

  if (!log) return null;

  const selectedPromptDisplayName =
    resolvedSelectedPromptName ?? log.selected_prompt_name ?? "";

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

  const audioFormat =
    (log.params as any)?.audio?.format ||
    (log.params as any)?.extra_params?.audio?.format ||
    undefined;
  const rawRequest = log.raw_request;
  const rawResponse = log.raw_response;
  const passthroughRequestBody = log.passthrough_request_body;
  const passthroughResponseBody = log.passthrough_response_body;
  const videoOutput =
    log.video_generation_output ||
    log.video_retrieve_output ||
    log.video_download_output;
  const videoListOutput = log.video_list_output;
  const pluginLogCount = (() => {
    if (!log.plugin_logs) return 0;
    try {
      const parsed = JSON.parse(log.plugin_logs);
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
        return Object.values(parsed).reduce<number>((sum, v) => sum + (Array.isArray(v) ? v.length : 0), 0);
      }
    } catch {}
    return 0;
  })();

  return loading ? (
    <div className="flex h-full items-center justify-center">
      <Loader2 className="text-muted-foreground h-6 w-6 animate-spin" />
    </div>
  ) : (
    <>
      {/* Breadcrumb header with actions */}
      <div className="flex items-center justify-between gap-3">
        <div className="text-muted-foreground flex items-center gap-2 text-sm">
          {headerAction}
          <span className="text-foreground font-medium">Request details</span>
        </div>
        {handleDelete && onClose ? (
          <AlertDialog>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  variant="ghost"
                  className="size-8"
                  type="button"
                  data-testid="logdetails-actions-button"
                >
                  <MoreVertical className="h-3 w-3" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem
                  onClick={() => copyRequestBody(log, copyBody)}
                  data-testid="logdetails-copy-request-body-button"
                >
                  <Clipboard className="h-4 w-4" />
                  Copy request body
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                <AlertDialogTrigger asChild>
                  <DropdownMenuItem
                    variant="destructive"
                    data-testid="logdetails-delete-item"
                  >
                    <Trash2 className="h-4 w-4" />
                    Delete log
                  </DropdownMenuItem>
                </AlertDialogTrigger>
              </DropdownMenuContent>
            </DropdownMenu>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>
                  Are you sure you want to delete this log?
                </AlertDialogTitle>
                <AlertDialogDescription>
                  This action cannot be undone. This will permanently delete the
                  log entry.
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel data-testid="logdetails-delete-cancel-button">
                  Cancel
                </AlertDialogCancel>
                <AlertDialogAction
                  data-testid="logdetails-delete-confirm-button"
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
      <div className="border border-border rounded-sm">
        <div className="flex items-start justify-between gap-6 px-5 pt-5 pb-4">
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-2">
              <StatusPill status={log.status as Status} />
              <Badge
                variant="outline"
                className={cn(
                  "rounded-sm px-2 py-0.5 font-medium",
                  RequestTypeColors[
                    log.object as keyof typeof RequestTypeColors
                  ] ?? "bg-gray-100 text-gray-800",
                )}
              >
                {RequestTypeLabels[
                  log.object as keyof typeof RequestTypeLabels
                ] ?? log.object}
              </Badge>
              {log.routing_rule && (
                <Badge
                  variant="outline"
                  className="bg-card text-muted-foreground rounded-sm px-2 py-0.5 font-normal"
                >
                  rule: {log.routing_rule.name}
                </Badge>
              )}
              {log.metadata?.isAsyncRequest ? (
                <Badge
                  variant="outline"
                  className="rounded-sm bg-teal-100 px-2 py-0.5 text-teal-800 dark:bg-teal-900 dark:text-teal-200"
                >
                  Async
                </Badge>
              ) : null}
              {(log.is_large_payload_request ||
                log.is_large_payload_response) && (
                <Badge
                  variant="outline"
                  className="rounded-sm border-amber-300 bg-amber-50 px-2 py-0.5 text-amber-700 dark:border-amber-600 dark:bg-amber-950 dark:text-amber-400"
                >
                  Large Payload
                </Badge>
              )}
            </div>
            <div className="mt-3 flex items-center gap-2">
              <div className="text-muted-foreground text-[10.5px] font-semibold tracking-wider uppercase">
                Request
              </div>
              <code className="text-foreground truncate font-mono text-[13px]">
                {log.id || "—"}
              </code>
              {log.id ? <CopyInlineButton text={log.id} /> : null}
            </div>
            {(log.routing_rule || log.selected_key) && (
              <div className="text-muted-foreground mt-1 text-[12px]">
                {log.routing_rule ? (
                  <>
                    matched rule{" "}
                    <span className="text-foreground font-medium">
                      &ldquo;{log.routing_rule.name}&rdquo;
                    </span>
                  </>
                ) : null}
                {log.routing_rule && log.selected_key ? " · " : ""}
                {log.selected_key ? (
                  <>
                    key{" "}
                    <span className="text-foreground font-mono">
                      {log.selected_key.name}
                    </span>
                  </>
                ) : null}
              </div>
            )}
          </div>
          <div className="flex shrink-0 items-center gap-1.5 rounded-sm border bg-white px-2 py-1 text-[12px] font-medium dark:bg-zinc-900">
            <RenderProviderIcon
              provider={log.provider as ProviderIconType}
              size="xs"
            />
            <span className="uppercase">{log.provider}</span>
          </div>
        </div>
        <div className="border-border grid grid-cols-2 border-t md:grid-cols-5">
          <HeroStat
            label="Latency"
            valueClass="text-primary"
            value={
              log.latency == null || isNaN(log.latency)
                ? "—"
                : formatLatency(log.latency)
            }
            sub={(() => {
              if (!log.timestamp) return "";
              const start = new Date(log.timestamp);
              if (isNaN(start.getTime())) return "";
              const startStr = format(start, "HH:mm:ss");
              if (log.latency == null || isNaN(log.latency)) return startStr;
              return `${startStr} → ${format(addMilliseconds(start, log.latency), "HH:mm:ss")}`;
            })()}
            hasRightBorder
          />
          <HeroStat
            label="Model"
            mono
            value={log.model || "—"}
            sub={log.provider?.toLowerCase() || ""}
            hasRightBorder
          />
          <HeroStat
            label="Tokens in / out"
            mono
            value={
              log.token_usage
                ? `${formatTokens(log.token_usage.prompt_tokens ?? 0)} / ${formatTokens(log.token_usage.completion_tokens ?? 0)}`
                : "—"
            }
            sub={
              log.token_usage
                ? `total ${formatTokens(log.token_usage.total_tokens ?? 0)}${
                    log.token_usage.completion_tokens_details?.reasoning_tokens
                      ? ` · reasoning ${formatTokens(log.token_usage.completion_tokens_details.reasoning_tokens)}`
                      : ""
                  }`
                : "—"
            }
            hasRightBorder
          />
          <HeroStat
            label="Cost"
            value={log.cost != null ? formatCost(log.cost) : "—"}
            sub={
              log.cost != null && log.token_usage?.total_tokens
                ? `≈ ${((log.cost / log.token_usage.total_tokens) * 1000).toFixed(6)}＄ per 1k`
                : ""
            }
            hasRightBorder
          />
          <HeroStat
            label="Tools available"
            value={(log.params?.tools?.length ?? 0).toString()}
            sub={
              (log.params as any)?.tool_choice != null
                ? `choice: ${formatToolChoice((log.params as any).tool_choice)}`
                : ""
            }
          />
        </div>

        {/* Timeline */}
        {log.latency != null &&
          !isNaN(log.latency) &&
          log.latency > 0 &&
          (() => {
            const total = log.latency;
            const tone =
              total >= 5000
                ? "bg-red-400"
                : total >= 2000
                  ? "bg-amber-400"
                  : "bg-blue-400";
            return (
              <div className="border-border border-t px-5 py-3">
                <div className="text-muted-foreground mb-1.5 flex items-center justify-between text-[11px]">
                  <span>timeline</span>
                  <span className="font-mono tabular-nums">
                    {formatLatency(total)}
                  </span>
                </div>
                <div className="relative h-4 overflow-hidden rounded-sm bg-zinc-100 dark:bg-zinc-800">
                  <div
                    className={cn("absolute inset-y-0 left-0 opacity-80", tone)}
                    style={{ width: "100%" }}
                    title="total latency"
                  />
                </div>
                <div className="text-muted-foreground mt-2 flex flex-wrap gap-4 text-[11px]">
                  <span className="inline-flex items-center gap-1.5">
                    <span className={cn("h-2 w-2 rounded-sm", tone)} /> total{" "}
                    {formatLatency(total)}
                  </span>
                </div>
              </div>
            );
          })()}
      </div>
      <details className="group bg-card rounded-sm border" open={false}>
        <summary className="hover:bg-muted/30 flex cursor-pointer items-center justify-between px-4 py-2.5 text-sm transition">
          <span className="text-foreground font-medium">More details</span>
          <span className="text-muted-foreground flex items-center gap-2 text-xs">
            <span className="hidden md:inline">
              timings, request meta, tokens, caching, metadata
            </span>
            <ChevronDown className="h-3.5 w-3.5 transition-transform group-open:rotate-180" />
          </span>
        </summary>
        <div className="space-y-4 border-t px-6 py-4">
          <div className="space-y-4">
            <BlockHeader title="Timings" />
            <div className="grid w-full grid-cols-3 items-center justify-between gap-4">
              <LogEntryDetailsView
                className="w-full"
                label="Start Timestamp"
                value={(() => {
                  const d = log.timestamp ? new Date(log.timestamp) : null;
                  return d && !isNaN(d.getTime())
                    ? format(d, "yyyy-MM-dd hh:mm:ss aa")
                    : "N/A";
                })()}
              />
              <LogEntryDetailsView
                className="w-full"
                label="End Timestamp"
                value={(() => {
                  const d = log.timestamp ? new Date(log.timestamp) : null;
                  return d && !isNaN(d.getTime())
                    ? format(
                        addMilliseconds(d, log.latency || 0),
                        "yyyy-MM-dd hh:mm:ss aa",
                      )
                    : "N/A";
                })()}
              />
              <LogEntryDetailsView
                className="w-full"
                label="Latency"
                value={
                  log.latency == null || isNaN(log.latency) ? (
                    "N/A"
                  ) : (
                    <div>{log.latency.toFixed(2)}ms</div>
                  )
                }
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
                    <RenderProviderIcon
                      provider={log.provider as ProviderIconType}
                      size="sm"
                    />
                    {log.provider}
                  </Badge>
                }
              />
              {!isContainer && (
                <LogEntryDetailsView
                  className="w-full"
                  label="Model"
                  value={log.model}
                />
              )}
              {!isContainer && log.alias && (
                <LogEntryDetailsView
                  className="w-full"
                  label="Alias"
                  value={log.alias}
                />
              )}
              <LogEntryDetailsView
                className="w-full"
                label="Type"
                value={
                  <div
                    className={`${RequestTypeColors[log.object as keyof typeof RequestTypeColors] ?? "bg-gray-100 text-gray-800"} rounded-sm px-3 py-1`}
                  >
                    {RequestTypeLabels[
                      log.object as keyof typeof RequestTypeLabels
                    ] ??
                      log.object ??
                      "unknown"}
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
                            className="text-primary hover:text-primary/80 block min-w-0 cursor-pointer font-normal break-all underline-offset-2 hover:underline"
                            onClick={() =>
                              onFilterByParentRequestId(
                                log.parent_request_id as string,
                              )
                            }
                          >
                            {log.parent_request_id}
                          </code>
                        </TooltipTrigger>
                        <TooltipContent sideOffset={6}>
                          Filter this session
                        </TooltipContent>
                      </Tooltip>
                    ) : (
                      <code className="block min-w-0 font-normal break-all">
                        {log.parent_request_id}
                      </code>
                    )
                  }
                />
              )}
              {log.selected_key && (
                <LogEntryDetailsView
                  className="w-full"
                  label="Selected Key"
                  value={log.selected_key.name}
                />
              )}
              {(log.selected_prompt_id ||
                log.selected_prompt_name ||
                log.selected_prompt_version) && (
                <LogEntryDetailsView
                  className="w-full"
                  label="Selected Prompt"
                  value={
                    <span className="break-words">
                      {selectedPromptDisplayName}
                      {selectedPromptDisplayName && log.selected_prompt_version
                        ? " · "
                        : ""}
                      {log.selected_prompt_version ? (
                        <>v{log.selected_prompt_version}</>
                      ) : null}
                    </span>
                  }
                />
              )}
              {log.number_of_retries > 0 && (
                <LogEntryDetailsView
                  className="w-full"
                  label="Number of Retries"
                  value={log.number_of_retries}
                />
              )}
              {log.team_id && (
                <LogEntryDetailsView
                  className="w-full"
                  label="Team"
                  value={
                    <Link
                      to="/workspace/logs"
                      search={{ team_ids: [log.team_id] }}
                      className="text-blue-600 hover:underline dark:text-blue-400"
                      data-testid="logdetails-team-link"
                    >
                      {log.team_name || log.team_id}
                    </Link>
                  }
                />
              )}
              {log.customer_id && (
                <LogEntryDetailsView
                  className="w-full"
                  label="Customer"
                  value={
                    <Link
                      to="/workspace/logs"
                      search={{ customer_ids: [log.customer_id] }}
                      className="text-blue-600 hover:underline dark:text-blue-400"
                      data-testid="logdetails-customer-link"
                    >
                      {log.customer_name || log.customer_id}
                    </Link>
                  }
                />
              )}
              {log.business_unit_id && (
                <LogEntryDetailsView
                  className="w-full"
                  label="Business Unit"
                  value={
                    <Link
                      to="/workspace/logs"
                      search={{ business_unit_ids: [log.business_unit_id] }}
                      className="text-blue-600 hover:underline dark:text-blue-400"
                      data-testid="logdetails-business-unit-link"
                    >
                      {log.business_unit_name || log.business_unit_id}
                    </Link>
                  }
                />
              )}
              {log.user_id && (
                <LogEntryDetailsView
                  className="w-full"
                  label="User"
                  value={
                    <Tooltip>
                      <TooltipTrigger asChild>
                        <Link
                          to="/workspace/logs"
                          search={{ user_ids: [log.user_id] }}
                          className={`text-primary hover:text-primary/80 block min-w-0 cursor-pointer text-sm font-normal break-all underline-offset-2 hover:underline${log.user_name ? "" : " font-mono"}`}
                          data-testid="logdetails-user-link"
                        >
                          {log.user_name || log.user_id}
                        </Link>
                      </TooltipTrigger>
                      <TooltipContent sideOffset={6}>
                        {log.user_name ? log.user_id : "Filter by user"}
                      </TooltipContent>
                    </Tooltip>
                  }
                />
              )}
              {log.fallback_index > 0 && (
                <LogEntryDetailsView
                  className="w-full"
                  label="Fallback Index"
                  value={log.fallback_index}
                />
              )}
              {log.virtual_key && (
                <LogEntryDetailsView
                  className="w-full"
                  label="Virtual Key"
                  value={log.virtual_key.name}
                />
              )}
              {log.routing_engines_used &&
                log.routing_engines_used.length > 0 && (
                  <LogEntryDetailsView
                    className="w-full"
                    label="Routing Engines Used"
                    value={
                      <div className="flex flex-wrap gap-2">
                        {log.routing_engines_used.map((engine) => (
                          <Badge
                            key={engine}
                            className={
                              RoutingEngineUsedColors[
                                engine as keyof typeof RoutingEngineUsedColors
                              ] ?? "bg-gray-100 text-gray-800"
                            }
                          >
                            <div className="flex items-center gap-2">
                              {RoutingEngineUsedIcons[
                                engine as keyof typeof RoutingEngineUsedIcons
                              ]?.()}
                              <span>
                                {RoutingEngineUsedLabels[
                                  engine as keyof typeof RoutingEngineUsedLabels
                                ] ?? engine}
                              </span>
                            </div>
                          </Badge>
                        ))}
                      </div>
                    }
                  />
                )}
              {log.routing_rule && (
                <LogEntryDetailsView
                  className="w-full"
                  label="Routing Rule"
                  value={log.routing_rule.name}
                />
              )}

              {(log.params as any)?.audio && (
                <>
                  {(log.params as any).audio.format && (
                    <LogEntryDetailsView
                      className="w-full"
                      label="Audio Format"
                      value={(log.params as any).audio.format}
                    />
                  )}
                  {(log.params as any).audio.voice && (
                    <LogEntryDetailsView
                      className="w-full"
                      label="Audio Voice"
                      value={(log.params as any).audio.voice}
                    />
                  )}
                </>
              )}

              {passthroughParams && (
                <>
                  {passthroughParams.method && (
                    <LogEntryDetailsView
                      className="w-full"
                      label="Method"
                      value={passthroughParams.method}
                    />
                  )}
                  {passthroughParams.path && (
                    <LogEntryDetailsView
                      className="w-full"
                      label="Path"
                      value={passthroughParams.path}
                    />
                  )}
                  {passthroughParams.raw_query && (
                    <LogEntryDetailsView
                      className="w-full"
                      label="Query"
                      value={passthroughParams.raw_query}
                    />
                  )}
                  {(passthroughParams.status_code ?? 0) !== 0 && (
                    <LogEntryDetailsView
                      className="w-full"
                      label="Status Code"
                      value={passthroughParams.status_code}
                    />
                  )}
                </>
              )}

              {log.params &&
                Object.keys(log.params).length > 0 &&
                Object.entries(log.params)
                  .filter(([key]) => {
                    const passthroughKeys = [
                      "method",
                      "path",
                      "raw_query",
                      "status_code",
                    ];
                    return (
                      key !== "tools" &&
                      key !== "instructions" &&
                      key !== "audio" &&
                      !(isPassthrough && passthroughKeys.includes(key))
                    );
                  })
                  .filter(
                    ([_, value]) =>
                      typeof value === "boolean" ||
                      typeof value === "number" ||
                      typeof value === "string",
                  )
                  .map(([key, value]) => (
                    <LogEntryDetailsView
                      key={key}
                      className="w-full"
                      label={key}
                      value={value}
                    />
                  ))}
            </div>
          </div>
          {log.status === "success" && !isContainer && !isPassthrough && (
            <>
              <DottedSeparator />
              <div className="space-y-4">
                <BlockHeader title="Tokens" />
                <div className="grid w-full grid-cols-3 items-center justify-between gap-4">
                  <LogEntryDetailsView
                    className="w-full"
                    label="Input Tokens"
                    value={log.token_usage?.prompt_tokens || "-"}
                  />
                  <LogEntryDetailsView
                    className="w-full"
                    label="Output Tokens"
                    value={log.token_usage?.completion_tokens || "-"}
                  />
                  <LogEntryDetailsView
                    className="w-full"
                    label="Total Tokens"
                    value={log.token_usage?.total_tokens || "-"}
                  />
                  <LogEntryDetailsView
                    className="w-full"
                    label="Cost"
                    value={
                      log.cost != null
                        ? `$${parseFloat(log.cost.toFixed(6))}`
                        : "-"
                    }
                  />
                  {log.token_usage?.prompt_tokens_details && (
                    <>
                      {log.token_usage.prompt_tokens_details
                        .cached_read_tokens && (
                        <LogEntryDetailsView
                          className="w-full"
                          label="Cache Read Tokens"
                          value={
                            log.token_usage.prompt_tokens_details
                              .cached_read_tokens ?? 0
                          }
                        />
                      )}
                      {log.token_usage.prompt_tokens_details
                        .cached_write_tokens && (
                        <LogEntryDetailsView
                          className="w-full"
                          label="Cache Write Tokens"
                          value={
                            log.token_usage.prompt_tokens_details
                              .cached_write_tokens ?? 0
                          }
                        />
                      )}
                      {log.token_usage.prompt_tokens_details.audio_tokens && (
                        <LogEntryDetailsView
                          className="w-full"
                          label="Input Audio Tokens"
                          value={
                            log.token_usage.prompt_tokens_details
                              .audio_tokens || "-"
                          }
                        />
                      )}
                    </>
                  )}
                  {log.token_usage?.completion_tokens_details && (
                    <>
                      {log.token_usage.completion_tokens_details
                        .reasoning_tokens && (
                        <LogEntryDetailsView
                          className="w-full"
                          label="Reasoning Tokens"
                          value={
                            log.token_usage.completion_tokens_details
                              .reasoning_tokens || "-"
                          }
                        />
                      )}
                      {log.token_usage.completion_tokens_details
                        .audio_tokens && (
                        <LogEntryDetailsView
                          className="w-full"
                          label="Output Audio Tokens"
                          value={
                            log.token_usage.completion_tokens_details
                              .audio_tokens || "-"
                          }
                        />
                      )}
                      {log.token_usage.completion_tokens_details
                        .accepted_prediction_tokens && (
                        <LogEntryDetailsView
                          className="w-full"
                          label="Accepted Prediction Tokens"
                          value={
                            log.token_usage.completion_tokens_details
                              .accepted_prediction_tokens || "-"
                          }
                        />
                      )}
                      {log.token_usage.completion_tokens_details
                        .rejected_prediction_tokens && (
                        <LogEntryDetailsView
                          className="w-full"
                          label="Rejected Prediction Tokens"
                          value={
                            log.token_usage.completion_tokens_details
                              .rejected_prediction_tokens || "-"
                          }
                        />
                      )}
                    </>
                  )}
                </div>
              </div>
              {(() => {
                const params = log.params as any;
                const reasoning = params?.reasoning;
                if (
                  !reasoning ||
                  typeof reasoning !== "object" ||
                  Object.keys(reasoning).length === 0
                ) {
                  return null;
                }
                return (
                  <>
                    <DottedSeparator />
                    <div className="space-y-4">
                      <BlockHeader title="Reasoning Parameters" />
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
                        {reasoning.max_tokens && (
                          <LogEntryDetailsView
                            className="w-full"
                            label="Max Tokens"
                            value={reasoning.max_tokens}
                          />
                        )}
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
                    />
                    <div className="grid w-full grid-cols-3 items-center justify-between gap-4">
                      {log.cache_debug.cache_hit ? (
                        <>
                          <LogEntryDetailsView
                            className="w-full"
                            label="Cache Type"
                            value={
                              <Badge variant="secondary" className="uppercase">
                                {log.cache_debug.hit_type}
                              </Badge>
                            }
                          />
                          {log.cache_debug.hit_type === "semantic" && (
                            <>
                              {log.cache_debug.provider_used && (
                                <LogEntryDetailsView
                                  className="w-full"
                                  label="Embedding Provider"
                                  value={
                                    <Badge
                                      variant="secondary"
                                      className="uppercase"
                                    >
                                      {log.cache_debug.provider_used}
                                    </Badge>
                                  }
                                />
                              )}
                              {log.cache_debug.model_used && (
                                <LogEntryDetailsView
                                  className="w-full"
                                  label="Embedding Model"
                                  value={log.cache_debug.model_used}
                                />
                              )}
                              {log.cache_debug.threshold && (
                                <LogEntryDetailsView
                                  className="w-full"
                                  label="Threshold"
                                  value={log.cache_debug.threshold || "-"}
                                />
                              )}
                              {log.cache_debug.similarity && (
                                <LogEntryDetailsView
                                  className="w-full"
                                  label="Similarity Score"
                                  value={
                                    log.cache_debug.similarity?.toFixed(2) ||
                                    "-"
                                  }
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
                                <Badge
                                  variant="secondary"
                                  className="uppercase"
                                >
                                  {log.cache_debug.provider_used}
                                </Badge>
                              }
                            />
                          )}
                          {log.cache_debug.model_used && (
                            <LogEntryDetailsView
                              className="w-full"
                              label="Embedding Model"
                              value={log.cache_debug.model_used}
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
                    </div>
                  </div>
                </>
              )}
              {log.metadata &&
                Object.keys(log.metadata).filter((k) => k !== "isAsyncRequest")
                  .length > 0 && (
                  <>
                    <DottedSeparator />
                    <div className="space-y-4">
                      <BlockHeader title="Metadata" />
                      <div className="grid w-full grid-cols-3 items-start justify-between gap-4">
                        {Object.entries(log.metadata)
                          .filter(([key]) => key !== "isAsyncRequest")
                          .map(([key, value]) => (
                            <LogEntryDetailsView
                              key={key}
                              className="w-full"
                              label={key}
                              value={String(value)}
                            />
                          ))}
                      </div>
                    </div>
                  </>
                )}
            </>
          )}
        </div>
      </details>
      <Tabs defaultValue="messages" className="gap-2">
        <TabsList className="bg-muted/60 h-10 w-fit">
          <TabsTrigger value="messages" className="px-3">
            Messages
            {log.input_history?.length ? (
              <span className="bg-background text-muted-foreground ml-1.5 rounded-sm border px-2 py-0.5 text-[10px] tabular-nums">
                {log.input_history.length + (log.output_message ? 1 : 0)}
              </span>
            ) : null}
          </TabsTrigger>
          <TabsTrigger value="tools" className="px-3">
            Tools
            {log.params?.tools?.length ? (
              <span className="bg-background text-muted-foreground ml-1.5 rounded-sm border px-2 py-0.5 text-[10px] tabular-nums">
                {log.params.tools.length}
              </span>
            ) : null}
          </TabsTrigger>
          <TabsTrigger value="routing" className="px-3">
            Routing
            {log.routing_engine_logs ? (
              <span className="bg-background text-muted-foreground ml-1.5 rounded-sm border px-2 py-0.5 text-[10px] tabular-nums">
                {log.routing_engine_logs.split("\n").filter(Boolean).length}
              </span>
            ) : null}
          </TabsTrigger>
          <TabsTrigger value="plugins" className="px-3">
            Plugin Logs
            {pluginLogCount > 0 ? (
              <span className="bg-background text-muted-foreground ml-1.5 rounded-sm border px-2 py-0.5 text-[10px] tabular-nums">
                {pluginLogCount}
              </span>
            ) : null}
          </TabsTrigger>
          <TabsTrigger value="raw" className="px-3">
            Raw JSON
          </TabsTrigger>
        </TabsList>

        <TabsContent value="messages" className="space-y-4">
          {(log.speech_input || log.speech_output) && (
            <SpeechView
              speechInput={log.speech_input}
              speechOutput={log.speech_output}
              isStreaming={log.stream}
            />
          )}
          {(log.transcription_input || log.transcription_output) && (
            <TranscriptionView
              transcriptionInput={log.transcription_input}
              transcriptionOutput={log.transcription_output}
              isStreaming={log.stream}
            />
          )}
          {(log.image_generation_input ||
            log.image_edit_input ||
            log.image_variation_input ||
            log.image_generation_output) && (
            <ImageView
              imageInput={log.image_generation_input}
              imageEditInput={log.image_edit_input}
              imageVariationInput={log.image_variation_input}
              imageOutput={log.image_generation_output}
              requestType={log.object}
            />
          )}
          {(log.video_generation_input || videoOutput || videoListOutput) && (
            <VideoView
              videoInput={log.video_generation_input}
              videoOutput={videoOutput}
              videoListOutput={videoListOutput}
              requestType={log.object}
            />
          )}

          {((log.input_history && log.input_history.length > 0) ||
            (log.output_message && !log.error_details?.error.message)) && (
            <div className="bg-card rounded-sm border p-5">
              {log.input_history?.map((message, index) => {
                const role = ((message.role as string) ||
                  "user") as MessageRole;
                const text = extractMessageText(message);
                const hasToolCalls =
                  Array.isArray(message.tool_calls) &&
                  message.tool_calls.length > 0;
                const isLast =
                  index === (log.input_history?.length ?? 0) - 1 &&
                  !log.output_message &&
                  !log.error_details?.error.message;
                const lineCount = text ? text.split("\n").length : 0;
                const approxTokens = text
                  ? Math.max(1, Math.round(text.length / 4))
                  : 0;
                const meta = text
                  ? role === "system" || role === "tool"
                    ? `${lineCount} line${lineCount === 1 ? "" : "s"} · ~${approxTokens} tokens`
                    : `${lineCount} line${lineCount === 1 ? "" : "s"}`
                  : hasToolCalls
                    ? `${message.tool_calls!.length} tool call${message.tool_calls!.length === 1 ? "" : "s"}`
                    : undefined;
                const usePlainText = role === "user" || role === "assistant";
                return (
                  <MessageRow key={index} role={role} meta={meta} last={isLast}>
                    {text ? (
                      usePlainText ? (
                        <CollapsibleCode text={text} preview={3} mono={false} />
                      ) : (
                        <CollapsibleCode
                          text={text}
                          preview={3}
                          lang={role === "system" ? "xml" : undefined}
                        />
                      )
                    ) : (
                      <LogChatMessageView
                        message={message}
                        audioFormat={audioFormat}
                      />
                    )}
                    {hasToolCalls && text ? (
                      <div className="text-muted-foreground mt-2 text-[11px]">
                        {message.tool_calls!.length} tool call
                        {message.tool_calls!.length === 1 ? "" : "s"}
                      </div>
                    ) : null}
                  </MessageRow>
                );
              })}
              {log.output_message &&
                !log.error_details?.error.message &&
                (() => {
                  const text = extractMessageText(log.output_message);
                  const lineCount = text ? text.split("\n").length : 0;
                  const tokenMeta = log.token_usage?.completion_tokens
                    ? `${log.token_usage.completion_tokens} tokens`
                    : undefined;
                  const meta = text
                    ? tokenMeta
                      ? `${lineCount} line${lineCount === 1 ? "" : "s"} · ${tokenMeta}`
                      : `${lineCount} line${lineCount === 1 ? "" : "s"}`
                    : tokenMeta;
                  return (
                    <MessageRow role="assistant" meta={meta} last>
                      {text ? (
                        <CollapsibleCode text={text} preview={3} mono={false} />
                      ) : (
                        <LogChatMessageView
                          message={log.output_message}
                          audioFormat={audioFormat}
                        />
                      )}
                    </MessageRow>
                  );
                })()}
            </div>
          )}

          {(() => {
            const inputMsgs = log.responses_input_history ?? [];
            const outputMsgs =
              log.status !== "processing" && !log.error_details?.error.message
                ? (log.responses_output ?? [])
                : [];
            const all: ResponsesMessage[] = [...inputMsgs, ...outputMsgs];
            if (all.length === 0) return null;
            return (
              <div className="bg-card rounded-sm border p-5">
                {all.map((msg, index) => {
                  const role = getResponsesRole(msg);
                  const text = extractResponsesText(msg);
                  const isLast = index === all.length - 1;
                  const lineCount = text ? text.split("\n").length : 0;
                  const approxTokens = text
                    ? Math.max(1, Math.round(text.length / 4))
                    : 0;
                  const isEncrypted =
                    msg.type === "reasoning" && !!msg.encrypted_content;
                  const meta = text
                    ? role === "system" || role === "tool"
                      ? `${lineCount} line${lineCount === 1 ? "" : "s"} · ~${approxTokens} tokens`
                      : role === "reasoning"
                        ? `~${approxTokens} tokens${isEncrypted ? " · encrypted" : ""}`
                        : `${lineCount} line${lineCount === 1 ? "" : "s"}`
                    : msg.type || undefined;
                  const usePlainText = role === "user" || role === "assistant";
                  return (
                    <MessageRow
                      key={index}
                      role={role}
                      meta={meta}
                      last={isLast}
                    >
                      {text ? (
                        usePlainText ? (
                          <CollapsibleCode
                            text={text}
                            preview={3}
                            mono={false}
                          />
                        ) : (
                          <CollapsibleCode
                            text={text}
                            preview={3}
                            lang={role === "system" ? "xml" : undefined}
                          />
                        )
                      ) : (
                        <div className="text-muted-foreground text-[12px]">
                          {msg.type || "—"}
                        </div>
                      )}
                    </MessageRow>
                  );
                })}
              </div>
            );
          })()}

          {log.is_large_payload_request &&
            !log.input_history?.length &&
            !log.responses_input_history?.length && (
              <div className="rounded-sm border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-800 dark:bg-amber-950/50 dark:text-amber-300">
                Large payload request — input content was streamed directly to
                the provider and is not available for display.
                {log.raw_request &&
                  " A truncated preview is available in the Raw JSON tab."}
              </div>
            )}
          {log.is_large_payload_response &&
            !log.output_message &&
            !log.responses_output?.length &&
            log.status !== "processing" && (
              <div className="rounded-sm border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-800 dark:bg-amber-950/50 dark:text-amber-300">
                Large payload response — response content was streamed directly
                to the client and is not available for display.
                {log.raw_response &&
                  " A truncated preview is available in the Raw JSON tab."}
              </div>
            )}

          {log.status !== "processing" &&
            log.embedding_output &&
            log.embedding_output.length > 0 &&
            !log.error_details?.error.message && (
              <div className="bg-card space-y-3 rounded-sm border p-5">
                <div className="text-sm font-medium">Embedding</div>
                <LogChatMessageView
                  message={{
                    role: "assistant",
                    content: JSON.stringify(
                      log.embedding_output.map(
                        (embedding) => embedding.embedding,
                      ),
                      null,
                      2,
                    ),
                  }}
                />
              </div>
            )}
          {log.status !== "processing" &&
            log.rerank_output &&
            !log.error_details?.error.message && (
              <CollapsibleBox
                title={`Rerank Output (${log.rerank_output.length})`}
                onCopy={() => JSON.stringify(log.rerank_output, null, 2)}
              >
                <CodeEditor
                  className="z-0 w-full"
                  shouldAdjustInitialHeight={true}
                  maxHeight={450}
                  wrap={true}
                  code={JSON.stringify(log.rerank_output, null, 2)}
                  lang="json"
                  readonly={true}
                  options={{
                    scrollBeyondLastLine: false,
                    lineNumbers: "off",
                    alwaysConsumeMouseWheel: false,
                  }}
                />
              </CollapsibleBox>
            )}

          {(log.error_details?.error.message ||
            log.error_details?.error.error != null) && (
            <div className="rounded-sm border border-red-200 bg-red-50/70 p-5 dark:border-red-900 dark:bg-red-950/30">
              <div className="flex items-center gap-2 text-red-700 dark:text-red-400">
                <AlertCircle className="h-4 w-4 shrink-0" />
                <span className="text-[12.5px] font-semibold">Error</span>
                {log.error_details?.error.message ? (
                  <CopyInlineButton text={log.error_details.error.message} />
                ) : null}
              </div>
              {log.error_details?.error.message ? (
                <div className="mt-2 text-[13px] leading-relaxed break-words whitespace-pre-wrap text-red-700 dark:text-red-400">
                  {log.error_details.error.message}
                </div>
              ) : null}
              {log.error_details?.error.error != null ? (
                <details className="group mt-3 rounded-sm border border-red-200/70 bg-white/40 dark:border-red-900/70 dark:bg-red-950/40">
                  <summary className="flex cursor-pointer items-center justify-between px-3 py-2 text-[12px] text-red-700 hover:bg-red-50/80 dark:text-red-400 dark:hover:bg-red-950/60">
                    <span className="font-medium">Details</span>
                    <ChevronDown className="h-3.5 w-3.5 transition-transform group-open:rotate-180" />
                  </summary>
                  <div className="custom-scrollbar max-h-[400px] overflow-y-auto border-t border-red-200/70 px-3 py-2 font-mono text-[11.5px] leading-[1.6] break-words whitespace-pre-wrap text-red-900 dark:border-red-900/70 dark:text-red-300">
                    {typeof log.error_details.error.error === "string"
                      ? log.error_details.error.error
                      : JSON.stringify(log.error_details.error.error, null, 2)}
                  </div>
                </details>
              ) : null}
            </div>
          )}
        </TabsContent>

        <TabsContent value="tools" className="space-y-3">
          {toolsParameter ? (
            <div className="bg-card rounded-sm border p-5">
              <div className="text-muted-foreground mb-3 text-[12px]">
                {log.params?.tools?.length ?? 0} tools exposed to the model
                {(log.params as any)?.tool_choice != null ? (
                  <>
                    {" "}
                    · tool_choice ={" "}
                    <span className="text-foreground font-mono break-all">
                      {formatToolChoice((log.params as any).tool_choice)}
                    </span>
                  </>
                ) : null}
              </div>
              <div className="grid grid-cols-1 gap-2 md:grid-cols-2">
                {(log.params?.tools as any[]).map((tool, i) => {
                  const name =
                    tool?.function?.name ?? tool?.name ?? `tool_${i}`;
                  const description =
                    tool?.function?.description ?? tool?.description ?? "";
                  const schema =
                    tool?.function?.parameters ??
                    tool?.input_schema ??
                    tool?.parameters ??
                    null;
                  const schemaJson =
                    schema != null ? JSON.stringify(schema, null, 2) : "";
                  return (
                    <details
                      key={i}
                      className="group bg-card rounded-sm border"
                    >
                      <summary className="hover:bg-muted/30 flex cursor-pointer list-none items-start gap-2 p-3 transition">
                        <div className="grid h-7 w-7 shrink-0 place-items-center rounded-sm border border-amber-300 bg-amber-50 text-amber-700 dark:border-amber-900 dark:bg-amber-950/50 dark:text-amber-400">
                          <Wrench className="h-3 w-3" strokeWidth={1.5} />
                        </div>
                        <div className="min-w-0 flex-1">
                          <div className="text-foreground truncate font-mono text-[12.5px] font-medium">
                            {name}
                          </div>
                          {description ? (
                            <div className="text-muted-foreground mt-0.5 line-clamp-2 text-[12px]">
                              {description}
                            </div>
                          ) : null}
                        </div>
                        <ChevronDown
                          className={cn(
                            "text-muted-foreground mt-1 h-3.5 w-3.5 shrink-0 transition-transform",
                            "group-open:rotate-180",
                            !schemaJson && "opacity-30",
                          )}
                        />
                      </summary>
                      {schemaJson ? (
                        <div className="border-t">
                          <div className="text-muted-foreground flex items-center justify-between px-3 py-1.5 text-[10.5px] uppercase tracking-wider">
                            <span className="font-semibold">Parameters</span>
                            <CopyInlineButton text={schemaJson} />
                          </div>
                          <pre className="custom-scrollbar max-h-[300px] overflow-auto border-t px-3 py-2 font-mono text-[11.5px] leading-[1.6] whitespace-pre">
                            {schemaJson}
                          </pre>
                        </div>
                      ) : (
                        <div className="text-muted-foreground border-t px-3 py-2 text-[11.5px]">
                          No parameter schema.
                        </div>
                      )}
                    </details>
                  );
                })}
              </div>
            </div>
          ) : null}
          {log.params?.instructions && (
            <CollapsibleBox
              title="Instructions"
              onCopy={() => log.params?.instructions || ""}
            >
              <div className="custom-scrollbar max-h-[400px] overflow-y-auto px-6 py-2 font-mono text-xs break-words whitespace-pre-wrap">
                {log.params.instructions}
              </div>
            </CollapsibleBox>
          )}
          {!toolsParameter && !log.params?.instructions && (
            <div className="text-muted-foreground rounded-sm border border-dashed p-5 text-center text-sm">
              No tools or instructions on this request.
            </div>
          )}
        </TabsContent>

        <TabsContent value="routing" className="space-y-3">
          {log.attempt_trail && log.attempt_trail.length > 1 && (
            <CollapsibleBox
              title={`Attempt Trail (${log.attempt_trail.length} attempts)`}
              onCopy={() => JSON.stringify(log.attempt_trail, null, 2)}
            >
              <div className="overflow-x-auto px-6 py-3">
                <table className="w-full border-collapse text-xs">
                  <thead>
                    <tr className="border-border text-muted-foreground border-b">
                      <th className="py-1 pr-6 text-left font-medium">#</th>
                      <th className="py-1 pr-6 text-left font-medium">Key</th>
                      <th className="py-1 text-left font-medium">Result</th>
                    </tr>
                  </thead>
                  <tbody>
                    {log.attempt_trail.map((record) => (
                      <tr
                        key={record.attempt}
                        className="border-border/50 border-b last:border-0"
                      >
                        <td className="text-muted-foreground py-1.5 pr-6 tabular-nums">
                          {record.attempt + 1}
                        </td>
                        <td className="py-1.5 pr-6 font-mono">
                          {record.key_name || record.key_id}
                        </td>
                        <td className="py-1.5">
                          {record.fail_reason ? (
                            <span className="text-destructive">
                              {record.fail_reason}
                            </span>
                          ) : (
                            <span className="text-green-600 dark:text-green-400">
                              success
                            </span>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </CollapsibleBox>
          )}
          {log.routing_engine_logs && (
            <CollapsibleBox
              title="Routing Decision Logs"
              onCopy={() => log.routing_engine_logs || ""}
            >
              <div className="custom-scrollbar max-h-[400px] overflow-y-auto">
                {log.routing_engine_logs
                  .split("\n")
                  .filter((l) => l.trim())
                  .map((line, i) => {
                    const m = line.match(
                      /^\[(\d+)\]\s+\[([^\]]+)\]\s+-\s+(.*)$/,
                    );
                    const ts = m ? Number(m[1]) : null;
                    const scope = m ? m[2] : null;
                    const message = m ? m[3] : line;
                    return (
                      <div
                        key={i}
                        className="flex items-start gap-3 border-b px-4 py-1.5 font-mono text-xs last:border-b-0"
                      >
                        {ts != null ? (
                          <span className="text-muted-foreground shrink-0">
                            {format(new Date(ts), "HH:mm:ss.SSS")}
                          </span>
                        ) : null}
                        {scope ? (
                          <span className="shrink-0 rounded bg-blue-100 px-1.5 py-0.5 text-[10px] font-semibold uppercase text-blue-700 dark:bg-blue-900 dark:text-blue-300">
                            {scope}
                          </span>
                        ) : null}
                        <span className="break-words whitespace-pre-wrap">
                          {message}
                        </span>
                      </div>
                    );
                  })}
              </div>
            </CollapsibleBox>
          )}
          {!log.attempt_trail?.length && !log.routing_engine_logs && (
              <div className="text-muted-foreground rounded-sm border border-dashed p-5 text-center text-sm">
                No routing logs for this request.
              </div>
            )}
        </TabsContent>

        <TabsContent value="plugins" className="space-y-3">
          {log.plugin_logs ? (
            <PluginLogsView pluginLogs={log.plugin_logs} />
          ) : (
            <div className="text-muted-foreground rounded-sm border border-dashed p-5 text-center text-sm">
              No plugin logs for this request.
            </div>
          )}
        </TabsContent>

        <TabsContent value="raw" className="space-y-3">
          {isPassthrough && passthroughRequestBody && (
            <CollapsibleBox
              title="Request Body"
              onCopy={() => {
                try {
                  return JSON.stringify(
                    JSON.parse(passthroughRequestBody || ""),
                    null,
                    2,
                  );
                } catch {
                  return passthroughRequestBody || "";
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
                    return JSON.stringify(
                      JSON.parse(passthroughRequestBody || ""),
                      null,
                      2,
                    );
                  } catch {
                    return passthroughRequestBody || "";
                  }
                })()}
                lang="json"
                readonly={true}
                options={{
                  scrollBeyondLastLine: false,
                  lineNumbers: "off",
                  alwaysConsumeMouseWheel: false,
                }}
              />
            </CollapsibleBox>
          )}
          {isPassthrough &&
            passthroughResponseBody &&
            log.status !== "processing" && (
              <CollapsibleBox
                title="Response Body"
                onCopy={() => {
                  try {
                    return JSON.stringify(
                      JSON.parse(passthroughResponseBody || ""),
                      null,
                      2,
                    );
                  } catch {
                    return passthroughResponseBody || "";
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
                      return JSON.stringify(
                        JSON.parse(passthroughResponseBody || ""),
                        null,
                        2,
                      );
                    } catch {
                      return passthroughResponseBody || "";
                    }
                  })()}
                  lang="json"
                  readonly={true}
                  options={{
                    scrollBeyondLastLine: false,
                    lineNumbers: "off",
                    alwaysConsumeMouseWheel: false,
                  }}
                />
              </CollapsibleBox>
            )}
          {rawRequest && (
            <>
              <div className="text-muted-foreground text-[12px]">
                Raw Request sent to{" "}
                <span className="text-foreground font-medium capitalize">
                  {log.provider}
                </span>
                {log.is_large_payload_request && (
                  <span className="ml-2 text-xs font-normal text-amber-600 dark:text-amber-400">
                    (truncated preview)
                  </span>
                )}
              </div>
              <CollapsibleBox
                title={
                  log.is_large_payload_request
                    ? "Raw Request (Truncated)"
                    : "Raw Request"
                }
                onCopy={() => formatJsonSafe(rawRequest)}
              >
                <CodeEditor
                  className="z-0 w-full"
                  shouldAdjustInitialHeight={true}
                  maxHeight={450}
                  wrap={true}
                  code={formatJsonSafe(rawRequest)}
                  lang="json"
                  readonly={true}
                  options={{
                    scrollBeyondLastLine: false,
                    lineNumbers: "off",
                    alwaysConsumeMouseWheel: false,
                  }}
                />
              </CollapsibleBox>
            </>
          )}
          {rawResponse && log.status !== "processing" && (
            <>
              <div className="text-muted-foreground text-[12px]">
                Raw Response from{" "}
                <span className="text-foreground font-medium capitalize">
                  {log.provider}
                </span>
                {log.is_large_payload_response && (
                  <span className="ml-2 text-xs font-normal text-amber-600 dark:text-amber-400">
                    (truncated preview)
                  </span>
                )}
              </div>
              <CollapsibleBox
                title={
                  log.is_large_payload_response
                    ? "Raw Response (Truncated)"
                    : "Raw Response"
                }
                onCopy={() => formatJsonSafe(rawResponse)}
              >
                <CodeEditor
                  className="z-0 w-full"
                  shouldAdjustInitialHeight={true}
                  maxHeight={450}
                  wrap={true}
                  code={formatJsonSafe(rawResponse)}
                  lang="json"
                  readonly={true}
                  options={{
                    scrollBeyondLastLine: false,
                    lineNumbers: "off",
                    alwaysConsumeMouseWheel: false,
                  }}
                />
              </CollapsibleBox>
            </>
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
                options={{
                  scrollBeyondLastLine: false,
                  lineNumbers: "off",
                  alwaysConsumeMouseWheel: false,
                }}
              />
            </CollapsibleBox>
          )}
          {!rawRequest &&
            !rawResponse &&
            !passthroughRequestBody &&
            !passthroughResponseBody &&
            !log.list_models_output && (
              <div className="text-muted-foreground rounded-sm border border-dashed p-5 text-center text-sm">
                No raw JSON available.
              </div>
            )}
        </TabsContent>
      </Tabs>
    </>
  );
}

const copyRequestBody = async (
  log: LogEntry,
  copy: (text: string) => Promise<void>,
) => {
  try {
    const isChat =
      log.object === "chat.completion" ||
      log.object === "chat.completion.chunk";
    const isResponses =
      log.object === "response" || log.object === "response.completion.chunk";
    const isRealtimeTurn = log.object === "realtime.turn";
    const isSpeech =
      log.object === "audio.speech" || log.object === "audio.speech.chunk";
    const isTextCompletion =
      log.object === "text.completion" ||
      log.object === "text.completion.chunk";
    const isEmbedding = log.object === "list";

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
          .map((block: any) => block.text)
          .join("\n");
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
        return message.content
          .filter((block: any) => block && block.type === "text" && block.text)
          .map((block: any) => block.text);
      }
      return [];
    };

    const isSupportedType =
      isChat ||
      isResponses ||
      isRealtimeTurn ||
      isSpeech ||
      isTextCompletion ||
      isEmbedding;
    if (!isSupportedType) {
      if (
        log.object === "audio.transcription" ||
        log.object === "audio.transcription.chunk"
      ) {
        toast.error(
          "Copy request body is not available for transcription requests",
        );
      } else {
        toast.error(
          "Copy request body is only available for chat, responses, speech, text completion, and embedding requests",
        );
      }
      return;
    }

    const requestBody: any = {
      model:
        log.provider && log.model
          ? `${log.provider}/${log.model}`
          : log.model || "",
    };

    if (isRealtimeTurn) {
      if (log.input_history && log.input_history.length > 0) {
        requestBody.messages = log.input_history;
      }
      if (log.output_message) {
        requestBody.output = log.output_message;
      }
    } else if (isChat && log.input_history && log.input_history.length > 0) {
      requestBody.messages = log.input_history;
    } else if (
      isResponses &&
      log.responses_input_history &&
      log.responses_input_history.length > 0
    ) {
      requestBody.input = log.responses_input_history;
    } else if (isSpeech && log.speech_input) {
      requestBody.input = log.speech_input.input;
    } else if (
      isTextCompletion &&
      log.input_history &&
      log.input_history.length > 0
    ) {
      const firstMessage = log.input_history[0];
      const prompt = extractTextFromMessage(firstMessage);
      if (prompt) {
        requestBody.prompt = prompt;
      }
    } else if (
      isEmbedding &&
      log.input_history &&
      log.input_history.length > 0
    ) {
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

    if (
      (isChat || isResponses || isRealtimeTurn) &&
      log.params?.tools &&
      Array.isArray(log.params.tools) &&
      log.params.tools.length > 0
    ) {
      requestBody.tools = log.params.tools;
    }
    if ((isResponses || isRealtimeTurn) && log.params?.instructions) {
      requestBody.instructions = log.params.instructions;
    }

    const requestBodyJson = JSON.stringify(requestBody, null, 2);
    await copy(requestBodyJson);
  } catch {
    toast.error("Failed to copy request body");
  }
};
