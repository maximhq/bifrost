"use client";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { getErrorMessage, useGetCoreConfigQuery, useUpdateCoreConfigMutation } from "@/lib/store";
import { CoreConfig } from "@/lib/types/config";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";

const defaultConfig: CoreConfig = {
	drop_excess_requests: false,
	initial_pool_size: 1000,
	prometheus_labels: [],
	enable_logging: true,
	disable_content_logging: false,
	log_retention_days: 365,
	enable_governance: true,
	enforce_governance_header: false,
	allow_direct_keys: false,
	allowed_origins: [],
	max_request_body_size_mb: 100,
	enable_litellm_fallbacks: false,
};

export default function LoggingView() {
	const hasSettingsUpdateAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	const { data: bifrostConfig } = useGetCoreConfigQuery({ fromDB: true });
	const config = bifrostConfig?.client_config;
	const [updateCoreConfig, { isLoading }] = useUpdateCoreConfigMutation();
	const [localConfig, setLocalConfig] = useState<CoreConfig>(defaultConfig);
	const [needsRestart, setNeedsRestart] = useState<boolean>(false);

	useEffect(() => {
		if (config) {
			setLocalConfig(config);
		}
	}, [config]);

	const hasChanges = useMemo(() => {
		if (!config) return false;
		return (
			localConfig.enable_logging !== config.enable_logging ||
			localConfig.disable_content_logging !== config.disable_content_logging ||
			localConfig.log_retention_days !== config.log_retention_days
		);
	}, [config, localConfig]);

	const handleConfigChange = useCallback((field: keyof CoreConfig, value: boolean | number | string[]) => {
		setLocalConfig((prev) => ({ ...prev, [field]: value }));
		if (field === "enable_logging" || field === "disable_content_logging") {
			setNeedsRestart(true);
		}
	}, []);

	const handleSave = useCallback(async () => {
		if (!bifrostConfig) {
			toast.error("Configuration not loaded");
			return;
		}

		// Validate log retention days
		if (localConfig.log_retention_days < 1) {
			toast.error("Log retention days must be at least 1 day");
			return;
		}

		try {
			await updateCoreConfig({ ...bifrostConfig, client_config: localConfig }).unwrap();
			toast.success("Logging configuration updated successfully.");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	}, [bifrostConfig, localConfig, updateCoreConfig]);

	return (
		<div className="mx-auto w-full max-w-4xl space-y-4">
			<div className="flex items-center justify-between">
				<div>
					<h2 className="text-2xl font-semibold tracking-tight">Logging</h2>
					<p className="text-muted-foreground text-sm">Configure logging settings for requests and responses.</p>
				</div>
				<Button onClick={handleSave} disabled={!hasChanges || isLoading || !hasSettingsUpdateAccess}>
					{isLoading ? "Saving..." : "Save Changes"}
				</Button>
			</div>

			<div className="space-y-4">
				{/* Enable Logs */}
				<div>
					<div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
						<div className="space-y-0.5">
							<label htmlFor="enable-logging" className="text-sm font-medium">
								Enable Logs
							</label>
							<p className="text-muted-foreground text-sm">
								Enable logging of requests and responses to a SQL database. This can add 40-60mb of overhead to the system memory.
								{!bifrostConfig?.is_logs_connected && (
									<span className="text-destructive font-medium"> Requires logs store to be configured and enabled in config.json.</span>
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
									Disable Content Logging
								</label>
								<p className="text-muted-foreground text-sm">
									When enabled, only usage metadata (latency, cost, token count, etc.) will be logged. Request/response content will not be stored.
								</p>
							</div>
							<Switch
								id="disable-content-logging"
								size="md"
								checked={localConfig.disable_content_logging}
								onCheckedChange={(checked) => handleConfigChange("disable_content_logging", checked)}
							/>
						</div>
						{needsRestart && <RestartWarning />}
					</div>
				)}

				{/* Log Retention Days */}
				{localConfig.enable_logging && bifrostConfig?.is_logs_connected && (
					<div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
						<div className="space-y-0.5">
							<Label htmlFor="log-retention-days" className="text-sm font-medium">
								Log Retention Days
							</Label>
							<p className="text-muted-foreground text-sm">
								Number of days to retain logs in the database. Minimum is 1 day. Older logs will be automatically deleted.
							</p>
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
			</div>
		</div>
	);
}

const RestartWarning = () => {
	return <div className="text-muted-foreground mt-2 pl-4 text-xs font-semibold">Need to restart Bifrost to apply changes.</div>;
};


