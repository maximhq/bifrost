"use client";

import { Button } from "@/components/ui/button";
import { EnvVarInput } from "@/components/ui/envVarInput";
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
import { Validator } from "@/lib/utils/validation";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Info } from "lucide-react";
import React, { useEffect, useState } from "react";
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
	client_id: "",
	client_secret: "",
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
	const hasCreateMCPClientAccess = useRbac(RbacResource.MCPGateway, RbacOperation.Create);
	const [form, setForm] = useState<CreateMCPClientRequest>(emptyForm);
	const [isLoading, setIsLoading] = useState(false);
	const [argsText, setArgsText] = useState("");
	const [envsText, setEnvsText] = useState("");
	const [scopesText, setScopesText] = useState("");
	const [oauthFlow, setOauthFlow] = useState<{
		authorizeUrl: string;
		oauthConfigId: string;
		mcpClientId: string;
	} | null>(null);
	const { toast } = useToast();

	// RTK Query mutations
	const [createMCPClient] = useCreateMCPClientMutation();

	// Reset form state when dialog opens
	useEffect(() => {
		if (open) {
			setForm(emptyForm);
			setArgsText("");
			setEnvsText("");
			setScopesText("");
			setOauthFlow(null);
			setIsLoading(false);
		}
	}, [open]);

	const handleChange = (
		field: keyof CreateMCPClientRequest,
		value: string | string[] | boolean | MCPConnectionType | MCPStdioConfig | undefined,
	) => {
		setForm((prev) => ({ ...prev, [field]: value }));
	};

	const handleStdioConfigChange = (field: keyof MCPStdioConfig, value: string | string[]) => {
		setForm((prev) => ({
			...prev,
			stdio_config: {
				command: "",
				args: [],
				envs: [],
				...(prev.stdio_config || {}),
				[field]: value,
			},
		}));
	};

	const handleHeadersChange = (value: Record<string, EnvVar>) => {
		setForm((prev) => ({ ...prev, headers: value }));
	};

	const handleConnectionStringChange = (value: EnvVar) => {
		setForm((prev) => ({
			...prev,
			connection_string: value,
		}));
	};

	const handleOAuthConfigChange = (field: keyof OAuthConfig, value: string | string[]) => {
		setForm((prev) => ({
			...prev,
			oauth_config: {
				...(prev.oauth_config || emptyOAuthConfig),
				[field]: value,
			},
		}));
	};

	// Validate headers format
	const validateHeaders = (): string | null => {
		if ((form.connection_type === "http" || form.connection_type === "sse") && form.auth_type === "headers" && form.headers) {
			// Ensure all EnvVar values have either a value or env_var
			for (const [key, envVar] of Object.entries(form.headers)) {
				if (!envVar.value && !envVar.env_var) {
					return `Header "${key}" must have a value`;
				}
			}
		}
		return null;
	};

	const headersValidationError = validateHeaders();

	// Get the connection string value for validation
	const connectionStringValue = form.connection_string?.value || "";

	const validator = new Validator([
		// Name validation
		Validator.required(form.name?.trim(), "Server name is required"),
		Validator.pattern(form.name || "", /^[a-zA-Z0-9_]+$/, "Server name can only contain letters, numbers, and underscores"),
		Validator.custom(!(form.name || "").includes("-"), "Server name cannot contain hyphens"),
		Validator.custom(!(form.name || "").includes(" "), "Server name cannot contain spaces"),
		Validator.custom((form.name || "").length === 0 || !/^[0-9]/.test(form.name || ""), "Server name cannot start with a number"),
		Validator.minLength(form.name || "", 3, "Server name must be at least 3 characters"),
		Validator.maxLength(form.name || "", 50, "Server name cannot exceed 50 characters"),

		// Connection type specific validation
		...(form.connection_type === "http" || form.connection_type === "sse"
			? [
				Validator.required(connectionStringValue?.trim(), "Connection URL is required"),
				Validator.pattern(
					connectionStringValue,
					/^((https?:\/\/.+)|(env\.[A-Z_]+))$/,
					"Connection URL must start with http://, https://, or be an environment variable (env.VAR_NAME)",
				),
				...(headersValidationError ? [Validator.custom(false, headersValidationError)] : []),
			]
			: []),

		// STDIO validation
		...(form.connection_type === "stdio"
			? [
				Validator.required(form.stdio_config?.command?.trim(), "Command is required for STDIO connections"),
				Validator.pattern(form.stdio_config?.command || "", /^[^<>|&;]+$/, "Command cannot contain special shell characters"),
			]
			: []),

		// OAuth validation
		...(form.auth_type === "oauth"
			? [
				// Client ID is optional if provider supports dynamic registration (RFC 7591)
				// URLs are optional (will be discovered), but if provided must be valid
				...(form.oauth_config?.authorize_url
					? [
						Validator.pattern(
							form.oauth_config.authorize_url,
							/^https?:\/\/.+$/,
							"Authorize URL must start with http:// or https://",
						),
					]
					: []),
				...(form.oauth_config?.token_url
					? [Validator.pattern(form.oauth_config.token_url, /^https?:\/\/.+$/, "Token URL must start with http:// or https://")]
					: []),
				...(form.oauth_config?.registration_url
					? [
						Validator.pattern(
							form.oauth_config.registration_url,
							/^https?:\/\/.+$/,
							"Registration URL must start with http:// or https://",
						),
					]
					: []),
			]
			: []),
	]);

	const handleSubmit = async () => {
		// Validate before submitting
		if (!validator.isValid()) {
			toast({
				title: "Validation Error",
				description: validator.getFirstError() || "Please fix validation errors",
				variant: "destructive",
			});
			return;
		}

		setIsLoading(true);

		// Prepare the payload
		const payload: CreateMCPClientRequest = {
			...form,
			stdio_config:
				form.connection_type === "stdio"
					? {
						command: form.stdio_config?.command || "",
						args: parseArrayFromText(argsText),
						envs: parseArrayFromText(envsText),
					}
					: undefined,
			oauth_config:
				form.auth_type === "oauth"
					? {
						client_id: form.oauth_config?.client_id || "", // Can be empty for dynamic registration
						client_secret: form.oauth_config?.client_secret || undefined,
						authorize_url: form.oauth_config?.authorize_url || undefined,
						token_url: form.oauth_config?.token_url || undefined,
						registration_url: form.oauth_config?.registration_url || undefined,
						scopes: scopesText.trim() ? parseArrayFromText(scopesText) : undefined,
						server_url: form.connection_string?.value || undefined, // Set server_url from connection_string
					}
					: undefined,
			headers: form.auth_type === "headers" && form.headers && Object.keys(form.headers).length > 0 ? form.headers : undefined,
			tools_to_execute: ["*"],
		};

		try {
			const response = await createMCPClient(payload).unwrap();

			// Check if OAuth flow was initiated
			if (response.status === "pending_oauth" && response.authorize_url) {
				setIsLoading(false);
				// Open OAuth authorizer popup
				setOauthFlow({
					authorizeUrl: response.authorize_url,
					oauthConfigId: response.oauth_config_id,
					mcpClientId: response.mcp_client_id,
				});
			} else {
				setIsLoading(false);
				toast({
					title: "Success",
					description: "Server created",
				});
				onSaved();
				onClose();
			}
		} catch (error) {
			setIsLoading(false);
			toast({ title: "Error", description: getErrorMessage(error), variant: "destructive" });
		}
	};

	return (
		<Sheet open={open} onOpenChange={(open) => !open && onClose()}>
			<SheetContent className="dark:bg-card flex w-full flex-col overflow-x-hidden bg-white px-4 pb-8">
				<SheetHeader className="flex flex-col items-start px-4 pt-8">
					<SheetTitle>New MCP Server</SheetTitle>
					<SheetDescription>Configure and connect to a new Model Context Protocol server.</SheetDescription>
				</SheetHeader>

				<div className="space-y-4 px-4">
					<div className="space-y-2">
						<Label htmlFor="client-name">Name</Label>
						<Input
							id="client-name"
							data-testid="client-name-input"
							value={form.name}
							onChange={(e: React.ChangeEvent<HTMLInputElement>) => handleChange("name", e.target.value)}
							placeholder="Server name"
							maxLength={50}
						/>
					</div>

					<div className="w-full space-y-2">
						<Label>Connection Type</Label>
						<Select value={form.connection_type} onValueChange={(value: MCPConnectionType) => handleChange("connection_type", value)}>
							<SelectTrigger className="w-full" data-testid="connection-type-select">
								<SelectValue placeholder="Select connection type" />
							</SelectTrigger>
							<SelectContent>
								<SelectItem value="http" data-testid="connection-type-http">HTTP (Streamable)</SelectItem>
								<SelectItem value="sse" data-testid="connection-type-sse">Server-Sent Events (SSE)</SelectItem>
								<SelectItem value="stdio" data-testid="connection-type-stdio">STDIO</SelectItem>
							</SelectContent>
						</Select>
					</div>

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
											className="text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring rounded"
											aria-label="Learn more about Code Mode"
										>
											<Info className="h-4 w-4 cursor-help" />
										</a>
									</TooltipTrigger>
									<TooltipContent>
										<p>Learn more about Code Mode</p>
									</TooltipContent>
								</Tooltip>
							</TooltipProvider>
						</div>
						<Switch
							id="code-mode"
							data-testid="code-mode-switch"
							checked={form.is_code_mode_client || false}
							onCheckedChange={(checked) => handleChange("is_code_mode_client", checked)}
						/>
					</div>

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
											Enable to use lightweight ping method for health checks. Disable if your MCP server doesn't support ping - will use
											listTools instead.
										</p>
									</TooltipContent>
								</Tooltip>
							</TooltipProvider>
						</div>
						<Switch
							id="ping-available"
							checked={form.is_ping_available === true}
							onCheckedChange={(checked) => handleChange("is_ping_available", checked)}
						/>
					</div>

					{(form.connection_type === "http" || form.connection_type === "sse") && (
						<>
							<div className="space-y-2">
								<div className="flex w-fit items-center gap-1">
									<Label>Connection URL</Label>
									<TooltipProvider>
										<Tooltip>
											<TooltipTrigger asChild>
												<span>
													<Info className="text-muted-foreground ml-1 h-3 w-3" />
												</span>
											</TooltipTrigger>
											<TooltipContent className="max-w-fit">
												<p>
													Use <code className="rounded bg-neutral-100 px-1 py-0.5 text-neutral-800">env.&lt;VAR&gt;</code> to read the value
													from an environment variable.
												</p>
											</TooltipContent>
										</Tooltip>
									</TooltipProvider>
								</div>

							<EnvVarInput
								value={form.connection_string}
								onChange={handleConnectionStringChange}
								placeholder="http://your-mcp-server:3000 or env.MCP_SERVER_URL"
								data-testid="connection-url-input"
							/>
							</div>
							<div className="w-full space-y-2">
								<Label>Authentication Type</Label>
								<Select value={form.auth_type} onValueChange={(value: MCPAuthType) => handleChange("auth_type", value)}>
									<SelectTrigger className="w-full" data-testid="auth-type-select">
										<SelectValue placeholder="Select authentication type" />
									</SelectTrigger>
									<SelectContent>
										<SelectItem value="none" data-testid="auth-type-none">None</SelectItem>
										<SelectItem value="headers" data-testid="auth-type-headers">Headers</SelectItem>
										<SelectItem value="oauth" data-testid="auth-type-oauth">OAuth 2.0</SelectItem>
									</SelectContent>
								</Select>
							</div>
							{form.auth_type === "headers" && (
								<div className="space-y-2" data-testid="mcp-headers-table">
									<HeadersTable
										value={form.headers || {}}
										onChange={handleHeadersChange}
										keyPlaceholder="Header name"
										valuePlaceholder="Header value"
										label="Headers"
										useEnvVarInput
									/>
									{headersValidationError && <p className="text-destructive text-xs">{headersValidationError}</p>}
								</div>
							)}

							{form.auth_type === "oauth" && (
								<>
									<div className="space-y-2">
										<div className="flex items-center gap-2">
											<Label>OAuth Client ID (optional)</Label>
											<TooltipProvider>
												<Tooltip>
													<TooltipTrigger asChild>
														<Info className="text-muted-foreground h-4 w-4 cursor-help" />
													</TooltipTrigger>
													<TooltipContent className="max-w-xs">
														<p>
															Leave empty to use Dynamic Client Registration (RFC 7591). Bifrost will automatically register with the OAuth
															provider if supported.
														</p>
													</TooltipContent>
												</Tooltip>
											</TooltipProvider>
										</div>
										<Input
											value={form.oauth_config?.client_id || ""}
											onChange={(e: React.ChangeEvent<HTMLInputElement>) => handleOAuthConfigChange("client_id", e.target.value)}
											placeholder="your-client-id (auto-generated if empty)"
										/>
										<p className="text-muted-foreground text-xs">
											Will be auto-generated via dynamic registration if left empty and provider supports it
										</p>
									</div>
									<div className="space-y-2">
										<Label>OAuth Client Secret (optional for PKCE)</Label>
										<Input
											type="password"
											value={form.oauth_config?.client_secret || ""}
											onChange={(e: React.ChangeEvent<HTMLInputElement>) => handleOAuthConfigChange("client_secret", e.target.value)}
											placeholder="your-client-secret"
										/>
										<p className="text-muted-foreground text-xs">Leave empty for public clients using PKCE</p>
									</div>
									<div className="space-y-2">
										<Label>Authorization URL (optional, auto-discovered)</Label>
										<Input
											value={form.oauth_config?.authorize_url || ""}
											onChange={(e: React.ChangeEvent<HTMLInputElement>) => handleOAuthConfigChange("authorize_url", e.target.value)}
											placeholder="https://provider.com/oauth/authorize"
										/>
										<p className="text-muted-foreground text-xs">Will be discovered from server if not provided</p>
									</div>
									<div className="space-y-2">
										<Label>Token URL (optional, auto-discovered)</Label>
										<Input
											value={form.oauth_config?.token_url || ""}
											onChange={(e: React.ChangeEvent<HTMLInputElement>) => handleOAuthConfigChange("token_url", e.target.value)}
											placeholder="https://provider.com/oauth/token"
										/>
										<p className="text-muted-foreground text-xs">Will be discovered from server if not provided</p>
									</div>
									<div className="space-y-2">
										<Label>Registration URL (optional, auto-discovered)</Label>
										<Input
											value={form.oauth_config?.registration_url || ""}
											onChange={(e: React.ChangeEvent<HTMLInputElement>) => handleOAuthConfigChange("registration_url", e.target.value)}
											placeholder="https://provider.com/oauth/register"
										/>
										<p className="text-muted-foreground text-xs">For dynamic client registration, will be discovered if not provided</p>
									</div>
									<div className="space-y-2">
										<Label>Scopes (optional, comma-separated)</Label>
										<Input
											value={scopesText}
											onChange={(e: React.ChangeEvent<HTMLInputElement>) => setScopesText(e.target.value)}
											placeholder="read, write, admin"
										/>
										<p className="text-muted-foreground text-xs">Will be discovered from server if not provided</p>
									</div>
								</>
							)}
						</>
					)}

					{form.connection_type === "stdio" && (
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
							<div className="space-y-2">
								<Label>Command</Label>
								<Input
									value={form.stdio_config?.command || ""}
									onChange={(e: React.ChangeEvent<HTMLInputElement>) => handleStdioConfigChange("command", e.target.value)}
									placeholder="node, python, /path/to/executable"
									data-testid="stdio-command-input"
								/>
							</div>
							<div className="space-y-2">
								<Label>Arguments (comma-separated)</Label>
								<Input
									value={argsText}
									onChange={(e: React.ChangeEvent<HTMLInputElement>) => setArgsText(e.target.value)}
									placeholder="--port, 3000, --config, config.json"
									data-testid="stdio-args-input"
								/>
							</div>
							<div className="space-y-2">
								<Label>Environment Variables (comma-separated)</Label>
								<Input
									value={envsText}
									onChange={(e: React.ChangeEvent<HTMLInputElement>) => setEnvsText(e.target.value)}
									placeholder="API_KEY, DATABASE_URL"
									data-testid="stdio-envs-input"
								/>
							</div>
						</>
					)}
				</div>
				{/* Form Footer */}
				<div className="dark:bg-card border-border bg-white px-4 py-6">
					<div className="flex justify-end gap-2">
						<Button type="button" variant="outline" onClick={onClose} disabled={isLoading} data-testid="cancel-client-btn">
							Cancel
						</Button>
						<TooltipProvider>
							<Tooltip>
								<TooltipTrigger asChild>
									<span className="inline-block">
										<Button
											onClick={handleSubmit}
											disabled={!validator.isValid() || isLoading || !hasCreateMCPClientAccess}
											isLoading={isLoading}
											data-testid="save-client-btn"
										>
											Create
										</Button>
									</span>
								</TooltipTrigger>
								{(!validator.isValid() || !hasCreateMCPClientAccess) && (
									<TooltipContent>
										<p>
											{!hasCreateMCPClientAccess
												? "You don't have permission to perform this action"
												: validator.getFirstError() || "Please fix validation errors"}
										</p>
									</TooltipContent>
								)}
							</Tooltip>
						</TooltipProvider>
					</div>
				</div>
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
						toast({
							title: "Success",
							description: "MCP server connected with OAuth",
						});
						onSaved();
						setOauthFlow(null);
						onClose();
					}}
					onError={(error) => {
						toast({
							title: "OAuth Error",
							description: error,
							variant: "destructive",
						});
						setOauthFlow(null);
					}}
					authorizeUrl={oauthFlow.authorizeUrl}
					oauthConfigId={oauthFlow.oauthConfigId}
					mcpClientId={oauthFlow.mcpClientId}
				/>
			)}
		</Sheet>
	);
};

export default ClientForm;
