import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { DottedSeparator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { useToast } from "@/hooks/use-toast";
import { getErrorMessage, useCreateMCPClientMutation } from "@/lib/store";
import { CreateMCPClientRequest, MCPAuthType, MCPLibraryEntry, SecretVar } from "@/lib/types/mcp";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { ShieldCheck } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useForm } from "react-hook-form";
import {
	authScopeOf,
	buildMCPClientPayload,
	getHeadersValidationError,
	MCPClientFormFields,
	useMCPClientFormSatellites,
	validateMCPClientForm,
} from "../../views/mcpClientFormFields";
import { MCPHeadersAuthorizer } from "../../views/mcpHeadersAuthorizer";
import { OAuth2Authorizer } from "../../views/oauth2Authorizer";
import { authLabel, MCP_ICON_FALLBACK, transportIcon, transportLabel } from "./mcpLibraryServerCard";

interface MCPLibraryInstallSheetProps {
	server: MCPLibraryEntry;
	open: boolean;
	onClose: () => void;
	onInstalled: () => void;
}

const emptySecretVar: SecretVar = { value: "", ref: "" };

/**
 * Sanitize a catalog server name into a valid MCP client name. The backend
 * only allows [a-zA-Z0-9_] and disallows a leading digit, so we slugify by
 * replacing any run of invalid characters with a single underscore. Case is
 * preserved (e.g. "Maxim AI" -> "Maxim_AI").
 */
export function sanitizeServerName(name: string): string {
	const cleaned = name
		.trim()
		.replace(/[^a-zA-Z0-9_]+/g, "_")
		.replace(/^_+|_+$/g, "");
	// Prefix if it ends up empty or starts with a digit (leading-digit is rejected).
	return /^[0-9]/.test(cleaned) || cleaned === "" ? `mcp_${cleaned}` : cleaned;
}

/**
 * Seed the create-client form from the library entry. Everything the entry
 * declares is prefilled; everything it can't know (credentials, per-user header
 * values) is left for the installer to supply in the shared form body.
 */
function buildInitialValues(server: MCPLibraryEntry): CreateMCPClientRequest {
	const authType = (server.auth_type || "none") as MCPAuthType;
	const isStdio = server.connection_type === "stdio";
	// A "headers" entry declares which header names it needs; prefill them as
	// empty rows so the installer only has to fill in values.
	const declaredHeaders = server.required_header_keys?.length
		? Object.fromEntries(server.required_header_keys.map((key) => [key, { ...emptySecretVar }]))
		: { Authorization: { ...emptySecretVar } };

	return {
		name: sanitizeServerName(server.name),
		is_code_mode_client: false,
		is_ping_available: true,
		connection_type: server.connection_type || "http",
		connection_string: isStdio ? undefined : { value: server.connection_url || "", ref: "" },
		stdio_config: isStdio && server.stdio_config ? { ...server.stdio_config } : undefined,
		// STDIO servers are launched locally, so no request auth applies and the
		// shared form renders no auth controls for them. The catalog sync does
		// not validate auth_type against connection_type, so an entry can still
		// declare one; ignore it rather than seeding state nothing can edit.
		auth_type: isStdio ? "none" : authType,
		headers: !isStdio && authType === "headers" ? declaredHeaders : undefined,
	};
}

function authHelpText(authType?: MCPAuthType | string): string {
	switch (authType) {
		case "headers":
			return "Add the request headers Bifrost should send with each tool call.";
		case "oauth":
			return "Create the MCP client, then complete the OAuth authorization flow.";
		case "per_user_oauth":
			return "Create the MCP client, then authorize the first user OAuth connection.";
		case "per_user_headers":
			return "Declare the header names each caller must supply, then verify a sample set on install.";
		case "token_exchange":
			return "Set the identity-provider audience for this server; each caller's identity token is exchanged automatically.";
		default:
			return "No credentials are required for this catalog entry.";
	}
}

export function MCPLibraryInstallSheet({ server, open, onClose, onInstalled }: MCPLibraryInstallSheetProps) {
	const hasCreateMCPClientAccess = useRbac(RbacResource.MCPGateway, RbacOperation.Create);
	const { toast } = useToast();
	const [createMCPClient] = useCreateMCPClientMutation();
	const [isLoading, setIsLoading] = useState(false);
	const [oauthFlow, setOauthFlow] = useState<{
		authorizeUrl: string;
		oauthConfigId: string;
		mcpClientId: string;
		isPerUserOauth?: boolean;
	} | null>(null);
	const [headersFlow, setHeadersFlow] = useState<{ payload: CreateMCPClientRequest } | null>(null);

	const defaultValues = useMemo(() => buildInitialValues(server), [server]);
	const methods = useForm<CreateMCPClientRequest>({ defaultValues });
	const { control, handleSubmit, reset, watch, setError } = methods;
	const satellites = useMCPClientFormSatellites();

	const authType = watch("auth_type") || "none";
	const headers = watch("headers");
	const headersValidationError = getHeadersValidationError(authType, headers);

	const isStdio = server.connection_type === "stdio";
	// Only the names the entry declares are editable as values; an entry with no
	// declared variables falls back to a free-form table.
	const stdioEnvKeys = useMemo(
		() => (isStdio && server.stdio_config?.envs?.length ? server.stdio_config.envs : undefined),
		[isStdio, server.stdio_config?.envs],
	);

	const satellitesInit = useMemo(
		() => ({
			argsText: (server.stdio_config?.args || []).join(", "),
			envVars: Object.fromEntries((stdioEnvKeys || []).map((name) => [name, ""])),
			perUserHeaderKeys: server.auth_type === "per_user_headers" ? (server.required_header_keys ?? []) : [],
			authScope: authScopeOf(server.auth_type),
		}),
		[server.auth_type, server.required_header_keys, server.stdio_config?.args, stdioEnvKeys],
	);

	const { reset: resetSatellites } = satellites;
	useEffect(() => {
		if (!open) return;
		reset(defaultValues);
		resetSatellites(satellitesInit);
		setOauthFlow(null);
		setHeadersFlow(null);
		setIsLoading(false);
	}, [defaultValues, open, reset, resetSatellites, satellitesInit]);

	const onSubmit = async (data: CreateMCPClientRequest) => {
		// The transport inputs are locked to the library entry, so a listing
		// published without a target would post an empty URL or command and
		// leave the installer staring at a server error they can't act on.
		const hasTarget = isStdio ? !!data.stdio_config?.command?.trim() : !!data.connection_string?.value?.trim();
		if (!hasTarget) {
			toast({
				title: "Incomplete library entry",
				description: "This entry has no connection target. Ask the publisher to update the listing.",
				variant: "destructive",
			});
			return;
		}

		const isValid = validateMCPClientForm({
			data,
			satellites,
			form: methods,
			// Transport and target are fixed by the library entry, so their
			// inputs are read-only and there is nothing for the installer to
			// get wrong here.
			skipConnection: true,
			onToast: (title, description) => toast({ title, description, variant: "destructive" }),
		});
		if (!isValid) return;
		// The headers table renders its own inline error, but a caller can still
		// reach here with it non-empty; a toast keeps the button from looking inert.
		if (headersValidationError) {
			toast({ title: "Headers incomplete", description: headersValidationError, variant: "destructive" });
			return;
		}

		const payload = buildMCPClientPayload(data, satellites);

		// Per-user-headers: stash the payload and open the headers test dialog.
		if (data.auth_type === "per_user_headers") {
			setHeadersFlow({ payload });
			return;
		}

		try {
			setIsLoading(true);
			const response = await createMCPClient(payload).unwrap();
			setIsLoading(false);

			if (response.status === "pending_oauth" && response.authorize_url) {
				setOauthFlow({
					authorizeUrl: response.authorize_url,
					oauthConfigId: response.oauth_config_id,
					mcpClientId: response.mcp_client_id,
					isPerUserOauth: data.auth_type === "per_user_oauth",
				});
				return;
			}

			toast({ title: "Installed", description: `${server.name} MCP server installed.` });
			onInstalled();
			onClose();
		} catch (error) {
			setIsLoading(false);
			if ((error as any)?.status === 409) {
				setError("name", { message: getErrorMessage(error) });
				return;
			}
			toast({ title: "Error", description: getErrorMessage(error), variant: "destructive" });
		}
	};

	const iconUrl = server.icon_url || MCP_ICON_FALLBACK;
	const isOauth = authType === "oauth" || authType === "per_user_oauth";
	const isPerUserHeaders = authType === "per_user_headers";
	const installButtonLabel = isOauth || isPerUserHeaders ? "Continue" : "Install";

	return (
		<Sheet open={open} onOpenChange={(sheetOpen) => !sheetOpen && !oauthFlow && !headersFlow && onClose()}>
			<SheetContent className="flex w-full flex-col gap-4 overflow-x-hidden p-0 pt-4">
				<SheetHeader className="flex flex-col items-start px-0 py-4" headerClassName="mb-0 sticky px-4 md:px-8 -top-4 bg-card z-10">
					<SheetTitle>Install MCP Server</SheetTitle>
					<SheetDescription>Connect this library server to Bifrost and supply the credentials it needs.</SheetDescription>
				</SheetHeader>

				<Form {...methods}>
					<form onSubmit={handleSubmit(onSubmit)} className="flex h-full flex-col gap-6">
						<div className="grow space-y-4 px-4 md:px-8">
							{/* Catalog entry being installed */}
							<div className="bg-muted/10 flex items-start gap-3 rounded-md border p-3">
								<div className="bg-background flex h-10 w-10 shrink-0 items-center justify-center overflow-hidden rounded-sm border">
									<img
										src={iconUrl}
										alt=""
										className="h-full w-full object-contain p-1"
										onError={(event) => {
											event.currentTarget.onerror = null;
											event.currentTarget.src = MCP_ICON_FALLBACK;
										}}
									/>
								</div>
								<div className="min-w-0 flex-1 space-y-2">
									<div className="min-w-0">
										<p className="truncate text-sm font-medium">{server.name}</p>
										<p className="text-muted-foreground line-clamp-2 text-xs">{server.description || "No description available."}</p>
									</div>
									<div className="flex min-w-0 flex-wrap items-center gap-1.5">
										<Badge variant="outline" className="bg-background">
											{transportIcon(server.connection_type)}
											{transportLabel(server.connection_type)}
										</Badge>
										<Badge variant="outline" className="bg-background">
											<ShieldCheck className="size-3.5" />
											{authLabel(server.auth_type)}
										</Badge>
										{server.category && (
											<Badge variant="secondary" className="max-w-full truncate">
												{server.category}
											</Badge>
										)}
									</div>
								</div>
							</div>

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
											<Input {...field} data-testid="library-mcp-name-input" placeholder="Server name" maxLength={50} />
										</FormControl>
										<p className="text-muted-foreground text-xs">Bifrost uses this name internally when routing MCP tool calls.</p>
										<FormMessage />
									</FormItem>
								)}
							/>

							<DottedSeparator />

							<MCPClientFormFields
								form={methods}
								satellites={satellites}
								headersValidationError={headersValidationError}
								lockConnection
								stdioEnvKeys={stdioEnvKeys}
							/>
						</div>

						<div className="bg-card sticky bottom-0 z-10 border-t px-4 py-4 md:px-8">
							<div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
								<p className="text-muted-foreground text-xs">
									{isOauth
										? "OAuth authorization starts after this step."
										: isPerUserHeaders
											? "Header verification starts after this step."
											: `${authHelpText(authType)} All discovered tools will be enabled after install.`}
								</p>
								<div className="flex justify-end gap-2">
									<Button type="button" variant="outline" onClick={onClose} disabled={isLoading} data-testid="library-install-cancel-btn">
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
														data-testid="library-install-submit-btn"
													>
														{installButtonLabel}
													</Button>
												</span>
											</TooltipTrigger>
											{!hasCreateMCPClientAccess && (
												<TooltipContent>
													<p>You don&apos;t have permission to perform this action</p>
												</TooltipContent>
											)}
										</Tooltip>
									</TooltipProvider>
								</div>
							</div>
						</div>
					</form>
				</Form>
			</SheetContent>

			{oauthFlow && (
				<OAuth2Authorizer
					open={!!oauthFlow}
					onClose={() => setOauthFlow(null)}
					onSuccess={() => {
						toast({ title: "Installed", description: `${server.name} MCP server connected with OAuth.` });
						setOauthFlow(null);
						onInstalled();
						onClose();
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

			{headersFlow && (
				<MCPHeadersAuthorizer
					open={!!headersFlow}
					onClose={() => setHeadersFlow(null)}
					onSuccess={() => {
						setHeadersFlow(null);
						toast({ title: "Installed", description: `${server.name} MCP server connected with per-user headers.` });
						onInstalled();
						onClose();
					}}
					onError={() => {
						/* error toast handled by the dialog itself */
					}}
					onConflict={(error) => {
						setHeadersFlow(null);
						setError("name", { message: error });
					}}
					perUserHeaderKeys={satellites.perUserHeaderKeys}
					submitHandler={async (values) => {
						await createMCPClient({ ...headersFlow.payload, user_headers: values }).unwrap();
					}}
				/>
			)}
		</Sheet>
	);
}