import { Button } from "@/components/ui/button";
import { EnvVarInput } from "@/components/ui/envVarInput";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { HeadersTable } from "@/components/ui/headersTable";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { useToast } from "@/hooks/use-toast";
import { getErrorMessage, useCreateMCPClientMutation } from "@/lib/store";
import { CreateMCPClientRequest, EnvVar, MCPAuthType, MCPConnectionType, MCPStdioConfig, OAuthConfig } from "@/lib/types/mcp";
import { parseArrayFromText } from "@/lib/utils/array";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Info } from "lucide-react";
import React, { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { OAuth2Authorizer } from "./oauth2Authorizer";

interface ClientFormProps {
	open: boolean;
	onClose: () => void;
	onSaved: () => void;
}

const emptyStdioConfig: MCPStdioConfig = {
	command: "",
	args: [],
	envs: [],
};

const emptyEnvVar: EnvVar = { value: "", env_var: "", from_env: false };

const emptyOAuthConfig: OAuthConfig = {
	client_id: emptyEnvVar,
	client_secret: emptyEnvVar,
	authorize_url: "",
	token_url: "",
	scopes: [],
};

const emptyForm: CreateMCPClientRequest = {
	name: "",
	is_code_mode_client: false,
	is_ping_available: true,
	connection_type: "http",
	connection_string: emptyEnvVar,
	stdio_config: emptyStdioConfig,
	auth_type: "none",
};

const ClientForm: React.FC<ClientFormProps> = ({ open, onClose, onSaved }) => {
	const { t } = useTranslation();
	const hasCreateMCPClientAccess = useRbac(RbacResource.MCPGateway, RbacOperation.Create);
	const { toast } = useToast();
	const [createMCPClient] = useCreateMCPClientMutation();

	const [isLoading, setIsLoading] = useState(false);
	const [argsText, setArgsText] = useState("");
	const [envsText, setEnvsText] = useState("");
	const [scopesText, setScopesText] = useState("");
	const [oauthFlow, setOauthFlow] = useState<{
		authorizeUrl: string;
		oauthConfigId: string;
		mcpClientId: string;
		isPerUserOauth?: boolean;
	} | null>(null);

	const methods = useForm<CreateMCPClientRequest>({ defaultValues: emptyForm });
	const { control, handleSubmit, setValue, watch, reset, setError, clearErrors } = methods;

	const connectionType = watch("connection_type");
	const authType = watch("auth_type");
	const connectionString = watch("connection_string");
	const stdioConfig = watch("stdio_config");
	const oauthConfig = watch("oauth_config");
	const headers = watch("headers");
	const isCodeMode = watch("is_code_mode_client");
	const isPingAvailable = watch("is_ping_available");

	// Inline header validation (shown live as user edits headers)
	let headersValidationError: string | null = null;
	if ((connectionType === "http" || connectionType === "sse") && authType === "headers" && headers) {
		for (const [key, envVar] of Object.entries(headers)) {
			if (!envVar.value && !envVar.env_var) {
				headersValidationError = `Header "${key}" must have a value`;
				break;
			}
		}
	}

	// Reset form state when dialog opens
	useEffect(() => {
		if (open) {
			reset(emptyForm);
			setArgsText("");
			setEnvsText("");
			setScopesText("");
			setOauthFlow(null);
			setIsLoading(false);
		}
	}, [open, reset]);

	const onSubmit = async (data: CreateMCPClientRequest) => {
		let hasErrors = false;

		if (connectionType === "http" || connectionType === "sse") {
			const connVal = data.connection_string?.value || "";
			if (!connVal.trim()) {
				setError("connection_string", { message: t("workspace.mcpForm.connectionUrlRequired") });
				hasErrors = true;
			} else if (!/^((https?:\/\/.+)|(env\.[A-Z_]+))$/.test(connVal)) {
				setError("connection_string", {
					message: t("workspace.mcpForm.connectionUrlInvalid"),
				});
				hasErrors = true;
			}
		}

		if (connectionType === "stdio") {
			const cmd = data.stdio_config?.command || "";
			if (!cmd.trim()) {
				setError("stdio_config.command", { message: t("workspace.mcpForm.commandRequired") });
				hasErrors = true;
			} else if (/[<>|&;]/.test(cmd)) {
				setError("stdio_config.command", { message: t("workspace.mcpForm.commandInvalid") });
				hasErrors = true;
			}
		}

		if (authType === "oauth" || authType === "per_user_oauth") {
			if (data.oauth_config?.authorize_url && !/^https?:\/\/.+$/.test(data.oauth_config.authorize_url)) {
				setError("oauth_config.authorize_url", { message: t("workspace.mcpForm.authorizeUrlInvalid") });
				hasErrors = true;
			}
			if (data.oauth_config?.token_url && !/^https?:\/\/.+$/.test(data.oauth_config.token_url)) {
				setError("oauth_config.token_url", { message: t("workspace.mcpForm.tokenUrlInvalid") });
				hasErrors = true;
			}
			if (data.oauth_config?.registration_url && !/^https?:\/\/.+$/.test(data.oauth_config.registration_url)) {
				setError("oauth_config.registration_url", { message: t("workspace.mcpForm.registrationUrlInvalid") });
				hasErrors = true;
			}
		}

		if (headersValidationError || hasErrors) return;

		setIsLoading(true);

		const payload: CreateMCPClientRequest = {
			...data,
			stdio_config:
				connectionType === "stdio"
					? {
							command: data.stdio_config?.command || "",
							args: parseArrayFromText(argsText),
							envs: parseArrayFromText(envsText),
						}
					: undefined,
			oauth_config:
				authType === "oauth" || authType === "per_user_oauth"
					? {
							client_id: data.oauth_config?.client_id ?? emptyEnvVar,
							client_secret:
								data.oauth_config?.client_secret?.value || data.oauth_config?.client_secret?.from_env
									? data.oauth_config.client_secret
									: undefined,
							authorize_url: data.oauth_config?.authorize_url || undefined,
							token_url: data.oauth_config?.token_url || undefined,
							registration_url: data.oauth_config?.registration_url || undefined,
							scopes: scopesText.trim() ? parseArrayFromText(scopesText) : undefined,
							server_url: data.connection_string?.value || undefined,
						}
					: undefined,
			headers: authType === "headers" && data.headers && Object.keys(data.headers).length > 0 ? data.headers : undefined,
			tools_to_execute: ["*"],
		};

		try {
			const response = await createMCPClient(payload).unwrap();

			if (response.status === "pending_oauth" && response.authorize_url) {
				setIsLoading(false);
				setOauthFlow({
					authorizeUrl: response.authorize_url,
					oauthConfigId: response.oauth_config_id,
					mcpClientId: response.mcp_client_id,
					isPerUserOauth: authType === "per_user_oauth",
				});
			} else {
				setIsLoading(false);
				toast({ title: t("workspace.mcpForm.successTitle"), description: t("workspace.mcpForm.serverCreated") });
				onSaved();
				onClose();
			}
		} catch (error) {
			setIsLoading(false);
			toast({ title: t("workspace.mcpForm.errorTitle"), description: getErrorMessage(error), variant: "destructive" });
		}
	};

	return (
		<Sheet open={open} onOpenChange={(open) => !open && onClose()}>
			<SheetContent className="flex w-full flex-col overflow-x-hidden px-0">
				<SheetHeader className="flex flex-col items-start px-7 pt-8">
					<SheetTitle>{t("workspace.mcpForm.newServerTitle")}</SheetTitle>
					<SheetDescription>{t("workspace.mcpForm.newServerDescription")}</SheetDescription>
				</SheetHeader>

				<Form {...methods}>
					<form onSubmit={handleSubmit(onSubmit)} className="flex min-h-0 flex-1 flex-col">
						<div className="flex-1 space-y-4 overflow-y-auto px-8">
							{/* Name */}
							<FormField
								control={control}
								name="name"
								rules={{
									required: t("workspace.mcpForm.serverNameRequired"),
									minLength: { value: 3, message: t("workspace.mcpForm.serverNameMin") },
									maxLength: { value: 50, message: t("workspace.mcpForm.serverNameMax") },
									validate: {
										format: (v) => /^[a-zA-Z0-9_]+$/.test(v) || t("workspace.mcpForm.serverNameFormat"),
										noLeadingDigit: (v) => !/^[0-9]/.test(v) || t("workspace.mcpForm.serverNameLeadingDigit"),
									},
								}}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("workspace.mcpForm.name")}</FormLabel>
										<FormControl>
											<Input
												id="client-name"
												data-testid="client-name-input"
												placeholder={t("workspace.mcpForm.serverNamePlaceholder")}
												maxLength={50}
												{...field}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>

							{/* Connection Type */}
							<FormField
								control={control}
								name="connection_type"
								render={({ field }) => (
									<FormItem className="w-full">
										<FormLabel>{t("workspace.mcpForm.connectionType")}</FormLabel>
										<Select
											value={field.value}
											onValueChange={(value: MCPConnectionType) => {
												field.onChange(value);
												if (value === "stdio") {
													setValue("auth_type", "none");
													setValue("headers", undefined);
													setValue("oauth_config", undefined);
												}
												clearErrors();
											}}
										>
											<FormControl>
												<SelectTrigger className="w-full" data-testid="connection-type-select">
													<SelectValue placeholder={t("workspace.mcpForm.selectConnectionType")} />
												</SelectTrigger>
											</FormControl>
											<SelectContent>
												<SelectItem value="http" data-testid="connection-type-http">
													{t("workspace.mcpForm.httpStreamable")}
												</SelectItem>
												<SelectItem value="sse" data-testid="connection-type-sse">
													{t("workspace.mcpForm.sse")}
												</SelectItem>
												<SelectItem value="stdio" data-testid="connection-type-stdio">
													{t("workspace.mcpForm.stdio")}
												</SelectItem>
											</SelectContent>
										</Select>
										<FormMessage />
									</FormItem>
								)}
							/>

							{/* Code Mode Server */}
							<FormField
								control={control}
								name="is_code_mode_client"
								render={({ field }) => (
									<div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
										<div className="flex items-center gap-2">
											<Label htmlFor="code-mode">{t("workspace.mcpForm.codeModeServer")}</Label>
											<TooltipProvider>
												<Tooltip>
													<TooltipTrigger asChild>
														<a
															href="https://docs.getbifrost.ai/mcp/code-mode"
															target="_blank"
															rel="noopener noreferrer"
															data-testid="code-mode-link-help"
															className="text-muted-foreground hover:text-foreground focus-visible:ring-ring rounded focus-visible:ring-2 focus-visible:outline-none"
															aria-label={t("workspace.mcpForm.learnMoreCodeMode")}
														>
															<Info className="h-4 w-4 cursor-help" />
														</a>
													</TooltipTrigger>
													<TooltipContent>
														<p>{t("workspace.mcpForm.learnMoreCodeMode")}</p>
													</TooltipContent>
												</Tooltip>
											</TooltipProvider>
										</div>
										<Switch id="code-mode" data-testid="code-mode-switch" checked={field.value || false} onCheckedChange={field.onChange} />
									</div>
								)}
							/>

							{/* Ping Available */}
							<FormField
								control={control}
								name="is_ping_available"
								render={({ field }) => (
									<div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
										<div className="flex items-center gap-2">
											<Label htmlFor="ping-available">{t("workspace.mcpForm.pingHealthCheck")}</Label>
											<TooltipProvider>
												<Tooltip>
													<TooltipTrigger asChild>
														<Info className="text-muted-foreground h-4 w-4 cursor-help" />
													</TooltipTrigger>
													<TooltipContent className="max-w-xs">
														<p>{t("workspace.mcpForm.pingHealthCheckDescription")}</p>
													</TooltipContent>
												</Tooltip>
											</TooltipProvider>
										</div>
										<Switch
											id="ping-available"
											data-testid="mcp-is-ping-available"
											checked={field.value === true}
											onCheckedChange={field.onChange}
										/>
									</div>
								)}
							/>

							{(connectionType === "http" || connectionType === "sse") && (
								<>
									{/* Connection URL */}
									<FormField
										control={control}
										name="connection_string"
										render={({ field }) => (
											<FormItem>
												<div className="flex w-fit items-center gap-1">
													<FormLabel>{t("workspace.mcpForm.connectionUrl")}</FormLabel>
													<TooltipProvider>
														<Tooltip>
															<TooltipTrigger asChild>
																<span>
																	<Info className="text-muted-foreground ml-1 h-3 w-3" />
																</span>
															</TooltipTrigger>
															<TooltipContent className="max-w-fit">
																<p>{t("workspace.mcpForm.envVarHint")}</p>
															</TooltipContent>
														</Tooltip>
													</TooltipProvider>
												</div>
												<EnvVarInput
													value={field.value}
													onChange={(value) => {
														field.onChange(value);
														clearErrors("connection_string");
													}}
													placeholder={t("workspace.mcpForm.connectionUrlPlaceholder")}
													data-testid="connection-url-input"
												/>
												<FormMessage />
											</FormItem>
										)}
									/>

									{/* Auth Type */}
									<FormField
										control={control}
										name="auth_type"
										render={({ field }) => (
											<FormItem className="w-full">
												<FormLabel>{t("workspace.mcpForm.authType")}</FormLabel>
												<Select value={field.value} onValueChange={(value: MCPAuthType) => field.onChange(value)}>
													<FormControl>
														<SelectTrigger className="w-full" data-testid="auth-type-select">
															<SelectValue placeholder={t("workspace.mcpForm.selectAuthType")} />
														</SelectTrigger>
													</FormControl>
													<SelectContent>
														<SelectItem value="none" data-testid="auth-type-none">
															{t("workspace.mcpForm.none")}
														</SelectItem>
														<SelectItem value="headers" data-testid="auth-type-headers">
															{t("workspace.mcpForm.headers")}
														</SelectItem>
														<SelectItem value="oauth" data-testid="auth-type-oauth">
															{t("workspace.mcpForm.oauth")}
														</SelectItem>
														<SelectItem value="per_user_oauth" data-testid="auth-type-per-user-oauth">
															{t("workspace.mcpForm.perUserOauth")}
														</SelectItem>
													</SelectContent>
												</Select>
												<FormMessage />
											</FormItem>
										)}
									/>

									{authType === "headers" && (
										<FormField
											control={control}
											name="headers"
											render={({ field }) => (
												<FormItem data-testid="mcp-headers-table">
													<HeadersTable
														value={field.value || {}}
														onChange={field.onChange}
														keyPlaceholder="Header name"
														valuePlaceholder="Header value"
														label="Headers"
														useEnvVarInput
													/>
													{headersValidationError && <p className="text-destructive text-xs">{headersValidationError}</p>}
													<FormMessage />
												</FormItem>
											)}
										/>
									)}

									{(authType === "oauth" || authType === "per_user_oauth") && (
										<>
											{/* OAuth Client ID */}
											<FormField
												control={control}
												name="oauth_config.client_id"
												render={({ field }) => (
													<FormItem>
														<div className="flex items-center gap-2">
															<FormLabel>OAuth Client ID (optional)</FormLabel>
															<TooltipProvider>
																<Tooltip>
																	<TooltipTrigger asChild>
																		<Info className="text-muted-foreground h-4 w-4 cursor-help" />
																	</TooltipTrigger>
																	<TooltipContent className="max-w-xs">
																		<p>
																			Leave empty to use Dynamic Client Registration (RFC 7591). Bifrost will automatically register with
																			the OAuth provider if supported.
																		</p>
																	</TooltipContent>
																</Tooltip>
															</TooltipProvider>
														</div>
														<FormControl>
															<EnvVarInput
																value={field.value}
																onChange={field.onChange}
																placeholder={t("workspace.mcpForm.oauthClientIdPlaceholder")}
																data-testid="mcp-oauth-client-id"
															/>
														</FormControl>
														<p className="text-muted-foreground text-xs">
															{t("workspace.mcpForm.oauthClientIdAutoGenerated")}
														</p>
														<FormMessage />
													</FormItem>
												)}
											/>

											{/* OAuth Client Secret */}
											<FormField
												control={control}
												name="oauth_config.client_secret"
												render={({ field }) => (
													<FormItem>
														<FormLabel>{t("workspace.mcpForm.oauthClientSecretOptionalForPkce")}</FormLabel>
														<FormControl>
															<EnvVarInput
																value={field.value}
																onChange={field.onChange}
																placeholder={t("workspace.mcpForm.oauthClientSecretPlaceholder")}
																hideValueWhenEnv
																maskNonEnvValue
																data-testid="mcp-oauth-client-secret"
															/>
														</FormControl>
														<p className="text-muted-foreground text-xs">{t("workspace.mcpForm.oauthClientSecretOptional")}</p>
														<FormMessage />
													</FormItem>
												)}
											/>

											{/* OAuth Authorize URL */}
											<FormField
												control={control}
												name="oauth_config.authorize_url"
												render={({ field }) => (
<FormItem>
														<FormLabel>{t("workspace.mcpForm.oauthTokenUrlLabel")}</FormLabel>
														<FormControl>
															<Input
																{...field}
																value={field.value ?? ""}
																onChange={(e) => {
																	field.onChange(e);
																	clearErrors("oauth_config.token_url");
																}}
																placeholder={t("workspace.mcpForm.oauthAuthorizationUrlPlaceholder")}
																data-testid="mcp-oauth-token-url"
															/>
														</FormControl>
														<p className="text-muted-foreground text-xs">{t("workspace.mcpForm.oauthAuthorizationUrlAutoDiscovered")}</p>
														<FormMessage />
													</FormItem>
												)}
											/>

											{/* OAuth Token URL */}
											<FormField
												control={control}
												name="oauth_config.token_url"
												render={({ field }) => (
													<FormItem>
														<FormLabel>Token URL (optional, auto-discovered)</FormLabel>
														<FormControl>
															<Input
																{...field}
																value={field.value ?? ""}
																onChange={(e) => {
																	field.onChange(e);
																	clearErrors("oauth_config.token_url");
																}}
																placeholder={t("workspace.mcpForm.oauthTokenUrlPlaceholder")}
															/>
														</FormControl>
														<FormMessage />
														<p className="text-muted-foreground text-xs">Will be discovered from server if not provided</p>
													</FormItem>
												)}
											/>

											{/* OAuth Registration URL */}
											<FormField
												control={control}
												name="oauth_config.registration_url"
												render={({ field }) => (
													<FormItem>
														<FormLabel>{t("workspace.mcpForm.oauthRegistrationUrlLabel")}</FormLabel>
														<FormControl>
															<Input
																{...field}
																value={field.value ?? ""}
																onChange={(e) => {
																	field.onChange(e);
																	clearErrors("oauth_config.registration_url");
																}}
																placeholder={t("workspace.mcpForm.oauthRegistrationUrlPlaceholder")}
															/>
														</FormControl>
														<FormMessage />
														<p className="text-muted-foreground text-xs">
															{t("workspace.mcpForm.oauthRegistrationUrlAutoDiscovered")}
														</p>
													</FormItem>
												)}
											/>

											{/* Scopes (local state, not RHF field) */}
											<div className="space-y-2">
												<Label>{t("workspace.mcpForm.oauthScopesLabel")}</Label>
												<Input value={scopesText} onChange={(e) => setScopesText(e.target.value)} placeholder={t("workspace.mcpForm.oauthScopesPlaceholder")} />
												<p className="text-muted-foreground text-xs">{t("workspace.mcpForm.oauthScopesAutoDiscovered")}</p>
											</div>
										</>
									)}
								</>
							)}

							{connectionType === "stdio" && (
								<>
									<div className="rounded-lg border border-amber-200 bg-amber-50 p-3">
										<div className="flex items-start gap-2">
											<Info className="mt-0.5 h-4 w-4 flex-shrink-0 text-amber-700" />
											<div className="flex-1">
												<p className="text-xs font-medium text-amber-900">Docker Notice</p>
												<p className="mt-0.5 text-xs text-amber-800">
													If not using the official Bifrost Docker image, STDIO connections may not work if required commands (npx, python,
													etc.) aren't installed. You can safely ignore this if running locally or using a custom image with the necessary
													dependencies.
												</p>
											</div>
										</div>
									</div>

									{/* STDIO Command */}
									<FormField
										control={control}
										name="stdio_config.command"
										render={({ field }) => (
											<FormItem>
												<FormLabel>Command</FormLabel>
												<FormControl>
													<Input
														{...field}
														value={field.value ?? ""}
														onChange={(e) => {
															field.onChange(e);
															clearErrors("stdio_config.command");
														}}
														placeholder="node, python, /path/to/executable"
														data-testid="stdio-command-input"
													/>
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>

									{/* Args (local state) */}
									<div className="space-y-2">
										<Label>Arguments (comma-separated)</Label>
										<Input
											value={argsText}
											onChange={(e) => setArgsText(e.target.value)}
											placeholder="--port, 3000, --config, config.json"
											data-testid="stdio-args-input"
										/>
									</div>

									{/* Envs (local state) */}
									<div className="space-y-2">
										<Label>Environment Variables (comma-separated)</Label>
										<Input
											value={envsText}
											onChange={(e) => setEnvsText(e.target.value)}
											placeholder="API_KEY, DATABASE_URL"
											data-testid="stdio-envs-input"
										/>
									</div>
								</>
							)}
						</div>

						{/* Form Footer */}
						<div className="dark:bg-card border-border border-t bg-white px-8 py-4">
							<div className="flex justify-end gap-2">
								<Button type="button" variant="outline" onClick={onClose} disabled={isLoading} data-testid="cancel-client-btn">
									Cancel
								</Button>
								<TooltipProvider>
									<Tooltip>
										<TooltipTrigger asChild>
											<span className="inline-block">
												<Button
													type="submit"
													disabled={isLoading || !hasCreateMCPClientAccess}
													isLoading={isLoading}
													data-testid="save-client-btn"
												>
													Create
												</Button>
											</span>
										</TooltipTrigger>
										{!hasCreateMCPClientAccess && (
											<TooltipContent>
												<p>You don't have permission to perform this action</p>
											</TooltipContent>
										)}
									</Tooltip>
								</TooltipProvider>
							</div>
						</div>
					</form>
				</Form>
			</SheetContent>

			{/* OAuth Authorizer Popup */}
			{oauthFlow && (
				<OAuth2Authorizer
					open={!!oauthFlow}
					onClose={() => {
						setOauthFlow(null);
						onClose();
					}}
					onSuccess={() => {
						toast({ title: "Success", description: "MCP server connected with OAuth" });
						onSaved();
						setOauthFlow(null);
						onClose();
					}}
					onError={(error) => {
						toast({ title: "OAuth Error", description: error, variant: "destructive" });
						setOauthFlow(null);
					}}
					authorizeUrl={oauthFlow.authorizeUrl}
					oauthConfigId={oauthFlow.oauthConfigId}
					mcpClientId={oauthFlow.mcpClientId}
					isPerUserOauth={oauthFlow.isPerUserOauth}
				/>
			)}
		</Sheet>
	);
};

export default ClientForm;