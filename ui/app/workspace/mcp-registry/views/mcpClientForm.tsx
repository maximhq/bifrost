import { Button } from "@/components/ui/button";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { HeadersTable } from "@/components/ui/headersTable";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { DottedSeparator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { useToast } from "@/hooks/use-toast";
import { getErrorMessage, useCreateMCPClientMutation } from "@/lib/store";
import { CreateMCPClientRequest, SecretVar, MCPAuthType, MCPConnectionType, MCPStdioConfig, MCPTLSConfig } from "@/lib/types/mcp";
import { parseArrayFromText } from "@/lib/utils/array";
import { IS_ENTERPRISE } from "@/lib/constants/config";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useGetSCIMProvidersQuery } from "@enterprise/lib/store/apis/scimApi";
import { Info } from "lucide-react";
import React, { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { MCPHeadersAuthorizer } from "./mcpHeadersAuthorizer";
import { OAuthAdvancedFields } from "./oauthAdvancedFields";
import { OAuth2Authorizer } from "./oauth2Authorizer";
import { SectionHeader } from "./sectionHeader";
import { TLSConfigFields } from "./tlsConfigFields";
import { TokenExchangeFields } from "./tokenExchangeFields";

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

	// Token exchange is backed by the deployment's identity-provider
	// integration, so the option only renders when one is enabled. The exact
	// exchange-client requirement is enforced server-side at create; a missing
	// tokenExchangeClient section surfaces as the create error.
	const { data: scimProviders } = useGetSCIMProvidersQuery(undefined, { skip: !IS_ENTERPRISE });
	const enabledScimProvider = scimProviders?.find((p) => (p as { enabled?: boolean }).enabled) as { name?: string } | undefined;
	const idpConfigured = !!enabledScimProvider;
	// Entra's on-behalf-of grant requires use_idp_credentials — see the
	// Prerequisites warning in docs/mcp/auth/token-exchange.mdx for why a
	// dedicated exchange app structurally can't work there.
	const isEntraIdp = ["entra", "azure", "azuread"].includes((enabledScimProvider?.name ?? "").toLowerCase());

	const [isLoading, setIsLoading] = useState(false);
	const [argsText, setArgsText] = useState("");
	// STDIO env vars as a name→value map. Empty value = pass the bare name so the
	// stdio process reads it from Bifrost's host environment.
	const [envVars, setEnvVars] = useState<Record<string, string>>({});
	const [scopesText, setScopesText] = useState("");
	const [tokenExchangeScopesText, setTokenExchangeScopesText] = useState("");
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
	//   - authKind: none | headers | oauth | token_exchange
	//   - authScope: shared | per_user (hidden when authKind is none or
	//     token_exchange — exchange is inherently per-caller, with no shared
	//     variant to scope)
	// They recombine into the wire `auth_type` ("oauth", "per_user_oauth",
	// "headers", "per_user_headers", "token_exchange", "none") so the backend
	// contract is unchanged.
	const [authScope, setAuthScope] = useState<"shared" | "per_user">("shared");

	const methods = useForm<CreateMCPClientRequest>({ defaultValues: emptyForm });
	const { control, handleSubmit, setValue, watch, reset, setError, clearErrors } = methods;

	const connectionType = watch("connection_type");
	const authType = watch("auth_type");
	const headers = watch("headers");

	const authKind: "none" | "headers" | "oauth" | "token_exchange" =
		authType === "oauth" || authType === "per_user_oauth"
			? "oauth"
			: authType === "headers" || authType === "per_user_headers"
				? "headers"
				: authType === "token_exchange"
					? "token_exchange"
					: "none";

	const applyAuthKind = (kind: "none" | "headers" | "oauth" | "token_exchange") => {
		if (kind === "none" || kind === "token_exchange") {
			setValue("auth_type", kind);
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
			setTokenExchangeScopesText("");
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

		if (authType === "token_exchange") {
			const audience = data.token_exchange?.audience?.trim() || "";
			if (!audience) {
				setError("token_exchange.audience", { message: "Audience is required for token exchange" });
				hasErrors = true;
			}
			if (!data.token_exchange?.use_idp_credentials) {
				const exchangeClientId = data.token_exchange?.client_id;
				if (!exchangeClientId?.value && !exchangeClientId?.ref) {
					setError("token_exchange.client_id", { message: "Exchange client ID is required for token exchange" });
					hasErrors = true;
				}
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
			token_exchange:
				authType === "token_exchange"
					? {
						audience: data.token_exchange?.audience?.trim() || "",
						use_idp_credentials: data.token_exchange?.use_idp_credentials || undefined,
						client_id: data.token_exchange?.use_idp_credentials ? undefined : (data.token_exchange?.client_id ?? emptySecretVar),
						client_secret: data.token_exchange?.use_idp_credentials
							? undefined
							: data.token_exchange?.client_secret?.value ||
								data.token_exchange?.client_secret?.type === "env" ||
								data.token_exchange?.client_secret?.type === "vault"
								? data.token_exchange.client_secret
								: undefined,
						scopes: tokenExchangeScopesText.trim() ? parseArrayFromText(tokenExchangeScopesText) : undefined,
						authorization_server_url: data.token_exchange?.authorization_server_url?.trim() || undefined,
					}
					: undefined,
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
			<SheetContent className="flex w-full flex-col gap-4 overflow-x-hidden p-0 pt-4">
				<SheetHeader className="flex flex-col items-start px-0 py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10 px-8">
					<SheetTitle>New MCP Server</SheetTitle>
					<SheetDescription>Configure and connect to a new Model Context Protocol server.</SheetDescription>
				</SheetHeader>

				<Form {...methods}>
					<form onSubmit={handleSubmit(onSubmit)} className="flex h-full flex-col gap-6">
						<div className="grow space-y-4 px-8">
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

							<DottedSeparator />

							{/* Server Behavior */}
							<div className="space-y-4">
								<SectionHeader title="Server Behavior" description="Control how this server participates in code mode and health checks." />
								<div className="divide-y rounded-md border">
									<FormField
										control={control}
										name="is_code_mode_client"
										render={({ field }) => (
											<FormItem className="flex flex-row items-center justify-between gap-4 px-4 py-3">
												<div className="flex items-center gap-2">
													<FormLabel htmlFor="code-mode">Code Mode Server</FormLabel>
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
												<FormControl>
													<Switch
														id="code-mode"
														data-testid="code-mode-switch"
														checked={field.value || false}
														onCheckedChange={field.onChange}
													/>
												</FormControl>
											</FormItem>
										)}
									/>
									<FormField
										control={control}
										name="is_ping_available"
										render={({ field }) => (
											<FormItem className="flex flex-row items-center justify-between gap-4 px-4 py-3">
												<div className="flex items-center gap-2">
													<FormLabel htmlFor="ping-available">Ping Available for Health Check</FormLabel>
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
												<FormControl>
													<Switch
														id="ping-available"
														data-testid="mcp-is-ping-available"
														checked={field.value === true}
														onCheckedChange={field.onChange}
													/>
												</FormControl>
											</FormItem>
										)}
									/>
									{connectionType === "http" &&
										authType !== "per_user_oauth" &&
										authType !== "per_user_headers" &&
										authType !== "token_exchange" && (
											<FormField
												control={control}
												name="needs_session_stickiness"
												render={({ field }) => (
													<FormItem className="flex flex-row items-center justify-between gap-4 px-4 py-3">
														<div className="flex items-center gap-2">
															<FormLabel htmlFor="needs-session-stickiness">Maintain Persistent Connection</FormLabel>
															<TooltipProvider>
																<Tooltip>
																	<TooltipTrigger asChild>
																		<Info className="text-muted-foreground h-4 w-4 cursor-help" />
																	</TooltipTrigger>
																	<TooltipContent className="max-w-xs">
																		<p>
																			Enable to keep one shared connection open and reused across every caller. Disable to connect fresh on
																			every call instead, same as per-user auth types. Only applies to HTTP connections; SSE and STDIO always
																			keep a persistent connection.
																		</p>
																	</TooltipContent>
																</Tooltip>
															</TooltipProvider>
														</div>
														<FormControl>
															<Switch
																id="needs-session-stickiness"
																data-testid="mcp-needs-session-stickiness"
																checked={field.value === true}
																onCheckedChange={field.onChange}
															/>
														</FormControl>
													</FormItem>
												)}
											/>
										)}
								</div>
							</div>

							<DottedSeparator />

							{/* Connection & Authentication */}
							<div className="space-y-4">
								<SectionHeader
									title="Connection & Authentication"
									description="Choose how Bifrost connects to this server and, for network transports, how requests are authenticated."
								/>
								<div className="space-y-4 rounded-md border p-4">
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
														// needs_session_stickiness=false is rejected for
														// non-http connection types; SSE/STDIO always keep
														// a persistent connection regardless, so drop any
														// explicit false picked while http was selected.
														if (value !== "http") {
															setValue("needs_session_stickiness", undefined);
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
												<p className="text-muted-foreground text-xs">
													Connection type and authentication settings cannot be changed later.
												</p>
												<FormMessage />
											</FormItem>
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
														{IS_ENTERPRISE && idpConfigured && (
															<SelectItem value="token_exchange" data-testid="auth-type-token-exchange">
																Token Exchange (On-Behalf-Of)
															</SelectItem>
														)}
													</SelectContent>
												</Select>
											</FormItem>

											{/* Auth Scope — only meaningful when there's an auth flow with a
											    shared variant; token exchange is inherently per-caller */}
											{authKind !== "none" && authKind !== "token_exchange" && (
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
										</>
									)}
								</div>
							</div>

							{(connectionType === "http" || connectionType === "sse") && (
								<>
									{authType === "headers" && (
										<>
											<DottedSeparator />
											<div className="space-y-4">
												<SectionHeader title="Headers" description="Static headers sent with every request to this server." />
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
																label=""
																useSecretVarInput
															/>
															{headersValidationError && <p className="text-destructive text-xs">{headersValidationError}</p>}
															<FormMessage />
														</FormItem>
													)}
												/>
											</div>
										</>
									)}

									{authType === "per_user_headers" && (
										<>
											<DottedSeparator />
											<div className="space-y-4">
												{/* Required header keys (admin schema). Same Textarea +
												    comma-separated pattern as workspace/config security
												    Required Headers, so the two surfaces stay visually
												    consistent. End users supply values per-user at first
												    tool use via the inline auth landing page. */}
												<SectionHeader
													title="Required Headers"
													description="Comma-separated header names each caller must supply on first use, e.g. X-API-Key, X-Tenant-ID. Values are submitted per user, not stored on this server config."
												/>
												<div className="rounded-md border p-4">
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
											</div>

											{/* Optional static admin headers (e.g. a fixed tenant header) */}
											<div className="space-y-4">
												<SectionHeader title="Static Headers" description="Optional, applied alongside the values each caller supplies." />
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
																label=""
																useSecretVarInput
															/>
															{headersValidationError && <p className="text-destructive text-xs">{headersValidationError}</p>}
															<FormMessage />
														</FormItem>
													)}
												/>
											</div>

											{/* Sample values are collected in the MCPHeadersAuthorizer
											    dialog that opens on Create — mirrors the OAuth flow
											    where the verification step is also a dialog, not an
											    inline panel. */}
										</>
									)}

									{authType === "token_exchange" && (
										<>
											<DottedSeparator />
											<div className="space-y-4" data-testid="token-exchange-fields">
												<SectionHeader
													title="Token Exchange Configuration"
													description="Credentials and scopes used to exchange caller identity tokens for access to this server."
													testId="token-exchange-heading"
												/>
												<div className="space-y-4 rounded-md border p-4">
													<TokenExchangeFields
														control={control}
														gridClassName="space-y-4"
														audienceLabel={
															<>
																Audience <span className="text-destructive">*</span>
															</>
														}
														audienceTooltip={
															isEntraIdp
																? "The resource app's Application (client) ID at your identity provider - a bare GUID, not the api://... Application ID URI shown under Expose an API. Exchanged tokens are scoped to it."
																: "The resource identifier this server is registered as at your identity provider. Exchanged tokens are scoped to it."
														}
														audienceTestId="token-exchange-audience-input"
														onAudienceTouched={() => clearErrors("token_exchange.audience")}
														useIdPCredentialsLabel="Exchange application"
														useIdPCredentialsDedicatedDescription="A separate identity-provider app, scoped only to this server. Recommended for most providers."
														useIdPCredentialsIdPDescription="Reuses your SSO login application's own credentials. Required for Microsoft Entra ID."
														useIdPCredentialsRequiredWarning={
															isEntraIdp && "Your identity provider is Microsoft Entra ID - a dedicated application might not work, switch to Identity provider application."
														}
														onUseIdPCredentialsToggled={(checked) => {
															if (checked) clearErrors(["token_exchange.client_id", "token_exchange.client_secret"]);
														}}
														clientIdLabel={
															<>
																Exchange Client ID <span className="text-destructive">*</span>
															</>
														}
														clientIdTooltip="A dedicated application at your identity provider with the token exchange (or on-behalf-of) grant enabled and permission to request this audience. Not the SSO login application. Ignored when using identity provider credentials above."
														clientIdPlaceholder="bifrost-exchange or env.EXCHANGE_CLIENT_ID"
														clientIdTestId="token-exchange-client-id-input"
														onClientIdTouched={() => clearErrors("token_exchange.client_id")}
														clientIdRedactNonEnvValue={false}
														clientSecretLabel="Exchange Client Secret (optional)"
														clientSecretPlaceholder="env.EXCHANGE_CLIENT_SECRET"
														clientSecretHelperText="Omit for public clients."
														clientSecretTestId="token-exchange-client-secret-input"
														clientSecretHideValueWhenEnv={false}
														clientSecretMaskNonEnvValue={true}
														clientSecretRedactNonEnvValue={false}
														authServerUrlLabel="Authorization Server URL (optional)"
														authServerUrlTooltip={
															<>
																Only needed when the audience above is registered on a different authorization server than the one your SSO
																login uses - for example, Okta&apos;s per-resource Custom Authorization Servers. Leave blank to use your SSO
																login&apos;s issuer, which is correct for most providers.
															</>
														}
														authServerUrlTestId="token-exchange-authorization-server-url-input"
														scopes={{
															variant: "textarea",
															value: tokenExchangeScopesText,
															onChange: setTokenExchangeScopesText,
															label: "Scopes (optional)",
															helperText: (
																<>
																	Comma-separated scopes to request on exchanged tokens. Include <code>offline_access</code> (where your
																	identity provider supports it) so the retained discovery credential can renew itself in the background.
																	{isEntraIdp && (
																		<>
																			{" "}
																			<code>offline_access</code> alone is the only scope combined with the audience&apos;s default resource
																			access - any other scope replaces the default entirely instead of adding to it.
																		</>
																	)}
																</>
															),
															testId: "token-exchange-scopes-textarea",
														}}
													/>
												</div>
											</div>
										</>
									)}

									{(authType === "oauth" || authType === "per_user_oauth") && (
										<>
											<DottedSeparator />
											<div className="space-y-4">
												<SectionHeader
													title="OAuth Configuration"
													description="Credentials and endpoints this server uses to authenticate via OAuth."
													testId="oauth-advanced-heading"
												/>
												<div className="space-y-4 rounded-md border p-4">
													<OAuthAdvancedFields
														control={control}
														scopesRaw={scopesText}
														onScopesRawChange={setScopesText}
														scopesLabel="Scopes (optional, comma-separated)"
														scopesTestId="mcp-oauth-scopes-input"
														resource={{ mode: "raw", value: resourceText, onChange: setResourceText }}
														resourceLabel="Resource"
														resourceTestId="mcp-oauth-resource-input"
														clientIdLabel="OAuth Client ID (optional)"
														clientIdPlaceholder="your-client-id (auto-generated if empty)"
														clientIdHelperText="Will be auto-generated via dynamic registration if left empty and provider supports it"
														clientIdTooltip="Leave empty to use Dynamic Client Registration (RFC 7591). Bifrost will automatically register with the OAuth provider if supported."
														clientIdTestId="mcp-oauth-client-id"
														clientSecretLabel="OAuth Client Secret (optional for PKCE)"
														clientSecretPlaceholder="your-client-secret"
														clientSecretHelperText="Leave empty for public clients using PKCE"
														clientSecretTestId="mcp-oauth-client-secret"
														authorizeUrlLabel="Authorization URL (optional, auto-discovered)"
														authorizeUrlTestId="mcp-oauth-authorize-url"
														tokenUrlLabel="Token URL (optional, auto-discovered)"
														tokenUrlTestId="mcp-oauth-token-url"
														registrationUrlLabel="Registration URL (optional, auto-discovered)"
														registrationUrlTestId="mcp-oauth-registration-url"
														onFieldTouched={(field) => clearErrors(`oauth_config.${field}`)}
													/>
												</div>
											</div>
										</>
									)}

									<DottedSeparator />

									{/* TLS / Certificate */}
									<div className="space-y-4">
										<SectionHeader
											title="TLS / Certificate"
											description="Configure certificate verification for HTTPS connections to this server."
											testId="tls-config-heading"
										/>
										<div className="space-y-4 rounded-md border p-4">
											<TLSConfigFields control={control} />
										</div>
									</div>
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
										<Label htmlFor="stdio-args-input">Arguments (comma-separated)</Label>
										<Input
											id="stdio-args-input"
											value={argsText}
											onChange={(e) => setArgsText(e.target.value)}
											placeholder="--port, 3000, --config, config.json"
											data-testid="stdio-args-input"
										/>
									</div>

									{/* Envs (local state) */}
									<div className="space-y-2" role="group" aria-labelledby="stdio-envs-label">
										<div className="flex items-center gap-2">
											<Label id="stdio-envs-label">Environment Variables</Label>
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
						<div className="bg-card sticky bottom-0 z-10 flex justify-end gap-2 border-t px-8 py-4">
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