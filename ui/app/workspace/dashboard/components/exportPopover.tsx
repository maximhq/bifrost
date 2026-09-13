import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuPortal,
	DropdownMenuSub,
	DropdownMenuSubContent,
	DropdownMenuSubTrigger,
	DropdownMenuTrigger,
} from "@/components/ui/dropdownMenu";
import { buildCSV, downloadCSV } from "@/lib/utils/csv";
import { Download, FileSpreadsheet, FileText, Loader2 } from "lucide-react";
import { useCallback, useState } from "react";
import { type DashboardData, type DashboardTab, type ExportTab, getCSVSections, getExportTabLabel } from "../utils/exportUtils";
import { useTranslation } from "react-i18next";

interface ExportPopoverProps {
	getData: () => DashboardData;
	/** The tab currently open, offered as the "this tab only" export scope. */
	activeTab: DashboardTab;
	/** Enters export mode, loads the scoped tabs' uncapped data and waits for it to render. */
	onPreloadData: (scope: ExportTab) => Promise<void>;
	onPdfExport: (scope: ExportTab) => Promise<{ element: HTMLElement; label: string }[]>;
	/** Leaves export mode; both flows must call this, since both enter it via onPreloadData. */
	onExportDone: () => void;
}

export function ExportPopover({ getData, activeTab, onPreloadData, onPdfExport, onExportDone }: ExportPopoverProps) {
	const { t } = useTranslation("observability");
	const [exporting, setExporting] = useState(false);

	const fileName = useCallback((scope: ExportTab) => (scope === "all" ? "dashboard-export" : `dashboard-${scope}`), []);

	const handleCsvExport = useCallback(
		async (scope: ExportTab) => {
			setExporting(true);
			try {
				await onPreloadData(scope);
				const sections = getCSVSections(getData(), scope);
				const parts: string[] = [];
				for (const section of sections) {
					if (section.csv.rows.length === 0) continue;
					parts.push(`# ${section.name}`);
					parts.push(buildCSV(section.csv.headers, section.csv.rows));
					parts.push("");
				}
				if (parts.length > 0) {
					downloadCSV(parts.join("\n"), fileName(scope));
				}
			} finally {
				onExportDone();
				setExporting(false);
			}
		},
		[getData, onPreloadData, onExportDone, fileName],
	);

	const handlePdfExport = useCallback(
		async (scope: ExportTab) => {
			setExporting(true);

			// Yield a frame so the spinner renders before heavy work starts
			await new Promise((r) => requestAnimationFrame(r));

			try {
				const { generatePdf } = await import("@/lib/utils/pdf");

				const sections = await onPdfExport(scope);

				await generatePdf(sections, fileName(scope), {
					branding: {
						logoSrc: "/bifrost-logo.webp",
						text: t("dashboard.export.poweredBy"),
					},
				});
			} finally {
				onExportDone();
				setExporting(false);
			}
		},
		[onPdfExport, onExportDone, fileName, t],
	);

	const activeTabLabel = getExportTabLabel(activeTab);

	return (
		<DropdownMenu>
			<Tooltip>
				{/* The span carries the tooltip, not the Button: a disabled button
				    emits no pointer or focus events, so anchoring the trigger on it
				    would hide the "Exporting..." tooltip exactly while it is the one
				    worth reading. DropdownMenuTrigger stays on the Button so the
				    disabled state still blocks a second export. */}
				<TooltipTrigger asChild>
					<span tabIndex={0} className="inline-flex">
						<DropdownMenuTrigger asChild>
							{/* Icon-only: the label would crowd the tab strip it now shares a
						    row with. State still reads from the spinner + tooltip. */}
							{/* size="icon" is size-9; the date range trigger next to it uses the
						    default h-7.5, so pin the square to that height instead. */}
							<Button
								variant="outline"
								size="icon"
								className="size-7.5"
								disabled={exporting}
								data-testid="dashboard-export-trigger"
								aria-label={exporting ? t("dashboard.export.exporting") : t("dashboard.export.export")}
							>
								{exporting ? <Loader2 className="h-4 w-4 animate-spin" /> : <Download className="h-4 w-4" />}
							</Button>
						</DropdownMenuTrigger>
					</span>
				</TooltipTrigger>
				<TooltipContent side="bottom">{exporting ? t("dashboard.export.exporting") : t("dashboard.export.export")}</TooltipContent>
			</Tooltip>
			<DropdownMenuContent align="end">
				<DropdownMenuSub>
					<DropdownMenuSubTrigger data-testid="export-csv-item" className="flex gap-2">
						<FileSpreadsheet className="h-4 w-4" />
						{t("dashboard.export.csv")}
					</DropdownMenuSubTrigger>
					<DropdownMenuPortal>
						<DropdownMenuSubContent>
							<DropdownMenuItem onClick={() => handleCsvExport(activeTab)} data-testid="export-csv-current-tab">
								{t("dashboard.export.thisTab", { label: activeTabLabel })}
							</DropdownMenuItem>
							<DropdownMenuItem onClick={() => handleCsvExport("all")} data-testid="export-csv-all-tabs">
								{t("dashboard.export.allTabs")}
							</DropdownMenuItem>
						</DropdownMenuSubContent>
					</DropdownMenuPortal>
				</DropdownMenuSub>
				<DropdownMenuSub>
					<DropdownMenuSubTrigger data-testid="export-pdf-item" className="flex gap-2">
						<FileText className="h-4 w-4" />
						{t("dashboard.export.pdf")}
					</DropdownMenuSubTrigger>
					<DropdownMenuPortal>
						<DropdownMenuSubContent>
							<DropdownMenuItem onClick={() => handlePdfExport(activeTab)} data-testid="export-pdf-current-tab">
								{t("dashboard.export.thisTab", { label: activeTabLabel })}
							</DropdownMenuItem>
							<DropdownMenuItem onClick={() => handlePdfExport("all")} data-testid="export-pdf-all-tabs">
								{t("dashboard.export.allTabs")}
							</DropdownMenuItem>
						</DropdownMenuSubContent>
					</DropdownMenuPortal>
				</DropdownMenuSub>
			</DropdownMenuContent>
		</DropdownMenu>
	);
}