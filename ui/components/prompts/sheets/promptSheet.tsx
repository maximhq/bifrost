import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { getErrorMessage } from "@/lib/store";
import { useCreatePromptMutation, useUpdatePromptMutation } from "@/lib/store/apis/promptsApi";
import { Prompt } from "@/lib/types/prompts";
import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

interface PromptFormData {
	name: string;
}

interface PromptSheetProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	prompt?: Prompt;
	folderId?: string;
	onSaved: (promptId?: string) => void;
}

export function PromptSheet({ open, onOpenChange, prompt, folderId, onSaved }: PromptSheetProps) {
	const { t } = useTranslation();
	const [createPrompt, { isLoading: isCreating }] = useCreatePromptMutation();
	const [updatePrompt, { isLoading: isUpdating }] = useUpdatePromptMutation();

	const isLoading = isCreating || isUpdating;
	const isEditing = !!prompt;

	const {
		register,
		handleSubmit,
		reset,
		formState: { errors },
	} = useForm<PromptFormData>({
		defaultValues: { name: "" },
	});

	useEffect(() => {
		if (open) {
			reset({ name: prompt?.name ?? "" });
		}
	}, [open, prompt, reset]);

	async function onSubmit(data: PromptFormData) {
		try {
			if (isEditing) {
				await updatePrompt({
					id: prompt.id,
					data: { name: data.name.trim() },
				}).unwrap();
				toast.success(t("workspace.promptRepository.sheets.promptUpdated"));
				onSaved();
			} else {
				const result = await createPrompt({
					name: data.name.trim(),
					...(folderId ? { folder_id: folderId } : {}),
				}).unwrap();
				toast.success(t("workspace.promptRepository.sheets.promptCreated"));
				onSaved(result.prompt.id);
			}
			onOpenChange(false);
		} catch (err) {
			toast.error(
				t("workspace.promptRepository.sheets.promptSaveFailed", {
					action: isEditing ? t("workspace.promptRepository.sheets.actionUpdate") : t("workspace.promptRepository.sheets.actionCreate"),
				}),
				{
					description: getErrorMessage(err),
				},
			);
		}
	}

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent
				className="p-0"
				onOpenAutoFocus={(e) => {
					e.preventDefault();
					document.getElementById("name")?.focus();
				}}
			>
				<form onSubmit={handleSubmit(onSubmit)} className="flex grow flex-col">
					<SheetHeader className="flex flex-col items-start px-8 pt-8">
						<SheetTitle>
							{isEditing ? t("workspace.promptRepository.sheets.renamePrompt") : t("workspace.promptRepository.sheets.createPrompt")}
						</SheetTitle>
						<SheetDescription>
							{isEditing
								? t("workspace.promptRepository.sheets.updatePromptName")
								: folderId
									? t("workspace.promptRepository.sheets.createPromptInFolder")
									: t("workspace.promptRepository.sheets.createPromptDescription")}
						</SheetDescription>
					</SheetHeader>

					<div className="flex grow flex-col gap-6">
						<div className="grow space-y-4 px-8">
							<div className="space-y-2">
								<Label htmlFor="name">{t("workspace.promptRepository.sheets.name")}</Label>
								<Input
									id="name"
									data-testid="prompt-name-input"
									placeholder={t("workspace.promptRepository.sheets.promptNamePlaceholder")}
									{...register("name", {
										required: t("workspace.promptRepository.sheets.promptNameRequired"),
										validate: (v) => v.trim().length > 0 || t("workspace.promptRepository.sheets.promptNameBlank"),
									})}
									autoFocus
								/>
								{errors.name && <p className="text-destructive text-xs">{errors.name.message}</p>}
							</div>
						</div>

						<SheetFooter className="flex flex-row items-center justify-end gap-2 border-t px-8 py-4">
							<Button type="button" variant="outline" data-testid="prompt-cancel" onClick={() => onOpenChange(false)}>
								{t("workspace.promptRepository.sheets.cancel")}
							</Button>
							<Button type="submit" data-testid="prompt-submit" disabled={isLoading}>
								{isLoading
									? t("workspace.promptRepository.sheets.saving")
									: isEditing
										? t("workspace.promptRepository.sheets.update")
										: t("workspace.promptRepository.sheets.create")}
							</Button>
						</SheetFooter>
					</div>
				</form>
			</SheetContent>
		</Sheet>
	);
}