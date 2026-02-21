"use client";

import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { getErrorMessage, useGetCoreConfigQuery, useUpdateCoreConfigMutation } from "@/lib/store";
import { CoreConfig, DefaultCoreConfig } from "@/lib/types/config";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";

export default function GovernanceView() {
	const hasSettingsUpdateAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	const { data: bifrostConfig } = useGetCoreConfigQuery({ fromDB: true });
	const config = bifrostConfig?.client_config;
	const [updateCoreConfig, { isLoading }] = useUpdateCoreConfigMutation();
	const [localConfig, setLocalConfig] = useState<CoreConfig>(DefaultCoreConfig);
	const [needsRestart, setNeedsRestart] = useState<boolean>(false);

	useEffect(() => {
		if (config) {
			setLocalConfig(config);
		}
	}, [config]);

	const hasChanges = useMemo(() => {
		if (!config) return false;
		return localConfig.enable_governance !== config.enable_governance;
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
			toast.success("Governance configuration updated successfully.");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	}, [bifrostConfig, localConfig, updateCoreConfig]);

	return (
		<div className="mx-auto w-full max-w-4xl space-y-4">
			<div>
				<h2 className="text-lg font-semibold tracking-tight">Governance</h2>
				<p className="text-muted-foreground text-sm">Configure governance settings for requests.</p>
			</div>

			<div className="space-y-4">
				{/* Enable Governance */}
				<div>
					<div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
						<div className="space-y-0.5">
							<label htmlFor="enable-governance" className="text-sm font-medium">
								Enable Governance
							</label>
							<p className="text-muted-foreground text-sm">
								Enable governance on requests. You can configure budgets and rate limits in the <b>Governance</b> section.
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
