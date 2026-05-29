import { Button } from "@/components/ui/button";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { HeadersTable } from "@/components/ui/headersTable";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from "@/components/ui/accordion";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { useToast } from "@/hooks/use-toast";
import { getErrorMessage, useCreateMCPClientMutation } from "@/lib/store";
import { CreateMCPClientRequest, SecretVar, MCPAuthType, MCPConnectionType, MCPStdioConfig, MCPTLSConfig } from "@/lib/types/mcp";
import { parseArrayFromText } from "@/lib/utils/array";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Info } from "lucide-react";
import React, { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { MCPHeadersAuthorizer } from "./mcpHeadersAuthorizer";
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

const emptySecretVar: SecretVar = { value: "", ref: "" };

/** Strips empty TLS config so we don't send `{}` to the server. */
function buildTLSConfigPayload(tls: MCPTLSConfig | undefined): MCPTLSConfig | undefined {
	if (!tls) return undefined;
	const hasSkipVerify = tls.insecure_skip_verify === true;
	const hasCACert = tls.ca_cert_pem?.value || tls.ca_cert_pem?.type === "env" || tls.ca_cert_pem?.type === "vault";
	if (!hasSkipVerify && !hasCACert) return undefined;
	return { insecure_skip_verify: tls.insecure_skip_verify, ca_cert_pem: hasCACert ? tls.ca_cert_pem : undefined };
}

const emptyForm: CreateMCPClientRequest = {
	name: "",
	is_code_mode_client: false,
	is_ping_available: true,
	connection_type: "http",
	connection_string: emptySecretVar,
	stdio_config: emptyStdioConfig,
	auth_type: "none",
};

function isValidOAuthResourceURI(value: string): boolean {
	try {
		const parsed = new URL(value);
		return parsed.protocol !== "" && parsed.hash === "";
	} catch {
		return false;
	}
}

const ClientForm: React.FC<ClientFormProps> = ({ open, onClose, onSaved }) => {
	const hasCreateMCPClientAccess = useRbac(RbacResource.MCPGateway, RbacOperation.Create);
	const { toast } = useToast();
	const [createMCPClient] = useCreateMCPClientMutation();

	const [isLoading, setIsLoading] = useState(false);
	const [argsText, setArgsText] = useState("");
	// STDIO env vars as a name→value map. Empty value = pass the bare name so the
	// stdio process reads it from Bifrost's host environment.
	const [envVars, setEnvVars] = useState<Record<string, string>>({});
	const [scopesText, setScopesText] = useState("");
	const [resourceText, setResourceText] = useState("");
	const [oauthFlow, setOauthFlow] = useState<{
		authorizeUrl: string;
		oauthConfigId: string;
		mcpClientId: string;
		isPerUserOauth?: boolean;
	} | null>(null);

	// Per-user-headers admin flow: admin declares the required key names
	// (perUserHeaderKeys), then on Create the MCPHeadersAuthorizer dialog
	// runs a sample-values verify and returns discovered tools. The form
	// then persists the MCP client with those tools attached — first-time
	// end users skip re-discovery that way. Mirrors the OAuth2Authorizer
	// flow exactly: nothing is persisted until the test succeeds.
	const [perUserHeaderKeys, setPerUserHeaderKeys] = useState<string[]>([]);
	const [newHeaderKeyInput, setNewHeaderKeyInput] = useState("");
	const [headersFlow, setHeadersFlow] = useState<{ payload: CreateMCPClientRequest } | null>(null);

	// UI splits the canonical `auth_type` into two dropdowns:
	//   - authKind: none | headers | oauth
	//   - authScope: shared | per_user (hidden when authKind = none)
	// They recombine into the wire `auth_type` ("oauth", "per_user_oauth",
	// "headers", "per_user_headers", "none") so the backend contract is
	// unchanged.
	const [authScope, setAuthScope] = useState<"shared" | "per_user">("shared");

	const methods = useForm<CreateMCPClientRequest>({ defaultValues: emptyForm });
	const { control, handleSubmit, setValue, watch, reset, setError, clearErrors } = methods;

	const connectionType = watch("connection_type");
	const authType = watch("auth_type");
	const headers = watch("headers");

	const authKind: "none" | "headers" | "oauth" =
		authType === "oauth" || authType === "per_user_oauth"
			? "oauth"
			: authType === "headers" || authType === "per_user_headers"
				? "headers"
				: "none";

	const applyAuthKind = (kind: "none" | "headers" | "oauth") => {
		if (kind === "none") {
			setValue("auth_type", "none");
			return;
		}
		if (kind === "oauth") {
			setValue("auth_type", authScope === "per_user" ? "per_user_oauth" : "oauth");
			return;
		}
		setValue("auth_type", authScope === "per_user" ? "per_user_headers" : "headers");
	};

	const applyAuthScope = (scope: "shared" | "per_user") => {
		setAuthScope(scope);
		if (authKind === "oauth") {
			setValue("auth_type", scope === "per_user" ? "per_user_oauth" : "oauth");
		} else if (authKind === "headers") {
			setValue("auth_type", scope === "per_user" ? "per_user_headers" : "headers");
		}
	};

	// Inline header validation (shown live as user edits headers).
	// Both "headers" and "per_user_headers" auth types persist the static
	// headers map via the submit path (see "headers" property of payload
	// below), so the validation gate must cover both — otherwise an empty
	// static header in the per-user flow slips past client validation and
	// opens MCPHeadersAuthorizer with an invalid config the server has to
	// reject.
	let headersValidationError: string | null = null;
	if ((connectionType === "http" || connectionType === "sse") && (authType === "headers" || authType === "per_user_headers") && headers) {
		for (const [key, secretVar] of Object.entries(headers)) {
			if (!secretVar.value && !secretVar.ref) {
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
			setEnvVars({});
			setScopesText("");
			setResourceText("");
			setOauthFlow(null);
			setHeadersFlow(null);
			setPerUserHeaderKeys([]);
			setNewHeaderKeyInput("");
			setAuthScope("shared");
			setIsLoading(false);
		}
	}, [open, reset]);

	const onSubmit = async (data: CreateMCPClientRequest) => {
		let hasErrors = false;

		if (connectionType === "http" || connectionType === "sse") {
			const connVal = data.connection_string?.value?.trim() || "";
			const connRef = data.connection_string?.ref?.trim() || "";
			const isSecret = data.connection_string?.type === "env" || data.connection_string?.type === "vault";
			if (!connVal && !connRef) {
				setError("connection_string", { message: "Connection URL is required" });
				hasErrors = true;
			} else if (!isSecret && connVal && !/^https?:\/\/.+/.test(connVal)) {
				setError("connection_string", {
					message: "Connection URL must start with http:// or https://",
				});
				hasErrors = true;
			}
		}

		if (connectionType === "stdio") {
			const cmd = data.stdio_config?.command || "";
			if (!cmd.trim()) {
				setError("stdio_config.command", { message: "Command is required for STDIO connections" });
				hasErrors = true;
			} else if (/[<>|&;]/.test(cmd)) {
				setError("stdio_config.command", { message: "Command cannot contain special shell characters" });
				hasErrors = true;
			}
		}

		if (authType === "oauth" || authType === "per_user_oauth") {
			if (data.oauth_config?.authorize_url && !/^https?:\/\/.+$/.test(data.oauth_config.authorize_url)) {
				setError("oauth_config.authorize_url", { message: "Authorize URL must start with http:// or https://" });
				hasErrors = true;
			}
			if (data.oauth_config?.token_url && !/^https?:\/\/.+$/.test(data.oauth_config.token_url)) {
				setError("oauth_config.token_url", { message: "Token URL must start with http:// or https://" });
				hasErrors = true;
			}
			if (data.oauth_config?.registration_url && !/^https?:\/\/.+$/.test(data.oauth_config.registration_url)) {
				setError("oauth_config.registration_url", { message: "Registration URL must start with http:// or https://" });
				hasErrors = true;
			}
			if (resourceText.trim() && !isValidOAuthResourceURI(resourceText.trim())) {
				toast({
					title: "Invalid resource URI",
					description: "OAuth resource must be an absolute URI without a fragment.",
					variant: "destructive",
				});
				hasErrors = true;
			}
		}

		if (authType === "per_user_headers") {
			if (perUserHeaderKeys.length === 0) {
				toast({
					title: "Header keys required",
					description: "Declare at least one header name users must supply.",
					variant: "destructive",
				});
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
						// Each row becomes KEY=value, or a bare KEY when no value is given
						// (read from Bifrost's host environment). Rows without a name are skipped.
						envs: Object.entries(envVars)
							.filter(([name]) => name.trim() !== "")
							.map(([name, value]) => {
								const v = value.trim();
								return v ? `${name}=${v}` : name;
							}),
					}
					: undefined,
			tls_config: connectionType === "http" || connectionType === "sse" ? buildTLSConfigPayload(data.tls_config) : undefined,
			oauth_config:
				authType === "oauth" || authType === "per_user_oauth"
					? {
						client_id: data.oauth_config?.client_id ?? emptySecretVar,
						client_secret:
							data.oauth_config?.client_secret?.value ||
								data.oauth_config?.client_secret?.type === "env" ||
								data.oauth_config?.client_secret?.type === "vault"
								? data.oauth_config.client_secret
								: undefined,
						authorize_url: data.oauth_config?.authorize_url || undefined,
						token_url: data.oauth_config?.token_url || undefined,
						registration_url: data.oauth_config?.registration_url || undefined,
						scopes: scopesText.trim() ? parseArrayFromText(scopesText) : undefined,
						server_url: data.connection_string?.value || undefined,
						resource: resourceText.trim() || undefined,
					}
					: undefined,
			// "headers" and "per_user_headers" both can carry static admin
			// headers on data.headers (per-user values are submitted
			// separately by end users). Persist when present.
			headers:
				(authType === "headers" || authType === "per_user_headers") && data.headers && Object.keys(data.headers).length > 0
					? data.headers
					: undefined,
			per_user_header_keys: authType === "per_user_headers" ? perUserHeaderKeys : undefined,
			tools_to_execute: ["*"],
		};

		// Per-user-headers: stash the payload and open the headers test
		// dialog. The dialog collects sample values and POSTs once to
		// /api/mcp/client where the server verifies, discovers tools,
		// and persists in a single round-trip. Mirrors the per-user
		// OAuth flow's single-call shape.
		if (authType === "per_user_headers") {
			setIsLoading(false);
			setHeadersFlow({ payload });
			return;
		}

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
				toast({ title: "Success", description: "Server created" });
				onSaved();
				onClose();
			}
		} catch (error) {
			setIsLoading(false);
			if ((error as any)?.status === 409) {
				setError("name", { message: getErrorMessage(error) });
				return;
			}
			toast({ title: "Error", description: getErrorMessage(error), variant: "destructive" });
		}
	};

	return (
		<Sheet open={open} onOpenChange={(open) => !open && !oauthFlow && onClose()}>
			<SheetContent className="flex w-full flex-col overflow-x-hidden px-0">
				<SheetHeader className="flex flex-col items-start px-7 pt-8">
					<SheetTitle>New MCP Server</SheetTitle>
					<SheetDescription>Configure and connect to a new Model Context Protocol server.</SheetDescription>
				</SheetHeader>

				<Form {...methods}>
					<form onSubmit={handleSubmit(onSubmit)} className="flex min-h-0 flex-1 flex-col">
						<div className="flex-1 space-y-4 overflow-y-auto px-8 pb-8">
							{/* Name */}
							<FormField
								control={control}
								name="name"
								rules={{
									required: "Server name is required",
									minLength: { value: 3, message: "Server name must be at least 3 characters" },
									maxLength: { value: 50, message: "Server name cannot exceed 50 characters" },
									validate: {
										format: (v) => /^[a-zA-Z0-9_]+$/.test(v) || "Server name can only contain letters, numbers, and underscores",
										noLeadingDigit: (v) => !/^[0-9]/.test(v) || "Server name cannot start with a number",
									},
								}}
								render={({ field }) => (
									<FormItem>
										<FormLabel>Name</FormLabel>
										<FormControl>
											<Input id="client-name" data-testid="client-name-input" placeholder="Server name" maxLength={50} {...field} />
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
										<FormLabel>Connection Type</FormLabel>
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
													<SelectValue placeholder="Select connection type" />
												</SelectTrigger>
											</FormControl>
											<SelectContent>
												<SelectItem value="http" data-testid="connection-type-http">
													HTTP (Streamable)
												</SelectItem>
												<SelectItem value="sse" data-testid="connection-type-sse">
													Server-Sent Events (SSE)
												</SelectItem>
												<SelectItem value="stdio" data-testid="connection-type-stdio">
													STDIO
												</SelectItem>
											</SelectContent>
										</Select>
										<p className="text-muted-foreground text-xs">Connection type and authentication settings cannot be changed later.</p>
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
											<Label htmlFor="code-mode">Code Mode Server</Label>
											<TooltipProvider>
												<Tooltip>
													<TooltipTrigger asChild>
														<a
															href="https://docs.getbifrost.ai/mcp/code-mode"
															target="_blank"
															rel="noopener noreferrer"
															data-testid="code-mode-link-help"
															className="text-muted-foreground hover:text-foreground focus-visible:ring-ring rounded focus-visible:ring-2 focus-visible:outline-none"
															aria-label="Learn more about Code Mode"
														>
															<Info className="h-4 w-4 cursor-help" />
														</a>
													</TooltipTrigger>
													<TooltipContent>
														<p>Click to learn more about Code Mode</p>
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
											<Label htmlFor="ping-available">Ping Available for Health Check</Label>
											<TooltipProvider>
												<Tooltip>
													<TooltipTrigger asChild>
														<Info className="text-muted-foreground h-4 w-4 cursor-help" />
													</TooltipTrigger>
													<TooltipContent className="max-w-xs">
														<p>
															Enable to use lightweight ping method for health checks. Disable if your MCP server doesn't support ping -
															will use listTools instead.
														</p>
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
												<FormLabel>Connection URL</FormLabel>
												<SecretVarInput
													value={field.value}
													onChange={(value) => {
														field.onChange(value);
														clearErrors("connection_string");
													}}
													placeholder="http://your-mcp-server:3000 or env.MCP_SERVER_URL"
													data-testid="connection-url-input"
												/>
												<FormMessage />
											</FormItem>
										)}
									/>

									{/* Auth Type */}
									<FormItem className="w-full">
										<FormLabel>Authentication Type</FormLabel>
										<Select value={authKind} onValueChange={(value: "none" | "headers" | "oauth") => applyAuthKind(value)}>
											<FormControl>
												<SelectTrigger className="w-full" data-testid="auth-type-select">
													<SelectValue placeholder="Select authentication type" />
												</SelectTrigger>
											</FormControl>
											<SelectContent>
												<SelectItem value="none" data-testid="auth-type-none">
													None
												</SelectItem>
												<SelectItem value="headers" data-testid="auth-type-headers">
													Headers
												</SelectItem>
												<SelectItem value="oauth" data-testid="auth-type-oauth">
													OAuth 2.0
												</SelectItem>
											</SelectContent>
										</Select>
									</FormItem>

									{/* Auth Scope — only meaningful when there's an auth flow */}
									{authKind !== "none" && (
										<FormItem className="w-full">
											<FormLabel>Auth Scope</FormLabel>
											<Select value={authScope} onValueChange={(value: "shared" | "per_user") => applyAuthScope(value)}>
												<FormControl>
													<SelectTrigger className="w-full" data-testid="auth-scope-select">
														<SelectValue placeholder="Select auth scope" />
													</SelectTrigger>
												</FormControl>
												<SelectContent>
													<SelectItem value="shared" data-testid="auth-scope-shared">
														Shared
													</SelectItem>
													<SelectItem value="per_user" data-testid="auth-scope-per-user">
														Per-User
													</SelectItem>
												</SelectContent>
											</Select>
										</FormItem>
									)}

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
														useSecretVarInput
													/>
													{headersValidationError && <p className="text-destructive text-xs">{headersValidationError}</p>}
													<FormMessage />
												</FormItem>
											)}
										/>
									)}

									{authType === "per_user_headers" && (
										<div className="space-y-4">
											{/* Required header keys (admin schema). Same Textarea +
											    comma-separated pattern as workspace/config security
											    Required Headers, so the two surfaces stay visually
											    consistent. End users supply values per-user at first
											    tool use via the inline auth landing page. */}
											<div className="space-y-1">
												<div className="space-y-0.5">
													<div className="text-sm font-medium">Required Headers</div>
													<p className="text-muted-foreground text-sm">
														Comma-separated list of header names each caller must supply when they first use this server (e.g.{" "}
														<code>X-API-Key, X-Tenant-ID</code>). Values are submitted per user - never stored on this server config.
													</p>
												</div>
												<Textarea
													id="per-user-header-keys"
													data-testid="per-user-header-keys-textarea"
													className="h-24"
													placeholder="X-API-Key, X-Tenant-ID"
													value={newHeaderKeyInput}
													onChange={(e) => {
														setNewHeaderKeyInput(e.target.value);
														setPerUserHeaderKeys(parseArrayFromText(e.target.value));
													}}
												/>
											</div>

											{/* Optional static admin headers (e.g. a fixed tenant header) */}
											<FormField
												control={control}
												name="headers"
												render={({ field }) => (
													<FormItem>
														<HeadersTable
															value={field.value || {}}
															onChange={field.onChange}
															keyPlaceholder="Header name"
															valuePlaceholder="Header value"
															label="Static Headers (optional, applied alongside user values)"
															useSecretVarInput
														/>
														{headersValidationError && <p className="text-destructive text-xs">{headersValidationError}</p>}
														<FormMessage />
													</FormItem>
												)}
											/>

											{/* Sample values are collected in the MCPHeadersAuthorizer
											    dialog that opens on Create — mirrors the OAuth flow
											    where the verification step is also a dialog, not an
											    inline panel. */}
										</div>
									)}

									{(authType === "oauth" || authType === "per_user_oauth") && (
										<Accordion type="single" collapsible className="w-full">
											<AccordionItem value="oauth-advanced" className="border-b-0">
												<AccordionTrigger className="py-0" data-testid="oauth-advanced-trigger">
													<span className="text-sm font-medium">OAuth Client Advanced Settings</span>
												</AccordionTrigger>
												<AccordionContent className="space-y-4 pt-4 pb-0">
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
																					Leave empty to use Dynamic Client Registration (RFC 7591). Bifrost will automatically register
																					with the OAuth provider if supported.
																				</p>
																			</TooltipContent>
																		</Tooltip>
																	</TooltipProvider>
																</div>
																<FormControl>
																	<SecretVarInput
																		value={field.value}
																		onChange={field.onChange}
																		placeholder="your-client-id (auto-generated if empty)"
																		data-testid="mcp-oauth-client-id"
																	/>
																</FormControl>
																<p className="text-muted-foreground text-xs">
																	Will be auto-generated via dynamic registration if left empty and provider supports it
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
																<FormLabel>OAuth Client Secret (optional for PKCE)</FormLabel>
																<FormControl>
																	<SecretVarInput
																		value={field.value}
																		onChange={field.onChange}
																		placeholder="your-client-secret"
																		hideValueWhenEnv
																		maskNonEnvValue
																		data-testid="mcp-oauth-client-secret"
																	/>
																</FormControl>
																<p className="text-muted-foreground text-xs">Leave empty for public clients using PKCE</p>
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
																<FormLabel>Authorization URL (optional, auto-discovered)</FormLabel>
																<FormControl>
																	<Input
																		{...field}
																		value={field.value ?? ""}
																		onChange={(e) => {
																			field.onChange(e);
																			clearErrors("oauth_config.authorize_url");
																		}}
																		placeholder="https://provider.com/oauth/authorize"
																		data-testid="mcp-oauth-authorize-url"
																	/>
																</FormControl>
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
																		placeholder="https://provider.com/oauth/token"
																		data-testid="mcp-oauth-token-url"
																	/>
																</FormControl>
																<FormMessage />
															</FormItem>
														)}
													/>

													{/* OAuth Registration URL */}
													<FormField
														control={control}
														name="oauth_config.registration_url"
														render={({ field }) => (
															<FormItem>
																<FormLabel>Registration URL (optional, auto-discovered)</FormLabel>
																<FormControl>
																	<Input
																		{...field}
																		value={field.value ?? ""}
																		onChange={(e) => {
																			field.onChange(e);
																			clearErrors("oauth_config.registration_url");
																		}}
																		placeholder="https://provider.com/oauth/register"
																		data-testid="mcp-oauth-registration-url"
																	/>
																</FormControl>
																<FormMessage />
															</FormItem>
														)}
													/>

													{/* Scopes (local state, not RHF field) */}
													<div className="space-y-2">
														<Label>Scopes (optional, comma-separated)</Label>
														<Input
															value={scopesText}
															onChange={(e) => setScopesText(e.target.value)}
															placeholder="read, write, admin"
															data-testid="mcp-oauth-scopes-input"
														/>
													</div>

													{/* OAuth Resource Indicator (local state, not RHF field) */}
													<div className="space-y-2">
														<Label>Resource</Label>
														<Input
															value={resourceText}
															onChange={(e) => setResourceText(e.target.value)}
															placeholder="https://provider.example.com/mcp or urn:example:mcp"
															data-testid="mcp-oauth-resource-input"
														/>
													</div>
												</AccordionContent>
											</AccordionItem>
										</Accordion>
									)}

									{/* TLS / Certificate */}
									<Accordion type="single" collapsible className="w-full">
										<AccordionItem value="tls-config" className="border-b-0">
											<AccordionTrigger className="py-0" data-testid="tls-config-trigger">
												<span className="text-sm font-medium">TLS / Certificate</span>
											</AccordionTrigger>
											<AccordionContent className="space-y-4 pt-4 pb-0">
												<FormField
													control={control}
													name="tls_config.insecure_skip_verify"
													render={({ field }) => (
														<FormItem className="flex flex-row items-center justify-between rounded-lg border p-4">
															<div className="space-y-0.5">
																<FormLabel>Skip TLS verification</FormLabel>
																<p className="text-muted-foreground text-sm">
																	Disable TLS certificate verification. Use only in trusted isolated environments. Takes priority over CA
																	certificate.
																</p>
															</div>
															<FormControl>
																<Switch
																	checked={field.value ?? false}
																	onCheckedChange={field.onChange}
																	data-testid="mcp-tls-insecure-skip-verify"
																/>
															</FormControl>
														</FormItem>
													)}
												/>
												<FormField
													control={control}
													name="tls_config.ca_cert_pem"
													render={({ field }) => (
														<FormItem>
															<FormLabel>CA Certificate (PEM) (Optional)</FormLabel>
															<FormControl>
																<SecretVarInput
																	variant="textarea"
																	placeholder={`-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE----- or env.MCP_CA_CERT_PEM`}
																	className="font-mono text-xs"
																	rows={6}
																	hideValueWhenEnv
																	redactNonEnvValue
																	{...field}
																	value={field.value}
																	data-testid="mcp-tls-ca-cert-pem"
																/>
															</FormControl>
															<p className="text-muted-foreground text-sm">
																PEM-encoded CA certificate to trust for MCP server connections (e.g. self-signed or private CA).
															</p>
															<FormMessage />
														</FormItem>
													)}
												/>
											</AccordionContent>
										</AccordionItem>
									</Accordion>
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
										<div className="flex items-center gap-2">
											<Label>Environment Variables</Label>
											<TooltipProvider>
												<Tooltip>
													<TooltipTrigger asChild>
														<Info className="text-muted-foreground h-4 w-4 cursor-help" />
													</TooltipTrigger>
													<TooltipContent className="max-w-xs">
														<p>
															Add a value for each variable, or leave it blank to read the value from the environment where Bifrost runs.
														</p>
													</TooltipContent>
												</Tooltip>
											</TooltipProvider>
										</div>
										<HeadersTable
											value={envVars}
											onChange={setEnvVars}
											keyPlaceholder="API_KEY"
											valuePlaceholder="Value (or leave blank to use host env)"
											label=""
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
					}}
					onSuccess={() => {
						toast({ title: "Success", description: "MCP server connected with OAuth" });
						setOauthFlow(null);
						onClose();
						onSaved();
					}}
					onError={(error) => {
						toast({ title: "OAuth Error", description: error, variant: "destructive" });
					}}
					onConflict={(error) => {
						setOauthFlow(null);
						setError("name", { message: error });
					}}
					authorizeUrl={oauthFlow.authorizeUrl}
					oauthConfigId={oauthFlow.oauthConfigId}
					mcpClientId={oauthFlow.mcpClientId}
					isPerUserOauth={oauthFlow.isPerUserOauth}
				/>
			)}

			{/* Per-user-headers create dialog. Collects sample values inline,
			    then calls POST /api/mcp/client once — the server verifies
			    upstream + discovers tools + persists atomically. Mirrors
			    the per-user OAuth flow's single-call shape. Nothing is
			    committed if the user cancels or verification fails. */}
			{headersFlow && (
				<MCPHeadersAuthorizer
					open={!!headersFlow}
					onClose={() => {
						setHeadersFlow(null);
					}}
					onSuccess={() => {
						setHeadersFlow(null);
						toast({ title: "Success", description: "MCP server connected with per-user headers" });
						onSaved();
						onClose();
					}}
					onError={() => {
						/* error toast handled by the dialog itself */
					}}
					onConflict={(error) => {
						setHeadersFlow(null);
						setError("name", { message: error });
					}}
					perUserHeaderKeys={perUserHeaderKeys}
					submitHandler={async (values) => {
						await createMCPClient({ ...headersFlow.payload, user_headers: values }).unwrap();
					}}
				/>
			)}
		</Sheet>
	);
};

export default ClientForm;
