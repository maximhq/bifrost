"use client";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { getErrorMessage, useGetCoreConfigQuery, useUpdateCoreConfigMutation } from "@/lib/store";
import { AuthConfig, CoreConfig } from "@/lib/types/config";
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
	const [authNeedsRestart, setAuthNeedsRestart] = useState<boolean>(false);

	const [localValues, setLocalValues] = useState<{
		allowed_origins: string;
	}>({
		allowed_origins: "",
	});

	const [authConfig, setAuthConfig] = useState<AuthConfig>({
		admin_username: "",
		admin_password: "",
		is_enabled: false,
	});

	useEffect(() => {
		if (bifrostConfig && config) {
			setLocalConfig(config);
			setLocalValues({
				allowed_origins: config?.allowed_origins?.join(", ") || "",
			});
		}
		if (bifrostConfig?.auth_config) {
			setAuthConfig(bifrostConfig.auth_config);
		}
	}, [config, bifrostConfig]);

	const hasChanges = useMemo(() => {
		if (!config) return false;
		const localOrigins = localConfig.allowed_origins?.slice().sort().join(",");
		const serverOrigins = config.allowed_origins?.slice().sort().join(",");
		const originsChanged = localOrigins !== serverOrigins;

		const authChanged =
			authConfig.is_enabled !== bifrostConfig?.auth_config?.is_enabled ||
			authConfig.admin_username !== bifrostConfig?.auth_config?.admin_username ||
			authConfig.admin_password !== bifrostConfig?.auth_config?.admin_password;

		return originsChanged || authChanged;
	}, [config, localConfig, authConfig, bifrostConfig]);

	const handleAllowedOriginsChange = useCallback((value: string) => {
		const nextOrigins = parseArrayFromText(value);
		setLocalValues((prev) => ({ ...prev, allowed_origins: value }));
		setLocalConfig((prev) => ({ ...prev, allowed_origins: parseArrayFromText(value) }));
		const currentOrigins = config?.allowed_origins ?? [];
		const requiresRestart =
			nextOrigins.length !== currentOrigins.length || nextOrigins.some((origin, index) => origin !== currentOrigins[index]);
		setNeedsRestart(requiresRestart);
	}, []);

	const handleAuthToggle = useCallback(
		(checked: boolean) => {
			setAuthConfig((prev) => ({ ...prev, is_enabled: checked }));
			const originalAuthEnabled = bifrostConfig?.auth_config?.is_enabled ?? false;
			setAuthNeedsRestart(checked !== originalAuthEnabled);
		},
		[bifrostConfig?.auth_config?.is_enabled],
	);

	const handleSave = useCallback(async () => {
		try {
			const validation = validateOrigins(localConfig.allowed_origins);

			if (!validation.isValid && localConfig.allowed_origins.length > 0) {
				toast.error(
					`Invalid origins: ${validation.invalidOrigins.join(", ")}. Origins must be valid URLs like https://example.com, wildcard patterns like https://*.example.com, or "*" to allow all origins`,
				);
				return;
			}

			await updateCoreConfig({
				...bifrostConfig!,
				client_config: localConfig,
				auth_config:
					authConfig.is_enabled && authConfig.admin_username && authConfig.admin_password
						? authConfig
						: { ...authConfig, is_enabled: false },
			}).unwrap();
			toast.success("Security settings updated successfully.");
			setAuthNeedsRestart(false);
			setNeedsRestart(false);
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	}, [bifrostConfig, localConfig, authConfig, updateCoreConfig]);

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

			<div className="space-y-4">
				{authNeedsRestart && <RestartWarning />}
				{/* Password Protect the Dashboard */}
				<div>
					<div className="space-y-4 rounded-lg border p-4">
						<div className="flex items-center justify-between">
							<div className="space-y-0.5">
								<Label htmlFor="auth-enabled" className="text-sm font-medium">
									Password protect the dashboard
								</Label>
								<p className="text-muted-foreground text-sm">
									Set up authentication credentials to protect your Bifrost dashboard. Once configured, use the generated token for all
									admin API calls.
								</p>
							</div>
							<Switch id="auth-enabled" checked={authConfig.is_enabled} onCheckedChange={handleAuthToggle} />
						</div>
						<div className="space-y-4">
							<div className="space-y-2">
								<Label htmlFor="admin-username">Username</Label>
								<Input
									id="admin-username"
									type="text"
									placeholder="Enter admin username"
									value={authConfig.admin_username}
									disabled={!authConfig.is_enabled}
									onChange={(e) => {
										setAuthConfig((prev) => ({ ...prev, admin_username: e.target.value }));
									}}
								/>
							</div>
							<div className="space-y-2">
								<Label htmlFor="admin-password">Password</Label>
								<Input
									id="admin-password"
									type="password"
									placeholder="Enter admin password"
									value={authConfig.admin_password}
									disabled={!authConfig.is_enabled}
									onChange={(e) => {
										setAuthConfig((prev) => ({ ...prev, admin_password: e.target.value }));
									}}
								/>
							</div>
						</div>
					</div>
				</div>

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
	return (
		<Alert variant="destructive" className="mt-2">
			<AlertTriangle className="h-4 w-4" />
			<AlertDescription>Need to restart Bifrost to apply changes.</AlertDescription>
		</Alert>
	);
};
