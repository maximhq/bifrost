import FullPageLoader from "@/components/fullPageLoader";
import { LoadErrorView } from "@/components/loadErrorView";
import PageTitle from "@/components/pageTitle";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "@/components/ui/dropdownMenu";
import { Input } from "@/components/ui/input";
import { useDebouncedValue } from "@/hooks/useDebounce";
import { useToast } from "@/hooks/use-toast";
import {
	getErrorMessage,
	useCreateEvalDatasetSplitMutation,
	useDeleteEvalDatasetColumnMutation,
	useDeleteEvalDatasetRowsMutation,
	useDeleteEvalDatasetSplitMutation,
	useGetEvalDatasetQuery,
	useGetEvalDatasetVersionsQuery,
	useReorderEvalDatasetColumnsMutation,
	useSaveEvalDatasetVersionMutation,
	useSetEvalDatasetSplitRowsMutation,
} from "@/lib/store";
import type { DatasetColumn } from "@/lib/types/evalDatasets";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { cn } from "@/lib/utils";
import { showLoadError } from "@/lib/utils/loadError";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { ChevronDown, Download, History, Plus, Save, Search, Settings2, Trash2, Upload, X } from "lucide-react";
import { parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { ColumnDialog } from "./columnDialog";
import { DatasetFormSheet } from "./datasetFormSheet";
import { DatasetGrid, type DatasetGridColumnMenu } from "./datasetGrid";
import { datasetExportUrl, versionLabel } from "./datasetUtils";
import { ImportCsvDialog } from "./importCsvDialog";
import { VersionHistorySheet } from "./versionHistorySheet";

// DatasetDetail is the right-hand side of the datasets screen: one dataset as a
// spreadsheet, with its versions, splits and actions. It reads the dataset to show
// from the `id` in the URL.
// The value of the tab that shows every row, among tabs whose values are split IDs.
const ALL_ROWS_TAB = "__all__";

export function DatasetDetail() {
	const { t, i18n } = useTranslation("config");
	const { t: tCommon } = useTranslation("common");
	const { toast } = useToast();
	const canEdit = useRbac(RbacResource.Evaluators, RbacOperation.Update);
	const canDelete = useRbac(RbacResource.Evaluators, RbacOperation.Delete);

	const [urlState, setUrlState] = useQueryStates(
		{
			id: parseAsString.withDefault(""),
			version: parseAsInteger.withDefault(0),
			split: parseAsString.withDefault(""),
			import: parseAsString.withDefault(""),
		},
		{ history: "replace" },
	);
	const { id, version } = urlState;
	const { data, error, isLoading, refetch } = useGetEvalDatasetQuery(id, { skip: !id });
	const { data: versionsData } = useGetEvalDatasetVersionsQuery(id, { skip: !id });
	const dataset = data?.dataset;
	const versions = useMemo(() => versionsData?.versions ?? [], [versionsData]);
	const viewed = version > 0 ? versions.find((item) => item.number === version) : undefined;
	const readOnly = version > 0;

	const [saveVersion, { isLoading: saving }] = useSaveEvalDatasetVersionMutation();
	const [deleteRows] = useDeleteEvalDatasetRowsMutation();
	const [deleteColumn] = useDeleteEvalDatasetColumnMutation();
	const [reorderColumns] = useReorderEvalDatasetColumnsMutation();
	const [createSplit] = useCreateEvalDatasetSplitMutation();
	const [deleteSplit] = useDeleteEvalDatasetSplitMutation();
	const [setSplitRows] = useSetEvalDatasetSplitRowsMutation();

	const [search, setSearch] = useState("");
	const debouncedSearch = useDebouncedValue(search, 300);
	const [selectedRows, setSelectedRows] = useState<string[]>([]);
	const [rowTotal, setRowTotal] = useState(0);
	const [reloadKey, setReloadKey] = useState(0);
	const [historyOpen, setHistoryOpen] = useState(false);
	const [importOpen, setImportOpen] = useState(false);
	const [settingsOpen, setSettingsOpen] = useState(false);
	const [columnDialog, setColumnDialog] = useState<{ column?: DatasetColumn } | null>(null);
	const [columnMenu, setColumnMenu] = useState<DatasetGridColumnMenu | null>(null);
	const [splitName, setSplitName] = useState<string | null>(null);

	// "New dataset" with a CSV start lands here with the import step open.
	useEffect(() => {
		if (urlState.import && dataset) {
			setImportOpen(true);
			void setUrlState({ import: null });
		}
	}, [urlState.import, dataset, setUrlState]);

	// The grid reads its rows again only when something outside it changed them: a
	// column change (seen here, in the columns themselves), or an import, a restore or
	// a bulk action, which call reloadGrid. It must not follow the dataset's own
	// updated_at or version number: the grid's edits move both, and a reload on every
	// edit would drop the selection and the undo history.
	const reloadGrid = useCallback(() => setReloadKey((current) => current + 1), []);
	const columnStamp = dataset ? dataset.columns.map((column) => `${column.id}:${column.name}:${column.type}`).join("|") : "";
	useEffect(() => {
		reloadGrid();
	}, [columnStamp, reloadGrid]);

	const run = useCallback(
		async (action: () => Promise<unknown>, done?: string) => {
			try {
				await action();
				if (done) toast({ title: done });
				return true;
			} catch (actionError) {
				toast({ title: tCommon("error"), description: getErrorMessage(actionError), variant: "destructive" });
				return false;
			}
		},
		[toast, tCommon],
	);

	const onSelectedRowsChange = useCallback((rowIds: string[]) => setSelectedRows(rowIds), []);
	const onColumnsReordered = useCallback(
		(columnIds: string[]) => void run(() => reorderColumns({ id, columnIds }).unwrap()),
		[run, reorderColumns, id],
	);

	if (!id) {
		return (
			<LoadErrorView entity={t("evals.datasets.list.title")} error={undefined} onRetry={() => undefined} testId="dataset-missing-id" />
		);
	}
	if (isLoading) {
		return <FullPageLoader />;
	}
	if (showLoadError(error, data) || !dataset) {
		return (
			<LoadErrorView entity={t("evals.datasets.list.title")} error={error} onRetry={() => void refetch()} testId="dataset-load-error" />
		);
	}

	const columns = viewed ? viewed.columns : dataset.columns;
	const activeSplit = dataset.splits.find((split) => split.id === urlState.split);
	const moveColumn = (column: DatasetColumn, by: number) => {
		const ids = dataset.columns.map((item) => item.id);
		const from = ids.indexOf(column.id);
		const to = from + by;
		if (from < 0 || to < 0 || to >= ids.length) return;
		ids.splice(to, 0, ids.splice(from, 1)[0]);
		onColumnsReordered(ids);
	};

	return (
		<div className="flex h-full min-w-0 flex-col gap-3 p-4" data-testid="dataset-detail-page">
			{/* The name, the way back to the list, the row count and the description all live in the topbar. */}
			<PageTitle
				breadcrumbs={[{ label: t("evals.datasets.list.title"), to: "/workspace/evals/datasets" }, { label: dataset.name }]}
				tags={[{ label: t("evals.datasets.versions.rows", { count: rowTotal }), "data-testid": "dataset-row-count" }]}
			>
				{dataset.description || undefined}
			</PageTitle>

			<div className="flex flex-wrap items-center justify-end gap-3">
				<div className="flex flex-wrap items-center gap-2">
					{/* What can be done with the selected rows sits with the other actions. */}
					{selectedRows.length > 0 && !readOnly && canEdit && (
						<>
							<span className="text-muted-foreground text-sm" data-testid="dataset-selection-count">
								{t("evals.datasets.grid.selected", { count: selectedRows.length })}
							</span>
							{dataset.splits.length > 0 && (
								<DropdownMenu>
									<DropdownMenuTrigger asChild>
										<Button variant="outline" data-testid="dataset-add-to-split-btn">
											{t("evals.datasets.splits.addRows")}
										</Button>
									</DropdownMenuTrigger>
									<DropdownMenuContent>
										{dataset.splits.map((split) => (
											<DropdownMenuItem
												key={split.id}
												onClick={() =>
													void run(
														() => setSplitRows({ id, splitId: split.id, add: selectedRows }).unwrap(),
														t("evals.datasets.splits.rowsAdded", { name: split.name }),
													)
												}
											>
												{split.name}
											</DropdownMenuItem>
										))}
									</DropdownMenuContent>
								</DropdownMenu>
							)}
							{activeSplit && (
								<Button
									variant="outline"
									onClick={() =>
										void run(
											() => setSplitRows({ id, splitId: activeSplit.id, remove: selectedRows }).unwrap(),
											t("evals.datasets.splits.rowsRemoved", { name: activeSplit.name }),
										).then((ok) => ok && reloadGrid())
									}
									data-testid="dataset-remove-from-split-btn"
								>
									{t("evals.datasets.splits.removeRows")}
								</Button>
							)}
							<Button
								variant="outline"
								className="text-destructive"
								onClick={() =>
									void run(
										() => deleteRows({ id, rowIds: selectedRows }).unwrap(),
										t("evals.datasets.grid.rowsDeleted", { count: selectedRows.length }),
									).then((ok) => ok && reloadGrid())
								}
								data-testid="dataset-delete-rows-btn"
							>
								<Trash2 className="h-3.5 w-3.5" />
								{t("evals.datasets.grid.deleteRows")}
							</Button>
						</>
					)}
					<DropdownMenu>
						<DropdownMenuTrigger asChild>
							<Button variant="outline" data-testid="dataset-version-picker">
								{viewed ? versionLabel(viewed) : t("evals.datasets.versions.draft")}
								{!readOnly && dataset.draft_changes > 0 && (
									<Badge variant="secondary">{t("evals.datasets.versions.unsaved", { count: dataset.draft_changes })}</Badge>
								)}
								<ChevronDown className="h-4 w-4" />
							</Button>
						</DropdownMenuTrigger>
						<DropdownMenuContent align="end" className="max-h-80 w-64 overflow-y-auto">
							<DropdownMenuItem onClick={() => void setUrlState({ version: null })} data-testid="dataset-version-option-draft">
								{t("evals.datasets.versions.draft")}
							</DropdownMenuItem>
							{versions.map((item) => (
								<DropdownMenuItem
									key={item.number}
									onClick={() => void setUrlState({ version: item.number })}
									data-testid={`dataset-version-option-${item.number}`}
								>
									{versionLabel(item)}
								</DropdownMenuItem>
							))}
							<DropdownMenuSeparator />
							<DropdownMenuItem onClick={() => setHistoryOpen(true)}>{t("evals.datasets.versions.seeAll")}</DropdownMenuItem>
						</DropdownMenuContent>
					</DropdownMenu>
					{canEdit && !readOnly && (
						<Button
							disabled={saving || dataset.draft_changes === 0}
							onClick={() => void run(() => saveVersion({ id }).unwrap(), t("evals.datasets.versions.saved"))}
							data-testid="dataset-save-version-btn"
						>
							<Save className="h-4 w-4" />
							{t("evals.datasets.versions.save")}
						</Button>
					)}
					<Button variant="outline" onClick={() => setHistoryOpen(true)} data-testid="dataset-history-btn">
						<History className="h-4 w-4" />
						{t("evals.datasets.versions.history")}
					</Button>
					{canEdit && !readOnly && (
						<Button variant="outline" onClick={() => setImportOpen(true)} data-testid="dataset-import-btn">
							<Upload className="h-4 w-4" />
							{t("evals.datasets.import.title")}
						</Button>
					)}
					<DropdownMenu>
						<DropdownMenuTrigger asChild>
							<Button variant="outline" data-testid="dataset-export-btn">
								<Download className="h-4 w-4" />
								{t("evals.datasets.export.title")}
							</Button>
						</DropdownMenuTrigger>
						<DropdownMenuContent align="end">
							<DropdownMenuItem asChild>
								<a
									href={datasetExportUrl(id, "csv", version || undefined, urlState.split || undefined)}
									download
									data-testid="dataset-export-csv"
								>
									{t("evals.datasets.export.csv")}
								</a>
							</DropdownMenuItem>
							<DropdownMenuItem asChild>
								<a
									href={datasetExportUrl(id, "jsonl", version || undefined, urlState.split || undefined)}
									download
									data-testid="dataset-export-jsonl"
								>
									{t("evals.datasets.export.jsonl")}
								</a>
							</DropdownMenuItem>
						</DropdownMenuContent>
					</DropdownMenu>
					{canEdit && (
						<Button
							variant="outline"
							size="icon"
							className="size-7.5"
							aria-label={t("evals.datasets.form.editTitle")}
							onClick={() => setSettingsOpen(true)}
							data-testid="dataset-settings-btn"
						>
							<Settings2 className="h-4 w-4" />
						</Button>
					)}
				</div>
			</div>

			{readOnly && (
				<div
					className="bg-muted flex flex-wrap items-center justify-between gap-2 rounded-sm border px-3 py-2 text-sm"
					data-testid="dataset-readonly-banner"
				>
					<span>{t("evals.datasets.versions.readOnly", { version: viewed ? versionLabel(viewed) : `v${version}` })}</span>
					<Button variant="outline" size="sm" onClick={() => void setUrlState({ version: null })} data-testid="dataset-back-to-draft-btn">
						{t("evals.datasets.versions.backToDraft")}
					</Button>
				</div>
			)}

			<div className="flex flex-wrap items-center justify-between gap-2">
				<div className="flex min-w-0 flex-wrap items-center gap-2">
					{/* The tabs are only the row sets. Making or deleting a split is an action, so it sits beside them. */}
					<Tabs
						value={activeSplit?.id ?? ALL_ROWS_TAB}
						onValueChange={(value) => void setUrlState({ split: value === ALL_ROWS_TAB ? null : value })}
					>
						<TabsList aria-label={t("evals.datasets.splits.title")} className="h-8 rounded-sm">
							<TabsTrigger value={ALL_ROWS_TAB} className="flex-none gap-1.5 rounded-sm px-3" data-testid="dataset-split-tab-all">
								{t("evals.datasets.splits.all")}
								<span className="text-muted-foreground font-mono text-xs font-normal">
									{dataset.row_count.toLocaleString(i18n.language)}
								</span>
							</TabsTrigger>
							{dataset.splits.map((split) => (
								<TabsTrigger
									key={split.id}
									value={split.id}
									className="flex-none gap-1.5 rounded-sm px-3"
									data-testid={`dataset-split-tab-${split.name}`}
								>
									{split.name}
									<span className="text-muted-foreground font-mono text-xs font-normal">
										{split.row_count.toLocaleString(i18n.language)}
									</span>
								</TabsTrigger>
							))}
						</TabsList>
					</Tabs>
					{canEdit && (
						<Button variant="outline" className="h-8" onClick={() => setSplitName("")} data-testid="dataset-new-split-btn">
							<Plus className="h-4 w-4" />
							{t("evals.datasets.splits.new")}
						</Button>
					)}
					{canEdit && activeSplit && (
						<Button
							variant="outline"
							className="text-destructive hover:text-destructive h-8"
							onClick={() =>
								void run(() => deleteSplit({ id, splitId: activeSplit.id }).unwrap(), t("evals.datasets.splits.deleted")).then(
									(ok) => ok && void setUrlState({ split: null }),
								)
							}
							data-testid="dataset-delete-split-btn"
						>
							<Trash2 className="h-4 w-4" />
							{t("evals.datasets.splits.delete")}
						</Button>
					)}
				</div>
				<div className="relative">
					<Search className="text-muted-foreground absolute top-1/2 left-2.5 h-4 w-4 -translate-y-1/2" />
					<Input
						className="h-8 w-60 pl-8"
						value={search}
						placeholder={t("evals.datasets.grid.searchPlaceholder")}
						aria-label={t("evals.datasets.grid.searchPlaceholder")}
						onChange={(event) => setSearch(event.target.value)}
						data-testid="dataset-row-search-input"
					/>
				</div>
			</div>

			<div className="bg-card relative min-h-0 grow overflow-hidden rounded-sm border">
				{columns.length === 0 ? (
					<div className="flex h-full flex-col items-center justify-center gap-3 text-center">
						<p className="text-muted-foreground text-sm">{t("evals.datasets.columns.none")}</p>
						{canEdit && !readOnly && (
							<div className="flex gap-2">
								<Button onClick={() => setColumnDialog({})} data-testid="dataset-first-column-btn">
									<Plus className="h-4 w-4" />
									{t("evals.datasets.columns.add")}
								</Button>
								<Button variant="outline" onClick={() => setImportOpen(true)}>
									<Upload className="h-4 w-4" />
									{t("evals.datasets.import.title")}
								</Button>
							</div>
						)}
					</div>
				) : (
					<DatasetGrid
						datasetId={id}
						columns={columns}
						version={version}
						splitId={urlState.split || undefined}
						search={debouncedSearch || undefined}
						reloadKey={reloadKey}
						canEdit={canEdit}
						onSelectedRowsChange={onSelectedRowsChange}
						onTotalChange={setRowTotal}
						onAddColumn={() => setColumnDialog({})}
						onColumnMenu={setColumnMenu}
						onColumnsReordered={onColumnsReordered}
					/>
				)}
			</div>

			{columnMenu && (
				<>
					<button
						type="button"
						aria-label={tCommon("close")}
						className="fixed inset-0 z-40 cursor-default"
						onClick={() => setColumnMenu(null)}
					/>
					<div
						role="menu"
						className="bg-popover fixed z-50 flex w-48 flex-col rounded-sm border p-1 shadow-md"
						style={{ top: columnMenu.bounds.y + columnMenu.bounds.height + 2, left: columnMenu.bounds.x }}
						data-testid="dataset-column-menu"
					>
						{(
							[
								["edit", () => setColumnDialog({ column: columnMenu.column })],
								["moveLeft", () => moveColumn(columnMenu.column, -1)],
								["moveRight", () => moveColumn(columnMenu.column, 1)],
								["delete", () => void run(() => deleteColumn({ id, columnId: columnMenu.column.id }).unwrap())],
							] as const
						).map(([action, handler]) => (
							<button
								key={action}
								type="button"
								role="menuitem"
								className={cn("hover:bg-accent rounded-sm px-2 py-1.5 text-left text-sm", action === "delete" && "text-destructive")}
								onClick={() => {
									setColumnMenu(null);
									handler();
								}}
								data-testid={`dataset-column-menu-${action}`}
							>
								{t(`evals.datasets.columns.menu.${action}`)}
							</button>
						))}
					</div>
				</>
			)}

			<ColumnDialog
				open={columnDialog !== null}
				onOpenChange={(open) => !open && setColumnDialog(null)}
				datasetId={id}
				columns={dataset.columns}
				column={columnDialog?.column}
			/>
			<ImportCsvDialog open={importOpen} onOpenChange={setImportOpen} dataset={dataset} onImported={reloadGrid} />
			<DatasetFormSheet open={settingsOpen} onOpenChange={setSettingsOpen} dataset={dataset} />
			<VersionHistorySheet
				open={historyOpen}
				onOpenChange={setHistoryOpen}
				dataset={dataset}
				canEdit={canEdit}
				canDelete={canDelete}
				onRestored={reloadGrid}
				onViewVersion={(number) => {
					setHistoryOpen(false);
					void setUrlState({ version: number });
				}}
			/>

			<Dialog open={splitName !== null} onOpenChange={(open) => !open && setSplitName(null)}>
				<DialogContent className="max-w-sm" data-testid="dataset-split-dialog">
					<DialogHeader>
						<DialogTitle>{t("evals.datasets.splits.new")}</DialogTitle>
					</DialogHeader>
					<Input
						value={splitName ?? ""}
						placeholder={t("evals.datasets.splits.namePlaceholder")}
						aria-label={t("evals.datasets.splits.namePlaceholder")}
						onChange={(event) => setSplitName(event.target.value)}
						data-testid="dataset-split-name-input"
					/>
					<DialogFooter>
						<Button variant="outline" onClick={() => setSplitName(null)}>
							<X className="h-4 w-4" />
							{tCommon("cancel")}
						</Button>
						<Button
							disabled={!splitName?.trim()}
							onClick={() =>
								void run(() => createSplit({ id, name: (splitName ?? "").trim() }).unwrap()).then((ok) => ok && setSplitName(null))
							}
							data-testid="dataset-split-create-btn"
						>
							{t("evals.datasets.splits.create")}
						</Button>
					</DialogFooter>
				</DialogContent>
			</Dialog>
		</div>
	);
}