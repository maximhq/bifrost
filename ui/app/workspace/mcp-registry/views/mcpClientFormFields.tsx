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
import { useT, type TranslateFn } from "@/lib/i18n";
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
	t: TranslateFn,
): string | null {
	if ((authType !== "headers" && authType !== "per_user_headers") || !headers) return null;
	for (const [key, secretVar] of Object.entries(headers)) {
		if (!secretVar.value && !secretVar.ref) {
			return t("mcp.validation.headerMustHaveValue", { key });
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
	t,
}: {
	data: CreateMCPClientRequest;
	satellites: MCPClientFormSatellites;
	form: UseFormReturn<CreateMCPClientRequest>;
	skipConnection?: boolean;
	onToast: (title: string, description: string) => void;
	t: TranslateFn;
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
			setError("connection_string", { message: t("mcp.validation.connectionUrlRequired") });
			hasErrors = true;
		} else if (!isSecret && connVal && !/^https?:\/\/.+/.test(connVal)) {
			setError("connection_string", { message: t("mcp.validation.connectionUrlProtocol") });
			hasErrors = true;
		}
	}

	if (!skipConnection && connectionType === "stdio") {
		const cmd = data.stdio_config?.command || "";
		if (!cmd.trim()) {
			setError("stdio_config.command", { message: t("mcp.validation.commandRequired") });
			hasErrors = true;
		} else if (/[<>|&;]/.test(cmd)) {
			setError("stdio_config.command", { message: t("mcp.validation.commandNoShellChars") });
			hasErrors = true;
		}
	}

	if (authType === "oauth" || authType === "per_user_oauth") {
		if (data.oauth_config?.authorize_url && !/^https?:\/\/.+$/.test(data.oauth_config.authorize_url)) {
			setError("oauth_config.authorize_url", { message: t("mcp.validation.authorizeUrlProtocol") });
			hasErrors = true;
		}
		if (data.oauth_config?.token_url && !/^https?:\/\/.+$/.test(data.oauth_config.token_url)) {
			setError("oauth_config.token_url", { message: t("mcp.validation.tokenUrlProtocol") });
			hasErrors = true;
		}
		if (data.oauth_config?.registration_url && !/^https?:\/\/.+$/.test(data.oauth_config.registration_url)) {
			setError("oauth_config.registration_url", { message: t("mcp.validation.registrationUrlProtocol") });
			hasErrors = true;
		}
		if (satellites.resourceText.trim() && !isValidOAuthResourceURI(satellites.resourceText.trim())) {
			onToast(t("mcp.validation.invalidResourceTitle"), t("mcp.validation.invalidResourceDesc"));
			hasErrors = true;
		}
	}

	if (authType === "token_exchange") {
		if (!data.token_exchange?.audience?.trim()) {
			setError("token_exchange.audience", { message: t("mcp.validation.audienceRequired") });
			hasErrors = true;
		}
		if (!data.token_exchange?.use_idp_credentials) {
			const exchangeClientId = data.token_exchange?.client_id;
			if (!exchangeClientId?.value && !exchangeClientId?.ref) {
				setError("token_exchange.client_id", { message: t("mcp.validation.exchangeClientIdRequired") });
				hasErrors = true;
			}
		}
	}

	if (authType === "per_user_headers" && satellites.perUserHeaderKeys.length === 0) {
		onToast(t("mcp.validation.headerKeysRequiredTitle"), t("mcp.validation.headerKeysRequiredDesc"));
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
	const t = useT();
	return (
		<div className="rounded-lg border border-amber-200 bg-amber-50 p-3" data-testid="stdio-docker-notice">
			<div className="flex items-start gap-2">
				<Info className="mt-0.5 h-4 w-4 flex-shrink-0 text-amber-700" />
				<div className="flex-1">
					<p className="text-xs font-medium text-amber-900">{t("mcp.form.dockerNotice")}</p>
					<p className="mt-0.5 text-xs text-amber-800">{t("mcp.form.dockerNoticeBody")}</p>
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
	const t = useT();
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
				<SectionHeader title={t("mcp.serverBehavior")} description={t("mcp.form.serverBehaviorDesc")} />
				<div className="divide-y rounded-md border">
					<FormField
						control={control}
						name="is_code_mode_client"
						render={({ field }) => (
							<FormItem className="flex flex-row items-center justify-between gap-4 px-4 py-3">
								<div className="flex items-center gap-2">
									<FormLabel htmlFor="code-mode">{t("mcp.form.codeMode")}</FormLabel>
									<TooltipProvider>
										<Tooltip>
											<TooltipTrigger asChild>
												<a
													href="https://docs.getbifrost.ai/mcp/code-mode"
													target="_blank"
													rel="noopener noreferrer"
													data-testid="code-mode-link-help"
													className="text-muted-foreground hover:text-foreground focus-visible:ring-ring rounded focus-visible:ring-2 focus-visible:outline-none"
													aria-label={t("mcp.learnMoreCodeMode")}
												>
													<Info className="h-4 w-4 cursor-help" />
												</a>
											</TooltipTrigger>
											<TooltipContent>
												<p>{t("mcp.clickLearnCodeMode")}</p>
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
									<FormLabel htmlFor="ping-available">{t("mcp.form.pingAvailable")}</FormLabel>
									<TooltipProvider>
										<Tooltip>
											<TooltipTrigger asChild>
												<Info className="text-muted-foreground h-4 w-4 cursor-help" />
											</TooltipTrigger>
											<TooltipContent className="max-w-xs">
												<p>{t("mcp.form.pingTooltip")}</p>
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
											<FormLabel htmlFor="needs-session-stickiness">{t("mcp.form.persistentConnection")}</FormLabel>
											<TooltipProvider>
												<Tooltip>
													<TooltipTrigger asChild>
														<Info className="text-muted-foreground h-4 w-4 cursor-help" />
													</TooltipTrigger>
													<TooltipContent className="max-w-xs">
														<p>{t("mcp.form.persistentTooltip")}</p>
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
					title={t("mcp.form.connectionAuth")}
					description={lockConnection ? t("mcp.form.connectionAuthDescLocked") : t("mcp.form.connectionAuthDesc")}
				/>
				<div className="space-y-4 rounded-md border p-4">
					<FormField
						control={control}
						name="connection_type"
						render={({ field }) => (
							<FormItem className="w-full">
								<FormLabel>{t("mcp.connectionType")}</FormLabel>
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
											<SelectValue placeholder={t("mcp.form.selectConnectionType")} />
										</SelectTrigger>
									</FormControl>
									<SelectContent>
										<SelectItem value="http" data-testid="connection-type-http">
											{t("mcp.form.httpStreamable")}
										</SelectItem>
										<SelectItem value="sse" data-testid="connection-type-sse">
											{t("mcp.form.sse")}
										</SelectItem>
										<SelectItem value="stdio" data-testid="connection-type-stdio">
											{t("mcp.form.stdio")}
										</SelectItem>
									</SelectContent>
								</Select>
								{!lockConnection && <p className="text-muted-foreground text-xs">{t("mcp.form.connectionImmutable")}</p>}
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
										<FormLabel>{t("mcp.form.connectionUrl")}</FormLabel>
										<SecretVarInput
											value={field.value}
											disabled={lockConnection}
											onChange={(value) => {
												field.onChange(value);
												clearErrors("connection_string");
											}}
											placeholder={t("mcp.form.connectionUrlPlaceholder")}
											data-testid="connection-url-input"
										/>
										<FormMessage />
									</FormItem>
								)}
							/>

							{/* Auth Type */}
							<FormItem className="w-full">
								<FormLabel>{t("mcp.form.authType")}</FormLabel>
								<Select value={authKind} onValueChange={(value: MCPAuthKind) => applyAuthKind(value)}>
									<FormControl>
										<SelectTrigger className="w-full" data-testid="auth-type-select">
											<SelectValue placeholder={t("mcp.form.selectAuthType")} />
										</SelectTrigger>
									</FormControl>
									<SelectContent>
										<SelectItem value="none" data-testid="auth-type-none">
											{t("mcp.form.authNone")}
										</SelectItem>
										<SelectItem value="headers" data-testid="auth-type-headers">
											{t("mcp.form.authHeaders")}
										</SelectItem>
										<SelectItem value="oauth" data-testid="auth-type-oauth">
											{t("mcp.form.authOauth")}
										</SelectItem>
										{/* Also rendered when it is already the selected value, so the
										    trigger shows the real auth type rather than a placeholder. */}
										{((IS_ENTERPRISE && idpConfigured) || tokenExchangeUnavailable) && (
											<SelectItem value="token_exchange" data-testid="auth-type-token-exchange">
												{t("mcp.form.authTokenExchange")}
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
										<p>{t("mcp.form.tokenExchangeUnavailable")}</p>
									</div>
								)}
							</FormItem>

							{/* Auth Scope — only meaningful when there's an auth flow with a
							    shared variant; token exchange is inherently per-caller */}
							{authKind !== "none" && authKind !== "token_exchange" && (
								<FormItem className="w-full">
									<FormLabel>{t("mcp.form.authScope")}</FormLabel>
									<Select value={authScope} onValueChange={(value: MCPAuthScope) => applyAuthScope(value)}>
										<FormControl>
											<SelectTrigger className="w-full" data-testid="auth-scope-select">
												<SelectValue placeholder={t("mcp.form.selectAuthScope")} />
											</SelectTrigger>
										</FormControl>
										<SelectContent>
											<SelectItem value="shared" data-testid="auth-scope-shared">
												{t("mcp.form.scopeShared")}
											</SelectItem>
											<SelectItem value="per_user" data-testid="auth-scope-per-user">
												{t("mcp.form.scopePerUser")}
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
								<SectionHeader title={t("mcp.form.headers")} description={t("mcp.form.headersDesc")} />
								<FormField
									control={control}
									name="headers"
									render={({ field }) => (
										<FormItem data-testid="mcp-headers-table">
											<HeadersTable
												value={field.value || {}}
												onChange={field.onChange}
												keyPlaceholder={t("mcp.headerName")}
												valuePlaceholder={t("mcp.headerValue")}
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
								<SectionHeader title={t("mcp.requiredHeaders")} description={t("mcp.form.requiredHeadersDesc")} />
								<div className="rounded-md border p-4">
									<Textarea
										id="per-user-header-keys"
										data-testid="per-user-header-keys-textarea"
										className="h-24"
										placeholder={t("mcp.form.requiredHeadersPlaceholder")}
										value={satellites.headerKeysInput}
										onChange={(e) => satellites.setHeaderKeysInput(e.target.value)}
									/>
								</div>
							</div>

							{/* Optional static admin headers (e.g. a fixed tenant header) */}
							<div className="space-y-4">
								<SectionHeader title={t("mcp.form.staticHeaders")} description={t("mcp.form.staticHeadersDesc")} />
								<FormField
									control={control}
									name="headers"
									render={({ field }) => (
										<FormItem>
											<HeadersTable
												value={field.value || {}}
												onChange={field.onChange}
												keyPlaceholder={t("mcp.headerName")}
												valuePlaceholder={t("mcp.headerValue")}
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
									title={t("mcp.form.tokenExchangeTitle")}
									description={t("mcp.form.tokenExchangeDesc")}
									testId="token-exchange-heading"
								/>
								<div className="space-y-4 rounded-md border p-4">
									<TokenExchangeFields
										control={control}
										gridClassName="space-y-4"
										audienceLabel={t("mcp.tokenExchange.audienceRequired")}
										audienceTooltip={isEntraIdp ? t("mcp.tokenExchange.audienceTooltipEntra") : t("mcp.tokenExchange.audienceTooltip")}
										audienceTestId="token-exchange-audience-input"
										onAudienceTouched={() => clearErrors("token_exchange.audience")}
										useIdPCredentialsLabel={t("mcp.tokenExchange.exchangeApp")}
										useIdPCredentialsDedicatedDescription={t("mcp.tokenExchange.dedicatedDesc")}
										useIdPCredentialsIdPDescription={t("mcp.tokenExchange.idpDesc")}
										useIdPCredentialsRequiredWarning={isEntraIdp && t("mcp.tokenExchange.entraWarningCreate")}
										onUseIdPCredentialsToggled={(checked) => {
											if (checked) clearErrors(["token_exchange.client_id", "token_exchange.client_secret"]);
										}}
										clientIdLabel={t("mcp.tokenExchange.clientIdRequired")}
										clientIdTooltip={t("mcp.tokenExchange.clientIdTooltip")}
										clientIdPlaceholder={t("mcp.tokenExchange.clientIdPlaceholder")}
										clientIdTestId="token-exchange-client-id-input"
										onClientIdTouched={() => clearErrors("token_exchange.client_id")}
										clientIdRedactNonEnvValue={false}
										clientSecretLabel={t("mcp.tokenExchange.clientSecretOptional")}
										clientSecretPlaceholder={t("mcp.tokenExchange.clientSecretPlaceholder")}
										clientSecretHelperText={t("mcp.tokenExchange.clientSecretHelper")}
										clientSecretTestId="token-exchange-client-secret-input"
										clientSecretHideValueWhenEnv={false}
										clientSecretMaskNonEnvValue={true}
										clientSecretRedactNonEnvValue={false}
										authServerUrlLabel={t("mcp.tokenExchange.authServerUrlOptional")}
										authServerUrlTooltip={t("mcp.tokenExchange.authServerUrlTooltip")}
										authServerUrlTestId="token-exchange-authorization-server-url-input"
										scopes={{
											variant: "textarea",
											value: satellites.tokenExchangeScopesText,
											onChange: satellites.setTokenExchangeScopesText,
											label: t("mcp.tokenExchange.scopesOptional"),
											helperText: `${t("mcp.tokenExchange.scopesHelperCreate")}${isEntraIdp ? t("mcp.tokenExchange.scopesHelperCreateEntra") : ""}`,
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
								<SectionHeader title={t("mcp.form.oauthTitle")} description={t("mcp.form.oauthDesc")} testId="oauth-advanced-heading" />
								<div className="space-y-4 rounded-md border p-4">
									<OAuthAdvancedFields
										control={control}
										scopesRaw={satellites.scopesText}
										onScopesRawChange={satellites.setScopesText}
										scopesLabel={t("mcp.oauthFields.scopesOptional")}
										scopesTestId="mcp-oauth-scopes-input"
										resource={{ mode: "raw", value: satellites.resourceText, onChange: satellites.setResourceText }}
										resourceLabel={t("mcp.resource")}
										resourceTestId="mcp-oauth-resource-input"
										clientIdLabel={t("mcp.oauthFields.clientIdOptional")}
										clientIdPlaceholder={t("mcp.oauthFields.clientIdPlaceholder")}
										clientIdHelperText={t("mcp.oauthFields.clientIdHelper")}
										clientIdTooltip={t("mcp.oauthFields.clientIdTooltip")}
										clientIdTestId="mcp-oauth-client-id"
										clientSecretLabel={t("mcp.oauthFields.clientSecretOptional")}
										clientSecretPlaceholder={t("mcp.oauthFields.clientSecretPlaceholder")}
										clientSecretHelperText={t("mcp.oauthFields.clientSecretHelper")}
										clientSecretTestId="mcp-oauth-client-secret"
										authorizeUrlLabel={t("mcp.oauthFields.authorizeUrlOptional")}
										authorizeUrlTestId="mcp-oauth-authorize-url"
										tokenUrlLabel={t("mcp.oauthFields.tokenUrlOptional")}
										tokenUrlTestId="mcp-oauth-token-url"
										registrationUrlLabel={t("mcp.oauthFields.registrationUrlOptional")}
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
						<SectionHeader title={t("mcp.form.tlsTitle")} description={t("mcp.form.tlsDesc")} testId="tls-config-heading" />
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
							title={t("mcp.form.launchCommand")}
							description={lockConnection ? t("mcp.form.launchCommandDescLocked") : t("mcp.form.launchCommandDesc")}
						/>
						<div className="space-y-4 rounded-md border p-4">
							<StdioRuntimeNotice />

							{/* STDIO Command */}
							<FormField
								control={control}
								name="stdio_config.command"
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("mcp.form.command")}</FormLabel>
										<FormControl>
											<Input
												{...field}
												value={field.value ?? ""}
												disabled={lockConnection}
												onChange={(e) => {
													field.onChange(e);
													clearErrors("stdio_config.command");
												}}
												placeholder={t("mcp.form.commandPlaceholder")}
												data-testid="stdio-command-input"
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>

							{/* Args (local state) */}
							<div className="space-y-2">
								<Label htmlFor="stdio-args-input">{t("mcp.form.arguments")}</Label>
								<Input
									id="stdio-args-input"
									value={satellites.argsText}
									disabled={lockConnection}
									onChange={(e) => satellites.setArgsText(e.target.value)}
									placeholder={t("mcp.form.argumentsPlaceholder")}
									data-testid="stdio-args-input"
								/>
							</div>

							{/* Envs (local state) */}
							<div className="space-y-2" role="group" aria-labelledby="stdio-envs-label">
								<div className="flex items-center gap-2">
									<Label id="stdio-envs-label">{t("mcp.form.envVars")}</Label>
									<TooltipProvider>
										<Tooltip>
											<TooltipTrigger asChild>
												<Info className="text-muted-foreground h-4 w-4 cursor-help" />
											</TooltipTrigger>
											<TooltipContent className="max-w-xs">
												<p>{t("mcp.form.envVarsTooltip")}</p>
											</TooltipContent>
										</Tooltip>
									</TooltipProvider>
								</div>
								<HeadersTable
									value={satellites.envVars}
									onChange={satellites.setEnvVars}
									fixedKeys={stdioEnvKeys}
									keyPlaceholder="API_KEY"
									valuePlaceholder={t("mcp.form.envValuePlaceholder")}
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