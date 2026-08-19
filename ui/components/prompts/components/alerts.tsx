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

export function DeleteFolderDialog() {
	const { deleteFolderDialog, setDeleteFolderDialog, isDeletingFolder, handleDeleteFolder } = usePromptContext();

	return (
		<AlertDialog open={deleteFolderDialog.open}>
			<AlertDialogContent>
				<AlertDialogHeader>
					<AlertDialogTitle>Delete Folder</AlertDialogTitle>
					<AlertDialogDescription>
						Are you sure you want to delete &quot;{deleteFolderDialog.folder?.name}&quot;? This will also delete all prompts, versions, and
						sessions in this folder. This action cannot be undone.
					</AlertDialogDescription>
				</AlertDialogHeader>
				<AlertDialogFooter>
					<AlertDialogCancel
						data-testid="delete-folder-cancel"
						onClick={() => setDeleteFolderDialog({ open: false })}
						disabled={isDeletingFolder}
					>
						Cancel
					</AlertDialogCancel>
					<AlertDialogAction data-testid="delete-folder-confirm" onClick={handleDeleteFolder} disabled={isDeletingFolder}>
						{isDeletingFolder ? "Deleting..." : "Delete"}
					</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}

export function DeletePromptDialog() {
	const { deletePromptDialog, setDeletePromptDialog, isDeletingPrompt, handleDeletePrompt } = usePromptContext();

	return (
		<AlertDialog open={deletePromptDialog.open}>
			<AlertDialogContent>
				<AlertDialogHeader>
					<AlertDialogTitle>Delete Prompt</AlertDialogTitle>
					<AlertDialogDescription>
						Are you sure you want to delete &quot;{deletePromptDialog.prompt?.name}&quot;? This will also delete all versions and sessions.
						This action cannot be undone.
					</AlertDialogDescription>
				</AlertDialogHeader>
				<AlertDialogFooter>
					<AlertDialogCancel
						data-testid="delete-prompt-cancel"
						onClick={() => setDeletePromptDialog({ open: false })}
						disabled={isDeletingPrompt}
					>
						Cancel
					</AlertDialogCancel>
					<AlertDialogAction data-testid="delete-prompt-confirm" onClick={handleDeletePrompt} disabled={isDeletingPrompt}>
						{isDeletingPrompt ? "Deleting..." : "Delete"}
					</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}