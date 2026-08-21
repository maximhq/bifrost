import { FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { HeadersTable } from "@/components/ui/headersTable";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { DottedSeparator } from "@/components/ui/separator";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { IS_ENTERPRISE } from "@/lib/constants/config";
import { CreateMCPClientRequest, MCPAuthType, MCPConnectionType, MCPTLSConfig, SecretVar } from "@/lib/types/mcp";
import { parseArrayFromText } from "@/lib/utils/array";
import { useGetSCIMProvidersQuery } from "@enterprise/lib/store/apis/scimApi";
import { Info } from "lucide-react";
import { useCallback, useState } from "react";
import type { UseFormReturn } from "react-hook-form";
import { OAuthAdvancedFields } from "./oauthAdvancedFields";
import { SectionHeader } from "./sectionHeader";
import { TLSConfigFields } from "./tlsConfigFields";
import { TokenExchangeFields } from "./tokenExchangeFields";

/**
 * Shared configuration body for creating an MCP client. Rendered identically by
 * the catalog's "New MCP Server" sheet and the library's install sheet, so the
 * two surfaces can never drift in layout, copy, or supported auth types. The
 * install sheet passes `lockConnection` because its transport and target come
 * from the library entry the form was seeded with.
 */

const emptySecretVar: SecretVar = { value: "", ref: "" };

export type MCPAuthKind = "none" | "headers" | "oauth" | "token_exchange";
export type MCPAuthScope = "shared" | "per_user";

/** Resolve the wire `auth_type` back into the two dropdowns the UI splits it into. */
export function authKindOf(authType: MCPAuthType | undefined): MCPAuthKind {
	switch (authType) {
		case "oauth":
		case "per_user_oauth":
			return "oauth";
		case "headers":
		case "per_user_headers":
			return "headers";
		case "token_exchange":
			return "token_exchange";
		default:
			return "none";
	}
}

export function authScopeOf(authType: MCPAuthType | undefined): MCPAuthScope {
	return authType === "per_user_oauth" || authType === "per_user_headers" ? "per_user" : "shared";
}

/**
 * Form values that live outside react-hook-form: comma-separated text inputs
 * whose parsed form is only needed at submit time, plus the auth scope half of
 * the split auth dropdowns.
 */
export interface MCPClientFormSatellites {
	argsText: string;
	setArgsText: (value: string) => void;
	envVars: Record<string, string>;
	setEnvVars: (value: Record<string, string>) => void;
	scopesText: string;
	setScopesText: (value: string) => void;
	tokenExchangeScopesText: string;
	setTokenExchangeScopesText: (value: string) => void;
	resourceText: string;
	setResourceText: (value: string) => void;
	perUserHeaderKeys: string[];
	headerKeysInput: string;
	setHeaderKeysInput: (value: string) => void;
	authScope: MCPAuthScope;
	setAuthScope: (value: MCPAuthScope) => void;
	reset: (init?: MCPClientFormSatellitesInit) => void;
}

export interface MCPClientFormSatellitesInit {
	argsText?: string;
	envVars?: Record<string, string>;
	perUserHeaderKeys?: string[];
	authScope?: MCPAuthScope;
}

export function useMCPClientFormSatellites(): MCPClientFormSatellites {
	const [argsText, setArgsText] = useState("");
	const [envVars, setEnvVars] = useState<Record<string, string>>({});
	const [scopesText, setScopesText] = useState("");
	const [tokenExchangeScopesText, setTokenExchangeScopesText] = useState("");
	const [resourceText, setResourceText] = useState("");
	const [headerKeysInput, setHeaderKeysInput] = useState("");
	const [authScope, setAuthScope] = useState<MCPAuthScope>("shared");

	const reset = useCallback((init?: MCPClientFormSatellitesInit) => {
		setArgsText(init?.argsText ?? "");
		setEnvVars(init?.envVars ?? {});
		setScopesText("");
		setTokenExchangeScopesText("");
		setResourceText("");
		setHeaderKeysInput((init?.perUserHeaderKeys ?? []).join(", "));
		setAuthScope(init?.authScope ?? "shared");
	}, []);

	return {
		argsText,
		setArgsText,
		envVars,
		setEnvVars,
		scopesText,
		setScopesText,
		tokenExchangeScopesText,
		setTokenExchangeScopesText,
		resourceText,
		setResourceText,
		// Derived rather than stored: the textarea is the single source of truth,
		// so the parsed list can never fall out of sync with what's on screen.
		perUserHeaderKeys: parseArrayFromText(headerKeysInput),
		headerKeysInput,
		setHeaderKeysInput,
		authScope,
		setAuthScope,
		reset,
	};
}

/** Strips empty TLS config so we don't send `{}` to the server. */
export function buildTLSConfigPayload(tls: MCPTLSConfig | undefined): MCPTLSConfig | undefined {
	if (!tls) return undefined;
	const hasSkipVerify = tls.insecure_skip_verify === true;
	const hasCACert = tls.ca_cert_pem?.value || tls.ca_cert_pem?.type === "env" || tls.ca_cert_pem?.type === "vault";
	if (!hasSkipVerify && !hasCACert) return undefined;
	return { insecure_skip_verify: tls.insecure_skip_verify, ca_cert_pem: hasCACert ? tls.ca_cert_pem : undefined };
}

export function isValidOAuthResourceURI(value: string): boolean {
	try {
		const parsed = new URL(value);
		return parsed.protocol !== "" && parsed.hash === "";
	} catch {
		return false;
	}
}

/**
 * Live header validation shared by both sheets. Both "headers" and
 * "per_user_headers" persist the static headers map, so the gate must cover
 * both — otherwise an empty static header in the per-user flow slips past
 * client validation and opens MCPHeadersAuthorizer with a config the server
 * has to reject.
 */
export function getHeadersValidationError(
	authType: MCPAuthType | undefined,
	headers: Record<string, SecretVar> | undefined,
): string | null {
	if ((authType !== "headers" && authType !== "per_user_headers") || !headers) return null;
	for (const [key, secretVar] of Object.entries(headers)) {
		if (!secretVar.value && !secretVar.ref) {
			return `Header "${key}" must have a value`;
		}
	}
	return null;
}

/**
 * Submit-time validation shared by both sheets. Writes field errors onto the
 * form and returns false when anything failed. `skipConnection` is set by the
 * install sheet, whose transport and target are fixed by the library entry and
 * therefore not user-editable.
 */
export function validateMCPClientForm({
	data,
	satellites,
	form,
	skipConnection,
	onToast,
}: {
	data: CreateMCPClientRequest;
	satellites: MCPClientFormSatellites;
	form: UseFormReturn<CreateMCPClientRequest>;
	skipConnection?: boolean;
	onToast: (title: string, description: string) => void;
}): boolean {
	const { setError } = form;
	const connectionType = data.connection_type;
	const authType = data.auth_type;
	let hasErrors = false;

	if (!skipConnection && (connectionType === "http" || connectionType === "sse")) {
		const connVal = data.connection_string?.value?.trim() || "";
		const connRef = data.connection_string?.ref?.trim() || "";
		const isSecret = data.connection_string?.type === "env" || data.connection_string?.type === "vault";
		if (!connVal && !connRef) {
			setError("connection_string", { message: "Connection URL is required" });
			hasErrors = true;
		} else if (!isSecret && connVal && !/^https?:\/\/.+/.test(connVal)) {
			setError("connection_string", { message: "Connection URL must start with http:// or https://" });
			hasErrors = true;
		}
	}

	if (!skipConnection && connectionType === "stdio") {
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
		if (satellites.resourceText.trim() && !isValidOAuthResourceURI(satellites.resourceText.trim())) {
			onToast("Invalid resource URI", "OAuth resource must be an absolute URI without a fragment.");
			hasErrors = true;
		}
	}

	if (authType === "token_exchange") {
		if (!data.token_exchange?.audience?.trim()) {
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

	if (authType === "per_user_headers" && satellites.perUserHeaderKeys.length === 0) {
		onToast("Header keys required", "Declare at least one header name users must supply.");
		hasErrors = true;
	}

	return !hasErrors;
}

/**
 * Assembles the POST /api/mcp/client body from the form values plus the
 * satellite text inputs. Shared so a field wired into one sheet can never be
 * silently dropped by the other.
 */
export function buildMCPClientPayload(data: CreateMCPClientRequest, satellites: MCPClientFormSatellites): CreateMCPClientRequest {
	const connectionType = data.connection_type;
	const authType = data.auth_type;
	const isStdio = connectionType === "stdio";

	return {
		...data,
		connection_string: isStdio ? undefined : data.connection_string,
		stdio_config: isStdio
			? {
					command: data.stdio_config?.command || "",
					args: parseArrayFromText(satellites.argsText),
					// Each row becomes KEY=value, or a bare KEY when no value is given
					// (read from Bifrost's host environment). Rows without a name are skipped.
					envs: Object.entries(satellites.envVars)
						.filter(([name]) => name.trim() !== "")
						.map(([name, value]) => {
							const trimmed = value.trim();
							return trimmed ? `${name}=${trimmed}` : name;
						}),
				}
			: undefined,
		tls_config: isStdio ? undefined : buildTLSConfigPayload(data.tls_config),
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
						scopes: satellites.scopesText.trim() ? parseArrayFromText(satellites.scopesText) : undefined,
						server_url: data.connection_string?.value || undefined,
						resource: satellites.resourceText.trim() || undefined,
					}
				: undefined,
		// "headers" and "per_user_headers" both can carry static admin headers on
		// data.headers (per-user values are submitted separately by end users).
		headers:
			(authType === "headers" || authType === "per_user_headers") && data.headers && Object.keys(data.headers).length > 0
				? data.headers
				: undefined,
		per_user_header_keys: authType === "per_user_headers" ? satellites.perUserHeaderKeys : undefined,
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
						scopes: satellites.tokenExchangeScopesText.trim() ? parseArrayFromText(satellites.tokenExchangeScopesText) : undefined,
						authorization_server_url: data.token_exchange?.authorization_server_url?.trim() || undefined,
					}
				: undefined,
		tools_to_execute: ["*"],
	};
}

/**
 * Warns that STDIO servers shell out to the host, which only carries the usual
 * runtimes on the official image. Shown wherever a STDIO server is configured.
 */
export function StdioRuntimeNotice() {
	return (
		<div className="rounded-lg border border-amber-200 bg-amber-50 p-3" data-testid="stdio-docker-notice">
			<div className="flex items-start gap-2">
				<Info className="mt-0.5 h-4 w-4 flex-shrink-0 text-amber-700" />
				<div className="flex-1">
					<p className="text-xs font-medium text-amber-900">Docker Notice</p>
					<p className="mt-0.5 text-xs text-amber-800">
						If not using the official Bifrost Docker image, STDIO connections may not work if required commands (npx, python, etc.)
						aren&apos;t installed. You can safely ignore this if running locally or using a custom image with the necessary dependencies.
					</p>
				</div>
			</div>
		</div>
	);
}

interface MCPClientFormFieldsProps {
	form: UseFormReturn<CreateMCPClientRequest>;
	satellites: MCPClientFormSatellites;
	headersValidationError: string | null;
	/**
	 * Renders the transport, target, and STDIO launch command read-only. Set by
	 * the install sheet, where those values come from the library entry rather
	 * than from the person filling in the form.
	 */
	lockConnection?: boolean;
	/**
	 * STDIO variable names declared by the library entry. When set, the env
	 * table renders exactly these rows and only their values are editable.
	 */
	stdioEnvKeys?: string[];
}

export function MCPClientFormFields({ form, satellites, headersValidationError, lockConnection, stdioEnvKeys }: MCPClientFormFieldsProps) {
	const { control, setValue, watch, clearErrors } = form;
	const connectionType = watch("connection_type");
	const authType = watch("auth_type");

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

	const authKind = authKindOf(authType);
	const { authScope, setAuthScope } = satellites;
	// A library entry can declare token_exchange, and the catalog sync does no
	// auth-type validation, so the form can be seeded with an auth type this
	// deployment can't offer. The Select would then show its placeholder while
	// still submitting token_exchange, so surface the mismatch instead.
	const tokenExchangeUnavailable = authKind === "token_exchange" && !(IS_ENTERPRISE && idpConfigured);

	/**
	 * The stickiness switch only renders for a shared http auth type. Picking an
	 * auth type that hides it has to drop any value chosen while it was visible,
	 * otherwise a stale `true` stays in form state and gets submitted. The
	 * connection-type handler below applies the same reset for non-http
	 * transports.
	 */
	const clearStickinessIfHidden = (kind: MCPAuthKind, scope: MCPAuthScope) => {
		if (kind === "token_exchange" || (kind !== "none" && scope === "per_user")) {
			setValue("needs_session_stickiness", undefined);
		}
	};

	const applyAuthKind = (kind: MCPAuthKind) => {
		clearErrors();
		clearStickinessIfHidden(kind, authScope);
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

	const applyAuthScope = (scope: MCPAuthScope) => {
		setAuthScope(scope);
		clearStickinessIfHidden(authKind, scope);
		if (authKind === "oauth") {
			setValue("auth_type", scope === "per_user" ? "per_user_oauth" : "oauth");
		} else if (authKind === "headers") {
			setValue("auth_type", scope === "per_user" ? "per_user_headers" : "headers");
		}
	};

	const isRemote = connectionType === "http" || connectionType === "sse";

	return (
		<>
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
									<Switch id="code-mode" data-testid="code-mode-switch" checked={field.value || false} onCheckedChange={field.onChange} />
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
													Enable to use lightweight ping method for health checks. Disable if your MCP server doesn&apos;t support ping -
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
															Enable to keep one shared connection open and reused across every caller. Disable to connect fresh on every
															call instead, same as per-user auth types. Only applies to HTTP connections; SSE and STDIO always keep a
															persistent connection.
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
					description={
						lockConnection
							? "The transport and target come from the library entry. Choose how requests to it are authenticated."
							: "Choose how Bifrost connects to this server and, for network transports, how requests are authenticated."
					}
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
									disabled={lockConnection}
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
								{!lockConnection && (
									<p className="text-muted-foreground text-xs">Connection type and authentication settings cannot be changed later.</p>
								)}
								<FormMessage />
							</FormItem>
						)}
					/>

					{isRemote && (
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
											disabled={lockConnection}
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
								<Select value={authKind} onValueChange={(value: MCPAuthKind) => applyAuthKind(value)}>
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
										{/* Also rendered when it is already the selected value, so the
										    trigger shows the real auth type rather than a placeholder. */}
										{((IS_ENTERPRISE && idpConfigured) || tokenExchangeUnavailable) && (
											<SelectItem value="token_exchange" data-testid="auth-type-token-exchange">
												Token Exchange (On-Behalf-Of)
											</SelectItem>
										)}
									</SelectContent>
								</Select>
								{tokenExchangeUnavailable && (
									<div
										className="flex items-start gap-2 rounded-lg border border-amber-200 bg-amber-50 p-3 text-xs text-amber-800"
										data-testid="token-exchange-unavailable-notice"
									>
										<Info className="mt-0.5 h-4 w-4 shrink-0 text-amber-600" />
										<p>
											This server expects token exchange, which needs an enabled identity provider. Configure one, or pick a different
											authentication type to continue.
										</p>
									</div>
								)}
							</FormItem>

							{/* Auth Scope — only meaningful when there's an auth flow with a
							    shared variant; token exchange is inherently per-caller */}
							{authKind !== "none" && authKind !== "token_exchange" && (
								<FormItem className="w-full">
									<FormLabel>Auth Scope</FormLabel>
									<Select value={authScope} onValueChange={(value: MCPAuthScope) => applyAuthScope(value)}>
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

			{isRemote && (
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
										value={satellites.headerKeysInput}
										onChange={(e) => satellites.setHeaderKeysInput(e.target.value)}
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
							    dialog that opens on submit — mirrors the OAuth flow
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
											isEntraIdp &&
											"Your identity provider is Microsoft Entra ID - a dedicated application might not work, switch to Identity provider application."
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
												Only needed when the audience above is registered on a different authorization server than the one your SSO login
												uses - for example, Okta&apos;s per-resource Custom Authorization Servers. Leave blank to use your SSO login&apos;s
												issuer, which is correct for most providers.
											</>
										}
										authServerUrlTestId="token-exchange-authorization-server-url-input"
										scopes={{
											variant: "textarea",
											value: satellites.tokenExchangeScopesText,
											onChange: satellites.setTokenExchangeScopesText,
											label: "Scopes (optional)",
											helperText: (
												<>
													Comma-separated scopes to request on exchanged tokens. Include <code>offline_access</code> (where your identity
													provider supports it) so the retained discovery credential can renew itself in the background.
													{isEntraIdp && (
														<>
															{" "}
															<code>offline_access</code> alone is the only scope combined with the audience&apos;s default resource access
															- any other scope replaces the default entirely instead of adding to it.
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
										scopesRaw={satellites.scopesText}
										onScopesRawChange={satellites.setScopesText}
										scopesLabel="Scopes (optional, comma-separated)"
										scopesTestId="mcp-oauth-scopes-input"
										resource={{ mode: "raw", value: satellites.resourceText, onChange: satellites.setResourceText }}
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
					<DottedSeparator />
					<div className="space-y-4">
						<SectionHeader
							title="Launch Command"
							description={
								lockConnection
									? "Bifrost runs this command to start the server. It comes from the library entry and can't be changed here."
									: "Bifrost runs this command to start the server on the machine it's deployed on."
							}
						/>
						<div className="space-y-4 rounded-md border p-4">
							<StdioRuntimeNotice />

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
												disabled={lockConnection}
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
									value={satellites.argsText}
									disabled={lockConnection}
									onChange={(e) => satellites.setArgsText(e.target.value)}
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
												<p>Add a value for each variable, or leave it blank to read the value from the environment where Bifrost runs.</p>
											</TooltipContent>
										</Tooltip>
									</TooltipProvider>
								</div>
								<HeadersTable
									value={satellites.envVars}
									onChange={satellites.setEnvVars}
									fixedKeys={stdioEnvKeys}
									keyPlaceholder="API_KEY"
									valuePlaceholder="Value (or leave blank to use host env)"
									label=""
								/>
							</div>
						</div>
					</div>
				</>
			)}
		</>
	);
}

// Re-exported so the library sheets can build the same section scaffolding
// without reaching past this module into the individual field fragments.
export { SectionHeader };