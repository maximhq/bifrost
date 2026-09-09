import { FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { Info } from "lucide-react";
import type { Control } from "react-hook-form";

interface OAuthAdvancedFieldsProps {
	// Loosely typed on purpose: shared between the create form (CreateMCPClientRequest)
	// and the edit sheet (MCPClientUpdateSchema), which use identical oauth_config.* field
	// names but different surrounding form types.
	control: Control<any>;
	disabled?: boolean;
	/** Prose / dirty-state warning banners rendered above the field grid. */
	beforeFields?: React.ReactNode;

	scopesRaw: string;
	onScopesRawChange: (value: string) => void;
	scopesLabel: React.ReactNode;
	scopesTestId: string;

	// The Resource field is a real RHF field in the edit sheet (control has an
	// oauth_config.resource entry) but raw local state in the create form
	// (resolved only at submit time, alongside its own URI validation).
	resource: { mode: "field" } | { mode: "raw"; value: string; onChange: (value: string) => void };
	resourceLabel: React.ReactNode;
	resourceTestId?: string;

	clientIdLabel: React.ReactNode;
	clientIdPlaceholder: string;
	clientIdHelperText?: React.ReactNode;
	clientIdTooltip?: React.ReactNode;
	clientIdTestId: string;

	clientSecretLabel: React.ReactNode;
	clientSecretPlaceholder: string;
	clientSecretHelperText?: React.ReactNode;
	clientSecretTestId: string;

	authorizeUrlLabel: React.ReactNode;
	authorizeUrlTestId: string;
	tokenUrlLabel: React.ReactNode;
	tokenUrlTestId: string;
	registrationUrlLabel: React.ReactNode;
	registrationUrlTestId: string;

	/** Called after a URL/client-id field changes, e.g. so the create form can clear a submit-time error for that field. */
	onFieldTouched?: (field: "authorize_url" | "token_url" | "registration_url") => void;
}

/** OAuth Client ID/Secret/Authorize URL/Token URL/Registration URL/Scopes/Resource fields, shared by the MCP create form and edit sheet. */
export function OAuthAdvancedFields({
	control,
	disabled,
	beforeFields,
	scopesRaw,
	onScopesRawChange,
	scopesLabel,
	scopesTestId,
	resource,
	resourceLabel,
	resourceTestId,
	clientIdLabel,
	clientIdPlaceholder,
	clientIdHelperText,
	clientIdTooltip,
	clientIdTestId,
	clientSecretLabel,
	clientSecretPlaceholder,
	clientSecretHelperText,
	clientSecretTestId,
	authorizeUrlLabel,
	authorizeUrlTestId,
	tokenUrlLabel,
	tokenUrlTestId,
	registrationUrlLabel,
	registrationUrlTestId,
	onFieldTouched,
}: OAuthAdvancedFieldsProps) {
	return (
		<>
			{beforeFields}
			<div className="grid grid-cols-1 gap-4 md:grid-cols-2">
				<FormField
					control={control}
					name="oauth_config.client_id"
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
									maskNonEnvValue
									value={field.value}
									onChange={field.onChange}
								/>
							</FormControl>
							{clientIdHelperText && <p className="text-muted-foreground text-xs">{clientIdHelperText}</p>}
							<FormMessage />
						</FormItem>
					)}
				/>
				<FormField
					control={control}
					name="oauth_config.client_secret"
					render={({ field }) => (
						<FormItem className="flex flex-col gap-2">
							<FormLabel>{clientSecretLabel}</FormLabel>
							<FormControl>
								<SecretVarInput
									data-testid={clientSecretTestId}
									placeholder={clientSecretPlaceholder}
									disabled={disabled}
									hideValueWhenEnv
									maskNonEnvValue
									value={field.value}
									onChange={field.onChange}
								/>
							</FormControl>
							{clientSecretHelperText && <p className="text-muted-foreground text-xs">{clientSecretHelperText}</p>}
							<FormMessage />
						</FormItem>
					)}
				/>
				<FormField
					control={control}
					name="oauth_config.authorize_url"
					render={({ field }) => (
						<FormItem className="flex flex-col gap-2">
							<FormLabel>{authorizeUrlLabel}</FormLabel>
							<FormControl>
								<Input
									{...field}
									value={field.value ?? ""}
									disabled={disabled}
									onChange={(e) => {
										field.onChange(e);
										onFieldTouched?.("authorize_url");
									}}
									placeholder="https://provider.com/oauth/authorize"
									data-testid={authorizeUrlTestId}
								/>
							</FormControl>
							<FormMessage />
						</FormItem>
					)}
				/>
				<FormField
					control={control}
					name="oauth_config.token_url"
					render={({ field }) => (
						<FormItem className="flex flex-col gap-2">
							<FormLabel>{tokenUrlLabel}</FormLabel>
							<FormControl>
								<Input
									{...field}
									value={field.value ?? ""}
									disabled={disabled}
									onChange={(e) => {
										field.onChange(e);
										onFieldTouched?.("token_url");
									}}
									placeholder="https://provider.com/oauth/token"
									data-testid={tokenUrlTestId}
								/>
							</FormControl>
							<FormMessage />
						</FormItem>
					)}
				/>
				<FormField
					control={control}
					name="oauth_config.registration_url"
					render={({ field }) => (
						<FormItem className="flex flex-col gap-2">
							<FormLabel>{registrationUrlLabel}</FormLabel>
							<FormControl>
								<Input
									{...field}
									value={field.value ?? ""}
									disabled={disabled}
									onChange={(e) => {
										field.onChange(e);
										onFieldTouched?.("registration_url");
									}}
									placeholder="https://provider.com/oauth/register"
									data-testid={registrationUrlTestId}
								/>
							</FormControl>
							<FormMessage />
						</FormItem>
					)}
				/>
				<FormItem className="flex flex-col gap-2">
					<FormLabel htmlFor="mcp-oauth-scopes-field">{scopesLabel}</FormLabel>
					<FormControl>
						<Input
							id="mcp-oauth-scopes-field"
							value={scopesRaw}
							disabled={disabled}
							onChange={(e) => onScopesRawChange(e.target.value)}
							placeholder="read, write, admin"
							data-testid={scopesTestId}
						/>
					</FormControl>
					<p className="text-muted-foreground text-xs">Comma-separated.</p>
				</FormItem>
				{resource.mode === "field" ? (
					<FormField
						control={control}
						name="oauth_config.resource"
						render={({ field }) => (
							<FormItem className="flex flex-col gap-2">
								<FormLabel>{resourceLabel}</FormLabel>
								<FormControl>
									<Input
										{...field}
										value={field.value ?? ""}
										disabled={disabled}
										placeholder="https://provider.example.com/mcp or urn:example:mcp"
										data-testid={resourceTestId}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
				) : (
					<FormItem className="flex flex-col gap-2">
						<FormLabel htmlFor="mcp-oauth-resource-field">{resourceLabel}</FormLabel>
						<FormControl>
							<Input
								id="mcp-oauth-resource-field"
								value={resource.value}
								disabled={disabled}
								onChange={(e) => resource.onChange(e.target.value)}
								placeholder="https://provider.example.com/mcp or urn:example:mcp"
								data-testid={resourceTestId}
							/>
						</FormControl>
					</FormItem>
				)}
			</div>
		</>
	);
}