import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { getErrorMessage, useDeleteTraceMutation, useGetTraceQuery } from "@/lib/store";
import type { TraceDetail, TraceSpan } from "@/lib/types/traces";
import { cn } from "@/lib/utils";
import { Link } from "@tanstack/react-router";
import { AlertCircle, ChevronDown, ChevronRight, Copy, EyeOff, Link2, Loader2, Maximize2, Minimize2, Trash2 } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { buildThread, buildTraceTree, spanUsage, type TraceTreeNode } from "../traceTree";
import { formatCost, formatDuration, formatTokens, prettyValue, spanKindMeta } from "./format";

interface TraceViewerProps {
	projectId: string | null;
	traceId: string | null;
	initialSpanId?: string | null;
	onClose: () => void;
	/** Extra tabs of the span detail pane (review, activity), added by the page. */
	renderSpanExtras?: (detail: TraceDetail, span: TraceSpan) => { tabs: { id: string; label: string; content: React.ReactNode }[] };
}

/**
 * The trace view: a header with the trace's rollups, the span tree (or its timeline, or the
 * conversation it holds) on the left, and the selected span on the right.
 */
export function TraceViewer({ projectId, traceId, initialSpanId, onClose, renderSpanExtras }: TraceViewerProps) {
	const { t } = useTranslation("observability");
	const open = Boolean(projectId && traceId);
	const { data, isLoading, error } = useGetTraceQuery(
		{ projectId: projectId ?? "", traceId: traceId ?? "" },
		// A trace that is still arriving is polled until its root span lands.
		{ skip: !open, pollingInterval: open ? 5000 : 0, skipPollingIfUnfocused: true },
	);
	const [deleteTrace] = useDeleteTraceMutation();
	const [selectedId, setSelectedId] = useState<string | null>(null);
	const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
	const [showHTTP, setShowHTTP] = useState(false);
	const [fullscreen, setFullscreen] = useState(false);
	const [leftTab, setLeftTab] = useState("spans");

	const tree = useMemo(() => buildTraceTree(data?.spans ?? [], showHTTP), [data?.spans, showHTTP]);
	const thread = useMemo(() => buildThread(data?.spans ?? []), [data?.spans]);
	const hiddenHTTP = useMemo(() => (data?.spans ?? []).filter((span) => span.kind === "http").length, [data?.spans]);

	useEffect(() => {
		setSelectedId(initialSpanId ?? null);
		setCollapsed(new Set());
	}, [traceId, initialSpanId]);

	const selected = useMemo(() => {
		const spans = data?.spans ?? [];
		return spans.find((span) => span.span_id === selectedId) ?? tree.rows[0]?.span ?? null;
	}, [data?.spans, selectedId, tree.rows]);

	const visibleRows = useMemo(() => {
		const rows: TraceTreeNode[] = [];
		const walk = (node: TraceTreeNode) => {
			rows.push(node);
			if (!collapsed.has(node.span.span_id)) node.children.forEach(walk);
		};
		tree.roots.forEach(walk);
		return rows;
	}, [tree.roots, collapsed]);

	const toggle = (id: string) =>
		setCollapsed((current) => {
			const next = new Set(current);
			if (next.has(id)) next.delete(id);
			else next.add(id);
			return next;
		});

	const trace = data?.trace;
	return (
		<Sheet open={open} onOpenChange={(next) => !next && onClose()}>
			<SheetContent
				side="right"
				data-testid="trace-viewer"
				className={cn("flex flex-col gap-0 p-0 sm:max-w-none", fullscreen ? "w-screen" : "w-[92vw] lg:w-[80vw]")}
			>
				<SheetHeader className="border-b px-4 py-3">
					<div className="flex items-center gap-2 pr-8">
						<SheetTitle className="truncate text-base">{trace?.name || t("tracing.viewer.untitled")}</SheetTitle>
						{trace?.in_progress && (
							<Badge variant="secondary" className="gap-1">
								<Loader2 className="h-3 w-3 animate-spin" />
								{t("tracing.inProgress")}
							</Badge>
						)}
						<div className="ml-auto flex items-center gap-1">
							<Button
								variant="ghost"
								size="icon"
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
								<Trash2 className="h-4 w-4" />
							</Button>
							<Button
								variant="ghost"
								size="icon"
								aria-label={fullscreen ? t("tracing.viewer.exitFullscreen") : t("tracing.viewer.fullscreen")}
								data-testid="trace-viewer-fullscreen-button"
								onClick={() => setFullscreen((value) => !value)}
							>
								{fullscreen ? <Minimize2 className="h-4 w-4" /> : <Maximize2 className="h-4 w-4" />}
							</Button>
						</div>
					</div>
					<SheetDescription asChild>
						<div className="text-muted-foreground flex flex-wrap items-center gap-x-4 gap-y-1 text-xs">
							{trace && (
								<>
									<span>{new Date(trace.start_time).toLocaleString()}</span>
									<span>{formatDuration(trace.duration_ms)}</span>
									<span>{t("tracing.viewer.spans", { count: trace.span_count })}</span>
									<span>{t("tracing.viewer.tokens", { tokens: formatTokens(trace.total_tokens) })}</span>
									<span>
										{formatCost(trace.cost)}
										{trace.estimated_cost ? ` (${t("tracing.estimatedShare", { cost: formatCost(trace.estimated_cost) })})` : ""}
									</span>
									{trace.unpriced_span_count > 0 && <span>{t("tracing.unpricedCalls", { count: trace.unpriced_span_count })}</span>}
									{trace.llm_span_count > 0 && (
										<span>{t("tracing.viewer.linked", { linked: trace.linked_span_count, total: trace.llm_span_count })}</span>
									)}
									{trace.error_count > 0 && (
										<span className="text-destructive">{t("tracing.viewer.errors", { count: trace.error_count })}</span>
									)}
								</>
							)}
						</div>
					</SheetDescription>
				</SheetHeader>

				{isLoading && (
					<div className="text-muted-foreground flex flex-1 items-center justify-center gap-2 text-sm">
						<Loader2 className="h-4 w-4 animate-spin" />
						{t("tracing.viewer.loading")}
					</div>
				)}
				{Boolean(error) && !data && (
					<div className="text-destructive flex flex-1 items-center justify-center text-sm">{t("tracing.viewer.loadFailed")}</div>
				)}

				{data && (
					<div className="flex min-h-0 flex-1">
						<div className="flex w-[44%] min-w-[320px] flex-col border-r">
							<Tabs value={leftTab} onValueChange={setLeftTab} className="flex min-h-0 flex-1 flex-col gap-0">
								<div className="flex items-center justify-between border-b px-3 py-2">
									<TabsList>
										<TabsTrigger value="spans" data-testid="trace-viewer-spans-tab">
											{t("tracing.viewer.spansTab")}
										</TabsTrigger>
										<TabsTrigger value="timeline" data-testid="trace-viewer-timeline-tab">
											{t("tracing.viewer.timelineTab")}
										</TabsTrigger>
										<TabsTrigger value="thread" data-testid="trace-viewer-thread-tab">
											{t("tracing.viewer.threadTab")}
										</TabsTrigger>
									</TabsList>
									{hiddenHTTP > 0 && (
										<label className="text-muted-foreground flex items-center gap-2 text-xs">
											<Switch checked={showHTTP} onCheckedChange={setShowHTTP} data-testid="trace-viewer-show-http-switch" />
											{t("tracing.viewer.showHTTP", { count: hiddenHTTP })}
										</label>
									)}
								</div>
								<TabsContent value="spans" className="mt-0 min-h-0 flex-1 overflow-auto">
									{visibleRows.map((node) => (
										<SpanRow
											key={node.span.span_id}
											node={node}
											mode="tree"
											selected={selected?.span_id === node.span.span_id}
											collapsed={collapsed.has(node.span.span_id)}
											onToggle={() => toggle(node.span.span_id)}
											onSelect={() => setSelectedId(node.span.span_id)}
										/>
									))}
								</TabsContent>
								<TabsContent value="timeline" className="mt-0 min-h-0 flex-1 overflow-auto">
									<div className="text-muted-foreground flex justify-between border-b px-3 py-1 text-[11px]">
										<span>0</span>
										<span>{formatDuration(tree.durationMs / 2)}</span>
										<span>{formatDuration(tree.durationMs)}</span>
									</div>
									{visibleRows.map((node) => (
										<SpanRow
											key={node.span.span_id}
											node={node}
											mode="timeline"
											selected={selected?.span_id === node.span.span_id}
											collapsed={collapsed.has(node.span.span_id)}
											onToggle={() => toggle(node.span.span_id)}
											onSelect={() => setSelectedId(node.span.span_id)}
										/>
									))}
								</TabsContent>
								<TabsContent value="thread" className="mt-0 min-h-0 flex-1 space-y-3 overflow-auto p-3">
									{thread.length === 0 && <p className="text-muted-foreground text-sm">{t("tracing.viewer.noThread")}</p>}
									{thread.map((message, index) => (
										<button
											key={`${message.spanId}-${index}`}
											type="button"
											data-testid={`trace-viewer-thread-message-${index}`}
											onClick={() => setSelectedId(message.spanId)}
											className={cn(
												"block w-full rounded-md border p-3 text-left text-sm",
												message.role === "user" ? "bg-muted/60" : "bg-background",
												selected?.span_id === message.spanId && "border-primary",
											)}
										>
											<div className="text-muted-foreground mb-1 text-[11px] font-medium tracking-wide uppercase">{message.role}</div>
											<div className="break-words whitespace-pre-wrap">{message.text}</div>
										</button>
									))}
								</TabsContent>
							</Tabs>
						</div>
						<div className="min-w-0 flex-1 overflow-auto">
							{selected ? (
								<SpanDetail span={selected} detail={data} extras={renderSpanExtras?.(data, selected)} />
							) : (
								<div className="text-muted-foreground p-6 text-sm">{t("tracing.viewer.noSpans")}</div>
							)}
						</div>
					</div>
				)}
			</SheetContent>
		</Sheet>
	);
}

function SpanRow({
	node,
	mode,
	selected,
	collapsed,
	onToggle,
	onSelect,
}: {
	node: TraceTreeNode;
	mode: "tree" | "timeline";
	selected: boolean;
	collapsed: boolean;
	onToggle: () => void;
	onSelect: () => void;
}) {
	const { span } = node;
	const meta = spanKindMeta(span.kind);
	const Icon = meta.icon;
	const usage = spanUsage(span);
	return (
		<div
			role="button"
			tabIndex={0}
			data-testid={`trace-span-row-${span.span_id}`}
			onClick={onSelect}
			onKeyDown={(event) => {
				if (event.key === "Enter" || event.key === " ") {
					event.preventDefault();
					onSelect();
				}
			}}
			className={cn("hover:bg-muted/60 flex cursor-pointer items-center gap-2 border-b px-2 py-1.5 text-sm", selected && "bg-muted")}
		>
			<div
				className={cn("flex min-w-0 items-center gap-1", mode === "timeline" ? "w-[42%] shrink-0" : "flex-1")}
				style={{ paddingLeft: node.depth * 14 }}
			>
				{node.children.length > 0 ? (
					<button
						type="button"
						aria-label={collapsed ? "Expand" : "Collapse"}
						data-testid={`trace-span-toggle-${span.span_id}`}
						onClick={(event) => {
							event.stopPropagation();
							onToggle();
						}}
						className="text-muted-foreground hover:text-foreground shrink-0"
					>
						{collapsed ? <ChevronRight className="h-3.5 w-3.5" /> : <ChevronDown className="h-3.5 w-3.5" />}
					</button>
				) : (
					<span className="w-3.5 shrink-0" />
				)}
				<Icon className={cn("h-3.5 w-3.5 shrink-0", meta.text)} />
				<span className="truncate">{span.name || span.span_id}</span>
				{span.status === "error" && <AlertCircle className="text-destructive h-3.5 w-3.5 shrink-0" />}
				{usage.linked && <Link2 className="text-muted-foreground h-3 w-3 shrink-0" />}
			</div>
			{mode === "tree" ? (
				<div className="text-muted-foreground flex shrink-0 items-center gap-3 text-xs tabular-nums">
					{usage.tokens > 0 && <span>{formatTokens(usage.tokens)}</span>}
					{usage.cost !== undefined && usage.cost !== null && (
						<span>
							{usage.estimated ? "~" : ""}
							{formatCost(usage.cost)}
						</span>
					)}
					<span className="w-14 text-right">{span.partial && !span.end_time ? "…" : formatDuration(span.duration_ms)}</span>
				</div>
			) : (
				<div className="relative h-4 min-w-0 flex-1">
					<div
						className={cn("absolute top-0.5 h-3 min-w-[2px] rounded-[2px]", meta.bar, span.status === "error" && "bg-destructive")}
						style={{ left: `${node.offset * 100}%`, width: `${Math.max(node.width * 100, 0.4)}%` }}
					>
						{/* The hatched share is the part of the prompt read from cache. */}
						{usage.cachedShare > 0 && (
							<div
								className="h-full rounded-l-[2px] opacity-60"
								style={{
									width: `${usage.cachedShare * 100}%`,
									backgroundImage: "repeating-linear-gradient(135deg, rgba(255,255,255,.75) 0 2px, transparent 2px 4px)",
								}}
							/>
						)}
					</div>
				</div>
			)}
		</div>
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
		<div>
			<div className="text-muted-foreground text-[11px]">{label}</div>
			<div className="text-sm tabular-nums">{value}</div>
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
	return (
		<div className="space-y-4 p-4" data-testid="trace-span-detail">
			<div className="flex flex-wrap items-center gap-2">
				<Icon className={cn("h-4 w-4", meta.text)} />
				<h3 className="font-medium">{span.name || span.span_id}</h3>
				<Badge variant="outline">{span.kind}</Badge>
				{model && <Badge variant="secondary">{model}</Badge>}
				{span.status === "error" && <Badge variant="destructive">{t("tracing.viewer.error")}</Badge>}
				{span.partial && <Badge variant="secondary">{t("tracing.viewer.partial")}</Badge>}
			</div>

			<div className="grid grid-cols-2 gap-3 md:grid-cols-4">
				<Metric label={t("tracing.columns.duration")} value={formatDuration(span.duration_ms)} />
				<Metric label={t("tracing.columns.tokens")} value={formatTokens(usage.tokens)} />
				<Metric
					label={t("tracing.columns.cost")}
					value={
						usage.cost === undefined || usage.cost === null ? (
							usage.tokens > 0 && (span.kind === "llm" || span.kind === "embedding") ? (
								<Tooltip>
									<TooltipTrigger className="underline decoration-dotted">{t("tracing.unpriced")}</TooltipTrigger>
									<TooltipContent>{t("tracing.unpricedHint")}</TooltipContent>
								</Tooltip>
							) : (
								"-"
							)
						) : (
							<>
								{formatCost(usage.cost)}
								{usage.estimated && <span className="text-muted-foreground ml-1 text-xs">{t("tracing.estimated")}</span>}
							</>
						)
					}
				/>
				<Metric label={t("tracing.viewer.firstToken")} value={formatDuration(span.time_to_first_token_ms)} />
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

			<Tabs defaultValue="details">
				<TabsList>
					<TabsTrigger value="details" data-testid="trace-span-details-tab">
						{t("tracing.viewer.detailsTab")}
					</TabsTrigger>
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
					<ValueBlock label={t("tracing.columns.input")} value={span.input} testId="trace-span-input" />
					<ValueBlock label={t("tracing.columns.output")} value={span.output} testId="trace-span-output" />
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
					{span.metadata && Object.keys(span.metadata).length > 0 && (
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