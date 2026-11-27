import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
} from "@/components/ui/alertDialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useToast } from "@/hooks/use-toast";
import { getLocale } from "@/lib/i18n";
import { dateFnsLocale } from "@/lib/i18n/dateLocale";
import {
	getErrorMessage,
	useDeleteEvalDatasetVersionMutation,
	useGetEvalDatasetDiffQuery,
	useGetEvalDatasetVersionsQuery,
	useRestoreEvalDatasetVersionMutation,
	useUpdateEvalDatasetVersionMutation,
} from "@/lib/store";
import type { Dataset, DatasetChangeKind, DatasetVersion } from "@/lib/types/evalDatasets";
import { cn } from "@/lib/utils";
import { formatDistanceToNow } from "date-fns";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { versionLabel } from "./datasetUtils";

const changeTone: Record<DatasetChangeKind, string> = {
	added: "bg-green-100 text-green-900 dark:bg-green-950 dark:text-green-200",
	removed: "bg-red-100 text-red-900 dark:bg-red-950 dark:text-red-200",
	changed: "bg-blue-100 text-blue-900 dark:bg-blue-950 dark:text-blue-200",
};

interface VersionHistorySheetProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	dataset: Dataset;
	canEdit: boolean;
	canDelete: boolean;
	// Called after a restore, which replaces the draft's rows.
	onRestored?: () => void;
	// Opens a saved version's rows in the grid, read-only.
	onViewVersion: (number: number) => void;
}

// VersionHistorySheet lists a dataset's saved versions and, for the one selected,
// shows what changed against an earlier version. From here a version can be renamed,
// opened, restored or deleted.
export function VersionHistorySheet({
	open,
	onOpenChange,
	dataset,
	canEdit,
	canDelete,
	onViewVersion,
	onRestored,
}: VersionHistorySheetProps) {
	const { t } = useTranslation("config");
	const { t: tCommon } = useTranslation("common");
	const { toast } = useToast();
	const { data } = useGetEvalDatasetVersionsQuery(dataset.id, { skip: !open });
	const versions = useMemo(() => data?.versions ?? [], [data]);
	const [restoreVersion, { isLoading: restoring }] = useRestoreEvalDatasetVersionMutation();
	const [deleteVersion] = useDeleteEvalDatasetVersionMutation();
	const [updateVersion] = useUpdateEvalDatasetVersionMutation();

	const draft = dataset.head_version + 1;
	const hasDraft = dataset.draft_changes > 0;
	// The selected entry is a version number; the draft is head_version + 1.
	const [selected, setSelected] = useState<number | null>(null);
	const [against, setAgainst] = useState<number | null>(null);
	const [name, setName] = useState("");
	const [confirm, setConfirm] = useState<{ kind: "restore" | "delete"; version: DatasetVersion } | null>(null);

	useEffect(() => {
		if (!open) return;
		setSelected((current) => current ?? (hasDraft ? draft : (versions[0]?.number ?? null)));
	}, [open, hasDraft, draft, versions]);

	const selectedVersion = versions.find((version) => version.number === selected);
	useEffect(() => {
		setAgainst(null);
		setName(selectedVersion?.name ?? "");
	}, [selected, selectedVersion?.name]);

	const earlier = versions.filter((version) => selected !== null && version.number < selected);
	const diffArgs =
		selected === null
			? null
			: { id: dataset.id, number: selected === draft ? ("draft" as const) : selected, against: against ?? undefined };
	const { data: diff, isFetching: diffLoading } = useGetEvalDatasetDiffQuery(diffArgs ?? { id: dataset.id, number: "draft" }, {
		skip: !open || diffArgs === null,
	});

	const columnNames = useMemo(() => {
		const names = new Map<string, string>();
		for (const column of dataset.columns) names.set(column.id, column.name);
		for (const version of versions) for (const column of version.columns) if (!names.has(column.id)) names.set(column.id, column.name);
		return names;
	}, [dataset.columns, versions]);

	const run = async (action: () => Promise<unknown>, done: string) => {
		try {
			await action();
			toast({ title: done });
		} catch (error) {
			toast({ title: tCommon("error"), description: getErrorMessage(error), variant: "destructive" });
		}
	};

	const confirmAction = async () => {
		if (!confirm) return;
		const { kind, version } = confirm;
		setConfirm(null);
		if (kind === "restore") {
			await run(
				() => restoreVersion({ id: dataset.id, number: version.number }).unwrap(),
				t("evals.datasets.versions.restored", { number: version.number }),
			);
			onRestored?.();
			setSelected(null);
		} else {
			await run(
				() => deleteVersion({ id: dataset.id, number: version.number }).unwrap(),
				t("evals.datasets.versions.deleted", { number: version.number }),
			);
			setSelected(null);
		}
	};

	const cellsText = (cells?: Record<string, string>) =>
		Object.entries(cells ?? {})
			.map(([columnId, value]) => `${columnNames.get(columnId) ?? columnId}: ${value}`)
			.join("\n");

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="flex w-full flex-col gap-0 p-0 sm:max-w-5xl" data-testid="dataset-history-sheet">
				<SheetHeader className="flex-col items-start gap-0.5" headerClassName="mb-0 border-b px-4 py-3">
					<SheetTitle>{t("evals.datasets.versions.historyTitle")}</SheetTitle>
					<SheetDescription>
						{dataset.max_versions
							? t("evals.datasets.versions.keeping", { count: dataset.max_versions })
							: t("evals.datasets.versions.keepingAll")}
					</SheetDescription>
				</SheetHeader>
				<div className="flex min-h-0 grow">
					<div className="w-80 shrink-0 overflow-y-auto border-r" role="listbox" aria-label={t("evals.datasets.versions.historyTitle")}>
						{hasDraft && (
							<button
								type="button"
								role="option"
								aria-selected={selected === draft}
								onClick={() => setSelected(draft)}
								className={cn("w-full border-b px-4 py-3 text-left", selected === draft && "bg-accent")}
								data-testid="dataset-history-draft"
							>
								<span className="flex items-center gap-2 text-sm font-medium">
									{t("evals.datasets.versions.draft")}
									<Badge variant="secondary">{t("evals.datasets.versions.unsaved", { count: dataset.draft_changes })}</Badge>
								</span>
								<span className="text-muted-foreground text-xs">{t("evals.datasets.versions.draftHint")}</span>
							</button>
						)}
						{versions.length === 0 && !hasDraft && (
							<p className="text-muted-foreground p-4 text-sm">{t("evals.datasets.versions.empty")}</p>
						)}
						{versions.map((version) => (
							<button
								key={version.number}
								type="button"
								role="option"
								aria-selected={selected === version.number}
								onClick={() => setSelected(version.number)}
								className={cn("w-full border-b px-4 py-3 text-left", selected === version.number && "bg-accent")}
								data-testid={`dataset-history-version-${version.number}`}
							>
								<span className="flex items-baseline justify-between gap-2">
									<span className="text-sm font-medium">{versionLabel(version)}</span>
									<span className="text-muted-foreground text-xs whitespace-nowrap">
										{formatDistanceToNow(new Date(version.created_at), { addSuffix: true, locale: dateFnsLocale(getLocale()) })}
									</span>
								</span>
								<span className="text-muted-foreground block text-xs">
									{[version.description, version.created_by].filter(Boolean).join(" · ")}
								</span>
								<span className="mt-1.5 flex flex-wrap gap-1 text-xs">
									{version.rows_added > 0 && (
										<span className={cn("rounded-sm px-1.5", changeTone.added)}>
											{t("evals.datasets.versions.added", { count: version.rows_added })}
										</span>
									)}
									{version.rows_changed > 0 && (
										<span className={cn("rounded-sm px-1.5", changeTone.changed)}>
											{t("evals.datasets.versions.changed", { count: version.rows_changed })}
										</span>
									)}
									{version.rows_removed > 0 && (
										<span className={cn("rounded-sm px-1.5", changeTone.removed)}>
											{t("evals.datasets.versions.removed", { count: version.rows_removed })}
										</span>
									)}
									<span className="bg-muted rounded-sm px-1.5">{t("evals.datasets.versions.rows", { count: version.row_count })}</span>
								</span>
							</button>
						))}
					</div>

					<div className="flex min-w-0 grow flex-col gap-4 overflow-y-auto p-4">
						{selectedVersion && (
							<div className="flex flex-wrap items-end justify-between gap-3">
								<div className="flex items-end gap-2">
									<div className="flex flex-col gap-1">
										<label htmlFor="version-name" className="text-muted-foreground text-xs">
											{t("evals.datasets.versions.nameLabel", { number: selectedVersion.number })}
										</label>
										<Input
											id="version-name"
											className="w-56"
											value={name}
											disabled={!canEdit}
											placeholder={t("evals.datasets.versions.namePlaceholder")}
											onChange={(event) => setName(event.target.value)}
											data-testid="dataset-version-name-input"
										/>
									</div>
									{canEdit && name !== (selectedVersion.name ?? "") && (
										<Button
											variant="outline"
											onClick={() =>
												void run(
													() =>
														updateVersion({
															id: dataset.id,
															number: selectedVersion.number,
															name,
															description: selectedVersion.description,
														}).unwrap(),
													t("evals.datasets.versions.renamed"),
												)
											}
											data-testid="dataset-version-rename-btn"
										>
											{tCommon("save")}
										</Button>
									)}
								</div>
								<div className="flex flex-wrap gap-2">
									<Button variant="outline" onClick={() => onViewVersion(selectedVersion.number)} data-testid="dataset-version-open-btn">
										{t("evals.datasets.versions.openRows")}
									</Button>
									{canEdit && (
										<Button
											variant="outline"
											disabled={restoring || (selectedVersion.number === dataset.head_version && !hasDraft)}
											onClick={() => setConfirm({ kind: "restore", version: selectedVersion })}
											data-testid="dataset-version-restore-btn"
										>
											{t("evals.datasets.versions.restore")}
										</Button>
									)}
									{canDelete && selectedVersion.number !== dataset.head_version && (
										<Button
											variant="outline"
											className="text-destructive"
											onClick={() => setConfirm({ kind: "delete", version: selectedVersion })}
											data-testid="dataset-version-delete-btn"
										>
											{t("evals.datasets.versions.delete")}
										</Button>
									)}
								</div>
							</div>
						)}

						{selected !== null && (
							<div className="rounded-sm border">
								<div className="flex flex-wrap items-center justify-between gap-3 border-b p-3">
									<div className="flex items-center gap-2 text-sm">
										<span className="font-semibold">{t("evals.datasets.versions.compareWith")}</span>
										<Select
											value={String(against ?? diff?.from ?? 0)}
											onValueChange={(value) => setAgainst(Number(value))}
											disabled={earlier.length === 0}
										>
											<SelectTrigger
												className="w-52"
												aria-label={t("evals.datasets.versions.compareWith")}
												data-testid="dataset-diff-against-select"
											>
												<SelectValue />
											</SelectTrigger>
											<SelectContent>
												{earlier.map((version) => (
													<SelectItem key={version.number} value={String(version.number)}>
														{versionLabel(version)}
													</SelectItem>
												))}
												<SelectItem value="0">{t("evals.datasets.versions.emptyDataset")}</SelectItem>
											</SelectContent>
										</Select>
									</div>
									{diff && diff.columns.length > 0 && (
										<span className="text-muted-foreground text-xs">
											{diff.columns
												.map((change) =>
													t(`evals.datasets.versions.column_${change.change}`, { name: change.after?.name ?? change.before?.name ?? "" }),
												)
												.join(" ")}
										</span>
									)}
								</div>
								<Table data-testid="dataset-diff-table">
									<TableHeader>
										<TableRow>
											<TableHead className="w-16">#</TableHead>
											<TableHead className="w-28">{t("evals.datasets.versions.change")}</TableHead>
											<TableHead>{t("evals.datasets.versions.before")}</TableHead>
											<TableHead>{t("evals.datasets.versions.after")}</TableHead>
										</TableRow>
									</TableHeader>
									<TableBody className={diffLoading ? "opacity-60" : undefined}>
										{diff?.rows.length === 0 && (
											<TableRow>
												<TableCell colSpan={4} className="text-muted-foreground h-20 text-center">
													{t("evals.datasets.versions.noRowChanges")}
												</TableCell>
											</TableRow>
										)}
										{diff?.rows.map((row) => (
											<TableRow key={row.row_id}>
												<TableCell className="text-muted-foreground">{row.seq}</TableCell>
												<TableCell>
													<span className={cn("rounded-sm px-1.5 py-0.5 text-xs", changeTone[row.change])}>
														{t(`evals.datasets.versions.kinds.${row.change}`)}
													</span>
												</TableCell>
												<TableCell className="max-w-72 align-top text-xs break-words whitespace-pre-wrap">
													{cellsText(row.before)}
												</TableCell>
												<TableCell className="max-w-72 align-top text-xs break-words whitespace-pre-wrap">{cellsText(row.after)}</TableCell>
											</TableRow>
										))}
									</TableBody>
								</Table>
								{diff && diff.next_after > 0 && (
									<p className="text-muted-foreground border-t p-3 text-xs">{t("evals.datasets.versions.moreChanges")}</p>
								)}
							</div>
						)}
					</div>
				</div>

				<AlertDialog open={confirm !== null} onOpenChange={(next) => !next && setConfirm(null)}>
					<AlertDialogContent>
						<AlertDialogHeader>
							<AlertDialogTitle>
								{confirm?.kind === "restore"
									? t("evals.datasets.versions.restoreTitle", { number: confirm.version.number })
									: t("evals.datasets.versions.deleteTitle", { number: confirm?.version.number ?? 0 })}
							</AlertDialogTitle>
							<AlertDialogDescription>
								{confirm?.kind === "restore"
									? t("evals.datasets.versions.restoreDescription")
									: t("evals.datasets.versions.deleteDescription")}
							</AlertDialogDescription>
						</AlertDialogHeader>
						<AlertDialogFooter>
							<AlertDialogCancel data-testid="dataset-version-confirm-cancel-btn">{tCommon("cancel")}</AlertDialogCancel>
							<AlertDialogAction onClick={() => void confirmAction()} data-testid="dataset-version-confirm-btn">
								{confirm?.kind === "restore" ? t("evals.datasets.versions.restore") : t("evals.datasets.versions.delete")}
							</AlertDialogAction>
						</AlertDialogFooter>
					</AlertDialogContent>
				</AlertDialog>
			</SheetContent>
		</Sheet>
	);
}