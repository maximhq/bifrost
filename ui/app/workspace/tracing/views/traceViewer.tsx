import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Sheet, SheetClose, SheetContent, SheetDescription, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { LogDetailView } from "@/app/workspace/logs/sheets/logDetailView";
import { CopyInlineButton } from "@/app/workspace/logs/views/logHeroStat";
import { MCPLogDetailView } from "@/app/workspace/mcp-logs/views/mcpLogDetailsSheet";
import { SheetNavigationButtons } from "@/components/sheetNavigationButtons";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { useSheetNavigation } from "@/hooks/useSheetNavigation";
import { getErrorMessage, useDeleteTraceMutation, useGetTraceQuery } from "@/lib/store";
import { useGetLogByIdQuery } from "@/lib/store/apis/logsApi";
import { useGetMCPLogByIdQuery } from "@/lib/store/apis/mcpLogsApi";
import type { TraceDetail, TraceSpan } from "@/lib/types/traces";
import { cn } from "@/lib/utils";
import { Link } from "@tanstack/react-router";
import {
	ChartNoAxesGantt,
	ChevronDown,
	ChevronRight,
	Copy,
	ExternalLink,
	EyeOff,
	Link2,
	Link as LinkIcon,
	ListTree,
	Loader2,
	MessagesSquare,
	Search,
	Sparkles,
	Trash2,
	X,
} from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { buildThread, buildTraceTree, messagesOf, spanUsage, type TraceTreeNode } from "../traceTree";
import { formatCost, formatDuration, formatTokens, prettyValue, spanKindMeta } from "./format";

interface TraceViewerProps {
	projectId: string | null;
	traceId: string | null;
	initialSpanId?: string | null;
	onClose: () => void;
	/** Prev and next trace of the list behind the sheet, as the log sheets step through their rows. */
	hasPrev?: boolean;
	hasNext?: boolean;
	onNavigate?: (direction: "prev" | "next") => void;
	/** Extra tabs of the span detail pane (review, activity), added by the page. */
	renderSpanExtras?: (detail: TraceDetail, span: TraceSpan) => { tabs: { id: string; label: string; content: React.ReactNode }[] };
}

// Width of the timeline bar track. The scale header uses the same class so its labels line up.
const TIMELINE_TRACK = "w-32";

function shortId(id: string): string {
	return id.length > 18 ? `${id.slice(0, 8)}…${id.slice(-6)}` : id;
}

function Summary({ label, value }: { label: string; value: React.ReactNode }) {
	return (
		<span className="text-muted-foreground inline-flex items-baseline gap-1.5">
			{label}
			<b className="text-foreground font-semibold tabular-nums">{value}</b>
		</span>
	);
}

function OpenInLogsLink({ to, id, label }: { to: "/workspace/logs" | "/workspace/mcp-logs"; id: string; label: string }) {
	return (
		<Link
			to={to}
			search={{ selected_log: id }}
			className="text-muted-foreground hover:text-foreground inline-flex shrink-0 items-center gap-1 rounded-sm border px-2 py-1 text-xs whitespace-nowrap"
			data-testid="trace-open-in-logs"
		>
			{label}
			<ExternalLink className="size-3" />
		</Link>
	);
}

/** A span this gateway served opens the gateway's own log, exactly as Agent Logs embeds it. */
function EmbeddedGatewayLLMLog({ logId }: { logId: string }) {
	const { t } = useTranslation("observability");
	const { data, isLoading, isError } = useGetLogByIdQuery({ id: logId });
	if (isLoading) return <DetailLoader />;
	if (isError || !data) return null;
	return (
		<LogDetailView
			log={data}
			variant="embedded"
			headerAction={<OpenInLogsLink to="/workspace/logs" id={logId} label={t("agentLogs.trace.openInLLMLogs")} />}
		/>
	);
}

function EmbeddedGatewayMCPLog({ logId }: { logId: string }) {
	const { t } = useTranslation("observability");
	const { data, isLoading, isError } = useGetMCPLogByIdQuery(logId);
	if (isLoading) return <DetailLoader />;
	if (isError || !data) return null;
	return (
		<MCPLogDetailView
			log={data}
			variant="embedded"
			headerEnd={<OpenInLogsLink to="/workspace/mcp-logs" id={logId} label={t("agentLogs.trace.openInMCPLogs")} />}
		/>
	);
}

function DetailLoader() {
	return (
		<div className="flex h-full items-center justify-center p-8">
			<Loader2 className="text-muted-foreground h-6 w-6 animate-spin" />
		</div>
	);
}

/**
 * The trace view, laid out as the Agent Logs trace sheet is: a top bar with the trace id and its
 * totals, Spans, Timeline and Thread tabs, the span tree on the left and the selected span on the
 * right. A model or tool call this gateway served opens the gateway's own log view.
 */
export function TraceViewer({
	projectId,
	traceId,
	initialSpanId,
	onClose,
	hasPrev = false,
	hasNext = false,
	onNavigate,
	renderSpanExtras,
}: TraceViewerProps) {
	const { t } = useTranslation("observability");
	const open = Boolean(projectId && traceId);
	const { data, isLoading, error } = useGetTraceQuery(
		{ projectId: projectId ?? "", traceId: traceId ?? "" },
		// A trace that is still arriving is polled until its root span lands.
		{ skip: !open, pollingInterval: open ? 5000 : 0, skipPollingIfUnfocused: true },
	);
	const [deleteTrace] = useDeleteTraceMutation();
	const { copy: copyLink } = useCopyToClipboard({ successMessage: t("agentLogs.trace.linkCopied") });
	const { prev: prevKeys, next: nextKeys } = useSheetNavigation({
		enabled: open,
		hasPrev,
		hasNext,
		onNavigate: (direction) => onNavigate?.(direction),
	});
	const [selectedId, setSelectedId] = useState<string | null>(null);
	const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
	const [showHTTP, setShowHTTP] = useState(false);
	const [view, setView] = useState<"spans" | "timeline" | "thread">("spans");
	const [query, setQuery] = useState("");

	const tree = useMemo(() => buildTraceTree(data?.spans ?? [], showHTTP), [data?.spans, showHTTP]);
	const thread = useMemo(() => buildThread(data?.spans ?? []), [data?.spans]);
	const hiddenHTTP = useMemo(() => (data?.spans ?? []).filter((span) => span.kind === "http").length, [data?.spans]);

	useEffect(() => {
		setSelectedId(initialSpanId ?? null);
		setCollapsed(new Set());
		setQuery("");
	}, [traceId, initialSpanId]);

	const selected = useMemo(
		() => (data?.spans ?? []).find((span) => span.span_id === selectedId) ?? tree.rows[0]?.span ?? null,
		[data?.spans, selectedId, tree.rows],
	);

	// Rows in tree order. A search shows each matching span with the ancestors that lead to it.
	const visibleRows = useMemo(() => {
		const needle = query.trim().toLowerCase();
		const keep = new Set<string>();
		if (needle) {
			const parentOf = new Map(tree.rows.map((node) => [node.span.span_id, node]));
			const byChild = new Map<string, string>();
			for (const node of tree.rows) for (const child of node.children) byChild.set(child.span.span_id, node.span.span_id);
			for (const node of tree.rows) {
				if (!(node.span.name || "").toLowerCase().includes(needle)) continue;
				for (let id: string | undefined = node.span.span_id; id && parentOf.has(id); id = byChild.get(id)) keep.add(id);
			}
		}
		const rows: TraceTreeNode[] = [];
		const walk = (node: TraceTreeNode) => {
			if (needle && !keep.has(node.span.span_id)) return;
			rows.push(node);
			if (needle || !collapsed.has(node.span.span_id)) node.children.forEach(walk);
		};
		tree.roots.forEach(walk);
		return rows;
	}, [tree, collapsed, query]);

	const toggle = (id: string) =>
		setCollapsed((current) => {
			const next = new Set(current);
			if (next.has(id)) next.delete(id);
			else next.add(id);
			return next;
		});

	const trace = data?.trace;
	const counts = { agent: 0, llm: 0, tool: 0 };
	for (const span of data?.spans ?? []) {
		if (span.kind === "agent") counts.agent++;
		else if (span.kind === "llm" || span.kind === "embedding") counts.llm++;
		else if (span.kind === "tool") counts.tool++;
	}
	const links = data?.links ?? [];
	const linkOf = (span: TraceSpan) => (span.linked_log_expired ? undefined : links.find((link) => link.span_id === span.span_id));

	// A model call is named by its model, as Agent Logs names it; its span name moves to the meta line.
	const label = (span: TraceSpan) =>
		((span.kind === "llm" || span.kind === "embedding") && (span.link_snapshot?.model || span.model)) || span.name || span.span_id;
	const meta = (node: TraceTreeNode) => {
		const { span } = node;
		const named = label(span) !== span.name ? span.name : "";
		const parts = [
			named || (span.kind === "unknown" ? "" : span.kind),
			span.partial && !span.end_time ? t("tracing.inProgress") : formatDuration(span.duration_ms),
		];
		if (node.subtreeTokens) parts.push(t("agentLogs.trace.tokensShort", { value: formatTokens(node.subtreeTokens) }));
		if (node.subtreeCost) parts.push(`${spanUsage(span).estimated ? "~" : ""}${formatCost(node.subtreeCost)}`);
		return parts.filter(Boolean).join(", ");
	};

	const detail = (() => {
		if (!data) return null;
		if (view === "thread")
			return (
				<div className="mx-auto flex max-w-3xl flex-col gap-6 py-2" data-testid="trace-viewer-thread">
					{thread.length === 0 && <p className="text-muted-foreground text-sm">{t("tracing.viewer.noThread")}</p>}
					{thread.map((message, index) =>
						message.role === "user" ? (
							<div
								key={`${message.spanId}-${index}`}
								className="flex flex-col items-end gap-1"
								data-testid={`trace-viewer-thread-message-${index}`}
							>
								<div className="bg-muted max-w-[80%] rounded-sm px-3.5 py-2.5 text-sm leading-relaxed whitespace-pre-wrap">
									{message.text}
								</div>
							</div>
						) : (
							<div
								key={`${message.spanId}-${index}`}
								className="flex flex-col gap-1.5"
								data-testid={`trace-viewer-thread-message-${index}`}
							>
								<button
									type="button"
									onClick={() => {
										setSelectedId(message.spanId);
										setView("spans");
									}}
									className="text-muted-foreground hover:text-foreground flex items-center gap-2 text-[12px]"
								>
									<span className={cn("flex size-5 shrink-0 items-center justify-center rounded-sm", spanKindMeta("llm").tile)}>
										<Sparkles className="size-3" />
									</span>
									<span className="font-medium capitalize">{message.role}</span>
									<ChevronRight className="size-3.5" />
								</button>
								<div className="text-sm leading-relaxed whitespace-pre-wrap">{message.text}</div>
							</div>
						),
					)}
				</div>
			);
		if (!selected) return <div className="text-muted-foreground p-6 text-sm">{t("tracing.viewer.noSpans")}</div>;
		const link = linkOf(selected);
		if (link?.log_kind === "llm") return <EmbeddedGatewayLLMLog key={link.log_id} logId={link.log_id} />;
		if (link?.log_kind === "mcp") return <EmbeddedGatewayMCPLog key={link.log_id} logId={link.log_id} />;
		return <SpanDetail span={selected} detail={data} extras={renderSpanExtras?.(data, selected)} />;
	})();

	return (
		<Sheet open={open} onOpenChange={(next) => !next && onClose()}>
			<SheetContent
				className="border-secondary flex w-full flex-col gap-0 overflow-hidden border p-0 sm:max-w-[90%] 2xl:max-w-[80%]"
				data-testid="trace-viewer"
			>
				<div className="border-b" data-testid="trace-viewer-topbar">
					<div className="flex items-center gap-3 px-4 pt-3 pb-2">
						<div className="flex min-w-0 items-center gap-2">
							<SheetNavigationButtons
								hasPrev={hasPrev}
								hasNext={hasNext}
								onNavigate={(direction) => onNavigate?.(direction)}
								prevKeys={prevKeys}
								nextKeys={nextKeys}
								entityLabel="trace"
							/>
							<SheetTitle className="text-foreground truncate text-sm font-medium">{t("tracing.viewer.title")}</SheetTitle>
							<SheetDescription className="sr-only">{trace?.name || t("tracing.viewer.untitled")}</SheetDescription>
						</div>
						{traceId && (
							<div
								className="bg-muted/50 flex min-w-0 items-center gap-1.5 rounded-sm py-0.5 pr-0.5 pl-2 text-xs"
								data-testid="trace-viewer-id"
							>
								<span className="text-muted-foreground shrink-0 text-[10.5px] font-semibold tracking-wider uppercase">
									{t("tracing.viewer.traceId")}
								</span>
								<code className="truncate font-mono text-[12px]" title={traceId}>
									{shortId(traceId)}
								</code>
								<CopyInlineButton text={traceId} testId="trace-viewer-copy-id" />
							</div>
						)}
						{trace?.in_progress && (
							<span className="text-muted-foreground inline-flex items-center gap-1.5 text-xs">
								<Loader2 className="size-3 animate-spin" />
								{t("tracing.inProgress")}
							</span>
						)}
						<div className="ml-auto flex shrink-0 items-center gap-1">
							<button
								type="button"
								onClick={() => copyLink(window.location.href)}
								className="text-muted-foreground hover:text-foreground hover:bg-muted inline-flex h-7 items-center gap-1.5 rounded-sm px-2 text-xs"
								data-testid="trace-viewer-copy-link"
							>
								<LinkIcon className="size-3.5" />
								{t("agentLogs.trace.copyLink")}
							</button>
							<Button
								variant="ghost"
								className="size-7"
								type="button"
								aria-label={t("tracing.settings.deleteTrace")}
								data-testid="trace-viewer-delete-button"
								disabled={!trace}
								onClick={async () => {
									if (!trace || !window.confirm(t("tracing.settings.deleteTraceConfirm"))) return;
									try {
										await deleteTrace({ projectId: trace.project_id, traceId: trace.trace_id }).unwrap();
										onClose();
									} catch (deleteError) {
										toast.error(getErrorMessage(deleteError));
									}
								}}
							>
								<Trash2 className="h-3.5 w-3.5" />
							</Button>
							<SheetClose asChild>
								<Button variant="ghost" className="size-7" type="button" aria-label={t("labels.close")} data-testid="trace-viewer-close">
									<X className="h-3.5 w-3.5" />
								</Button>
							</SheetClose>
						</div>
					</div>
					{/* The same counters in the same order on every trace, zeros included, so moving between
					    traces never reflows the header. The row keeps its height while a trace loads. */}
					<div className="flex min-h-5 flex-wrap items-center gap-x-4 gap-y-1.5 px-4 pb-3 text-xs" data-testid="trace-viewer-summary">
						{trace && (
							<>
								<Summary label={t("agentLogs.trace.latency")} value={formatDuration(trace.duration_ms)} />
								<Summary label={t("agentLogs.trace.tokens")} value={formatTokens(trace.total_tokens)} />
								<Summary
									label={t("agentLogs.trace.cost")}
									value={
										trace.unpriced_span_count > 0 && !trace.cost ? (
											<Tooltip>
												<TooltipTrigger className="underline decoration-dotted">{t("tracing.unpriced")}</TooltipTrigger>
												<TooltipContent>{t("tracing.unpricedCalls", { count: trace.unpriced_span_count })}</TooltipContent>
											</Tooltip>
										) : (
											`${trace.estimated_cost ? "~" : ""}${formatCost(trace.cost)}`
										)
									}
								/>
								<span className="bg-border h-3.5 w-px" aria-hidden="true" />
								<Summary label={t("tracing.viewer.spansLabel")} value={trace.span_count} />
								<Summary label={t("agentLogs.trace.agents")} value={counts.agent} />
								<Summary label={t("agentLogs.trace.llmCalls")} value={counts.llm} />
								<Summary label={t("tracing.viewer.toolCalls")} value={counts.tool} />
								<Summary label={t("tracing.viewer.viaGateway")} value={`${trace.linked_span_count}/${trace.llm_span_count}`} />
								<span
									className={cn(
										"inline-flex items-baseline gap-1.5",
										trace.error_count > 0 ? "text-chart-error-ink" : "text-muted-foreground",
									)}
									data-testid="trace-viewer-errors"
								>
									<span
										className={cn("size-1.5 self-center rounded-sm", trace.error_count > 0 ? "bg-chart-error" : "bg-border")}
										aria-hidden="true"
									/>
									{t("agentLogs.trace.errors")}
									<b className="font-semibold tabular-nums">{trace.error_count}</b>
								</span>
							</>
						)}
					</div>
				</div>
				<div className="flex h-11 shrink-0 items-center gap-1 border-b px-3" role="tablist" data-testid="trace-viewer-views">
					{(
						[
							["spans", ListTree, t("agentLogs.trace.spansTab")],
							["timeline", ChartNoAxesGantt, t("agentLogs.trace.timeline")],
							["thread", MessagesSquare, t("agentLogs.trace.threadTab")],
						] as const
					).map(([value, Icon, label]) => (
						<button
							key={value}
							type="button"
							role="tab"
							aria-selected={view === value}
							onClick={() => setView(value)}
							data-testid={`trace-viewer-${value}-tab`}
							className={cn(
								"inline-flex h-8 items-center gap-1.5 rounded-sm px-2.5 text-[13px] font-medium",
								view === value ? "bg-muted text-foreground" : "text-muted-foreground hover:text-foreground",
							)}
						>
							<Icon className="size-3.5" />
							{label}
						</button>
					))}
					{hiddenHTTP > 0 && (
						<label className="text-muted-foreground ml-auto flex items-center gap-2 text-xs">
							<Switch checked={showHTTP} onCheckedChange={setShowHTTP} data-testid="trace-viewer-show-http-switch" />
							{t("tracing.viewer.showHTTP", { count: hiddenHTTP })}
						</label>
					)}
				</div>
				{isLoading && <DetailLoader />}
				{Boolean(error) && !data && (
					<div className="text-destructive flex flex-1 items-center justify-center text-sm">{t("tracing.viewer.loadFailed")}</div>
				)}
				{data && (
					<div className="flex min-h-0 flex-1 flex-col md:flex-row">
						<div className="flex max-h-[45vh] min-h-0 w-full shrink-0 flex-col border-b md:max-h-none md:w-[380px] md:border-r md:border-b-0">
							<div className="flex items-center gap-2 px-3 pt-2.5 pb-1.5">
								<label className="text-muted-foreground flex min-w-0 flex-1 items-center gap-1.5 rounded-sm border px-2 py-1">
									<Search className="size-3.5 shrink-0" />
									<span className="sr-only">{t("agentLogs.trace.findSpan")}</span>
									<input
										type="search"
										value={query}
										onChange={(event) => setQuery(event.target.value)}
										placeholder={t("agentLogs.trace.findSpan")}
										className="text-foreground min-w-0 flex-1 bg-transparent text-xs outline-none"
										data-testid="trace-viewer-find"
									/>
								</label>
							</div>
							<div className="min-h-0 flex-1 overflow-y-auto p-1.5" role="tree" aria-label={t("agentLogs.trace.treeLabel")}>
								{view === "timeline" && tree.durationMs > 0 && (
									// Same box model as a row: button padding, then the bar track, then the chevron slot,
									// so "0" and the total sit exactly over the ends of every bar.
									<div className="text-muted-foreground flex items-center pr-1 pb-1 text-[10.5px] tabular-nums" aria-hidden="true">
										<div className="flex min-w-0 flex-1 justify-end px-2">
											<div className={cn("flex justify-between", TIMELINE_TRACK)}>
												<span>0</span>
												<span>{formatDuration(tree.durationMs)}</span>
											</div>
										</div>
										<span className="size-7 shrink-0" />
									</div>
								)}
								{visibleRows.map((node) => {
									const { span } = node;
									const style = spanKindMeta(span.kind);
									const Icon = style.icon;
									const isSelected = selected?.span_id === span.span_id && view !== "thread";
									const isOpen = !collapsed.has(span.span_id);
									const usage = spanUsage(span);
									return (
										<div
											key={span.span_id}
											className="relative [contain-intrinsic-size:auto_40px] [content-visibility:auto]"
											style={{ paddingLeft: node.depth * 16 }}
											role="treeitem"
											aria-selected={isSelected}
											aria-expanded={node.children.length > 0 ? isOpen : undefined}
										>
											{node.depth > 0 && (
												<span className="bg-border absolute top-0 bottom-0 w-px" style={{ left: node.depth * 16 - 4 }} aria-hidden="true" />
											)}
											<div className={cn("flex items-center rounded-sm pr-1", isSelected ? "bg-muted" : "hover:bg-muted/60")}>
												<button
													type="button"
													onClick={() => {
														setSelectedId(span.span_id);
														if (view === "thread") setView("spans");
													}}
													className="flex min-w-0 flex-1 items-center gap-2 px-2 py-1 text-left"
													data-testid={`trace-span-row-${span.span_id}`}
												>
													<span className={cn("flex size-6 shrink-0 items-center justify-center rounded-sm", style.tile)}>
														<Icon className="size-3.5" />
													</span>
													<span className="flex min-w-0 flex-1 flex-col">
														<span className="flex min-w-0 items-center gap-1.5">
															<span className="truncate text-[13px] font-medium">{label(span)}</span>
															{usage.linked && (
																<span className="bg-background text-muted-foreground shrink-0 rounded-sm border px-1 text-[10px] font-medium">
																	{t("tracing.viewer.gatewayBadge")}
																</span>
															)}
															{node.orphan && (
																<span className="bg-background text-muted-foreground shrink-0 rounded-sm border px-1 text-[10px] font-medium">
																	{t("tracing.viewer.partial")}
																</span>
															)}
															{span.status === "error" && (
																<span className="bg-chart-error/10 text-chart-error-ink shrink-0 rounded-sm px-1 text-[10px] font-semibold uppercase">
																	{t("agentLogs.trace.error")}
																</span>
															)}
														</span>
														<span className="text-muted-foreground truncate text-[11.5px] tabular-nums">{meta(node)}</span>
													</span>
													{view === "timeline" && (
														<span className={cn("bg-muted/70 relative h-1.5 shrink-0", TIMELINE_TRACK)} aria-hidden="true">
															<span
																className={cn(
																	"absolute inset-y-0 overflow-hidden rounded-r-[1.5px]",
																	span.status === "error" ? "bg-chart-error" : style.bar,
																)}
																style={{ left: `${node.offset * 100}%`, width: `${Math.max(node.width * 100, 2)}%` }}
															>
																{/* The hatched share is the part of the prompt read from cache. */}
																{usage.cachedShare > 0 && (
																	<span
																		className="block h-full opacity-60"
																		style={{
																			width: `${usage.cachedShare * 100}%`,
																			backgroundImage:
																				"repeating-linear-gradient(135deg, rgba(255,255,255,.75) 0 2px, transparent 2px 4px)",
																		}}
																	/>
																)}
															</span>
														</span>
													)}
												</button>
												{node.children.length > 0 ? (
													<button
														type="button"
														onClick={() => toggle(span.span_id)}
														className="text-muted-foreground hover:text-foreground hover:bg-background flex size-7 shrink-0 items-center justify-center rounded-sm"
														aria-label={t(isOpen ? "agentLogs.trace.collapse" : "agentLogs.trace.expand", { name: span.name })}
														data-testid={`trace-span-toggle-${span.span_id}`}
													>
														{isOpen ? <ChevronDown className="size-4" /> : <ChevronRight className="size-4" />}
													</button>
												) : (
													// Leaves keep the chevron slot so every bar track ends at the same x.
													<span className="size-7 shrink-0" aria-hidden="true" />
												)}
											</div>
										</div>
									);
								})}
								{query.trim() && visibleRows.length === 0 && (
									<div className="text-muted-foreground px-2 py-1.5 text-xs">{t("agentLogs.trace.noMatches")}</div>
								)}
							</div>
						</div>
						<div className="min-h-0 min-w-0 flex-1 overflow-y-auto p-4 md:p-5" data-testid="trace-viewer-detail">
							{detail}
						</div>
					</div>
				)}
			</SheetContent>
		</Sheet>
	);
}

function ValueBlock({ label, value, testId }: { label: string; value?: string; testId: string }) {
	const { copy } = useCopyToClipboard();
	if (!value) return null;
	const pretty = prettyValue(value);
	return (
		<section className="space-y-1">
			<div className="flex items-center justify-between">
				<h4 className="text-muted-foreground text-xs font-medium tracking-wide uppercase">{label}</h4>
				<Button variant="ghost" size="icon" aria-label={`Copy ${label}`} data-testid={`${testId}-copy-button`} onClick={() => copy(pretty)}>
					<Copy className="h-3.5 w-3.5" />
				</Button>
			</div>
			<pre
				data-testid={testId}
				className="bg-muted/50 max-h-[420px] overflow-auto rounded-md border p-3 text-xs break-words whitespace-pre-wrap"
			>
				{pretty}
			</pre>
		</section>
	);
}

function Metric({ label, value }: { label: string; value: React.ReactNode }) {
	return (
		<div className="min-w-0">
			<div className="text-muted-foreground text-[10.5px] font-semibold tracking-wider uppercase">{label}</div>
			<div className="mt-0.5 truncate font-mono text-[13px] tabular-nums">{value}</div>
		</div>
	);
}

/**
 * A span's input and output. A message list reads as the conversation it is, as Agent Logs shows an
 * operation: each message under its role, the reply last. Anything else is shown as JSON or text.
 */
function Conversation({ span }: { span: TraceSpan }) {
	const { t } = useTranslation("observability");
	const input = messagesOf(span.input);
	const output = messagesOf(span.output);
	if (input.length === 0 && output.length === 0) {
		return (
			<>
				<ValueBlock label={t("tracing.columns.input")} value={span.input} testId="trace-span-input" />
				<ValueBlock label={t("tracing.columns.output")} value={span.output} testId="trace-span-output" />
			</>
		);
	}
	const reply = output.length > 0 ? output : span.output ? [{ role: "assistant", text: span.output }] : [];
	return (
		<div className="flex flex-col gap-3" data-testid="trace-span-conversation">
			{input.length === 0 && <ValueBlock label={t("tracing.columns.input")} value={span.input} testId="trace-span-input" />}
			{[...input, ...reply].map((message, index) => (
				<div key={index} className="flex flex-col gap-1">
					<span className="text-muted-foreground text-[10.5px] font-semibold tracking-wider uppercase">{message.role}</span>
					<div
						className={cn(
							"rounded-sm px-3.5 py-2.5 text-sm leading-relaxed break-words whitespace-pre-wrap",
							message.role === "user" ? "bg-muted" : message.role === "system" ? "border border-dashed" : "border",
						)}
					>
						{message.text}
					</div>
				</div>
			))}
		</div>
	);
}

function SpanDetail({
	span,
	detail,
	extras,
}: {
	span: TraceSpan;
	detail: TraceDetail;
	extras?: { tabs: { id: string; label: string; content: React.ReactNode }[] };
}) {
	const { t } = useTranslation("observability");
	const meta = spanKindMeta(span.kind);
	const Icon = meta.icon;
	const usage = spanUsage(span);
	const links = (detail.links ?? []).filter((link) => link.span_id === span.span_id);
	const scores = (detail.scores ?? []).filter((score) => (score.span_id || detail.trace.root_span_id) === span.span_id);
	const model = span.link_snapshot?.model || span.model;
	const hasMetadata = !!span.metadata && Object.keys(span.metadata).length > 0;
	// The Details tab shows content only. A span with none (an OTLP span with only numbers) opens on Raw.
	const hasDetails = !!(span.error || span.input || span.output || span.expected || span.tags?.length) || scores.length > 0 || hasMetadata;
	const [tab, setTab] = useState("details");
	const activeTab = tab === "details" && !hasDetails ? "raw" : tab;
	const unpriced =
		(usage.cost === undefined || usage.cost === null) && usage.tokens > 0 && (span.kind === "llm" || span.kind === "embedding");
	const hasCost = (usage.cost !== undefined && usage.cost !== null) || unpriced;
	return (
		<div className="space-y-4" data-testid="trace-span-detail">
			<div className="flex items-center justify-between gap-2">
				<span className="text-sm font-medium first-letter:uppercase">
					{t("tracing.viewer.spanHeading", { kind: span.kind === "llm" ? "LLM" : span.kind })}
				</span>
			</div>
			{span.status === "error" && (
				<span className="bg-chart-error/10 text-chart-error-ink border-chart-error/30 inline-flex items-center gap-1.5 rounded-sm border px-1.5 py-0.5 text-xs font-medium">
					<span className="bg-chart-error size-1.5 rounded-sm" aria-hidden="true" />
					{t("tracing.viewer.error")}
				</span>
			)}
			<div className="grid grid-cols-1 gap-x-6 gap-y-3 rounded-sm border p-4 sm:grid-cols-2" data-testid="trace-span-identity">
				<div className="flex min-w-0 items-center gap-2 sm:col-span-2">
					<span className={cn("flex size-6 shrink-0 items-center justify-center rounded-sm", meta.tile)}>
						<Icon className="size-3.5" />
					</span>
					<span className="truncate font-medium">{span.name || span.span_id}</span>
					{span.partial && <Badge variant="secondary">{t("tracing.viewer.partial")}</Badge>}
				</div>
				{(
					[
						[t("tracing.filters.spanKind"), span.kind],
						[t("tracing.columns.model"), model],
						[t("tracing.viewer.spanId"), span.span_id],
						[t("tracing.viewer.parentSpan"), span.parent_span_id],
						[t("filters.sessionID"), span.session_id],
						[t("tracing.sessions.user"), span.user_id],
					] as [string, string | undefined][]
				)
					.filter(([, value]) => value)
					.map(([fieldLabel, value]) => (
						<Metric key={fieldLabel} label={fieldLabel} value={value} />
					))}
			</div>

			{/* Only the numbers this span has: a missing value is left out, not shown as "-". */}
			<div className="grid grid-cols-2 gap-x-6 gap-y-3 rounded-sm border p-4 md:grid-cols-4" data-testid="trace-span-metrics">
				<Metric label={t("tracing.columns.duration")} value={formatDuration(span.duration_ms)} />
				{usage.tokens > 0 && <Metric label={t("tracing.columns.tokens")} value={formatTokens(usage.tokens)} />}
				{hasCost && (
					<Metric
						label={t("tracing.columns.cost")}
						value={
							unpriced ? (
								<Tooltip>
									<TooltipTrigger className="underline decoration-dotted">{t("tracing.unpriced")}</TooltipTrigger>
									<TooltipContent>{t("tracing.unpricedHint")}</TooltipContent>
								</Tooltip>
							) : (
								<>
									{formatCost(usage.cost)}
									{usage.estimated && <span className="text-muted-foreground ml-1 text-xs">{t("tracing.estimated")}</span>}
								</>
							)
						}
					/>
				)}
				{span.time_to_first_token_ms !== undefined && span.time_to_first_token_ms !== null && (
					<Metric label={t("tracing.viewer.firstToken")} value={formatDuration(span.time_to_first_token_ms)} />
				)}
			</div>

			{links.length > 0 && (
				<div className="bg-muted/40 flex flex-wrap items-center gap-2 rounded-md border p-2 text-xs" data-testid="trace-span-gateway-link">
					<Link2 className="h-3.5 w-3.5" />
					<span>{t("tracing.viewer.servedByGateway", { method: span.link_method ?? links[0].method })}</span>
					{span.linked_log_expired ? (
						<span className="text-muted-foreground">{t("tracing.viewer.linkExpired")}</span>
					) : (
						links.map((link) => (
							<Link
								key={link.log_id}
								to={link.log_kind === "mcp" ? "/workspace/mcp-logs" : "/workspace/logs"}
								search={{ request_id: link.log_id } as never}
								data-testid={`trace-span-gateway-log-link-${link.log_id}`}
								className="text-primary underline"
							>
								{link.log_kind === "mcp" ? t("tracing.viewer.openMCPLog") : t("tracing.viewer.openLLMLog")}
							</Link>
						))
					)}
				</div>
			)}

			{span.content_hidden && (
				<div className="text-muted-foreground flex items-center gap-2 text-xs">
					<EyeOff className="h-3.5 w-3.5" />
					{t("tracing.viewer.contentHidden")}
				</div>
			)}

			<Tabs value={activeTab} onValueChange={setTab}>
				<TabsList>
					{hasDetails && (
						<TabsTrigger value="details" data-testid="trace-span-details-tab">
							{t("tracing.viewer.detailsTab")}
						</TabsTrigger>
					)}
					<TabsTrigger value="raw" data-testid="trace-span-raw-tab">
						{t("tracing.viewer.rawTab")}
					</TabsTrigger>
					{extras?.tabs.map((tab) => (
						<TabsTrigger key={tab.id} value={tab.id} data-testid={`trace-span-${tab.id}-tab`}>
							{tab.label}
						</TabsTrigger>
					))}
				</TabsList>
				<TabsContent value="details" className="space-y-4">
					{span.error && <ValueBlock label={t("tracing.viewer.error")} value={span.error} testId="trace-span-error" />}
					<Conversation span={span} />
					<ValueBlock label={t("tracing.viewer.expected")} value={span.expected} testId="trace-span-expected" />
					{scores.length > 0 && (
						<section className="space-y-1">
							<h4 className="text-muted-foreground text-xs font-medium tracking-wide uppercase">{t("tracing.columns.scores")}</h4>
							<div className="flex flex-wrap gap-2">
								{scores.map((score) => (
									<Badge key={score.id} variant="outline" className="gap-1">
										{score.name}
										<span className="tabular-nums">
											{score.value !== undefined && score.value !== null ? score.value.toFixed(2) : score.string_value}
										</span>
									</Badge>
								))}
							</div>
						</section>
					)}
					{span.tags && span.tags.length > 0 && (
						<div className="flex flex-wrap gap-1">
							{span.tags.map((tag) => (
								<Badge key={tag} variant="secondary">
									{tag}
								</Badge>
							))}
						</div>
					)}
					{hasMetadata && (
						<ValueBlock label={t("tracing.viewer.metadata")} value={JSON.stringify(span.metadata)} testId="trace-span-metadata" />
					)}
				</TabsContent>
				<TabsContent value="raw" className="space-y-4">
					<ValueBlock
						label={t("tracing.viewer.identifiers")}
						value={JSON.stringify({
							trace_id: span.trace_id,
							span_id: span.span_id,
							parent_span_ids: span.parent_span_ids,
							row_id: span.row_id,
							link_method: span.link_method,
							session_id: span.session_id,
							user_id: span.user_id,
							environment: span.environment,
							experiment_id: span.experiment_id,
						})}
						testId="trace-span-identifiers"
					/>
					{span.link_snapshot && (
						<ValueBlock
							label={t("tracing.viewer.gatewayNumbers")}
							value={JSON.stringify(span.link_snapshot)}
							testId="trace-span-gateway-numbers"
						/>
					)}
					<ValueBlock
						label={t("tracing.viewer.sdkNumbers")}
						value={JSON.stringify({
							model: span.model,
							provider: span.provider,
							prompt_tokens: span.prompt_tokens,
							completion_tokens: span.completion_tokens,
							total_tokens: span.total_tokens,
							cached_read_tokens: span.cached_read_tokens,
							cost: span.cost,
							cost_estimated: span.cost_estimated,
						})}
						testId="trace-span-sdk-numbers"
					/>
					{span.attributes && Object.keys(span.attributes).length > 0 && (
						<ValueBlock label={t("tracing.viewer.attributes")} value={JSON.stringify(span.attributes)} testId="trace-span-attributes" />
					)}
				</TabsContent>
				{extras?.tabs.map((tab) => (
					<TabsContent key={tab.id} value={tab.id}>
						{tab.content}
					</TabsContent>
				))}
			</Tabs>
		</div>
	);
}