import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { LoaderCircle, Save } from "lucide-react";
import type { ReactNode } from "react";

interface Props {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	// The shared Typesafe alert and Jev fields, built once by the page so the
	// primary sheet and the embedding sheet's fallback section stay identical.
	jevSettings: ReactNode;
	canSave: boolean;
	isSaving: boolean;
	onSave: () => void;
	submitError: string | null;
}

// JevSettingsSheet holds Jev's set-once request settings when Jev is the
// primary classifier, mirroring the embedding sheet so the page itself is left
// for the tier guidance operators actually iterate on.
export default function JevSettingsSheet({ open, onOpenChange, jevSettings, canSave, isSaving, onSave, submitError }: Props) {
	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="flex flex-col p-0" data-testid="complexity-router-jev-sheet">
				<SheetHeader className="flex flex-col items-start gap-1 py-4" headerClassName="bg-card z-10 mb-0 border-b px-4 md:px-6">
					<SheetTitle>Jev settings</SheetTitle>
					<SheetDescription>
						How much conversation Jev sees and how long it may take. Credentials come from your Typesafe provider.
					</SheetDescription>
				</SheetHeader>

				<div className="custom-scrollbar min-h-0 flex-1 space-y-5 overflow-y-auto px-6 py-5">
					{jevSettings}
					{submitError && (
						<div
							role="alert"
							className="border-destructive/40 bg-destructive/10 text-destructive rounded-sm border px-3 py-2 font-mono text-sm"
						>
							{submitError}
						</div>
					)}
				</div>

				<SheetFooter className="bg-card flex-row items-center justify-end gap-2 border-t px-6 py-4">
					<Button
						type="button"
						variant="outline"
						size="sm"
						onClick={() => onOpenChange(false)}
						data-testid="complexity-router-jev-sheet-close-button"
					>
						Close
					</Button>
					<Button
						type="button"
						size="sm"
						onClick={onSave}
						disabled={!canSave || isSaving}
						data-testid="complexity-router-jev-sheet-save-button"
					>
						{isSaving ? <LoaderCircle className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />}
						{isSaving ? "Saving…" : "Save changes"}
					</Button>
				</SheetFooter>
			</SheetContent>
		</Sheet>
	);
}