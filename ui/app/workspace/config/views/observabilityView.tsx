"use client";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { getErrorMessage, useGetCoreConfigQuery, useUpdateCoreConfigMutation } from "@/lib/store";
import { CoreConfig } from "@/lib/types/config";
import { parseArrayFromText } from "@/lib/utils/array";
import { AlertTriangle } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";

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

export default function ObservabilityView() {
	const { data: bifrostConfig } = useGetCoreConfigQuery({ fromDB: true });
	const config = bifrostConfig?.client_config;
	const [updateCoreConfig, { isLoading }] = useUpdateCoreConfigMutation();
	const [localConfig, setLocalConfig] = useState<CoreConfig>(defaultConfig);
	const [needsRestart, setNeedsRestart] = useState<boolean>(false);

	const [localValues, setLocalValues] = useState<{
		prometheus_labels: string;
	}>({
		prometheus_labels: "",
	});

	useEffect(() => {
		if (bifrostConfig && config) {
			setLocalConfig(config);
			setLocalValues({
				prometheus_labels: config?.prometheus_labels?.join(", ") || "",
			});
		}
	}, [config, bifrostConfig]);

	const hasChanges = useMemo(() => {
		if (!config) return false;
		const localLabels = localConfig.prometheus_labels.slice().sort().join(",");
		const serverLabels = config.prometheus_labels.slice().sort().join(",");
		return localLabels !== serverLabels;
	}, [config, localConfig]);

	const handlePrometheusLabelsChange = useCallback((value: string) => {
		setLocalValues((prev) => ({ ...prev, prometheus_labels: value }));
		setLocalConfig((prev) => ({ ...prev, prometheus_labels: parseArrayFromText(value) }));
		setNeedsRestart(true);
	}, []);

	const handleSave = useCallback(async () => {
		if (!bifrostConfig) {
			toast.error("Could not save settings: configuration not loaded.");
			return;
		}
		try {
			await updateCoreConfig({ ...bifrostConfig, client_config: localConfig }).unwrap();
			toast.success("Observability settings updated successfully.");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	}, [bifrostConfig, localConfig, updateCoreConfig]);

	return (
		<div className="space-y-4">
			<div className="flex items-center justify-between">
				<div>
					<h2 className="text-2xl font-semibold tracking-tight">Observability Settings</h2>
					<p className="text-muted-foreground text-sm">Configure monitoring and observability features.</p>
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
				{/* Prometheus Labels */}
				<div>
					<div className="space-y-2 rounded-lg border p-4">
						<div className="space-y-0.5">
							<label htmlFor="prometheus-labels" className="text-sm font-medium">
								Prometheus Labels
							</label>
							<p className="text-muted-foreground text-sm">Comma-separated list of custom labels to add to the Prometheus metrics.</p>
						</div>
						<Textarea
							id="prometheus-labels"
							className="h-24"
							placeholder="teamId, projectId, environment"
							value={localValues.prometheus_labels}
							onChange={(e) => handlePrometheusLabelsChange(e.target.value)}
						/>
					</div>
					{needsRestart && <RestartWarning />}
				</div>
			</div>
		</div>
	);
}

const RestartWarning = () => {
	return <div className="text-muted-foreground mt-2 pl-4 text-xs font-semibold">Need to restart Bifrost to apply changes.</div>;
};
