import PageTitle from "@/components/pageTitle";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { getErrorMessage, useGetCoreConfigQuery, useUpdateCoreConfigMutation } from "@/lib/store";
import { DefaultCoreConfig } from "@/lib/types/config";
import { SecretVar } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";

// The managed-settings editor holds pretty-printed JSON; "" means no managed policy.
const formatManagedSettings = (settings?: Record<string, unknown>) => (settings ? JSON.stringify(settings, null, 2) : "");

const secretVarEquals = (a?: SecretVar, b?: SecretVar) =>
	(a?.value ?? "") === (b?.value ?? "") && (a?.ref ?? "") === (b?.ref ?? "") && (a?.type ?? "plain_text") === (b?.type ?? "plain_text");

const isIssuerSet = (issuer?: SecretVar) => !!(issuer?.value?.trim() || issuer?.ref?.trim());

// Claude Code is pointed at {issuer}/claude-code. Only a plain-text issuer can be
// shown; an env reference resolves on the server.
const gatewayURL = (issuer?: SecretVar) => {
	const value = issuer?.type === "env" ? "" : (issuer?.value ?? "").trim();
	return value ? `${value.replace(/\/+$/, "")}/claude-code` : "<issuer URL>/claude-code";
};

export default function ClaudeCodeView() {
	const hasSettingsUpdateAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	const { data: bifrostConfig } = useGetCoreConfigQuery({ fromDB: true });
	const config = bifrostConfig?.client_config;
	const [updateCoreConfig, { isLoading }] = useUpdateCoreConfigMutation();

	const [enabled, setEnabled] = useState(false);
	const [issuerURL, setIssuerURL] = useState<SecretVar | undefined>(undefined);
	const [managedSettings, setManagedSettings] = useState("");

	useEffect(() => {
		if (!config) return;
		setEnabled(config.oauth2_server_config?.claude_code_gateway?.enabled ?? false);
		setIssuerURL(config.oauth2_server_config?.issuer_url);
		setManagedSettings(formatManagedSettings(config.oauth2_server_config?.claude_code_gateway?.managed_settings));
	}, [config]);

	const hasChanges = useMemo(() => {
		if (!config) return false;
		const stored = config.oauth2_server_config;
		return (
			enabled !== (stored?.claude_code_gateway?.enabled ?? false) ||
			!secretVarEquals(issuerURL, stored?.issuer_url) ||
			managedSettings !== formatManagedSettings(stored?.claude_code_gateway?.managed_settings)
		);
	}, [config, enabled, issuerURL, managedSettings]);

	const url = gatewayURL(issuerURL);
	const mdmSnippet = JSON.stringify({ forceLoginMethod: "gateway", forceLoginGatewayUrl: url }, null, 2);

	const handleSave = useCallback(async () => {
		if (!bifrostConfig) {
			toast.error("Configuration not loaded. Please refresh and try again.");
			return;
		}
		if (enabled && !isIssuerSet(issuerURL)) {
			toast.error("Set the issuer URL before enabling Claude Code sign-in.");
			return;
		}
		// Managed settings are edited as JSON text and parsed only here, so a
		// half-typed document is never saved.
		let parsedSettings: Record<string, unknown> | undefined;
		const text = managedSettings.trim();
		if (text) {
			let parsed: unknown;
			try {
				parsed = JSON.parse(text);
			} catch {
				toast.error("Managed settings must be valid JSON.");
				return;
			}
			if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
				toast.error("Managed settings must be a JSON object.");
				return;
			}
			parsedSettings = parsed as Record<string, unknown>;
		}

		const clientConfig = bifrostConfig.client_config ?? DefaultCoreConfig;
		try {
			await updateCoreConfig({
				...bifrostConfig,
				client_config: {
					...clientConfig,
					oauth2_server_config: {
						...clientConfig.oauth2_server_config,
						issuer_url: issuerURL,
						claude_code_gateway: { enabled, managed_settings: parsedSettings },
					},
				},
			}).unwrap();
			toast.success("Claude Code settings updated successfully.");
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	}, [bifrostConfig, enabled, issuerURL, managedSettings, updateCoreConfig]);

	return (
		<div className="mx-auto w-full max-w-4xl space-y-6" data-testid="claude-code-view">
			<PageTitle title="Claude Code">
				Let developers sign in to Bifrost from Claude Code with <code className="text-xs">/login</code> instead of configuring a virtual key
				by hand.
			</PageTitle>

			<div className="space-y-4">
				<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
					<div className="space-y-0.5">
						<label htmlFor="claude-code-gateway-enabled" className="text-sm font-medium">
							Enable Claude Code sign-in
						</label>
						<p className="text-muted-foreground text-sm">
							Developers choose <b>Cloud gateway</b> in <code className="text-xs">/login</code>, confirm a code in the browser, and pick an
							identity: a virtual key, their account, or an anonymous session when inference auth is not enforced. Requests then run under
							that identity with its budgets, rate limits and model access. Sign-ins appear under OAuth grants, where they can be revoked.
						</p>
					</div>
					<Switch
						id="claude-code-gateway-enabled"
						data-testid="claude-code-gateway-switch"
						size="md"
						checked={enabled}
						onCheckedChange={setEnabled}
						disabled={!hasSettingsUpdateAccess}
					/>
				</div>

				<div className="space-y-1.5 rounded-sm border p-4">
					<label htmlFor="claude-code-issuer-url" className="text-sm font-medium">
						Issuer URL
					</label>
					<p className="text-muted-foreground text-sm">
						Bifrost's stable public URL. Claude Code signs in at <code className="text-xs">{"<issuer URL>/claude-code"}</code>. This is the
						same issuer the MCP OAuth server uses, so changing it also affects MCP clients signed in with OAuth. Supports env var syntax
						(e.g. <code className="text-xs">env.BIFROST_ISSUER_URL</code>).
					</p>
					<SecretVarInput
						id="claude-code-issuer-url"
						data-testid="claude-code-issuer-url-input"
						placeholder="https://bifrost.example.com or env.BIFROST_ISSUER_URL"
						value={issuerURL}
						onChange={setIssuerURL}
						disabled={!hasSettingsUpdateAccess}
					/>
				</div>

				{enabled && (
					<div className="space-y-3 rounded-sm border p-4" data-testid="claude-code-connect-instructions">
						<p className="text-sm font-medium">Connect Claude Code</p>
						<p className="text-muted-foreground text-sm">
							In Claude Code, run <code className="text-xs">/login</code>, choose <b>Cloud gateway</b> and enter:
						</p>
						<code className="bg-muted block rounded-sm px-3 py-2 text-xs" data-testid="claude-code-gateway-url">
							{url}
						</code>
						<p className="text-muted-foreground text-sm">
							To preset it on managed machines, add these keys to Claude Code&apos;s managed settings file:
						</p>
						<pre className="bg-muted overflow-x-auto rounded-sm px-3 py-2 text-xs">{mdmSnippet}</pre>
						<Alert>
							<AlertDescription>
								Claude Code requires https (http only on localhost) and a gateway host that resolves to a private network address.
							</AlertDescription>
						</Alert>
					</div>
				)}

				<div className="space-y-1.5 rounded-sm border p-4">
					<label htmlFor="claude-code-managed-settings" className="text-sm font-medium">
						Managed settings (JSON)
					</label>
					<p className="text-muted-foreground text-sm">
						Optional Claude Code <code className="text-xs">managed-settings.json</code> pushed to every signed-in client (permissions, env,
						model allowlist and so on). Clients refresh it hourly. Leave empty for no managed policy.
					</p>
					<Textarea
						id="claude-code-managed-settings"
						data-testid="claude-code-managed-settings-input"
						className="min-h-40 font-mono text-xs"
						placeholder={'{\n  "env": { "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1" }\n}'}
						value={managedSettings}
						onChange={(e) => setManagedSettings(e.target.value)}
						disabled={!hasSettingsUpdateAccess}
					/>
				</div>
			</div>

			<div className="flex justify-end pt-2">
				<Button onClick={handleSave} disabled={!hasChanges || isLoading || !hasSettingsUpdateAccess} data-testid="claude-code-save-btn">
					{isLoading ? "Saving..." : "Save Changes"}
				</Button>
			</div>
		</div>
	);
}