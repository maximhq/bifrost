import PageTitle from "@/components/pageTitle";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useT } from "@/lib/i18n";
import { getErrorMessage } from "@/lib/store";
import { useListFeatureFlagsQuery, useUpdateFeatureFlagMutation } from "@/lib/store/apis/featureFlagsApi";
import type { FeatureFlagStatus } from "@/lib/types/featureFlag";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Crown, Lock } from "lucide-react";
import { toast } from "sonner";

export default function FeatureFlagsView() {
	const t = useT();
	const hasUpdateAccess = useRbac(RbacResource.FeatureFlags, RbacOperation.Update);
	const { data, isLoading, isError, error } = useListFeatureFlagsQuery();
	const [updateFeatureFlag] = useUpdateFeatureFlagMutation();

	const flags = data?.flags ?? [];

	async function handleToggle(flag: FeatureFlagStatus, checked: boolean) {
		try {
			await updateFeatureFlag({ id: flag.id, enabled: checked }).unwrap();
			toast.success(
				t(checked ? "configForms.featureFlags.enabledToast" : "configForms.featureFlags.disabledToast", {
					name: flag.display_name || flag.id,
				}),
			);
		} catch (err) {
			toast.error(getErrorMessage(err));
		}
	}

	return (
		<div className="w-full space-y-4">
			<PageTitle title="Feature Flags">{t("configForms.featureFlags.pageDescription")}</PageTitle>

			{isLoading && <p className="text-muted-foreground text-sm">{t("configForms.featureFlags.loading")}</p>}
			{isError && <p className="text-sm text-red-500">{t("configForms.featureFlags.loadFailed", { error: getErrorMessage(error) })}</p>}

			{!isLoading && !isError && (
				<div className="overflow-auto rounded-sm border">
					<Table data-testid="feature-flags-table">
						<TableHeader>
							<TableRow className="bg-muted/50">
								<TableHead className="font-semibold">{t("configForms.featureFlags.flag")}</TableHead>
								<TableHead className="w-px text-right font-semibold">{t("common.enabled")}</TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{flags.length === 0 ? (
								<TableRow data-testid="feature-flags-table-empty-state">
									<TableCell colSpan={2} className="h-24 text-center">
										<span className="text-muted-foreground text-sm">{t("configForms.featureFlags.empty")}</span>
									</TableCell>
								</TableRow>
							) : (
								flags.map((flag) => <FeatureFlagRow key={flag.id} flag={flag} canUpdate={hasUpdateAccess} onToggle={handleToggle} />)
							)}
						</TableBody>
					</Table>
				</div>
			)}
		</div>
	);
}

interface FeatureFlagRowProps {
	flag: FeatureFlagStatus;
	canUpdate: boolean;
	onToggle: (flag: FeatureFlagStatus, checked: boolean) => Promise<void>;
}

function FeatureFlagRow({ flag, canUpdate, onToggle }: FeatureFlagRowProps) {
	const t = useT();
	const disabled = flag.locked || !flag.registered || !canUpdate;
	// Fall back to id when display_name is empty so unregistered orphans
	// still render something readable in the primary slot.
	const primaryLabel = flag.display_name || flag.id;

	return (
		<TableRow className="group hover:bg-muted/50 transition-colors">
			<TableCell className="align-top">
				<div className="flex flex-col gap-1">
					<div className="flex flex-wrap items-center gap-2">
						<span className="text-sm font-medium">{primaryLabel}</span>
						{flag.display_name && <span className="text-muted-foreground font-mono text-xs">{flag.id}</span>}
						<SourceBadge source={flag.source} />
						{flag.enterprise_only && <EnterpriseBadge />}
						{flag.locked && !flag.enterprise_only && <LockedBadge />}
						{!flag.registered && <UnregisteredBadge />}
					</div>
					{flag.description && <p className="text-muted-foreground text-sm">{flag.description}</p>}
					{!flag.registered && <p className="text-muted-foreground text-xs">{t("configForms.featureFlags.unregisteredDescription")}</p>}
				</div>
			</TableCell>
			<TableCell className="w-px text-right align-top">
				<Switch
					data-testid={`feature-flag-toggle-${flag.id}`}
					size="md"
					checked={flag.enabled}
					disabled={disabled}
					onAsyncCheckedChange={(checked) => onToggle(flag, checked)}
				/>
			</TableCell>
		</TableRow>
	);
}

function SourceBadge({ source }: { source: FeatureFlagStatus["source"] }) {
	return (
		<Badge variant="outline" className="text-xs capitalize">
			{source}
		</Badge>
	);
}

function LockedBadge() {
	const t = useT();
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<Badge variant="secondary" className="flex items-center gap-1 text-xs">
					<Lock className="size-3" />
					{t("configForms.featureFlags.locked")}
				</Badge>
			</TooltipTrigger>
			<TooltipContent>{t("configForms.featureFlags.lockedHint")}</TooltipContent>
		</Tooltip>
	);
}

function EnterpriseBadge() {
	const t = useT();
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<Badge variant="secondary" className="flex items-center gap-1 text-xs">
					<Crown className="size-3" />
					{t("configForms.enterprise")}
				</Badge>
			</TooltipTrigger>
			<TooltipContent>{t("configForms.featureFlags.enterpriseHint")}</TooltipContent>
		</Tooltip>
	);
}

function UnregisteredBadge() {
	const t = useT();
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<Badge variant="destructive" className="text-xs">
					{t("configForms.featureFlags.unregistered")}
				</Badge>
			</TooltipTrigger>
			<TooltipContent>{t("configForms.featureFlags.unregisteredHint")}</TooltipContent>
		</Tooltip>
	);
}