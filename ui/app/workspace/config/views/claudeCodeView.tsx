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
import { Trans, useTranslation } from "react-i18next";
import { toast } from "sonner";

// The managed-settings editor holds pretty-printed JSON; "" means no managed policy.
const formatManagedSettings = (settings?: Record<string, unknown>) => (settings ? JSON.stringify(settings, null, 2) : "");

const secretVarEquals = (a?: SecretVar, b?: SecretVar) =>
	(a?.value ?? "") === (b?.value ?? "") && (a?.ref ?? "") === (b?.ref ?? "") && (a?.type ?? "plain_text") === (b?.type ?? "plain_text");

const isIssuerSet = (issuer?: SecretVar) => !!(issuer?.value?.trim() || issuer?.ref?.trim());

// Claude Code is pointed at {issuer}/gateway/claude-code. Only a plain-text issuer can be
// shown; env and vault references resolve on the server, so the caller shows the
// translated "<issuer URL>/gateway/claude-code" template instead (fallback).
const gatewayURL = (issuer: SecretVar | undefined, fallback: string) => {
	const value = issuer?.type === "env" || issuer?.type === "vault" ? "" : (issuer?.value ?? "").trim();
	return value ? `${value.replace(/\/+$/, "")}/gateway/claude-code` : fallback;
};

export default function ClaudeCodeView() {
	const { t } = useTranslation("config");
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

	const urlTemplate = t("claudeCode.gatewayURLTemplate");
	const url = gatewayURL(issuerURL, urlTemplate);
	const mdmSnippet = JSON.stringify({ forceLoginMethod: "gateway", forceLoginGatewayUrl: url }, null, 2);

	const handleSave = useCallback(async () => {
		if (!bifrostConfig) {
			toast.error(t("configNotLoadedRefresh"));
			return;
		}
		if (enabled && !isIssuerSet(issuerURL)) {
			toast.error(t("claudeCode.issuerRequired"));
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
				toast.error(t("claudeCode.invalidJSON"));
				return;
			}
			if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
				toast.error(t("claudeCode.notObject"));
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
			toast.success(t("claudeCode.updated"));
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	}, [bifrostConfig, enabled, issuerURL, managedSettings, updateCoreConfig, t]);

	return (
		<div className="mx-auto w-full max-w-4xl space-y-6" data-testid="claude-code-view">
			<PageTitle title={t("claudeCode.title")}>
				<Trans t={t} i18nKey="claudeCode.description" components={{ code0: <code className="text-xs" /> }} />
			</PageTitle>

			<div className="space-y-4">
				<div className="flex items-center justify-between space-x-2 rounded-sm border p-4">
					<div className="space-y-0.5">
						<label htmlFor="claude-code-gateway-enabled" className="text-sm font-medium">
							{t("claudeCode.enableLabel")}
						</label>
						<p className="text-muted-foreground text-sm">
							<Trans t={t} i18nKey="claudeCode.enableDescription" components={{ b0: <b />, code1: <code className="text-xs" /> }} />
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
						{t("claudeCode.issuerLabel")}
					</label>
					<p className="text-muted-foreground text-sm">
						<Trans
							t={t}
							i18nKey="claudeCode.issuerDescription"
							components={{ code0: <code className="text-xs">{urlTemplate}</code>, code1: <code className="text-xs" /> }}
						/>
					</p>
					<SecretVarInput
						id="claude-code-issuer-url"
						data-testid="claude-code-issuer-url-input"
						placeholder={t("claudeCode.issuerPlaceholder")}
						value={issuerURL}
						onChange={setIssuerURL}
						disabled={!hasSettingsUpdateAccess}
					/>
				</div>

				{enabled && (
					<div className="space-y-3 rounded-sm border p-4" data-testid="claude-code-connect-instructions">
						<p className="text-sm font-medium">{t("claudeCode.connectTitle")}</p>
						<p className="text-muted-foreground text-sm">{t("claudeCode.gatewayURLLabel")}</p>
						<code className="bg-muted block rounded-sm px-3 py-2 text-xs" data-testid="claude-code-gateway-url">
							{url}
						</code>
						<p className="text-muted-foreground text-sm">
							<Trans
								t={t}
								i18nKey="claudeCode.connectBody"
								components={{ code0: <code className="text-xs" />, code1: <code className="text-xs" />, b2: <b /> }}
							/>
						</p>
						<pre className="bg-muted overflow-x-auto rounded-sm px-3 py-2 text-xs">{mdmSnippet}</pre>
						<Alert>
							<AlertDescription>{t("claudeCode.requirements")}</AlertDescription>
						</Alert>
					</div>
				)}

				<div className="space-y-1.5 rounded-sm border p-4">
					<label htmlFor="claude-code-managed-settings" className="text-sm font-medium">
						{t("claudeCode.managedSettingsLabel")}
					</label>
					<p className="text-muted-foreground text-sm">
						<Trans t={t} i18nKey="claudeCode.managedSettingsDescription" components={{ code0: <code className="text-xs" /> }} />
					</p>
					<Textarea
						id="claude-code-managed-settings"
						data-testid="claude-code-managed-settings-input"
						className="min-h-40 font-mono text-xs"
						// i18n-ignore: a JSON example, not prose
						placeholder={'{\n  "env": { "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1" }\n}'}
						value={managedSettings}
						onChange={(e) => setManagedSettings(e.target.value)}
						disabled={!hasSettingsUpdateAccess}
					/>
				</div>
			</div>

			<div className="flex justify-end pt-2">
				<Button onClick={handleSave} disabled={!hasChanges || isLoading || !hasSettingsUpdateAccess} data-testid="claude-code-save-btn">
					{isLoading ? t("saving") : t("saveChanges")}
				</Button>
			</div>
		</div>
	);
}