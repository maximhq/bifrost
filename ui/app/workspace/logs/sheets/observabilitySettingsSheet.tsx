import LoggingView from "@/app/workspace/config/views/loggingView";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { useTranslation } from "react-i18next";

interface ObservabilitySettingsSheetProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
}

export function ObservabilitySettingsSheet({ open, onOpenChange }: ObservabilitySettingsSheetProps) {
	const { t } = useTranslation("observability");
	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent side="right" className="flex w-full flex-col overflow-x-hidden px-4 pt-6 sm:max-w-3xl md:px-8">
				<SheetHeader className="">
					<SheetTitle className="text-lg font-semibold">{t("logs.loggingSettings")}</SheetTitle>
				</SheetHeader>
				<div className="custom-scrollbar min-h-0 flex-1 overflow-x-hidden overflow-y-auto">
					<LoggingView />
				</div>
			</SheetContent>
		</Sheet>
	);
}