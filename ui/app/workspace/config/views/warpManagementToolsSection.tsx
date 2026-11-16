import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { getLocale } from "@/lib/i18n";
import { WARP_MANAGEMENT_WRITES, type WarpManagementToolSize, type WarpManagementWrite } from "@/lib/types/warp";
import { Link } from "@tanstack/react-router";
import { AlertTriangle, Info } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { setWarpManagementWrite, warpManagementTokens, warpManagementWriteSwitchDisabled } from "./warpConfig.utils";

interface Props {
	writes: WarpManagementWrite[];
	onChange: (writes: WarpManagementWrite[]) => void;
	/** Per kind, what Warp could be offered; absent while the Bifrost MCP is not loaded. */
	sizes?: WarpManagementToolSize[];
	/** Whether the Bifrost MCP serves writes at all (bifrost_mcp_allow_writes, the ceiling). */
	writesAvailable: boolean;
	disabled: boolean;
}

/**
 * Which kinds of configuration change Warp may offer through the Bifrost MCP.
 * Reads are always on; every other kind is off until an admin turns it on here,
 * because each one adds its tools to every Warp request. The sizes say what each
 * kind costs, and the warning says what the current choice sends.
 */
export default function WarpManagementToolsSection({ writes, onChange, sizes, writesAvailable, disabled }: Props) {
	const { t } = useTranslation("config");
	const sizeOf = (kind: WarpManagementToolSize["kind"]) => sizes?.find((size) => size.kind === kind);
	const total = warpManagementTokens(sizes, writes);
	const loaded = !!sizes && sizes.length > 0;
	const formatTokens = (tokens: number) =>
		new Intl.NumberFormat(getLocale(), { notation: "compact", maximumFractionDigits: 0 }).format(tokens);
	const sizeLabel = (size: WarpManagementToolSize | undefined) =>
		size ? t("warp.managementTools.size", { count: size.tools, tokens: formatTokens(size.estimated_tokens) }) : null;

	return (
		<div className="space-y-4" data-testid="warp-management-tools-section">
			<div className="flex items-start justify-between gap-4">
				<div className="space-y-0.5">
					<Label>{t("warp.managementTools.reads")}</Label>
					<p className="text-muted-foreground text-xs">{t("warp.managementTools.readsHelp")}</p>
				</div>
				<div className="flex shrink-0 items-center gap-2">
					{sizeLabel(sizeOf("read")) && <span className="text-muted-foreground text-xs">{sizeLabel(sizeOf("read"))}</span>}
					<Badge variant="secondary">{t("warp.managementTools.alwaysOn")}</Badge>
				</div>
			</div>

			{WARP_MANAGEMENT_WRITES.map((kind) => {
				const size = sizeOf(kind);
				return (
					<div key={kind} className="flex items-start justify-between gap-4">
						<div className="space-y-0.5">
							<Label htmlFor={`warp-management-write-${kind}`}>{t(`warp.managementTools.kinds.${kind}.label`)}</Label>
							<p className="text-muted-foreground text-xs">{t(`warp.managementTools.kinds.${kind}.help`)}</p>
						</div>
						<div className="flex shrink-0 items-center gap-2">
							{sizeLabel(size) && <span className="text-muted-foreground text-xs">{sizeLabel(size)}</span>}
							<Switch
								id={`warp-management-write-${kind}`}
								data-testid={`warp-management-write-${kind}-switch`}
								checked={writes.includes(kind)}
								disabled={warpManagementWriteSwitchDisabled({
									disabled,
									checked: writes.includes(kind),
									writesAvailable,
									loaded,
									offered: !!size,
								})}
								onCheckedChange={(checked) => onChange(setWarpManagementWrite(writes, kind, checked))}
							/>
						</div>
					</div>
				);
			})}

			{!loaded ? (
				<p className="text-muted-foreground flex items-center gap-1.5 text-xs" data-testid="warp-management-tools-not-loaded">
					<Info className="h-3.5 w-3.5 shrink-0" /> {t("warp.managementTools.notLoaded")}
				</p>
			) : !writesAvailable ? (
				<p className="text-muted-foreground flex items-center gap-1.5 text-xs" data-testid="warp-management-writes-ceiling">
					<Info className="h-3.5 w-3.5 shrink-0" />
					<span>
						<Trans
							t={t}
							i18nKey="warp.managementTools.writesOff"
							components={{ link0: <Link to="/workspace/config/security" className="text-primary underline-offset-4 hover:underline" /> }}
						/>
					</span>
				</p>
			) : null}

			{total !== null && (
				<p
					className={
						writes.length > 0 ? "flex items-start gap-1.5 text-xs text-amber-700 dark:text-amber-400" : "text-muted-foreground text-xs"
					}
					data-testid="warp-management-tools-total"
					data-writes={writes.length}
				>
					{writes.length > 0 && <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />}
					<span>
						{writes.length > 0
							? t("warp.managementTools.contextWarning", { tokens: formatTokens(total) })
							: t("warp.managementTools.contextReadsOnly", { tokens: formatTokens(total) })}
					</span>
				</p>
			)}
		</div>
	);
}