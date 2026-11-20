import { Alert, AlertDescription } from "@/components/ui/alert";
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
import { Button } from "@/components/ui/button";
import { getErrorMessage } from "@/lib/store";
import { useDeleteWarpLeftBehindIndexMutation, useDismissWarpLeftBehindIndexMutation } from "@/lib/store/apis/warpApi";
import type { WarpLeftBehindIndex } from "@/lib/types/warp";
import { TriangleAlert } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

interface Props {
	indexes: WarpLeftBehindIndex[];
	canUpdate: boolean;
}

/**
 * Log indexes Warp left behind when warp.vector_store was added, changed or
 * removed. It renders inside the settings form, so its buttons are
 * type="button": a cleanup must not also save the settings. Bifrost does not delete them on the way out: one in the shared store
 * can be deleted from here, one in a store Bifrost no longer reaches can only
 * be described and dismissed.
 */
export default function WarpLeftBehindIndexes({ indexes, canUpdate }: Props) {
	const { t } = useTranslation("config");
	const { t: tc } = useTranslation("common");
	const [confirming, setConfirming] = useState<WarpLeftBehindIndex | null>(null);
	const [deleteIndex, { isLoading: isDeleting }] = useDeleteWarpLeftBehindIndexMutation();
	const [dismissIndex, { isLoading: isDismissing }] = useDismissWarpLeftBehindIndexMutation();

	if (indexes.length === 0) {
		return null;
	}

	const handleDelete = async (entry: WarpLeftBehindIndex) => {
		try {
			await deleteIndex(entry.id).unwrap();
			toast.success(t("warp.leftBehind.deleted"));
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const handleDismiss = async (entry: WarpLeftBehindIndex) => {
		try {
			await dismissIndex(entry.id).unwrap();
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	return (
		<div className="mb-4 flex flex-col gap-2">
			{indexes.map((entry) => (
				<Alert
					key={entry.id}
					variant="warning"
					data-testid={entry.deletable ? "warp-left-behind-index-shared" : "warp-left-behind-index-dedicated"}
				>
					<TriangleAlert className="h-4 w-4" />
					<AlertDescription className="gap-2">
						<span>
							{entry.deletable
								? t("warp.leftBehind.shared", { namespace: entry.namespace })
								: t("warp.leftBehind.dedicated", { namespace: entry.namespace, type: entry.type ?? "" })}
						</span>
						{canUpdate && (
							<div className="flex gap-2">
								{entry.deletable && (
									<Button
										type="button"
										variant="outline"
										size="sm"
										data-testid="warp-left-behind-index-delete-button"
										disabled={isDeleting}
										onClick={() => setConfirming(entry)}
									>
										{t("warp.leftBehind.delete")}
									</Button>
								)}
								<Button
									type="button"
									variant="ghost"
									size="sm"
									data-testid="warp-left-behind-index-dismiss-button"
									disabled={isDismissing}
									onClick={() => handleDismiss(entry)}
								>
									{t("warp.leftBehind.dismiss")}
								</Button>
							</div>
						)}
					</AlertDescription>
				</Alert>
			))}
			<AlertDialog open={confirming !== null} onOpenChange={(open) => !open && setConfirming(null)}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>{t("warp.leftBehind.confirmTitle")}</AlertDialogTitle>
						<AlertDialogDescription>
							{t("warp.leftBehind.confirmDescription", { namespace: confirming?.namespace ?? "" })}
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel data-testid="warp-left-behind-index-cancel-button">{tc("cancel")}</AlertDialogCancel>
						<AlertDialogAction
							data-testid="warp-left-behind-index-confirm-button"
							onClick={() => {
								if (confirming) {
									void handleDelete(confirming);
								}
								setConfirming(null);
							}}
						>
							{t("warp.leftBehind.delete")}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</div>
	);
}