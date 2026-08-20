import { Button } from "@/components/ui/button";
import { FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { Info, TriangleAlert } from "lucide-react";
import type { Control } from "react-hook-form";
import { useWatch } from "react-hook-form";

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
	const credentialFieldsDisabled = disabled || !!useIdPCredentials;

	return (
		<>
			{beforeFields}
			<div className={gridClassName}>
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
										<div className="bg-muted inline-flex w-fit gap-0.5 rounded-md p-0.5" role="radiogroup" aria-label={String(useIdPCredentialsLabel)}>
											<Button
												type="button"
												size="sm"
												variant="ghost"
												className={checked ? "text-muted-foreground hover:text-foreground" : "bg-background dark:bg-input/30 text-foreground shadow-sm hover:bg-background dark:hover:bg-input/30"}
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
												className={checked ? "bg-background dark:bg-input/30 text-foreground shadow-sm hover:bg-background dark:hover:bg-input/30" : "text-muted-foreground hover:text-foreground"}
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