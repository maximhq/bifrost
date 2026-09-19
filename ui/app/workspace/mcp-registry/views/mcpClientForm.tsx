import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { DottedSeparator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { useToast } from "@/hooks/use-toast";
import { getErrorMessage, useCreateMCPClientMutation } from "@/lib/store";
import { CreateMCPClientRequest, SecretVar, MCPStdioConfig } from "@/lib/types/mcp";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import React, { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import {
	buildMCPClientPayload,
	getHeadersValidationError,
	MCPClientFormFields,
	useMCPClientFormSatellites,
	validateMCPClientForm,
} from "./mcpClientFormFields";
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

const emptyForm: CreateMCPClientRequest = {
	name: "",
	endpoint_slug: "",
	is_code_mode_client: false,
	is_ping_available: true,
	connection_type: "http",
	connection_string: emptySecretVar,
	stdio_config: emptyStdioConfig,
	auth_type: "none",
};

const ClientForm: React.FC<ClientFormProps> = ({ open, onClose, onSaved }) => {
	const { t } = useTranslation("mcp");
	const { t: tc } = useTranslation("common");
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

	// Per-user-headers admin flow: admin declares the required key names, then
	// on Create the MCPHeadersAuthorizer dialog runs a sample-values verify and
	// returns discovered tools. The form then persists the MCP client with those
	// tools attached — first-time end users skip re-discovery that way. Mirrors
	// the OAuth2Authorizer flow exactly: nothing is persisted until the test
	// succeeds.
	const [headersFlow, setHeadersFlow] = useState<{ payload: CreateMCPClientRequest } | null>(null);

	const methods = useForm<CreateMCPClientRequest>({ defaultValues: emptyForm });
	const { control, handleSubmit, watch, reset, setError } = methods;
	const satellites = useMCPClientFormSatellites();

	const connectionType = watch("connection_type");
	const authType = watch("auth_type");
	const headers = watch("headers");

	const headersValidationError =
		connectionType === "http" || connectionType === "sse" ? getHeadersValidationError(authType, headers) : null;

	// Reset form state when the sheet opens
	const { reset: resetSatellites } = satellites;
	useEffect(() => {
		if (!open) return;
		reset(emptyForm);
		resetSatellites();
		setOauthFlow(null);
		setHeadersFlow(null);
		setIsLoading(false);
	}, [open, reset, resetSatellites]);

	const onSubmit = async (data: CreateMCPClientRequest) => {
		const isValid = validateMCPClientForm({
			data,
			satellites,
			form: methods,
			onToast: (title, description) => toast({ title, description, variant: "destructive" }),
		});
		if (!isValid || headersValidationError) return;

		setIsLoading(true);
		const payload = buildMCPClientPayload(data, satellites);

		// Per-user-headers: stash the payload and open the headers test dialog.
		// The dialog collects sample values and POSTs once to /api/mcp/client
		// where the server verifies, discovers tools, and persists in a single
		// round-trip. Mirrors the per-user OAuth flow's single-call shape.
		if (data.auth_type === "per_user_headers") {
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
					isPerUserOauth: data.auth_type === "per_user_oauth",
				});
			} else {
				setIsLoading(false);
				toast({ title: t("common.success"), description: t("registry.form.serverCreated") });
				onSaved();
				onClose();
			}
		} catch (error) {
			setIsLoading(false);
			if ((error as any)?.status === 409) {
				setError("name", { message: getErrorMessage(error) });
				return;
			}
			toast({ title: tc("error"), description: getErrorMessage(error), variant: "destructive" });
		}
	};

	return (
		<Sheet open={open} onOpenChange={(open) => !open && !oauthFlow && onClose()}>
			<SheetContent className="flex w-full flex-col gap-4 overflow-x-hidden p-0 pt-4">
				<SheetHeader className="flex flex-col items-start px-0 py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10 px-4 md:px-8">
					<SheetTitle>{t("registry.newServer")}</SheetTitle>
					<SheetDescription>{t("registry.form.description")}</SheetDescription>
				</SheetHeader>

				<Form {...methods}>
					<form onSubmit={handleSubmit(onSubmit)} className="flex h-full flex-col gap-6">
						<div className="grow space-y-4 px-4 md:px-8">
							{/* Name */}
							<FormField
								control={control}
								name="name"
								rules={{
									required: t("registry.form.serverNameRequired"),
									minLength: { value: 3, message: t("registry.form.serverNameMin") },
									maxLength: { value: 50, message: t("registry.form.serverNameMax") },
									validate: {
										format: (v) => /^[a-zA-Z0-9_]+$/.test(v) || t("registry.form.serverNameFormat"),
										noLeadingDigit: (v) => !/^[0-9]/.test(v) || t("registry.form.serverNameLeadingDigit"),
									},
								}}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("common.name")}</FormLabel>
										<FormControl>
											<Input id="client-name" data-testid="client-name-input" placeholder={t("registry.form.serverNamePlaceholder")} maxLength={50} {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>

							{/* Endpoint slug: optional on create, immutable after. Served at /mcp/<slug>. */}
							<FormField
								control={control}
								name="endpoint_slug"
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("registry.form.endpointSlug")}</FormLabel>
										<FormControl>
											<Input
												id="client-endpoint-slug"
												data-testid="client-endpoint-slug-input"
												placeholder={t("registry.form.endpointSlugPlaceholder")}
												{...field}
												value={field.value ?? ""}
											/>
										</FormControl>
										<p className="text-muted-foreground text-xs">{t("registry.form.endpointSlugHelp")}</p>
										<FormMessage />
									</FormItem>
								)}
							/>

							<DottedSeparator />

							<MCPClientFormFields form={methods} satellites={satellites} headersValidationError={headersValidationError} />
						</div>

						{/* Form Footer */}
						<div className="bg-card sticky bottom-0 z-10 flex justify-end gap-2 border-t px-4 py-4 md:px-8">
							<Button type="button" variant="outline" onClick={onClose} disabled={isLoading} data-testid="cancel-client-btn">
								{tc("cancel")}
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
												{tc("create")}
											</Button>
										</span>
									</TooltipTrigger>
									{!hasCreateMCPClientAccess && (
										<TooltipContent>
											<p>{t("common.noPermission")}</p>
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
						toast({ title: t("common.success"), description: t("registry.form.connectedOauth") });
						setOauthFlow(null);
						onClose();
						onSaved();
					}}
					onError={(error) => {
						toast({ title: t("registry.form.oauthError"), description: error, variant: "destructive" });
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
						toast({ title: t("common.success"), description: t("registry.form.connectedHeaders") });
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
					perUserHeaderKeys={satellites.perUserHeaderKeys}
					submitHandler={async (values) => {
						await createMCPClient({ ...headersFlow.payload, user_headers: values }).unwrap();
					}}
				/>
			)}
		</Sheet>
	);
};

export default ClientForm;