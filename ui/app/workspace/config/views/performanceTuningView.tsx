"use client";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { getErrorMessage, useGetCoreConfigQuery, useUpdateCoreConfigMutation } from "@/lib/store";
import { CoreConfig, DefaultCoreConfig } from "@/lib/types/config";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { AlertTriangle } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";

export default function PerformanceTuningView() {
	const hasSettingsUpdateAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	const { data: bifrostConfig } = useGetCoreConfigQuery({ fromDB: true });
	const config = bifrostConfig?.client_config;
	const [updateCoreConfig, { isLoading }] = useUpdateCoreConfigMutation();
	const [localConfig, setLocalConfig] = useState<CoreConfig>(DefaultCoreConfig);
	const [needsRestart, setNeedsRestart] = useState<boolean>(false);

	const [localValues, setLocalValues] = useState<{
		initial_pool_size: string;
		max_request_body_size_mb: string;
	}>({
		initial_pool_size: "1000",
		max_request_body_size_mb: "100",
	});

	useEffect(() => {
		if (bifrostConfig && config) {
			setLocalConfig(config);
			setLocalValues({
				initial_pool_size: config?.initial_pool_size?.toString() || "1000",
				max_request_body_size_mb: config?.max_request_body_size_mb?.toString() || "100",
			});
		}
	}, [config, bifrostConfig]);

	const hasChanges = useMemo(() => {
		if (!config) return false;
		return (
			localConfig.initial_pool_size !== config.initial_pool_size || localConfig.max_request_body_size_mb !== config.max_request_body_size_mb
		);
	}, [config, localConfig]);

	const handlePoolSizeChange = useCallback((value: string) => {
		setLocalValues((prev) => ({ ...prev, initial_pool_size: value }));
		const numValue = Number.parseInt(value);
		if (!isNaN(numValue) && numValue > 0) {
			setLocalConfig((prev) => ({ ...prev, initial_pool_size: numValue }));
		}
		setNeedsRestart(true);
	}, []);

	const handleMaxRequestBodySizeMBChange = useCallback((value: string) => {
		setLocalValues((prev) => ({ ...prev, max_request_body_size_mb: value }));
		const numValue = Number.parseInt(value);
		if (!isNaN(numValue) && numValue > 0) {
			setLocalConfig((prev) => ({ ...prev, max_request_body_size_mb: numValue }));
		}
		setNeedsRestart(true);
	}, []);

	const handleSave = useCallback(async () => {
		try {
			const poolSize = Number.parseInt(localValues.initial_pool_size);
			const maxBodySize = Number.parseInt(localValues.max_request_body_size_mb);

			if (isNaN(poolSize) || poolSize <= 0) {
				toast.error("Initial pool size must be a positive number.");
				return;
			}

			if (isNaN(maxBodySize) || maxBodySize <= 0) {
				toast.error("Max request body size must be a positive number.");
				return;
			}

			if (!bifrostConfig) {
				toast.error("Configuration not loaded. Please refresh and try again.");
				return;
			}
			await updateCoreConfig({ ...bifrostConfig, client_config: localConfig }).unwrap();
			toast.success("Performance settings updated successfully.");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	}, [bifrostConfig, localConfig, localValues, updateCoreConfig]);

	return (
		<div className="mx-auto w-full max-w-4xl space-y-4">
			<div>
				<h2 className="text-lg font-semibold tracking-tight">Performance Tuning</h2>
				<p className="text-muted-foreground text-sm">Configure performance-related settings.</p>
			</div>

			<Alert variant="destructive">
				<AlertTriangle className="h-4 w-4" />
				<AlertDescription>
					These settings require a Bifrost service restart to take effect. Current connections will continue with existing settings until
					restart.
				</AlertDescription>
			</Alert>

			<div className="space-y-4">
				{/* Initial Pool Size */}
				<div>
					<div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
						<div className="space-y-0.5">
							<label htmlFor="initial-pool-size" className="text-sm font-medium">
								Initial Pool Size
							</label>
							<p className="text-muted-foreground text-sm">The initial connection pool size.</p>
						</div>
						<Input
							id="initial-pool-size"
							type="number"
							className="w-24"
							value={localValues.initial_pool_size}
							onChange={(e) => handlePoolSizeChange(e.target.value)}
							min="1"
						/>
					</div>
					{needsRestart && <RestartWarning />}
				</div>

				{/* Max Request Body Size */}
				<div>
					<div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
						<div className="space-y-0.5">
							<label htmlFor="max-request-body-size-mb" className="text-sm font-medium">
								Max Request Body Size (MB)
							</label>
							<p className="text-muted-foreground text-sm">Maximum size of request body in megabytes.</p>
						</div>
						<Input
							id="max-request-body-size-mb"
							type="number"
							className="w-24"
							value={localValues.max_request_body_size_mb}
							onChange={(e) => handleMaxRequestBodySizeMBChange(e.target.value)}
							min="1"
						/>
					</div>
					{needsRestart && <RestartWarning />}
				</div>
			</div>
			<div className="flex justify-end pt-2">
				<Button onClick={handleSave} disabled={!hasChanges || isLoading || !hasSettingsUpdateAccess}>
					{isLoading ? "Saving..." : "Save Changes"}
				</Button>
			</div>
		</div>
	);
}

const RestartWarning = () => {
	return <div className="text-muted-foreground mt-2 pl-4 text-xs font-semibold">Need to restart Bifrost to apply changes.</div>;
};
