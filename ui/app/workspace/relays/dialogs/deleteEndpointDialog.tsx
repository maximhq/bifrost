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
import { useTranslation } from "react-i18next";

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
	const { t } = useTranslation("config");
	const [deleteEndpoint, { isLoading }] = useDeleteRelayMutation();
	const canDelete = useRbac(RbacResource.Relays, RbacOperation.Delete);
	return (
		<AlertDialog open={show}>
			<AlertDialogContent>
				<AlertDialogHeader>
					<AlertDialogTitle>{t("relays.deleteDialog.title")}</AlertDialogTitle>
					<AlertDialogDescription>
						{t("relays.deleteDialog.description", { name })}
					</AlertDialogDescription>
				</AlertDialogHeader>
				<AlertDialogFooter>
					<AlertDialogCancel onClick={onCancel}>{t("cancel", { ns: "common" })}</AlertDialogCancel>
					<AlertDialogAction
						data-testid="relay-delete-confirm"
						disabled={isLoading || !canDelete}
						onClick={() =>
							deleteEndpoint(name)
								.unwrap()
								.then(() => {
									toast.success(t("relays.deleteDialog.toastDeleted"));
									onDeleted();
								})
								.catch((err) => toast.error(t("relays.deleteDialog.toastFailed"), { description: getErrorMessage(err) }))
						}
					>
						{isLoading ? t("relays.deleteDialog.deleting") : t("delete", { ns: "common" })}
					</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}