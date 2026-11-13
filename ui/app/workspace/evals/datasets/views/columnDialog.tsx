import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useToast } from "@/hooks/use-toast";
import { getErrorMessage, useAddEvalDatasetColumnMutation, useUpdateEvalDatasetColumnMutation } from "@/lib/store";
import { DATASET_COLUMN_TYPES, type DatasetColumn, type DatasetColumnType } from "@/lib/types/evalDatasets";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { columnTypesLeft, isReservedVariableName, isStructuredColumn } from "./datasetUtils";

interface ColumnDialogProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	datasetId: string;
	columns: DatasetColumn[];
	// Set to rename or retype a column. Leave out to add one.
	column?: DatasetColumn;
}

// ColumnDialog adds a column to a dataset, or renames and retypes an existing one.
export function ColumnDialog({ open, onOpenChange, datasetId, columns, column }: ColumnDialogProps) {
	const { t } = useTranslation("config");
	const { t: tCommon } = useTranslation("common");
	const { toast } = useToast();
	const [addColumn, { isLoading: adding }] = useAddEvalDatasetColumnMutation();
	const [updateColumn, { isLoading: updating }] = useUpdateEvalDatasetColumnMutation();
	const [name, setName] = useState("");
	const [type, setType] = useState<DatasetColumnType>("variable");

	useEffect(() => {
		if (!open) return;
		setName(column?.name ?? "");
		setType(column?.type ?? "variable");
	}, [open, column]);

	const types = useMemo(
		() =>
			columnTypesLeft(
				DATASET_COLUMN_TYPES,
				columns.map((existing) => existing.type),
				column?.type,
			),
		[columns, column],
	);
	const trimmed = name.trim();
	const duplicate = columns.some((existing) => existing.id !== column?.id && existing.name === trimmed);
	const reserved = (type === "variable" || type === "file") && isReservedVariableName(trimmed);
	const problem = duplicate
		? t("evals.datasets.columns.nameTaken")
		: reserved
			? t("evals.datasets.columns.nameReserved", { name: trimmed })
			: "";
	const canSubmit = trimmed !== "" && !problem && !adding && !updating;

	const submit = async () => {
		try {
			if (column) {
				await updateColumn({ id: datasetId, columnId: column.id, data: { name: trimmed, type } }).unwrap();
			} else {
				await addColumn({ id: datasetId, data: { name: trimmed, type } }).unwrap();
			}
			onOpenChange(false);
		} catch (error) {
			toast({ title: tCommon("error"), description: getErrorMessage(error), variant: "destructive" });
		}
	};

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent className="max-w-md" data-testid="dataset-column-dialog">
				<DialogHeader>
					<DialogTitle>{column ? t("evals.datasets.columns.editTitle") : t("evals.datasets.columns.addTitle")}</DialogTitle>
					<DialogDescription>{t("evals.datasets.columns.description")}</DialogDescription>
				</DialogHeader>
				<div className="flex flex-col gap-4">
					<div className="flex flex-col gap-2">
						<Label htmlFor="column-name">{t("evals.datasets.columns.name")}</Label>
						<Input
							id="column-name"
							value={name}
							aria-invalid={Boolean(problem)}
							onChange={(event) => setName(event.target.value)}
							data-testid="dataset-column-name-input"
						/>
						{problem && <p className="text-destructive text-xs">{problem}</p>}
					</div>
					<div className="flex flex-col gap-2">
						<Label htmlFor="column-type">{t("evals.datasets.columns.type")}</Label>
						<Select value={type} onValueChange={(value) => setType(value as DatasetColumnType)}>
							<SelectTrigger id="column-type" data-testid="dataset-column-type-select">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								{types.map((option) => (
									<SelectItem key={option} value={option} data-testid={`dataset-column-type-${option}`}>
										{t(`evals.datasets.columnTypes.${option}`)}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
						<p className="text-muted-foreground text-xs">
							{t(`evals.datasets.columnTypeHints.${type}`)}
							{isStructuredColumn(type) && ` ${t("evals.datasets.columns.jsonHint")}`}
						</p>
					</div>
				</div>
				<DialogFooter>
					<Button variant="outline" onClick={() => onOpenChange(false)} data-testid="dataset-column-cancel-btn">
						{tCommon("cancel")}
					</Button>
					<Button onClick={() => void submit()} disabled={!canSubmit} data-testid="dataset-column-submit-btn">
						{column ? tCommon("save") : t("evals.datasets.columns.add")}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}