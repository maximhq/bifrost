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

interface Props {
	show: boolean;
	customProviderName: string;
	knownProvider: KnownProvider;
	onDismiss: () => void;
	onProceed: () => void;
}

export default function FirstPartyProviderAvailableDialog({ show, customProviderName, knownProvider, onDismiss, onProceed }: Props) {
	const label = ProviderLabels[knownProvider];

	return (
		<AlertDialog open={show}>
			<AlertDialogContent data-testid="first-party-provider-available-dialog">
				<AlertDialogHeader>
					<AlertDialogTitle className="flex items-center gap-2">
						<RenderProviderIcon provider={knownProvider as ProviderIconType} size="sm" className="h-5 w-5 shrink-0" />
						Official {label} integration available
					</AlertDialogTitle>
					<AlertDialogDescription>
						We noticed you have a custom provider named <span className="text-foreground font-medium">{customProviderName}</span>.
						Bifrost now supports {label} natively. You can delete the custom provider and add the official {label} integration from{" "}
						<span className="text-foreground font-medium">Add Provider</span>, or keep using your custom provider as is.
					</AlertDialogDescription>
				</AlertDialogHeader>
				<AlertDialogFooter>
					<AlertDialogCancel onClick={onDismiss}>Keep custom provider</AlertDialogCancel>
					<AlertDialogAction onClick={onProceed}>Take me there</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}
