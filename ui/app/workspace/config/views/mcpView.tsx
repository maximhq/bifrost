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
import { toast } from "sonner";

const secretVarEquals = (a?: SecretVar, b?: SecretVar) =>
	(a?.value ?? "") === (b?.value ?? "") && (a?.ref ?? "") === (b?.ref ?? "") && (a?.type ?? "plain_text") === (b?.type ?? "plain_text");

export default function MCPView() {
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
        oauth2_auth_code_ttl: (
          config?.oauth2_server_config?.auth_code_ttl || 300
        ).toString(),
        oauth2_access_token_ttl: (
          config?.oauth2_server_config?.access_token_ttl || 600
        ).toString(),
			});
		}
	}, [config, bifrostConfig]);

	const hasChanges = useMemo(() => {
		if (!config) return false;
		const clientURLChanged = !secretVarEquals(localConfig.mcp_external_client_url, config.mcp_external_client_url);
    const issuerURLChanged = !secretVarEquals(
      localConfig.oauth2_server_config?.issuer_url,
      config.oauth2_server_config?.issuer_url,
    );
		return (
			localConfig.mcp_agent_depth !== config.mcp_agent_depth ||
			localConfig.mcp_tool_execution_timeout !== config.mcp_tool_execution_timeout ||
			localConfig.mcp_code_mode_binding_level !== (config.mcp_code_mode_binding_level || "server") ||
			localConfig.mcp_tool_sync_interval !== (config.mcp_tool_sync_interval ?? 10) ||
			localConfig.mcp_disable_auto_tool_inject !== (config.mcp_disable_auto_tool_inject ?? false) ||
			localConfig.mcp_enable_temp_token_auth !== (config.mcp_enable_temp_token_auth ?? false) ||
			clientURLChanged ||
      (localConfig.mcp_server_auth_mode ?? "headers") !==
        (config.mcp_server_auth_mode ?? "headers") ||
      issuerURLChanged ||
      (localConfig.oauth2_server_config?.auth_code_ttl ?? 300) !==
        (config.oauth2_server_config?.auth_code_ttl ?? 300) ||
      (localConfig.oauth2_server_config?.access_token_ttl ?? 600) !==
        (config.oauth2_server_config?.access_token_ttl ?? 600) ||
      (localConfig.oauth2_server_config?.disable_vk_identity ?? false) !==
        (config.oauth2_server_config?.disable_vk_identity ?? false)
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
        oauth2_server_config:
          value === "oauth"
            ? prev.oauth2_server_config
            : { ...prev.oauth2_server_config, disable_vk_identity: false },
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
				toast.error("Max agent depth must be a positive number.");
				return;
			}

			if (isNaN(toolTimeout) || toolTimeout <= 0) {
				toast.error("Tool execution timeout must be a positive number.");
				return;
			}

      // The TTL fields are only shown (and only relevant) in OAuth modes; the
      // backend likewise validates oauth2_server_config only then. Guard the
      // checks so a stale value can't dead-end the save after switching back to
      // headers mode, where the fields are hidden and unfixable.
      const oauthModeActive =
        localConfig.mcp_server_auth_mode === "both" ||
        localConfig.mcp_server_auth_mode === "oauth";

      const authCodeTTL = Number.parseInt(localValues.oauth2_auth_code_ttl);
      const accessTokenTTL = Number.parseInt(
        localValues.oauth2_access_token_ttl,
      );

      if (
        oauthModeActive &&
        (isNaN(authCodeTTL) || authCodeTTL < 1 || authCodeTTL > 900)
      ) {
        toast.error(
          "Authorization code TTL must be between 1 and 900 seconds (15 minutes).",
        );
        return;
      }

      if (oauthModeActive && (isNaN(accessTokenTTL) || accessTokenTTL < 60)) {
        toast.error("Access token TTL must be at least 60 seconds.");
        return;
      }

			if (!bifrostConfig) {
				toast.error("Configuration not loaded. Please refresh and try again.");
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
			toast.success("MCP settings updated successfully.");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	}, [bifrostConfig, localConfig, localValues, updateCoreConfig]);

	return (
		<div className="mx-auto w-full max-w-7xl space-y-4" data-testid="mcp-settings-view">
			<div>
				<h2 className="text-lg font-semibold tracking-tight">MCP 设置</h2>
				<p className="text-muted-foreground text-sm">配置 MCP（模型上下文协议）代理和工具设置。</p>
			</div>
			<div className="space-y-4">
				{/* Max Agent Depth */}
				<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
					<div className="space-y-0.5">
						<label htmlFor="mcp-agent-depth" className="text-sm font-medium">最大代理深度</label>
						<p className="text-muted-foreground text-sm">MCP 代理执行的最大深度。</p>
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
						<label htmlFor="mcp-tool-execution-timeout" className="text-sm font-medium">工具执行超时（秒）</label>
						<p className="text-muted-foreground text-sm">工具执行的最大秒数。</p>
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
						<label htmlFor="mcp-tool-sync-interval" className="text-sm font-medium">工具同步间隔（分钟）</label>
						<p className="text-muted-foreground text-sm">从 MCP 服务器刷新工具列表的频率。设为 0 可禁用。</p>
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
						<label htmlFor="mcp-disable-auto-tool-inject" className="text-sm font-medium">禁用自动工具注入</label>
						<p className="text-muted-foreground text-sm">
							When enabled, MCP tools are not automatically included in every request. Tools are only injected when explicitly specified via
							request headers (<code className="text-xs">x-bf-mcp-include-tools</code>) and still must be allowed by the virtual key MCP
							configuration.
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
						<label htmlFor="mcp-enable-temp-token-auth" className="text-sm font-medium">允许临时 Token 认证链接</label>
						<p className="text-muted-foreground text-sm">
							When enabled, per-user MCP OAuth links can include a short-lived scoped token so someone without an active Bifrost dashboard
							session can complete the flow. Keep disabled to require normal dashboard authentication.
						</p>
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
						<label htmlFor="mcp-binding-level" className="text-sm font-medium">Code Mode 绑定级别</label>
						<p className="text-muted-foreground text-sm">工具在 VFS 中的暴露方式：服务器级（每个服务器的所有工具）或工具级（单个工具）。</p>
					</div>
					<Select value={localValues.mcp_code_mode_binding_level} onValueChange={handleCodeModeBindingLevelChange}>
						<SelectTrigger id="mcp-binding-level" data-testid="mcp-binding-level" className="w-56">
							<SelectValue placeholder="选择绑定级别" />
						</SelectTrigger>
						<SelectContent>
							<SelectItem value="server">服务器级</SelectItem>
							<SelectItem value="tool">工具级</SelectItem>
						</SelectContent>
					</Select>

					{/* Visual Example */}
					<div className="mt-6 space-y-2">
						<p className="text-foreground text-xs font-semibold tracking-wide uppercase">VFS 结构：</p>

						{localValues.mcp_code_mode_binding_level === "server" ? (
							<div className="bg-muted border-border rounded-sm border p-4">
								<div className="text-foreground space-y-1 font-mono text-xs">
									<div>servers/</div>
									<div className="pl-3">├─ calculator.py</div>
									<div className="pl-3">├─ youtube.py</div>
									<div className="pl-3">└─ weather.py</div>
								</div>
								<p className="text-muted-foreground mt-3 text-xs">每个服务器的所有工具放在一个 .py 文件中</p>
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
								<p className="text-muted-foreground mt-3 text-xs">每个工具一个独立的 .py 文件</p>
							</div>
						)}
					</div>
				</div>
				{/* Advanced Settings — collapsed by default so people don't accidentally
				    edit the redirect_uri, which would break already-authorized MCP clients. */}
				<Accordion type="single" collapsible className="rounded-sm border px-4">
					<AccordionItem value="advanced-settings" className="border-b-0">
						<AccordionTrigger data-testid="mcp-settings-advanced-trigger">
							<span className="text-sm font-medium">高级设置</span>
						</AccordionTrigger>
						<AccordionContent className="space-y-2 pt-2">
							<label htmlFor="external-client-url" className="text-sm font-medium">外部客户端 URL</label>
							<p className="text-muted-foreground text-sm">Bifrost 在反向代理后面运行时，覆盖其公共基础 URL。<b>留空则自动推导 URL</b> from the
								incoming <code className="text-xs">主机</code> header. Used as the <code className="text-xs">redirect_uri</code> Bifrost
								registers with upstream OAuth providers when it acts as a client to an MCP server (e.g. Notion or Jira redirect the browser
								to <code className="text-xs">{"<URL>/api/oauth/callback"}</code> after login). Supports env var syntax (e.g.{" "}
								<code className="text-xs">env.BIFROST_EXTERNAL_URL</code>).
							</p>
							<SecretVarInput
								id="external-client-url"
								data-testid="mcp-external-client-url-input"
								placeholder="https://bifrost.example.com or env.BIFROST_OAUTH_REDIRECT_URL"
								value={localConfig.mcp_external_client_url}
								onChange={handleClientURLChange}
								disabled={!hasSettingsUpdateAccess}
							/>
							<Alert variant="warning">
								<AlertTriangle className="size-4" />
								<AlertTitle>更改此 URL 可能导致现有 MCP 客户端无法使用</AlertTitle>
								<AlertDescription>
									<p>
										Upstream OAuth providers lock the <code className="text-xs">redirect_uri</code> to whatever was registered initially, so
										MCP clients that already completed OAuth will fail with <em>“无效的重定向 URI”</em>. To recover, clear the
										stored OAuth client credentials for the affected MCP servers and re-authorize so Bifrost re-runs Dynamic Client
										Registration with the new URL.
									</p>
								</AlertDescription>
							</Alert>
              {/* MCP Server Auth Mode */}
              <div className="mt-4 space-y-2 border-t pt-4">
                <label
                  htmlFor="mcp-server-auth-mode"
                  className="text-sm font-medium"
                >MCP 服务器认证模式</label>
                <p className="text-muted-foreground text-sm">
                  Controls how inbound MCP clients (e.g. Claude Code, Cursor)
                  authenticate to the <code className="text-xs">/mcp</code>{" "}
                  endpoint.{" "}
                  <b>headers</b> (default) - VK / api-key / session headers
                  only, OAuth discovery disabled.{" "}
                  <b>both</b> - accepts header credentials and Bifrost-issued
                  JWTs; existing integrations are unaffected.{" "}
                  <b>oauth</b>- 仅 JWT；虚拟密钥和请求头访问被禁用。</p>
                <Select
                  value={localConfig.mcp_server_auth_mode ?? "headers"}
                  onValueChange={handleAuthModeChange}
                  disabled={!hasSettingsUpdateAccess}
                >
                  <SelectTrigger
                    id="mcp-server-auth-mode"
                    data-testid="mcp-server-auth-mode-select"
                    className="w-40"
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="headers">请求头</SelectItem>
                    <SelectItem value="both">两者</SelectItem>
                    <SelectItem value="oauth">OAuth</SelectItem>
                  </SelectContent>
                </Select>
                {/* oauth: VK/header access disabled */}
                {localConfig.mcp_server_auth_mode === "oauth" && (
                  <Alert variant="warning">
                    <AlertTriangle className="size-4" />
                    <AlertTitle>虚拟密钥 / 请求头 MCP 访问将被禁用</AlertTitle>
                    <AlertDescription>
                      All existing MCP integrations that use a virtual key,
                      api-key, or session header will stop working immediately.
                      Clients must re-authenticate via the OAuth consent flow to
                      obtain a JWT before they can connect.
                    </AlertDescription>
                  </Alert>
                )}

                {/* headers: warn if downgrading from oauth-enabled mode */}
                {localConfig.mcp_server_auth_mode === "headers" &&
                  (config?.mcp_server_auth_mode === "both" ||
                    config?.mcp_server_auth_mode === "oauth") && (
                  <Alert variant="warning">
                    <AlertTriangle className="size-4" />
                    <AlertTitle>OAuth 发现将被禁用</AlertTitle>
                    <AlertDescription>
                      All MCP clients that authenticated via the OAuth consent
                      flow will lose access; their JWTs will be rejected and
                      their refresh tokens will become unusable. They will need
                      to reconfigure using a virtual key or api-key header.
                    </AlertDescription>
                  </Alert>
                )}

                {/* both: informational note about additive nature */}
                {localConfig.mcp_server_auth_mode === "both" &&
                  (config?.mcp_server_auth_mode ?? "headers") !== "both" && (
                  <Alert>
                    <AlertDescription>
                      Existing VK / header integrations continue to work
                      unchanged. New MCP clients can connect via OAuth - they'll
                      be redirected to the consent page to pick an identity.
                    </AlertDescription>
                  </Alert>
                )}
              </div>

              {/* OAuth2 AS Settings — only shown when auth mode is not headers */}
              {(localConfig.mcp_server_auth_mode === "both" ||
                localConfig.mcp_server_auth_mode === "oauth") && (
                <div className="mt-4 space-y-4 border-t pt-4">
                  <p className="text-sm font-medium">OAuth2 服务器设置</p>

                  {/* Issuer URL */}
                  <div className="space-y-1.5">
                    <label
                      htmlFor="oauth2-issuer-url"
                      className="text-sm font-medium"
                    >签发者 URL</label>
                    <p className="text-muted-foreground text-sm">
                      Stable public URL advertised in discovery documents and
                      embedded as the <code className="text-xs">iss</code> claim
                      in every JWT. Leave blank to derive it from the request{" "}
                      <code className="text-xs">主机</code> header (sufficient
                      for most deployments). Multi-host or reverse-proxy
                      deployments might need this. Supports env var syntax (e.g.{" "}
                      <code className="text-xs">env.BIFROST_ISSUER_URL</code>).
                    </p>
                    <SecretVarInput
                      id="oauth2-issuer-url"
                      data-testid="oauth2-issuer-url-input"
                      placeholder="https://bifrost.example.com or env.BIFROST_ISSUER_URL"
                      value={localConfig.oauth2_server_config?.issuer_url}
                      onChange={handleIssuerURLChange}
                      disabled={!hasSettingsUpdateAccess}
                    />
                  </div>

                  {/* Token TTLs */}
                  <div className="flex gap-6">
                    <div className="space-y-1.5">
                      <label
                        htmlFor="oauth2-auth-code-ttl"
                        className="text-sm font-medium"
                      >授权码 TTL（秒）</label>
                      <p className="text-muted-foreground text-xs">
                        How long the one-time code is valid after the consent
                        page redirects back to the MCP client (default: 300, max
                        900 = 15 min).
                      </p>
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
                      <label
                        htmlFor="oauth2-access-token-ttl"
                        className="text-sm font-medium"
                      >访问令牌 TTL（秒）</label>
                      <p className="text-muted-foreground text-xs">
                        Lifetime of issued JWT Bearer tokens. Clients silently
                        refresh when expired (default: 600 = 10 min). Also bounds
                        how long a revoked grant keeps working before it is cut off.
                      </p>
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
                    (idpConfigured ||
                      localConfig.oauth2_server_config?.disable_vk_identity) && (
                    <div className="mt-4 space-y-2 border-t pt-4">
                      <div className="flex items-center justify-between space-x-2">
                        <div className="space-y-0.5">
                          <label
                            htmlFor="oauth2-disable-vk-identity"
                            className="text-sm font-medium"
                          >要求身份提供商登录</label>
                          <p className="text-muted-foreground text-sm">
                            When enabled, the OAuth consent flow only offers
                            identity-provider login. Virtual keys can no longer be
                            used to obtain an MCP token, and existing virtual-key
                            OAuth sessions lose access immediately. Anonymous session
                            access is unaffected (controlled by Enforce Authentication
                            on Inference).
                          </p>
                        </div>
                        <Switch
                          id="oauth2-disable-vk-identity"
                          data-testid="oauth2-disable-vk-identity-switch"
                          size="md"
                          checked={
                            localConfig.oauth2_server_config?.disable_vk_identity ?? false
                          }
                          onCheckedChange={handleDisableVKIdentityChange}
                          disabled={!hasSettingsUpdateAccess}
                        />
                      </div>
                      {localConfig.oauth2_server_config?.disable_vk_identity && (
                        <Alert variant="warning">
                          <AlertTriangle className="size-4" />
                          <AlertTitle>通过 OAuth 的虚拟密钥 MCP 访问将停止</AlertTitle>
                          <AlertDescription>
                            MCP clients that authenticated with a virtual key will
                            lose access immediately and must sign in through your
                            identity provider to reconnect.
                          </AlertDescription>
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
					{isLoading ? "Saving..." : "保存更改"}
				</Button>
			</div>
		</div>
	);
}