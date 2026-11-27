import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Textarea } from "@/components/ui/textarea";
import { useToast } from "@/hooks/use-toast";
import { getErrorMessage, useCreateEvalDatasetFolderMutation, useUpdateEvalDatasetFolderMutation } from "@/lib/store";
import type { DatasetFolder } from "@/lib/types/evalDatasets";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

interface DatasetFolderSheetProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	// Set to edit a folder. Leave out to create one.
	folder?: DatasetFolder;
	// The folder a new folder is created in. Leave out for the top level.
	parentId?: string;
}

// DatasetFolderSheet creates a dataset folder, or edits its name and description.
export function DatasetFolderSheet({ open, onOpenChange, folder, parentId }: DatasetFolderSheetProps) {
	const { t } = useTranslation("config");
	const { t: tCommon } = useTranslation("common");
	const { toast } = useToast();
	const [createFolder, { isLoading: creating }] = useCreateEvalDatasetFolderMutation();
	const [updateFolder, { isLoading: updating }] = useUpdateEvalDatasetFolderMutation();
	const [name, setName] = useState("");
	const [description, setDescription] = useState("");

	useEffect(() => {
		if (!open) return;
		setName(folder?.name ?? "");
		setDescription(folder?.description ?? "");
	}, [open, folder]);

	const submit = async () => {
		const data = { name: name.trim(), description };
		try {
			if (folder) {
				await updateFolder({ id: folder.id, data }).unwrap();
				toast({ title: t("promptRepo.toastFolderUpdated") });
			} else {
				await createFolder({ ...data, parent_id: parentId }).unwrap();
				toast({ title: t("promptRepo.toastFolderCreated") });
			}
			onOpenChange(false);
		} catch (error) {
			toast({ title: tCommon("error"), description: getErrorMessage(error), variant: "destructive" });
		}
	};

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="p-0 pt-4 sm:max-w-xl" data-testid="dataset-folder-sheet">
				<SheetHeader className="flex flex-col items-start px-0 py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10 px-4 md:px-8">
					<SheetTitle>{folder ? t("promptRepo.editFolder") : t("promptRepo.createFolder")}</SheetTitle>
					<SheetDescription>{folder ? t("promptRepo.updateFolderDesc") : t("evals.datasets.folders.createDescription")}</SheetDescription>
				</SheetHeader>
				<div className="flex flex-1 flex-col gap-4 overflow-y-auto px-4 py-4 md:px-8">
					<div className="flex flex-col gap-2">
						<Label htmlFor="dataset-folder-name">{t("evals.datasets.form.name")}</Label>
						<Input
							id="dataset-folder-name"
							value={name}
							onChange={(event) => setName(event.target.value)}
							data-testid="dataset-folder-name-input"
						/>
					</div>
					<div className="flex flex-col gap-2">
						<Label htmlFor="dataset-folder-description">{t("evals.datasets.form.descriptionLabel")}</Label>
						<Textarea
							id="dataset-folder-description"
							rows={2}
							value={description}
							onChange={(event) => setDescription(event.target.value)}
							data-testid="dataset-folder-description-input"
						/>
					</div>
				</div>
				<SheetFooter className="bg-card sticky bottom-0 flex-row justify-end gap-2 border-t px-4 py-4 md:px-6">
					<Button variant="outline" onClick={() => onOpenChange(false)} data-testid="dataset-folder-cancel-btn">
						{tCommon("cancel")}
					</Button>
					<Button
						onClick={() => void submit()}
						disabled={name.trim() === "" || creating || updating}
						data-testid="dataset-folder-submit-btn"
					>
						{folder ? tCommon("save") : tCommon("create")}
					</Button>
				</SheetFooter>
			</SheetContent>
		</Sheet>
	);
}