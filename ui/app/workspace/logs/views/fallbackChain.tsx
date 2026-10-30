import { formatLatency } from "@/app/workspace/dashboard/utils/chartUtils";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { RenderProviderIcon, type ProviderIconType } from "@/lib/constants/icons";
import { useGetLogByIdQuery, useGetLogsQuery } from "@/lib/store/apis/logsApi";
import type { LogEntry } from "@/lib/types/logs";
import { cn } from "@/lib/utils";
import { Check, Loader2, X } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { buildFallbackChain, fallbackChainOutcome, type FallbackChainOutcome } from "../sheets/logDetailView.utils";

// The search endpoint's page cap. The server takes any number of fallbacks, so
// one page this size holds every attempt of any real request.
const CHAIN_LIMIT = 1000;
const CHAIN_POLL_MS = 2000;
// The next fallback's row can be written just after the previous attempt
// settles, so polling keeps going this long after nothing is running.
const CHAIN_SETTLE_MS = 10_000;

// Loads the other attempts of the request this row belongs to. Returns a chain
// only when there is more than one attempt, so ordinary requests render nothing.
export function useFallbackChain(log: LogEntry): LogEntry[] | undefined {
	const rootId = log.parent_request_id || log.id;
	const skip = !!log.batch_debug || !!log.video_debug;
	const [pollingInterval, setPollingInterval] = useState(0);
	const { currentData: children } = useGetLogsQuery(
		{
			filters: { parent_request_id: rootId },
			pagination: {
				limit: CHAIN_LIMIT,
				offset: 0,
				sort_by: "timestamp",
				order: "asc",
			},
		},
		{ skip, pollingInterval, skipPollingIfUnfocused: true },
	);
	const { currentData: root } = useGetLogByIdQuery(rootId, {
		skip: skip || !log.parent_request_id,
		pollingInterval,
		skipPollingIfUnfocused: true,
	});
	const chain = skip || !children ? undefined : buildFallbackChain(log, root, children.logs);
	const running = !!chain?.some((e) => e.status === "processing");
	useEffect(() => {
		if (running) {
			setPollingInterval(CHAIN_POLL_MS);
			return;
		}
		const timer = setTimeout(() => setPollingInterval(0), CHAIN_SETTLE_MS);
		return () => clearTimeout(timer);
	}, [running]);
	return chain && chain.length > 1 ? chain : undefined;
}

const outcomeCopy: Record<FallbackChainOutcome, { text: string; className: string }> = {
	succeeded: {
		text: "Request succeeded",
		className: "border-chart-success/30 bg-chart-success/10 text-chart-success-ink",
	},
	failed: {
		text: "All attempts failed",
		className: "border-chart-error/30 bg-chart-error/10 text-chart-error-ink",
	},
	in_progress: {
		text: "Still running",
		className: "border-blue-200 bg-blue-50 text-blue-700 dark:border-blue-900 dark:bg-blue-950/40 dark:text-blue-400",
	},
};

function StepIcon({ status }: { status: string }) {
	if (status === "success") return <Check className="text-chart-success-ink h-3 w-3" />;
	if (status === "processing") return <Loader2 className="h-3 w-3 animate-spin text-blue-500" />;
	return <X className={cn("h-3 w-3", status === "error" ? "text-chart-error-ink" : "text-muted-foreground")} />;
}

function attemptLabel(entry: LogEntry): string {
	return `${entry.provider}/${entry.model}`;
}

function attemptError(entry: LogEntry): string | undefined {
	if (entry.status !== "error") return undefined;
	return entry.error_details?.error?.message || undefined;
}

// Short reason shown under a chip: the HTTP code, or the error type for
// failures that never got a response (timeouts, network).
function attemptReason(entry: LogEntry): string | undefined {
	if (entry.status === "success") return undefined;
	if (entry.status === "processing") return "running";
	const code = entry.error_details?.status_code;
	if (code) return String(code);
	return entry.error_details?.error?.type ?? (entry.status === "cancelled" ? "cancelled" : undefined);
}

function reasonClass(entry: LogEntry): string {
	const code = entry.error_details?.status_code ?? 0;
	// Worth retrying elsewhere (rate limit, timeout, overload) vs. a fault in the request or credentials.
	if (entry.status !== "error") return "text-muted-foreground";
	return code === 429 || code === 0 || code >= 500 ? "text-amber-700 dark:text-amber-400" : "text-chart-error-ink";
}

// The list endpoint leaves out error_details, so each attempt loads its full
// row (cached by RTK, and shared with opening that attempt).
function WithDetails({ entry, children }: { entry: LogEntry; children: (full: LogEntry) => ReactNode }) {
	const { currentData } = useGetLogByIdQuery(entry.id, {
		skip: entry.status !== "error",
	});
	return <>{children(currentData ?? entry)}</>;
}

interface FallbackChainProps {
	chain: LogEntry[];
	currentId: string;
	onOpenLog?: (logId: string) => void;
}

// Header strip: says in one line what the whole request did, and lets the
// reader hop between attempts without leaving the sheet.
export function FallbackChainBanner({ chain, currentId, onOpenLog }: FallbackChainProps) {
	const { t } = useTranslation("observability");
	const outcome = fallbackChainOutcome(chain);
	const last = chain[chain.length - 1];
	const position = chain.findIndex((e) => e.id === currentId) + 1;
	const current = chain[position - 1];
	const isFinal = current?.id === last.id;
	const copy = outcomeCopy[outcome];
	let summary = `Attempt ${position} of ${chain.length}`;
	if (!isFinal && current?.status === "error" && outcome === "succeeded") {
		summary += `. This attempt failed and the request fell back to ${attemptLabel(last)}.`;
	} else if (!isFinal) {
		summary += ". Later attempts ran after this one.";
	}
	return (
		<div className="border-border bg-card flex flex-col gap-3 rounded-sm border p-4" data-testid="logdetails-fallback-banner">
			<div className="flex flex-wrap items-center gap-x-3 gap-y-1">
				<span className={cn("inline-flex items-center rounded-sm border px-2 py-0.5 text-[11px] font-semibold uppercase", copy.className)}>
					{copy.text}
				</span>
				<span className="text-foreground text-sm">{summary}</span>
				{!isFinal && onOpenLog && (
					<button
						type="button"
						className="ml-auto text-xs text-blue-600 underline-offset-2 hover:underline dark:text-blue-400"
						onClick={() => onOpenLog(last.id)}
						data-testid="logdetails-fallback-open-final"
					>
						{t("logs.fallbackChain.openFinalAttempt")}
					</button>
				)}
			</div>
			<div className="flex flex-wrap items-center gap-1.5">
				{chain.map((listEntry, i) => (
					<WithDetails key={listEntry.id} entry={listEntry}>
						{(entry) => (
							<div className="flex items-center gap-1.5">
								{i > 0 && <span className="text-muted-foreground text-xs">→</span>}
								<Tooltip>
									<TooltipTrigger asChild>
										<button
											type="button"
											disabled={!onOpenLog || entry.id === currentId}
											onClick={() => onOpenLog?.(entry.id)}
											aria-current={entry.id === currentId ? "true" : undefined}
											data-testid={`logdetails-fallback-chip-${i + 1}`}
											className={cn(
												"flex items-center gap-1.5 rounded-sm border px-2 py-1 text-xs",
												entry.id === currentId ? "border-foreground/40 bg-muted font-medium" : "hover:bg-muted/60 cursor-pointer",
											)}
										>
											<RenderProviderIcon provider={entry.provider as ProviderIconType} size={12} className="h-4 w-4" />
											<span className="max-w-[160px] truncate">{entry.provider}</span>
											{attemptReason(entry) && entry.status === "error" && (
												<span className={cn("text-[11px] font-normal tabular-nums", reasonClass(entry))}>{attemptReason(entry)}</span>
											)}
											{entry.status !== "error" && <StepIcon status={entry.status} />}
										</button>
									</TooltipTrigger>
									<TooltipContent sideOffset={6} className="max-w-sm break-words" side="bottom">
										{attemptLabel(entry)}
										{entry.latency != null && !isNaN(entry.latency) ? ` · ${formatLatency(entry.latency)}` : ""}
										{attemptError(entry) ? ` · ${attemptError(entry)}` : ""}
									</TooltipContent>
								</Tooltip>
							</div>
						)}
					</WithDetails>
				))}
			</div>
		</div>
	);
}

// Routing tab block: one row per attempt with the facts that explain why the
// next one ran. A row opens that attempt on click.
export function FallbackChainTimeline({ chain, currentId, onOpenLog }: FallbackChainProps) {
	const { t } = useTranslation("observability");
	const outcome = fallbackChainOutcome(chain);
	const totalMs = chain.reduce((sum, e) => sum + (e.latency ?? 0), 0);
	const startMs = new Date(chain[0].timestamp).getTime();
	return (
		<div className="border-border rounded-sm border" data-testid="logdetails-fallback-timeline">
			<div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 border-b px-4 py-3">
				<div className="flex items-center gap-2">
					<div className="text-sm font-medium">{t("logs.fallbackChain.title")}</div>
					<span className={cn("rounded-sm border px-2 py-0.5 text-[11px] font-semibold uppercase", outcomeCopy[outcome].className)}>
						{outcomeCopy[outcome].text}
					</span>
				</div>
				<div className="text-muted-foreground text-xs tabular-nums">
					{chain.length} attempts
					{totalMs > 0 ? ` · ${formatLatency(totalMs)} total` : ""}
				</div>
			</div>
			<ol className="divide-border divide-y">
				{chain.map((listEntry, i) => (
					<WithDetails key={listEntry.id} entry={listEntry}>
						{(entry) => {
							const error = attemptError(entry);
							const reason = attemptReason(entry);
							const isCurrent = entry.id === currentId;
							const clickable = !isCurrent && !!onOpenLog;
							const offset = new Date(entry.timestamp).getTime() - startMs;
							const meta = [
								entry.selected_key?.name ? `key ${entry.selected_key.name}` : undefined,
								entry.number_of_retries > 0 ? `${entry.number_of_retries} retr${entry.number_of_retries === 1 ? "y" : "ies"}` : undefined,
							].filter(Boolean);
							return (
								<li>
									<div
										role={clickable ? "button" : undefined}
										tabIndex={clickable ? 0 : undefined}
										data-testid={`logdetails-fallback-row-${i + 1}`}
										onClick={clickable ? () => onOpenLog(entry.id) : undefined}
										onKeyDown={
											clickable
												? (e) => {
														if (e.key === "Enter" || e.key === " ") {
															e.preventDefault();
															onOpenLog(entry.id);
														}
													}
												: undefined
										}
										className={cn(
											"flex items-start gap-3 px-4 py-3",
											isCurrent && "bg-muted/50",
											clickable && "hover:bg-muted/40 cursor-pointer",
										)}
									>
										<span
											className={cn(
												"flex h-6 w-6 shrink-0 items-center justify-center rounded-full border text-[11px] tabular-nums",
												entry.status === "success"
													? "border-chart-success/40 bg-chart-success/10 text-chart-success-ink"
													: "bg-card text-muted-foreground",
											)}
										>
											{entry.status === "success" ? <Check className="h-3 w-3" /> : i + 1}
										</span>
										<div className="min-w-0 flex-1">
											<div className="flex flex-wrap items-center gap-x-2 gap-y-1">
												<RenderProviderIcon provider={entry.provider as ProviderIconType} size={16} />
												<span className="text-sm font-medium">{entry.provider}</span>
												<span className="text-muted-foreground font-mono text-xs">{entry.model}</span>
												{reason && entry.status === "error" && (
													<span
														className={cn("rounded-sm border border-current/20 px-1.5 py-0.5 text-[11px] tabular-nums", reasonClass(entry))}
													>
														{reason}
													</span>
												)}
												{entry.status === "processing" && <Loader2 className="h-3 w-3 animate-spin text-blue-500" />}
												{isCurrent && (
													<span className="bg-foreground/10 text-foreground rounded-sm px-1.5 py-0.5 text-[10px] font-medium uppercase">
														{t("logs.fallbackChain.viewing")}
													</span>
												)}
											</div>
											{error && (
												<div className="text-muted-foreground mt-1 line-clamp-2 text-xs break-words" title={error}>
													{error}
												</div>
											)}
											{meta.length > 0 && <div className="text-muted-foreground mt-1 text-[11px]">{meta.join(" · ")}</div>}
										</div>
										<div className="flex shrink-0 flex-col items-end text-xs tabular-nums">
											<span>{entry.latency == null || isNaN(entry.latency) ? "—" : formatLatency(entry.latency)}</span>
											{i > 0 && <span className="text-muted-foreground text-[11px]">+{formatLatency(offset)}</span>}
										</div>
									</div>
								</li>
							);
						}}
					</WithDetails>
				))}
			</ol>
		</div>
	);
}