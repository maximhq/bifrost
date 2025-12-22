"use client";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { IS_ENTERPRISE } from "@/lib/constants/config";
import { getErrorMessage, useGetCoreConfigQuery, useUpdateCoreConfigMutation } from "@/lib/store";
import { AuthConfig, CoreConfig } from "@/lib/types/config";
import { parseArrayFromText } from "@/lib/utils/array";
import { validateOrigins } from "@/lib/utils/validation";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { AlertTriangle, Info } from "lucide-react";
import Link from "next/link";
import { useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";

const defaultConfig: CoreConfig = {
	drop_excess_requests: false,
	initial_pool_size: 1000,
	prometheus_labels: [],
	enable_logging: true,
	disable_content_logging: false,
	enable_governance: true,
	enforce_governance_header: false,
	allow_direct_keys: false,
	allowed_origins: [],
	max_request_body_size_mb: 100,
	enable_litellm_fallbacks: false,
	log_retention_days: 365,
	mcp_agent_depth: 10,
	mcp_tool_execution_timeout: 30,
	mcp_code_mode_binding_level: "server",
};

export default function SecurityView() {
	const hasSettingsUpdateAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	const { data: bifrostConfig } = useGetCoreConfigQuery({ fromDB: true });
	const config = bifrostConfig?.client_config;
	const [updateCoreConfig, { isLoading }] = useUpdateCoreConfigMutation();
	const [localConfig, setLocalConfig] = useState<CoreConfig>(defaultConfig);
	const [needsRestart, setNeedsRestart] = useState<boolean>(false);
	const [authNeedsRestart, setAuthNeedsRestart] = useState<boolean>(false);
	const hideAuthDashboard = IS_ENTERPRISE;

	const [localValues, setLocalValues] = useState<{
		allowed_origins: string;
	}>({
		allowed_origins: "",
	});

	const [authConfig, setAuthConfig] = useState<AuthConfig>({
		admin_username: "",
		admin_password: "",
		is_enabled: false,
		disable_auth_on_inference: false,
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
			authConfig.admin_password !== bifrostConfig?.auth_config?.admin_password ||
			authConfig.disable_auth_on_inference !== bifrostConfig?.auth_config?.disable_auth_on_inference;

		const enforceVirtualKeyChanged = localConfig.enforce_governance_header !== config.enforce_governance_header;
		const allowDirectKeysChanged = localConfig.allow_direct_keys !== config.allow_direct_keys;

		return originsChanged || authChanged || enforceVirtualKeyChanged || allowDirectKeysChanged;
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

	const handleConfigChange = useCallback((field: keyof CoreConfig, value: boolean) => {
		setLocalConfig((prev) => ({ ...prev, [field]: value }));
	}, []);

	const checkAuthNeedsRestart = useCallback(
		(newAuthConfig: AuthConfig) => {
			const originalAuth = bifrostConfig?.auth_config;
			const hasChanged =
				newAuthConfig.is_enabled !== (originalAuth?.is_enabled ?? false) ||
				newAuthConfig.admin_username !== (originalAuth?.admin_username ?? "") ||
				newAuthConfig.admin_password !== (originalAuth?.admin_password ?? "") ||
				newAuthConfig.disable_auth_on_inference !== (originalAuth?.disable_auth_on_inference ?? false);
			setAuthNeedsRestart(hasChanged);
		},
		[bifrostConfig?.auth_config],
	);

	const handleAuthToggle = useCallback(
		(checked: boolean) => {
			const newAuthConfig = { ...authConfig, is_enabled: checked };
			setAuthConfig(newAuthConfig);
			checkAuthNeedsRestart(newAuthConfig);
		},
		[authConfig, checkAuthNeedsRestart],
	);

	const handleDisableAuthOnInferenceToggle = useCallback(
		(checked: boolean) => {
			const newAuthConfig = { ...authConfig, disable_auth_on_inference: checked };
			setAuthConfig(newAuthConfig);
			checkAuthNeedsRestart(newAuthConfig);
		},
		[authConfig, checkAuthNeedsRestart],
	);

	const handleAuthFieldChange = useCallback(
		(field: "admin_username" | "admin_password", value: string) => {
			const newAuthConfig = { ...authConfig, [field]: value };
			setAuthConfig(newAuthConfig);
			checkAuthNeedsRestart(newAuthConfig);
		},
		[authConfig, checkAuthNeedsRestart],
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
		<div className="mx-auto w-full max-w-4xl space-y-4">
			<div className="flex items-center justify-between">
				<div>
					<h2 className="text-2xl font-semibold tracking-tight">Security Settings</h2>
					<p className="text-muted-foreground text-sm">Configure security and access control settings.</p>
				</div>
				<Button onClick={handleSave} disabled={!hasChanges || isLoading || !hasSettingsUpdateAccess}>
					{isLoading ? "Saving..." : "Save Changes"}
				</Button>
			</div>

			<div className="space-y-4">
				{authNeedsRestart && <RestartWarning />}
				{authConfig.is_enabled && !authConfig.disable_auth_on_inference && (
					<Alert variant="default" className="border-blue-20">
						<Info className="h-4 w-4 text-blue-600" />
						<AlertDescription>
							You will need to use Basic Auth for all your inference calls (including MCP tool execution). You can disable it below. Check{" "}
							<Link href="/workspace/config/api-keys" className="text-md text-primary underline">
								API Keys
							</Link>
						</AlertDescription>
					</Alert>
				)}
				{authConfig.is_enabled && authConfig.disable_auth_on_inference && (
					<Alert variant="default" className="border-blue-20">
						<Info className="h-4 w-4 text-blue-600" />
						<AlertDescription>
							Authentication is disabled for inference calls. Only dashboard, admin API and MCP tool execution calls require authentication.
						</AlertDescription>
					</Alert>
				)}
				{/* Password Protect the Dashboard */}
				{!hideAuthDashboard && (
					<div>
						<div className="space-y-4 rounded-lg border p-4">
							<div className="flex items-center justify-between">
								<div className="space-y-0.5">
									<Label htmlFor="auth-enabled" className="text-sm font-medium">
										Password protect the dashboard <Badge variant="secondary">BETA</Badge>
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
										onChange={(e) => handleAuthFieldChange("admin_username", e.target.value)}
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
										onChange={(e) => handleAuthFieldChange("admin_password", e.target.value)}
									/>
								</div>
								<div className="flex items-center justify-between rounded-lg border p-4">
									<div className="space-y-0.5">
										<Label htmlFor="disable-auth-inference" className="text-sm font-medium">
											Disable authentication on inference calls
										</Label>
										<p className="text-muted-foreground text-sm">
											When enabled, inference API calls (chat completions, embeddings, etc.) will not require authentication. Dashboard and
											admin API calls will still require authentication.
										</p>
									</div>
									<Switch
										id="disable-auth-inference"
										className="ml-5"
										checked={authConfig.disable_auth_on_inference ?? false}
										disabled={!authConfig.is_enabled}
										onCheckedChange={handleDisableAuthOnInferenceToggle}
									/>
								</div>
							</div>
						</div>
					</div>
				)}
				{/* Enforce Virtual Keys */}
				{localConfig.enable_governance && (
					<div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
						<div className="space-y-0.5">
							<label htmlFor="enforce-governance" className="text-sm font-medium">
								Enforce Virtual Keys
							</label>
							<p className="text-muted-foreground text-sm">
								Enforce the use of a virtual key for all requests. If enabled, requests without the <b>x-bf-vk</b> header will be rejected.
							</p>
						</div>
						<Switch
							id="enforce-governance"
							checked={localConfig.enforce_governance_header}
							onCheckedChange={(checked) => handleConfigChange("enforce_governance_header", checked)}
						/>
					</div>
				)}
				{/* Allow Direct API Keys */}
				<div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
					<div className="space-y-0.5">
						<label htmlFor="allow-direct-keys" className="text-sm font-medium">
							Allow Direct API Keys
						</label>
						<p className="text-muted-foreground text-sm">
							Allow API keys to be passed directly in request headers (<b>Authorization</b>, <b>x-api-key</b>, or <b>x-goog-api-key</b>).
							Bifrost will directly use the key.
						</p>
					</div>
					<Switch
						id="allow-direct-keys"
						checked={localConfig.allow_direct_keys}
						onCheckedChange={(checked) => handleConfigChange("allow_direct_keys", checked)}
					/>
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
