import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
} from "@/components/ui/alertDialog";
import { getErrorMessage, useDeleteProviderMutation } from "@/lib/store";
import { ModelProvider } from "@/lib/types/config";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { AlertDialogTitle } from "@radix-ui/react-alert-dialog";
import { toast } from "sonner";
import { useTranslation } from "react-i18next";

interface Props {
	show: boolean;
	onCancel: () => void;
	onDelete: () => void;
	provider: ModelProvider;
}

export default function ConfirmDeleteProviderDialog({ show, onCancel, onDelete, provider }: Props) {
	const { t } = useTranslation("models");
	const { t: tc } = useTranslation("common");
	const [deleteProvider, { isLoading: isDeletingProvider }] = useDeleteProviderMutation();
	const hasDeleteAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Delete);

	const onDeleteHandler = () => {
		deleteProvider(provider.name)
			.unwrap()
			.then(() => {
				onDelete();
			})
			.catch((err) => {
				toast.error(t("providers.failedToDelete"), {
					description: getErrorMessage(err),
				});
			});
	};

	return (
		<AlertDialog open={show}>
			<AlertDialogContent>
				<AlertDialogHeader>
					<AlertDialogTitle>{t("providers.deleteTitle")}</AlertDialogTitle>
					<AlertDialogDescription>{t("providers.deleteDescription")}</AlertDialogDescription>
				</AlertDialogHeader>
				<AlertDialogFooter>
					<AlertDialogCancel onClick={onCancel}>{tc("cancel")}</AlertDialogCancel>
					<AlertDialogAction onClick={onDeleteHandler} disabled={isDeletingProvider || !hasDeleteAccess}>
						{isDeletingProvider ? t("providers.deleting") : tc("delete")}
					</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}