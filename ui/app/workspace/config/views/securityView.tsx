"use client";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { getErrorMessage, useGetCoreConfigQuery, useUpdateCoreConfigMutation } from "@/lib/store";
import { CoreConfig } from "@/lib/types/config";
import { parseArrayFromText } from "@/lib/utils/array";
import { validateOrigins } from "@/lib/utils/validation";
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

export default function SecurityView() {
	const { data: bifrostConfig } = useGetCoreConfigQuery({ fromDB: true });
	const config = bifrostConfig?.client_config;
	const [updateCoreConfig, { isLoading }] = useUpdateCoreConfigMutation();
	const [localConfig, setLocalConfig] = useState<CoreConfig>(defaultConfig);
	const [needsRestart, setNeedsRestart] = useState<boolean>(false);

	const [localValues, setLocalValues] = useState<{
		allowed_origins: string;
	}>({
		allowed_origins: "",
	});

	useEffect(() => {
		if (bifrostConfig && config) {
			setLocalConfig(config);
			setLocalValues({
				allowed_origins: config?.allowed_origins?.join(", ") || "",
			});
		}
	}, [config, bifrostConfig]);

	const hasChanges = useMemo(() => {
		if (!config) return false;
		const localOrigins = localConfig.allowed_origins?.slice().sort().join(",");
		const serverOrigins = config.allowed_origins?.slice().sort().join(",");
		return localOrigins !== serverOrigins;
	}, [config, localConfig]);

	const handleAllowedOriginsChange = useCallback((value: string) => {
		const nextOrigins = parseArrayFromText(value);
		setLocalValues((prev) => ({ ...prev, allowed_origins: value }));
		setLocalConfig((prev) => ({ ...prev, allowed_origins: parseArrayFromText(value) }));
		const currentOrigins = config?.allowed_origins ?? [];
		const requiresRestart =
			nextOrigins.length !== currentOrigins.length || nextOrigins.some((origin, index) => origin !== currentOrigins[index]);
		setNeedsRestart(requiresRestart);
	}, []);

	const handleSave = useCallback(async () => {
		try {
			const validation = validateOrigins(localConfig.allowed_origins);

			if (!validation.isValid && localConfig.allowed_origins.length > 0) {
				toast.error(
					`Invalid origins: ${validation.invalidOrigins.join(", ")}. Origins must be valid URLs like https://example.com, wildcard patterns like https://*.example.com, or "*" to allow all origins`,
				);
				return;
			}

			await updateCoreConfig({ ...bifrostConfig!, client_config: localConfig }).unwrap();
			toast.success("Security settings updated successfully.");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	}, [bifrostConfig, localConfig, updateCoreConfig]);

	return (
		<div className="space-y-4">
			<div className="flex items-center justify-between">
				<div>
					<h2 className="text-2xl font-semibold tracking-tight">Security Settings</h2>
					<p className="text-muted-foreground text-sm">Configure security and access control settings.</p>
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
				{/* Allowed Origins */}
				<div>
					<div className="space-y-2 rounded-lg border p-4">
						<div className="space-y-0.5">
							<label htmlFor="allowed-origins" className="text-sm font-medium">
								Allowed Origins
							</label>
							<p className="text-muted-foreground text-sm">
								Comma-separated list of allowed origins for CORS and WebSocket connections. Localhost origins are always allowed. Each
								origin must be a complete URL with protocol (e.g., https://app.example.com, http://10.0.0.100:3000). Wildcards are supported
								for subdomains (e.g., https://*.example.com) or use "*" to allow all origins.
							</p>
						</div>
						<Textarea
							id="allowed-origins"
							className="h-24"
							placeholder="https://app.example.com, https://*.example.com, *"
							value={localValues.allowed_origins}
							onChange={(e) => handleAllowedOriginsChange(e.target.value)}
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
