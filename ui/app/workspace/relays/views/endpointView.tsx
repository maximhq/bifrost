import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import type { Relay } from "@/lib/types/relays";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { FlaskConical, SettingsIcon, Trash } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { HeaderActionButton } from "../fragments/headerActionButton";
import { UsagePreviewPanel } from "../fragments/usagePreviewPanel";
import { EndpointConfigSheet } from "../sheets/endpointConfigSheet";
import { EndpointRoutesTable } from "./endpointRoutesTable";

/** One endpoint's routes, with the same header actions (delete, edit config) a provider shows above its keys. */
export function EndpointView({ endpoint, onRequestDelete }: { endpoint: Relay; onRequestDelete: () => void }) {
	const { t } = useTranslation("config");
	const [showConfigSheet, setShowConfigSheet] = useState(false);
	const [showTestSheet, setShowTestSheet] = useState(false);
	const canDelete = useRbac(RbacResource.Relays, RbacOperation.Delete);
	const canUpdate = useRbac(RbacResource.Relays, RbacOperation.Update);

	const headerActions = (
		<div className="flex min-w-0 flex-wrap items-center gap-2">
			{canDelete && (
				<Button
					variant="outline"
					onClick={onRequestDelete}
					className="text-destructive hover:bg-destructive/10 hover:text-destructive size-9 px-0"
					aria-label={t("relays.view.deleteAria")}
					data-testid="relay-delete"
				>
					<Trash className="h-4 w-4" />
				</Button>
			)}
			<HeaderActionButton
				variant="outline"
				icon={<FlaskConical className="h-4 w-4" />}
				label={t("relays.view.testPricing")}
				onClick={() => setShowTestSheet(true)}
				aria-label={t("relays.view.testPricingAria")}
				data-testid="relay-preview-toggle"
			/>
			<HeaderActionButton
				variant="outline"
				icon={<SettingsIcon className="h-4 w-4" />}
				label={canUpdate ? t("relays.view.editConfig") : t("relays.view.viewConfig")}
				onClick={() => setShowConfigSheet(true)}
				aria-label={canUpdate ? t("relays.view.editConfigAria") : t("relays.view.viewConfigAria")}
				data-testid="relay-edit-config"
			/>
		</div>
	);

	return (
		<div className="@container/card-header flex w-full flex-col gap-6" data-testid="relay-view">
			<EndpointConfigSheet show={showConfigSheet} onCancel={() => setShowConfigSheet(false)} endpoint={endpoint} />
			<Sheet open={showTestSheet} onOpenChange={setShowTestSheet}>
				<SheetContent className="p-0 pt-4 sm:max-w-[50%]" data-testid="relay-preview-sheet">
					<SheetHeader className="flex flex-col items-start py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10 px-4 md:px-8">
						<SheetTitle>{t("relays.view.testSheetTitle")}</SheetTitle>
						<SheetDescription>
							{t("relays.view.testSheetDescription", { name: endpoint.name })}
						</SheetDescription>
					</SheetHeader>
					<div className="flex grow flex-col gap-6 pt-4">
						<UsagePreviewPanel endpoint={endpoint} />
					</div>
				</SheetContent>
			</Sheet>
			<EndpointRoutesTable endpoint={endpoint} headerActions={headerActions} />
			{/* Enterprise reverse lookup of the access profiles granting this relay; OSS renders nothing. */}
		</div>
	);
}