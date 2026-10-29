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
import { getErrorMessage, useDeleteRelayMutation } from "@/lib/store";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { toast } from "sonner";

/** Confirms deleting a relay, worded like deleting a provider. */
export function DeleteEndpointDialog({
	name,
	show,
	onCancel,
	onDeleted,
}: {
	name: string;
	show: boolean;
	onCancel: () => void;
	onDeleted: () => void;
}) {
	const [deleteEndpoint, { isLoading }] = useDeleteRelayMutation();
	const canDelete = useRbac(RbacResource.Relays, RbacOperation.Delete);
	return (
		<AlertDialog open={show}>
			<AlertDialogContent>
				<AlertDialogHeader>
					<AlertDialogTitle>Delete Relay</AlertDialogTitle>
					<AlertDialogDescription>
						Are you sure you want to delete this relay? Calls to /relay/{name} stop working and its virtual key grants are removed. This
						action cannot be undone.
					</AlertDialogDescription>
				</AlertDialogHeader>
				<AlertDialogFooter>
					<AlertDialogCancel onClick={onCancel}>Cancel</AlertDialogCancel>
					<AlertDialogAction
						data-testid="relay-delete-confirm"
						disabled={isLoading || !canDelete}
						onClick={() =>
							deleteEndpoint(name)
								.unwrap()
								.then(() => {
									toast.success("Relay deleted successfully");
									onDeleted();
								})
								.catch((err) => toast.error("Failed to delete relay", { description: getErrorMessage(err) }))
						}
					>
						{isLoading ? "Deleting..." : "Delete"}
					</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}