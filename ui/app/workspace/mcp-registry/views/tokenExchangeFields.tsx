import { Button } from "@/components/ui/button";
import { FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import type { MCPTokenExchangeConfig } from "@/lib/types/mcp";
import { Info, TriangleAlert } from "lucide-react";
import type { Control } from "react-hook-form";
import { useWatch } from "react-hook-form";

/**
 * tokenExchangeProfilePayload returns the Cross App Access (id_jag) fields of a
 * token_exchange create/update payload. The backend replaces the block
 * wholesale, so every form that submits token_exchange must spread this in or
 * an id_jag client would silently fall back to the direct profile.
 */
export function tokenExchangeProfilePayload(te?: Partial<MCPTokenExchangeConfig>): Partial<MCPTokenExchangeConfig> {
	if (te?.profile !== "id_jag") return {};
	const secret = te.resource_client_secret;
	return {
		profile: "id_jag",
		resource: te.resource?.trim() || undefined,
		resource_token_endpoint: te.resource_token_endpoint?.trim() || undefined,
		resource_client_id: te.resource_client_id,
		resource_client_secret: secret?.value || secret?.type === "env" || secret?.type === "vault" ? secret : undefined,
	};
}

/** tokenExchangeProfileFormValues seeds the id_jag fields of a form from a stored config. */
export function tokenExchangeProfileFormValues(te?: Partial<MCPTokenExchangeConfig>): Partial<MCPTokenExchangeConfig> {
	return {
		profile: te?.profile ?? "",
		resource: te?.resource,
		resource_token_endpoint: te?.resource_token_endpoint,
		resource_client_id: te?.resource_client_id,
		resource_client_secret: te?.resource_client_secret,
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
	const credentialFieldsDisabled = disabled || !!useIdPCredentials;
	const fullRow = gridClassName.includes("grid-cols") ? "md:col-span-2" : "";

	return (
		<>
			{beforeFields}
			<div className={gridClassName}>
				<FormField
					control={control}
					name="token_exchange.profile"
					render={({ field }) => (
						<FormItem className={`flex flex-col gap-2 ${fullRow}`}>
							<FormLabel>How the upstream token is obtained</FormLabel>
							<FormControl>
								<div className="bg-muted inline-flex w-fit gap-0.5 rounded-md p-0.5" role="radiogroup" aria-label="Token exchange profile">
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
									? "Cross App Access (ID-JAG), for example Okta XAA: the identity provider issues an identity assertion (ID-JAG) for the upstream authorization server named in Audience, and that server exchanges it for its own token. Users must have signed in to Bifrost through the identity provider. The exchange application is usually the identity provider application, since the identity provider only accepts the user's sign-in token from the app it was issued to."
									: "The identity provider exchanges the caller's token for one scoped to the audience. Use this for internal servers that trust your identity provider."}
							</p>
						</FormItem>
					)}
				/>
				{useIdPCredentialsLabel && (
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
											hideValueWhenEnv={clientSecretHideValueWhenEnv}
											maskNonEnvValue={clientSecretMaskNonEnvValue}
											redactNonEnvValue={clientSecretRedactNonEnvValue}
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
							name="token_exchange.resource_token_endpoint"
							render={({ field }) => (
								<FormItem className="flex flex-col gap-2">
									<FormLabel>Upstream token endpoint (optional)</FormLabel>
									<FormControl>
										<Input
											{...field}
											value={field.value ?? ""}
											disabled={disabled}
											placeholder="Discovered from the MCP server"
											data-testid="token-exchange-resource-token-endpoint-input"
										/>
									</FormControl>
									<p className="text-muted-foreground text-xs">Leave empty to discover it; the server must support the jwt-bearer grant.</p>
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