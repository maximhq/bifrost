import {
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
} from "@/components/ui/accordion";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { EnvVarInput } from "@/components/ui/envVarInput";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import {
  getErrorMessage,
  useGetCoreConfigQuery,
  useUpdateCoreConfigMutation,
} from "@/lib/store";
import { CoreConfig, DefaultCoreConfig } from "@/lib/types/config";
import { EnvVar } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { AlertTriangle } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";

const envVarEquals = (a?: EnvVar, b?: EnvVar) =>
  (a?.value ?? "") === (b?.value ?? "") &&
  (a?.env_var ?? "") === (b?.env_var ?? "") &&
  (a?.from_env ?? false) === (b?.from_env ?? false);

export default function MCPView() {
  const hasSettingsUpdateAccess = useRbac(
    RbacResource.Settings,
    RbacOperation.Update,
  );
  const { data: bifrostConfig } = useGetCoreConfigQuery({ fromDB: true });
  const config = bifrostConfig?.client_config;
  const [updateCoreConfig, { isLoading }] = useUpdateCoreConfigMutation();
  const [localConfig, setLocalConfig] = useState<CoreConfig>(DefaultCoreConfig);

  const [localValues, setLocalValues] = useState<{
    mcp_agent_depth: string;
    mcp_tool_execution_timeout: string;
    mcp_code_mode_binding_level: string;
    mcp_tool_sync_interval: string;
  }>({
    mcp_agent_depth: "10",
    mcp_tool_execution_timeout: "30",
    mcp_code_mode_binding_level: "server",
    mcp_tool_sync_interval: "10",
  });

  useEffect(() => {
    if (bifrostConfig && config) {
      setLocalConfig(config);
      setLocalValues({
        mcp_agent_depth: config?.mcp_agent_depth?.toString() || "10",
        mcp_tool_execution_timeout:
          config?.mcp_tool_execution_timeout?.toString() || "30",
        mcp_code_mode_binding_level:
          config?.mcp_code_mode_binding_level || "server",
        mcp_tool_sync_interval:
          config?.mcp_tool_sync_interval?.toString() || "10",
      });
    }
  }, [config, bifrostConfig]);

  const hasChanges = useMemo(() => {
    if (!config) return false;
    const clientURLChanged = !envVarEquals(
      localConfig.mcp_external_client_url,
      config.mcp_external_client_url,
    );
    return (
      localConfig.mcp_agent_depth !== config.mcp_agent_depth ||
      localConfig.mcp_tool_execution_timeout !==
        config.mcp_tool_execution_timeout ||
      localConfig.mcp_code_mode_binding_level !==
        (config.mcp_code_mode_binding_level || "server") ||
      localConfig.mcp_tool_sync_interval !==
        (config.mcp_tool_sync_interval ?? 10) ||
      localConfig.mcp_disable_auto_tool_inject !==
        (config.mcp_disable_auto_tool_inject ?? false) ||
      localConfig.mcp_enable_temp_token_auth !==
        (config.mcp_enable_temp_token_auth ?? false) ||
      clientURLChanged
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

  const handleClientURLChange = useCallback((value: EnvVar) => {
    setLocalConfig((prev) => ({ ...prev, mcp_external_client_url: value }));
  }, []);

  const handleSave = useCallback(async () => {
    try {
      const agentDepth = Number.parseInt(localValues.mcp_agent_depth);
      const toolTimeout = Number.parseInt(
        localValues.mcp_tool_execution_timeout,
      );

      if (isNaN(agentDepth) || agentDepth <= 0) {
        toast.error("Max agent depth must be a positive number.");
        return;
      }

      if (isNaN(toolTimeout) || toolTimeout <= 0) {
        toast.error("Tool execution timeout must be a positive number.");
        return;
      }

      if (!bifrostConfig) {
        toast.error("Configuration not loaded. Please refresh and try again.");
        return;
      }
      await updateCoreConfig({
        ...bifrostConfig,
        client_config: localConfig,
      }).unwrap();
      toast.success("MCP settings updated successfully.");
    } catch (error) {
      toast.error(getErrorMessage(error));
    }
  }, [bifrostConfig, localConfig, localValues, updateCoreConfig]);

  return (
    <div
      className="mx-auto w-full max-w-7xl space-y-4"
      data-testid="mcp-settings-view"
    >
      <div>
        <h2 className="text-lg font-semibold tracking-tight">MCP Settings</h2>
        <p className="text-muted-foreground text-sm">
          Configure MCP (Model Context Protocol) agent and tool settings.
        </p>
      </div>
      <div className="space-y-4">
        {/* Max Agent Depth */}
        <div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
          <div className="space-y-0.5">
            <label htmlFor="mcp-agent-depth" className="text-sm font-medium">
              Max Agent Depth
            </label>
            <p className="text-muted-foreground text-sm">
              Maximum depth for MCP agent execution.
            </p>
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
            <label
              htmlFor="mcp-tool-execution-timeout"
              className="text-sm font-medium"
            >
              Tool Execution Timeout (seconds)
            </label>
            <p className="text-muted-foreground text-sm">
              Maximum time in seconds for tool execution.
            </p>
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
            <label
              htmlFor="mcp-tool-sync-interval"
              className="text-sm font-medium"
            >
              Tool Sync Interval (minutes)
            </label>
            <p className="text-muted-foreground text-sm">
              How often to refresh tool lists from MCP servers. Set to 0 to
              disable.
            </p>
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
            <label
              htmlFor="mcp-disable-auto-tool-inject"
              className="text-sm font-medium"
            >
              Disable Auto Tool Injection
            </label>
            <p className="text-muted-foreground text-sm">
              When enabled, MCP tools are not automatically included in every
              request. Tools are only injected when explicitly specified via
              request headers (
              <code className="text-xs">x-bf-mcp-include-tools</code>) and still
              must be allowed by the virtual key MCP configuration.
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
            <label
              htmlFor="mcp-enable-temp-token-auth"
              className="text-sm font-medium"
            >
              Allow Temp Token Auth Links
            </label>
            <p className="text-muted-foreground text-sm">
              When enabled, per-user MCP OAuth links can include a short-lived
              scoped token so someone without an active Bifrost dashboard
              session can complete the flow. Keep disabled to require normal
              dashboard authentication.
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
            <label htmlFor="mcp-binding-level" className="text-sm font-medium">
              Code Mode Binding Level
            </label>
            <p className="text-muted-foreground text-sm">
              How tools are exposed in the VFS: server-level (all tools per
              server) or tool-level (individual tools).
            </p>
          </div>
          <Select
            value={localValues.mcp_code_mode_binding_level}
            onValueChange={handleCodeModeBindingLevelChange}
          >
            <SelectTrigger
              id="mcp-binding-level"
              data-testid="mcp-binding-level"
              className="w-56"
            >
              <SelectValue placeholder="Select binding level" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="server">Server-Level</SelectItem>
              <SelectItem value="tool">Tool-Level</SelectItem>
            </SelectContent>
          </Select>

          {/* Visual Example */}
          <div className="mt-6 space-y-2">
            <p className="text-foreground text-xs font-semibold tracking-wide uppercase">
              VFS Structure:
            </p>

            {localValues.mcp_code_mode_binding_level === "server" ? (
              <div className="bg-muted border-border rounded-sm border p-4">
                <div className="text-foreground space-y-1 font-mono text-xs">
                  <div>servers/</div>
                  <div className="pl-3">├─ calculator.py</div>
                  <div className="pl-3">├─ youtube.py</div>
                  <div className="pl-3">└─ weather.py</div>
                </div>
                <p className="text-muted-foreground mt-3 text-xs">
                  All tools per server in a single .py file
                </p>
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
                <p className="text-muted-foreground mt-3 text-xs">
                  Individual .py file for each tool
                </p>
              </div>
            )}
          </div>
        </div>
        {/* Advanced Settings — collapsed by default so people don't accidentally
				    edit the redirect_uri, which would break already-authorized MCP clients. */}
        <Accordion type="single" collapsible className="rounded-sm border px-4">
          <AccordionItem value="advanced-settings" className="border-b-0">
            <AccordionTrigger data-testid="mcp-settings-advanced-trigger">
              <span className="text-sm font-medium">Advanced Settings</span>
            </AccordionTrigger>
            <AccordionContent className="space-y-2 pt-2">
              <label
                htmlFor="external-client-url"
                className="text-sm font-medium"
              >
                External Client URL
              </label>
              <p className="text-muted-foreground text-sm">
                Override Bifrost's public base URL when it runs behind a reverse
                proxy. <b>Leave blank to derive the URL</b> from the incoming{" "}
                <code className="text-xs">Host</code> header. Used as the{" "}
                <code className="text-xs">redirect_uri</code> Bifrost registers
                with upstream OAuth providers when it acts as a client to an MCP
                server (e.g. Notion or Jira redirect the browser to{" "}
                <code className="text-xs">{"<URL>/api/oauth/callback"}</code>{" "}
                after login). Supports env var syntax (e.g.{" "}
                <code className="text-xs">env.BIFROST_EXTERNAL_URL</code>).
              </p>
              <EnvVarInput
                id="external-client-url"
                data-testid="mcp-external-client-url-input"
                placeholder="https://bifrost.example.com or env.BIFROST_OAUTH_REDIRECT_URL"
                value={localConfig.mcp_external_client_url}
                onChange={handleClientURLChange}
                disabled={!hasSettingsUpdateAccess}
              />
              <Alert variant="warning">
                <AlertTriangle className="size-4" />
                <AlertTitle>
                  Changing this URL can break existing MCP clients
                </AlertTitle>
                <AlertDescription>
                  <p>
                    Upstream OAuth providers lock the{" "}
                    <code className="text-xs">redirect_uri</code> to whatever
                    was registered initially, so MCP clients that already
                    completed OAuth will fail with{" "}
                    <em>&quot;Invalid redirect URI&quot;</em>. To recover, clear
                    the stored OAuth client credentials for the affected MCP
                    servers and re-authorize so Bifrost re-runs Dynamic Client
                    Registration with the new URL.
                  </p>
                </AlertDescription>
              </Alert>
            </AccordionContent>
          </AccordionItem>
        </Accordion>
      </div>
      <div className="flex justify-end pt-2">
        <Button
          onClick={handleSave}
          disabled={!hasChanges || isLoading || !hasSettingsUpdateAccess}
          data-testid="mcp-settings-save-btn"
        >
          {isLoading ? "Saving..." : "Save Changes"}
        </Button>
      </div>
    </div>
  );
}
