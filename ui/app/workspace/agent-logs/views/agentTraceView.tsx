import { formatCost, formatLatency } from "@/app/workspace/dashboard/utils/chartUtils";
import { LogDetailView } from "@/app/workspace/logs/sheets/logDetailView";
import { MCPLogDetailView } from "@/app/workspace/mcp-logs/views/mcpLogDetailsSheet";
import {
	useGetAgentLogOperationByIdQuery,
	useLazyGetAgentLogOperationsQuery,
	useLazyGetAgentLogsQuery,
} from "@/lib/store/apis/agentLogsApi";
import { useGetLogByIdQuery, useLazyGetLogsQuery } from "@/lib/store/apis/logsApi";
import { useLazyGetMCPLogsQuery } from "@/lib/store/apis/mcpLogsApi";
import type { AgentLogOperation, AgentLogSummary } from "@/lib/types/agentLogs";
import type { LogEntry } from "@/lib/types/logs";
import { cn } from "@/lib/utils";
import { formatCompactNumber } from "@/lib/utils/numbers";
import { Link } from "@tanstack/react-router";
import { CopyInlineButton } from "@/app/workspace/logs/views/logHeroStat";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import {
	Bot,
	Braces,
	ChartNoAxesGantt,
	ChevronDown,
	ChevronRight,
	ExternalLink,
	FileText,
	Link as LinkIcon,
	ListTree,
	Loader2,
	MessagesSquare,
	Paperclip,
	Search,
	ShieldCheck,
	Sparkles,
	Wrench,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { parseAsStringLiteral, useQueryState } from "nuqs";
import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";
import { ArtifactView } from "./a2aPayloadView";
import {
	artifactType,
	buildTrace,
	createLimiter,
	type ArtifactKind,
	mergeOperations,
	operationPrompt,
	type TraceData,
	type TraceNode,
	type TraceNodeKind,
} from "./agentTrace";

const OPERATIONS_PAGE = { limit: 200, offset: 0, sort_by: "timestamp", order: "asc" } as const;
const CALLS_PAGE = { limit: 500, offset: 0, sort_by: "timestamp", order: "asc" } as const;

// Bifrost's tinted tiles: a soft fill with the icon in the kind's colour, readable in light and dark.
const kindStyles: Record<TraceNodeKind, { icon: typeof Bot; tile: string; bar: string }> = {
	session: { icon: MessagesSquare, tile: "bg-muted text-foreground", bar: "bg-zinc-400" },
	agent: { icon: Bot, tile: "bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-400", bar: "bg-emerald-500" },
	guard: { icon: ShieldCheck, tile: "bg-violet-50 text-violet-700 dark:bg-violet-950/40 dark:text-violet-400", bar: "bg-violet-500" },
	llm: { icon: Sparkles, tile: "bg-blue-50 text-blue-700 dark:bg-blue-950/40 dark:text-blue-400", bar: "bg-blue-500" },
	mcp: { icon: Wrench, tile: "bg-orange-50 text-orange-700 dark:bg-orange-950/40 dark:text-orange-400", bar: "bg-orange-500" },
	artifact: { icon: FileText, tile: "bg-rose-50 text-rose-700 dark:bg-rose-950/40 dark:text-rose-400", bar: "bg-rose-500" },
};

export type TraceScope = "trace" | "session";
// failed: a query for the task failed; the task is not marked loaded, so opening or selecting it again retries.
type TaskLoadState = "loading" | "loaded" | "failed";
// Pages followed per query before the trace is marked partial instead of loading without bound.
const MAX_PAGES = 10;
// While the trace is open, tasks still processing reload this often; finished tasks never reload.
const RUNNING_REFRESH_MS = 5000;

// allPages follows a query's pages until a short page or the reported total, at most MAX_PAGES; onCapped marks
// the trace partial when rows remain past the cap.
async function allPages<T>(
	fetchPage: (offset: number) => Promise<{ logs: T[]; pagination: { limit: number; total_count?: number } }>,
	onCapped: () => void,
): Promise<T[]> {
	const rows: T[] = [];
	for (let page = 0; ; page++) {
		const result = await fetchPage(rows.length);
		rows.push(...result.logs);
		const total = result.pagination.total_count;
		if (result.logs.length < result.pagination.limit || (total !== undefined && rows.length >= total)) return rows;
		if (page + 1 >= MAX_PAGES) {
			onCapped();
			return rows;
		}
	}
}

// Session skeleton page: metadata-only operation rows. Pages load oldest first as the tree scrolls, so a caller's
// operation always arrives before (or with) the operations it delegated.
const SUMMARY_PAGE = 50;
// Requests in flight at once while turns load, so opening many turns never floods the API.
const MAX_IN_FLIGHT = 4;

interface TraceLoader {
	data: TraceData;
	taskState: Record<string, TaskLoadState>;
	skeletonLoading: boolean; // a page of the session's turn list is loading
	hasMoreTurns: boolean; // the session has turns past the loaded pages
	loadMoreTurns: () => void; // loads the next page of the session's turn list
	loadTask: (taskId: string) => void; // loads a task's detail and, through it, every agent it delegated to
}

/**
 * Loads a trace progressively. Trace scope starts from the opened operation and loads its subtree at once.
 * Session scope pages the A2A context's operation summaries as the tree scrolls (enough to draw each turn and the
 * agents nested under it, since summaries carry caller_correlation_id), and loads a turn's detail (its events and
 * artifacts, its delegated operations, its LLM and MCP calls) only when the turn is opened or selected.
 */
function useAgentTraceData(root: AgentLogSummary, open: boolean, scope: TraceScope): TraceLoader {
	const { data: rootOperation, isError: rootError } = useGetAgentLogOperationByIdQuery(root.id, { skip: !open });
	const [fetchOperations] = useLazyGetAgentLogOperationsQuery();
	const [fetchSummaries] = useLazyGetAgentLogsQuery();
	const [fetchLLMLogs] = useLazyGetLogsQuery();
	const [fetchMCPLogs] = useLazyGetMCPLogsQuery();
	const [operations, setOperations] = useState<Map<string, AgentLogOperation>>(new Map());
	const [calls, setCalls] = useState<Pick<TraceData, "llm" | "mcp">>({ llm: [], mcp: [] });
	const [taskState, setTaskState] = useState<Record<string, TaskLoadState>>({});
	const [skeletonLoading, setSkeletonLoading] = useState(true);
	const [hasMoreTurns, setHasMoreTurns] = useState(false);
	const paging = useRef({ offset: 0, hasMore: false, busy: false, contextId: "" });
	const [partial, setPartial] = useState(false);
	// Bumped on every reset, so a response for a previous operation or scope is dropped.
	const generation = useRef(0);
	const requested = useRef(new Set<string>());
	const limit = useMemo(() => createLimiter(MAX_IN_FLIGHT), []);
	// The list hands over a fresh row object on every poll; the trace reloads only when the operation changes.
	const rootRef = useRef(root);
	rootRef.current = root;

	const loadTaskRef = useRef<(taskId: string, refresh?: boolean) => void>(() => {});
	loadTaskRef.current = (taskId: string, refresh = false) => {
		if (!taskId || requested.current.has(taskId)) return;
		requested.current.add(taskId);
		const gen = generation.current;
		// A refresh of a running task keeps showing what is loaded until the new rows arrive.
		if (!refresh) setTaskState((current) => ({ ...current, [taskId]: "loading" }));
		let failed = false;
		const settle = <T,>(request: Promise<T>, fallback: T) =>
			request.catch(() => {
				failed = true;
				if (gen === generation.current) setPartial(true);
				return fallback;
			});
		const capped = () => {
			if (gen === generation.current) setPartial(true);
		};
		// A refresh must reach the server: the cached page is the stale snapshot it replaces.
		const useCache = !refresh;
		// A request still queued when the trace closed or moved on never starts.
		const run = <T,>(request: () => Promise<T>) =>
			limit(() => (gen === generation.current ? request() : Promise.reject(new Error("trace closed"))));
		const operationsWhere = (filters: { task_id: string } | { caller_correlation_id: string }) =>
			allPages((offset) => run(() => fetchOperations({ filters, pagination: { ...OPERATIONS_PAGE, offset } }, useCache).unwrap()), capped);
		void Promise.all([
			settle(operationsWhere({ task_id: taskId }), [] as AgentLogOperation[]),
			settle(operationsWhere({ caller_correlation_id: taskId }), [] as AgentLogOperation[]),
			settle(
				allPages(
					(offset) =>
						run(() =>
							fetchLLMLogs({ filters: { agent_correlation_id: taskId }, pagination: { ...CALLS_PAGE, offset } }, useCache).unwrap(),
						),
					capped,
				).then((entries) => entries.map((entry) => ({ taskId, entry }))),
				[] as TraceData["llm"],
			),
			settle(
				allPages(
					(offset) =>
						run(() =>
							fetchMCPLogs({ filters: { agent_correlation_id: taskId }, pagination: { ...CALLS_PAGE, offset } }, useCache).unwrap(),
						),
					capped,
				).then((entries) => entries.map((entry) => ({ taskId, entry }))),
				[] as TraceData["mcp"],
			),
		]).then(([own, delegated, llm, mcp]) => {
			if (gen !== generation.current) return;
			setOperations((current) => mergeOperations(current, [...own, ...delegated], true));
			setCalls((current) => ({
				llm: [...current.llm.filter((call) => call.taskId !== taskId), ...llm],
				mcp: [...current.mcp.filter((call) => call.taskId !== taskId), ...mcp],
			}));
			if (failed) {
				// Not loaded: forget the request so opening or selecting the task again retries it.
				requested.current.delete(taskId);
				setTaskState((current) => ({ ...current, [taskId]: "failed" }));
			} else {
				setTaskState((current) => ({ ...current, [taskId]: "loaded" }));
			}
			// A delegated agent belongs to its caller's turn, so it loads with it.
			for (const operation of delegated) if (operation.task_id) loadTaskRef.current(operation.task_id);
		});
	};

	// A task still running when it loaded reloads until it finishes, so an open trace follows it.
	const operationsRef = useRef(operations);
	operationsRef.current = operations;
	const taskStateRef = useRef(taskState);
	taskStateRef.current = taskState;
	useEffect(() => {
		if (!open) return;
		const timer = setInterval(() => {
			const running = new Set<string>();
			for (const operation of operationsRef.current.values()) {
				if (operation.task_id && operation.status === "processing" && taskStateRef.current[operation.task_id] === "loaded")
					running.add(operation.task_id);
			}
			for (const taskId of running) {
				requested.current.delete(taskId);
				loadTaskRef.current(taskId, true);
			}
		}, RUNNING_REFRESH_MS);
		return () => clearInterval(timer);
	}, [open]);

	// Closing or unmounting the trace drops every response still in flight and stops queued requests.
	useEffect(
		() => () => {
			generation.current++;
		},
		[],
	);
	const loadTask = useCallback((taskId: string) => loadTaskRef.current(taskId), []);

	// loadPageRef fetches the next page of the session's turn list; it returns the page's rows, or null when there
	// was nothing to load, the request failed, or the trace moved on.
	const loadPageRef = useRef<() => Promise<AgentLogSummary[] | null>>(async () => null);
	loadPageRef.current = async () => {
		const state = paging.current;
		if (state.busy || !state.hasMore || !state.contextId) return null;
		state.busy = true;
		const gen = generation.current;
		setSkeletonLoading(true);
		const page = await fetchSummaries(
			{
				filters: { context_id: state.contextId, record_kind: ["request"] },
				pagination: { limit: SUMMARY_PAGE, offset: state.offset, sort_by: "timestamp", order: "asc" },
			},
			true,
		)
			.unwrap()
			.catch(() => null);
		if (gen !== generation.current) return null;
		state.busy = false;
		if (!page) {
			setPartial(true);
			setSkeletonLoading(false);
			return null;
		}
		setOperations((current) =>
			mergeOperations(
				current,
				page.logs.map((summary) => ({ ...summary, events: [] })),
				false,
			),
		);
		state.offset += page.logs.length;
		state.hasMore = page.logs.length > 0 && state.offset < page.pagination.total_count;
		setHasMoreTurns(state.hasMore);
		setSkeletonLoading(false);
		return page.logs;
	};
	const loadMoreTurns = useCallback(() => void loadPageRef.current(), []);

	useEffect(() => {
		if (!open) {
			generation.current++; // the sheet closed: drop what is in flight
			return;
		}
		if (!rootOperation && !rootError) return;
		generation.current++;
		const gen = generation.current;
		requested.current = new Set();
		const start: AgentLogOperation = rootOperation ?? { ...rootRef.current, events: [] };
		setCalls({ llm: [], mcp: [] });
		setTaskState({});
		setPartial(rootError);
		if (scope === "trace" || !start.context_id) {
			paging.current = { offset: 0, hasMore: false, busy: false, contextId: "" };
			setHasMoreTurns(false);
			setSkeletonLoading(false);
			setOperations(mergeOperations(new Map(), [start], true));
			if (start.task_id) loadTaskRef.current(start.task_id);
			return;
		}
		// The session fills in page by page; the opened operation joins when its page arrives, so it is numbered
		// against the turns before it rather than shown out of order.
		paging.current = { offset: 0, hasMore: true, busy: false, contextId: start.context_id };
		setOperations(new Map());
		(async () => {
			// Load pages until the opened operation's turn is on screen, then load that turn's detail.
			for (;;) {
				const rows = await loadPageRef.current();
				if (gen !== generation.current || rows === null) break;
				if (rows.some((row) => row.id === start.id) || !paging.current.hasMore) break;
			}
			if (gen === generation.current && start.task_id) loadTaskRef.current(start.task_id);
		})();
	}, [open, root.id, scope, rootOperation, rootError]);

	const data = useMemo<TraceData>(
		() => ({
			operations: [...operations.values()],
			llm: calls.llm,
			mcp: calls.mcp,
			loading: skeletonLoading || Object.values(taskState).includes("loading"),
			partial,
		}),
		[operations, calls, skeletonLoading, taskState, partial],
	);
	return { data, taskState, skeletonLoading, hasMoreTurns, loadMoreTurns, loadTask };
}

function EmbeddedLLMLog({ entry }: { entry: LogEntry }) {
	const { t } = useTranslation("observability");
	const { data, isLoading } = useGetLogByIdQuery({ id: entry.id, timestamp: entry.timestamp });
	if (isLoading && !data) return <DetailLoader />;
	return (
		<LogDetailView
			log={data ?? entry}
			variant="embedded"
			headerAction={<OpenInLogsLink to="/workspace/logs" id={entry.id} label={t("agentLogs.trace.openInLLMLogs")} />}
		/>
	);
}

function OpenInLogsLink({ to, id, label }: { to: "/workspace/logs" | "/workspace/mcp-logs"; id: string; label: string }) {
	return (
		<Link
			to={to}
			search={{ selected_log: id }}
			className="text-muted-foreground hover:text-foreground inline-flex shrink-0 items-center gap-1 rounded-sm border px-2 py-1 text-xs whitespace-nowrap"
			data-testid="agent-trace-open-in-logs"
		>
			{label}
			<ExternalLink className="size-3" />
		</Link>
	);
}

// artifactLabel is what an artifact card and span say it holds: a file's media type as-is, else a translated kind.
function artifactLabel(kind: ArtifactKind, t: TFunction<"observability">): string {
	return kind.kind === "file" ? kind.mediaType : t(`agentLogs.trace.artifactKind.${kind.kind}`);
}

// shortId keeps an ID readable in the topbar: the head and tail, with the full value on hover and in the copy.
function shortId(id: string, head?: number): string {
	if (head) return id.slice(0, head);
	return id.length > 18 ? `${id.slice(0, 8)}…${id.slice(-6)}` : id;
}

function Metric({ label, value }: { label: string; value: ReactNode }) {
	return (
		<span className="text-muted-foreground inline-flex items-baseline gap-1.5">
			{label}
			<b className="text-foreground font-semibold tabular-nums">{value}</b>
		</span>
	);
}

// LoadMoreSentinel loads the next page when it scrolls within reach of the tree's bottom edge. It is a real
// button too, so keyboard and screen-reader users can load more without scrolling.
function LoadMoreSentinel({
	scrollRoot,
	loading,
	onLoad,
	label,
}: {
	scrollRoot: React.RefObject<HTMLDivElement | null>;
	loading: boolean;
	onLoad: () => void;
	label: string;
}) {
	const ref = useRef<HTMLButtonElement>(null);
	const onLoadRef = useRef(onLoad);
	onLoadRef.current = onLoad;
	// Re-observed after every page: a page that added no new row leaves the sentinel in view, and a new observer
	// reports that at once, so paging continues without a click.
	useEffect(() => {
		const element = ref.current;
		if (loading || !element || typeof IntersectionObserver === "undefined") return;
		const observer = new IntersectionObserver((entries) => entries.some((entry) => entry.isIntersecting) && onLoadRef.current(), {
			root: scrollRoot.current,
			rootMargin: "0px 0px 300px 0px",
		});
		observer.observe(element);
		return () => observer.disconnect();
	}, [scrollRoot, loading]);
	return (
		<button
			ref={ref}
			type="button"
			onClick={onLoad}
			disabled={loading}
			className="text-muted-foreground hover:text-foreground hover:bg-muted/60 flex w-full items-center justify-center gap-2 rounded-sm px-2 py-2 text-xs"
			data-testid="agent-trace-load-more"
		>
			{loading && <Loader2 className="size-3 animate-spin" />}
			{label}
		</button>
	);
}

// ConversationTurn is one turn of the session read as a thread: the user's message on the right, then the agent's
// reply as plain text under a small header naming the agent. A turn whose detail has not loaded asks for it when it
// scrolls near the viewport, so replies fill in as you read.
function ConversationTurn({
	turn,
	prompt,
	reply,
	loaded,
	loading,
	metaText,
	artifacts,
	scrollRoot,
	onVisible,
	onViewSpans,
	onArtifact,
}: {
	turn: TraceNode;
	prompt: string;
	reply?: string;
	loaded: boolean;
	loading: boolean;
	metaText: string;
	artifacts: TraceNode[];
	scrollRoot: React.RefObject<HTMLDivElement | null>;
	onVisible: () => void;
	onViewSpans: () => void;
	onArtifact: (id: string) => void;
}) {
	const { t } = useTranslation("observability");
	const ref = useRef<HTMLDivElement>(null);
	const onVisibleRef = useRef(onVisible);
	onVisibleRef.current = onVisible;
	useEffect(() => {
		const element = ref.current;
		if (loaded || !element || typeof IntersectionObserver === "undefined") return;
		const observer = new IntersectionObserver(
			(entries) => {
				if (!entries.some((entry) => entry.isIntersecting)) return;
				onVisibleRef.current();
				observer.disconnect();
			},
			{ root: scrollRoot.current, rootMargin: "400px 0px" },
		);
		observer.observe(element);
		return () => observer.disconnect();
	}, [loaded, scrollRoot]);
	const failed = turn.status === "error";
	const agentStyle = kindStyles.agent;
	return (
		<div
			ref={ref}
			className="flex flex-col gap-3 [contain-intrinsic-size:auto_180px] [content-visibility:auto]"
			data-testid="agent-trace-turn"
		>
			<div className="flex flex-col items-end gap-1">
				<div className="bg-muted max-w-[80%] rounded-sm px-3.5 py-2.5 text-sm leading-relaxed whitespace-pre-wrap">{prompt}</div>
				<button type="button" onClick={onViewSpans} className="text-muted-foreground hover:text-foreground text-[11px]">
					{t("agentLogs.trace.turn", { value: turn.turn })}
				</button>
			</div>
			<div className="flex flex-col gap-1.5">
				<div className="text-muted-foreground flex items-center gap-2 text-[12px]">
					<span className={cn("flex size-5 shrink-0 items-center justify-center rounded-sm", agentStyle.tile)}>
						<Bot className="size-3" />
					</span>
					<button
						type="button"
						onClick={onViewSpans}
						className="text-foreground hover:text-foreground/80 inline-flex items-center gap-0.5 font-medium"
						aria-label={t("agentLogs.trace.viewSpans")}
					>
						{turn.name}
						<ChevronRight className="size-3.5" />
					</button>
					<span>{t("agentLogs.trace.agentSpan")}</span>
					<span className="tabular-nums">{formatLatency(turn.end - turn.start)}</span>
					{failed && <span className="text-chart-error-ink font-medium">{t("agentLogs.trace.error")}</span>}
				</div>
				{loaded ? (
					<div
						className={cn(
							"text-sm leading-relaxed whitespace-pre-wrap",
							!reply && "text-muted-foreground",
							failed && reply && "text-chart-error-ink",
						)}
					>
						{reply ?? t("agentLogs.trace.noReply")}
					</div>
				) : (
					<div className="space-y-1.5 py-1" aria-busy={loading}>
						<Skeleton className="h-3.5 w-4/5" />
						<Skeleton className="h-3.5 w-3/5" />
					</div>
				)}
				{artifacts.length > 0 && (
					<div className="flex flex-wrap gap-1.5 pt-0.5">
						{artifacts.map((artifact) => (
							<button
								key={artifact.id}
								type="button"
								onClick={() => onArtifact(artifact.id)}
								className="hover:bg-muted/60 inline-flex max-w-full items-center gap-1.5 rounded-sm border px-2 py-1 text-xs"
							>
								<FileText className="size-3 shrink-0 text-rose-600" />
								<span className="truncate">{artifact.name}</span>
							</button>
						))}
					</div>
				)}
				<div className="text-muted-foreground text-[11.5px] tabular-nums">{metaText}</div>
			</div>
		</div>
	);
}

function DetailLoader() {
	const { t } = useTranslation("observability");
	return (
		<div className="flex h-full items-center justify-center p-8">
			<span className="sr-only">{t("agentLogs.trace.loading")}</span>
			<Loader2 className="text-muted-foreground h-6 w-6 animate-spin" />
		</div>
	);
}

interface AgentTraceViewProps {
	root: AgentLogSummary;
	open: boolean;
	/** Left side of the header row, e.g. the sheet's prev/next buttons and title. */
	header?: ReactNode;
	/** Right end of the header row, after Copy link, e.g. the sheet's close button. */
	actions?: ReactNode;
	/** Renders an agent operation's detail; the sheet passes AgentLogDetailView so this module never imports it. */
	renderAgentDetail: (operation: AgentLogOperation) => ReactNode;
}

/**
 * AgentTraceView is the agent trace: a span tree of the operation, the agents it delegated to, their LLM and MCP
 * calls and the artifacts they produced, beside the existing detail view for whichever span is selected.
 */
export function AgentTraceView({ root, open, header, actions, renderAgentDetail }: AgentTraceViewProps) {
	const { t } = useTranslation("observability");
	// Trace shows this operation and what it delegated; Session shows every turn of its A2A conversation.
	// Kept in the URL so "Copy link" reopens the same scope.
	const [scope, setScope] = useQueryState(
		"agent_trace_scope",
		parseAsStringLiteral(["trace", "session"] as const)
			.withDefault("trace")
			.withOptions({ history: "replace" }),
	);
	const { copy: copyLink } = useCopyToClipboard({ successMessage: t("agentLogs.trace.linkCopied") });
	const traceId = scope === "session" ? root.context_id : root.task_id || root.id;
	const sessionId = scope === "session" ? root.context_id : undefined;
	const { data, taskState, skeletonLoading, hasMoreTurns, loadMoreTurns, loadTask } = useAgentTraceData(
		root,
		open,
		sessionId ? "session" : "trace",
	);
	const { nodes, rootNodeId } = useMemo(() => buildTrace(root.id, data, { sessionId }), [root.id, data, sessionId]);
	const rootNode = rootNodeId ? nodes.get(rootNodeId) : undefined;
	const [selectedId, setSelectedId] = useState<string | null>(null);
	const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
	// Session turns start collapsed and load when opened; everything else starts open.
	const [openTurns, setOpenTurns] = useState<Set<string>>(new Set());
	const taskOf = (node: TraceNode) => (node.kind === "agent" ? node.agent?.task_id : undefined);
	const isLoaded = (node: TraceNode) => {
		const taskId = taskOf(node);
		return !taskId || taskState[taskId] === "loaded";
	};
	const isOpen = (node: TraceNode) => (node.turn ? openTurns.has(node.id) : !collapsed.has(node.id));
	// Spans: the tree; Timeline: the tree with duration bars; Thread: the session read as a conversation.
	const [view, setView] = useState<"spans" | "timeline" | "thread">("spans");
	const showTimeline = view === "timeline";
	const treeRef = useRef<HTMLDivElement>(null);
	const detailRef = useRef<HTMLDivElement>(null);
	const [query, setQuery] = useState("");
	const selected = (selectedId && nodes.get(selectedId)) || rootNode;

	const traceStart = rootNode?.start ?? 0;
	const traceSpan = Math.max(1, Math.max(...[...nodes.values()].map((node) => node.end), traceStart + 1) - traceStart);
	const artifacts = [...nodes.values()].filter((node) => node.kind === "artifact");
	const counts = { agent: 0, llm: 0, mcp: 0, guard: 0 };
	for (const node of nodes.values())
		if (node.kind === "agent" || node.kind === "llm" || node.kind === "mcp" || node.kind === "guard") counts[node.kind]++;
	const turns = [...nodes.values()].filter((node) => node.turn).sort((a, b) => (a.turn ?? 0) - (b.turn ?? 0));
	// Session totals add up loaded turns only, so the topbar says how many are in.
	const loadedTurns = turns.filter((node) => isLoaded(node)).length;

	// Rows in tree order. A search shows each matching span with the ancestors that lead to it.
	const rows = useMemo(() => {
		if (!rootNodeId) return [];
		const needle = query.trim().toLowerCase();
		const keep = new Set<string>();
		if (needle) {
			for (const node of nodes.values()) {
				if (!node.name.toLowerCase().includes(needle)) continue;
				for (let id: string | null = node.id; id; id = nodes.get(id)?.parentId ?? null) keep.add(id);
			}
		}
		const out: { node: TraceNode; depth: number }[] = [];
		const walk = (id: string, depth: number) => {
			const node = nodes.get(id)!;
			if (needle && !keep.has(id)) return;
			out.push({ node, depth });
			const expanded = node.turn ? openTurns.has(id) : !collapsed.has(id);
			if (needle || expanded) node.children.forEach((childId) => walk(childId, depth + 1));
		};
		walk(rootNodeId, 0);
		return out;
	}, [nodes, rootNodeId, collapsed, openTurns, query]);

	// Loading follows what is on screen: an opened or selected agent loads its detail and its delegated agents.
	const ensureLoaded = (node: TraceNode) => {
		const taskId = taskOf(node);
		if (taskId) loadTask(taskId);
	};

	const toggle = (node: TraceNode) => {
		const opening = !isOpen(node);
		const flip = (current: Set<string>) => {
			const next = new Set(current);
			if (next.has(node.id)) next.delete(node.id);
			else next.add(node.id);
			return next;
		};
		if (node.turn) setOpenTurns(flip);
		else setCollapsed(flip);
		if (opening) ensureLoaded(node);
	};

	const select = (node: TraceNode) => {
		setSelectedId(node.id);
		ensureLoaded(node);
	};

	// Selecting from the artifacts strip opens every collapsed ancestor so the row is visible.
	const reveal = (id: string) => {
		const ancestors: TraceNode[] = [];
		for (let parent = nodes.get(id)?.parentId ?? null; parent; parent = nodes.get(parent)?.parentId ?? null)
			ancestors.push(nodes.get(parent)!);
		setCollapsed((current) => new Set([...current].filter((item) => !ancestors.some((node) => node.id === item))));
		setOpenTurns((current) => new Set([...current, ...ancestors.filter((node) => node.turn).map((node) => node.id)]));
		const target = nodes.get(id);
		if (target) select(target);
		// Thread mode always shows the conversation, so revealing a span or artifact switches to Spans to show it.
		if (view === "thread") setView("spans");
	};

	const meta = (node: TraceNode): string => {
		const parts: string[] = [];
		if (node.kind === "session") {
			parts.push(
				t("agentLogs.trace.turnCount", { value: hasMoreTurns ? `${turns.length}+` : turns.length }),
				formatLatency(node.end - node.start),
			);
			if (node.tokens) parts.push(t("agentLogs.trace.tokensShort", { value: formatCompactNumber(node.tokens) }));
			if (node.cost) parts.push(formatCost(node.cost));
		} else if (node.kind === "agent") {
			parts.push(node.agent?.operation ?? "", formatLatency(node.end - node.start));
			if (node.tokens) parts.push(t("agentLogs.trace.tokensShort", { value: formatCompactNumber(node.tokens) }));
			if (node.cost) parts.push(formatCost(node.cost));
			if (node.followUps) parts.push(t("agentLogs.trace.followUps", { value: node.followUps }));
			if (node.errors) parts.push(t("agentLogs.trace.errorsShort", { value: node.errors }));
		} else if (node.kind === "llm") {
			parts.push(formatLatency(node.end - node.start), t("agentLogs.trace.tokensShort", { value: formatCompactNumber(node.tokens) }));
			if (node.cost) parts.push(formatCost(node.cost));
		} else if (node.kind === "guard" && node.guard) {
			parts.push(node.guard.phase, node.guard.outcome);
			if (node.guard.ruleName && node.guard.ruleName !== node.name) parts.push(node.guard.ruleName);
		} else if (node.kind === "mcp") {
			if (node.mcp?.server_label) parts.push(node.mcp.server_label);
			parts.push(formatLatency(node.end - node.start));
		} else {
			if (node.artifact) parts.push(artifactLabel(artifactType(node.artifact.artifact), t));
			parts.push(t("agentLogs.trace.producedBy", { agent: node.producer ?? "" }));
		}
		return parts.filter(Boolean).join(", ");
	};

	const detail = (() => {
		if (!selected) return <DetailLoader />;
		if (selected.kind === "session" || (scope === "session" && view === "thread"))
			return (
				<div className="mx-auto flex max-w-3xl flex-col gap-8 py-2" data-testid="agent-trace-conversation">
					{turns.map((turn) => {
						const taskId = taskOf(turn);
						return (
							<ConversationTurn
								key={turn.id}
								turn={turn}
								prompt={operationPrompt(turn.agent?.input) || turn.name}
								reply={turn.reply}
								loaded={isLoaded(turn)}
								loading={Boolean(taskId && taskState[taskId] === "loading")}
								metaText={[
									turn.tokens ? t("agentLogs.trace.tokensShort", { value: formatCompactNumber(turn.tokens) }) : "",
									turn.cost ? formatCost(turn.cost) : "",
								]
									.filter(Boolean)
									.join(", ")}
								artifacts={turn.children.map((id) => nodes.get(id)!).filter((child) => child.kind === "artifact")}
								scrollRoot={detailRef}
								onVisible={() => ensureLoaded(turn)}
								onViewSpans={() => reveal(turn.id)}
								onArtifact={(id) => reveal(id)}
							/>
						);
					})}
					{!hasMoreTurns && turns.length > 0 && (
						<div className="text-muted-foreground flex items-center gap-3 text-xs" data-testid="agent-trace-conversation-end">
							<span className="bg-border h-px flex-1" aria-hidden="true" />
							{t("agentLogs.trace.conversationEnd")}
							<span className="bg-border h-px flex-1" aria-hidden="true" />
						</div>
					)}
					{hasMoreTurns && (
						<LoadMoreSentinel
							key={turns.length}
							scrollRoot={detailRef}
							loading={skeletonLoading}
							onLoad={loadMoreTurns}
							label={t("agentLogs.trace.loadMoreTurns")}
						/>
					)}
				</div>
			);
		if (selected.kind === "agent" && selected.agent) return renderAgentDetail(selected.agent);
		if (selected.kind === "llm" && selected.llm) return <EmbeddedLLMLog key={selected.id} entry={selected.llm} />;
		if (selected.kind === "mcp" && selected.mcp)
			return (
				<MCPLogDetailView
					key={selected.id}
					log={selected.mcp}
					variant="embedded"
					headerEnd={<OpenInLogsLink to="/workspace/mcp-logs" id={selected.mcp.id} label={t("agentLogs.trace.openInMCPLogs")} />}
				/>
			);
		if (selected.kind === "guard" && selected.guard) {
			const parent = selected.parentId ? nodes.get(selected.parentId) : undefined;
			const fields: [string, string | undefined][] = [
				[t("agentLogs.trace.guard.guardrail"), selected.guard.guardrailName],
				[t("agentLogs.trace.guard.rule"), selected.guard.ruleName],
				[t("agentLogs.trace.guard.provider"), selected.guard.provider],
				[t("agentLogs.trace.guard.phase"), selected.guard.phase],
				[t("agentLogs.trace.guard.outcome"), selected.guard.outcome],
				[t("agentLogs.trace.guard.judgeModel"), selected.guard.judgeModel],
				[t("agentLogs.trace.guard.assessments"), selected.guard.assessments?.join(", ")],
			];
			return (
				<div className="space-y-3" data-testid="agent-trace-guard-detail">
					<div className="flex items-center justify-between gap-2">
						<span className="text-sm font-medium">{selected.name}</span>
						{parent?.llm && <OpenInLogsLink to="/workspace/logs" id={parent.llm.id} label={t("agentLogs.trace.openInLLMLogs")} />}
					</div>
					<div className="rounded-sm border">
						<div className="grid grid-cols-1 gap-x-6 gap-y-3 p-4 sm:grid-cols-2">
							{fields
								.filter(([, value]) => value)
								.map(([label, value]) => (
									<div key={label} className="min-w-0">
										<div className="text-muted-foreground text-[10.5px] font-semibold tracking-wider uppercase">{label}</div>
										<div className="mt-0.5 truncate font-mono text-[13px]">{value}</div>
									</div>
								))}
						</div>
						{selected.guard.reason && (
							<div className="border-t p-4 text-sm">
								<div className="text-muted-foreground mb-1 text-[10.5px] font-semibold tracking-wider uppercase">
									{t("agentLogs.trace.guard.reason")}
								</div>
								{selected.guard.reason}
							</div>
						)}
					</div>
				</div>
			);
		}
		if (selected.kind === "artifact" && selected.artifact)
			return (
				<div className="space-y-3" data-testid="agent-trace-artifact-detail">
					<div className="text-muted-foreground text-xs">{t("agentLogs.trace.producedBy", { agent: selected.producer ?? "" })}</div>
					<ArtifactView artifact={selected.artifact.artifact} />
				</div>
			);
		return null;
	})();

	return (
		<div className="flex h-full min-h-0 flex-col" data-testid="agent-trace-view">
			<div className="border-b" data-testid="agent-trace-topbar">
				<div className="flex items-center gap-3 px-4 pt-3 pb-2">
					{header}
					{traceId && (
						<div
							className="bg-muted/50 flex min-w-0 items-center gap-1.5 rounded-sm py-0.5 pr-0.5 pl-2 text-xs"
							data-testid="agent-trace-id"
						>
							<span className="text-muted-foreground shrink-0 text-[10.5px] font-semibold tracking-wider uppercase">
								{scope === "session" ? t("agentLogs.trace.sessionId") : t("agentLogs.trace.taskId")}
							</span>
							<code className="truncate font-mono text-[12px]" title={traceId}>
								{shortId(traceId)}
							</code>
							<CopyInlineButton text={traceId} testId="agent-trace-copy-id" />
						</div>
					)}
					<div className="ml-auto flex shrink-0 items-center gap-1">
						<button
							type="button"
							onClick={() => copyLink(window.location.href)}
							className="text-muted-foreground hover:text-foreground hover:bg-muted inline-flex h-7 items-center gap-1.5 rounded-sm px-2 text-xs"
							data-testid="agent-trace-copy-link"
						>
							<LinkIcon className="size-3.5" />
							{t("agentLogs.trace.copyLink")}
						</button>
						{actions}
					</div>
				</div>
				{rootNode && (
					<div className="flex flex-wrap items-center gap-x-4 gap-y-1.5 px-4 pb-3 text-xs" data-testid="agent-trace-summary">
						<Metric label={t("agentLogs.trace.latency")} value={formatLatency(rootNode.end - rootNode.start)} />
						<Metric label={t("agentLogs.trace.tokens")} value={formatCompactNumber(rootNode.tokens)} />
						<Metric label={t("agentLogs.trace.cost")} value={formatCost(rootNode.cost)} />
						<span className="bg-border h-3.5 w-px" aria-hidden="true" />
						<Metric label={t("agentLogs.trace.agents")} value={counts.agent} />
						<Metric label={t("agentLogs.trace.llmCalls")} value={counts.llm} />
						<Metric label={t("agentLogs.trace.mcpCalls")} value={counts.mcp} />
						{counts.guard > 0 && <Metric label={t("agentLogs.trace.guardrails")} value={counts.guard} />}
						{scope === "session" && (skeletonLoading || hasMoreTurns || loadedTurns < turns.length) && (
							<span className="text-muted-foreground inline-flex items-center gap-1.5" data-testid="agent-trace-turn-progress">
								{skeletonLoading && <Loader2 className="size-3 animate-spin" />}
								{t("agentLogs.trace.turnsLoaded", { loaded: loadedTurns, total: hasMoreTurns ? `${turns.length}+` : turns.length })}
							</span>
						)}
						{rootNode.errors > 0 && (
							<span className="bg-chart-error/10 text-chart-error-ink border-chart-error/30 inline-flex items-center gap-1.5 rounded-sm border px-1.5 py-0.5 font-medium">
								<span className="bg-chart-error size-1.5 rounded-sm" aria-hidden="true" />
								{t("agentLogs.trace.errors")} {rootNode.errors}
							</span>
						)}
					</div>
				)}
			</div>
			<div className="flex h-11 shrink-0 items-center gap-1 border-b px-3" role="tablist" data-testid="agent-trace-views">
				{(
					[
						["spans", ListTree, t("agentLogs.trace.spansTab")],
						["timeline", ChartNoAxesGantt, t("agentLogs.trace.timeline")],
						...(scope === "session" ? ([["thread", MessagesSquare, t("agentLogs.trace.threadTab")]] as const) : []),
					] as const
				).map(([value, Icon, label]) => (
					<button
						key={value}
						type="button"
						role="tab"
						aria-selected={view === value}
						onClick={() => setView(value)}
						className={cn(
							"inline-flex h-8 items-center gap-1.5 rounded-sm px-2.5 text-[13px] font-medium",
							view === value ? "bg-muted text-foreground" : "text-muted-foreground hover:text-foreground",
						)}
					>
						<Icon className="size-3.5" />
						{label}
					</button>
				))}
			</div>
			<div className="flex min-h-0 flex-1 flex-col md:flex-row">
				<div className="flex max-h-[45vh] min-h-0 w-full shrink-0 flex-col border-b md:max-h-none md:w-[380px] md:border-r md:border-b-0">
					<div className="flex items-center gap-2 px-3 pt-2.5 pb-1.5">
						{root.context_id && (
							<Tabs
								value={scope}
								onValueChange={(value) => {
									void setScope(value as TraceScope);
									setSelectedId(null);
									setCollapsed(new Set());
									setOpenTurns(new Set());
									if (value === "trace" && view === "thread") setView("spans");
								}}
								className="shrink-0"
							>
								<TabsList className="h-8" data-testid="agent-trace-scope">
									<TabsTrigger value="trace" className="px-2.5 text-xs">
										{t("agentLogs.trace.scopeTrace")}
									</TabsTrigger>
									<TabsTrigger value="session" className="px-2.5 text-xs">
										{t("agentLogs.trace.scopeSession")}
									</TabsTrigger>
								</TabsList>
							</Tabs>
						)}
						<label className="text-muted-foreground flex min-w-0 flex-1 items-center gap-1.5 rounded-sm border px-2 py-1">
							<Search className="size-3.5 shrink-0" />
							<span className="sr-only">{t("agentLogs.trace.findSpan")}</span>
							<input
								type="search"
								value={query}
								onChange={(event) => setQuery(event.target.value)}
								placeholder={t("agentLogs.trace.findSpan")}
								className="text-foreground min-w-0 flex-1 bg-transparent text-xs outline-none"
								data-testid="agent-trace-find"
							/>
						</label>
					</div>
					<div ref={treeRef} className="min-h-0 flex-1 overflow-y-auto p-1.5" role="tree" aria-label={t("agentLogs.trace.treeLabel")}>
						{data.loading && rows.length <= 1 && (
							<div className="text-muted-foreground flex items-center gap-2 px-2 py-1.5 text-xs">
								<Loader2 className="size-3 animate-spin" />
								{t("agentLogs.trace.loading")}
							</div>
						)}
						{rows.map(({ node, depth }) => {
							const style = kindStyles[node.kind];
							const Icon = style.icon;
							const open = isOpen(node);
							const isSelected = selected?.id === node.id;
							const taskId = taskOf(node);
							const loading = Boolean(taskId && taskState[taskId] === "loading");
							// An agent whose detail has not loaded yet may still have calls and delegated agents to show.
							const expandable = node.children.length > 0 || !isLoaded(node);
							return (
								<div
									key={node.id}
									className="relative [contain-intrinsic-size:auto_40px] [content-visibility:auto]"
									style={{ paddingLeft: depth * 16 }}
									role="treeitem"
									aria-selected={isSelected}
									aria-expanded={expandable ? open : undefined}
								>
									{depth > 0 && (
										<span className="bg-border absolute top-0 bottom-0 w-px" style={{ left: depth * 16 - 4 }} aria-hidden="true" />
									)}
									<div className={cn("flex items-center rounded-sm pr-1", isSelected ? "bg-muted" : "hover:bg-muted/60")}>
										<button
											type="button"
											onClick={() => select(node)}
											className="flex min-w-0 flex-1 items-center gap-2 px-2 py-1 text-left"
											data-testid={`agent-trace-node-${node.kind}`}
										>
											<span className={cn("flex size-6 shrink-0 items-center justify-center rounded-sm", style.tile)}>
												<Icon className="size-3.5" />
											</span>
											<span className="flex min-w-0 flex-1 flex-col">
												<span className="flex min-w-0 items-center gap-1.5">
													<span className="truncate text-[13px] font-medium">
														{node.kind === "session" ? t("agentLogs.trace.sessionName", { value: node.name.slice(0, 8) }) : node.name}
													</span>
													{node.turn ? (
														<span className="bg-background text-muted-foreground shrink-0 rounded-sm border px-1 text-[10px] font-medium">
															{t("agentLogs.trace.turn", { value: node.turn })}
														</span>
													) : null}
													{node.kind === "agent" && node.parentId && !node.turn && (
														<span className="bg-background text-muted-foreground shrink-0 rounded-sm border px-1 text-[10px] font-medium">
															{t("agentLogs.trace.a2a")}
														</span>
													)}
													{node.status === "error" && (
														<span className="bg-chart-error/10 text-chart-error-ink shrink-0 rounded-sm px-1 text-[10px] font-semibold uppercase">
															{t("agentLogs.trace.error")}
														</span>
													)}
												</span>
												<span className="text-muted-foreground truncate text-[11.5px] tabular-nums">{meta(node)}</span>
											</span>
											{showTimeline && (
												<span className="relative h-1.5 w-20 shrink-0" aria-hidden="true">
													<span
														className={cn("absolute inset-y-0 rounded-r-[1.5px]", node.status === "error" ? "bg-chart-error" : style.bar)}
														style={{
															left: `${((node.start - traceStart) / traceSpan) * 100}%`,
															width: `${Math.max(((node.end - node.start) / traceSpan) * 100, 2)}%`,
														}}
													/>
												</span>
											)}
										</button>
										{expandable && (
											<button
												type="button"
												onClick={() => toggle(node)}
												className="text-muted-foreground hover:text-foreground hover:bg-background flex size-7 shrink-0 items-center justify-center rounded-sm"
												aria-label={t(open ? "agentLogs.trace.collapse" : "agentLogs.trace.expand", { name: node.name })}
											>
												{loading ? (
													<Loader2 className="size-3.5 animate-spin" />
												) : open ? (
													<ChevronDown className="size-4" />
												) : (
													<ChevronRight className="size-4" />
												)}
											</button>
										)}
									</div>
								</div>
							);
						})}
						{query.trim() && rows.length === 0 && (
							<div className="text-muted-foreground px-2 py-1.5 text-xs">{t("agentLogs.trace.noMatches")}</div>
						)}
						{scope === "session" && hasMoreTurns && !query.trim() && (
							// Remounted per page so a page too short to fill the pane still pulls the next one.
							<LoadMoreSentinel
								key={turns.length}
								scrollRoot={treeRef}
								loading={skeletonLoading}
								onLoad={loadMoreTurns}
								label={t("agentLogs.trace.loadMoreTurns")}
							/>
						)}
						{data.partial && !data.loading && (
							<div className="text-muted-foreground px-2 py-1.5 text-xs" data-testid="agent-trace-partial">
								{t("agentLogs.trace.partial")}
							</div>
						)}
					</div>
					{artifacts.length > 0 && (
						<div className="bg-card shrink-0 border-t" data-testid="agent-trace-artifacts">
							<div className="text-muted-foreground flex items-center gap-1.5 px-3 pt-2.5 pb-2 text-[11px] font-medium">
								<Paperclip className="size-3.5" />
								{t("agentLogs.trace.artifacts")}
								<span className="bg-muted text-foreground rounded-sm px-1.5 tabular-nums">{artifacts.length}</span>
							</div>
							<div className="flex gap-2 overflow-x-auto px-3 pb-3">
								{artifacts.map((node) => {
									const type: ArtifactKind = node.artifact ? artifactType(node.artifact.artifact) : { kind: "text" };
									const TypeIcon = type.kind === "data" ? Braces : FileText;
									return (
										<button
											key={node.id}
											type="button"
											onClick={() => reveal(node.id)}
											className={cn(
												"bg-background hover:bg-muted/60 flex w-52 shrink-0 items-center gap-2.5 rounded-sm border p-2 text-left transition",
												selected?.id === node.id && "border-rose-300 ring-1 ring-rose-200 dark:border-rose-800 dark:ring-rose-900",
											)}
											aria-pressed={selected?.id === node.id}
										>
											<span className="flex size-8 shrink-0 items-center justify-center rounded-sm bg-rose-50 text-rose-600 dark:bg-rose-950/40 dark:text-rose-400">
												<TypeIcon className="size-4" />
											</span>
											<span className="flex min-w-0 flex-col">
												<span className="truncate text-xs font-medium">{node.name}</span>
												<span className="text-muted-foreground truncate text-[11px]">
													{node.producer} · {artifactLabel(type, t)}
												</span>
											</span>
										</button>
									);
								})}
							</div>
						</div>
					)}
				</div>
				<div ref={detailRef} className="min-h-0 min-w-0 flex-1 overflow-y-auto p-4 md:p-5" data-testid="agent-trace-detail">
					{detail}
				</div>
			</div>
		</div>
	);
}