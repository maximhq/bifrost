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
import type { MCPLibraryEntry } from "@/lib/types/mcp";
import { useTranslation } from "react-i18next";

interface MCPLibraryDeleteDialogProps {
	/** The entry being removed; when null the dialog is closed. */
	server: MCPLibraryEntry | null;
	open: boolean;
	isDeleting: boolean;
	onOpenChange: (open: boolean) => void;
	onConfirm: () => void;
	confirmTestId: string;
}

// Shared confirmation dialog for soft-deleting a library entry, used by both the
// card and table views. Copy is sync-aware: custom entries simply disappear,
// while remote entries are tombstoned so they don't reappear on the next sync.
export function MCPLibraryDeleteDialog({ server, open, isDeleting, onOpenChange, onConfirm, confirmTestId }: MCPLibraryDeleteDialogProps) {
	const { t } = useTranslation("mcp");
	const { t: tc } = useTranslation("common");
	const isCustom = server?.source === "custom";

	return (
		<AlertDialog open={open} onOpenChange={onOpenChange}>
			<AlertDialogContent>
				<AlertDialogHeader>
					<AlertDialogTitle>{t("library.delete.title", { name: server?.name })}</AlertDialogTitle>
					<AlertDialogDescription>
						{isCustom ? t("library.delete.customBody") : t("library.delete.remoteBody")} {t("library.delete.existingInstalls")}
					</AlertDialogDescription>
				</AlertDialogHeader>
				<AlertDialogFooter>
					<AlertDialogCancel disabled={isDeleting}>{tc("cancel")}</AlertDialogCancel>
					<AlertDialogAction
						onClick={(event) => {
							event.preventDefault();
							onConfirm();
						}}
						disabled={isDeleting}
						data-testid={confirmTestId}
					>
						{isDeleting ? t("common.removing") : t("common.remove")}
					</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}