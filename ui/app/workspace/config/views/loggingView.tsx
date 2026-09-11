import PageTitle from "@/components/pageTitle";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { MultiSelect } from "@/components/ui/multiSelect";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { getErrorMessage, useGetCoreConfigQuery, useUpdateCoreConfigMutation } from "@/lib/store";
import { RequestTypeLabels, RequestTypes } from "@/lib/constants/logs";
import { CoreConfig, DefaultCoreConfig } from "@/lib/types/config";
import { parseArrayFromText } from "@/lib/utils/array";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

const requestTypeLabel = (requestType: string): string =>
	Object.hasOwn(RequestTypeLabels, requestType) ? RequestTypeLabels[requestType as keyof typeof RequestTypeLabels] : requestType;

// Every known request type, plus any stored value the UI does not know about yet
// (for example a type set from config.json on a newer server) so it stays selectable.
const hiddenRequestTypeOptions = (selected: string[]) => {
	const known = new Set<string>(RequestTypes);
	const extra = selected.filter((requestType) => !known.has(requestType));
	return [...RequestTypes, ...extra].map((requestType) => ({ value: requestType, label: requestTypeLabel(requestType) }));
};

// Order-independent equality; the multi-select emits values in selection order.
const sameRequestTypes = (a: string[] | undefined, b: string[] | undefined): boolean => {
	const left = [...(a || [])].sort();
	const right = [...(b || [])].sort();
	return left.length === right.length && left.every((value, index) => value === right[index]);
};

export default function LoggingView() {
	const { t } = useTranslation("config");
	const hasSettingsUpdateAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	const { data: bifrostConfig, isLoading: isConfigLoading, isError: isConfigError } = useGetCoreConfigQuery({ fromDB: true });
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
			localConfig.retain_content_in_object_storage !== config.retain_content_in_object_storage ||
			localConfig.allow_per_request_content_storage_override !== config.allow_per_request_content_storage_override ||
			localConfig.allow_per_request_raw_override !== config.allow_per_request_raw_override ||
			localConfig.log_retention_days !== config.log_retention_days ||
			localConfig.hide_deleted_virtual_keys_in_filters !== config.hide_deleted_virtual_keys_in_filters ||
			JSON.stringify(localConfig.logging_headers || []) !== JSON.stringify(config.logging_headers || []) ||
			!sameRequestTypes(localConfig.hidden_request_types, config.hidden_request_types)
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
			toast.error(t("configNotLoaded"));
			return;
		}

		// Validate log retention days
		if (localConfig.log_retention_days < 1) {
			toast.error(t("logging.toastRetentionMin"));
			return;
		}

		try {
			await updateCoreConfig({ ...bifrostConfig, client_config: localConfig }).unwrap();
			toast.success(t("logging.toastSaved"));
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	}, [bifrostConfig, localConfig, t, updateCoreConfig]);

	return (
		<div className="mx-auto w-full max-w-4xl space-y-4 px-4 py-6 md:px-0">
			<PageTitle title={t("logging.title")}>{t("logging.description")}</PageTitle>

			<div className="space-y-4">
				<div>
					<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
						<div className="space-y-0.5">
							<label htmlFor="enable-logging" className="text-sm font-medium">
								{t("logging.enableLogs")}
							</label>
							<p className="text-muted-foreground text-sm">
								{t("logging.enableLogsHelp")}
								{!bifrostConfig?.is_logs_connected && (
									<span className="text-destructive font-medium"> {t("logging.requiresStore")}</span>
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

				<div className="min-w-0 space-y-3 rounded-sm border p-4" data-testid="workspace-hidden-request-types">
					<div className="space-y-0.5">
						<Label htmlFor="hidden-request-types" className="text-sm font-medium">
							{t("logging.hiddenRequestTypes")}
						</Label>
						<p className="text-muted-foreground text-sm">
							{t("logging.hiddenRequestTypesHelp")}{" "}
							<code className="text-xs">client.hidden_request_types</code> {t("logging.inConfigJson")}
						</p>
					</div>
					{isConfigError ? (
						<p className="text-destructive text-sm" role="alert">
							{t("logging.hiddenRequestTypesLoadError")}
						</p>
					) : (
						<MultiSelect
							id="hidden-request-types"
							data-testid="hidden-request-types-select"
							options={hiddenRequestTypeOptions(localConfig.hidden_request_types || [])}
							defaultValue={localConfig.hidden_request_types || []}
							resetOnDefaultValueChange
							onValueChange={(values) => handleConfigChange("hidden_request_types", values)}
							placeholder={isConfigLoading ? t("logging.loadingConfiguration") : t("logging.allRequestTypesVisible")}
							emptyIndicator={t("logging.noRequestTypes")}
							disabled={!bifrostConfig || !hasSettingsUpdateAccess}
							maxCount={6}
							className="border-input text-foreground hover:bg-accent hover:text-accent-foreground min-h-9 rounded-sm bg-transparent font-normal"
						/>
					)}
				</div>

				{/* Disable Content Logging - Only show when logging is enabled */}
				{localConfig.enable_logging && bifrostConfig?.is_logs_connected && (
					<div>
						<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
							<div className="space-y-0.5">
								<label htmlFor="disable-content-logging" className="text-sm font-medium">
									{t("logging.disableContentLogging")}
								</label>
								<p className="text-muted-foreground text-sm">{t("logging.disableContentLoggingHelp")}</p>
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

				{localConfig.enable_logging && bifrostConfig?.is_logs_connected && (
					<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
						<div className="space-y-0.5">
							<label htmlFor="retain-content-in-object-storage" className="text-sm font-medium">
								{t("logging.retainContent")}
							</label>
							<p className="text-muted-foreground text-sm">
								{t("logging.retainContentHelp")}
								{!bifrostConfig?.is_object_storage_connected && (
									<span className="text-destructive font-medium"> {t("logging.requiresObjectStorage")}</span>
								)}
							</p>
						</div>
						<Switch
							id="retain-content-in-object-storage"
							data-testid="workspace-retain-content-in-object-storage-switch"
							size="md"
							checked={localConfig.retain_content_in_object_storage && bifrostConfig?.is_object_storage_connected === true}
							disabled={!bifrostConfig?.is_object_storage_connected}
							onCheckedChange={(checked) => {
								if (bifrostConfig?.is_object_storage_connected) {
									handleConfigChange("retain_content_in_object_storage", checked);
								}
							}}
						/>
					</div>
				)}

				{localConfig.enable_logging && bifrostConfig?.is_logs_connected && (
					<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
						<div className="space-y-0.5">
							<label htmlFor="allow-per-request-content-storage-override" className="text-sm font-medium">
								{t("logging.allowContentOverride")}
							</label>
							<p className="text-muted-foreground text-sm">
								{t("logging.allowContentOverrideHelp", { setting: t("logging.allowRawOverride") })}
							</p>
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
				<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
					<div className="space-y-0.5">
						<label htmlFor="allow-per-request-raw-override" className="text-sm font-medium">
							{t("logging.allowRawOverride")}
						</label>
						<p className="text-muted-foreground text-sm">
							{t("logging.allowRawOverrideHelp", { setting: t("logging.allowContentOverride") })}
						</p>
					</div>
					<Switch
						id="allow-per-request-raw-override"
						data-testid="workspace-raw-override-switch"
						size="md"
						checked={localConfig.allow_per_request_raw_override}
						onCheckedChange={(checked) => handleConfigChange("allow_per_request_raw_override", checked)}
					/>
				</div>

				{localConfig.enable_logging && bifrostConfig?.is_logs_connected && (
					<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
						<div className="space-y-0.5">
							<Label htmlFor="log-retention-days" className="text-sm font-medium">
								{t("logging.logRetentionDays")}
							</Label>
							<p className="text-muted-foreground text-sm">{t("logging.logRetentionDaysHelp")}</p>
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

				<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
					<div className="space-y-0.5">
						<label htmlFor="hide-deleted-virtual-keys-in-filters" className="text-sm font-medium">
							{t("logging.hideDeletedVks")}
						</label>
						<p className="text-muted-foreground text-sm">{t("logging.hideDeletedVksHelp")}</p>
					</div>
					<Switch
						id="hide-deleted-virtual-keys-in-filters"
						data-testid="hide-deleted-virtual-keys-in-filters-switch"
						size="md"
						checked={localConfig.hide_deleted_virtual_keys_in_filters}
						onCheckedChange={(checked) => handleConfigChange("hide_deleted_virtual_keys_in_filters", checked)}
					/>
				</div>

				{localConfig.enable_logging && bifrostConfig?.is_logs_connected && (
					<div className="space-y-2 rounded-sm border p-4">
						<label htmlFor="logging-headers" className="text-sm font-medium">
							{t("logging.loggingHeaders")}
						</label>
						<p className="text-muted-foreground text-sm">{t("logging.loggingHeadersHelp")}</p>
						<Textarea
							id="logging-headers"
							data-testid="workspace-logging-headers-textarea"
							className="h-24"
							placeholder={t("logging.loggingHeadersPlaceholder")}
							value={loggingHeadersText}
							onChange={(e) => handleLoggingHeadersChange(e.target.value)}
						/>
					</div>
				)}
			</div>

			<div className="flex justify-end pt-2">
				<Button onClick={handleSave} disabled={!hasChanges || isLoading || !hasSettingsUpdateAccess}>
					{isLoading ? t("saving") : t("saveChanges")}
				</Button>
			</div>
		</div>
	);
}

const RestartWarning = () => {
	const { t } = useTranslation("config");
	return <div className="text-muted-foreground mt-2 pl-4 text-xs font-semibold">{t("restartWarning")}</div>;
};