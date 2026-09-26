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
import { usePromptContext } from "../context";
import { useTranslation } from "react-i18next";

export function DeleteFolderDialog() {
	const { t } = useTranslation("config");
	const { t: tc } = useTranslation("common");
	const { deleteFolderDialog, setDeleteFolderDialog, isDeletingFolder, handleDeleteFolder } = usePromptContext();

	return (
		<AlertDialog open={deleteFolderDialog.open}>
			<AlertDialogContent>
				<AlertDialogHeader>
					<AlertDialogTitle>{t("promptRepo.deleteFolder")}</AlertDialogTitle>
					<AlertDialogDescription>
						{t("promptRepo.deleteFolderConfirm", { name: deleteFolderDialog.folder?.name })}
					</AlertDialogDescription>
				</AlertDialogHeader>
				<AlertDialogFooter>
					<AlertDialogCancel
						data-testid="delete-folder-cancel"
						onClick={() => setDeleteFolderDialog({ open: false })}
						disabled={isDeletingFolder}
					>
						{tc("cancel")}
					</AlertDialogCancel>
					<AlertDialogAction data-testid="delete-folder-confirm" onClick={handleDeleteFolder} disabled={isDeletingFolder}>
						{isDeletingFolder ? t("promptRepo.deletingDots") : tc("delete")}
					</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}

export function DeletePromptDialog() {
	const { t } = useTranslation("config");
	const { t: tc } = useTranslation("common");
	const { deletePromptDialog, setDeletePromptDialog, isDeletingPrompt, handleDeletePrompt } = usePromptContext();

	return (
		<AlertDialog open={deletePromptDialog.open}>
			<AlertDialogContent>
				<AlertDialogHeader>
					<AlertDialogTitle>{t("promptRepo.deletePrompt")}</AlertDialogTitle>
					<AlertDialogDescription>
						{t("promptRepo.deletePromptConfirm", { name: deletePromptDialog.prompt?.name })}
					</AlertDialogDescription>
				</AlertDialogHeader>
				<AlertDialogFooter>
					<AlertDialogCancel
						data-testid="delete-prompt-cancel"
						onClick={() => setDeletePromptDialog({ open: false })}
						disabled={isDeletingPrompt}
					>
						{tc("cancel")}
					</AlertDialogCancel>
					<AlertDialogAction data-testid="delete-prompt-confirm" onClick={handleDeletePrompt} disabled={isDeletingPrompt}>
						{isDeletingPrompt ? t("promptRepo.deletingDots") : tc("delete")}
					</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}