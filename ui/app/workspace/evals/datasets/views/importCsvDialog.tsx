import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useToast } from "@/hooks/use-toast";
import { getErrorMessage, useImportEvalDatasetCSVMutation, usePreviewEvalDatasetImportMutation } from "@/lib/store";
import type { Dataset, DatasetImportPreview } from "@/lib/types/evalDatasets";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { defaultImportTargets, importMapping, type ImportTarget, newColumnType } from "./datasetUtils";

// The preview reads only the start of the file, so a large file is not sent twice.
const PREVIEW_BYTES = 64 * 1024;
const NO_SPLIT = "__none__";
const NEW_COLUMN = "__new__";
const SKIP = "__skip__";

interface ImportCsvDialogProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	dataset: Dataset;
	// Called after an import that added rows, so the grid can read them.
	onImported?: () => void;
}

// ImportCsvDialog picks a CSV, shows its first rows, lets the user say where each
// field goes, and imports it. The import is all or nothing.
export function ImportCsvDialog({ open, onOpenChange, dataset, onImported }: ImportCsvDialogProps) {
	const { t } = useTranslation("config");
	const { t: tCommon } = useTranslation("common");
	const { toast } = useToast();
	const [previewImport, { isLoading: previewing }] = usePreviewEvalDatasetImportMutation();
	const [importCsv, { isLoading: importing }] = useImportEvalDatasetCSVMutation();

	const [file, setFile] = useState<File | null>(null);
	const [preview, setPreview] = useState<DatasetImportPreview | null>(null);
	const [targets, setTargets] = useState<ImportTarget[]>([]);
	const [hasHeader, setHasHeader] = useState(true);
	const [splitId, setSplitId] = useState(NO_SPLIT);
	const [failure, setFailure] = useState("");

	useEffect(() => {
		if (open) return;
		setFile(null);
		setPreview(null);
		setTargets([]);
		setHasHeader(true);
		setSplitId(NO_SPLIT);
		setFailure("");
	}, [open]);

	const pick = async (picked: File | null) => {
		setFile(picked);
		setPreview(null);
		setFailure("");
		if (!picked) return;
		try {
			const head = await picked.slice(0, PREVIEW_BYTES).text();
			const result = await previewImport({ id: dataset.id, csv: head }).unwrap();
			setPreview(result);
			setTargets(
				defaultImportTargets(
					result.header,
					result.mapping,
					dataset.columns.map((column) => column.type),
				),
			);
		} catch (error) {
			setFailure(getErrorMessage(error));
		}
	};

	// The type a new column made from this field would get, given what is already taken.
	const newTypeFor = (index: number, current: ImportTarget[]) =>
		newColumnType(preview?.header[index] ?? "", [
			...dataset.columns.map((column) => column.type),
			...current.flatMap((target, at) => (at !== index && target.kind === "new" ? [target.type] : [])),
		]);

	const setTarget = (index: number, value: string) =>
		setTargets((current) =>
			current.map((target, at) => {
				if (at !== index) return target;
				if (value === SKIP) return { kind: "skip" };
				if (value === NEW_COLUMN) {
					const type = newTypeFor(index, current);
					return type ? { kind: "new", name: (preview?.header[index] ?? "").trim(), type } : { kind: "skip" };
				}
				return { kind: "column", columnId: value };
			}),
		);

	const mapping = importMapping(targets);
	// Without a header row the first row is data, and fields can only be mapped by position.
	const fieldLabel = (index: number) =>
		hasHeader ? (preview?.header[index] ?? "") : t("evals.datasets.import.fieldNumber", { number: index + 1 });
	const sampleRow = hasHeader ? preview?.rows[0] : preview?.header;

	const submit = async () => {
		if (!file || !mapping) return;
		setFailure("");
		try {
			const result = await importCsv({
				id: dataset.id,
				file,
				hasHeader,
				mapping,
				splitId: splitId === NO_SPLIT ? undefined : splitId,
			}).unwrap();
			toast({ title: t("evals.datasets.import.done", { count: result.rows_added }) });
			onImported?.();
			onOpenChange(false);
		} catch (error) {
			// The reason names the line that failed, so it stays on screen.
			setFailure(getErrorMessage(error));
		}
	};

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent className="max-h-[90vh] max-w-3xl overflow-y-auto" data-testid="dataset-import-dialog">
				<DialogHeader>
					<DialogTitle>{t("evals.datasets.import.title")}</DialogTitle>
					<DialogDescription>{t("evals.datasets.import.description")}</DialogDescription>
				</DialogHeader>
				<div className="flex flex-col gap-4">
					<div className="flex flex-col gap-2">
						<Label htmlFor="import-file">{t("evals.datasets.import.file")}</Label>
						<Input
							id="import-file"
							type="file"
							accept=".csv,text/csv"
							onChange={(event) => void pick(event.target.files?.[0] ?? null)}
							data-testid="dataset-import-file-input"
						/>
					</div>

					{preview && (
						<>
							<div className="flex flex-wrap items-center gap-6">
								<div className="flex items-center gap-2">
									<Checkbox
										id="import-header"
										checked={hasHeader}
										onCheckedChange={(checked) => {
											setHasHeader(checked === true);
											// A new column takes its name from the header, so without one it cannot be made.
											if (checked !== true) {
												setTargets((current) => current.map((target) => (target.kind === "new" ? { kind: "skip" } : target)));
											}
										}}
										data-testid="dataset-import-header-checkbox"
									/>
									<Label htmlFor="import-header">{t("evals.datasets.import.hasHeader")}</Label>
								</div>
								{dataset.splits.length > 0 && (
									<div className="flex items-center gap-2">
										<Label htmlFor="import-split">{t("evals.datasets.import.split")}</Label>
										<Select value={splitId} onValueChange={setSplitId}>
											<SelectTrigger id="import-split" className="w-44" data-testid="dataset-import-split-select">
												<SelectValue />
											</SelectTrigger>
											<SelectContent>
												<SelectItem value={NO_SPLIT}>{t("evals.datasets.splits.none")}</SelectItem>
												{dataset.splits.map((split) => (
													<SelectItem key={split.id} value={split.id}>
														{split.name}
													</SelectItem>
												))}
											</SelectContent>
										</Select>
									</div>
								)}
							</div>

							<div className="rounded-sm border">
								<Table>
									<TableHeader>
										<TableRow>
											<TableHead>{t("evals.datasets.import.csvColumn")}</TableHead>
											<TableHead>{t("evals.datasets.import.sample")}</TableHead>
											<TableHead className="w-64">{t("evals.datasets.import.goesTo")}</TableHead>
										</TableRow>
									</TableHeader>
									<TableBody>
										{preview.header.map((_, index) => {
											const target = targets[index] ?? { kind: "skip" };
											const value = target.kind === "column" ? target.columnId : target.kind === "new" ? NEW_COLUMN : SKIP;
											const usedElsewhere = (columnId: string) =>
												targets.some((other, at) => at !== index && other.kind === "column" && other.columnId === columnId);
											return (
												<TableRow key={index}>
													<TableCell className="font-mono text-xs">{fieldLabel(index)}</TableCell>
													<TableCell className="text-muted-foreground max-w-56 truncate">{sampleRow?.[index] ?? ""}</TableCell>
													<TableCell>
														<Select value={value} onValueChange={(next) => setTarget(index, next)}>
															<SelectTrigger
																aria-label={t("evals.datasets.import.targetFor", { field: fieldLabel(index) })}
																data-testid={`dataset-import-target-${index}`}
															>
																<SelectValue />
															</SelectTrigger>
															<SelectContent>
																{dataset.columns.map((column) => (
																	<SelectItem key={column.id} value={column.id} disabled={usedElsewhere(column.id)}>
																		{column.name} ({t(`evals.datasets.columnTypes.${column.type}`)})
																	</SelectItem>
																))}
																{hasHeader && newTypeFor(index, targets) && (
																	<SelectItem value={NEW_COLUMN}>
																		{t("evals.datasets.import.newColumn", { name: preview.header[index] })} (
																		{t(`evals.datasets.columnTypes.${newTypeFor(index, targets) ?? "variable"}`)})
																	</SelectItem>
																)}
																<SelectItem value={SKIP}>{t("evals.datasets.import.skip")}</SelectItem>
															</SelectContent>
														</Select>
													</TableCell>
												</TableRow>
											);
										})}
									</TableBody>
								</Table>
							</div>
						</>
					)}

					{failure && (
						<Alert variant="destructive" data-testid="dataset-import-error">
							<AlertDescription>{failure}</AlertDescription>
						</Alert>
					)}
					<p className="text-muted-foreground text-xs">{t("evals.datasets.import.note")}</p>
				</div>
				<DialogFooter>
					<Button variant="outline" onClick={() => onOpenChange(false)} data-testid="dataset-import-cancel-btn">
						{tCommon("cancel")}
					</Button>
					<Button
						onClick={() => void submit()}
						disabled={!file || !mapping || previewing || importing}
						data-testid="dataset-import-submit-btn"
					>
						{importing ? t("evals.datasets.import.importing") : t("evals.datasets.import.submit")}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}