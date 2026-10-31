import ObservabilityView from "@/app/workspace/config/views/observabilityView";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";

interface ObservabilityConfigSheetProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
}

export function ObservabilityConfigSheet({ open, onOpenChange }: ObservabilityConfigSheetProps) {
	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent side="right" className="flex w-full flex-col gap-4 overflow-x-hidden p-4 sm:max-w-[60%] md:p-8">
				<SheetHeader className="flex flex-row items-center px-0">
					<SheetTitle>Observability settings</SheetTitle>
				</SheetHeader>
				<div className="custom-scrollbar min-h-0 flex-1 overflow-y-auto px-4 py-2 md:px-6">
					<ObservabilityView />
				</div>
			</SheetContent>
		</Sheet>
	);
}