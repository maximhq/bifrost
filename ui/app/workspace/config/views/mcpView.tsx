import PageTitle from "@/components/pageTitle";
import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from "@/components/ui/accordion";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { getErrorMessage, useGetCoreConfigQuery, useUpdateCoreConfigMutation } from "@/lib/store";
import { CoreConfig, DefaultCoreConfig } from "@/lib/types/config";
import { SecretVar } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useGetSCIMProvidersQuery } from "@enterprise/lib/store/apis/scimApi";
import { IS_ENTERPRISE } from "@/lib/constants/config";
import { AlertTriangle } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import { toast } from "sonner";

const secretVarEquals = (a?: SecretVar, b?: SecretVar) =>
	(a?.value ?? "") === (b?.value ?? "") && (a?.ref ?? "") === (b?.ref ?? "") && (a?.type ?? "plain_text") === (b?.type ?? "plain_text");

export default function MCPView() {
	const { t } = useTranslation("config");
	const hasSettingsUpdateAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	const { data: bifrostConfig } = useGetCoreConfigQuery({ fromDB: true });
	const config = bifrostConfig?.client_config;

	// The "require identity-provider login" toggle is enterprise-only and only
	// meaningful when an identity provider is configured — the backend ignores
	// disable_vk_identity otherwise, so surfacing it would be a no-op. The SCIM
	// query is skipped (and stubbed to []) in OSS builds.
	const { data: scimProviders } = useGetSCIMProvidersQuery(undefined, { skip: !IS_ENTERPRISE });
	const idpConfigured = !!scimProviders?.some((p) => (p as { enabled?: boolean }).enabled);
	const [updateCoreConfig, { isLoading }] = useUpdateCoreConfigMutation();
	const [localConfig, setLocalConfig] = useState<CoreConfig>(DefaultCoreConfig);

	const [localValues, setLocalValues] = useState<{
		mcp_agent_depth: string;
		mcp_tool_execution_timeout: string;
		mcp_code_mode_binding_level: string;
		mcp_tool_sync_interval: string;
		oauth2_auth_code_ttl: string;
		oauth2_access_token_ttl: string;
	}>({
		mcp_agent_depth: "10",
		mcp_tool_execution_timeout: "30",
		mcp_code_mode_binding_level: "server",
		mcp_tool_sync_interval: "10",
		oauth2_auth_code_ttl: "300",
		oauth2_access_token_ttl: "600",
	});

	useEffect(() => {
		if (bifrostConfig && config) {
			setLocalConfig(config);
			setLocalValues({
				mcp_agent_depth: config?.mcp_agent_depth?.toString() || "10",
				mcp_tool_execution_timeout: config?.mcp_tool_execution_timeout?.toString() || "30",
				mcp_code_mode_binding_level: config?.mcp_code_mode_binding_level || "server",
				mcp_tool_sync_interval: config?.mcp_tool_sync_interval?.toString() || "10",
				// Coerce a stored 0 (which the backend treats as "use default") to the
				// displayed default so the inputs never show a confusing 0.
				oauth2_auth_code_ttl: (config?.oauth2_server_config?.auth_code_ttl || 300).toString(),
				oauth2_access_token_ttl: (config?.oauth2_server_config?.access_token_ttl || 600).toString(),
			});
		}
	}, [config, bifrostConfig]);

	const hasChanges = useMemo(() => {
		if (!config) return false;
		const clientURLChanged = !secretVarEquals(localConfig.mcp_external_client_url, config.mcp_external_client_url);
		const issuerURLChanged = !secretVarEquals(localConfig.oauth2_server_config?.issuer_url, config.oauth2_server_config?.issuer_url);
		return (
			localConfig.mcp_agent_depth !== config.mcp_agent_depth ||
			localConfig.mcp_tool_execution_timeout !== config.mcp_tool_execution_timeout ||
			localConfig.mcp_code_mode_binding_level !== (config.mcp_code_mode_binding_level || "server") ||
			localConfig.mcp_tool_sync_interval !== (config.mcp_tool_sync_interval ?? 10) ||
			localConfig.mcp_disable_auto_tool_inject !== (config.mcp_disable_auto_tool_inject ?? false) ||
			localConfig.mcp_enable_temp_token_auth !== (config.mcp_enable_temp_token_auth ?? false) ||
			clientURLChanged ||
			(localConfig.mcp_server_auth_mode ?? "headers") !== (config.mcp_server_auth_mode ?? "headers") ||
			issuerURLChanged ||
			(localConfig.oauth2_server_config?.auth_code_ttl ?? 300) !== (config.oauth2_server_config?.auth_code_ttl ?? 300) ||
			(localConfig.oauth2_server_config?.access_token_ttl ?? 600) !== (config.oauth2_server_config?.access_token_ttl ?? 600) ||
			(localConfig.oauth2_server_config?.disable_vk_identity ?? false) !== (config.oauth2_server_config?.disable_vk_identity ?? false)
		);
	}, [config, localConfig]);

	const handleAgentDepthChange = useCallback((value: string) => {
		setLocalValues((prev) => ({ ...prev, mcp_agent_depth: value }));
		const numValue = Number.parseInt(value);
		if (!isNaN(numValue) && numValue > 0) {
			setLocalConfig((prev) => ({ ...prev, mcp_agent_depth: numValue }));
		}
	}, []);

	const handleToolExecutionTimeoutChange = useCallback((value: string) => {
		setLocalValues((prev) => ({ ...prev, mcp_tool_execution_timeout: value }));
		const numValue = Number.parseInt(value);
		if (!isNaN(numValue) && numValue > 0) {
			setLocalConfig((prev) => ({
				...prev,
				mcp_tool_execution_timeout: numValue,
			}));
		}
	}, []);

	const handleCodeModeBindingLevelChange = useCallback((value: string) => {
		setLocalValues((prev) => ({ ...prev, mcp_code_mode_binding_level: value }));
		if (value === "server" || value === "tool") {
			setLocalConfig((prev) => ({
				...prev,
				mcp_code_mode_binding_level: value,
			}));
		}
	}, []);

	const handleToolSyncIntervalChange = useCallback((value: string) => {
		setLocalValues((prev) => ({ ...prev, mcp_tool_sync_interval: value }));
		const numValue = Number.parseInt(value);
		if (!isNaN(numValue) && numValue >= 0) {
			setLocalConfig((prev) => ({ ...prev, mcp_tool_sync_interval: numValue }));
		}
	}, []);

	const handleDisableAutoToolInjectChange = useCallback((checked: boolean) => {
		setLocalConfig((prev) => ({
			...prev,
			mcp_disable_auto_tool_inject: checked,
		}));
	}, []);

	const handleTempTokenAuthChange = useCallback((checked: boolean) => {
		setLocalConfig((prev) => ({
			...prev,
			mcp_enable_temp_token_auth: checked,
		}));
	}, []);

	const handleClientURLChange = useCallback((value: SecretVar) => {
		setLocalConfig((prev) => ({ ...prev, mcp_external_client_url: value }));
	}, []);

	const handleAuthModeChange = useCallback((value: string) => {
		if (value === "headers" || value === "both" || value === "oauth") {
			setLocalConfig((prev) => ({
				...prev,
				mcp_server_auth_mode: value,
				// disable_vk_identity is oauth-only and its toggle is hidden outside oauth
				// mode; clear it on the way out so a hidden-stale value can't be saved and
				// rejected by the backend (400) on the next save.
				oauth2_server_config: value === "oauth" ? prev.oauth2_server_config : { ...prev.oauth2_server_config, disable_vk_identity: false },
			}));
		}
	}, []);

	const handleIssuerURLChange = useCallback((value: SecretVar) => {
		setLocalConfig((prev) => ({
			...prev,
			oauth2_server_config: { ...prev.oauth2_server_config, issuer_url: value },
		}));
	}, []);

	const handleAuthCodeTTLChange = useCallback((value: string) => {
		setLocalValues((prev) => ({ ...prev, oauth2_auth_code_ttl: value }));
		const num = Number.parseInt(value);
		if (!isNaN(num) && num >= 1) {
			setLocalConfig((prev) => ({
				...prev,
				oauth2_server_config: { ...prev.oauth2_server_config, auth_code_ttl: num },
			}));
		}
	}, []);

	const handleAccessTokenTTLChange = useCallback((value: string) => {
		setLocalValues((prev) => ({ ...prev, oauth2_access_token_ttl: value }));
		const num = Number.parseInt(value);
		if (!isNaN(num) && num >= 60) {
			setLocalConfig((prev) => ({
				...prev,
				oauth2_server_config: { ...prev.oauth2_server_config, access_token_ttl: num },
			}));
		}
	}, []);

	const handleDisableVKIdentityChange = useCallback((checked: boolean) => {
		setLocalConfig((prev) => ({
			...prev,
			oauth2_server_config: { ...prev.oauth2_server_config, disable_vk_identity: checked },
		}));
	}, []);

	const handleSave = useCallback(async () => {
		try {
			const agentDepth = Number.parseInt(localValues.mcp_agent_depth);
			const toolTimeout = Number.parseInt(localValues.mcp_tool_execution_timeout);

			if (isNaN(agentDepth) || agentDepth <= 0) {
				toast.error(t("mcpSettings.toastDepthPositive"));
				return;
			}

			if (isNaN(toolTimeout) || toolTimeout <= 0) {
				toast.error(t("mcpSettings.toastTimeoutPositive"));
				return;
			}

			// The TTL fields are only shown (and only relevant) in OAuth modes; the
			// backend likewise validates oauth2_server_config only then. Guard the
			// checks so a stale value can't dead-end the save after switching back to
			// headers mode, where the fields are hidden and unfixable.
			const oauthModeActive = localConfig.mcp_server_auth_mode === "both" || localConfig.mcp_server_auth_mode === "oauth";

			const authCodeTTL = Number.parseInt(localValues.oauth2_auth_code_ttl);
			const accessTokenTTL = Number.parseInt(localValues.oauth2_access_token_ttl);

			if (oauthModeActive && (isNaN(authCodeTTL) || authCodeTTL < 1 || authCodeTTL > 900)) {
				toast.error(t("mcpSettings.toastAuthCodeTtl"));
				return;
			}

			if (oauthModeActive && (isNaN(accessTokenTTL) || accessTokenTTL < 60)) {
				toast.error(t("mcpSettings.toastAccessTokenTtl"));
				return;
			}

			if (!bifrostConfig) {
				toast.error(t("configNotLoadedRefresh"));
				return;
			}

			// The TTLs live in localValues (the text inputs) and only sync into
			// localConfig when the field is edited, so a setup that never touches them
			// (e.g. only toggling a switch or setting the issuer URL) would serialize
			// oauth2_server_config with the TTLs omitted — which the Go side unmarshals
			// back as 0. Write the validated values so the stored config always matches
			// what the form shows.
			const clientConfigToSave: CoreConfig = oauthModeActive
				? {
						...localConfig,
						oauth2_server_config: {
							...localConfig.oauth2_server_config,
							auth_code_ttl: authCodeTTL,
							access_token_ttl: accessTokenTTL,
						},
					}
				: localConfig;

			await updateCoreConfig({
				...bifrostConfig,
				client_config: clientConfigToSave,
			}).unwrap();
			toast.success(t("mcpSettings.toastSaved"));
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	}, [bifrostConfig, localConfig, localValues, updateCoreConfig, t]);

	return (
		<div className="mx-auto w-full max-w-4xl space-y-4 px-4 py-6 md:px-0" data-testid="mcp-settings-view">
			<PageTitle title={t("mcpSettings.title")}>{t("mcpSettings.description")}</PageTitle>
			<div className="space-y-4">
				{/* Max Agent Depth */}
				<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
					<div className="space-y-0.5">
						<label htmlFor="mcp-agent-depth" className="text-sm font-medium">
							{t("mcpSettings.maxAgentDepth")}
						</label>
						<p className="text-muted-foreground text-sm">{t("mcpSettings.maxDepthHelp")}</p>
					</div>
					<Input
						id="mcp-agent-depth"
						data-testid="mcp-agent-depth-input"
						type="number"
						className="w-24"
						value={localValues.mcp_agent_depth}
						onChange={(e) => handleAgentDepthChange(e.target.value)}
						min="1"
					/>
				</div>

				{/* Tool Execution Timeout */}
				<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
					<div className="space-y-0.5">
						<label htmlFor="mcp-tool-execution-timeout" className="text-sm font-medium">
							{t("mcpSettings.toolTimeout")}
						</label>
						<p className="text-muted-foreground text-sm">{t("mcpSettings.toolTimeoutHelp")}</p>
					</div>
					<Input
						id="mcp-tool-execution-timeout"
						data-testid="mcp-tool-timeout-input"
						type="number"
						className="w-24"
						value={localValues.mcp_tool_execution_timeout}
						onChange={(e) => handleToolExecutionTimeoutChange(e.target.value)}
						min="1"
					/>
				</div>

				{/* Tool Sync Interval */}
				<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
					<div className="space-y-0.5">
						<label htmlFor="mcp-tool-sync-interval" className="text-sm font-medium">
							{t("mcpSettings.toolSyncInterval")}
						</label>
						<p className="text-muted-foreground text-sm">{t("mcpSettings.refreshIntervalHelp")}</p>
					</div>
					<Input
						id="mcp-tool-sync-interval"
						data-testid="mcp-tool-sync-interval-input"
						type="number"
						className="w-24"
						value={localValues.mcp_tool_sync_interval}
						onChange={(e) => handleToolSyncIntervalChange(e.target.value)}
						min="0"
					/>
				</div>

				{/* Disable Auto Tool Injection */}
				<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
					<div className="space-y-0.5">
						<label htmlFor="mcp-disable-auto-tool-inject" className="text-sm font-medium">
							{t("mcpSettings.disableAutoToolInjection")}
						</label>
						<p className="text-muted-foreground text-sm">
							<Trans t={t} i18nKey="mcpSettings.disableAutoToolInjectionHelp" components={{ code0: <code className="text-xs" /> }} />
						</p>
					</div>
					<Switch
						id="mcp-disable-auto-tool-inject"
						checked={localConfig.mcp_disable_auto_tool_inject ?? false}
						onCheckedChange={handleDisableAutoToolInjectChange}
						disabled={!hasSettingsUpdateAccess}
						data-testid="mcp-disable-auto-tool-inject-switch"
					/>
				</div>

				{/* Temp Token Auth */}
				<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
					<div className="space-y-0.5">
						<label htmlFor="mcp-enable-temp-token-auth" className="text-sm font-medium">
							{t("mcpSettings.allowTempToken")}
						</label>
						<p className="text-muted-foreground text-sm">{t("mcpSettings.tempTokenHelp")}</p>
					</div>
					<Switch
						id="mcp-enable-temp-token-auth"
						checked={localConfig.mcp_enable_temp_token_auth ?? false}
						onCheckedChange={handleTempTokenAuthChange}
						disabled={!hasSettingsUpdateAccess}
						data-testid="mcp-enable-temp-token-auth-switch"
					/>
				</div>

				{/* Code Mode Binding Level */}
				<div className="space-y-4 rounded-sm border p-4">
					<div className="space-y-0.5">
						<label htmlFor="mcp-binding-level" className="text-sm font-medium">
							{t("mcpSettings.codeModeBinding")}
						</label>
						<p className="text-muted-foreground text-sm">{t("mcpSettings.bindingHelp")}</p>
					</div>
					<Select value={localValues.mcp_code_mode_binding_level} onValueChange={handleCodeModeBindingLevelChange}>
						<SelectTrigger id="mcp-binding-level" data-testid="mcp-binding-level" className="w-56">
							<SelectValue placeholder={t("mcpSettings.selectBindingLevel")} />
						</SelectTrigger>
						<SelectContent>
							<SelectItem value="server">{t("mcpSettings.serverLevel")}</SelectItem>
							<SelectItem value="tool">{t("mcpSettings.toolLevel")}</SelectItem>
						</SelectContent>
					</Select>

					{/* Visual Example */}
					<div className="mt-6 space-y-2">
						<p className="text-foreground text-xs font-semibold tracking-wide uppercase">{t("mcpSettings.vFSStructure")}</p>

						{localValues.mcp_code_mode_binding_level === "server" ? (
							<div className="bg-muted border-border rounded-sm border p-4">
								<div className="text-foreground space-y-1 font-mono text-xs">
									<div>servers/</div>
									<div className="pl-3">├─ calculator.py</div>
									<div className="pl-3">├─ youtube.py</div>
									<div className="pl-3">└─ weather.py</div>
								</div>
								<p className="text-muted-foreground mt-3 text-xs">{t("mcpSettings.allToolsPerServerInASinglePyFile")}</p>
							</div>
						) : (
							<div className="bg-muted border-border rounded-sm border p-4">
								<div className="text-foreground space-y-1 font-mono text-xs">
									<div>servers/</div>
									<div className="pl-3">├─ calculator/</div>
									<div className="pl-6">├─ add.py</div>
									<div className="pl-6">└─ subtract.py</div>
									<div className="pl-3">├─ youtube/</div>
									<div className="pl-6">├─ GET_CHANNELS.py</div>
									<div className="pl-6">└─ SEARCH_VIDEOS.py</div>
									<div className="pl-3">└─ weather/</div>
									<div className="pl-6">└─ get_forecast.py</div>
								</div>
								<p className="text-muted-foreground mt-3 text-xs">{t("mcpSettings.individualPyFileForEachTool")}</p>
							</div>
						)}
					</div>
				</div>
				{/* Advanced Settings — collapsed by default so people don't accidentally
				    edit the redirect_uri, which would break already-authorized MCP clients. */}
				<Accordion type="single" collapsible className="rounded-sm border px-4">
					<AccordionItem value="advanced-settings" className="border-b-0">
						<AccordionTrigger data-testid="mcp-settings-advanced-trigger">
							<span className="text-sm font-medium">{t("mcpSettings.advancedSettings")}</span>
						</AccordionTrigger>
						<AccordionContent className="space-y-2 pt-2">
							<label htmlFor="external-client-url" className="text-sm font-medium">
								{t("mcpSettings.externalClientUrl")}
							</label>
							<p className="text-muted-foreground text-sm">
								<Trans
									t={t}
									i18nKey="mcpSettings.externalClientUrlHelp"
									shouldUnescape
									components={{
										b0: <b />,
										code1: <code className="text-xs" />,
										code2: <code className="text-xs" />,
										code3: <code className="text-xs" />,
										code4: <code className="text-xs" />,
									}}
								/>
							</p>
							<SecretVarInput
								id="external-client-url"
								data-testid="mcp-external-client-url-input"
								placeholder={t("mcpSettings.externalClientUrlPlaceholder")}
								value={localConfig.mcp_external_client_url}
								onChange={handleClientURLChange}
								disabled={!hasSettingsUpdateAccess}
							/>
							<Alert variant="warning">
								<AlertTriangle className="size-4" />
								<AlertTitle>{t("mcpSettings.externalClientUrlWarning")}</AlertTitle>
								<AlertDescription>
									<p>
										<Trans
											t={t}
											i18nKey="mcpSettings.externalClientUrlWarningHelp"
											components={{ code0: <code className="text-xs" />, em1: <em /> }}
										/>
									</p>
								</AlertDescription>
							</Alert>
							{/* MCP Server Auth Mode */}
							<div className="mt-4 space-y-2 border-t pt-4">
								<label htmlFor="mcp-server-auth-mode" className="text-sm font-medium">
									{t("mcpSettings.authMode")}
								</label>
								<p className="text-muted-foreground text-sm">
									<Trans
										t={t}
										i18nKey="mcpSettings.authModeHelp"
										components={{ code0: <code className="text-xs" />, b1: <b />, b2: <b />, b3: <b /> }}
									/>
								</p>
								<Select
									value={localConfig.mcp_server_auth_mode ?? "headers"}
									onValueChange={handleAuthModeChange}
									disabled={!hasSettingsUpdateAccess}
								>
									<SelectTrigger id="mcp-server-auth-mode" data-testid="mcp-server-auth-mode-select" className="w-40">
										<SelectValue />
									</SelectTrigger>
									<SelectContent>
										<SelectItem value="headers">{t("mcpSettings.headers")}</SelectItem>
										<SelectItem value="both">{t("mcpSettings.both")}</SelectItem>
										<SelectItem value="oauth">OAuth</SelectItem>
									</SelectContent>
								</Select>
								{/* oauth: VK/header access disabled */}
								{localConfig.mcp_server_auth_mode === "oauth" && (
									<Alert variant="warning">
										<AlertTriangle className="size-4" />
										<AlertTitle>{t("mcpSettings.headerAccessDisabled")}</AlertTitle>
										<AlertDescription>{t("mcpSettings.headerAccessDisabledHelp")}</AlertDescription>
									</Alert>
								)}

								{/* headers: warn if downgrading from oauth-enabled mode */}
								{localConfig.mcp_server_auth_mode === "headers" &&
									(config?.mcp_server_auth_mode === "both" || config?.mcp_server_auth_mode === "oauth") && (
										<Alert variant="warning">
											<AlertTriangle className="size-4" />
											<AlertTitle>{t("mcpSettings.oauthDiscoveryDisabled")}</AlertTitle>
											<AlertDescription>{t("mcpSettings.oauthDiscoveryDisabledHelp")}</AlertDescription>
										</Alert>
									)}

								{/* both: informational note about additive nature */}
								{localConfig.mcp_server_auth_mode === "both" && (config?.mcp_server_auth_mode ?? "headers") !== "both" && (
									<Alert>
										<AlertDescription>{t("mcpSettings.bothAuthModesHelp")}</AlertDescription>
									</Alert>
								)}
							</div>

							{/* OAuth2 AS Settings — only shown when auth mode is not headers */}
							{(localConfig.mcp_server_auth_mode === "both" || localConfig.mcp_server_auth_mode === "oauth") && (
								<div className="mt-4 space-y-4 border-t pt-4">
									<p className="text-sm font-medium">{t("mcpSettings.oauth2Settings")}</p>

									{/* Issuer URL */}
									<div className="space-y-1.5">
										<label htmlFor="oauth2-issuer-url" className="text-sm font-medium">
											{t("mcpSettings.issuerUrl")}
										</label>
										<p className="text-muted-foreground text-sm">
											<Trans
												t={t}
												i18nKey="mcpSettings.issuerUrlHelp"
												components={{
													code0: <code className="text-xs" />,
													code1: <code className="text-xs" />,
													code2: <code className="text-xs" />,
												}}
											/>
										</p>
										<SecretVarInput
											id="oauth2-issuer-url"
											data-testid="oauth2-issuer-url-input"
											placeholder={t("mcpSettings.issuerUrlPlaceholder")}
											value={localConfig.oauth2_server_config?.issuer_url}
											onChange={handleIssuerURLChange}
											disabled={!hasSettingsUpdateAccess}
										/>
									</div>

									{/* Token TTLs */}
									<div className="flex gap-6">
										<div className="space-y-1.5">
											<label htmlFor="oauth2-auth-code-ttl" className="text-sm font-medium">
												{t("mcpSettings.authCodeTtl")}
											</label>
											<p className="text-muted-foreground text-xs">{t("mcpSettings.authCodeTtlHelp")}</p>
											<Input
												id="oauth2-auth-code-ttl"
												data-testid="oauth2-auth-code-ttl-input"
												type="number"
												className="w-28"
												min="1"
												max="900"
												value={localValues.oauth2_auth_code_ttl}
												onChange={(e) => handleAuthCodeTTLChange(e.target.value)}
												disabled={!hasSettingsUpdateAccess}
											/>
										</div>
										<div className="space-y-1.5">
											<label htmlFor="oauth2-access-token-ttl" className="text-sm font-medium">
												{t("mcpSettings.accessTokenTtl")}
											</label>
											<p className="text-muted-foreground text-xs">{t("mcpSettings.accessTokenTtlHelp")}</p>
											<Input
												id="oauth2-access-token-ttl"
												data-testid="oauth2-access-token-ttl-input"
												type="number"
												className="w-28"
												min="60"
												value={localValues.oauth2_access_token_ttl}
												onChange={(e) => handleAccessTokenTTLChange(e.target.value)}
												disabled={!hasSettingsUpdateAccess}
											/>
										</div>
									</div>

									{/* Require identity-provider login. Enterprise-only and only
                      meaningful in oauth mode with an IdP configured. Stays visible
                      when the setting is already enabled so it is never silently lost. */}
									{localConfig.mcp_server_auth_mode === "oauth" &&
										(idpConfigured || localConfig.oauth2_server_config?.disable_vk_identity) && (
											<div className="mt-4 space-y-2 border-t pt-4">
												<div className="flex items-center justify-between space-x-2">
													<div className="space-y-0.5">
														<label htmlFor="oauth2-disable-vk-identity" className="text-sm font-medium">
															{t("mcpSettings.requireIdpLogin")}
														</label>
														<p className="text-muted-foreground text-sm">{t("mcpSettings.requireIdpLoginHelp")}</p>
													</div>
													<Switch
														id="oauth2-disable-vk-identity"
														data-testid="oauth2-disable-vk-identity-switch"
														size="md"
														checked={localConfig.oauth2_server_config?.disable_vk_identity ?? false}
														onCheckedChange={handleDisableVKIdentityChange}
														disabled={!hasSettingsUpdateAccess}
													/>
												</div>
												{localConfig.oauth2_server_config?.disable_vk_identity && (
													<Alert variant="warning">
														<AlertTriangle className="size-4" />
														<AlertTitle>{t("mcpSettings.virtualKeyOauthDisabled")}</AlertTitle>
														<AlertDescription>{t("mcpSettings.virtualKeyOauthDisabledHelp")}</AlertDescription>
													</Alert>
												)}
											</div>
										)}
								</div>
							)}
						</AccordionContent>
					</AccordionItem>
				</Accordion>
			</div>
			<div className="flex justify-end pt-2">
				<Button onClick={handleSave} disabled={!hasChanges || isLoading || !hasSettingsUpdateAccess} data-testid="mcp-settings-save-btn">
					{isLoading ? t("saving") : t("saveChanges")}
				</Button>
			</div>
		</div>
	);
}