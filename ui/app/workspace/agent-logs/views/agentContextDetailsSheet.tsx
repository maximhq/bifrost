import { formatLatency } from "@/app/workspace/dashboard/utils/chartUtils";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { getErrorMessage } from "@/lib/store";
import { useGetAgentLogStatsQuery, useLazyGetAgentLogOperationsQuery } from "@/lib/store/apis/agentLogsApi";
import type { AgentLogOperation, AgentLogSummary } from "@/lib/types/agentLogs";
import { cn } from "@/lib/utils";
import { format, isValid } from "date-fns";
import { ArrowDown, ArrowUp, CheckCircle, ChevronLeft, ChevronRight, Clock, Filter, Loader2, XCircle } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { toast } from "sonner";
import { A2APartsView, A2APayloadView, TaskStateBadge } from "./a2aPayloadView";
import { CorrelatedLogsContent, useCorrelatedLogCounts } from "./correlatedLogs";

const CONTEXT_PAGE_SIZE = 10;
const CONTEXT_FILTERS = (contextId: string) => ({ context_id: contextId, record_kind: ["request"] });

interface AgentContextDetailsSheetProps {
	contextId: string | null;
	currentLogId: string | null;
	open: boolean;
	onOpenChange: (open: boolean) => void;
	onLogClick: (log: AgentLogSummary) => void;
	onFilterContext: (contextId: string) => void;
}

interface SessionContent {
	key: string;
	role: "user" | "agent";
	kind: "message" | "status" | "artifact";
	parts: unknown[];
	state?: string;
	artifactName?: string;
}

function asRecord(value: unknown): Record<string, unknown> | null {
	return typeof value === "object" && value !== null && !Array.isArray(value) ? (value as Record<string, unknown>) : null;
}

function parseProtocolBody(body: string | undefined): Record<string, unknown> | null {
	if (!body) return null;
	try {
		const parsed = asRecord(JSON.parse(body));
		return asRecord(parsed?.result) ?? parsed;
	} catch {
		return null;
	}
}

function partsFromMessage(value: unknown): unknown[] | null {
	const message = asRecord(value);
	return message && Array.isArray(message.parts) ? message.parts : null;
}

interface AssembledArtifact {
	key: string;
	parts: unknown[];
	name?: string;
}

function messageIdentity(message: Record<string, unknown>): string | null {
	return typeof message.messageId === "string" ? message.messageId : null;
}

function taskIdentity(task: Record<string, unknown>, fallback?: string): string | undefined {
	return typeof task.id === "string" ? task.id : typeof task.taskId === "string" ? task.taskId : fallback;
}

function artifactIdentity(taskId: string | undefined, artifact: Record<string, unknown>, fallback: string) {
	const artifactId = typeof artifact.artifactId === "string" ? artifact.artifactId : fallback;
	return `${taskId ?? "unknown-task"}:${artifactId}`;
}

function buildSessionContent(log: AgentLogOperation): SessionContent[] {
	const content: SessionContent[] = [];
	const seenMessageIds = new Set<string>();
	const assembledArtifacts = new Map<string, AssembledArtifact>();
	const artifactOrder: string[] = [];

	const addMessage = (messageValue: unknown, key: string, role: "user" | "agent", kind: SessionContent["kind"], state?: string) => {
		const message = asRecord(messageValue);
		const parts = partsFromMessage(message);
		if (!message || !parts) return;
		const messageId = messageIdentity(message);
		if (messageId && seenMessageIds.has(messageId)) return;
		if (messageId) seenMessageIds.add(messageId);
		content.push({ key, role, kind, parts, state });
	};

	const setArtifact = (taskId: string | undefined, artifact: Record<string, unknown>, fallback: string, append: boolean) => {
		const identity = artifactIdentity(taskId, artifact, fallback);
		const incoming = Array.isArray(artifact.parts) ? artifact.parts : [];
		const existing = assembledArtifacts.get(identity);
		if (!existing) artifactOrder.push(identity);
		assembledArtifacts.set(identity, {
			key: identity,
			parts: append ? [...(existing?.parts ?? []), ...incoming] : incoming,
			name: typeof artifact.name === "string" ? artifact.name : existing?.name,
		});
	};

	const addTaskSnapshot = (task: Record<string, unknown>, keyPrefix: string, fallbackTaskId?: string) => {
		const status = asRecord(task.status);
		addMessage(status?.message, `${keyPrefix}-status`, "agent", "status", typeof status?.state === "string" ? status.state : undefined);
		const taskId = taskIdentity(task, fallbackTaskId);
		if (Array.isArray(task.artifacts)) {
			task.artifacts.forEach((value, index) => {
				const artifact = asRecord(value);
				if (artifact) setArtifact(taskId, artifact, `${keyPrefix}-${index}`, false);
			});
		}
	};

	const processUpdate = (update: Record<string, unknown>, keyPrefix: string, fallbackTaskId?: string, fallbackArtifactId?: string) => {
		if (partsFromMessage(update)) {
			addMessage(update, `${keyPrefix}-message`, "agent", "message");
			return;
		}

		const statusUpdate = asRecord(update.statusUpdate) ?? (asRecord(update.status) && (update.taskId || update.contextId) ? update : null);
		if (statusUpdate) {
			const status = asRecord(statusUpdate.status);
			addMessage(status?.message, `${keyPrefix}-status`, "agent", "status", typeof status?.state === "string" ? status.state : undefined);
			return;
		}

		const artifactUpdate = asRecord(update.artifactUpdate) ?? (asRecord(update.artifact) ? update : null);
		const artifact = asRecord(artifactUpdate?.artifact);
		if (artifactUpdate && artifact) {
			const taskId = typeof artifactUpdate.taskId === "string" ? artifactUpdate.taskId : fallbackTaskId;
			setArtifact(taskId, artifact, fallbackArtifactId ?? keyPrefix, artifactUpdate.append === true);
			return;
		}

		if (asRecord(update.status) || Array.isArray(update.artifacts)) addTaskSnapshot(update, `${keyPrefix}-task`, fallbackTaskId);
	};

	const isPushRelay = log.operation === "push_notification" || log.operation === "push_delivery";
	const request = parseProtocolBody(log.request_body);
	if (request) {
		if (isPushRelay) processUpdate(request, "push-update", log.task_id, log.artifact_id);
		else {
			const requestMessage = asRecord(request.message) ?? asRecord(asRecord(request.params)?.message);
			addMessage(requestMessage, "request-message", "user", "message");
		}
	}

	const response = parseProtocolBody(log.response_body);
	if (response && !isPushRelay) {
		if (partsFromMessage(response)) addMessage(response, "response-message", "agent", "message");
		else addTaskSnapshot(response, "response-task", log.task_id);
	}

	const event = parseProtocolBody(log.event_body);
	if (event) processUpdate(event, `event-${log.event_sequence ?? log.id}`, log.task_id, log.artifact_id);
	log.events.forEach((eventLog) => {
		const eventBody = parseProtocolBody(eventLog.event_body);
		if (eventBody) {
			processUpdate(eventBody, `event-${eventLog.event_sequence ?? eventLog.id}`, eventLog.task_id, eventLog.artifact_id);
		}
	});

	artifactOrder.forEach((identity) => {
		const artifact = assembledArtifacts.get(identity);
		if (!artifact) return;
		content.push({
			key: `artifact-${artifact.key}`,
			role: "agent",
			kind: "artifact",
			parts: artifact.parts,
			artifactName: artifact.name,
		});
	});

	return content;
}

function ConversationMessage({ item, timestamp, context }: { item: SessionContent; timestamp?: string; context?: string }) {
	const user = item.role === "user";
	const label = user ? "You" : item.kind === "artifact" ? "Artifact" : item.kind === "status" ? "Agent update" : "Agent response";
	return (
		<div className="flex gap-3">
			<div className="flex flex-col items-center pt-1.5">
				<span className={cn("size-2 rounded-full", user ? "bg-blue-500" : item.kind === "artifact" ? "bg-violet-500" : "bg-foreground")} />
				<div className="bg-border mt-1 w-px flex-1" />
			</div>
			<div className="min-w-0 flex-1 pb-5">
				<div className="mb-1.5 flex min-w-0 items-center gap-2">
					<span className="text-xs font-semibold">{label}</span>
					{item.state ? <TaskStateBadge state={item.state} /> : null}
					{item.kind === "artifact" && item.artifactName ? (
						<span className="text-muted-foreground truncate text-xs">{item.artifactName}</span>
					) : null}
					{context ? <span className="text-muted-foreground truncate text-xs">{context}</span> : null}
					{timestamp ? <span className="text-muted-foreground ml-auto font-mono text-[10px]">{timestamp}</span> : null}
				</div>
				<div
					className={cn(
						"rounded-sm border p-3 text-sm leading-5",
						user && "border-blue-200 bg-blue-50/60 dark:border-blue-900 dark:bg-blue-950/30",
						item.kind === "artifact" && "border-violet-200 bg-violet-50/60 dark:border-violet-900 dark:bg-violet-950/25",
						!user && item.kind !== "artifact" && "bg-card",
					)}
				>
					<A2APartsView parts={item.parts} />
				</div>
			</div>
		</div>
	);
}

// ConversationFailedResponse renders a failed task as the Agent's response turn when no status message was recorded.
function ConversationFailedResponse({ state }: { state: string }) {
	return (
		<div className="flex gap-3">
			<div className="flex flex-col items-center pt-1.5">
				<span className="bg-destructive size-2 rounded-full" />
				<div className="bg-border mt-1 w-px flex-1" />
			</div>
			<div className="min-w-0 flex-1 pb-5">
				<div className="mb-1.5 flex min-w-0 items-center gap-2">
					<span className="text-xs font-semibold">Agent response</span>
					<TaskStateBadge state={state} />
				</div>
				<div className="bg-card text-muted-foreground rounded-sm border p-3 text-sm leading-5">The agent did not return a response.</div>
			</div>
		</div>
	);
}

function ConversationError({ error, timestamp }: { error: unknown; timestamp?: string }) {
	return (
		<div className="flex gap-3">
			<div className="flex flex-col items-center pt-1.5">
				<span className="bg-destructive size-2 rounded-full" />
				<div className="bg-border mt-1 w-px flex-1" />
			</div>
			<div className="min-w-0 flex-1 pb-5">
				<div className="mb-1.5 flex min-w-0 items-center gap-2">
					<span className="text-xs font-semibold">Error</span>
					{timestamp ? <span className="text-muted-foreground ml-auto font-mono text-[10px]">{timestamp}</span> : null}
				</div>
				<A2APayloadView errorDetails={error} showSectionTitles={false} />
			</div>
		</div>
	);
}

export function A2AConversationHistory({ responseBody }: { responseBody?: string }) {
	const response = parseProtocolBody(responseBody);
	const history = Array.isArray(response?.history) ? response.history : [];
	if (history.length === 0) return null;

	return (
		<details className="group rounded-sm border">
			<summary className="hover:bg-muted/30 flex cursor-pointer items-center justify-between px-3 py-2 text-sm transition">
				<span className="font-medium">Conversation history</span>
				<span className="text-muted-foreground text-xs">
					{history.length} {history.length === 1 ? "message" : "messages"}
				</span>
			</summary>
			<div className="space-y-3 border-t p-3">
				{history.map((value, index) => {
					const message = asRecord(value);
					const parts = partsFromMessage(message);
					if (!message || !parts) return null;
					const role = typeof message.role === "string" && message.role.replace(/^ROLE_/, "").toLowerCase() === "user" ? "user" : "agent";
					return (
						<ConversationMessage
							key={messageIdentity(message) ?? index}
							item={{
								key: `history-${index}`,
								role,
								kind: "message",
								parts,
							}}
						/>
					);
				})}
			</div>
		</details>
	);
}

interface ProtocolActivityDefinition {
	label: string;
	showsTaskState?: boolean;
}

const PROTOCOL_ACTIVITY_DEFINITIONS: Record<string, ProtocolActivityDefinition> = {
	GetTask: { label: "Task state fetched", showsTaskState: true },
	ListTasks: { label: "Tasks listed" },
	CancelTask: { label: "Task cancellation requested", showsTaskState: true },
	SubscribeToTask: { label: "Task update stream", showsTaskState: true },
	CreateTaskPushNotificationConfig: { label: "Push notifications configured" },
	GetTaskPushNotificationConfig: { label: "Push configuration fetched" },
	ListTaskPushNotificationConfigs: { label: "Push configurations listed" },
	DeleteTaskPushNotificationConfig: { label: "Push configuration deleted" },
	push_notification: { label: "Task update received", showsTaskState: true },
	push_delivery: { label: "Push notification forwarded" },
	GetAgentCard: { label: "Public Agent Card fetched" },
	GetExtendedAgentCard: { label: "Extended Agent Card fetched" },
};

function protocolActivityDefinition(operation: string): ProtocolActivityDefinition {
	return (
		PROTOCOL_ACTIVITY_DEFINITIONS[operation] ?? {
			label: operation
				.replaceAll("/", " ")
				.replace(/([a-z])([A-Z])/g, "$1 $2")
				.replaceAll("_", " ")
				.replace(/\b\w/g, (character) => character.toUpperCase()),
		}
	);
}

function latestTaskState(log: AgentLogOperation) {
	const latestEventState = [...log.events]
		.reverse()
		.map((eventLog) => {
			if (eventLog.task_state) return eventLog.task_state;
			const event = parseProtocolBody(eventLog.event_body);
			const statusUpdate = asRecord(event?.statusUpdate) ?? event;
			const eventStatus = asRecord(statusUpdate?.status);
			return typeof eventStatus?.state === "string" ? eventStatus.state : undefined;
		})
		.find(Boolean);
	if (latestEventState) return latestEventState;
	if (log.task_state) return log.task_state;
	const response = parseProtocolBody(log.response_body);
	const responseStatus = asRecord(response?.status);
	if (typeof responseStatus?.state === "string") return responseStatus.state;
	const event = parseProtocolBody(log.event_body);
	const statusUpdate = asRecord(event?.statusUpdate) ?? event;
	const eventStatus = asRecord(statusUpdate?.status);
	return typeof eventStatus?.state === "string" ? eventStatus.state : undefined;
}

function ProtocolActivity({ log, showTimestamp }: { log: AgentLogOperation; showTimestamp: boolean }) {
	const definition = protocolActivityDefinition(log.operation);
	const taskState = definition.showsTaskState ? latestTaskState(log) : undefined;
	const timestamp = new Date(log.timestamp);

	return (
		<div className="flex gap-3">
			<div className="flex flex-col items-center pt-1.5">
				<span className="size-2 rotate-45 bg-sky-500" />
				<div className="bg-border mt-1 w-px flex-1" />
			</div>
			<div className="min-w-0 flex-1 pb-5">
				<div className="mb-1.5 flex min-w-0 items-center gap-2">
					<span className="text-xs font-semibold">Event</span>
					{showTimestamp ? (
						<span className="text-muted-foreground ml-auto shrink-0 font-mono text-[10px]">
							{isValid(timestamp) ? format(timestamp, "MMM d, HH:mm:ss") : log.timestamp}
						</span>
					) : null}
				</div>
				<div className="rounded-sm border border-sky-200 bg-sky-50/60 p-3 dark:border-sky-900 dark:bg-sky-950/30">
					<div className="flex flex-wrap items-center gap-2">
						<span className="text-sm font-medium">{definition.label}</span>
						{taskState ? <TaskStateBadge state={taskState} /> : null}
						{log.status === "error" ? <span className="text-destructive text-xs">Failed</span> : null}
						{log.status === "processing" ? <span className="text-muted-foreground text-xs">In progress</span> : null}
					</div>
				</div>
			</div>
		</div>
	);
}

export function isA2AConversationOperation(operation: string) {
	return operation === "SendMessage" || operation === "SendStreamingMessage";
}

export function isA2APushConversationOperation(operation: string) {
	return operation === "push_notification" || operation === "push_delivery";
}

export function A2AOperationConversation({
	log,
	showTimestamp = false,
	allowPushConversation = false,
}: {
	log: AgentLogOperation;
	showTimestamp?: boolean;
	allowPushConversation?: boolean;
}) {
	const timestamp = new Date(log.timestamp);
	const conversationOperation =
		isA2AConversationOperation(log.operation) || (allowPushConversation && isA2APushConversationOperation(log.operation));
	const messages = conversationOperation ? buildSessionContent(log) : [];
	const hasError = log.error_details !== undefined && log.error_details !== null;
	const taskState = latestTaskState(log);
	const hasFailedTaskState = taskState === "TASK_STATE_FAILED" || taskState === "TASK_STATE_REJECTED";

	if (!conversationOperation) return <ProtocolActivity log={log} showTimestamp={showTimestamp} />;

	const renderedTimestamp = showTimestamp ? (isValid(timestamp) ? format(timestamp, "MMM d, HH:mm:ss") : log.timestamp) : undefined;

	return (
		<div>
			{messages.map((item, index) => (
				<ConversationMessage
					key={item.key}
					item={item}
					timestamp={index === 0 ? renderedTimestamp : undefined}
					context={
						index === 0 && allowPushConversation && isA2APushConversationOperation(log.operation)
							? log.operation === "push_notification"
								? "Push notification received from Agent"
								: "Push notification sent to client callback"
							: undefined
					}
				/>
			))}
			{hasError ? <ConversationError error={log.error_details} timestamp={messages.length === 0 ? renderedTimestamp : undefined} /> : null}
			{!hasError && hasFailedTaskState && !messages.some((item) => item.role === "agent" && item.kind === "status") ? (
				<ConversationFailedResponse state={taskState} />
			) : null}
			{messages.length === 0 && !hasError && !hasFailedTaskState && log.status === "processing" ? (
				<div className="text-muted-foreground flex items-center justify-between gap-3 px-5 text-sm">
					<span>Waiting for a response.</span>
					{renderedTimestamp ? <span className="font-mono text-[10px]">{renderedTimestamp}</span> : null}
				</div>
			) : null}
		</div>
	);
}

function Operation({ log, current, onClick }: { log: AgentLogOperation; current: boolean; onClick: () => void }) {
	const handleKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
		if (event.target !== event.currentTarget || (event.key !== "Enter" && event.key !== " ")) return;
		event.preventDefault();
		onClick();
	};

	return (
		<div
			role="button"
			tabIndex={0}
			onClick={onClick}
			onKeyDown={handleKeyDown}
			className={cn(
				"group hover:bg-muted/40 focus-visible:bg-muted/40 focus-visible:ring-ring active:bg-muted/60 relative w-full cursor-pointer rounded-md px-3 py-3 text-left transition-colors focus-visible:ring-2 focus-visible:outline-none",
				current && "mt-2 border border-sky-400/45",
			)}
		>
			{current ? (
				<span className="bg-background pointer-events-none absolute -top-1.5 left-3 z-10 rounded-full border border-sky-400/45 px-1.5 py-0 text-[9px] leading-tight font-semibold tracking-wide text-sky-600 uppercase dark:text-sky-300">
					Current
				</span>
			) : null}

			<div className={cn(current && "pt-1")}>
				<A2AOperationConversation log={log} showTimestamp allowPushConversation={log.operation === "push_notification"} />
			</div>
		</div>
	);
}

export function AgentContextDetailsSheet({
	contextId,
	currentLogId,
	open,
	onOpenChange,
	onLogClick,
	onFilterContext,
}: AgentContextDetailsSheetProps) {
	const [triggerGetLogs] = useLazyGetAgentLogOperationsQuery();
	const [logs, setLogs] = useState<AgentLogOperation[]>([]);
	const [loading, setLoading] = useState(false);
	const [page, setPage] = useState(0);
	const [totalCount, setTotalCount] = useState(0);
	const [sortOrder, setSortOrder] = useState<"asc" | "desc">("asc");
	const [activeTab, setActiveTab] = useState({ correlationKey: "", value: "operations" });
	const [positionedSelection, setPositionedSelection] = useState<string | null>(null);
	const requestSeq = useRef(0);
	const positioningKey = `${contextId ?? ""}\x00${sortOrder}`;
	const filters = useMemo(() => CONTEXT_FILTERS(contextId || ""), [contextId]);
	const { data: stats } = useGetAgentLogStatsQuery({ filters }, { skip: !open || !contextId });
	const correlation = useMemo(() => (contextId ? { session_id: contextId } : null), [contextId]);
	const correlationKey = contextId ?? "";
	const { llmCount, mcpCount } = useCorrelatedLogCounts(correlation, open);

	const loadPage = useCallback(
		async (pageIndex: number) => {
			if (!contextId) return;
			const seq = ++requestSeq.current;
			const shouldPositionSelection = positionedSelection !== positioningKey;
			setLoading(true);
			try {
				const result = await triggerGetLogs({
					filters,
					selectedId: shouldPositionSelection ? (currentLogId ?? undefined) : undefined,
					pagination: {
						limit: CONTEXT_PAGE_SIZE,
						offset: pageIndex * CONTEXT_PAGE_SIZE,
						sort_by: "timestamp",
						order: sortOrder,
					},
				});
				if (seq !== requestSeq.current) return;
				if (result.error) {
					toast.error("Failed to load context", { description: getErrorMessage(result.error) });
					return;
				}
				if (result.data) {
					const pageData = result.data;
					if (shouldPositionSelection) {
						setPositionedSelection(positioningKey);
						const selectedPage = Math.floor((pageData.pagination.selected_offset ?? 0) / CONTEXT_PAGE_SIZE);
						if (selectedPage !== pageIndex) {
							setPage(selectedPage);
							return;
						}
					}
					setTotalCount(pageData.pagination.total_count);
					setLogs(pageData.logs);
				}
			} finally {
				if (seq === requestSeq.current) setLoading(false);
			}
		},
		[contextId, currentLogId, filters, positionedSelection, positioningKey, sortOrder, triggerGetLogs],
	);

	useEffect(() => {
		requestSeq.current += 1;
		setLoading(false);
		if (!open) {
			setPositionedSelection(null);
			return;
		}
		if (!contextId) return;
		setLogs([]);
		setPage(0);
		setTotalCount(0);
		setPositionedSelection(null);
	}, [contextId, open, sortOrder]);

	useEffect(() => {
		if (!open || !contextId) return;
		void loadPage(page);
	}, [contextId, loadPage, open, page]);

	const totalPages = Math.max(1, Math.ceil(totalCount / CONTEXT_PAGE_SIZE));

	const statsSummary = [
		{ label: "Operations", value: stats?.total_entries ?? 0, icon: Clock },
		{ label: "Successful", value: stats?.success_count ?? 0, icon: CheckCircle },
		{ label: "Failed", value: stats?.error_count ?? 0, icon: XCircle },
		{ label: "Avg duration", value: formatLatency(stats?.average_latency ?? 0), icon: Clock },
	] as const;

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="flex w-full flex-col gap-4 overflow-x-hidden p-4 sm:max-w-[60%] md:p-8">
				<SheetHeader className="flex min-w-0 flex-col items-start px-0" headerClassName="mb-0">
					<SheetTitle>Agent session</SheetTitle>
					<code className="text-muted-foreground block max-w-full truncate text-xs" title={contextId || undefined}>
						{contextId}
					</code>
				</SheetHeader>

				<div className="flex flex-wrap items-center justify-between gap-3">
					<div className="text-muted-foreground flex flex-wrap gap-x-4 gap-y-1 text-xs">
						{statsSummary.map(({ label, value, icon: Icon }) => (
							<span key={label} className="flex items-center gap-1.5">
								<Icon className="size-3.5" />
								{label} <span className="text-foreground font-mono">{value}</span>
							</span>
						))}
					</div>
					<div className="flex shrink-0 items-center gap-2">
						<Button variant="outline" size="sm" onClick={() => contextId && onFilterContext(contextId)}>
							<Filter className="size-4" /> Filter session
						</Button>
						<Button variant="outline" size="sm" onClick={() => setSortOrder((order) => (order === "asc" ? "desc" : "asc"))}>
							{sortOrder === "asc" ? <ArrowUp className="size-4" /> : <ArrowDown className="size-4" />}
							{sortOrder === "asc" ? "Earliest first" : "Latest first"}
						</Button>
					</div>
				</div>

				<Tabs
					key={contextId}
					defaultValue="operations"
					onValueChange={(value) => setActiveTab({ correlationKey, value })}
					className="min-h-0 flex-1 gap-2"
				>
					{llmCount > 0 || mcpCount > 0 ? (
						<TabsList className="bg-muted/60 h-10 w-fit">
							<TabsTrigger value="operations" className="px-3">
								Operations
							</TabsTrigger>
							{llmCount > 0 ? (
								<TabsTrigger value="llm-logs" className="px-3">
									LLM Logs{" "}
									<span className="bg-background text-muted-foreground ml-1.5 rounded-sm border px-2 py-0.5 text-[10px] tabular-nums">
										{llmCount}
									</span>
								</TabsTrigger>
							) : null}
							{mcpCount > 0 ? (
								<TabsTrigger value="mcp-logs" className="px-3">
									MCP Logs{" "}
									<span className="bg-background text-muted-foreground ml-1.5 rounded-sm border px-2 py-0.5 text-[10px] tabular-nums">
										{mcpCount}
									</span>
								</TabsTrigger>
							) : null}
						</TabsList>
					) : null}
					<TabsContent value="operations" className="flex min-h-0 flex-1 flex-col gap-3">
						<div className="custom-scrollbar min-h-0 flex-1 overflow-y-auto pr-1">
							{loading && logs.length === 0 ? (
								<div className="flex h-32 items-center justify-center gap-2 text-sm">
									<Loader2 className="size-4 animate-spin" /> Loading context...
								</div>
							) : logs.length ? (
								<div className="space-y-1">
									{logs.map((log) => (
										<Operation key={log.id} log={log} current={log.id === currentLogId} onClick={() => onLogClick(log)} />
									))}
								</div>
							) : (
								<div className="text-muted-foreground flex h-32 items-center justify-center text-sm">
									No operations found for this session.
								</div>
							)}
						</div>

						{totalCount > CONTEXT_PAGE_SIZE ? (
							<div className="flex items-center justify-between text-xs" data-testid="pagination">
								<div className="text-muted-foreground flex items-center gap-2">
									{(page * CONTEXT_PAGE_SIZE + 1).toLocaleString()}-{Math.min((page + 1) * CONTEXT_PAGE_SIZE, totalCount).toLocaleString()}{" "}
									of {totalCount.toLocaleString()} entries
								</div>
								<div className="flex items-center gap-2">
									<Button
										variant="ghost"
										size="sm"
										onClick={() => setPage((current) => Math.max(0, current - 1))}
										disabled={loading || page === 0}
										aria-label="Previous page"
									>
										<ChevronLeft className="size-3" />
									</Button>
									<div className="flex items-center gap-1">
										<span>Page</span>
										<span>{page + 1}</span>
										<span>of {totalPages}</span>
									</div>
									<Button
										variant="ghost"
										size="sm"
										onClick={() => setPage((current) => Math.min(totalPages - 1, current + 1))}
										disabled={loading || page >= totalPages - 1}
										aria-label="Next page"
									>
										<ChevronRight className="size-3" />
									</Button>
								</div>
							</div>
						) : null}
					</TabsContent>
					{correlation && llmCount > 0 ? (
						<CorrelatedLogsContent
							key={`llm:${correlationKey}`}
							kind="llm"
							correlation={correlation}
							active={activeTab.correlationKey === correlationKey && activeTab.value === "llm-logs"}
							sortOrder={sortOrder}
						/>
					) : null}
					{correlation && mcpCount > 0 ? (
						<CorrelatedLogsContent
							key={`mcp:${correlationKey}`}
							kind="mcp"
							correlation={correlation}
							active={activeTab.correlationKey === correlationKey && activeTab.value === "mcp-logs"}
							sortOrder={sortOrder}
						/>
					) : null}
				</Tabs>
			</SheetContent>
		</Sheet>
	);
}