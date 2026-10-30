import { calculateExportTargetCount, collectLogsInBatches, DEFAULT_MAX_EXPORT_LIMIT } from "@/app/workspace/logs/utils/logsExport";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Progress } from "@/components/ui/progress";
import { getErrorMessage } from "@/lib/store";
import { useLazyGetRelayLogsQuery } from "@/lib/store/apis/relaysApi";
import type { RelayLog, RelayLogFilters, RelayLogPagination } from "@/lib/types/relays";
import { cn } from "@/lib/utils";
import { downloadCSV } from "@/lib/utils/csv";
import { Download, Loader2, ShieldCheck, X } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { formatRelayActiveFiltersSummary, normalizeRelayExportFilters, relayLogsToCSV } from "./relayLogsExportDialog.utils";

export type RelayLogsExportScope = "current_page" | "all";

interface RelayLogsExportDialogProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	filters: RelayLogFilters;
	currentPageLogs: RelayLog[];
	totalLogs: number;
	sortBy?: RelayLogPagination["sort_by"];
	order?: RelayLogPagination["order"];
}

/**
 * Exports relay logs to CSV: either the rows on screen, or every matching row collected
 * client-side in batches up to the same cap as the LLM export (DEFAULT_MAX_EXPORT_LIMIT).
 */
export function RelayLogsExportDialog({
	open,
	onOpenChange,
	filters,
	currentPageLogs,
	totalLogs,
	sortBy = "timestamp",
	order = "desc",
}: RelayLogsExportDialogProps) {
	const { t } = useTranslation("observability");
	const [exportScope, setExportScope] = useState<RelayLogsExportScope>("current_page");
	const [exportMaxLimit, setExportMaxLimit] = useState("");
	const [isExporting, setIsExporting] = useState(false);
	const [progress, setProgress] = useState<{ current: number; total: number } | null>(null);

	const exportRunRef = useRef(0);
	const prevOpenRef = useRef(open);
	const [triggerGetLogs] = useLazyGetRelayLogsQuery();

	// Reset the fields on open; closing cancels an export still in flight.
	useEffect(() => {
		if (open && !prevOpenRef.current) {
			setExportScope("current_page");
			setExportMaxLimit("");
			setIsExporting(false);
			setProgress(null);
		} else if (!open && prevOpenRef.current) {
			exportRunRef.current++;
			if (isExporting) {
				setIsExporting(false);
				setProgress(null);
			}
		}
		prevOpenRef.current = open;
	}, [open, isExporting]);

	const activeFilters = useMemo(() => formatRelayActiveFiltersSummary(filters), [filters]);

	const handleCancel = () => {
		if (isExporting) {
			exportRunRef.current++;
			setIsExporting(false);
			setProgress(null);
			toast.info(t("logs.export.toastCancelled"), { id: "relay-logs-export-cancel" });
		} else {
			onOpenChange(false);
		}
	};

	const handleExport = async () => {
		if (exportScope === "current_page") {
			if (currentPageLogs.length === 0) {
				toast.info(t("relayLogs.export.toastNoLogsOnPage"));
				return;
			}
			downloadCSV(relayLogsToCSV(currentPageLogs), "relay-logs");
			toast.success(t("relayLogs.export.toastExported", { count: currentPageLogs.length }));
			onOpenChange(false);
			return;
		}

		const userCap = exportMaxLimit ? Number(exportMaxLimit) : undefined;
		if (userCap !== undefined && (!Number.isInteger(userCap) || userCap < 1)) {
			toast.error(t("logs.export.toastInvalidMax"));
			return;
		}
		const targetTotal = calculateExportTargetCount(totalLogs, userCap);
		if (targetTotal === 0 || totalLogs === 0) {
			toast.info(t("relayLogs.export.toastNoMatch"));
			return;
		}

		const runId = ++exportRunRef.current;
		const isRunCancelled = () => exportRunRef.current !== runId;

		setIsExporting(true);
		setProgress({ current: 0, total: targetTotal });

		const exportFilters = normalizeRelayExportFilters(filters);

		try {
			const collectedLogs = await collectLogsInBatches<RelayLog>({
				targetTotal,
				fetchBatch: async (offset, limit) => {
					const result = await triggerGetLogs({ filters: exportFilters, pagination: { limit, offset, sort_by: sortBy, order } }).unwrap();
					return result.logs ?? [];
				},
				onProgress: (current, total) => {
					if (!isRunCancelled()) setProgress({ current, total });
				},
				isCancelled: isRunCancelled,
			});

			if (isRunCancelled()) {
				toast.info(t("logs.export.toastCancelled"), { id: "relay-logs-export-cancel" });
				return;
			}
			if (collectedLogs.length === 0) {
				toast.info(t("relayLogs.export.toastNoMatch"));
				onOpenChange(false);
				return;
			}

			downloadCSV(relayLogsToCSV(collectedLogs), "relay-logs");
			toast.success(t("relayLogs.export.toastExported", { count: collectedLogs.length }));
			onOpenChange(false);
		} catch (error) {
			if (!isRunCancelled()) toast.error(t("logs.export.toastFailed", { error: getErrorMessage(error) }));
		} finally {
			if (!isRunCancelled()) {
				setIsExporting(false);
				setProgress(null);
			}
		}
	};

	const scopeButtonClass = (selected: boolean) =>
		cn(
			"flex cursor-pointer flex-col items-center gap-1 rounded-md border px-3 py-3 text-sm transition-colors",
			selected
				? "border-primary bg-primary/5 text-foreground"
				: "border-border text-muted-foreground hover:border-primary/50 hover:text-foreground",
			isExporting && "cursor-not-allowed opacity-60",
		);

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent className="sm:max-w-[425px]" data-testid="relay-logs-export-dialog">
				<DialogHeader className="pb-0">
					<DialogTitle>{t("relayLogs.export.title")}</DialogTitle>
					<DialogDescription>{t("relayLogs.export.description")}</DialogDescription>
				</DialogHeader>

				<div className="space-y-4">
					<div className="space-y-2">
						<Label className="text-sm">{t("logs.export.scope")}</Label>
						<div className="grid grid-cols-1 gap-2 sm:grid-cols-2" data-testid="relay-logs-export-scope">
							<button
								type="button"
								data-testid="relay-logs-export-scope-current"
								disabled={isExporting}
								onClick={() => setExportScope("current_page")}
								className={scopeButtonClass(exportScope === "current_page")}
							>
								<span className="font-medium">{t("logs.export.currentPage")}</span>
								<span className="text-muted-foreground text-xs">{t("logs.export.entriesCount", { count: currentPageLogs.length })}</span>
							</button>

							<button
								type="button"
								data-testid="relay-logs-export-scope-all"
								disabled={isExporting}
								onClick={() => setExportScope("all")}
								className={scopeButtonClass(exportScope === "all")}
							>
								<span className="font-medium">{t("logs.export.allEntries")}</span>
								<span className="text-muted-foreground text-xs">{t("logs.export.totalCount", { total: totalLogs.toLocaleString() })}</span>
							</button>
						</div>
					</div>

					{exportScope === "all" && (
						<div className="space-y-2">
							<Label htmlFor="relay-logs-export-max-limit" className="text-sm">
								{t("logs.export.maxEntries")}{" "}
								<span className="text-muted-foreground font-normal">
								{t("relayLogs.export.maxEntriesHint", { max: DEFAULT_MAX_EXPORT_LIMIT.toLocaleString() })}
								</span>
							</Label>
							<Input
								id="relay-logs-export-max-limit"
								data-testid="relay-logs-export-max-limit"
								type="number"
								min="1"
								max={DEFAULT_MAX_EXPORT_LIMIT}
								disabled={isExporting}
								placeholder={t("logs.export.defaultMax", {
									value: Math.min(DEFAULT_MAX_EXPORT_LIMIT, totalLogs || DEFAULT_MAX_EXPORT_LIMIT).toLocaleString(),
								})}
								value={exportMaxLimit}
								onChange={(e) => setExportMaxLimit(e.target.value)}
							/>
						</div>
					)}

					{activeFilters.length > 0 && (
						<p className="text-muted-foreground text-xs leading-relaxed">{t("logs.export.filtersApplied", { filters: activeFilters.join(", ") })}</p>
					)}

					<div className="text-muted-foreground flex items-center gap-2">
						<ShieldCheck className="h-3.5 w-3.5 shrink-0" />
						<p className="text-xs">{t("relayLogs.export.bodiesExcluded")}</p>
					</div>

					{isExporting && progress && (
						<div className="space-y-1.5 pt-1" data-testid="relay-logs-export-progress">
							<Progress value={progress.total > 0 ? (progress.current / progress.total) * 100 : 0} className="h-1.5" />
							<p className="text-muted-foreground text-center text-xs">
								{t("logs.export.progress", { current: progress.current.toLocaleString(), total: progress.total.toLocaleString() })}
							</p>
						</div>
					)}
				</div>

				<DialogFooter className="pt-0">
					<Button variant="outline" onClick={handleCancel} data-testid="relay-logs-export-cancel-btn">
						{isExporting ? (
							<>
								<X className="mr-1 h-3.5 w-3.5" />
								{t("logs.export.cancelExport")}
							</>
						) : (
							t("labels.cancel")
						)}
					</Button>

					<Button onClick={handleExport} disabled={isExporting} data-testid="relay-logs-export-confirm-btn">
						{isExporting ? (
							<>
								<Loader2 className="h-4 w-4 animate-spin" />
								{t("logs.export.exporting")}
							</>
						) : (
							<>
								<Download className="h-4 w-4" />
								{t("logs.export.exportCsv")}
							</>
						)}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}