import { FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { Switch } from "@/components/ui/switch";
import type { MCPClientCredentialsConfig } from "@/lib/types/mcp";
import { isSecretVarSet } from "@/lib/types/schemas";
import { type Control, useWatch } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { type UpstreamClientAuthNames, UpstreamClientAuthFields } from "./tokenExchangeFields";

const clientCredentialsAuthNames: UpstreamClientAuthNames = {
	mode: "client_credentials.auth_mode",
	secret: "client_credentials.client_secret",
	method: "client_credentials.auth_method",
	key: "client_credentials.private_key",
	keyId: "client_credentials.key_id",
	alg: "client_credentials.signing_algorithm",
	audience: "client_credentials.assertion_audience",
};

/**
 * clientCredentialsPayload returns the client_credentials block to send: only
 * the chosen authentication method's fields, and scopes parsed from the
 * comma-separated text. The backend replaces the block whole, so the edit sheet
 * sends this in full whenever any field changed.
 */
export function clientCredentialsPayload(cc?: Partial<MCPClientCredentialsConfig>): MCPClientCredentialsConfig {
	const usesKey = isSecretVarSet(cc?.private_key);
	const usesSecret = !usesKey && isSecretVarSet(cc?.client_secret);
	const scopes = (cc?.scopes_text ?? "")
		.split(",")
		.map((s) => s.trim())
		.filter(Boolean);
	return {
		token_url: cc?.token_url?.trim() || undefined,
		client_id: cc?.client_id,
		client_secret: usesSecret ? cc?.client_secret : undefined,
		auth_method: usesSecret ? cc?.auth_method || undefined : undefined,
		private_key: usesKey ? cc?.private_key : undefined,
		key_id: usesKey ? cc?.key_id?.trim() || undefined : undefined,
		signing_algorithm: usesKey ? cc?.signing_algorithm || undefined : undefined,
		assertion_audience: usesKey ? cc?.assertion_audience?.trim() || undefined : undefined,
		scopes: scopes.length > 0 ? scopes : undefined,
		// The server tells "" (send no resource) from absent (send the server URL).
		resource: cc?.omit_resource ? "" : cc?.resource?.trim() || undefined,
	};
}

/** clientCredentialsFormValues seeds the edit sheet from a stored, masked block. */
export function clientCredentialsFormValues(cc?: MCPClientCredentialsConfig): Partial<MCPClientCredentialsConfig> {
	return {
		token_url: cc?.token_url,
		client_id: cc?.client_id,
		client_secret: cc?.client_secret,
		auth_method: cc?.auth_method ?? "",
		private_key: cc?.private_key,
		key_id: cc?.key_id,
		signing_algorithm: cc?.signing_algorithm,
		assertion_audience: cc?.assertion_audience,
		scopes_text: (cc?.scopes ?? []).join(", "),
		resource: cc?.resource,
		omit_resource: cc?.resource === "",
		auth_mode: isSecretVarSet(cc?.private_key) ? "private_key" : "secret",
	};
}

interface ClientCredentialsFieldsProps {
	// Shared by the create form and the edit sheet, whose form types differ but
	// both store the block under client_credentials.*.
	control: Control<any>;
	disabled?: boolean;
	/** SecretVarInput masking: the edit sheet shows stored values masked. */
	maskNonEnvValue?: boolean;
	redactNonEnvValue?: boolean;
}

/** Client ID, secret or private key, token URL, scopes and resource of a client_credentials server. */
export function ClientCredentialsFields({ control, disabled, maskNonEnvValue, redactNonEnvValue }: ClientCredentialsFieldsProps) {
	const { t } = useTranslation("mcp");
	const omitResource = useWatch({ control, name: "client_credentials.omit_resource" }) === true;
	return (
		<>
			<FormField
				control={control}
				name="client_credentials.client_id"
				render={({ field }) => (
					<FormItem className="flex flex-col gap-2">
						<FormLabel>
							{t("registry.clientCredentials.clientId")} <span className="text-destructive">*</span>
						</FormLabel>
						<FormControl>
							<SecretVarInput
								data-testid="client-credentials-client-id-input"
								placeholder={t("registry.clientCredentials.clientIdPlaceholder")}
								disabled={disabled}
								redactNonEnvValue={false}
								value={field.value}
								onChange={field.onChange}
							/>
						</FormControl>
						<FormMessage />
					</FormItem>
				)}
			/>
			<UpstreamClientAuthFields
				control={control}
				names={clientCredentialsAuthNames}
				testIdPrefix="client-credentials"
				labels={{
					mode: t("registry.clientCredentials.authenticateWith"),
					secretOption: t("registry.clientCredentials.secretOption"),
					secret: t("registry.clientCredentials.clientSecret"),
					secretPlaceholder: t("registry.clientCredentials.clientSecretPlaceholder"),
					privateKeyHelp: t("registry.clientCredentials.privateKeyHelp"),
				}}
				autoAlgorithm
				disabled={disabled}
				maskNonEnvValue={maskNonEnvValue}
				redactNonEnvValue={redactNonEnvValue}
			/>
			<FormField
				control={control}
				name="client_credentials.token_url"
				render={({ field }) => (
					<FormItem className="flex flex-col gap-2">
						<FormLabel>{t("registry.clientCredentials.tokenUrl")}</FormLabel>
						<FormControl>
							<Input
								{...field}
								value={field.value ?? ""}
								disabled={disabled}
								placeholder={t("registry.clientCredentials.tokenUrlPlaceholder")}
								data-testid="client-credentials-token-url-input"
							/>
						</FormControl>
						<p className="text-muted-foreground text-xs">{t("registry.clientCredentials.tokenUrlHelp")}</p>
						<FormMessage />
					</FormItem>
				)}
			/>
			<FormField
				control={control}
				name="client_credentials.scopes_text"
				render={({ field }) => (
					<FormItem className="flex flex-col gap-2">
						<FormLabel>{t("registry.form.scopesOptional")}</FormLabel>
						<FormControl>
							<Input
								{...field}
								value={field.value ?? ""}
								disabled={disabled}
								placeholder={t("registry.clientCredentials.scopesPlaceholder")}
								data-testid="client-credentials-scopes-input"
							/>
						</FormControl>
						<p className="text-muted-foreground text-xs">{t("registry.oauthFields.commaSeparated")}</p>
						<FormMessage />
					</FormItem>
				)}
			/>
			<FormField
				control={control}
				name="client_credentials.resource"
				render={({ field }) => (
					<FormItem className="flex flex-col gap-2">
						<FormLabel>{t("registry.tokenExchange.resourceOptional")}</FormLabel>
						<FormControl>
							<Input
								{...field}
								value={field.value ?? ""}
								disabled={disabled || omitResource}
								placeholder={t("registry.clientCredentials.resourcePlaceholder")}
								data-testid="client-credentials-resource-input"
							/>
						</FormControl>
						<FormMessage />
					</FormItem>
				)}
			/>
			<FormField
				control={control}
				name="client_credentials.omit_resource"
				render={({ field }) => (
					<FormItem className="flex flex-row items-center justify-between gap-4">
						<div className="space-y-0.5">
							<FormLabel>{t("registry.clientCredentials.omitResource")}</FormLabel>
							<p className="text-muted-foreground text-xs">{t("registry.clientCredentials.omitResourceHelp")}</p>
						</div>
						<FormControl>
							<Switch
								data-testid="client-credentials-omit-resource-switch"
								checked={field.value === true}
								onCheckedChange={field.onChange}
								disabled={disabled}
							/>
						</FormControl>
					</FormItem>
				)}
			/>
		</>
	);
}