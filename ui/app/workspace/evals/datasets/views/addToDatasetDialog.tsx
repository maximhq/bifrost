import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useToast } from "@/hooks/use-toast";
import {
	getErrorMessage,
	useAddEvalDatasetRowsFromLogsMutation,
	useAddEvalDatasetRowsFromSessionsMutation,
	useGetEvalDatasetQuery,
	useGetEvalDatasetsQuery,
} from "@/lib/store";
import type { DatasetCurateResult } from "@/lib/types/evalDatasets";
import { Link } from "@tanstack/react-router";
import { Database } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

const NO_SPLIT = "__none__";

interface AddToDatasetDialogProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	// Give log IDs to add one row per log, or session IDs to add one row per session.
	logIds?: string[];
	sessionIds?: string[];
}

// AddToDatasetDialog turns request logs, or whole sessions, into dataset rows.
export function AddToDatasetDialog({ open, onOpenChange, logIds, sessionIds }: AddToDatasetDialogProps) {
	const { t } = useTranslation("config");
	const { t: tCommon } = useTranslation("common");
	const { toast } = useToast();
	const sessions = (sessionIds?.length ?? 0) > 0;
	const count = sessions ? (sessionIds?.length ?? 0) : (logIds?.length ?? 0);

	const { data: datasetsData } = useGetEvalDatasetsQuery({ limit: 200 }, { skip: !open });
	const datasets = datasetsData?.datasets ?? [];
	const [datasetId, setDatasetId] = useState("");
	const [splitId, setSplitId] = useState(NO_SPLIT);
	const { data: datasetData } = useGetEvalDatasetQuery(datasetId, { skip: !open || !datasetId });
	const splits = datasetData?.dataset.splits ?? [];
	const [addFromLogs, { isLoading: addingLogs }] = useAddEvalDatasetRowsFromLogsMutation();
	const [addFromSessions, { isLoading: addingSessions }] = useAddEvalDatasetRowsFromSessionsMutation();
	const [result, setResult] = useState<DatasetCurateResult | null>(null);

	useEffect(() => {
		if (!open) {
			setResult(null);
			setSplitId(NO_SPLIT);
		}
	}, [open]);
	useEffect(() => setSplitId(NO_SPLIT), [datasetId]);

	const submit = async () => {
		const split = splitId === NO_SPLIT ? undefined : splitId;
		try {
			const outcome = sessions
				? await addFromSessions({ id: datasetId, sessionIds: sessionIds ?? [], splitId: split }).unwrap()
				: await addFromLogs({ id: datasetId, logIds: logIds ?? [], splitId: split }).unwrap();
			toast({ title: t("evals.datasets.addTo.done", { count: outcome.rows_added }) });
			// Skipped items stay on screen with their reasons; a clean run closes the dialog.
			if (outcome.skipped.length > 0) setResult(outcome);
			else onOpenChange(false);
		} catch (error) {
			toast({ title: tCommon("error"), description: getErrorMessage(error), variant: "destructive" });
		}
	};

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent className="max-w-md" data-testid="add-to-dataset-dialog">
				<DialogHeader>
					<DialogTitle>
						{sessions ? t("evals.datasets.addTo.titleSessions", { count }) : t("evals.datasets.addTo.titleLogs", { count })}
					</DialogTitle>
					<DialogDescription>
						{sessions ? t("evals.datasets.addTo.descriptionSessions") : t("evals.datasets.addTo.descriptionLogs")}
					</DialogDescription>
				</DialogHeader>
				{datasetsData && datasets.length === 0 ? (
					<p className="text-muted-foreground text-sm">
						{t("evals.datasets.addTo.noDatasets")}{" "}
						<Link to="/workspace/evals/datasets" className="underline" data-testid="add-to-dataset-create-link">
							{t("evals.datasets.list.newDataset")}
						</Link>
					</p>
				) : (
					<div className="flex flex-col gap-4">
						<div className="flex flex-col gap-2">
							<Label htmlFor="add-to-dataset">{t("evals.datasets.addTo.dataset")}</Label>
							<Select value={datasetId} onValueChange={setDatasetId}>
								<SelectTrigger id="add-to-dataset" data-testid="add-to-dataset-select">
									<SelectValue placeholder={t("evals.datasets.addTo.pickDataset")} />
								</SelectTrigger>
								<SelectContent>
									{datasets.map((dataset) => (
										<SelectItem key={dataset.id} value={dataset.id}>
											{dataset.name}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</div>
						{splits.length > 0 && (
							<div className="flex flex-col gap-2">
								<Label htmlFor="add-to-split">{t("evals.datasets.addTo.split")}</Label>
								<Select value={splitId} onValueChange={setSplitId}>
									<SelectTrigger id="add-to-split" data-testid="add-to-dataset-split-select">
										<SelectValue />
									</SelectTrigger>
									<SelectContent>
										<SelectItem value={NO_SPLIT}>{t("evals.datasets.splits.none")}</SelectItem>
										{splits.map((split) => (
											<SelectItem key={split.id} value={split.id}>
												{split.name}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
							</div>
						)}
						{result && (
							<Alert data-testid="add-to-dataset-skipped">
								<AlertDescription>
									<p className="font-medium">{t("evals.datasets.addTo.skipped", { count: result.skipped.length })}</p>
									<ul className="mt-1 list-disc pl-4 text-xs">
										{result.skipped.map((item) => (
											<li key={item.id}>
												<span className="font-mono">{item.id}</span>: {item.reason}
											</li>
										))}
									</ul>
								</AlertDescription>
							</Alert>
						)}
						<p className="text-muted-foreground text-xs">{t("evals.datasets.addTo.note")}</p>
					</div>
				)}
				<DialogFooter>
					<Button variant="outline" onClick={() => onOpenChange(false)} data-testid="add-to-dataset-cancel-btn">
						{result ? tCommon("close") : tCommon("cancel")}
					</Button>
					{!result && (
						<Button
							onClick={() => void submit()}
							disabled={!datasetId || count === 0 || addingLogs || addingSessions}
							data-testid="add-to-dataset-submit-btn"
						>
							{t("evals.datasets.addTo.submit")}
						</Button>
					)}
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}

// AddToDatasetButton is the action with its own dialog, for places that have one log.
export function AddToDatasetButton({ logId }: { logId: string }) {
	const { t } = useTranslation("config");
	const [open, setOpen] = useState(false);
	return (
		<>
			<Button variant="outline" size="sm" onClick={() => setOpen(true)} data-testid="log-add-to-dataset-btn">
				<Database className="h-4 w-4" />
				{t("evals.datasets.addTo.button")}
			</Button>
			<AddToDatasetDialog open={open} onOpenChange={setOpen} logIds={[logId]} />
		</>
	);
}