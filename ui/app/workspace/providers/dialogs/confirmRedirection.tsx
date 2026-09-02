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
import { useTranslation } from "react-i18next";

interface Props {
	show: boolean;
	onContinue: () => void;
	onCancel: () => void;
}

export default function ConfirmRedirectionDialog({ show, onContinue, onCancel }: Props) {
	const { t } = useTranslation("models");
	const { t: tc } = useTranslation("common");
	return (
		<AlertDialog open={show}>
			<AlertDialogContent>
				<AlertDialogHeader>
					<AlertDialogTitle>{t("providers.redirectTitle")}</AlertDialogTitle>
					<AlertDialogDescription>{t("providers.redirectDescription")}</AlertDialogDescription>
				</AlertDialogHeader>
				<AlertDialogFooter className="mt-4">
					<AlertDialogCancel onClick={onCancel}>{tc("cancel")}</AlertDialogCancel>
					<AlertDialogAction
						onClick={() => {
							onContinue();
						}}
					>
						{t("providers.continue")}
					</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}
