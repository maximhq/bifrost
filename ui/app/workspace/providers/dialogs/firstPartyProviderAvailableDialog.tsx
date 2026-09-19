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
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderLabels } from "@/lib/constants/logs";
import { KnownProvider } from "@/lib/types/config";
import { Trans, useTranslation } from "react-i18next";

interface Props {
	show: boolean;
	customProviderName: string;
	knownProvider: KnownProvider;
	onDismiss: () => void;
	onProceed: () => void;
}

export default function FirstPartyProviderAvailableDialog({ show, customProviderName, knownProvider, onDismiss, onProceed }: Props) {
	const { t } = useTranslation("models");
	const label = ProviderLabels[knownProvider];

	return (
		<AlertDialog open={show}>
			<AlertDialogContent data-testid="first-party-provider-available-dialog">
				<AlertDialogHeader>
					<AlertDialogTitle className="flex items-center gap-2">
						<RenderProviderIcon provider={knownProvider as ProviderIconType} size="sm" className="h-5 w-5 shrink-0" />
						{t("providers.firstParty.title", { label })}
					</AlertDialogTitle>
					<AlertDialogDescription>
						<Trans
							i18nKey="providers.firstParty.description"
							ns="models"
							values={{ customName: customProviderName, label }}
							components={{
								name: <span className="text-foreground font-medium" />,
								add: <span className="text-foreground font-medium" />,
							}}
						/>
					</AlertDialogDescription>
				</AlertDialogHeader>
				<AlertDialogFooter>
					<AlertDialogCancel onClick={onDismiss}>{t("providers.firstParty.keepCustom")}</AlertDialogCancel>
					<AlertDialogAction onClick={onProceed}>{t("providers.firstParty.takeMeThere")}</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}
