"use client";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EnvVarInput } from "@/components/ui/envVarInput";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { IS_ENTERPRISE } from "@/lib/constants/config";
import { getErrorMessage, useGetCoreConfigQuery, useUpdateCoreConfigMutation } from "@/lib/store";
import { AuthConfig, CoreConfig, DefaultCoreConfig } from "@/lib/types/config";
import { EnvVar } from "@/lib/types/schemas";
import { parseArrayFromText } from "@/lib/utils/array";
import { validateOrigins } from "@/lib/utils/validation";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { AlertTriangle, Info } from "lucide-react";
import Link from "next/link";
import { useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";

export default function SecurityView() {
	const hasSettingsUpdateAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	const { data: bifrostConfig } = useGetCoreConfigQuery({ fromDB: true });
	const config = bifrostConfig?.client_config;
	const [updateCoreConfig, { isLoading }] = useUpdateCoreConfigMutation();
	const [localConfig, setLocalConfig] = useState<CoreConfig>(DefaultCoreConfig);
	const hideAuthDashboard = IS_ENTERPRISE;

	const [localValues, setLocalValues] = useState<{
		allowed_origins: string;
		allowed_headers: string;
		required_headers: string;
	}>({
		allowed_origins: "",
		allowed_headers: "",
		required_headers: "",
	});

	const [authConfig, setAuthConfig] = useState<AuthConfig>({
		admin_username: { value: "", env_var: "", from_env: false },
		admin_password: { value: "", env_var: "", from_env: false },
		is_enabled: false,
		disable_auth_on_inference: false,
	});

	useEffect(() => {
		if (bifrostConfig && config) {
			setLocalConfig(config);
			setLocalValues({
				allowed_origins: config?.allowed_origins?.join(", ") || "",
				allowed_headers: config?.allowed_headers?.join(", ") || "",
				required_headers: config?.required_headers?.join(", ") || "",
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

		const localHeaders = localConfig.allowed_headers?.slice().sort().join(",");
		const serverHeaders = config.allowed_headers?.slice().sort().join(",");
		const headersChanged = localHeaders !== serverHeaders;

		const usernameChanged =
			authConfig.admin_username?.value !== bifrostConfig?.auth_config?.admin_username?.value ||
			authConfig.admin_username?.env_var !== bifrostConfig?.auth_config?.admin_username?.env_var ||
			authConfig.admin_username?.from_env !== bifrostConfig?.auth_config?.admin_username?.from_env;
		const passwordChanged =
			authConfig.admin_password?.value !== bifrostConfig?.auth_config?.admin_password?.value ||
			authConfig.admin_password?.env_var !== bifrostConfig?.auth_config?.admin_password?.env_var ||
			authConfig.admin_password?.from_env !== bifrostConfig?.auth_config?.admin_password?.from_env;
		const authChanged =
			authConfig.is_enabled !== bifrostConfig?.auth_config?.is_enabled ||
			usernameChanged ||
			passwordChanged ||
			authConfig.disable_auth_on_inference !== bifrostConfig?.auth_config?.disable_auth_on_inference;

		const localRequired = localConfig.required_headers?.slice().sort().join(",");
		const serverRequired = config.required_headers?.slice().sort().join(",");
		const requiredChanged = localRequired !== serverRequired;

		const enforceAuthOnInferenceChanged = localConfig.enforce_auth_on_inference !== config.enforce_auth_on_inference;
		const allowDirectKeysChanged = localConfig.allow_direct_keys !== config.allow_direct_keys;

		return originsChanged || headersChanged || requiredChanged || authChanged || enforceAuthOnInferenceChanged || allowDirectKeysChanged;
	}, [config, localConfig, authConfig, bifrostConfig]);

	const needsRestart = useMemo(() => {
		if (!config) return false;

		const localOrigins = localConfig.allowed_origins?.slice().sort().join(",");
		const serverOrigins = config.allowed_origins?.slice().sort().join(",");
		const originsChanged = localOrigins !== serverOrigins;

		const localHeaders = localConfig.allowed_headers?.slice().sort().join(",");
		const serverHeaders = config.allowed_headers?.slice().sort().join(",");
		const headersChanged = localHeaders !== serverHeaders;

		const enforceAuthOnInferenceChanged = localConfig.enforce_auth_on_inference !== config.enforce_auth_on_inference && IS_ENTERPRISE;

		return originsChanged || headersChanged || enforceAuthOnInferenceChanged;
	}, [config, localConfig]);

	const handleAllowedOriginsChange = useCallback((value: string) => {
		setLocalValues((prev) => ({ ...prev, allowed_origins: value }));
		setLocalConfig((prev) => ({ ...prev, allowed_origins: parseArrayFromText(value) }));
	}, []);

	const handleAllowedHeadersChange = useCallback((value: string) => {
		setLocalValues((prev) => ({ ...prev, allowed_headers: value }));
		setLocalConfig((prev) => ({ ...prev, allowed_headers: parseArrayFromText(value) }));
	}, []);

	const handleRequiredHeadersChange = useCallback((value: string) => {
		setLocalValues((prev) => ({ ...prev, required_headers: value }));
		setLocalConfig((prev) => ({ ...prev, required_headers: parseArrayFromText(value) }));
	}, []);

	const handleConfigChange = useCallback((field: keyof CoreConfig, value: boolean) => {
		setLocalConfig((prev) => ({ ...prev, [field]: value }));
	}, []);

	const handleAuthToggle = useCallback((checked: boolean) => {
		setAuthConfig((prev) => ({ ...prev, is_enabled: checked }));
	}, []);

	const handleDisableAuthOnInferenceToggle = useCallback((checked: boolean) => {
		setAuthConfig((prev) => ({ ...prev, disable_auth_on_inference: checked }));
	}, []);

	const handleAuthFieldChange = useCallback((field: "admin_username" | "admin_password", value: EnvVar) => {
		setAuthConfig((prev) => ({ ...prev, [field]: value }));
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
			const hasUsername = authConfig.admin_username?.value || authConfig.admin_username?.env_var;
			const hasPassword = authConfig.admin_password?.value || authConfig.admin_password?.env_var;
			await updateCoreConfig({
				...bifrostConfig!,
				client_config: localConfig,
				auth_config: authConfig.is_enabled && hasUsername && hasPassword ? authConfig : { ...authConfig, is_enabled: false },
			}).unwrap();
			toast.success("Security settings updated successfully.");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	}, [bifrostConfig, localConfig, authConfig, updateCoreConfig]);

	return (
		<div className="mx-auto w-full max-w-4xl space-y-4">
			<div>
				<h2 className="text-lg font-semibold tracking-tight">Security Settings</h2>
				<p className="text-muted-foreground text-sm">Configure security and access control settings.</p>
			</div>

			<div className="space-y-4">
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
									<EnvVarInput
										id="admin-username"
										type="text"
										placeholder="Enter admin username or env.VAR_NAME"
										value={authConfig.admin_username}
										disabled={!authConfig.is_enabled}
										onChange={(value) => handleAuthFieldChange("admin_username", value)}
									/>
								</div>
								<div className="space-y-2">
									<Label htmlFor="admin-password">Password</Label>
									<EnvVarInput
										id="admin-password"
										type="password"
										placeholder="Enter admin password or env.VAR_NAME"
										value={authConfig.admin_password}
										disabled={!authConfig.is_enabled}
										onChange={(value) => handleAuthFieldChange("admin_password", value)}
									/>
								</div>
								<div className="flex items-center justify-between">
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
				{/* Enable Auth on Inference */}
				<div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
					<div className="space-y-0.5">
						<label htmlFor="enforce-auth-on-inference" className="text-sm font-medium">
						{IS_ENTERPRISE ? "Enable Auth on Inference" : "Enforce Virtual Keys on Inference"}
						</label>
						<p className="text-muted-foreground text-sm">
							{IS_ENTERPRISE
								? "Require authentication (virtual key, API key, or user token) for all inference endpoints."
								: "Require a virtual key for all inference requests."}{" "}
							See{" "}
							<Link
								href="https://docs.getbifrost.ai/features/governance/virtual-keys"
								target="_blank"
								rel="noopener noreferrer"
								className="text-primary underline"
								data-testid="security-virtual-keys-docs-link"
							>
								documentation
							</Link>{" "}
							for details.
						</p>
					</div>
					<Switch
						id="enforce-auth-on-inference"
						data-testid="enforce-auth-on-inference-switch"
						checked={localConfig.enforce_auth_on_inference}
						onCheckedChange={(checked) => handleConfigChange("enforce_auth_on_inference", checked)}
					/>
				</div>
				{/* Allowed Origins */}
				{needsRestart && <RestartWarning />}
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
				</div>
				{/* Allowed Headers */}
				<div>
					<div className="space-y-2 rounded-lg border p-4">
						<div className="space-y-0.5">
							<label htmlFor="allowed-headers" className="text-sm font-medium">
								Allowed Headers
							</label>
							<p className="text-muted-foreground text-sm">Comma-separated list of allowed headers for CORS.</p>
						</div>
						<Textarea
							id="allowed-headers"
							className="h-24"
							placeholder="X-Stainless-Timeout"
							value={localValues.allowed_headers}
							onChange={(e) => handleAllowedHeadersChange(e.target.value)}
						/>
					</div>
				</div>
				{/* Required Headers */}
				<div>
					<div className="space-y-2 rounded-lg border p-4">
						<div className="space-y-0.5">
							<label htmlFor="required-headers" className="text-sm font-medium">
								Required Headers
							</label>
							<p className="text-muted-foreground text-sm">
								Comma-separated list of headers that must be present on every request. Requests missing any of these headers will be
								rejected with a 400 error. Header names are case-insensitive.
							</p>
						</div>
						<Textarea
							id="required-headers"
							data-testid="required-headers-textarea"
							className="h-24"
							placeholder="X-Tenant-ID, X-Custom-Header"
							value={localValues.required_headers}
							onChange={(e) => handleRequiredHeadersChange(e.target.value)}
						/>
					</div>
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
	return (
		<Alert variant="destructive" className="mt-2">
			<AlertTriangle className="h-4 w-4" />
			<AlertDescription>Need to restart Bifrost to apply changes.</AlertDescription>
		</Alert>
	);
};
