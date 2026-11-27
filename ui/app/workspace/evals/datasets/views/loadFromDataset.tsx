import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useToast } from "@/hooks/use-toast";
import {
	getErrorMessage,
	useGetEvalDatasetQuery,
	useGetEvalDatasetRowsQuery,
	useGetEvalDatasetsQuery,
	useLazyGetEvalDatasetRowVariablesQuery,
} from "@/lib/store";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

// How many rows the row picker offers. It is a way to grab a sample, not to browse.
const ROW_CHOICES = 100;

// What part of a dataset a test reads: all of it, one split, or one row.
export type DatasetScope = "dataset" | "split" | "row";

const SCOPES: DatasetScope[] = ["dataset", "split", "row"];

// The rows a test runs on when the scope is a whole dataset or a split.
export interface DatasetBatch {
	datasetId: string;
	splitId?: string;
	total: number;
}

interface LoadFromDatasetProps {
	// Called with the variables of the chosen row, when the scope is one row.
	onLoad: (variables: Record<string, string>) => void;
	// Called with the rows to run on when the scope is a dataset or a split, and with
	// null when it is one row, or nothing is chosen yet.
	onBatchChange: (batch: DatasetBatch | null) => void;
	disabled?: boolean;
}

// LoadFromDataset points an evaluator test at a dataset: one row fills the test
// inputs, and a whole dataset or a split makes the test run on every row of it.
export function LoadFromDataset({ onLoad, onBatchChange, disabled }: LoadFromDatasetProps) {
	const { t } = useTranslation("config");
	const { t: tCommon } = useTranslation("common");
	const { toast } = useToast();
	const { data: datasetsData } = useGetEvalDatasetsQuery({ limit: 200 });
	const datasets = datasetsData?.datasets ?? [];
	const [datasetId, setDatasetId] = useState("");
	const [scope, setScope] = useState<DatasetScope>("row");
	const [splitId, setSplitId] = useState("");
	const [rowId, setRowId] = useState("");
	const { data: datasetData } = useGetEvalDatasetQuery(datasetId, { skip: !datasetId });
	const dataset = datasetData?.dataset.id === datasetId ? datasetData.dataset : undefined;
	const splits = dataset?.splits ?? [];
	const { data: rowsData } = useGetEvalDatasetRowsQuery({ id: datasetId, limit: ROW_CHOICES }, { skip: !datasetId || scope !== "row" });
	const [fetchVariables, { isFetching }] = useLazyGetEvalDatasetRowVariablesQuery();

	useEffect(() => {
		setRowId("");
		setSplitId("");
	}, [datasetId]);

	const split = splits.find((candidate) => candidate.id === splitId);
	const batchSplitId = scope === "split" ? split?.id : undefined;
	const batchTotal = scope === "dataset" ? dataset?.row_count : scope === "split" ? split?.row_count : undefined;
	const batchDatasetId = batchTotal === undefined ? "" : datasetId;
	useEffect(() => {
		onBatchChange(batchDatasetId ? { datasetId: batchDatasetId, splitId: batchSplitId, total: batchTotal ?? 0 } : null);
		// onBatchChange is a state setter in the caller; the target is what matters.
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [batchDatasetId, batchSplitId, batchTotal]);

	// Nothing to load from: the control stays out of the way.
	if (datasets.length === 0) return null;

	const fill = async () => {
		try {
			const result = await fetchVariables({ id: datasetId, rowId }).unwrap();
			onLoad(result.variables);
		} catch (error) {
			toast({ title: tCommon("error"), description: getErrorMessage(error), variant: "destructive" });
		}
	};

	return (
		<div className="flex flex-col gap-3 rounded-sm border p-3" data-testid="evaluator-test-load-from-dataset">
			<div className="flex flex-col gap-1.5">
				<Label htmlFor="load-dataset" className="text-xs">
					{t("evals.datasets.load.label")}
				</Label>
				<Select value={datasetId} onValueChange={setDatasetId} disabled={disabled}>
					<SelectTrigger id="load-dataset" className="w-full" data-testid="evaluator-test-load-dataset-select">
						<SelectValue placeholder={t("evals.datasets.addTo.pickDataset")} />
					</SelectTrigger>
					<SelectContent>
						{datasets.map((candidate) => (
							<SelectItem key={candidate.id} value={candidate.id}>
								{candidate.name}
							</SelectItem>
						))}
					</SelectContent>
				</Select>
			</div>
			<div className="flex flex-col gap-1.5">
				<Label htmlFor="load-scope" className="text-xs">
					{t("evals.datasets.load.scope")}
				</Label>
				<Select value={scope} onValueChange={(value) => setScope(value as DatasetScope)} disabled={disabled || !datasetId}>
					<SelectTrigger id="load-scope" className="w-full" data-testid="evaluator-test-load-scope-select">
						<SelectValue />
					</SelectTrigger>
					<SelectContent>
						{SCOPES.map((option) => (
							<SelectItem key={option} value={option}>
								{t(`evals.datasets.load.scopes.${option}`)}
							</SelectItem>
						))}
					</SelectContent>
				</Select>
			</div>
			{scope === "split" && (
				<div className="flex flex-col gap-1.5">
					<Label htmlFor="load-split" className="text-xs">
						{t("evals.datasets.load.split")}
					</Label>
					<Select value={splitId} onValueChange={setSplitId} disabled={disabled || !datasetId || splits.length === 0}>
						<SelectTrigger id="load-split" className="w-full" data-testid="evaluator-test-load-split-select">
							<SelectValue placeholder={t("evals.datasets.load.pickSplit")} />
						</SelectTrigger>
						<SelectContent>
							{splits.map((candidate) => (
								<SelectItem key={candidate.id} value={candidate.id}>
									{candidate.name} · {candidate.row_count}
								</SelectItem>
							))}
						</SelectContent>
					</Select>
					{dataset && splits.length === 0 && <p className="text-muted-foreground text-xs">{t("evals.datasets.load.noSplits")}</p>}
				</div>
			)}
			{scope === "row" && (
				<>
					<div className="flex flex-col gap-1.5">
						<Label htmlFor="load-row" className="text-xs">
							{t("evals.datasets.load.row")}
						</Label>
						<Select value={rowId} onValueChange={setRowId} disabled={disabled || !datasetId}>
							<SelectTrigger id="load-row" className="w-full" data-testid="evaluator-test-load-row-select">
								<SelectValue placeholder={t("evals.datasets.load.pickRow")} />
							</SelectTrigger>
							<SelectContent>
								{(rowsData?.rows ?? []).map((row, index) => (
									<SelectItem key={row.id} value={row.id}>
										<span className="block max-w-64 truncate">
											{index + 1} · {Object.values(row.cells)[0] ?? ""}
										</span>
									</SelectItem>
								))}
							</SelectContent>
						</Select>
					</div>
					<Button
						type="button"
						variant="outline"
						className="w-full"
						disabled={disabled || !rowId || isFetching}
						onClick={() => void fill()}
						data-testid="evaluator-test-load-fill-btn"
					>
						{t("evals.datasets.load.fill")}
					</Button>
				</>
			)}
			{batchDatasetId && (
				<p className="text-muted-foreground text-xs" data-testid="evaluator-test-load-batch-hint">
					{t("evals.datasets.load.batchHint", { rows: batchTotal ?? 0 })}
				</p>
			)}
		</div>
	);
}