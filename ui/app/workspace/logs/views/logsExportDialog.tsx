import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Progress } from "@/components/ui/progress";
import { getErrorMessage } from "@/lib/store";
import { useLazyGetLogsQuery } from "@/lib/store/apis/logsApi";
import type { LogEntry, LogFilters } from "@/lib/types/logs";
import { cn } from "@/lib/utils";
import { downloadCSV } from "@/lib/utils/csv";
import { Download, Loader2, ShieldCheck, X } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import {
	DEFAULT_MAX_EXPORT_LIMIT,
	calculateExportTargetCount,
	collectLogsInBatches,
	formatActiveFiltersSummary,
	logsToCSV,
	normalizeExportFilters,
} from "../utils/logsExport";

export type LogsExportScope = "current_page" | "all";

interface LogsExportDialogProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	filters: LogFilters;
	currentPageLogs: LogEntry[];
	totalLogs: number;
	sortBy?: string;
	order?: string;
	grouped?: boolean;
}

/**
 * Modal dialog for exporting filtered logs to CSV.
 * Supports exporting the currently visible table rows or client-side batched collection of all matching logs up to 10,000.
 */
export function LogsExportDialog({
	open,
	onOpenChange,
	filters,
	currentPageLogs,
	totalLogs,
	sortBy = "timestamp",
	order = "desc",
	grouped = false,
}: LogsExportDialogProps) {
	const [exportScope, setExportScope] = useState<LogsExportScope>("current_page");
	const [exportMaxLimit, setExportMaxLimit] = useState("");
	const [isExporting, setIsExporting] = useState(false);
	const [progress, setProgress] = useState<{ current: number; total: number } | null>(null);

	const exportRunRef = useRef(0);
	const prevOpenRef = useRef(open);
	const [triggerGetLogs] = useLazyGetLogsQuery();

	// Reset dialog state when opened, or cancel active export when closed
	useEffect(() => {
		if (open && !prevOpenRef.current) {
			// Dialog transitioned from closed to open: reset fields
			setExportScope("current_page");
			setExportMaxLimit("");
			setIsExporting(false);
			setProgress(null);
		} else if (!open && prevOpenRef.current) {
			// Dialog transitioned from open to closed: cancel any in-flight export
			exportRunRef.current++;
			if (isExporting) {
				setIsExporting(false);
				setProgress(null);
			}
		}
		prevOpenRef.current = open;
	}, [open, isExporting]);

	const activeFilters = useMemo(() => formatActiveFiltersSummary(filters), [filters]);

	const handleCancel = () => {
		if (isExporting) {
			exportRunRef.current++;
			setIsExporting(false);
			setProgress(null);
			toast.info("Export cancelled", { id: "logs-export-cancel" });
		} else {
			onOpenChange(false);
		}
	};

	const handleExport = async () => {
		if (exportScope === "current_page") {
			if (currentPageLogs.length === 0) {
				toast.info("No logs on the current page to export");
				return;
			}
			const csv = logsToCSV(currentPageLogs);
			downloadCSV(csv, "logs");
			toast.success(`Exported ${currentPageLogs.length} logs`);
			onOpenChange(false);
			return;
		}

		// Scope: All matching entries (client-side batched collection)
		const userCap = exportMaxLimit ? Number(exportMaxLimit) : undefined;
		if (userCap !== undefined && (!Number.isInteger(userCap) || userCap < 1)) {
			toast.error("Max entries must be a whole number of at least 1");
			return;
		}
		const targetTotal = calculateExportTargetCount(totalLogs, userCap);

		if (targetTotal === 0 || totalLogs === 0) {
			toast.info("No logs matched the selected filters to export");
			return;
		}

		const runId = ++exportRunRef.current;
		const isRunCancelled = () => exportRunRef.current !== runId;

		setIsExporting(true);
		setProgress({ current: 0, total: targetTotal });

		const exportFilters = normalizeExportFilters(filters);

		try {
			const collectedLogs = await collectLogsInBatches({
				targetTotal,
				fetchBatch: async (offset, limit) => {
					const result = await triggerGetLogs({
						filters: exportFilters,
						pagination: {
							limit,
							offset,
							sort_by: (sortBy as "timestamp" | "latency" | "tokens" | "cost") || "timestamp",
							order: (order as "asc" | "desc") || "desc",
						},
						rootsOnly: grouped,
						groupSessions: grouped,
					}).unwrap();
					return result.logs ?? [];
				},
				onProgress: (current, total) => {
					if (!isRunCancelled()) {
						setProgress({ current, total });
					}
				},
				isCancelled: isRunCancelled,
			});

			if (isRunCancelled()) {
				toast.info("Export cancelled", { id: "logs-export-cancel" });
				return;
			}

			if (collectedLogs.length === 0) {
				toast.info("No logs matched the selected filters to export");
				onOpenChange(false);
				return;
			}

			const csv = logsToCSV(collectedLogs);
			downloadCSV(csv, "logs");
			toast.success(`Exported ${collectedLogs.length} logs`);
			onOpenChange(false);
		} catch (error) {
			if (!isRunCancelled()) {
				toast.error(`Export failed: ${getErrorMessage(error)}`);
			}
		} finally {
			if (!isRunCancelled()) {
				setIsExporting(false);
				setProgress(null);
			}
		}
	};

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent className="sm:max-w-[425px]">
				<DialogHeader className="pb-0">
					<DialogTitle>Export Logs</DialogTitle>
					<DialogDescription>Download logs as CSV with current filters and sorting applied.</DialogDescription>
				</DialogHeader>

				<div className="space-y-4">
					<div className="space-y-2">
						<Label className="text-sm">Export scope</Label>
						<div className="grid grid-cols-1 gap-2 sm:grid-cols-2" data-testid="logs-export-scope">
							<button
								type="button"
								data-testid="logs-export-scope-current"
								disabled={isExporting}
								onClick={() => setExportScope("current_page")}
								className={cn(
									"flex cursor-pointer flex-col items-center gap-1 rounded-md border px-3 py-3 text-sm transition-colors",
									exportScope === "current_page"
										? "border-primary bg-primary/5 text-foreground"
										: "border-border text-muted-foreground hover:border-primary/50 hover:text-foreground",
									isExporting && "cursor-not-allowed opacity-60",
								)}
							>
								<span className="font-medium">Current page</span>
								<span className="text-muted-foreground text-xs">{currentPageLogs.length} entries</span>
							</button>

							<button
								type="button"
								data-testid="logs-export-scope-all"
								disabled={isExporting}
								onClick={() => setExportScope("all")}
								className={cn(
									"flex cursor-pointer flex-col items-center gap-1 rounded-md border px-3 py-3 text-sm transition-colors",
									exportScope === "all"
										? "border-primary bg-primary/5 text-foreground"
										: "border-border text-muted-foreground hover:border-primary/50 hover:text-foreground",
									isExporting && "cursor-not-allowed opacity-60",
								)}
							>
								<span className="font-medium">All entries</span>
								<span className="text-muted-foreground text-xs">{totalLogs.toLocaleString()} total</span>
							</button>
						</div>
					</div>

					{exportScope === "all" && (
						<div className="space-y-2">
							<Label htmlFor="logs-export-max-limit" className="text-sm">
								Max entries <span className="text-muted-foreground font-normal">(optional, up to 10,000)</span>
							</Label>
							<Input
								id="logs-export-max-limit"
								data-testid="logs-export-max-limit"
								type="number"
								min="1"
								max="10000"
								disabled={isExporting}
								placeholder={`Default ${Math.min(DEFAULT_MAX_EXPORT_LIMIT, totalLogs || DEFAULT_MAX_EXPORT_LIMIT).toLocaleString()}`}
								value={exportMaxLimit}
								onChange={(e) => setExportMaxLimit(e.target.value)}
							/>
						</div>
					)}

					{activeFilters.length > 0 && (
						<p className="text-muted-foreground text-xs leading-relaxed">Filters applied: {activeFilters.join(", ")}</p>
					)}

					<div className="text-muted-foreground flex items-center gap-2">
						<ShieldCheck className="h-3.5 w-3.5 shrink-0" />
						<p className="text-xs">Prompt and response message bodies are excluded from the export.</p>
					</div>

					{isExporting && progress && (
						<div className="space-y-1.5 pt-1" data-testid="logs-export-progress">
							<Progress value={progress.total > 0 ? (progress.current / progress.total) * 100 : 0} className="h-1.5" />
							<p className="text-muted-foreground text-center text-xs">
								Exporting {progress.current.toLocaleString()} of {progress.total.toLocaleString()} logs...
							</p>
						</div>
					)}
				</div>

				<DialogFooter className="pt-0">
					<Button variant="outline" onClick={handleCancel} data-testid="logs-export-cancel-btn">
						{isExporting ? (
							<>
								<X className="mr-1 h-3.5 w-3.5" />
								Cancel Export
							</>
						) : (
							"Cancel"
						)}
					</Button>

					<Button onClick={handleExport} disabled={isExporting} data-testid="logs-export-confirm-btn">
						{isExporting ? (
							<>
								<Loader2 className="h-4 w-4 animate-spin" />
								Exporting...
							</>
						) : (
							<>
								<Download className="h-4 w-4" />
								Export CSV
							</>
						)}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}