import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Textarea } from "@/components/ui/textarea";
import { useToast } from "@/hooks/use-toast";
import { getErrorMessage, useCreateEvalDatasetMutation, useUpdateEvalDatasetMutation } from "@/lib/store";
import type { Dataset, DatasetTemplate } from "@/lib/types/evalDatasets";
import { cn } from "@/lib/utils";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { parseTags } from "./datasetUtils";

type StartWith = "empty" | DatasetTemplate | "csv";

const STARTS: { value: StartWith; columns?: string }[] = [
	{ value: "empty" },
	{ value: "qa", columns: "input, expected_output" },
	{ value: "retrieval", columns: "input, context, expected_output" },
	{ value: "agent", columns: "input, expected_tool_calls, expected_steps" },
	{ value: "csv" },
];

interface DatasetFormSheetProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	// Set to edit an existing dataset's details. Leave out to create one.
	dataset?: Dataset;
	// Called with the new dataset and whether the user chose to start from a CSV.
	onCreated?: (dataset: Dataset, importNext: boolean) => void;
	// The folder a new dataset is created in. Leave out for none.
	folderId?: string;
}

// DatasetFormSheet creates a dataset, or edits the name, description, tags and
// version limit of an existing one.
export function DatasetFormSheet({ open, onOpenChange, dataset, onCreated, folderId }: DatasetFormSheetProps) {
	const { t } = useTranslation("config");
	const { t: tCommon } = useTranslation("common");
	const { toast } = useToast();
	const [createDataset, { isLoading: creating }] = useCreateEvalDatasetMutation();
	const [updateDataset, { isLoading: updating }] = useUpdateEvalDatasetMutation();

	const [name, setName] = useState("");
	const [description, setDescription] = useState("");
	const [tags, setTags] = useState("");
	const [maxVersions, setMaxVersions] = useState("");
	const [start, setStart] = useState<StartWith>("qa");

	useEffect(() => {
		if (!open) return;
		setName(dataset?.name ?? "");
		setDescription(dataset?.description ?? "");
		setTags((dataset?.tags ?? []).join(", "));
		setMaxVersions(dataset?.max_versions ? String(dataset.max_versions) : "");
		setStart("qa");
	}, [open, dataset]);

	const limit = maxVersions.trim() === "" ? null : Number(maxVersions);
	const limitValid = limit === null || (Number.isInteger(limit) && limit >= 1);
	const canSubmit = name.trim() !== "" && limitValid && !creating && !updating;

	const submit = async () => {
		const details = { name: name.trim(), description, tags: parseTags(tags), max_versions: limit };
		try {
			if (dataset) {
				await updateDataset({ id: dataset.id, data: details }).unwrap();
				toast({ title: t("evals.datasets.form.saved") });
			} else {
				const template = start === "empty" || start === "csv" ? undefined : start;
				const created = await createDataset({ ...details, template, folder_id: folderId }).unwrap();
				onCreated?.(created.dataset, start === "csv");
			}
			onOpenChange(false);
		} catch (error) {
			toast({ title: tCommon("error"), description: getErrorMessage(error), variant: "destructive" });
		}
	};

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="p-0 pt-4 sm:max-w-xl" data-testid="dataset-form-sheet">
				<SheetHeader className="flex flex-col items-start px-0 py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10 px-4 md:px-8">
					<SheetTitle>{dataset ? t("evals.datasets.form.editTitle") : t("evals.datasets.form.newTitle")}</SheetTitle>
					<SheetDescription>{t("evals.datasets.form.description")}</SheetDescription>
				</SheetHeader>
				<div className="flex flex-1 flex-col gap-4 overflow-y-auto px-4 py-4 md:px-8">
					<div className="flex flex-col gap-2">
						<Label htmlFor="dataset-name">{t("evals.datasets.form.name")}</Label>
						<Input id="dataset-name" value={name} onChange={(event) => setName(event.target.value)} data-testid="dataset-form-name-input" />
					</div>
					<div className="flex flex-col gap-2">
						<Label htmlFor="dataset-description">{t("evals.datasets.form.descriptionLabel")}</Label>
						<Textarea
							id="dataset-description"
							rows={2}
							value={description}
							onChange={(event) => setDescription(event.target.value)}
							data-testid="dataset-form-description-input"
						/>
					</div>
					<div className="flex flex-col gap-2">
						<Label htmlFor="dataset-tags">{t("evals.datasets.form.tags")}</Label>
						<Input
							id="dataset-tags"
							value={tags}
							placeholder={t("evals.datasets.form.tagsPlaceholder")}
							onChange={(event) => setTags(event.target.value)}
							data-testid="dataset-form-tags-input"
						/>
					</div>
					{dataset ? (
						<div className="flex flex-col gap-2">
							<Label htmlFor="dataset-max-versions">{t("evals.datasets.form.maxVersions")}</Label>
							<Input
								id="dataset-max-versions"
								type="number"
								min={1}
								className="w-40"
								value={maxVersions}
								placeholder={t("evals.datasets.form.keepAll")}
								onChange={(event) => setMaxVersions(event.target.value)}
								data-testid="dataset-form-max-versions-input"
							/>
							<p className="text-muted-foreground text-xs">{t("evals.datasets.form.maxVersionsHint")}</p>
						</div>
					) : (
						<fieldset className="flex flex-col gap-2">
							<legend className="mb-2 text-sm font-medium">{t("evals.datasets.form.startWith")}</legend>
							{STARTS.map((option) => (
								<label
									key={option.value}
									className={cn(
										"flex cursor-pointer gap-3 rounded-sm border px-3 py-2",
										start === option.value ? "border-primary ring-primary ring-1" : "border-border",
									)}
								>
									<input
										type="radio"
										name="dataset-start"
										className="mt-1"
										checked={start === option.value}
										onChange={() => setStart(option.value)}
										data-testid={`dataset-form-start-${option.value}`}
									/>
									<span className="flex flex-col">
										<span className="text-sm font-medium">{t(`evals.datasets.form.starts.${option.value}`)}</span>
										<span className="text-muted-foreground text-xs">
											{option.columns ? (
												<span className="font-mono">{option.columns}</span>
											) : option.value === "csv" ? (
												t("evals.datasets.form.startHints.csv")
											) : (
												t("evals.datasets.form.startHints.empty")
											)}
										</span>
									</span>
								</label>
							))}
						</fieldset>
					)}
				</div>
				<SheetFooter className="bg-card sticky bottom-0 flex-row justify-end gap-2 border-t px-4 py-4 md:px-6">
					<Button variant="outline" onClick={() => onOpenChange(false)} data-testid="dataset-form-cancel-btn">
						{tCommon("cancel")}
					</Button>
					<Button onClick={() => void submit()} disabled={!canSubmit} data-testid="dataset-form-submit-btn">
						{dataset ? tCommon("save") : t("evals.datasets.form.create")}
					</Button>
				</SheetFooter>
			</SheetContent>
		</Sheet>
	);
}