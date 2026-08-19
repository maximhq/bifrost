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
	const isCustom = server?.source === "custom";

	return (
		<AlertDialog open={open} onOpenChange={onOpenChange}>
			<AlertDialogContent>
				<AlertDialogHeader>
					<AlertDialogTitle>Remove "{server?.name}" from library?</AlertDialogTitle>
					<AlertDialogDescription>
						{isCustom
							? "This custom server will no longer be available for members to install."
							: "This server will be hidden from the library and will not reappear on the next catalog sync."}{" "}
						Existing installations are not affected.
					</AlertDialogDescription>
				</AlertDialogHeader>
				<AlertDialogFooter>
					<AlertDialogCancel disabled={isDeleting}>Cancel</AlertDialogCancel>
					<AlertDialogAction
						onClick={(event) => {
							event.preventDefault();
							onConfirm();
						}}
						disabled={isDeleting}
						data-testid={confirmTestId}
					>
						{isDeleting ? "Removing..." : "Remove"}
					</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}