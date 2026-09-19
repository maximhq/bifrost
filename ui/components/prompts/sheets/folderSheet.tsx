import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Textarea } from "@/components/ui/textarea";
import { getErrorMessage } from "@/lib/store";
import { useCreateFolderMutation, useUpdateFolderMutation } from "@/lib/store/apis/promptsApi";
import { Folder } from "@/lib/types/prompts";
import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

interface FolderFormData {
	name: string;
	description: string;
}

interface FolderSheetProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	folder?: Folder;
	onSaved: () => void;
}

export function FolderSheet({ open, onOpenChange, folder, onSaved }: FolderSheetProps) {
	const { t } = useTranslation("config");
	const { t: tc } = useTranslation("common");
	const [createFolder, { isLoading: isCreating }] = useCreateFolderMutation();
	const [updateFolder, { isLoading: isUpdating }] = useUpdateFolderMutation();

	const isLoading = isCreating || isUpdating;
	const isEditing = !!folder;

	const {
		register,
		handleSubmit,
		reset,
		formState: { errors },
	} = useForm<FolderFormData>({
		defaultValues: { name: "", description: "" },
	});

	useEffect(() => {
		if (open) {
			reset({
				name: folder?.name ?? "",
				description: folder?.description ?? "",
			});
		}
	}, [open, folder, reset]);

	async function onSubmit(data: FolderFormData) {
		try {
			if (isEditing) {
				await updateFolder({
					id: folder.id,
					data: { name: data.name.trim(), description: data.description.trim() || undefined },
				}).unwrap();
				toast.success(t("promptRepo.toastFolderUpdated"));
			} else {
				await createFolder({
					name: data.name.trim(),
					description: data.description.trim() || undefined,
				}).unwrap();
				toast.success(t("promptRepo.toastFolderCreated"));
			}
			onSaved();
			onOpenChange(false);
		} catch (err) {
			toast.error(t("promptRepo.toastFolderFailed", { action: isEditing ? t("promptRepo.updateAction") : t("promptRepo.createAction") }), {
				description: getErrorMessage(err),
			});
		}
	}

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent
				className="p-4 md:p-8"
				onOpenAutoFocus={(e) => {
					e.preventDefault();
					document.getElementById("name")?.focus();
				}}
			>
				<form onSubmit={handleSubmit(onSubmit)}>
					<SheetHeader className="flex flex-col items-start">
						<SheetTitle>{isEditing ? t("promptRepo.editFolder") : t("promptRepo.createFolder")}</SheetTitle>
						<SheetDescription>
							{isEditing ? t("promptRepo.updateFolderDesc") : t("promptRepo.createFolderDesc")}
						</SheetDescription>
					</SheetHeader>

					<div className="mt-6 space-y-4">
						<div className="space-y-2">
							<Label htmlFor="name">{t("promptRepo.name")}</Label>
							<Input
								id="name"
								data-testid="folder-name-input"
								placeholder={t("promptRepo.folderNamePlaceholder")}
								{...register("name", {
									required: t("promptRepo.folderNameRequired"),
									validate: (v) => v.trim().length > 0 || t("promptRepo.folderNameBlank"),
								})}
								autoFocus
							/>
							{errors.name && <p className="text-destructive text-xs">{errors.name.message}</p>}
						</div>

						<div className="space-y-2">
							<Label htmlFor="description">{t("promptRepo.descriptionOptional")}</Label>
							<Textarea
								id="description"
								data-testid="folder-description-input"
								placeholder={t("promptRepo.folderDescriptionPlaceholder")}
								className="resize-none"
								{...register("description")}
							/>
						</div>
					</div>

					<SheetFooter className="mt-6 flex flex-row items-center justify-end gap-2 p-0">
						<Button type="button" variant="outline" data-testid="folder-cancel" onClick={() => onOpenChange(false)}>
							{tc("cancel")}
						</Button>
						<Button type="submit" data-testid="folder-submit" disabled={isLoading}>
							{isLoading ? t("promptRepo.savingDots") : isEditing ? t("promptRepo.update") : tc("create")}
						</Button>
					</SheetFooter>
				</form>
			</SheetContent>
		</Sheet>
	);
}