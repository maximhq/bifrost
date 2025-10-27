"use client";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { getErrorMessage, useGetCoreConfigQuery, useUpdateCoreConfigMutation } from "@/lib/store";
import { CoreConfig } from "@/lib/types/config";
import { AlertTriangle } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import PluginsForm from "./pluginsForm";

const defaultConfig: CoreConfig = {
	drop_excess_requests: false,
	initial_pool_size: 1000,
	prometheus_labels: [],
	enable_logging: true,
	enable_governance: true,
	enforce_governance_header: false,
	allow_direct_keys: false,
	allowed_origins: [],
	max_request_body_size_mb: 100,
	enable_litellm_fallbacks: false,
};

export default function FeatureTogglesView() {
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
		return localConfig.enable_logging !== config.enable_logging || localConfig.enable_governance !== config.enable_governance;
	}, [config, localConfig]);

	const handleConfigChange = useCallback((field: keyof CoreConfig, value: boolean | number | string[]) => {
		setLocalConfig((prev) => ({ ...prev, [field]: value }));
		setNeedsRestart(true);
	}, []);

	const handleSave = useCallback(async () => {
		if (!bifrostConfig) {
			toast.error("Configuration not loaded");
			return;
		}
		try {
			await updateCoreConfig({ ...bifrostConfig, client_config: localConfig }).unwrap();
			toast.success("Feature toggles updated successfully.");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	}, [bifrostConfig, localConfig, updateCoreConfig]);

	return (
		<div className="space-y-4">
			<div className="flex items-center justify-between">
				<div>
					<h2 className="text-2xl font-semibold tracking-tight">Feature Toggles</h2>
					<p className="text-muted-foreground text-sm">Enable or disable major features.</p>
				</div>
				<Button onClick={handleSave} disabled={!hasChanges || isLoading}>
					{isLoading ? "Saving..." : "Save Changes"}
				</Button>
			</div>

			<Alert variant="destructive">
				<AlertTriangle className="h-4 w-4" />
				<AlertDescription>
					These settings require a Bifrost service restart to take effect. Current connections will continue with existing settings until
					restart.
				</AlertDescription>
			</Alert>

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

				{/* Enable Governance */}
				<div>
					<div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
						<div className="space-y-0.5">
							<label htmlFor="enable-governance" className="text-sm font-medium">
								Enable Governance
							</label>
							<p className="text-muted-foreground text-sm">
								Enable governance on requests. You can configure budgets and rate limits in the <b>Governance</b> tab.
							</p>
						</div>
						<Switch
							id="enable-governance"
							size="md"
							checked={localConfig.enable_governance}
							onCheckedChange={(checked) => handleConfigChange("enable_governance", checked)}
						/>
					</div>
					{needsRestart && <RestartWarning />}
				</div>

				{/* Plugins Form */}
				<PluginsForm isVectorStoreEnabled={bifrostConfig?.is_cache_connected ?? false} />
			</div>
		</div>
	);
}

const RestartWarning = () => {
	return <div className="text-muted-foreground mt-2 pl-4 text-xs font-semibold">Need to restart Bifrost to apply changes.</div>;
};
