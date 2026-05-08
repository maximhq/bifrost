import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { getErrorMessage, useGetCoreConfigQuery, useUpdateCoreConfigMutation } from "@/lib/store";
import { CoreConfig, DefaultCoreConfig } from "@/lib/types/config";
import { parseArrayFromText } from "@/lib/utils/array";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

export default function LoggingView() {
	const { t } = useTranslation();
	const hasSettingsUpdateAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	const { data: bifrostConfig } = useGetCoreConfigQuery({ fromDB: true });
	const config = bifrostConfig?.client_config;
	const [updateCoreConfig, { isLoading }] = useUpdateCoreConfigMutation();
	const [localConfig, setLocalConfig] = useState<CoreConfig>(DefaultCoreConfig);
	const [needsRestart, setNeedsRestart] = useState<boolean>(false);
	const [loggingHeadersText, setLoggingHeadersText] = useState<string>("");

	useEffect(() => {
		if (config) {
			setLocalConfig(config);
			setLoggingHeadersText(config.logging_headers?.join(", ") || "");
		}
	}, [config]);

	const hasChanges = useMemo(() => {
		if (!config) return false;
		return (
			localConfig.enable_logging !== config.enable_logging ||
			localConfig.disable_content_logging !== config.disable_content_logging ||
			localConfig.allow_per_request_content_storage_override !== config.allow_per_request_content_storage_override ||
			localConfig.allow_per_request_raw_override !== config.allow_per_request_raw_override ||
			localConfig.log_retention_days !== config.log_retention_days ||
			localConfig.hide_deleted_virtual_keys_in_filters !== config.hide_deleted_virtual_keys_in_filters ||
			JSON.stringify(localConfig.logging_headers || []) !== JSON.stringify(config.logging_headers || [])
		);
	}, [config, localConfig]);

	const handleConfigChange = useCallback((field: keyof CoreConfig, value: boolean | number | string[]) => {
		setLocalConfig((prev) => ({ ...prev, [field]: value }));
		// Only enable_logging requires a restart (logging plugin is registered/skipped at startup).
		// disable_content_logging is read live via pointer by the logging plugin and applies on the next request.
		if (field === "enable_logging") {
			setNeedsRestart(true);
		}
	}, []);

	const handleLoggingHeadersChange = useCallback((value: string) => {
		setLoggingHeadersText(value);
		setLocalConfig((prev) => ({ ...prev, logging_headers: parseArrayFromText(value) }));
	}, []);

	const handleSave = useCallback(async () => {
		if (!bifrostConfig) {
			toast.error(t("workspace.config.configNotLoaded"));
			return;
		}

		// Validate log retention days
		if (localConfig.log_retention_days < 1) {
			toast.error(t("workspace.config.logging.retentionMinError"));
			return;
		}

		try {
			await updateCoreConfig({ ...bifrostConfig, client_config: localConfig }).unwrap();
			toast.success(t("workspace.config.logging.configurationUpdated"));
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	}, [bifrostConfig, localConfig, t, updateCoreConfig]);

	return (
		<div className="mx-auto w-full max-w-4xl space-y-4">
			<div>
				<h2 className="text-lg font-semibold tracking-tight">{t("workspace.config.logging.title")}</h2>
				<p className="text-muted-foreground text-sm">{t("workspace.config.logging.description")}</p>
			</div>

			<div className="space-y-4">
				{/* Enable Logs */}
				<div>
					<div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
						<div className="space-y-0.5">
							<label htmlFor="enable-logging" className="text-sm font-medium">
								{t("workspace.config.logging.enableLogs")}
							</label>
							<p className="text-muted-foreground text-sm">
								{t("workspace.config.logging.enableLogsDesc")}
								{!bifrostConfig?.is_logs_connected && (
									<span className="text-destructive font-medium"> {t("workspace.config.logging.logsStoreRequired")}</span>
								)}
							</p>
						</div>
						<Switch
							id="enable-logging"
							size="md"
							checked={localConfig.enable_logging && bifrostConfig?.is_logs_connected}
							disabled={!bifrostConfig?.is_logs_connected}
							onCheckedChange={(checked) => {
								if (bifrostConfig?.is_logs_connected) {
									handleConfigChange("enable_logging", checked);
								}
							}}
						/>
					</div>
					{needsRestart && <RestartWarning />}
				</div>

				{/* Disable Content Logging - Only show when logging is enabled */}
				{localConfig.enable_logging && bifrostConfig?.is_logs_connected && (
					<div>
						<div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
							<div className="space-y-0.5">
								<label htmlFor="disable-content-logging" className="text-sm font-medium">
									{t("workspace.config.logging.disableContentLogging")}
								</label>
								<p className="text-muted-foreground text-sm">{t("workspace.config.logging.disableContentLoggingDesc")}</p>
							</div>
							<Switch
								id="disable-content-logging"
								size="md"
								checked={localConfig.disable_content_logging}
								onCheckedChange={(checked) => handleConfigChange("disable_content_logging", checked)}
							/>
						</div>
					</div>
				)}

				{/* Allow Per-Request Content Storage Override - Only show when logging is enabled */}
				{localConfig.enable_logging && bifrostConfig?.is_logs_connected && (
					<div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
						<div className="space-y-0.5">
							<label htmlFor="allow-per-request-content-storage-override" className="text-sm font-medium">
								{t("workspace.config.logging.perRequestContentOverride")}
							</label>
							<p className="text-muted-foreground text-sm">{t("workspace.config.logging.perRequestContentOverrideDesc")}</p>
						</div>
						<Switch
							id="allow-per-request-content-storage-override"
							data-testid="workspace-content-storage-override-switch"
							size="md"
							checked={localConfig.allow_per_request_content_storage_override}
							onCheckedChange={(checked) => handleConfigChange("allow_per_request_content_storage_override", checked)}
						/>
					</div>
				)}

				{/* Allow Per-Request Raw Override */}
				<div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
					<div className="space-y-0.5">
						<label htmlFor="allow-per-request-raw-override" className="text-sm font-medium">
							{t("workspace.config.logging.perRequestRawOverride")}
						</label>
						<p className="text-muted-foreground text-sm">{t("workspace.config.logging.perRequestRawOverrideDesc")}</p>
					</div>
					<Switch
						id="allow-per-request-raw-override"
						data-testid="workspace-raw-override-switch"
						size="md"
						checked={localConfig.allow_per_request_raw_override}
						onCheckedChange={(checked) => handleConfigChange("allow_per_request_raw_override", checked)}
					/>
				</div>

				{/* Log Retention Days */}
				{localConfig.enable_logging && bifrostConfig?.is_logs_connected && (
					<div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
						<div className="space-y-0.5">
							<Label htmlFor="log-retention-days" className="text-sm font-medium">
								{t("workspace.config.logging.retentionDays")}
							</Label>
							<p className="text-muted-foreground text-sm">{t("workspace.config.logging.retentionDaysDesc")}</p>
						</div>
						<Input
							id="log-retention-days"
							type="number"
							min="1"
							value={localConfig.log_retention_days}
							onChange={(e) => {
								const value = parseInt(e.target.value) || 1;
								handleConfigChange("log_retention_days", Math.max(1, value));
							}}
							className="w-24"
						/>
					</div>
				)}

				<div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
					<div className="space-y-0.5">
						<label htmlFor="hide-deleted-virtual-keys-in-filters" className="text-sm font-medium">
							{t("workspace.config.logging.hideDeletedVirtualKeys")}
						</label>
						<p className="text-muted-foreground text-sm">{t("workspace.config.logging.hideDeletedVirtualKeysDesc")}</p>
					</div>
					<Switch
						id="hide-deleted-virtual-keys-in-filters"
						data-testid="hide-deleted-virtual-keys-in-filters-switch"
						size="md"
						checked={localConfig.hide_deleted_virtual_keys_in_filters}
						onCheckedChange={(checked) => handleConfigChange("hide_deleted_virtual_keys_in_filters", checked)}
					/>
				</div>

				{/* Logging Headers */}
				{localConfig.enable_logging && bifrostConfig?.is_logs_connected && (
					<div className="space-y-2 rounded-lg border p-4">
						<label htmlFor="logging-headers" className="text-sm font-medium">
							{t("workspace.config.logging.loggingHeaders")}
						</label>
						<p className="text-muted-foreground text-sm">{t("workspace.config.logging.loggingHeadersDesc")}</p>
						<Textarea
							id="logging-headers"
							data-testid="workspace-logging-headers-textarea"
							className="h-24"
							placeholder={t("workspace.config.logging.loggingHeadersPlaceholder")}
							value={loggingHeadersText}
							onChange={(e) => handleLoggingHeadersChange(e.target.value)}
						/>
					</div>
				)}
			</div>

			<div className="flex justify-end pt-2">
				<Button onClick={handleSave} disabled={!hasChanges || isLoading || !hasSettingsUpdateAccess}>
					{isLoading ? t("common.saving") : t("workspace.config.saveChanges")}
				</Button>
			</div>
		</div>
	);
}

const RestartWarning = () => {
	const { t } = useTranslation();
	return <div className="text-muted-foreground mt-2 pl-4 text-xs font-semibold">{t("workspace.config.logging.restartHint")}</div>;
};