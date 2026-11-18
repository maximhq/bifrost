import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { getErrorMessage, useGetCoreConfigQuery } from "@/lib/store";
import { useGetWarpConfigQuery } from "@/lib/store/apis/warpApi";
import {
	isAuthorizationError,
	readWarpBifrostMCPNoticeAcknowledged,
	readWarpBifrostMCPNoticeLaterCount,
	warpBifrostMCPNoticeMode,
	warpBifrostMCPSettings,
	warpManagementMode,
	writeWarpBifrostMCPNoticeAcknowledged,
	writeWarpBifrostMCPNoticeLaterCount,
	type WarpBifrostMCPNoticeMode,
} from "@/lib/utils/warpBifrostMCPNotice";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Link } from "@tanstack/react-router";
import { AlertTriangle, CheckCircle2, ExternalLink, Info, Loader2 } from "lucide-react";
import { ReactNode, useEffect, useState } from "react";
import { Trans, useTranslation } from "react-i18next";

const UPGRADE_DOCS_URL = "https://docs.getbifrost.ai/features/warp#upgrading-from-read-only-warp";
// Setup needs the settings that load the Bifrost MCP, not the upgrade notes.
const SETUP_DOCS_URL = "https://docs.getbifrost.ai/features/warp#configuration-tools";

export interface WarpBifrostMCPNoticeState {
	mode: WarpBifrostMCPNoticeMode;
	/** Warp does not answer: the notice is about setting the Bifrost MCP up, not about what changed. */
	setup: boolean;
	open: boolean;
	review: () => void;
	/** Closes it as "Remind me later": one reminder towards the banner, unless it was opened from the banner. */
	putOff: () => void;
	/** Closes it for this visit without counting a reminder: the admin acted on it. */
	close: () => void;
	acknowledge: () => void;
}

/**
 * Whether and how an admin sees the Bifrost MCP notice.
 *
 * It opens by itself, and closing it without acknowledging ("Remind me later",
 * Esc, clicking outside) counts one reminder and brings it back on the next visit.
 * After WARP_BIFROST_MCP_NOTICE_MAX_REMINDERS of those it stops opening and stays
 * as a banner, whose button opens it again. Without the Bifrost MCP Warp does not
 * answer whatever the notice does, so there it is a setup step with nothing to
 * acknowledge; the reminder cap still applies.
 *
 * setupOnly hides the informational notice, for surfaces that should only
 * interrupt when Warp cannot answer.
 */
export function useWarpBifrostMCPNotice({
	bifrostMCPEnabled,
	warpEnabled,
	bifrostMCPAvailable,
	setupOnly = false,
}: {
	/** Whether Warp uses the Bifrost MCP at all; off, the notice never shows. */
	bifrostMCPEnabled: boolean;
	warpEnabled: boolean;
	bifrostMCPAvailable: boolean;
	setupOnly?: boolean;
}): WarpBifrostMCPNoticeState {
	const canUpdateWarp = useRbac(RbacResource.Warp, RbacOperation.Update);
	const [acknowledged, setAcknowledged] = useState<boolean | null>(null);
	const [laterCount, setLaterCount] = useState(0);
	const [dismissed, setDismissed] = useState(false);
	const [reviewing, setReviewing] = useState(false);

	// Read in an effect, not during render, so the first paint never depends on storage.
	useEffect(() => {
		setLaterCount(readWarpBifrostMCPNoticeLaterCount());
		setAcknowledged(readWarpBifrostMCPNoticeAcknowledged());
	}, []);

	const setup = bifrostMCPEnabled && !bifrostMCPAvailable;
	const mode =
		acknowledged === null || (setupOnly && !setup)
			? "hidden"
			: warpBifrostMCPNoticeMode({ bifrostMCPEnabled, warpEnabled, canUpdateWarp, bifrostMCPAvailable, acknowledged, laterCount });
	const open = reviewing || (mode === "dialog" && !dismissed);
	const close = () => {
		setReviewing(false);
		setDismissed(true);
	};

	return {
		mode,
		setup,
		open,
		review: () => setReviewing(true),
		putOff: () => {
			if (reviewing) {
				setReviewing(false);
				return;
			}
			setDismissed(true);
			const count = laterCount + 1;
			writeWarpBifrostMCPNoticeLaterCount(count);
			setLaterCount(count);
		},
		close,
		acknowledge: () => {
			writeWarpBifrostMCPNoticeAcknowledged();
			setAcknowledged(true);
			close();
		},
	};
}

/** The page banner: Warp is off until set up, or the notice was put off too often to keep opening. */
export function WarpBifrostMCPNoticeBanner({ setup, onReview }: { setup: boolean; onReview: () => void }) {
	const { t } = useTranslation("config");
	return (
		<Alert variant={setup ? "warning" : "info"} data-testid="warp-mcp-notice-banner" data-setup={setup}>
			{setup ? <AlertTriangle className="h-4 w-4" /> : <Info className="h-4 w-4" />}
			<AlertTitle>{setup ? t("warp.mcpNotice.setupTitle") : t("warp.mcpNotice.title")}</AlertTitle>
			<AlertDescription className="flex flex-wrap items-center justify-between gap-3">
				<span>{setup ? t("warp.mcpNotice.bannerSetupDescription") : t("warp.mcpNotice.bannerDescription")}</span>
				<Button variant="outline" size="sm" onClick={onReview} data-testid="warp-mcp-notice-banner-review">
					{setup ? t("warp.mcpNotice.setUp") : t("warp.mcpNotice.review")}
				</Button>
			</AlertDescription>
		</Alert>
	);
}

/**
 * The notice for admins of a deployment that runs Warp: Warp reaches the
 * gateway's API through the Bifrost MCP, reading configuration and creating
 * the changes an admin allows after the person confirms, and does not answer
 * without it. It shows the settings that decide that, and links to Warp's
 * settings, where the kinds of change are chosen (reads only by default).
 */
export function WarpBifrostMCPNoticeDialog({ notice }: { notice: WarpBifrostMCPNoticeState }) {
	const { t } = useTranslation("config");
	const { t: tCommon } = useTranslation("common");
	const setup = notice.setup;

	const { data: bifrostConfig, isLoading, isError, error, refetch } = useGetCoreConfigQuery({ fromDB: true }, { skip: !notice.open });
	// Which kinds of change Warp may make is Warp's own setting; the notice shows it and links there.
	const { data: warpConfig } = useGetWarpConfigQuery(undefined, { skip: !notice.open });
	const writes = warpConfig?.management_writes ?? [];
	const settings = warpBifrostMCPSettings(bifrostConfig?.client_config, bifrostConfig?.framework_config);
	const mode = warpManagementMode(settings, writes);
	// bifrost_url is set, yet the gateway has no Bifrost MCP: its spec did not load.
	const notLoaded = setup && mode !== "off";
	// Kinds are chosen, but the Bifrost MCP serves no writes (Security's switch is the ceiling).
	const writesCeiling = writes.length > 0 && !settings.allowWrites;
	const changesValue =
		writes.length > 0
			? writes.map((kind) => t(`warp.managementTools.kinds.${kind}.label`)).join(", ")
			: t("warp.mcpNotice.changesReadsOnly");

	return (
		<Dialog open={notice.open} onOpenChange={(open) => !open && notice.putOff()}>
			<DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl" data-testid="warp-mcp-notice-dialog" data-setup={setup}>
				<DialogHeader>
					<DialogTitle>{setup ? t("warp.mcpNotice.setupTitle") : t("warp.mcpNotice.title")}</DialogTitle>
					<DialogDescription>{setup ? t("warp.mcpNotice.setupIntro") : t("warp.mcpNotice.intro")}</DialogDescription>
				</DialogHeader>

				<div className="space-y-4 text-sm">
					<ul className="list-disc space-y-1.5 pl-5" data-testid="warp-mcp-notice-changes">
						<li>{t("warp.mcpNotice.changeReads")}</li>
						<li>{t("warp.mcpNotice.changeWrites")}</li>
						<li>{t("warp.mcpNotice.changeAccess")}</li>
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
									ok={!notLoaded}
									hint={settings.openAPIURL ? undefined : t("warp.mcpNotice.openapiDefaultHint")}
								/>
								<div
									className="space-y-1 p-3"
									data-testid="warp-mcp-notice-changes-row"
									data-writes={writes.join(",")}
									data-ok={!writesCeiling}
								>
									<div className="flex items-center justify-between gap-4">
										<span className="font-medium">{t("warp.mcpNotice.changes")}</span>
										<span className="text-muted-foreground flex min-w-0 items-center gap-1.5 truncate text-xs">
											{writesCeiling ? (
												<AlertTriangle className="h-3.5 w-3.5 shrink-0 text-amber-600" />
											) : (
												<CheckCircle2 className="h-3.5 w-3.5 shrink-0 text-green-600" />
											)}
											<span className="truncate">{changesValue}</span>
										</span>
									</div>
									<p className="text-muted-foreground text-xs">
										<Trans
											t={t}
											i18nKey={writesCeiling ? "warp.mcpNotice.changesCeiling" : "warp.mcpNotice.changesHint"}
											components={{
												link0: (
													<Link
														to="/workspace/config/warp"
														onClick={notice.close}
														className="text-primary underline-offset-4 hover:underline"
														data-testid="warp-mcp-notice-warp-settings-link"
													/>
												),
											}}
										/>
									</p>
								</div>
							</div>
						)}
					</div>

					{bifrostConfig && (
						<p
							className={
								notLoaded ? "rounded-sm bg-amber-50 p-3 text-amber-900 dark:bg-amber-950/40 dark:text-amber-100" : "bg-muted rounded-sm p-3"
							}
							data-testid="warp-mcp-notice-outcome"
							data-mode={notLoaded ? "not_loaded" : mode}
						>
							{notLoaded ? t("warp.mcpNotice.notLoaded") : t(`warp.mcpNotice.outcome.${mode}`)}
						</p>
					)}

					<a
						href={setup ? SETUP_DOCS_URL : UPGRADE_DOCS_URL}
						data-testid="warp-mcp-notice-docs-link"
						target="_blank"
						rel="noopener noreferrer"
						className="text-primary inline-flex items-center gap-1 text-sm underline-offset-4 hover:underline"
					>
						{setup ? t("warp.mcpNotice.docsSetupLink") : t("warp.mcpNotice.docsLink")} <ExternalLink className="h-3.5 w-3.5" />
					</a>
				</div>

				<DialogFooter className="gap-2 sm:justify-between">
					<Button variant={setup ? "default" : "outline"} asChild data-testid="warp-mcp-notice-client-settings">
						{/* Inside the panel the dialog outlives the navigation, so it closes itself; going to fix it is not a reminder. */}
						<Link to="/workspace/config/client-settings" onClick={setup ? notice.close : undefined}>
							{t("warp.mcpNotice.openClientSettings")}
						</Link>
					</Button>
					<div className="flex gap-2">
						<Button variant="ghost" onClick={notice.putOff} data-testid="warp-mcp-notice-later">
							{t("warp.mcpNotice.later")}
						</Button>
						{/* Setup has nothing to acknowledge while Warp does not answer. */}
						{!setup && (
							<Button onClick={notice.acknowledge} disabled={!bifrostConfig && !isError} data-testid="warp-mcp-notice-acknowledge">
								{t("warp.mcpNotice.gotIt")}
							</Button>
						)}
					</div>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}

/** The settings page's notice: the banner in the page flow and the dialog over it. */
export default function WarpBifrostMCPNotice({
	bifrostMCPEnabled,
	warpEnabled,
	bifrostMCPAvailable,
}: {
	bifrostMCPEnabled: boolean;
	warpEnabled: boolean;
	bifrostMCPAvailable: boolean;
}) {
	const notice = useWarpBifrostMCPNotice({ bifrostMCPEnabled, warpEnabled, bifrostMCPAvailable });
	return (
		<>
			{notice.mode === "banner" && <WarpBifrostMCPNoticeBanner setup={notice.setup} onReview={notice.review} />}
			<WarpBifrostMCPNoticeDialog notice={notice} />
		</>
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