import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Switch } from "@/components/ui/switch";
import { getErrorMessage, useGetCoreConfigQuery, useUpdateCoreConfigMutation } from "@/lib/store";
import {
	isAuthorizationError,
	readWarpBifrostMCPNoticeAcknowledged,
	shouldShowWarpBifrostMCPNotice,
	warpBifrostMCPSettings,
	warpManagementMode,
	writeWarpBifrostMCPNoticeAcknowledged,
} from "@/lib/utils/warpBifrostMCPNotice";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Link } from "@tanstack/react-router";
import { AlertTriangle, CheckCircle2, ExternalLink, Loader2 } from "lucide-react";
import { ReactNode, useEffect, useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import { toast } from "sonner";

const UPGRADE_DOCS_URL = "https://docs.getbifrost.ai/features/warp#upgrading-from-read-only-warp";

interface Props {
	/** Warp's saved enabled flag: the notice is for deployments that already run Warp. */
	warpEnabled: boolean;
}

/**
 * One-time notice for admins of a deployment that already runs Warp: Warp now
 * reaches the gateway's API through the Bifrost MCP, reading configuration and
 * creating things after the person confirms. It shows the settings that decide
 * that, and lets the admin turn creates off before anyone uses them.
 */
export default function WarpBifrostMCPNoticeDialog({ warpEnabled }: Props) {
	const { t } = useTranslation("config");
	const { t: tCommon } = useTranslation("common");
	const canUpdateWarp = useRbac(RbacResource.Warp, RbacOperation.Update);
	const canUpdateSettings = useRbac(RbacResource.Settings, RbacOperation.Update);
	const [acknowledged, setAcknowledged] = useState<boolean | null>(null);
	const [dismissed, setDismissed] = useState(false);
	const show = acknowledged !== null && !dismissed && shouldShowWarpBifrostMCPNotice({ warpEnabled, canUpdateWarp, acknowledged });

	// Read in an effect, not during render, so the first paint never depends on storage.
	useEffect(() => {
		setAcknowledged(readWarpBifrostMCPNoticeAcknowledged());
	}, []);

	const { data: bifrostConfig, isLoading, isError, error, refetch } = useGetCoreConfigQuery({ fromDB: true }, { skip: !show });
	const [updateCoreConfig, { isLoading: isSaving }] = useUpdateCoreConfigMutation();
	const settings = warpBifrostMCPSettings(bifrostConfig?.client_config, bifrostConfig?.framework_config);
	const [allowWrites, setAllowWrites] = useState(settings.allowWrites);
	useEffect(() => {
		setAllowWrites(settings.allowWrites);
	}, [settings.allowWrites]);
	const writesChanged = allowWrites !== settings.allowWrites;
	const mode = warpManagementMode({ ...settings, allowWrites });

	const acknowledge = async () => {
		if (writesChanged && bifrostConfig) {
			try {
				await updateCoreConfig({
					...bifrostConfig,
					client_config: { ...bifrostConfig.client_config, bifrost_mcp_allow_writes: allowWrites },
				}).unwrap();
			} catch (error) {
				toast.error(t("warp.mcpNotice.saveFailed", { error: getErrorMessage(error) }));
				return;
			}
		}
		writeWarpBifrostMCPNoticeAcknowledged();
		setAcknowledged(true);
	};

	return (
		<Dialog open={show} onOpenChange={(open) => !open && setDismissed(true)}>
			<DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl" data-testid="warp-mcp-notice-dialog">
				<DialogHeader>
					<DialogTitle>{t("warp.mcpNotice.title")}</DialogTitle>
					<DialogDescription>{t("warp.mcpNotice.intro")}</DialogDescription>
				</DialogHeader>

				<div className="space-y-4 text-sm">
					<ul className="list-disc space-y-1.5 pl-5" data-testid="warp-mcp-notice-changes">
						<li>{t("warp.mcpNotice.changeReads")}</li>
						<li>{t("warp.mcpNotice.changeCreates")}</li>
						<li>{t("warp.mcpNotice.changePermissions")}</li>
						<li>{t("warp.mcpNotice.changeData")}</li>
					</ul>

					<div className="space-y-2">
						<p className="font-medium">{t("warp.mcpNotice.settingsHeading")}</p>
						{isError && isAuthorizationError(error) ? (
							<p className="text-muted-foreground" data-testid="warp-mcp-notice-settings-error">
								{t("warp.mcpNotice.settingsUnreadable")}
							</p>
						) : isError ? (
							<div className="flex items-center justify-between gap-4" data-testid="warp-mcp-notice-settings-load-error">
								<p className="text-destructive">{t("warp.mcpNotice.settingsLoadFailed", { error: getErrorMessage(error) })}</p>
								<Button variant="outline" size="sm" onClick={() => refetch()} data-testid="warp-mcp-notice-settings-retry">
									{tCommon("loadError.retry")}
								</Button>
							</div>
						) : isLoading || !bifrostConfig ? (
							<p className="text-muted-foreground flex items-center gap-2">
								<Loader2 className="h-4 w-4 animate-spin" /> {t("warp.mcpNotice.loading")}
							</p>
						) : (
							<div className="divide-y rounded-sm border">
								<SettingRow
									testId="warp-mcp-notice-bifrost-url"
									label={t("warp.mcpNotice.bifrostUrl")}
									value={settings.bifrostURL || t("warp.mcpNotice.notSet")}
									ok={!!settings.bifrostURL}
									hint={settings.bifrostURL ? t("warp.mcpNotice.bifrostUrlSet") : t("warp.mcpNotice.bifrostUrlMissing")}
								/>
								<SettingRow
									testId="warp-mcp-notice-openapi-url"
									label={t("warp.mcpNotice.openapiUrl")}
									value={settings.openAPIURL || t("warp.mcpNotice.openapiDefault")}
									ok
									hint={settings.openAPIURL ? undefined : t("warp.mcpNotice.openapiDefaultHint")}
								/>
								<div className="flex items-start justify-between gap-4 p-3">
									<div className="space-y-0.5">
										<label htmlFor="warp-mcp-notice-allow-writes" className="font-medium">
											{t("warp.mcpNotice.allowCreates")}
										</label>
										<p className="text-muted-foreground text-xs">
											<Trans t={t} i18nKey="warp.mcpNotice.allowCreatesHelp" components={{ code0: <code className="text-xs" /> }} />
										</p>
										{!canUpdateSettings && <p className="text-muted-foreground text-xs">{t("warp.mcpNotice.needSettingsAccess")}</p>}
									</div>
									<Switch
										id="warp-mcp-notice-allow-writes"
										data-testid="warp-mcp-notice-allow-writes"
										checked={allowWrites}
										disabled={!canUpdateSettings || isSaving}
										onCheckedChange={setAllowWrites}
									/>
								</div>
							</div>
						)}
					</div>

					{bifrostConfig && (
						<p className="bg-muted rounded-sm p-3" data-testid="warp-mcp-notice-outcome" data-mode={mode}>
							{t(`warp.mcpNotice.outcome.${mode}`)}
						</p>
					)}

					<a
						href={UPGRADE_DOCS_URL}
						target="_blank"
						rel="noopener noreferrer"
						className="text-primary inline-flex items-center gap-1 text-sm underline-offset-4 hover:underline"
					>
						{t("warp.mcpNotice.docsLink")} <ExternalLink className="h-3.5 w-3.5" />
					</a>
				</div>

				<DialogFooter className="gap-2 sm:justify-between">
					<Button variant="outline" asChild data-testid="warp-mcp-notice-client-settings">
						<Link to="/workspace/config/client-settings">{t("warp.mcpNotice.openClientSettings")}</Link>
					</Button>
					<div className="flex gap-2">
						<Button variant="ghost" onClick={() => setDismissed(true)} disabled={isSaving} data-testid="warp-mcp-notice-later">
							{t("warp.mcpNotice.later")}
						</Button>
						<Button onClick={acknowledge} disabled={isSaving || (!bifrostConfig && !isError)} data-testid="warp-mcp-notice-acknowledge">
							{isSaving && <Loader2 className="h-4 w-4 animate-spin" />}
							{writesChanged ? t("warp.mcpNotice.saveAndClose") : t("warp.mcpNotice.gotIt")}
						</Button>
					</div>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}

function SettingRow({ testId, label, value, ok, hint }: { testId: string; label: string; value: ReactNode; ok: boolean; hint?: string }) {
	return (
		<div className="space-y-1 p-3" data-testid={testId} data-ok={ok}>
			<div className="flex items-center justify-between gap-4">
				<span className="font-medium">{label}</span>
				<span className="text-muted-foreground flex min-w-0 items-center gap-1.5 truncate font-mono text-xs">
					{ok ? (
						<CheckCircle2 className="h-3.5 w-3.5 shrink-0 text-green-600" />
					) : (
						<AlertTriangle className="h-3.5 w-3.5 shrink-0 text-amber-600" />
					)}
					<span className="truncate">{value}</span>
				</span>
			</div>
			{hint && <p className="text-muted-foreground text-xs">{hint}</p>}
		</div>
	);
}