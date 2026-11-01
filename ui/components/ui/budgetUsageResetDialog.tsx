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

interface BudgetUsageResetDialogProps {
	"data-testid"?: string;
	open: boolean;
	onOpenChange: (open: boolean) => void;
	/** Called with the operator's choice: true to zero usage, false to preserve it. */
	onChoice: (resetUsage: boolean) => void;
	/** Name of the thing being edited, e.g. "team", used in the prompt copy. */
	ownerLabel: string;
}

/**
 * Asks whether to clear accumulated spend when a budget's configuration changes.
 *
 * Shared by every owner's edit sheet rather than reimplemented per sheet: the
 * wording is a promise about what the backend does, and four copies would drift
 * from each other and from the API.
 *
 * Note the reset clears usage only. The reset boundary is never moved - it is
 * guarded to advance forward only so that cluster nodes agree on which window is
 * current - so the window keeps its existing start and end either way.
 */
export default function BudgetUsageResetDialog({
	"data-testid": testId,
	open,
	onOpenChange,
	onChoice,
	ownerLabel,
}: BudgetUsageResetDialogProps) {
	const { t } = useTranslation("models");
	return (
		<AlertDialog open={open} onOpenChange={onOpenChange}>
			<AlertDialogContent data-testid={testId}>
				<AlertDialogHeader>
					<AlertDialogTitle>{t("budgetUsageReset.title")}</AlertDialogTitle>
					<AlertDialogDescription>{t("budgetUsageReset.description", { owner: ownerLabel })}</AlertDialogDescription>
				</AlertDialogHeader>
				<AlertDialogFooter>
					<AlertDialogCancel onClick={() => onChoice(false)} data-testid={testId ? `${testId}-preserve-btn` : undefined}>
						{t("virtualKeys.preserveUsage")}
					</AlertDialogCancel>
					<AlertDialogAction onClick={() => onChoice(true)} data-testid={testId ? `${testId}-confirm-btn` : undefined}>
						{t("virtualKeys.resetUsage")}
					</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}