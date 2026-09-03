import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
} from "@/components/ui/alertDialog";
import { useT } from "@/lib/i18n";
import { getErrorMessage, useDeleteProviderMutation } from "@/lib/store";
import { ModelProvider } from "@/lib/types/config";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { AlertDialogTitle } from "@radix-ui/react-alert-dialog";
import { toast } from "sonner";

interface Props {
	show: boolean;
	onCancel: () => void;
	onDelete: () => void;
	provider: ModelProvider;
}

export default function ConfirmDeleteProviderDialog({ show, onCancel, onDelete, provider }: Props) {
	const t = useT();
	const [deleteProvider, { isLoading: isDeletingProvider }] = useDeleteProviderMutation();
	const hasDeleteAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Delete);

	const onDeleteHandler = () => {
		deleteProvider(provider.name)
			.unwrap()
			.then(() => {
				onDelete();
			})
			.catch((err) => {
				toast.error(t("providers.deleteFailed"), {
					description: getErrorMessage(err),
				});
			});
	};

	return (
		<AlertDialog open={show}>
			<AlertDialogContent>
				<AlertDialogHeader>
					<AlertDialogTitle>{t("providers.deleteProvider")}</AlertDialogTitle>
					<AlertDialogDescription>
						{t("providers.deleteConfirm")} {t("common.deleteConfirmDescription")}
					</AlertDialogDescription>
				</AlertDialogHeader>
				<AlertDialogFooter>
					<AlertDialogCancel onClick={onCancel}>{t("common.cancel")}</AlertDialogCancel>
					<AlertDialogAction onClick={onDeleteHandler} disabled={isDeletingProvider || !hasDeleteAccess}>
						{isDeletingProvider ? t("providers.deleting") : t("common.delete")}
					</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}