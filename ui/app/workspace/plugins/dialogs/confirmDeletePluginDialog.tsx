import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
} from "@/components/ui/alertDialog";
import { getErrorMessage, useDeletePluginMutation } from "@/lib/store";
import i18n from "@/lib/i18n";
import { Plugin } from "@/lib/types/plugins";
import { AlertDialogTitle } from "@radix-ui/react-alert-dialog";
import { toast } from "sonner";

interface Props {
	show: boolean;
	onCancel: () => void;
	onDelete: () => void;
	plugin: Plugin;
}

export default function ConfirmDeletePluginDialog({ show, onCancel, onDelete, plugin }: Props) {
	const [deletePlugin, { isLoading: isDeletingPlugin }] = useDeletePluginMutation();

	const onDeleteHandler = () => {
		deletePlugin(plugin.name)
			.unwrap()
			.then(() => {
				onDelete();
			})
			.catch((err) => {
				toast.error(i18n.t("workspace.plugins.failedToDeletePlugin"), {
					description: getErrorMessage(err),
				});
			});
	};

	return (
		<AlertDialog open={show}>
			<AlertDialogContent>
				<AlertDialogHeader>
					<AlertDialogTitle>{i18n.t("workspace.plugins.deletePluginTitle")}</AlertDialogTitle>
					<AlertDialogDescription>{i18n.t("workspace.plugins.deletePluginDescription", { name: plugin.name })}</AlertDialogDescription>
				</AlertDialogHeader>
				<AlertDialogFooter>
					<AlertDialogCancel onClick={onCancel}>{i18n.t("workspace.plugins.cancel")}</AlertDialogCancel>
					<AlertDialogAction onClick={onDeleteHandler} disabled={isDeletingPlugin}>
						{isDeletingPlugin ? i18n.t("workspace.plugins.deleting") : i18n.t("common.delete")}
					</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}