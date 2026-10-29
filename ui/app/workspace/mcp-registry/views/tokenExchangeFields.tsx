import { Button } from "@/components/ui/button";
import { FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { IS_ENTERPRISE } from "@/lib/constants/config";
import { OAuthSigningAlgorithms } from "@/lib/types/config";
import type { MCPResourceAuthMode, MCPTokenExchangeConfig } from "@/lib/types/mcp";
import { isSecretVarSet, type SecretVar } from "@/lib/types/schemas";
import { Info, TriangleAlert } from "lucide-react";
import type { Control } from "react-hook-form";
import { useFormContext, useWatch } from "react-hook-form";

/**
 * tokenExchangeProfilePayload returns the Cross App Access (id_jag) fields of a
 * token_exchange create/update payload. The backend replaces the block
 * wholesale, so every form that submits token_exchange must spread this in or
 * an id_jag client would silently fall back to the direct profile.
 */
export function tokenExchangeProfilePayload(te?: Partial<MCPTokenExchangeConfig>): Partial<MCPTokenExchangeConfig> {
	if (te?.profile !== "id_jag") return {};
	const usesKey = isSecretVarSet(te.resource_private_key);
	const usesSecret = !usesKey && isSecretVarSet(te.resource_client_secret);
	return {
		profile: "id_jag",
		resource: te.resource?.trim() || undefined,
		resource_token_endpoint: te.resource_token_endpoint?.trim() || undefined,
		resource_client_id: te.resource_client_id,
		resource_client_secret: usesSecret ? te.resource_client_secret : undefined,
		resource_client_auth_method: usesSecret ? te.resource_client_auth_method || undefined : undefined,
		resource_private_key: usesKey ? te.resource_private_key : undefined,
		resource_key_id: usesKey ? te.resource_key_id?.trim() || undefined : undefined,
		resource_signing_algorithm: usesKey ? te.resource_signing_algorithm || undefined : undefined,
		resource_assertion_audience: usesKey ? te.resource_assertion_audience?.trim() || undefined : undefined,
	};
}

/**
 * exchangesAsSSOApplication mirrors MCPTokenExchangeConfig.ExchangesAsSSOApplication:
 * id_jag always exchanges as the SSO login application, so no exchange app is sent.
 */
export function exchangesAsSSOApplication(te?: Partial<MCPTokenExchangeConfig>): boolean {
	return !!te?.use_idp_credentials || te?.profile === "id_jag";
}

/** tokenExchangeProfileFormValues seeds the id_jag fields of a form from a stored config. */
export function tokenExchangeProfileFormValues(te?: Partial<MCPTokenExchangeConfig>): Partial<MCPTokenExchangeConfig> {
	return {
		profile: te?.profile ?? "",
		resource: te?.resource,
		resource_token_endpoint: te?.resource_token_endpoint,
		resource_client_id: te?.resource_client_id,
		resource_client_secret: te?.resource_client_secret,
		resource_client_auth_method: te?.resource_client_auth_method ?? "",
		resource_private_key: te?.resource_private_key,
		resource_key_id: te?.resource_key_id,
		resource_signing_algorithm: te?.resource_signing_algorithm,
		resource_assertion_audience: te?.resource_assertion_audience,
		resource_auth_mode: isSecretVarSet(te?.resource_private_key) ? "private_key" : "secret",
	};
}

interface TokenExchangeScopesFieldProps {
	variant: "input" | "textarea";
	value: string;
	onChange: (value: string) => void;
	label: React.ReactNode;
	helperText?: React.ReactNode;
	testId: string;
	disabled?: boolean;
}

interface TokenExchangeFieldsProps {
	// Loosely typed on purpose: shared between the create form (CreateMCPClientRequest)
	// and the edit sheet (MCPClientUpdateSchema), which use identical token_exchange.*
	// field names but different surrounding form types.
	control: Control<any>;
	disabled?: boolean;
	/** Prose / dirty-state warning banners rendered above the field grid. */
	beforeFields?: React.ReactNode;

	audienceLabel: React.ReactNode;
	audienceTooltip?: React.ReactNode;
	audienceTestId: string;
	onAudienceTouched?: () => void;

	/** Dedicated-app vs. identity-provider-app exchange source picker. Omit useIdPCredentialsLabel to hide it entirely (dedicated app is then the only option). */
	useIdPCredentialsLabel?: React.ReactNode;
	/** Short one-liner shown below the picker for the currently selected option — swaps as the user toggles, so the consequence of each choice is visible without both descriptions competing for attention at once. */
	useIdPCredentialsDedicatedDescription?: React.ReactNode;
	useIdPCredentialsIdPDescription?: React.ReactNode;
	/** Shown as a warning under the picker when "Dedicated application" is selected but the connected identity provider requires "Identity provider application" (currently Microsoft Entra ID). Omit when the provider has no such requirement. */
	useIdPCredentialsRequiredWarning?: React.ReactNode;
	useIdPCredentialsTestId?: string;
	onUseIdPCredentialsToggled?: (checked: boolean) => void;

	clientIdLabel: React.ReactNode;
	clientIdTooltip?: React.ReactNode;
	clientIdPlaceholder: string;
	clientIdTestId: string;
	onClientIdTouched?: () => void;
	/** SecretVarInput masking mode for the exchange client ID; create and edit differ here. */
	clientIdRedactNonEnvValue?: boolean;

	clientSecretLabel: React.ReactNode;
	clientSecretPlaceholder: string;
	clientSecretHelperText?: React.ReactNode;
	clientSecretTestId: string;
	/** SecretVarInput masking mode for the exchange client secret; create and edit differ here. */
	clientSecretHideValueWhenEnv?: boolean;
	clientSecretMaskNonEnvValue?: boolean;
	clientSecretRedactNonEnvValue?: boolean;

	authServerUrlLabel: React.ReactNode;
	authServerUrlTooltip?: React.ReactNode;
	authServerUrlHelperText?: React.ReactNode;
	authServerUrlTestId: string;

	scopes: TokenExchangeScopesFieldProps;

	/** Wrap the whole field set (e.g. in a grid) — defaults to a 2-column grid like the edit sheet. */
	gridClassName?: string;
}

/** Audience/Exchange Client ID/Exchange Client Secret/Authorization Server URL/Scopes fields, shared by the MCP create form and edit sheet. */
export function TokenExchangeFields({
	control,
	disabled,
	beforeFields,
	audienceLabel,
	audienceTooltip,
	audienceTestId,
	onAudienceTouched,
	useIdPCredentialsLabel,
	useIdPCredentialsDedicatedDescription,
	useIdPCredentialsIdPDescription,
	useIdPCredentialsRequiredWarning,
	useIdPCredentialsTestId = "token-exchange-use-idp-credentials-picker",
	onUseIdPCredentialsToggled,
	clientIdLabel,
	clientIdTooltip,
	clientIdPlaceholder,
	clientIdTestId,
	onClientIdTouched,
	clientIdRedactNonEnvValue = true,
	clientSecretLabel,
	clientSecretPlaceholder,
	clientSecretHelperText,
	clientSecretTestId,
	clientSecretHideValueWhenEnv = true,
	clientSecretMaskNonEnvValue = false,
	clientSecretRedactNonEnvValue = true,
	authServerUrlLabel,
	authServerUrlTooltip,
	authServerUrlHelperText,
	authServerUrlTestId,
	scopes,
	gridClassName = "grid grid-cols-1 gap-4 md:grid-cols-2",
}: TokenExchangeFieldsProps) {
	const useIdPCredentials = useWatch({ control, name: "token_exchange.use_idp_credentials" });
	const profile = useWatch({ control, name: "token_exchange.profile" });
	const isIDJAG = profile === "id_jag";
	// id_jag always exchanges as the SSO login application, so it has no exchange app to pick.
	const credentialFieldsDisabled = disabled || !!useIdPCredentials || isIDJAG;
	const fullRow = gridClassName.includes("grid-cols") ? "md:col-span-2" : "";

	return (
		<>
			{beforeFields}
			<div className={gridClassName}>
				{/* Cross App Access is performed by Bifrost Enterprise; other builds refuse the profile. */}
				{(IS_ENTERPRISE || isIDJAG) && (
					<FormField
						control={control}
						name="token_exchange.profile"
						render={({ field }) => (
							<FormItem className={`flex flex-col gap-2 ${fullRow}`}>
								<FormLabel>How the upstream token is obtained</FormLabel>
								<FormControl>
									<div
										className="bg-muted inline-flex w-fit gap-0.5 rounded-md p-0.5"
										role="radiogroup"
										aria-label="Token exchange profile"
									>
										{(
											[
												["", "Identity provider issues it"],
												["id_jag", "Cross App Access (ID-JAG)"],
											] as const
										).map(([value, label]) => {
											const selected = (field.value ?? "") === value;
											return (
												<Button
													key={value || "direct"}
													type="button"
													size="sm"
													variant="ghost"
													className={
														selected
															? "bg-background dark:bg-input/30 text-foreground hover:bg-background dark:hover:bg-input/30 shadow-sm"
															: "text-muted-foreground hover:text-foreground"
													}
													disabled={disabled}
													onClick={() => field.onChange(value)}
													role="radio"
													aria-checked={selected}
													data-testid={`token-exchange-profile-${value || "direct"}`}
												>
													{label}
												</Button>
											);
										})}
									</div>
								</FormControl>
								<p className="text-muted-foreground text-xs">
									{isIDJAG
										? "Cross App Access (ID-JAG), for example Okta XAA: the identity provider issues an identity assertion (ID-JAG) for the upstream authorization server named in Audience, and that server exchanges it for its own token. Users must have signed in to Bifrost through the identity provider. Bifrost asks as your SSO login application, the only app the identity provider accepts the user's sign-in token from."
										: "The identity provider exchanges the caller's token for one scoped to the audience. Use this for internal servers that trust your identity provider."}
								</p>
							</FormItem>
						)}
					/>
				)}
				{useIdPCredentialsLabel && !isIDJAG && (
					<FormField
						control={control}
						name="token_exchange.use_idp_credentials"
						render={({ field }) => {
							const checked = !!field.value;
							const select = (value: boolean) => {
								field.onChange(value);
								onUseIdPCredentialsToggled?.(value);
							};
							return (
								<FormItem className={`flex flex-col gap-2 ${gridClassName.includes("grid-cols") ? "md:col-span-2" : ""}`}>
									<FormLabel>{useIdPCredentialsLabel}</FormLabel>
									<FormControl>
										<div
											className="bg-muted inline-flex w-fit gap-0.5 rounded-md p-0.5"
											role="radiogroup"
											aria-label={String(useIdPCredentialsLabel)}
										>
											<Button
												type="button"
												size="sm"
												variant="ghost"
												className={
													checked
														? "text-muted-foreground hover:text-foreground"
														: "bg-background dark:bg-input/30 text-foreground hover:bg-background dark:hover:bg-input/30 shadow-sm"
												}
												disabled={disabled}
												onClick={() => select(false)}
												role="radio"
												aria-checked={!checked}
												data-testid={`${useIdPCredentialsTestId}-dedicated`}
											>
												Dedicated application
											</Button>
											<Button
												type="button"
												size="sm"
												variant="ghost"
												className={
													checked
														? "bg-background dark:bg-input/30 text-foreground hover:bg-background dark:hover:bg-input/30 shadow-sm"
														: "text-muted-foreground hover:text-foreground"
												}
												disabled={disabled}
												onClick={() => select(true)}
												role="radio"
												aria-checked={checked}
												data-testid={`${useIdPCredentialsTestId}-idp`}
											>
												Identity provider application
											</Button>
										</div>
									</FormControl>
									{(checked ? useIdPCredentialsIdPDescription : useIdPCredentialsDedicatedDescription) && (
										<p className="text-muted-foreground text-xs">
											{checked ? useIdPCredentialsIdPDescription : useIdPCredentialsDedicatedDescription}
										</p>
									)}
									{!checked && useIdPCredentialsRequiredWarning && (
										<p className="flex items-start gap-1.5 text-xs text-amber-700 dark:text-amber-400">
											<TriangleAlert className="mt-0.5 size-3.5 shrink-0" />
											<span>{useIdPCredentialsRequiredWarning}</span>
										</p>
									)}
								</FormItem>
							);
						}}
					/>
				)}
				{!credentialFieldsDisabled && (
					<>
						<FormField
							control={control}
							name="token_exchange.client_id"
							render={({ field }) => (
								<FormItem className="flex flex-col gap-2">
									<div className="flex items-center gap-2">
										<FormLabel>{clientIdLabel}</FormLabel>
										{clientIdTooltip && (
											<TooltipProvider>
												<Tooltip>
													<TooltipTrigger asChild>
														<Info className="text-muted-foreground h-4 w-4 cursor-help" />
													</TooltipTrigger>
													<TooltipContent className="max-w-xs">{clientIdTooltip}</TooltipContent>
												</Tooltip>
											</TooltipProvider>
										)}
									</div>
									<FormControl>
										<SecretVarInput
											data-testid={clientIdTestId}
											placeholder={clientIdPlaceholder}
											disabled={disabled}
											redactNonEnvValue={clientIdRedactNonEnvValue}
											value={field.value}
											onChange={(value) => {
												field.onChange(value);
												onClientIdTouched?.();
											}}
										/>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
						<FormField
							control={control}
							name="token_exchange.client_secret"
							render={({ field }) => (
								<FormItem className="flex flex-col gap-2">
									<FormLabel>{clientSecretLabel}</FormLabel>
									<FormControl>
										<SecretVarInput
											data-testid={clientSecretTestId}
											placeholder={clientSecretPlaceholder}
											disabled={disabled}
											hideValueWhenEnv={clientSecretHideValueWhenEnv}
											maskNonEnvValue={clientSecretMaskNonEnvValue}
											redactNonEnvValue={clientSecretRedactNonEnvValue}
											value={field.value}
											onChange={field.onChange}
										/>
									</FormControl>
									{clientSecretHelperText && <p className="text-muted-foreground text-xs">{clientSecretHelperText}</p>}
									<FormMessage />
								</FormItem>
							)}
						/>
					</>
				)}
				<FormField
					control={control}
					name="token_exchange.audience"
					render={({ field }) => (
						<FormItem className="flex flex-col gap-2">
							<div className="flex items-center gap-2">
								<FormLabel>{audienceLabel}</FormLabel>
								{audienceTooltip && (
									<TooltipProvider>
										<Tooltip>
											<TooltipTrigger asChild>
												<Info className="text-muted-foreground h-4 w-4 cursor-help" />
											</TooltipTrigger>
											<TooltipContent className="max-w-xs">{audienceTooltip}</TooltipContent>
										</Tooltip>
									</TooltipProvider>
								)}
							</div>
							<FormControl>
								<Input
									{...field}
									value={field.value ?? ""}
									disabled={disabled}
									onChange={(e) => {
										field.onChange(e);
										onAudienceTouched?.();
									}}
									placeholder="api://my-mcp-server"
									data-testid={audienceTestId}
								/>
							</FormControl>
							<FormMessage />
						</FormItem>
					)}
				/>
				<FormField
					control={control}
					name="token_exchange.authorization_server_url"
					render={({ field }) => (
						<FormItem className="flex flex-col gap-2">
							<div className="flex items-center gap-2">
								<FormLabel>{authServerUrlLabel}</FormLabel>
								{authServerUrlTooltip && (
									<TooltipProvider>
										<Tooltip>
											<TooltipTrigger asChild>
												<Info className="text-muted-foreground h-4 w-4 cursor-help" />
											</TooltipTrigger>
											<TooltipContent className="max-w-xs">{authServerUrlTooltip}</TooltipContent>
										</Tooltip>
									</TooltipProvider>
								)}
							</div>
							<FormControl>
								<Input
									{...field}
									value={field.value ?? ""}
									disabled={disabled}
									placeholder="https://your-domain.okta.com/oauth2/your-auth-server-id"
									data-testid={authServerUrlTestId}
								/>
							</FormControl>
							{authServerUrlHelperText && <p className="text-muted-foreground text-xs">{authServerUrlHelperText}</p>}
							<FormMessage />
						</FormItem>
					)}
				/>
				{isIDJAG && (
					<>
						<FormField
							control={control}
							name="token_exchange.resource_client_id"
							render={({ field }) => (
								<FormItem className="flex flex-col gap-2">
									<FormLabel>Client ID at the upstream server</FormLabel>
									<FormControl>
										<SecretVarInput
											data-testid="token-exchange-resource-client-id-input"
											placeholder="Bifrost's client ID registered with the MCP server's authorization server"
											disabled={disabled}
											redactNonEnvValue={clientIdRedactNonEnvValue}
											value={field.value}
											onChange={field.onChange}
										/>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
						<ResourceClientAuthFields
							control={control}
							disabled={disabled}
							hideValueWhenEnv={clientSecretHideValueWhenEnv}
							maskNonEnvValue={clientSecretMaskNonEnvValue}
							redactNonEnvValue={clientSecretRedactNonEnvValue}
						/>
						<FormField
							control={control}
							name="token_exchange.resource_token_endpoint"
							render={({ field }) => (
								<FormItem className="flex flex-col gap-2">
									<FormLabel>Upstream token endpoint (optional)</FormLabel>
									<FormControl>
										<Input
											{...field}
											value={field.value ?? ""}
											disabled={disabled}
											placeholder="Discovered from the audience"
											data-testid="token-exchange-resource-token-endpoint-input"
										/>
									</FormControl>
									<p className="text-muted-foreground text-xs">Leave empty to discover it from the audience; the server must support the jwt-bearer grant.</p>
									<FormMessage />
								</FormItem>
							)}
						/>
						<FormField
							control={control}
							name="token_exchange.resource"
							render={({ field }) => (
								<FormItem className="flex flex-col gap-2">
									<FormLabel>Resource (optional)</FormLabel>
									<FormControl>
										<Input
											{...field}
											value={field.value ?? ""}
											disabled={disabled}
											placeholder="Only if your IdP connection defines one"
											data-testid="token-exchange-resource-input"
										/>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
					</>
				)}
				{scopes.variant === "input" ? (
					<FormItem className="flex flex-col gap-2">
						<FormLabel htmlFor="mcp-token-exchange-scopes-field">{scopes.label}</FormLabel>
						<FormControl>
							<Input
								id="mcp-token-exchange-scopes-field"
								value={scopes.value}
								disabled={scopes.disabled ?? disabled}
								onChange={(e) => scopes.onChange(e.target.value)}
								placeholder="jira.read, jira.write, offline_access"
								data-testid={scopes.testId}
							/>
						</FormControl>
						{scopes.helperText && <p className="text-muted-foreground text-xs">{scopes.helperText}</p>}
					</FormItem>
				) : (
					<div className="space-y-1 md:col-span-2">
						<div className="space-y-0.5">
							<div className="text-sm font-medium" id="mcp-token-exchange-scopes-label">
								{scopes.label}
							</div>
							{scopes.helperText && <p className="text-muted-foreground text-sm">{scopes.helperText}</p>}
						</div>
						<Textarea
							aria-labelledby="mcp-token-exchange-scopes-label"
							className="h-20"
							placeholder="jira.read, jira.write, offline_access"
							value={scopes.value}
							disabled={scopes.disabled ?? disabled}
							onChange={(e) => scopes.onChange(e.target.value)}
							data-testid={scopes.testId}
						/>
					</div>
				)}
			</div>
		</>
	);
}

// Radix Select can't hold an empty value; this stands in for "" (follow discovery).
const AUTO_CLIENT_AUTH = "auto";

interface ResourceClientAuthFieldsProps {
	control: Control<any>;
	disabled?: boolean;
	hideValueWhenEnv?: boolean;
	maskNonEnvValue?: boolean;
	redactNonEnvValue?: boolean;
}

/**
 * ResourceClientAuthFields picks how Bifrost authenticates to the upstream
 * authorization server when redeeming an ID-JAG: a client secret, or a client
 * assertion signed with a private key (private_key_jwt). Switching clears the
 * other method's settings, since the backend refuses both together. The fields
 * themselves are registered by their FormFields; this only watches and clears.
 */
function ResourceClientAuthFields({
	control,
	disabled,
	hideValueWhenEnv,
	maskNonEnvValue,
	redactNonEnvValue,
}: ResourceClientAuthFieldsProps) {
	const { setValue } = useFormContext();
	const key = useWatch({ control, name: "token_exchange.resource_private_key" }) as SecretVar | undefined;
	const mode = useWatch({ control, name: "token_exchange.resource_auth_mode" }) as MCPResourceAuthMode | undefined;
	// The choice lives in the form so validation can require a key for it; a
	// form seeded without one goes by whether a key is stored.
	const method: MCPResourceAuthMode = mode ?? (isSecretVarSet(key) ? "private_key" : "secret");

	const choose = (next: MCPResourceAuthMode) => {
		setValue("token_exchange.resource_auth_mode", next, { shouldDirty: true });
		const clear = (name: string) => setValue(`token_exchange.${name}`, undefined, { shouldDirty: true });
		if (next === "private_key") {
			["resource_client_secret", "resource_client_auth_method"].forEach(clear);
		} else {
			["resource_private_key", "resource_key_id", "resource_signing_algorithm", "resource_assertion_audience"].forEach(clear);
		}
	};

	return (
		<>
			<FormItem className="flex flex-col gap-2">
				<FormLabel>Authentication at the upstream server</FormLabel>
				<Select value={method} onValueChange={(v) => choose(v as MCPResourceAuthMode)} disabled={disabled}>
					<SelectTrigger className="w-full" data-testid="token-exchange-resource-auth-method-select">
						<SelectValue />
					</SelectTrigger>
					<SelectContent>
						<SelectItem value="secret">Client secret, or none for a public client</SelectItem>
						<SelectItem value="private_key">Private key (private_key_jwt)</SelectItem>
					</SelectContent>
				</Select>
			</FormItem>
			{method === "secret" ? (
				<>
					<FormField
						control={control}
						name="token_exchange.resource_client_secret"
						render={({ field }) => (
							<FormItem className="flex flex-col gap-2">
								<FormLabel>Client secret at the upstream server</FormLabel>
								<FormControl>
									<SecretVarInput
										data-testid="token-exchange-resource-client-secret-input"
										placeholder="Leave empty for a public client"
										disabled={disabled}
										hideValueWhenEnv={hideValueWhenEnv}
										maskNonEnvValue={maskNonEnvValue}
										redactNonEnvValue={redactNonEnvValue}
										value={field.value}
										onChange={field.onChange}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name="token_exchange.resource_client_auth_method"
						render={({ field }) => (
							<FormItem className="flex flex-col gap-2">
								<FormLabel>Send the secret</FormLabel>
								<Select
									value={field.value || AUTO_CLIENT_AUTH}
									onValueChange={(v) => field.onChange(v === AUTO_CLIENT_AUTH ? "" : v)}
									disabled={disabled}
								>
									<FormControl>
										<SelectTrigger className="w-full" data-testid="token-exchange-resource-client-auth-method-select">
											<SelectValue />
										</SelectTrigger>
									</FormControl>
									<SelectContent>
										<SelectItem value={AUTO_CLIENT_AUTH}>Automatic</SelectItem>
										<SelectItem value="client_secret_basic">HTTP Basic (client_secret_basic)</SelectItem>
										<SelectItem value="client_secret_post">Request body (client_secret_post)</SelectItem>
									</SelectContent>
								</Select>
								<p className="text-muted-foreground text-xs">
									Automatic uses the request body when the server&apos;s metadata lists only client_secret_post, otherwise HTTP Basic.
									Choose one when you set the token endpoint yourself.
								</p>
								<FormMessage />
							</FormItem>
						)}
					/>
				</>
			) : (
				<>
					<FormField
						control={control}
						name="token_exchange.resource_private_key"
						render={({ field }) => (
							<FormItem className="flex flex-col gap-2">
								<FormLabel>Private key</FormLabel>
								<FormControl>
									<SecretVarInput
										data-testid="token-exchange-resource-private-key-input"
										variant="textarea"
										rows={4}
										placeholder="-----BEGIN PRIVATE KEY----- or env.UPSTREAM_PRIVATE_KEY"
										disabled={disabled}
										hideValueWhenEnv={hideValueWhenEnv}
										maskNonEnvValue={maskNonEnvValue}
										redactNonEnvValue={redactNonEnvValue}
										value={field.value}
										onChange={field.onChange}
									/>
								</FormControl>
								<p className="text-muted-foreground text-xs">
									RSA or EC key in PEM form. Bifrost signs a client assertion with it for each redemption; register the public key with the
									upstream server.
								</p>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name="token_exchange.resource_key_id"
						render={({ field }) => (
							<FormItem className="flex flex-col gap-2">
								<FormLabel>Key ID (optional)</FormLabel>
								<FormControl>
									<Input
										{...field}
										value={field.value ?? ""}
										disabled={disabled}
										placeholder="The kid the upstream server registered for this key"
										data-testid="token-exchange-resource-key-id-input"
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name="token_exchange.resource_signing_algorithm"
						render={({ field }) => (
							<FormItem className="flex flex-col gap-2">
								<FormLabel>Signing algorithm</FormLabel>
								<Select value={field.value || "RS256"} onValueChange={field.onChange} disabled={disabled}>
									<FormControl>
										<SelectTrigger className="w-full" data-testid="token-exchange-resource-signing-algorithm-select">
											<SelectValue />
										</SelectTrigger>
									</FormControl>
									<SelectContent>
										{OAuthSigningAlgorithms.map((alg) => (
											<SelectItem key={alg} value={alg}>
												{alg}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name="token_exchange.resource_assertion_audience"
						render={({ field }) => (
							<FormItem className="flex flex-col gap-2">
								<FormLabel>Assertion audience (optional)</FormLabel>
								<FormControl>
									<Input
										{...field}
										value={field.value ?? ""}
										disabled={disabled}
										placeholder="Defaults to the upstream token endpoint"
										data-testid="token-exchange-resource-assertion-audience-input"
									/>
								</FormControl>
								<p className="text-muted-foreground text-xs">
									Set this only when the upstream server expects a different aud, such as its issuer URL.
								</p>
								<FormMessage />
							</FormItem>
						)}
					/>
				</>
			)}
		</>
	);
}