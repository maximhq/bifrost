"use client";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Sheet, SheetContent } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import type { ProviderName } from "@/lib/constants/logs";
import { RequestTypeColors, RequestTypeLabels, Status, StatusBarColors } from "@/lib/constants/logs";
import { useWebSocket } from "@/hooks/useWebSocket";
import { useGetLogSessionSummaryByIdQuery, useLazyGetLogSessionByIdQuery } from "@/lib/store/apis/logsApi";
import { LogEntry } from "@/lib/types/logs";
import { getErrorMessage } from "@/lib/store";
import { cn } from "@/lib/utils";
import { toast } from "sonner";
import { ArrowDown, ArrowUp, Loader2 } from "lucide-react";
import moment from "moment";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { LogMessageCell } from "../views/columns";

const SESSION_LOG_PAGE_SIZE = 500;

const HIGHLIGHTED_ROW = "border-l-2 border-l-sky-500 bg-sky-500/[0.08] shadow-[inset_0_0_0_1px_rgba(56,189,248,0.18)] hover:bg-sky-500/[0.24] hover:shadow-[inset_0_0_0_1px_rgba(56,189,248,0.38)] dark:hover:bg-sky-400/[0.18]";

function formatDurationFromMs(durationMs?: number) {
	if (!durationMs || durationMs <= 0) return "0s";
	const totalSeconds = Math.floor(durationMs / 1000);
	const hours = Math.floor(totalSeconds / 3600);
	const minutes = Math.floor((totalSeconds % 3600) / 60);
	const seconds = totalSeconds % 60;

	if (hours > 0) {
		return `${hours}h ${String(minutes).padStart(2, "0")}m ${String(seconds).padStart(2, "0")}s`;
	}
	if (minutes > 0) {
		return `${minutes}m ${String(seconds).padStart(2, "0")}s`;
	}
	return `${seconds}s`;
}

interface SummaryCard {
	label: string;
	value: string;
	helper?: string;
	size?: "sm";
}

interface SessionDetailsSheetProps {
	sessionId: string | null;
	highlightedLogId?: string | null;
	open: boolean;
	onOpenChange: (open: boolean) => void;
	liveEnabled: boolean;
	onLogClick?: (log: LogEntry) => void;
	onFilterByParentRequestId?: (parentRequestId: string) => void;
}

export function SessionDetailsSheet({
	sessionId,
	highlightedLogId,
	open,
	onOpenChange,
	liveEnabled,
	onLogClick,
	onFilterByParentRequestId,
}: SessionDetailsSheetProps) {
	const [triggerGetSession] = useLazyGetLogSessionByIdQuery();
	const [sessionLogs, setSessionLogs] = useState<LogEntry[]>([]);
	const [loadingSession, setLoadingSession] = useState(false);
	const [totalCount, setTotalCount] = useState(0);
	const [fetchedCount, setFetchedCount] = useState(0);
	const fetchedCountRef = useRef(fetchedCount);
	const totalCountRef = useRef(totalCount);
	const [hasMore, setHasMore] = useState(false);
	const [sortOrder, setSortOrder] = useState<"asc" | "desc">("asc");
	const { subscribe } = useWebSocket();
	const { data: sessionSummary } = useGetLogSessionSummaryByIdQuery(sessionId || "", {
		skip: !open || !sessionId,
		pollingInterval: 5000,
		refetchOnMountOrArgChange: true,
	});

	const summaryCards: SummaryCard[] = useMemo(
		() => [
			{
				label: "Logs",
				value: (sessionSummary?.count || 0).toLocaleString(),
				helper:
					sessionSummary && sessionLogs.length < sessionSummary.count
						? `(${sessionLogs.length.toLocaleString()} loaded)`
						: undefined,
			},
			{
				label: "Total Cost",
				value: `$${(sessionSummary?.total_cost || 0).toFixed(4)}`,
			},
			{
				label: "Total Tokens",
				value: (sessionSummary?.total_tokens || 0).toLocaleString(),
			},
			{
				label: "Started",
				value: sessionSummary?.started_at ? moment(sessionSummary.started_at).format("MMM D, YYYY hh:mm:ss A") : "N/A",
				size: "sm",
			},
			{
				label: "Latest Update",
				value: sessionSummary?.latest_at ? moment(sessionSummary.latest_at).format("MMM D, YYYY hh:mm:ss A") : "N/A",
				size: "sm",
			},
			{
				label: "Duration",
				value: formatDurationFromMs(sessionSummary?.duration_ms),
			},
		],
		[sessionSummary, sessionLogs.length],
	);

	const sortSessionLogs = useCallback(
		(logs: LogEntry[]) =>
			[...logs].sort((a, b) =>
				sortOrder === "asc"
					? new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime()
					: new Date(b.timestamp).getTime() - new Date(a.timestamp).getTime(),
			),
		[sortOrder],
	);

	const loadSessionPage = useCallback(
		async (offset: number, reset = false) => {
			if (!sessionId) return;
			setLoadingSession(true);
			try {
				const result = await triggerGetSession({
					sessionId,
					pagination: { limit: SESSION_LOG_PAGE_SIZE, offset, order: sortOrder },
				});
				if (result.error) {
					toast.error("Failed to load session logs", {
						description: getErrorMessage(result.error),
					});
					return;
				}
				if (result.data) {
					if (reset && result.data.count === 0) {
						onOpenChange(false);
						return;
					}
					setTotalCount(result.data.count);
					setHasMore(result.data.has_more);
					setFetchedCount(offset + result.data.returned_count);
					setSessionLogs((prev) => {
						const next = reset ? result.data!.logs : [...prev, ...result.data!.logs];
						const seen = new Map<string, LogEntry>();
						for (const log of next) {
							seen.set(log.id, log);
						}
						return sortSessionLogs(Array.from(seen.values()));
					});
				}
			} finally {
				setLoadingSession(false);
			}
		},
		[onOpenChange, sessionId, sortOrder, sortSessionLogs, triggerGetSession],
	);

	useEffect(() => { fetchedCountRef.current = fetchedCount; }, [fetchedCount]);
	useEffect(() => { totalCountRef.current = totalCount; }, [totalCount]);

	useEffect(() => {
		if (!open || !sessionId) {
			return;
		}
		setSessionLogs([]);
		setFetchedCount(0);
		setTotalCount(0);
		fetchedCountRef.current = 0;
		totalCountRef.current = 0;
		setHasMore(false);
		loadSessionPage(0, true);
	}, [open, sessionId, sortOrder, loadSessionPage]);

	useEffect(() => {
		if (!open || !sessionId || !liveEnabled) {
			return;
		}
		const unsubscribe = subscribe("log", (data) => {
			const log = data.payload as LogEntry;
			const operation = data.operation as "create" | "update";
			if (!log?.parent_request_id || log.parent_request_id !== sessionId) {
				return;
			}

			setSessionLogs((prev) => {
				const idx = prev.findIndex((item) => item.id === log.id);
				if (idx >= 0) {
					const next = [...prev];
					next[idx] = log;
					return sortSessionLogs(next);
				}
				return sortSessionLogs([...prev, log]);
			});

			if (operation === "create") {
				setTotalCount((prev) => prev + 1);
				setHasMore((prev) => prev || fetchedCountRef.current < totalCountRef.current + 1);
			}
		});
		return unsubscribe;
	}, [open, sessionId, liveEnabled, subscribe, sortSessionLogs]);

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="flex w-full flex-col gap-4 overflow-x-hidden p-8 sm:max-w-[60%]">
				<div className="flex items-center justify-between gap-4">
					<div>
						<div className="text-lg font-medium">Session</div>
						{sessionId && onFilterByParentRequestId ? (
							<Tooltip>
								<TooltipTrigger asChild>
									<code
										className="text-primary hover:text-primary/80 text-sm break-all underline-offset-2 cursor-pointer hover:underline"
										onClick={() => onFilterByParentRequestId(sessionId)}
									>
										{sessionId}
									</code>
								</TooltipTrigger>
								<TooltipContent sideOffset={6}>Filter this session</TooltipContent>
							</Tooltip>
						) : (
							<code className="text-sm break-all">{sessionId}</code>
						)}
					</div>
					<div className="flex items-center gap-3">
						<Button variant="outline" size="sm" data-testid="session-details-sort-btn" onClick={() => setSortOrder((prev) => (prev === "asc" ? "desc" : "asc"))}>
							{sortOrder === "asc" ? <ArrowUp className="mr-2 h-4 w-4" /> : <ArrowDown className="mr-2 h-4 w-4" />}
							{sortOrder === "asc" ? "Earliest first" : "Latest first"}
						</Button>
					</div>
				</div>

				<div className="grid shrink-0 grid-cols-1 gap-4 sm:grid-cols-3">
					{summaryCards.map((card) => (
						<Card key={card.label} className="py-4 shadow-none">
							<CardContent className="px-4">
								<div className="text-muted-foreground text-xs">{card.label}</div>
								<div
									className={
										card.size === "sm"
											? "font-mono text-sm leading-5 sm:text-base break-words"
											: "truncate font-mono text-xl font-medium sm:text-2xl"
									}
								>
									{card.helper ? (
										<div className="flex items-baseline gap-2">
											<span>{card.value}</span>
											<span className="text-muted-foreground text-sm">{card.helper}</span>
										</div>
									) : (
										card.value
									)}
								</div>
							</CardContent>
						</Card>
					))}
				</div>

				<div className="min-h-0 flex-1 overflow-hidden rounded-sm border">
					<Table containerClassName="h-full overflow-auto">
						<TableHeader className="sticky top-0 z-10 bg-[#f9f9f9] dark:bg-[#27272a]">
							<TableRow>
								<TableHead className="w-2"></TableHead>
								<TableHead>Time</TableHead>
								<TableHead>Type</TableHead>
								<TableHead>Message</TableHead>
								<TableHead>Provider</TableHead>
								<TableHead>Model</TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{loadingSession && sessionLogs.length === 0 ? (
								<TableRow>
									<TableCell colSpan={6} className="h-24 text-center">
										<div className="flex items-center justify-center gap-2">
											<Loader2 className="h-4 w-4 animate-spin" />
											Loading session...
										</div>
									</TableCell>
								</TableRow>
							) : sessionLogs.length ? (
								sessionLogs.map((log) => (
									<TableRow
										key={log.id}
										className={cn(
											"cursor-pointer transition-colors",
											log.id === highlightedLogId ? HIGHLIGHTED_ROW : "hover:bg-muted/40",
										)}
										onClick={() => onLogClick?.(log)}
									>
										<TableCell>
											<div className={`h-6 w-1 rounded-sm ${StatusBarColors[log.status as Status]}`} />
										</TableCell>
										<TableCell className="relative text-xs">
											{log.id === highlightedLogId ? (
												<div className="bg-background text-sky-600 dark:text-sky-300 pointer-events-none absolute -top-1.5 left-1 z-10 rounded-full border border-sky-400/45 px-1.5 py-0 text-[9px] leading-tight font-semibold tracking-wide uppercase">
													Current
												</div>
											) : null}
											{moment(log.timestamp).format("YYYY-MM-DD hh:mm:ss A (Z)")}
										</TableCell>
										<TableCell>
											<Badge variant="outline" className={`${RequestTypeColors[log.object as keyof typeof RequestTypeColors]} text-xs`}>
												{RequestTypeLabels[log.object as keyof typeof RequestTypeLabels]}
											</Badge>
										</TableCell>
										<TableCell className="max-w-[360px]">
											<LogMessageCell log={log} maxWidth="max-w-[360px]" />
										</TableCell>
										<TableCell>
											<Badge variant="secondary" className="font-mono text-xs uppercase">
												<RenderProviderIcon provider={log.provider as ProviderIconType} size="sm" />
												{log.provider as ProviderName}
											</Badge>
										</TableCell>
										<TableCell className="max-w-[140px] truncate font-mono text-xs">{log.model || "N/A"}</TableCell>
									</TableRow>
								))
							) : (
								<TableRow>
									<TableCell colSpan={6} className="text-muted-foreground h-24 text-center">
										No logs found for this session.
									</TableCell>
								</TableRow>
							)}
						</TableBody>
					</Table>
				</div>

				{hasMore ? (
					<div className="flex justify-center">
						<Button variant="outline" data-testid="session-details-load-more-btn" onClick={() => loadSessionPage(fetchedCount)} disabled={loadingSession}>
							{loadingSession ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : null}
							Load More
						</Button>
					</div>
				) : null}
			</SheetContent>
		</Sheet>
	);
}
