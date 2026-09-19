import { ModelAccessSelector } from "@/components/modelAccess";
import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from "@/components/ui/accordion";
import { FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { TagInput } from "@/components/ui/tagInput";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { hasCopilotApiToken, isRedacted } from "@/lib/utils/validation";
import { Info } from "lucide-react";
import { useEffect, useState } from "react";
import { Control, UseFormReturn } from "react-hook-form";
import { Trans, useTranslation } from "react-i18next";
import { DeploymentsTable } from "./deploymentsTable";

// Providers that support batch APIs
const BATCH_SUPPORTED_PROVIDERS = ["openai", "bedrock", "anthropic", "gemini", "azure", "vertex", "wafer"];

interface Props {
	control: Control<any>;
	providerName: string;
	// For custom providers, the underlying base provider type (e.g. "bedrock").
	// Drives which credential UI renders; falls back to providerName for native providers.
	baseProviderType?: string;
	form: UseFormReturn<any>;
}

// Batch API form field for all providers
function BatchAPIFormField({ control }: { control: Control<any>; form: UseFormReturn<any> }) {
	const { t } = useTranslation("models");
	return (
		<FormField
			control={control}
			name={`key.use_for_batch_api`}
			render={({ field }) => (
				<FormItem className="flex flex-row items-center justify-between rounded-sm border p-2">
					<div className="space-y-1.5">
						<FormLabel>{t("providers.keys.useForBatch")}</FormLabel>
						<FormDescription>{t("providers.keys.useForBatchHelp")}</FormDescription>
					</div>
					<FormControl>
						<Switch checked={field.value ?? false} onCheckedChange={field.onChange} />
					</FormControl>
				</FormItem>
			)}
		/>
	);
}

// AWS endpoint services Bifrost dials for Bedrock. `name` is the config field, `placeholder` the
// DNS name shape for that service - S3 differs from the rest, so each is spelled out.
const BEDROCK_VPC_ENDPOINT_SERVICES = [
	{
		name: "runtime",
		label: "Runtime",
		description: "Serves all inference.",
		placeholder: "vpce-0abc123-x1y2z3.bedrock-runtime.us-east-1.vpce.amazonaws.com",
	},
	{
		name: "control_plane",
		label: "Control Plane",
		description: "Serves model listing and batch jobs.",
		placeholder: "vpce-0abc123-x1y2z3.bedrock.us-east-1.vpce.amazonaws.com",
	},
	{
		name: "mantle",
		label: "Mantle",
		description: "Serves mantle-routed models.",
		placeholder: "vpce-0abc123-x1y2z3.bedrock-mantle.us-east-1.vpce.amazonaws.com",
	},
	{
		name: "agent_runtime",
		label: "Agent Runtime",
		description: "Serves rerank.",
		placeholder: "vpce-0abc123-x1y2z3.bedrock-agent-runtime.us-east-1.vpce.amazonaws.com",
	},
	{
		name: "s3",
		label: "S3",
		description: "Serves batch file I/O. Requires the bucket-prefixed endpoint name. A Gateway endpoint needs no value here.",
		placeholder: "bucket.vpce-0abc123-x1y2z3.s3.us-east-1.vpce.amazonaws.com",
	},
];

// VPC endpoint host overrides for AWS PrivateLink. Collapsed by default: most deployments reach
// Bedrock over the public regional endpoints and never set these.
function VPCEndpointsFormField({
	control,
	configKey,
	services,
}: {
	control: Control<any>;
	configKey: string;
	services: typeof BEDROCK_VPC_ENDPOINT_SERVICES;
}) {
	const { t } = useTranslation("models");
	return (
		<Accordion type="single" collapsible className="w-full">
			<AccordionItem value="vpc-endpoints" className="rounded-sm border px-2 last:border-b">
				<AccordionTrigger className="py-2 hover:no-underline" data-testid="bedrock-vpc-endpoints-trigger">
					<span className="block space-y-1.5 pr-2">
						<span className="block text-sm leading-none font-medium">{t("providers.keys.vpcEndpointsOptional")}</span>
						<span className="text-muted-foreground block text-sm font-normal">{t("providers.keys.vpcEndpointsHelp")}</span>
					</span>
				</AccordionTrigger>
				<AccordionContent className="space-y-4 pt-2 pb-3">
					{services.map((service) => (
						<FormField
							key={service.name}
							control={control}
							name={`${configKey}.endpoints.${service.name}`}
							render={({ field }) => (
								<FormItem>
									<FormLabel>{t(`providers.keys.vpc.${service.name}.label`)}</FormLabel>
									<FormDescription>{t(`providers.keys.vpc.${service.name}.description`)}</FormDescription>
									<FormControl>
										<SecretVarInput
											data-testid={`apikey-bedrock-endpoint-${service.name}-input`}
											placeholder={service.placeholder}
											{...field}
										/>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
					))}
				</AccordionContent>
			</AccordionItem>
		</Accordion>
	);
}

export function ApiKeyFormFragment({ control, providerName, baseProviderType, form }: Props) {
	const { t } = useTranslation("models");
	// Credential UI keys off the base provider type for custom providers; the
	// model list, deployments table, and API calls still use the real providerName.
	const effectiveProvider = baseProviderType ?? providerName;
	const isBedrock = effectiveProvider === "bedrock";
	const isBedrockMantle = effectiveProvider === "bedrock_mantle";
	const isVertex = effectiveProvider === "vertex";
	const isAzure = effectiveProvider === "azure";
	const isReplicate = effectiveProvider === "replicate";
	const isVLLM = effectiveProvider === "vllm";
	const isOllama = effectiveProvider === "ollama";
	const isSGL = effectiveProvider === "sgl";
	const isDeepseek = effectiveProvider === "deepseek";
	const isFireworks = effectiveProvider === "fireworks";
	const isDatabricks = effectiveProvider === "databricks";
	const isGithubCopilot = effectiveProvider === "github-copilot";
	// Reactive, so the App-credential labels stay truthful. Once a Copilot token is present
	// those fields genuinely are optional, and a static "(Required)" would contradict the
	// section note telling the operator they can leave them blank.
	const copilotAppSuffix = hasCopilotApiToken(form.watch("key.value"))
		? t("providers.optionalParen")
		: t("providers.requiredParen");
	const isKeylessProvider = isOllama || isSGL;
	const supportsBatchAPI = BATCH_SUPPORTED_PROVIDERS.includes(effectiveProvider);

	// Auth type state for Azure: 'api_key', 'entra_id', or 'default_credential'
	const [azureAuthType, setAzureAuthType] = useState<"api_key" | "entra_id" | "default_credential">("api_key");

	// Auth type state for Bedrock: 'iam_role', 'explicit', or 'api_key'
	const [bedrockAuthType, setBedrockAuthType] = useState<"iam_role" | "explicit" | "api_key">("iam_role");

	// Auth type state for Bedrock Mantle: 'iam_role', 'explicit', or 'api_key'
	const [bedrockMantleAuthType, setBedrockMantleAuthType] = useState<"iam_role" | "explicit" | "api_key">("iam_role");

	// Auth type state for Databricks: 'pat' (personal access token) or 'oauth_m2m' (service principal)
	const [databricksAuthType, setDatabricksAuthType] = useState<"pat" | "oauth_m2m">("pat");

	// Auth type state for Vertex: 'service_account', 'service_account_json', or 'api_key'
	const [vertexAuthType, setVertexAuthType] = useState<"service_account" | "service_account_json" | "api_key">("service_account");

	// Detect auth type from existing form values when editing
	useEffect(() => {
		if (form.formState.isDirty) return;
		if (isAzure) {
			const clientId = form.getValues("key.azure_key_config.client_id");
			const clientSecret = form.getValues("key.azure_key_config.client_secret");
			const tenantId = form.getValues("key.azure_key_config.tenant_id");
			const apiKey = form.getValues("key.value");
			const hasEntraField =
				clientId?.value || clientId?.ref || clientSecret?.value || clientSecret?.ref || tenantId?.value || tenantId?.ref;
			const hasApiKey = apiKey?.value || apiKey?.ref;
			let detected: "api_key" | "entra_id" | "default_credential" = "api_key";
			if (hasEntraField) {
				detected = "entra_id";
			} else if (!hasApiKey) {
				detected = "default_credential";
			}
			setAzureAuthType(detected);
			form.setValue("key.azure_key_config._auth_type", detected);
		}
	}, [isAzure, form]);

	useEffect(() => {
		if (form.formState.isDirty) return;
		if (isVertex) {
			const authCredentials = form.getValues("key.vertex_key_config.auth_credentials")?.value;
			const authCredentialsEnv = form.getValues("key.vertex_key_config.auth_credentials")?.ref;
			const apiKey = form.getValues("key.value")?.value;
			const apiKeyEnv = form.getValues("key.value")?.ref;
			let detected: "service_account" | "service_account_json" | "api_key" = "service_account";
			if (authCredentials || authCredentialsEnv) {
				detected = "service_account_json";
			} else if (apiKey || apiKeyEnv) {
				detected = "api_key";
			}
			setVertexAuthType(detected);
			form.setValue("key.vertex_key_config._auth_type", detected);
		}
	}, [isVertex, form]);

	const databricksDefaults = form.formState.defaultValues?.key?.databricks_key_config;
	useEffect(() => {
		if (form.formState.isDirty) return;
		if (isDatabricks) {
			const clientId = form.getValues("key.databricks_key_config.client_id");
			const clientSecret = form.getValues("key.databricks_key_config.client_secret");
			const hasServicePrincipal = clientId?.value || clientId?.ref || clientSecret?.value || clientSecret?.ref;
			const detected: "pat" | "oauth_m2m" = hasServicePrincipal ? "oauth_m2m" : "pat";
			setDatabricksAuthType(detected);
			form.setValue("key.databricks_key_config._auth_type", detected);
		}
		// databricksDefaults re-runs detection after the key form resets itself, which
		// happens once the key resolves - after this effect has already run once against
		// an empty form and settled on the personal access token tab.
	}, [isDatabricks, form, databricksDefaults]);

	useEffect(() => {
		if (form.formState.isDirty) return;
		if (isBedrock) {
			const accessKey = form.getValues("key.bedrock_key_config.access_key");
			const secretKey = form.getValues("key.bedrock_key_config.secret_key");
			const apiKey = form.getValues("key.value");
			const hasExplicitCreds = accessKey?.value || accessKey?.ref || secretKey?.value || secretKey?.ref;
			const hasApiKey = apiKey?.value || apiKey?.ref;
			let detected: "iam_role" | "explicit" | "api_key" = "iam_role";
			if (hasExplicitCreds) {
				detected = "explicit";
			} else if (hasApiKey) {
				detected = "api_key";
			}
			setBedrockAuthType(detected);
			form.setValue("key.bedrock_key_config._auth_type", detected);
		}
	}, [isBedrock, form]);

	useEffect(() => {
		if (form.formState.isDirty) return;
		if (isBedrockMantle) {
			const accessKey = form.getValues("key.bedrock_mantle_key_config.access_key");
			const secretKey = form.getValues("key.bedrock_mantle_key_config.secret_key");
			const apiKey = form.getValues("key.value");
			const hasExplicitCreds = accessKey?.value || accessKey?.ref || secretKey?.value || secretKey?.ref;
			const hasApiKey = apiKey?.value || apiKey?.ref;
			let detected: "iam_role" | "explicit" | "api_key" = "iam_role";
			if (hasExplicitCreds) {
				detected = "explicit";
			} else if (hasApiKey) {
				detected = "api_key";
			}
			setBedrockMantleAuthType(detected);
			form.setValue("key.bedrock_mantle_key_config._auth_type", detected);
		}
		// form.formState.defaultValues is a dependency so detection re-runs when ProviderKeyForm
		// repopulates an existing key via form.reset(...) after mount, not only on first render.
	}, [isBedrockMantle, form, form.formState.defaultValues]);

	return (
		<div data-tab="api-keys" className="space-y-4 overflow-hidden">
			<div className="flex items-start gap-4">
				<div className="flex-1">
					<FormField
						control={control}
						name={`key.name`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers.name")}</FormLabel>
								<FormControl>
									<Input placeholder={t("providers.keys.productionKeyPlaceholder")} type="text" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
				</div>
				<FormField
					control={control}
					name={`key.weight`}
					render={({ field }) => (
						<FormItem>
							<div className="flex items-center gap-2">
								<FormLabel>{t("providers.weight")}</FormLabel>
								<TooltipProvider>
									<Tooltip>
										<TooltipTrigger asChild>
											<span>
												<Info className="text-muted-foreground h-3 w-3" />
											</span>
										</TooltipTrigger>
										<TooltipContent className="max-w-sm">
											<p>{t("providers.keys.weightHelp")}</p>
										</TooltipContent>
									</Tooltip>
								</TooltipProvider>
							</div>
							<FormControl>
								<Input
									placeholder="1.0"
									className="w-[260px]"
									value={field.value === undefined || field.value === null ? "" : String(field.value)}
									onChange={(e) => {
										// Keep as string during typing to allow partial input
										field.onChange(e.target.value === "" ? "" : e.target.value);
									}}
									onBlur={(e) => {
										const v = e.target.value.trim();
										if (v !== "") {
											const num = parseFloat(v);
											if (!isNaN(num)) {
												field.onChange(num);
											}
										}
										field.onBlur();
									}}
									name={field.name}
									ref={field.ref}
									type="text"
								/>
							</FormControl>
							<FormMessage />
						</FormItem>
					)}
				/>
			</div>
			{/* Hide API Key field for providers with dedicated auth tabs */}
			{!isAzure && !isBedrock && !isBedrockMantle && !isVertex && !isDatabricks && (
				<FormField
					control={control}
					name={`key.value`}
					render={({ field }) => (
						<FormItem>
							<FormLabel>
								{isGithubCopilot ? t("providers.keys.copilotApiToken") : t("providers.apiKey")}{" "}
								{isVLLM || isGithubCopilot ? t("providers.optionalParen") : ""}
							</FormLabel>
							{isGithubCopilot && <FormDescription>{t("providers.keys.copilotTokenHelp")}</FormDescription>}
							<FormControl>
								<SecretVarInput
									placeholder={
										isGithubCopilot ? t("providers.keys.copilotTokenPlaceholder") : t("providers.keys.apiKeyOrEnv")
									}
									type="text"
									{...field}
								/>
							</FormControl>
							<FormMessage />
						</FormItem>
					)}
				/>
			)}
				<>
					<FormField
						control={control}
						name={`key.models`}
						render={({ field }) => (
							<FormItem>
								<FormControl>
									<ModelAccessSelector
										mode="allow"
										data-testid="api-keys-models-multiselect"
										provider={providerName}
										unfiltered
										value={field.value || []}
										onChange={field.onChange}
										label={
											<>
												<FormLabel>{t("providers.keys.allowedModels")}</FormLabel>
												<TooltipProvider>
													<Tooltip>
														<TooltipTrigger asChild>
															<span>
																<Info className="text-muted-foreground h-3 w-3" />
															</span>
														</TooltipTrigger>
														<TooltipContent className="max-w-sm">
															<p>{t("providers.keys.allowedModelsHelp")}</p>
														</TooltipContent>
													</Tooltip>
												</TooltipProvider>
											</>
										}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name={`key.blacklisted_models`}
						render={({ field }) => (
							<FormItem data-testid="apikey-blacklisted-models-field">
								<FormControl>
									<ModelAccessSelector
										mode="block"
										data-testid="api-keys-blocked-models-multiselect"
										provider={providerName}
										unfiltered
										value={field.value || []}
										onChange={field.onChange}
										label={
											<>
												<FormLabel>{t("providers.keys.blockedModels")}</FormLabel>
												<TooltipProvider>
													<Tooltip>
														<TooltipTrigger asChild>
															<span>
																<Info className="text-muted-foreground h-3 w-3" />
															</span>
														</TooltipTrigger>
														<TooltipContent className="max-w-sm">
															<p>{t("providers.keys.blockedModelsHelp")}</p>
														</TooltipContent>
													</Tooltip>
												</TooltipProvider>
											</>
										}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name={`key.aliases`}
						render={({ field }) => (
							<FormItem data-testid="apikey-deployments-field">
								<FormLabel>{t("providers.keys.deploymentsOptional")}</FormLabel>
								<FormDescription>
									{t("providers.keys.deploymentsHelp")}
									{isReplicate && <> {t("providers.keys.deploymentsHelpReplicate")}</>}
								</FormDescription>
								<FormControl>
									<div data-testid="apikey-deployments-table">
										<DeploymentsTable
											providerName={providerName}
											value={field.value}
											onChange={(next) => {
												form.clearErrors("key.aliases");
												field.onChange(Object.keys(next).length > 0 ? next : {});
											}}
										/>
									</div>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
				</>
			{supportsBatchAPI && !isBedrock && !isAzure && !isVertex && <BatchAPIFormField control={control} form={form} />}
			{isAzure && (
				<div className="space-y-4">
					<Separator className="my-6" />
					<div className="space-y-2">
						<FormLabel>{t("providers.keys.authMethod")}</FormLabel>
						<Tabs
							value={azureAuthType}
							onValueChange={(v) => {
								setAzureAuthType(v as "api_key" | "entra_id" | "default_credential");
								form.setValue("key.azure_key_config._auth_type", v, { shouldDirty: true, shouldValidate: true });
								if (v === "entra_id" || v === "default_credential") {
									// Clear API key when switching away from API Key
									form.setValue("key.value", undefined, { shouldDirty: true });
								}
								if (v === "api_key" || v === "default_credential") {
									// Clear Entra ID fields when switching away from Entra ID
									form.setValue("key.azure_key_config.client_id", undefined, { shouldDirty: true });
									form.setValue("key.azure_key_config.client_secret", undefined, { shouldDirty: true });
									form.setValue("key.azure_key_config.tenant_id", undefined, { shouldDirty: true });
									form.setValue("key.azure_key_config.scopes", undefined, { shouldDirty: true });
								}
							}}
						>
							<TabsList className="flex w-full justify-start">
								<TabsTrigger data-testid="apikey-azure-default-credential-tab" value="default_credential">
									{t("providers.keys.defaultCredential")}
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-azure-api-key-tab" value="api_key">
									{t("providers.apiKey")}
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-azure-entra-id-tab" value="entra_id">
									{t("providers.keys.entraId")}
								</TabsTrigger>
							</TabsList>
						</Tabs>
					</div>
					{azureAuthType === "api_key" && (
						<FormField
							control={control}
							name={`key.value`}
							render={({ field }) => (
								<FormItem>
									<FormLabel>
										{isVertex
											? t("providers.keys.apiKeyGeminiOnly")
											: isVLLM
												? `${t("providers.apiKey")} ${t("providers.optionalParen")}`
												: t("providers.apiKey")}
									</FormLabel>
									<FormControl>
										<SecretVarInput placeholder={t("providers.keys.apiKeyOrEnv")} type="text" {...field} />
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
					)}
					{azureAuthType === "default_credential" && (
						<p className="text-muted-foreground text-sm">
							{t("providers.keys.azureDefaultHelp")}
						</p>
					)}

					<FormField
						control={control}
						name={`key.azure_key_config.endpoint`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers.keys.endpointRequired")}</FormLabel>
								<FormControl>
									<SecretVarInput placeholder={t("providers.keys.azureEndpointPlaceholder")} {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					{azureAuthType === "entra_id" && (
						<>
							<FormField
								control={control}
								name={`key.azure_key_config.client_id`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers.keys.clientIdRequired")}</FormLabel>
										<FormControl>
											<SecretVarInput placeholder={t("providers.keys.azureClientIdPlaceholder")} {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.azure_key_config.client_secret`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers.keys.clientSecretRequired")}</FormLabel>
										<FormControl>
											<SecretVarInput placeholder={t("providers.keys.azureClientSecretPlaceholder")} {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.azure_key_config.tenant_id`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers.keys.tenantIdRequired")}</FormLabel>
										<FormControl>
											<SecretVarInput placeholder={t("providers.keys.azureTenantIdPlaceholder")} {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.azure_key_config.scopes`}
								render={({ field }) => (
									<FormItem>
										<div className="flex items-center gap-2">
											<FormLabel>{t("providers.keys.scopesOptional")}</FormLabel>
											<TooltipProvider>
												<Tooltip>
													<TooltipTrigger asChild>
														<span>
															<Info className="text-muted-foreground h-3 w-3" />
														</span>
													</TooltipTrigger>
													<TooltipContent>
														<p>{t("providers.keys.azureScopesHelp")}</p>
													</TooltipContent>
												</Tooltip>
											</TooltipProvider>
										</div>
										<FormControl>
											<TagInput
												data-testid="apikey-azure-scopes-input"
												placeholder={t("providers.keys.addScopePlaceholder")}
												value={field.value ?? []}
												onValueChange={field.onChange}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</>
					)}
					{supportsBatchAPI && <BatchAPIFormField control={control} form={form} />}
				</div>
			)}
			{isVertex && (
				<div className="space-y-4">
					<Separator className="my-6" />
					<div className="space-y-2">
						<FormLabel>{t("providers.keys.authMethod")}</FormLabel>
						<Tabs
							value={vertexAuthType}
							onValueChange={(v) => {
								setVertexAuthType(v as "service_account" | "service_account_json" | "api_key");
								form.setValue("key.vertex_key_config._auth_type", v, { shouldDirty: true, shouldValidate: true });
								if (v === "service_account" || v === "api_key") {
									// Clear auth credentials when switching away from service account JSON
									form.setValue("key.vertex_key_config.auth_credentials", undefined, { shouldDirty: true });
								}
								if (v === "service_account" || v === "service_account_json") {
									// Clear API key when switching away from API Key
									form.setValue("key.value", undefined, { shouldDirty: true });
								}
							}}
						>
							<TabsList className="flex w-full justify-start">
								<TabsTrigger data-testid="apikey-vertex-service-account-tab" value="service_account">
									{t("providers.keys.serviceAccountAttached")}
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-vertex-service-account-json-tab" value="service_account_json">
									{t("providers.keys.serviceAccountJson")}
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-vertex-api-key-tab" value="api_key">
									{t("providers.apiKey")}
								</TabsTrigger>
							</TabsList>
						</Tabs>
						{vertexAuthType === "service_account" && (
							<p className="text-muted-foreground text-sm">
								{t("providers.keys.vertexAttachedHelp")}
							</p>
						)}
					</div>

					<FormField
						control={control}
						name={`key.vertex_key_config.project_id`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers.keys.projectIdRequired")}</FormLabel>
								<FormControl>
									<SecretVarInput placeholder={t("providers.keys.vertexProjectIdPlaceholder")} {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name={`key.vertex_key_config.project_number`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers.keys.projectNumberFineTuned")}</FormLabel>
								<FormControl>
									<SecretVarInput placeholder={t("providers.keys.vertexProjectNumberPlaceholder")} {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name={`key.vertex_key_config.region`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers.keys.regionRequired")}</FormLabel>
								<FormDescription>
									<Trans
										t={t}
										i18nKey="providers.keys.vertexRegionHelp"
										components={{ medium: <span className="font-medium" /> }}
									/>
								</FormDescription>
								<FormControl>
									<SecretVarInput placeholder={t("providers.keys.vertexRegionPlaceholder")} {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>

					{vertexAuthType === "service_account_json" && (
						<FormField
							control={control}
							name={`key.vertex_key_config.auth_credentials`}
							render={({ field }) => (
								<FormItem>
									<FormLabel>{t("providers.keys.authCredentialsRequired")}</FormLabel>
									<FormDescription>{t("providers.keys.authCredentialsHelp")}</FormDescription>
									<FormControl>
										<SecretVarInput
											data-testid="apikey-vertex-auth-credentials-input"
											variant="textarea"
											rows={4}
											placeholder={t("providers.keys.vertexCredsPlaceholder")}
											inputClassName="font-mono text-sm"
											{...field}
										/>
									</FormControl>
									{isRedacted(field.value?.value ?? "") && (
										<div className="text-muted-foreground mt-1 flex items-center gap-1 text-xs">
											<Info className="h-3 w-3" />
											<span>{t("providers.keys.credsStoredSecurely")}</span>
										</div>
									)}
									<FormMessage />
								</FormItem>
							)}
						/>
					)}

					{vertexAuthType === "api_key" && (
						<FormField
							control={control}
							name={`key.value`}
							render={({ field }) => (
								<FormItem>
									<FormLabel>{t("providers.keys.apiKeyGeminiOnly")}</FormLabel>
									<FormControl>
										<SecretVarInput data-testid="apikey-vertex-api-key-input" placeholder={t("providers.keys.apiKeyOrEnv")} type="text" {...field} />
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
					)}
					<FormField
						control={control}
						name="key.vertex_key_config.force_single_region"
						render={({ field }) => (
							<FormItem className="flex flex-row items-center justify-between rounded-sm border p-2">
								<div className="space-y-1.5">
									<FormLabel>{t("providers.keys.forceSingleRegion")}</FormLabel>
									<FormDescription>{t("providers.keys.forceSingleRegionHelp")}</FormDescription>
								</div>
								<FormControl>
									<Switch checked={field.value ?? false} onCheckedChange={field.onChange} />
								</FormControl>
							</FormItem>
						)}
					/>
					{supportsBatchAPI && <BatchAPIFormField control={control} form={form} />}
				</div>
			)}
			{isReplicate && (
				<div className="space-y-4">
					<Separator className="my-6" />
					<FormField
						control={control}
						name="key.replicate_key_config.use_deployments_endpoint"
						render={({ field }) => (
							<FormItem className="flex flex-row items-center justify-between rounded-sm border p-2">
								<div className="space-y-1.5">
									<FormLabel>{t("providers.keys.useDeploymentsEndpoint")}</FormLabel>
									<FormDescription>
										<Trans t={t} i18nKey="providers.keys.useDeploymentsEndpointHelp" components={{ strong: <strong /> }} />
									</FormDescription>
									<FormMessage />
								</div>
								<FormControl>
									<Switch checked={field.value ?? false} onCheckedChange={field.onChange} />
								</FormControl>
							</FormItem>
						)}
					/>
				</div>
			)}
			{isVLLM && (
				<div className="space-y-4">
					<Separator className="my-6" />
					<FormField
						control={control}
						name="key.vllm_key_config.url"
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers.keys.serverUrlRequired")}</FormLabel>
								<FormDescription>{t("providers.keys.vllmUrlHelp")}</FormDescription>
								<FormControl>
									<SecretVarInput data-testid="key-input-vllm-url" placeholder={t("providers.keys.vllmUrlPlaceholder")} {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name="key.vllm_key_config.model_name"
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers.keys.modelNameRequired")}</FormLabel>
								<FormDescription>{t("providers.keys.vllmModelHelp")}</FormDescription>
								<FormControl>
									<Input data-testid="key-input-vllm-model-name" placeholder={t("providers.keys.vllmModelPlaceholder")} {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
				</div>
			)}
			{isDatabricks && (
				<div className="space-y-4">
					<FormField
						control={control}
						name="key.databricks_key_config.workspace_url"
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers.keys.workspaceUrlRequired")}</FormLabel>
								<FormDescription>{t("providers.keys.databricksWorkspaceHelp")}</FormDescription>
								<FormControl>
									<SecretVarInput
										data-testid="key-input-databricks-workspace-url"
										placeholder={t("providers.keys.databricksWorkspacePlaceholder")}
										{...field}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name="key.databricks_key_config.api_format"
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers.keys.inferenceSurface")}</FormLabel>
								<FormDescription>{t("providers.keys.inferenceSurfaceHelp")}</FormDescription>
								<Select value={field.value ?? "auto"} onValueChange={field.onChange}>
									<FormControl>
										<SelectTrigger data-testid="key-select-databricks-api-format">
											<SelectValue placeholder={t("providers.keys.autoPlaceholder")} />
										</SelectTrigger>
									</FormControl>
									<SelectContent>
										<SelectItem value="auto">{t("providers.keys.autoByModelName")}</SelectItem>
										<SelectItem value="model_serving">{t("providers.keys.modelServingPath")}</SelectItem>
										<SelectItem value="ai_gateway">{t("providers.keys.unityAiGatewayPath")}</SelectItem>
									</SelectContent>
								</Select>
								<FormMessage />
							</FormItem>
						)}
					/>
					<Separator className="my-6" />
					<div className="space-y-2">
						<FormLabel>{t("providers.keys.authMethod")}</FormLabel>
						<Tabs
							value={databricksAuthType}
							onValueChange={(v) => {
								setDatabricksAuthType(v as "pat" | "oauth_m2m");
								form.setValue("key.databricks_key_config._auth_type", v, { shouldDirty: true, shouldValidate: true });
								if (v === "oauth_m2m") {
									// The token and the service principal are alternatives, never both.
									form.setValue("key.value", undefined, { shouldDirty: true });
								} else {
									form.setValue("key.databricks_key_config.client_id", undefined, { shouldDirty: true });
									form.setValue("key.databricks_key_config.client_secret", undefined, { shouldDirty: true });
								}
							}}
						>
							<TabsList className="grid w-full grid-cols-2">
								<TabsTrigger data-testid="apikey-databricks-pat-tab" value="pat">
									{t("providers.keys.personalAccessToken")}
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-databricks-oauth-tab" value="oauth_m2m">
									{t("providers.keys.oauthM2m")}
								</TabsTrigger>
							</TabsList>
						</Tabs>
					</div>
					{databricksAuthType === "pat" && (
						<FormField
							control={control}
							name="key.value"
							render={({ field }) => (
								<FormItem>
									<FormLabel>{t("providers.keys.personalAccessToken")}</FormLabel>
									<FormDescription>{t("providers.keys.patHelp")}</FormDescription>
									<FormControl>
										<SecretVarInput
											data-testid="key-input-databricks-pat"
											placeholder={t("providers.keys.patPlaceholder")}
											type="text"
											{...field}
										/>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
					)}
					{databricksAuthType === "oauth_m2m" && (
						<>
							<p className="text-muted-foreground text-sm">
								{t("providers.keys.oauthM2mHelp")}
							</p>
							<FormField
								control={control}
								name="key.databricks_key_config.client_id"
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers.keys.clientId")}</FormLabel>
										<FormControl>
											<SecretVarInput
												data-testid="key-input-databricks-client-id"
												placeholder={t("providers.keys.databricksClientIdPlaceholder")}
												type="text"
												{...field}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name="key.databricks_key_config.client_secret"
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers.keys.clientSecret")}</FormLabel>
										<FormControl>
											<SecretVarInput
												data-testid="key-input-databricks-client-secret"
												placeholder={t("providers.keys.databricksClientSecretPlaceholder")}
												type="text"
												{...field}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</>
					)}
					<FormField
						control={control}
						name="key.databricks_key_config.forward_gateway_tags"
						render={({ field }) => (
							<FormItem className="flex flex-row items-center justify-between rounded-sm border p-2">
								<div className="space-y-1.5">
									<FormLabel htmlFor="databricks-forward-gateway-tags-switch">{t("providers.keys.forwardGovernanceTags")}</FormLabel>
									<FormDescription>{t("providers.keys.forwardGovernanceTagsHelp")}</FormDescription>
								</div>
								<FormControl>
									<Switch id="databricks-forward-gateway-tags-switch" checked={field.value ?? false} onCheckedChange={field.onChange} />
								</FormControl>
							</FormItem>
						)}
					/>
				</div>
			)}
			{isKeylessProvider && (
				<div className="space-y-4">
					<FormField
						control={control}
						name={`key.${isOllama ? "ollama_key_config" : "sgl_key_config"}.url`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers.keys.serverUrlRequired")}</FormLabel>
								<FormDescription>
									{t("providers.keys.serverUrlHelp", {
										server: isOllama ? "Ollama" : "SGLang",
										example: isOllama ? "http://localhost:11434" : "http://localhost:30000",
										envVar: isOllama ? "env.OLLAMA_URL" : "env.SGL_URL",
									})}
								</FormDescription>
								<FormControl>
									<SecretVarInput
										data-testid={`key-input-${isOllama ? "ollama" : "sgl"}-url`}
										placeholder={isOllama ? "http://localhost:11434" : "http://localhost:30000"}
										{...field}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
				</div>
			)}
			{isGithubCopilot && (
				<div className="space-y-4">
					<Separator />
					<div className="bg-muted/50 flex items-start gap-2 rounded-md border p-3">
						<Info className="text-muted-foreground mt-0.5 h-4 w-4 shrink-0" />
						<p className="text-muted-foreground text-sm">
							<Trans
								t={t}
								i18nKey="providers.keys.copilotEither"
								components={{
									strong: <strong />,
									appLink: (
										<a
											href="https://docs.github.com/en/copilot/how-tos/copilot-sdk/auth/server-to-server-tokens"
											target="_blank"
											rel="noopener noreferrer"
											className="text-primary hover:underline"
											data-testid="copilot-docs-link-server-to-server"
										/>
									),
									tokenLink: (
										<a
											href="https://docs.github.com/en/copilot/how-tos/copilot-sdk/authenticate-copilot-sdk/authenticate-copilot-sdk"
											target="_blank"
											rel="noopener noreferrer"
											className="text-primary hover:underline"
											data-testid="copilot-docs-link-api-token"
										/>
									),
								}}
							/>
						</p>
					</div>
					<div className="space-y-1.5">
						{/* Label, not FormLabel: this heads a section rather than labelling one
						    control, so there is no FormItem id for htmlFor to point at. */}
						<Label>{t("providers.keys.githubAppCredentials")}</Label>
						<p className="text-muted-foreground text-sm">{t("providers.keys.githubAppHelp")}</p>
					</div>
					<FormField
						control={control}
						name="key.github_copilot_key_config.app_id"
						render={({ field }) => (
							<FormItem>
								<FormLabel>
									{t("providers.keys.appId")} {copilotAppSuffix}
								</FormLabel>
								<FormDescription>
									<Trans
										t={t}
										i18nKey="providers.keys.appIdHelp"
										components={{
											link: (
												<a
													href="https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/registering-a-github-app"
													target="_blank"
													rel="noopener noreferrer"
													className="text-primary hover:underline"
													data-testid="copilot-docs-link-create-app"
												/>
											),
										}}
									/>
								</FormDescription>
								<FormControl>
									<SecretVarInput data-testid="key-input-copilot-app-id" placeholder={t("providers.keys.copilotAppIdPlaceholder")} {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name="key.github_copilot_key_config.installation_id"
						render={({ field }) => (
							<FormItem>
								<FormLabel>
									{t("providers.keys.installationId")} {copilotAppSuffix}
								</FormLabel>
								<FormDescription>
									<Trans
										t={t}
										i18nKey="providers.keys.installationIdHelp"
										components={{
											link: (
												<a
													href="https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-an-installation-access-token-for-a-github-app"
													target="_blank"
													rel="noopener noreferrer"
													className="text-primary hover:underline"
													data-testid="copilot-docs-link-installation-id"
												/>
											),
										}}
									/>
								</FormDescription>
								<FormControl>
									<SecretVarInput
										data-testid="key-input-copilot-installation-id"
										placeholder={t("providers.keys.copilotInstallationIdPlaceholder")}
										{...field}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name="key.github_copilot_key_config.repository_id"
						render={({ field }) => (
							<FormItem>
								<FormLabel>
									{t("providers.keys.repositoryId")} {copilotAppSuffix}
								</FormLabel>
								<FormDescription>{t("providers.keys.repositoryIdHelp")}</FormDescription>
								<FormControl>
									<SecretVarInput
										data-testid="key-input-copilot-repository-id"
										placeholder={t("providers.keys.copilotRepositoryIdPlaceholder")}
										{...field}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name="key.github_copilot_key_config.private_key"
						render={({ field }) => (
							<FormItem>
								<FormLabel>
									{t("providers.keys.privateKey")} {copilotAppSuffix}
								</FormLabel>
								<FormDescription>{t("providers.keys.privateKeyHelp")}</FormDescription>
								<FormControl>
									<SecretVarInput
										data-testid="key-input-copilot-private-key"
										variant="textarea"
										rows={4}
										placeholder={t("providers.keys.copilotPrivateKeyPlaceholder")}
										{...field}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name="key.github_copilot_key_config.github_domain"
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers.keys.githubEnterpriseDomain")}</FormLabel>
								<FormDescription>{t("providers.keys.githubEnterpriseHelp")}</FormDescription>
								<FormControl>
									<SecretVarInput data-testid="key-input-copilot-github-domain" placeholder={t("providers.keys.copilotDomainPlaceholder")} {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
				</div>
			)}
			{(isSGL || isDeepseek || isFireworks || isVLLM) && (
				<div className="space-y-4">
					<FormField
						control={control}
						name="key.use_anthropic_endpoints"
						render={({ field }) => (
							<FormItem className="flex flex-row items-center justify-between rounded-sm border p-2">
								<div className="space-y-1.5">
									<FormLabel htmlFor="use-anthropic-endpoints-alias-override-switch">{t("providers.keys.useAnthropicEndpoints")}</FormLabel>
									<FormDescription>{t("providers.keys.useAnthropicEndpointsHelp")}</FormDescription>
								</div>
								<FormControl>
									<Switch
										id="use-anthropic-endpoints-alias-override-switch"
										checked={field.value ?? false}
										onCheckedChange={field.onChange}
									/>
								</FormControl>
							</FormItem>
						)}
					/>
				</div>
			)}
			{isBedrock && (
				<div className="space-y-4">
					<FormField
						control={control}
						name="key.use_openai_endpoints"
						render={({ field }) => (
							<FormItem className="flex flex-row items-center justify-between rounded-sm border p-2">
								<div className="space-y-1.5">
									<FormLabel htmlFor="use-openai-endpoints-switch">{t("providers.keys.useOpenaiEndpoints")}</FormLabel>
									<FormDescription>{t("providers.keys.useOpenaiEndpointsHelp")}</FormDescription>
								</div>
								<FormControl>
									<Switch
										id="use-openai-endpoints-switch"
										data-testid="key-switch-bedrock-use-openai-endpoints"
										checked={field.value ?? false}
										onCheckedChange={field.onChange}
									/>
								</FormControl>
							</FormItem>
						)}
					/>
					<Separator className="my-6" />
					<div className="space-y-2">
						<FormLabel>{t("providers.keys.authMethod")}</FormLabel>
						<Tabs
							value={bedrockAuthType}
							onValueChange={(v) => {
								setBedrockAuthType(v as "iam_role" | "explicit" | "api_key");
								form.setValue("key.bedrock_key_config._auth_type", v, { shouldDirty: true, shouldValidate: true });
								if (v === "iam_role") {
									// Clear explicit credentials and API key when switching to IAM Role
									form.setValue("key.bedrock_key_config.access_key", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_key_config.secret_key", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_key_config.session_token", undefined, { shouldDirty: true });
									form.setValue("key.value", undefined, { shouldDirty: true });
								} else if (v === "explicit") {
									// Clear API key when switching to Explicit Credentials
									form.setValue("key.value", undefined, { shouldDirty: true });
								} else if (v === "api_key") {
									// Clear AWS credentials and assume-role fields when switching to API Key
									form.setValue("key.bedrock_key_config.access_key", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_key_config.secret_key", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_key_config.session_token", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_key_config.role_arn", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_key_config.external_id", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_key_config.session_name", undefined, { shouldDirty: true });
								}
							}}
						>
							<TabsList className="flex w-full justify-start">
								<TabsTrigger data-testid="apikey-bedrock-iam-role-tab" value="iam_role">
									{t("providers.keys.iamRoleInherited")}
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-bedrock-explicit-credentials-tab" value="explicit">
									{t("providers.keys.explicitCredentials")}
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-bedrock-api-key-tab" value="api_key">
									{t("providers.apiKey")}
								</TabsTrigger>
							</TabsList>
						</Tabs>
						{bedrockAuthType === "iam_role" && (
							<p className="text-muted-foreground text-sm">{t("providers.keys.iamRoleHelp")}</p>
						)}
						{bedrockAuthType === "api_key" && (
							<p className="text-muted-foreground text-sm">{t("providers.keys.bearerApiKeyHelp")}</p>
						)}
					</div>

					{bedrockAuthType === "explicit" && (
						<>
							<FormField
								control={control}
								name={`key.bedrock_key_config.access_key`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers.keys.accessKeyRequired")}</FormLabel>
										<FormControl>
											<SecretVarInput placeholder={t("providers.keys.awsAccessKeyPlaceholder")} {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.bedrock_key_config.secret_key`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers.keys.secretKeyRequired")}</FormLabel>
										<FormControl>
											<SecretVarInput placeholder={t("providers.keys.awsSecretKeyPlaceholder")} {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.bedrock_key_config.session_token`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers.keys.sessionTokenOptional")}</FormLabel>
										<FormControl>
											<SecretVarInput placeholder={t("providers.keys.awsSessionTokenPlaceholder")} {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</>
					)}

					{bedrockAuthType === "api_key" && (
						<FormField
							control={control}
							name={`key.value`}
							render={({ field }) => (
								<FormItem>
									<FormLabel>{t("providers.apiKey")}</FormLabel>
									<FormControl>
										<SecretVarInput
											data-testid="apikey-bedrock-api-key-input"
											placeholder={t("providers.keys.bedrockApiKeyPlaceholder")}
											type="text"
											{...field}
										/>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
					)}

					<FormField
						control={control}
						name={`key.bedrock_key_config.region`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers.keys.regionRequired")}</FormLabel>
								<FormControl>
									<SecretVarInput placeholder={t("providers.keys.awsRegionPlaceholder")} {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name={`key.bedrock_key_config.project_id`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers.keys.mantleProjectIdOptional")}</FormLabel>
								<FormDescription>{t("providers.keys.mantleProjectIdHelp")}</FormDescription>
								<FormControl>
									<SecretVarInput
										data-testid="apikey-bedrock-project-id-input"
										placeholder={t("providers.keys.projectIdPlaceholder")}
										{...field}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					{bedrockAuthType !== "api_key" && (
						<>
							<FormField
								control={control}
								name={`key.bedrock_key_config.role_arn`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers.keys.assumeRoleArnOptional")}</FormLabel>
										<FormDescription>
											{t("providers.keys.assumeRoleHelp")}
										</FormDescription>
										<FormControl>
											<SecretVarInput
												data-testid="apikey-bedrock-role-arn-input"
												placeholder={t("providers.keys.roleArnPlaceholder")}
												{...field}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.bedrock_key_config.external_id`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers.keys.externalIdOptional")}</FormLabel>
										<FormDescription>{t("providers.keys.externalIdHelp")}</FormDescription>
										<FormControl>
											<SecretVarInput
												data-testid="apikey-bedrock-external-id-input"
												placeholder={t("providers.keys.externalIdPlaceholder")}
												{...field}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.bedrock_key_config.session_name`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers.keys.sessionNameOptional")}</FormLabel>
										<FormDescription>{t("providers.keys.sessionNameHelp")}</FormDescription>
										<FormControl>
											<SecretVarInput
												data-testid="apikey-bedrock-session-name-input"
												placeholder={t("providers.keys.sessionNamePlaceholder")}
												{...field}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</>
					)}
					<FormField
						control={control}
						name={`key.bedrock_key_config.arn`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers.keys.arnOptional")}</FormLabel>
								<FormControl>
									<SecretVarInput placeholder={t("providers.keys.arnPlaceholder")} {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					{supportsBatchAPI && (
						<FormField
							control={control}
							name={`key.bedrock_key_config.batch_role_arn`}
							render={({ field }) => (
								<FormItem>
									<FormLabel>{t("providers.keys.batchRoleArnOptional")}</FormLabel>
									<FormDescription>{t("providers.keys.batchRoleArnHelp")}</FormDescription>
									<FormControl>
										<SecretVarInput
											data-testid="apikey-bedrock-batch-role-arn-input"
											placeholder={t("providers.keys.batchRoleArnPlaceholder")}
											{...field}
										/>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
					)}
					{supportsBatchAPI && <BatchAPIFormField control={control} form={form} />}
					<VPCEndpointsFormField control={control} configKey="key.bedrock_key_config" services={BEDROCK_VPC_ENDPOINT_SERVICES} />
				</div>
			)}

			{isBedrockMantle && (
				<div className="space-y-4">
					<Separator className="my-6" />
					<div className="space-y-2">
						<FormLabel>{t("providers.keys.authMethod")}</FormLabel>
						<Tabs
							value={bedrockMantleAuthType}
							onValueChange={(v) => {
								setBedrockMantleAuthType(v as "iam_role" | "explicit" | "api_key");
								form.setValue("key.bedrock_mantle_key_config._auth_type", v, { shouldDirty: true, shouldValidate: true });
								if (v === "iam_role") {
									// Clear explicit credentials and API key when switching to IAM Role
									form.setValue("key.bedrock_mantle_key_config.access_key", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_mantle_key_config.secret_key", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_mantle_key_config.session_token", undefined, { shouldDirty: true });
									form.setValue("key.value", undefined, { shouldDirty: true });
								} else if (v === "explicit") {
									// Clear API key when switching to Explicit Credentials
									form.setValue("key.value", undefined, { shouldDirty: true });
								} else if (v === "api_key") {
									// Clear AWS credentials and assume-role fields when switching to API Key
									form.setValue("key.bedrock_mantle_key_config.access_key", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_mantle_key_config.secret_key", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_mantle_key_config.session_token", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_mantle_key_config.role_arn", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_mantle_key_config.external_id", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_mantle_key_config.session_name", undefined, { shouldDirty: true });
								}
							}}
						>
							<TabsList className="flex w-full justify-start">
								<TabsTrigger data-testid="apikey-bedrock-mantle-iam-role-tab" value="iam_role">
									{t("providers.keys.iamRoleInherited")}
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-bedrock-mantle-explicit-credentials-tab" value="explicit">
									{t("providers.keys.explicitCredentials")}
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-bedrock-mantle-api-key-tab" value="api_key">
									{t("providers.apiKey")}
								</TabsTrigger>
							</TabsList>
						</Tabs>
						{bedrockMantleAuthType === "iam_role" && (
							<p className="text-muted-foreground text-sm">{t("providers.keys.iamRoleHelp")}</p>
						)}
						{bedrockMantleAuthType === "api_key" && (
							<p className="text-muted-foreground text-sm">{t("providers.keys.mantleApiKeyHelp")}</p>
						)}
					</div>

					{bedrockMantleAuthType === "explicit" && (
						<>
							<FormField
								control={control}
								name={`key.bedrock_mantle_key_config.access_key`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers.keys.accessKeyRequired")}</FormLabel>
										<FormControl>
											<SecretVarInput placeholder={t("providers.keys.awsAccessKeyPlaceholder")} {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.bedrock_mantle_key_config.secret_key`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers.keys.secretKeyRequired")}</FormLabel>
										<FormControl>
											<SecretVarInput placeholder={t("providers.keys.awsSecretKeyPlaceholder")} {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.bedrock_mantle_key_config.session_token`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers.keys.sessionTokenOptional")}</FormLabel>
										<FormControl>
											<SecretVarInput placeholder={t("providers.keys.awsSessionTokenPlaceholder")} {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</>
					)}

					{bedrockMantleAuthType === "api_key" && (
						<FormField
							control={control}
							name={`key.value`}
							render={({ field }) => (
								<FormItem>
									<FormLabel>{t("providers.apiKey")}</FormLabel>
									<FormControl>
										<SecretVarInput
											data-testid="apikey-bedrock-mantle-api-key-input"
											placeholder={t("providers.keys.bedrockMantleApiKeyPlaceholder")}
											type="text"
											{...field}
										/>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
					)}

					<FormField
						control={control}
						name={`key.bedrock_mantle_key_config.region`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers.keys.regionRequired")}</FormLabel>
								<FormControl>
									<SecretVarInput placeholder={t("providers.keys.awsRegionPlaceholder")} {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>

					<FormField
						control={control}
						name={`key.bedrock_mantle_key_config.project_id`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers.keys.projectIdOptional")}</FormLabel>
								<FormDescription>{t("providers.keys.mantleProjectHelp")}</FormDescription>
								<FormControl>
									<SecretVarInput
										data-testid="apikey-bedrock-mantle-project-id-input"
										placeholder={t("providers.keys.projectIdPlaceholder")}
										{...field}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>

					{bedrockMantleAuthType !== "api_key" && (
						<>
							<FormField
								control={control}
								name={`key.bedrock_mantle_key_config.role_arn`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers.keys.assumeRoleArnOptional")}</FormLabel>
										<FormDescription>
											{t("providers.keys.assumeRoleHelp")}
										</FormDescription>
										<FormControl>
											<SecretVarInput placeholder={t("providers.keys.roleArnPlaceholder")} {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.bedrock_mantle_key_config.external_id`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers.keys.externalIdOptional")}</FormLabel>
										<FormDescription>{t("providers.keys.externalIdHelpPeriod")}</FormDescription>
										<FormControl>
											<SecretVarInput placeholder={t("providers.keys.externalIdPlaceholder")} {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.bedrock_mantle_key_config.session_name`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers.keys.sessionNameOptional")}</FormLabel>
										<FormDescription>{t("providers.keys.sessionNameHelpPeriod")}</FormDescription>
										<FormControl>
											<SecretVarInput placeholder={t("providers.keys.sessionNamePlaceholder")} {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</>
					)}
					<VPCEndpointsFormField
						control={control}
						configKey="key.bedrock_mantle_key_config"
						services={BEDROCK_VPC_ENDPOINT_SERVICES.filter((s) => s.name === "mantle")}
					/>
				</div>
			)}
		</div>
	);
}
