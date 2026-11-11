import { HeadersTable } from "@/components/ui/headersTable";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import type { AgentAuthType, AgentOAuthConfig, AgentTLSConfig, AgentUpstreamAuth } from "@/lib/types/agents";
import type { SecretVar } from "@/lib/types/schemas";
import type { ParseKeys, TFunction } from "i18next";
import { useTranslation } from "react-i18next";

export const emptySecret = (): SecretVar => ({ value: "", ref: "" });
export const emptyAuth = (): AgentUpstreamAuth => ({ type: "none", headers: {} });

const AUTH_TYPE_LABEL_KEYS: Record<AgentAuthType, ParseKeys<"observability">> = {
	none: "agentGateway.authFields.typeNone",
	headers: "agentGateway.authFields.typeHeaders",
	oauth: "agentGateway.authFields.typeOauth",
};

export function hasTLS(tls?: AgentTLSConfig): boolean {
	return !!(
		tls?.insecure_skip_verify ||
		tls?.ca_cert_pem?.value ||
		tls?.ca_cert_pem?.ref ||
		tls?.client_cert_pem?.value ||
		tls?.client_cert_pem?.ref ||
		tls?.client_key_pem?.value ||
		tls?.client_key_pem?.ref
	);
}

export function normalizeAuth(auth: AgentUpstreamAuth): AgentUpstreamAuth | undefined {
	const tls = hasTLS(auth.tls) ? auth.tls : undefined;
	if (auth.type === "none" && !tls) return undefined;
	return {
		type: auth.type,
		headers: Object.keys(auth.headers ?? {}).length ? auth.headers : undefined,
		oauth: auth.type === "oauth" ? auth.oauth : undefined,
		security_schemes: auth.security_schemes?.length ? auth.security_schemes : undefined,
		advanced: auth.advanced || undefined,
		tls,
	};
}

// One-line summary for collapsed auth sections, e.g. "Headers · custom TLS".
export function authSummary(auth: AgentUpstreamAuth, t: TFunction<"observability">): string {
	const parts: string[] = [t(AUTH_TYPE_LABEL_KEYS[auth.type])];
	if (auth.tls?.insecure_skip_verify) parts.push(t("agentGateway.authFields.tlsVerificationOff"));
	else if (hasTLS(auth.tls)) parts.push(t("agentGateway.authFields.customTls"));
	return parts.join(" · ");
}

export function TLSFields({ value, onChange }: { value: AgentTLSConfig; onChange: (next: AgentTLSConfig) => void }) {
	const { t } = useTranslation("observability");
	return (
		<div className="space-y-4">
			<div className="flex items-center justify-between gap-4">
				<div className="space-y-0.5">
					<Label>{t("agentGateway.authFields.skipTls")}</Label>
					<p className="text-muted-foreground text-xs">{t("agentGateway.authFields.skipTlsHelp")}</p>
				</div>
				<Switch
					checked={value.insecure_skip_verify ?? false}
					onCheckedChange={(insecure_skip_verify) => onChange({ ...value, insecure_skip_verify })}
				/>
			</div>
			<div className="flex flex-col gap-2">
				<Label>{t("agentGateway.authFields.caCert")}</Label>
				<SecretVarInput
					variant="textarea"
					rows={3}
					placeholder="-----BEGIN CERTIFICATE-----"
					value={value.ca_cert_pem}
					onChange={(ca_cert_pem) => onChange({ ...value, ca_cert_pem })}
				/>
			</div>
			<div className="grid grid-cols-1 gap-3 md:grid-cols-2">
				<div className="flex flex-col gap-2">
					<Label>{t("agentGateway.authFields.clientCert")}</Label>
					<SecretVarInput
						variant="textarea"
						rows={3}
						placeholder="-----BEGIN CERTIFICATE-----"
						value={value.client_cert_pem}
						onChange={(client_cert_pem) => onChange({ ...value, client_cert_pem })}
					/>
				</div>
				<div className="flex flex-col gap-2">
					<Label>{t("agentGateway.authFields.clientKey")}</Label>
					<SecretVarInput
						variant="textarea"
						rows={3}
						// i18n-ignore: PEM header literal
						placeholder="-----BEGIN PRIVATE KEY-----"
						value={value.client_key_pem}
						onChange={(client_key_pem) => onChange({ ...value, client_key_pem })}
					/>
				</div>
			</div>
		</div>
	);
}

function OAuthFields({ value, onChange }: { value: AgentOAuthConfig; onChange: (next: AgentOAuthConfig) => void }) {
	const { t } = useTranslation("observability");
	return (
		<div className="space-y-4">
			<div className="flex flex-col gap-2">
				<Label>{t("agentGateway.authFields.tokenUrl")}</Label>
				<Input
					value={value.token_url ?? ""}
					placeholder="https://auth.example.com/oauth/token"
					onChange={(event) => onChange({ ...value, token_url: event.target.value })}
				/>
			</div>
			<div className="grid grid-cols-1 gap-3 md:grid-cols-2">
				<div className="flex flex-col gap-2">
					<Label>{t("agentGateway.authFields.clientId")}</Label>
					<SecretVarInput
						value={value.client_id}
						placeholder={t("agentGateway.authFields.clientIdPlaceholder")}
						onChange={(client_id) => onChange({ ...value, client_id })}
					/>
				</div>
				<div className="flex flex-col gap-2">
					<Label>{t("agentGateway.authFields.clientSecret")}</Label>
					<SecretVarInput
						value={value.client_secret}
						placeholder={t("agentGateway.authFields.clientSecretPlaceholder")}
						onChange={(client_secret) => onChange({ ...value, client_secret })}
					/>
				</div>
			</div>
			<div className="grid grid-cols-1 gap-3 md:grid-cols-2">
				<div className="flex flex-col gap-2">
					<Label>{t("agentGateway.authFields.scopes")}</Label>
					<Input
						value={value.scopes?.join(" ") ?? ""}
						placeholder={t("agentGateway.authFields.scopesPlaceholder")}
						onChange={(event) => onChange({ ...value, scopes: event.target.value.split(/\s+/).filter(Boolean) })}
					/>
				</div>
				<div className="flex flex-col gap-2">
					<Label>{t("agentGateway.authFields.resource")}</Label>
					<Input
						value={value.resource ?? ""}
						placeholder="https://agent.example.com"
						onChange={(event) => onChange({ ...value, resource: event.target.value })}
					/>
				</div>
			</div>
			<div className="grid grid-cols-1 gap-3 md:grid-cols-2">
				<div className="flex flex-col gap-2">
					<Label className="text-muted-foreground">{t("agentGateway.authFields.providerUrl")}</Label>
					<Input
						value={value.provider_url ?? ""}
						placeholder="https://auth.example.com"
						onChange={(event) => onChange({ ...value, provider_url: event.target.value })}
					/>
				</div>
				<div className="flex flex-col gap-2">
					<Label className="text-muted-foreground">{t("agentGateway.authFields.discoveryUrl")}</Label>
					<Input
						value={value.discovery_url ?? ""}
						placeholder="https://auth.example.com/.well-known/openid-configuration"
						onChange={(event) => onChange({ ...value, discovery_url: event.target.value })}
					/>
				</div>
			</div>
		</div>
	);
}

export function AuthFields({
	value,
	onChange,
	readOnlyAuth = false,
	disabled = false,
	requiredHeaderKeys = [],
}: {
	value: AgentUpstreamAuth;
	onChange: (next: AgentUpstreamAuth) => void;
	readOnlyAuth?: boolean;
	disabled?: boolean;
	requiredHeaderKeys?: string[];
}) {
	const { t } = useTranslation("observability");
	const changeKind = (type: AgentAuthType) =>
		onChange({
			...emptyAuth(),
			type,
			oauth: type === "oauth" ? (value.oauth ?? { client_id: emptySecret(), client_secret: emptySecret() }) : undefined,
			tls: value.tls,
			advanced: value.advanced,
		});

	return (
		<fieldset disabled={disabled} className="space-y-4 disabled:opacity-70">
			<div className="flex flex-col gap-2">
				<Label>{t("agentGateway.authFields.type")}</Label>
				<Select value={value.type} onValueChange={changeKind} disabled={readOnlyAuth}>
					<SelectTrigger className="w-full">
						<SelectValue placeholder={t("agentGateway.authFields.typePlaceholder")} />
					</SelectTrigger>
					<SelectContent>
						<SelectItem value="none">{t(AUTH_TYPE_LABEL_KEYS.none)}</SelectItem>
						<SelectItem value="headers">{t(AUTH_TYPE_LABEL_KEYS.headers)}</SelectItem>
						<SelectItem value="oauth">{t("agentGateway.authFields.typeOauthClientCredentials")}</SelectItem>
					</SelectContent>
				</Select>
			</div>
			{value.type === "headers" && (
				<div className="space-y-4">
					{requiredHeaderKeys.length > 0 && (
						<HeadersTable
							value={value.headers ?? {}}
							onChange={(headers) => onChange({ ...value, headers })}
							fixedKeys={requiredHeaderKeys}
							useSecretVarInput
							label={t("agentGateway.authFields.requiredByCard")}
						/>
					)}
					<HeadersTable
						value={Object.fromEntries(Object.entries(value.headers ?? {}).filter(([name]) => !requiredHeaderKeys.includes(name)))}
						onChange={(headers) =>
							onChange({
								...value,
								headers: {
									...Object.fromEntries(Object.entries(value.headers ?? {}).filter(([name]) => requiredHeaderKeys.includes(name))),
									...headers,
								},
							})
						}
						useSecretVarInput
						label={t("agentGateway.authFields.extraHeaders")}
					/>
				</div>
			)}
			{value.type === "oauth" && (
				<div className="space-y-4">
					<OAuthFields value={value.oauth ?? {}} onChange={(oauth) => onChange({ ...value, oauth })} />
					<HeadersTable
						value={value.headers ?? {}}
						onChange={(headers) => onChange({ ...value, headers })}
						useSecretVarInput
						label={t("agentGateway.authFields.extraHeaders")}
					/>
				</div>
			)}
		</fieldset>
	);
}