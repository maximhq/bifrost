import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { relayAuthTypeLabel, type Relay } from "@/lib/types/relays";
import { cleanUsage, describePricing, relayGatewayPath } from "@/lib/utils/relayForm";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { CheckIcon, CopyIcon, EllipsisIcon, FlaskConical, SettingsIcon, Trash } from "lucide-react";
import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { HeaderActionButton } from "../fragments/headerActionButton";
import { UsagePreviewPanel } from "../fragments/usagePreviewPanel";
import { EndpointConfigSheet } from "../sheets/endpointConfigSheet";
import { EndpointRoutesTable } from "./endpointRoutesTable";

function DetailItem({ label, children, testId }: { label: string; children: ReactNode; testId?: string }) {
	return (
		<div className="flex min-w-0 flex-col gap-1" data-testid={testId}>
			<dt className="text-muted-foreground text-xs font-medium">{label}</dt>
			<dd className="flex h-6 min-w-0 items-center gap-1.5 text-sm">{children}</dd>
		</div>
	);
}

/** One relay: a header naming it with its gateway path, upstream, auth and default pricing, then its routes. */
export function EndpointView({ endpoint, onRequestDelete }: { endpoint: Relay; onRequestDelete: () => void }) {
	const { t } = useTranslation("config");
	const [showConfigSheet, setShowConfigSheet] = useState(false);
	const [showTestSheet, setShowTestSheet] = useState(false);
	const canDelete = useRbac(RbacResource.Relays, RbacOperation.Delete);
	const canUpdate = useRbac(RbacResource.Relays, RbacOperation.Update);
	const { copy, copied } = useCopyToClipboard();
	const gatewayPath = relayGatewayPath(endpoint);
	const auth = endpoint.auth;
	const authTarget = auth.type === "header" ? auth.header_name : auth.type === "query" ? auth.query_param : undefined;

	return (
		<div className="flex w-full flex-col gap-6 pb-6 md:min-h-0 md:flex-1 md:pb-0" data-testid="relay-view">
			<EndpointConfigSheet show={showConfigSheet} onCancel={() => setShowConfigSheet(false)} endpoint={endpoint} />
			<Sheet open={showTestSheet} onOpenChange={setShowTestSheet}>
				<SheetContent className="p-0 pt-4 sm:max-w-[50%]" data-testid="relay-preview-sheet">
					<SheetHeader className="flex flex-col items-start py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10 px-4 md:px-8">
						<SheetTitle>{t("relays.view.testSheetTitle")}</SheetTitle>
						<SheetDescription>{t("relays.view.testSheetDescription", { name: endpoint.name })}</SheetDescription>
					</SheetHeader>
					<div className="flex grow flex-col gap-6 pt-4">
						<UsagePreviewPanel endpoint={endpoint} />
					</div>
				</SheetContent>
			</Sheet>

			<section className="@container/card-header flex shrink-0 flex-col gap-5" data-testid="relay-header">
				<div className="flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
					<div className="min-w-0">
						<h2 className="truncate text-lg font-semibold" data-testid="relay-title">
							{endpoint.name}
						</h2>
						{endpoint.description && <p className="text-muted-foreground mt-0.5 text-sm">{endpoint.description}</p>}
					</div>
					<div className="flex shrink-0 items-center gap-2">
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
						{canDelete && (
							<DropdownMenu>
								<DropdownMenuTrigger asChild>
									<Button variant="ghost" className="size-9 px-0" aria-label={t("relays.view.moreActionsAria")} data-testid="relay-actions">
										<EllipsisIcon className="h-4 w-4" aria-hidden="true" />
									</Button>
								</DropdownMenuTrigger>
								<DropdownMenuContent align="end">
									<DropdownMenuItem variant="destructive" onSelect={onRequestDelete} data-testid="relay-delete">
										<Trash className="mr-1 h-4 w-4" />
										{t("relays.view.deleteRelay")}
									</DropdownMenuItem>
								</DropdownMenuContent>
							</DropdownMenu>
						)}
					</div>
				</div>

				<dl className="grid grid-cols-1 gap-x-6 gap-y-4 rounded-md border px-4 py-3 sm:grid-cols-2 xl:grid-cols-4">
					<DetailItem label={t("relays.view.gatewayPath")} testId="relay-detail-gateway">
						<code className="truncate font-mono">{gatewayPath}</code>
						<Button
							variant="ghost"
							size="icon"
							className="size-6 shrink-0"
							aria-label={t("relays.view.copyPathAria")}
							onClick={() => void copy(gatewayPath)}
							data-testid="relay-copy-path"
						>
							{copied ? <CheckIcon className="h-3.5 w-3.5" /> : <CopyIcon className="h-3.5 w-3.5" />}
						</Button>
					</DetailItem>
					<DetailItem label={t("relays.view.upstream")} testId="relay-detail-upstream">
						<code className="truncate font-mono" title={endpoint.base_url}>
							{endpoint.base_url}
						</code>
					</DetailItem>
					<DetailItem label={t("relays.view.auth")} testId="relay-detail-auth">
						<span className="truncate">{relayAuthTypeLabel(auth.type)}</span>
						{authTarget && <code className="text-muted-foreground truncate font-mono text-xs">{authTarget}</code>}
					</DetailItem>
					<DetailItem label={t("relays.view.pricing")} testId="relay-detail-pricing">
						<span className="truncate">{describePricing(cleanUsage(endpoint.usage, false), endpoint.ignore_upstream_cost)}</span>
						{endpoint.skip_route_check && (
							<Badge variant="outline" className="shrink-0">
								{t("relays.view.anyPath")}
							</Badge>
						)}
					</DetailItem>
				</dl>
			</section>

			<EndpointRoutesTable endpoint={endpoint} />
		</div>
	);
}