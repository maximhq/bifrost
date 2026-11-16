import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { LoaderCircle, Save } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

interface Props {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	title: string;
	description: string;
	// The sheet is `${testIdPrefix}-sheet`, its buttons `${testIdPrefix}-sheet-close-button`
	// and `${testIdPrefix}-sheet-save-button`.
	testIdPrefix: string;
	// The classifier's fields, built once by the page so the primary sheet and the
	// embedding sheet's fallback section stay identical.
	children: ReactNode;
	canSave: boolean;
	isSaving: boolean;
	onSave: () => void;
	submitError: string | null;
}

// ClassifierSettingsSheet holds a model-based classifier's set-once settings when it
// is the primary classifier, mirroring the embedding sheet so the page itself is
// left for what operators actually iterate on: the decision model's tier guidance,
// or the llm classifier's prompt.
export default function ClassifierSettingsSheet({
	open,
	onOpenChange,
	title,
	description,
	testIdPrefix,
	children,
	canSave,
	isSaving,
	onSave,
	submitError,
}: Props) {
	const { t } = useTranslation("models");
	const { t: tcpx } = useTranslation("models", { keyPrefix: "routing.complexityUi" });
	const { t: tCommon } = useTranslation("common");
	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="flex flex-col p-0" data-testid={`${testIdPrefix}-sheet`}>
				<SheetHeader className="flex flex-col items-start gap-1 py-4" headerClassName="bg-card z-10 mb-0 border-b px-4 md:px-6">
					<SheetTitle>{title}</SheetTitle>
					<SheetDescription>{description}</SheetDescription>
				</SheetHeader>

				<div className="custom-scrollbar min-h-0 flex-1 space-y-5 overflow-y-auto px-6 py-5">
					{children}
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
						data-testid={`${testIdPrefix}-sheet-close-button`}
					>
						{tCommon("close")}
					</Button>
					<Button
						type="button"
						size="sm"
						onClick={onSave}
						disabled={!canSave || isSaving}
						data-testid={`${testIdPrefix}-sheet-save-button`}
					>
						{isSaving ? <LoaderCircle className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />}
						{isSaving ? t("routing.saving") : tcpx("saveChanges")}
					</Button>
				</SheetFooter>
			</SheetContent>
		</Sheet>
	);
}