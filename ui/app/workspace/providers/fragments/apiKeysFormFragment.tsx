import { ModelAccessSelector } from "@/components/modelAccess";
import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from "@/components/ui/accordion";
import { FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { DefaultVertexAWSWorkloadIdentityConfig, OAuthSigningAlgorithms } from "@/lib/types/config";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { TagInput } from "@/components/ui/tagInput";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { isRedacted } from "@/lib/utils/validation";
import { Info } from "lucide-react";
import { useEffect, useState } from "react";
import { Control, UseFormReturn } from "react-hook-form";
import { Trans, useTranslation } from "react-i18next";
import { DeploymentsTable } from "./deploymentsTable";
import { GithubCopilotSignIn } from "./githubCopilotSignIn";

// Providers that support batch APIs
// Vertex authentication methods offered by the key form. The backend picks the mode from which
// fields are set; this discriminator only drives the tabs and validation.
type VertexAuthType = "service_account" | "service_account_json" | "api_key" | "aws_workload_identity";

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
		label: "Runtime", // i18n-ignore
		description: "Serves all inference.", // i18n-ignore
		placeholder: "vpce-0abc123-x1y2z3.bedrock-runtime.us-east-1.vpce.amazonaws.com",
	},
	{
		name: "control_plane",
		label: "Control Plane", // i18n-ignore
		description: "Serves model listing and batch jobs.", // i18n-ignore
		placeholder: "vpce-0abc123-x1y2z3.bedrock.us-east-1.vpce.amazonaws.com",
	},
	{
		name: "mantle",
		label: "Mantle", // i18n-ignore
		description: "Serves mantle-routed models.", // i18n-ignore
		placeholder: "vpce-0abc123-x1y2z3.bedrock-mantle.us-east-1.vpce.amazonaws.com",
	},
	{
		name: "agent_runtime",
		label: "Agent Runtime", // i18n-ignore
		description: "Serves rerank.", // i18n-ignore
		placeholder: "vpce-0abc123-x1y2z3.bedrock-agent-runtime.us-east-1.vpce.amazonaws.com",
	},
	{
		name: "s3",
		label: "S3", // i18n-ignore
		description: "Serves batch file I/O. Requires the bucket-prefixed endpoint name. A Gateway endpoint needs no value here.", // i18n-ignore
		placeholder: "bucket.vpce-0abc123-x1y2z3.s3.us-east-1.vpce.amazonaws.com",
	},
];

// The one runtime host SageMaker endpoints are invoked on; an interface VPC endpoint, FIPS or
// dual-stack host can replace it.
const SAGEMAKER_VPC_ENDPOINT_SERVICES = [
	{
		name: "runtime",
		label: "Runtime", // i18n-ignore
		description: "Serves all endpoint invocations.", // i18n-ignore
		placeholder: "vpce-0abc123-x1y2z3.runtime.sagemaker.us-east-1.vpce.amazonaws.com",
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
	const isSageMaker = effectiveProvider === "sagemaker";
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
	const isCloudflare = effectiveProvider === "cloudflare";
	// openai, and every custom provider built on the openai base, may mint its bearer from
	// oauth_key_config instead of carrying a static API key.
	const isOpenAICompatible = effectiveProvider === "openai";
	const isKeylessProvider = isOllama || isSGL;
	const supportsBatchAPI = BATCH_SUPPORTED_PROVIDERS.includes(effectiveProvider);

	// Auth type state for Azure: 'api_key', 'entra_id', or 'default_credential'
	const [azureAuthType, setAzureAuthType] = useState<"api_key" | "entra_id" | "default_credential">("api_key");

	// Auth type state for Bedrock: 'iam_role', 'explicit', or 'api_key'
	const [bedrockAuthType, setBedrockAuthType] = useState<"iam_role" | "explicit" | "api_key">("iam_role");

	// Auth type state for Bedrock Mantle: 'iam_role', 'explicit', or 'api_key'
	const [bedrockMantleAuthType, setBedrockMantleAuthType] = useState<"iam_role" | "explicit" | "api_key">("iam_role");

	// Auth type state for SageMaker: 'iam_role' (default AWS credential chain), 'explicit', or 'api_key' (pre-minted token)
	const [sageMakerAuthType, setSageMakerAuthType] = useState<"iam_role" | "explicit" | "api_key">("iam_role");

	// Auth type state for Databricks: 'pat' (personal access token) or 'oauth_m2m' (service principal)
	const [databricksAuthType, setDatabricksAuthType] = useState<"pat" | "oauth_m2m">("pat");

	// Auth type state for openai-based keys: a static API key, or an OAuth-minted bearer
	const [openaiAuthType, setOpenaiAuthType] = useState<"api_key" | "oauth">("api_key");
	const oauthGrantType = form.watch("key.oauth_key_config.grant_type") ?? "client_credentials";

	// Auth type state for Vertex: 'service_account' (ADC), 'service_account_json', 'api_key', or
	// 'aws_workload_identity' (GCP Workload Identity Federation from the workload's AWS identity)
	const [vertexAuthType, setVertexAuthType] = useState<VertexAuthType>("service_account");

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
			const wifAudience = form.getValues("key.vertex_key_config.aws_workload_identity.audience");
			const authCredentials = form.getValues("key.vertex_key_config.auth_credentials")?.value;
			const authCredentialsEnv = form.getValues("key.vertex_key_config.auth_credentials")?.ref;
			const apiKey = form.getValues("key.value")?.value;
			const apiKeyEnv = form.getValues("key.value")?.ref;
			let detected: VertexAuthType = "service_account";
			if (wifAudience?.value || wifAudience?.ref) {
				detected = "aws_workload_identity";
			} else if (authCredentials || authCredentialsEnv) {
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

	const oauthDefaults = form.formState.defaultValues?.key?.oauth_key_config;
	useEffect(() => {
		if (form.formState.isDirty) return;
		if (isOpenAICompatible) {
			const grant = form.getValues("key.oauth_key_config.grant_type");
			const tokenURL = form.getValues("key.oauth_key_config.token_url");
			const detected: "api_key" | "oauth" = grant || tokenURL?.value || tokenURL?.ref ? "oauth" : "api_key";
			setOpenaiAuthType(detected);
			form.setValue("key.oauth_key_config._auth_type", detected);
		}
		// oauthDefaults re-runs detection after the key form resets itself once the key
		// resolves, for the same reason as databricksDefaults above.
	}, [isOpenAICompatible, form, oauthDefaults]);

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

	useEffect(() => {
		if (form.formState.isDirty) return;
		if (isSageMaker) {
			const accessKey = form.getValues("key.sagemaker_key_config.access_key");
			const secretKey = form.getValues("key.sagemaker_key_config.secret_key");
			const apiKey = form.getValues("key.value");
			const hasExplicitCreds = accessKey?.value || accessKey?.ref || secretKey?.value || secretKey?.ref;
			const hasApiKey = apiKey?.value || apiKey?.ref;
			let detected: "iam_role" | "explicit" | "api_key" = "iam_role";
			if (hasExplicitCreds) {
				detected = "explicit";
			} else if (hasApiKey) {
				detected = "api_key";
			}
			setSageMakerAuthType(detected);
			form.setValue("key.sagemaker_key_config._auth_type", detected);
		}
		// Re-run when ProviderKeyForm repopulates an existing key via form.reset(...) after mount.
	}, [isSageMaker, form, form.formState.defaultValues]);

	return (
		<div data-tab="api-keys" className="space-y-4 overflow-hidden">
			<div className="flex items-start gap-4 px-0.5">
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
			{!isAzure &&
				!isBedrock &&
				!isBedrockMantle &&
				!isSageMaker &&
				!isVertex &&
				!isDatabricks &&
				!isOpenAICompatible &&
				!isGithubCopilot && (
					<FormField
						control={control}
						name={`key.value`}
						render={({ field }) => (
							<FormItem className="px-0.5">
								<FormLabel>
									{t("providers.apiKey")} {isVLLM ? t("providers.optionalParen") : ""}
								</FormLabel>
								<FormControl>
									<SecretVarInput placeholder={t("providers.keys.apiKeyOrEnv")} type="text" {...field} />
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
						<FormItem className="px-0.5">
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
				<div className="space-y-4 px-0.5">
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
						<p className="text-muted-foreground text-sm">{t("providers.keys.azureDefaultHelp")}</p>
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
					<FormField
						control={control}
						name="key.azure_key_config.use_speech_endpoints"
						render={({ field }) => (
							<FormItem className="flex flex-row items-center justify-between rounded-sm border p-2">
								<div className="space-y-1.5">
									<FormLabel>{t("providers.keys.azureUseSpeech")}</FormLabel>
									<FormDescription>{t("providers.keys.azureUseSpeechHelp")}</FormDescription>
								</div>
								<FormControl>
									<Switch
										data-testid="apikey-azure-use-speech-endpoints-switch"
										checked={field.value ?? false}
										onCheckedChange={field.onChange}
									/>
								</FormControl>
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name="key.azure_key_config.use_voice_live_endpoints"
						render={({ field }) => (
							<FormItem className="flex flex-row items-center justify-between rounded-sm border p-2">
								<div className="space-y-1.5">
									<FormLabel>{t("providers.keys.azureUseVoiceLive")}</FormLabel>
									<FormDescription>{t("providers.keys.azureUseVoiceLiveHelp")}</FormDescription>
								</div>
								<FormControl>
									<Switch
										data-testid="apikey-azure-use-voice-live-endpoints-switch"
										checked={field.value ?? false}
										onCheckedChange={field.onChange}
									/>
								</FormControl>
							</FormItem>
						)}
					/>
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
								const next = v as VertexAuthType;
								setVertexAuthType(next);
								form.setValue("key.vertex_key_config._auth_type", next, { shouldDirty: true, shouldValidate: true });
								if (next !== "service_account_json") {
									// Clear auth credentials when switching away from service account JSON
									form.setValue("key.vertex_key_config.auth_credentials", undefined, { shouldDirty: true });
								}
								if (next !== "api_key") {
									// Clear the API key when switching away from API Key. The update endpoint has patch
									// semantics (an omitted field keeps its stored value), and the backend prefers a stored
									// API key over federation or ADC, so this must be an explicit empty value, not undefined.
									form.setValue("key.value", { value: "", ref: "" }, { shouldDirty: true });
								}
								if (next === "aws_workload_identity") {
									// Seed the block so its inputs are controlled from the first keystroke
									if (!form.getValues("key.vertex_key_config.aws_workload_identity")) {
										form.setValue(
											"key.vertex_key_config.aws_workload_identity",
											{ ...DefaultVertexAWSWorkloadIdentityConfig },
											{ shouldDirty: true },
										);
									}
								} else {
									// Drop the block entirely: an audience left behind would take precedence on the server
									form.setValue("key.vertex_key_config.aws_workload_identity", undefined, { shouldDirty: true });
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
								<TabsTrigger data-testid="apikey-vertex-aws-workload-identity-tab" value="aws_workload_identity">
									{t("providers.keys.wifTab")}
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-vertex-api-key-tab" value="api_key">
									{t("providers.apiKey")}
								</TabsTrigger>
							</TabsList>
						</Tabs>
						{vertexAuthType === "service_account" && (
							<p className="text-muted-foreground text-sm">{t("providers.keys.vertexAttachedHelp")}</p>
						)}
						{vertexAuthType === "aws_workload_identity" && <p className="text-muted-foreground text-sm">{t("providers.keys.wifHelp")}</p>}
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
									<Trans t={t} i18nKey="providers.keys.vertexRegionHelp" components={{ medium: <span className="font-medium" /> }} />
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

					{vertexAuthType === "aws_workload_identity" && (
						<>
							<FormField
								control={control}
								name={`key.vertex_key_config.aws_workload_identity.audience`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>
											{t("providers.keys.wifAudienceLabel")} {t("providers.requiredParen")}
										</FormLabel>
										<FormDescription>{t("providers.keys.wifAudienceHelp")}</FormDescription>
										<FormControl>
											<SecretVarInput
												data-testid="apikey-vertex-aws-wif-audience-input"
												placeholder="//iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/eks-pool/providers/aws or env.VERTEX_WIF_AUDIENCE"
												inputClassName="font-mono text-sm"
												{...field}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.vertex_key_config.aws_workload_identity.service_account_email`}
								render={({ field }) => (
									<FormItem>
										<div className="flex items-center gap-2">
											<FormLabel>
												{t("providers.keys.wifServiceAccountLabel")} {t("providers.optionalParen")}
											</FormLabel>
											<TooltipProvider>
												<Tooltip>
													<TooltipTrigger asChild>
														<span>
															<Info className="text-muted-foreground h-3 w-3" />
														</span>
													</TooltipTrigger>
													<TooltipContent>
														<p>{t("providers.keys.wifServiceAccountHelp")}</p>
													</TooltipContent>
												</Tooltip>
											</TooltipProvider>
										</div>
										<FormControl>
											<SecretVarInput
												data-testid="apikey-vertex-aws-wif-service-account-email-input"
												placeholder={t("providers.keys.wifServiceAccountPlaceholder")}
												{...field}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.vertex_key_config.aws_workload_identity.token_lifetime_seconds`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>
											{t("providers.keys.wifTokenLifetimeLabel")} {t("providers.optionalParen")}
										</FormLabel>
										<FormDescription>{t("providers.keys.wifTokenLifetimeHelp")}</FormDescription>
										<FormControl>
											<Input
												data-testid="apikey-vertex-aws-wif-token-lifetime-input"
												type="number"
												min={600}
												max={43200}
												step={60}
												placeholder="3600"
												value={field.value ?? ""}
												onChange={(e) => field.onChange(e.target.value === "" ? undefined : Number(e.target.value))}
												onBlur={field.onBlur}
												name={field.name}
												ref={field.ref}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.vertex_key_config.aws_workload_identity.aws_region`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>
											{t("providers.keys.wifAwsRegionLabel")} {t("providers.optionalParen")}
										</FormLabel>
										<FormDescription>{t("providers.keys.wifAwsRegionHelp")}</FormDescription>
										<FormControl>
											<SecretVarInput
												data-testid="apikey-vertex-aws-wif-aws-region-input"
												placeholder={t("providers.keys.wifAwsRegionPlaceholder")}
												{...field}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.vertex_key_config.aws_workload_identity.aws_role_arn`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>
											{t("providers.keys.wifAwsRoleArnLabel")} {t("providers.optionalParen")}
										</FormLabel>
										<FormDescription>{t("providers.keys.wifAwsRoleArnHelp")}</FormDescription>
										<FormControl>
											<SecretVarInput
												data-testid="apikey-vertex-aws-wif-aws-role-arn-input"
												placeholder={t("providers.keys.wifAwsRoleArnPlaceholder")}
												{...field}
											/>
										</FormControl>
										{isRedacted(field.value?.value ?? "") && (
											<div className="text-muted-foreground mt-1 flex items-center gap-1 text-xs">
												<Info className="h-3 w-3" />
												<span>{t("providers.keys.storedSecurely")}</span>
											</div>
										)}
										<FormMessage />
									</FormItem>
								)}
							/>
						</>
					)}

					{vertexAuthType === "api_key" && (
						<FormField
							control={control}
							name={`key.value`}
							render={({ field }) => (
								<FormItem>
									<FormLabel>{t("providers.keys.apiKeyGeminiOnly")}</FormLabel>
									<FormControl>
										<SecretVarInput
											data-testid="apikey-vertex-api-key-input"
											placeholder={t("providers.keys.apiKeyOrEnv")}
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
					<FormField
						control={control}
						name="key.vertex_key_config.use_session_routing"
						render={({ field }) => (
							<FormItem className="flex flex-row items-center justify-between rounded-sm border p-2">
								<div className="space-y-1.5">
									<FormLabel>{t("providers.keys.sessionRouting")}</FormLabel>
									<FormDescription>{t("providers.keys.sessionRoutingHelp")}</FormDescription>
								</div>
								<FormControl>
									<Switch
										data-testid="apikey-vertex-use-session-routing-switch"
										checked={field.value ?? false}
										onCheckedChange={field.onChange}
									/>
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
							<p className="text-muted-foreground text-sm">{t("providers.keys.oauthM2mHelp")}</p>
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
			{isCloudflare && (
				<div className="space-y-4">
					<FormField
						control={control}
						name="key.cloudflare_key_config.account_id"
						render={({ field }) => (
							<FormItem>
								<FormLabel>
									{t("providers.cloudflareMigration.accountId")} {t("providers.requiredParen")}
								</FormLabel>
								<FormDescription>{t("providers.keys.cloudflareAccountIdHelp")}</FormDescription>
								<FormControl>
									<SecretVarInput data-testid="key-input-cloudflare-account-id" placeholder="env.CLOUDFLARE_ACCOUNT_ID" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
				</div>
			)}
			{isOpenAICompatible && (
				<div className="space-y-4">
					<div className="space-y-2">
						<FormLabel>{t("providers.keys.authMethod")}</FormLabel>
						<Tabs
							value={openaiAuthType}
							onValueChange={(v) => {
								const next = v as "api_key" | "oauth";
								setOpenaiAuthType(next);
								form.setValue("key.oauth_key_config._auth_type", next, { shouldDirty: true, shouldValidate: true });
								if (next === "oauth") {
									// The static key and the minted bearer are alternatives, never both.
									form.setValue("key.value", undefined, { shouldDirty: true });
									if (!form.getValues("key.oauth_key_config.grant_type")) {
										form.setValue("key.oauth_key_config.grant_type", "client_credentials", { shouldDirty: true });
									}
								}
							}}
						>
							<TabsList className="grid w-full grid-cols-2">
								<TabsTrigger data-testid="apikey-openai-api-key-tab" value="api_key">
									{t("providers.apiKey")}
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-openai-oauth-tab" value="oauth">
									{t("providers.keys.oauthTab")}
								</TabsTrigger>
							</TabsList>
						</Tabs>
					</div>
					{openaiAuthType === "api_key" && (
						<FormField
							control={control}
							name="key.value"
							render={({ field }) => (
								<FormItem>
									<FormLabel>{t("providers.apiKey")}</FormLabel>
									<FormControl>
										<SecretVarInput
											data-testid="key-input-openai-api-key"
											placeholder={t("providers.keys.apiKeyOrEnv")}
											type="text"
											{...field}
										/>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
					)}
					{openaiAuthType === "oauth" && (
						<>
							<p className="text-muted-foreground text-sm">{t("providers.keys.oauthHelp")}</p>
							<FormField
								control={control}
								name="key.oauth_key_config.grant_type"
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers.keys.oauthGrantType")}</FormLabel>
										<Select value={field.value ?? "client_credentials"} onValueChange={field.onChange}>
											<FormControl>
												<SelectTrigger className="w-full" data-testid="key-select-oauth-grant-type">
													<SelectValue placeholder={t("providers.keys.oauthGrantClientCredentialsShort")} />
												</SelectTrigger>
											</FormControl>
											<SelectContent>
												<SelectItem value="client_credentials">{t("providers.keys.oauthGrantClientCredentials")}</SelectItem>
												<SelectItem value="jwt_bearer">{t("providers.keys.oauthGrantJwtBearer")}</SelectItem>
											</SelectContent>
										</Select>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name="key.oauth_key_config.token_url"
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers.keys.oauthTokenUrl")}</FormLabel>
										<FormDescription>{t("providers.keys.oauthTokenUrlHelp")}</FormDescription>
										<FormControl>
											<SecretVarInput
												data-testid="key-input-oauth-token-url"
												placeholder="https://idp.example.com/oauth2/token or env.IDP_TOKEN_URL"
												{...field}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							{oauthGrantType === "client_credentials" && (
								<>
									<FormField
										control={control}
										name="key.oauth_key_config.client_id"
										render={({ field }) => (
											<FormItem>
												<FormLabel>{t("providers.keys.clientId")}</FormLabel>
												<FormControl>
													<SecretVarInput
														data-testid="key-input-oauth-client-id"
														placeholder={t("providers.keys.oauthClientIdPlaceholder")}
														{...field}
													/>
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
									<FormField
										control={control}
										name="key.oauth_key_config.client_secret"
										render={({ field }) => (
											<FormItem>
												<FormLabel>{t("providers.keys.clientSecret")}</FormLabel>
												<FormControl>
													<SecretVarInput
														data-testid="key-input-oauth-client-secret"
														placeholder={t("providers.keys.oauthClientSecretPlaceholder")}
														{...field}
													/>
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
									<FormField
										control={control}
										name="key.oauth_key_config.auth_style"
										render={({ field }) => (
											<FormItem>
												<FormLabel>{t("providers.keys.oauthCredentialPlacement")}</FormLabel>
												<FormDescription>{t("providers.keys.oauthCredentialPlacementHelp")}</FormDescription>
												<Select value={field.value ?? "header"} onValueChange={field.onChange}>
													<FormControl>
														<SelectTrigger className="w-full" data-testid="key-select-oauth-auth-style">
															<SelectValue placeholder={t("providers.keys.oauthPlacementHeaderShort")} />
														</SelectTrigger>
													</FormControl>
													<SelectContent>
														<SelectItem value="header">{t("providers.keys.oauthPlacementHeader")}</SelectItem>
														<SelectItem value="body">{t("providers.keys.oauthPlacementBody")}</SelectItem>
													</SelectContent>
												</Select>
												<FormMessage />
											</FormItem>
										)}
									/>
								</>
							)}
							{oauthGrantType === "jwt_bearer" && (
								<>
									<FormField
										control={control}
										name="key.oauth_key_config.private_key"
										render={({ field }) => (
											<FormItem>
												<FormLabel>{t("providers.keys.privateKey")}</FormLabel>
												<FormDescription>{t("providers.keys.oauthPrivateKeyHelp")}</FormDescription>
												<FormControl>
													<SecretVarInput
														data-testid="key-input-oauth-private-key"
														variant="textarea"
														rows={4}
														placeholder={t("providers.keys.oauthPrivateKeyPlaceholder")}
														{...field}
													/>
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
									<FormField
										control={control}
										name="key.oauth_key_config.issuer"
										render={({ field }) => (
											<FormItem>
												<FormLabel>{t("providers.keys.oauthIssuer")}</FormLabel>
												<FormDescription>{t("providers.keys.oauthIssuerHelp")}</FormDescription>
												<FormControl>
													<Input data-testid="key-input-oauth-issuer" placeholder="client-id" {...field} value={field.value ?? ""} />
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
									<FormField
										control={control}
										name="key.oauth_key_config.subject"
										render={({ field }) => (
											<FormItem>
												<FormLabel>
													{t("providers.keys.oauthSubject")} {t("providers.optionalParen")}
												</FormLabel>
												<FormDescription>{t("providers.keys.oauthSubjectHelp")}</FormDescription>
												<FormControl>
													<Input
														data-testid="key-input-oauth-subject"
														placeholder="service-account@example"
														{...field}
														value={field.value ?? ""}
													/>
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
									<FormField
										control={control}
										name="key.oauth_key_config.audience"
										render={({ field }) => (
											<FormItem>
												<FormLabel>{t("providers.keys.oauthAudience")}</FormLabel>
												<FormDescription>{t("providers.keys.oauthAudienceHelp")}</FormDescription>
												<FormControl>
													<Input
														data-testid="key-input-oauth-audience"
														placeholder="https://idp.example.com/oauth2/token"
														{...field}
														value={field.value ?? ""}
													/>
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
									<FormField
										control={control}
										name="key.oauth_key_config.key_id"
										render={({ field }) => (
											<FormItem>
												<FormLabel>
													{t("providers.keys.oauthKeyId")} {t("providers.optionalParen")}
												</FormLabel>
												<FormDescription>{t("providers.keys.oauthKeyIdHelp")}</FormDescription>
												<FormControl>
													<Input data-testid="key-input-oauth-key-id" placeholder="kid-1" {...field} value={field.value ?? ""} />
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
									<FormField
										control={control}
										name="key.oauth_key_config.signing_algorithm"
										render={({ field }) => (
											<FormItem>
												<FormLabel>{t("providers.keys.oauthSigningAlgorithm")}</FormLabel>
												<Select value={field.value ?? "RS256"} onValueChange={field.onChange}>
													<FormControl>
														<SelectTrigger className="w-full" data-testid="key-select-oauth-signing-algorithm">
															<SelectValue placeholder="RS256" />
														</SelectTrigger>
													</FormControl>
													<SelectContent>
														{OAuthSigningAlgorithms.map((alg) => (
															<SelectItem key={alg} value={alg}>
																{alg}
															</SelectItem>
														))}
													</SelectContent>
												</Select>
												<FormMessage />
											</FormItem>
										)}
									/>
									<FormField
										control={control}
										name="key.oauth_key_config.assertion_lifetime_seconds"
										render={({ field }) => (
											<FormItem>
												<FormLabel>{t("providers.keys.oauthAssertionLifetime")}</FormLabel>
												<FormDescription>{t("providers.keys.oauthAssertionLifetimeHelp")}</FormDescription>
												<FormControl>
													<Input
														data-testid="key-input-oauth-assertion-lifetime"
														type="number"
														min={0}
														max={3600}
														placeholder="300"
														{...field}
														value={field.value ?? ""}
														onChange={(e) => field.onChange(e.target.value === "" ? undefined : Number(e.target.value))}
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
								name="key.oauth_key_config.scopes"
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers.keys.scopesOptional")}</FormLabel>
										<FormControl>
											<TagInput
												data-testid="key-input-oauth-scopes"
												placeholder={t("providers.keys.addScopePlaceholder")}
												value={field.value ?? []}
												onValueChange={field.onChange}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							{oauthGrantType === "client_credentials" && (
								<FormField
									control={control}
									name="key.oauth_key_config.audience"
									render={({ field }) => (
										<FormItem>
											<FormLabel>
												{t("providers.keys.oauthAudience")} {t("providers.optionalParen")}
											</FormLabel>
											<FormDescription>{t("providers.keys.oauthAudienceCcHelp")}</FormDescription>
											<FormControl>
												<Input
													data-testid="key-input-oauth-cc-audience"
													placeholder="api://my-llm-gateway"
													{...field}
													value={field.value ?? ""}
												/>
											</FormControl>
											<FormMessage />
										</FormItem>
									)}
								/>
							)}
						</>
					)}
				</div>
			)}
			{isGithubCopilot && (
				<div className="space-y-4">
					<Separator />
					<FormField
						control={control}
						name="key.value"
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers.keys.copilotTokenLabel")}</FormLabel>
								<FormDescription>
									<Trans t={t} i18nKey="providers.keys.copilotTokenDescription" components={{ code: <code />, strong: <strong /> }} />
								</FormDescription>
								<FormControl>
									<SecretVarInput
										data-testid="key-input-copilot-token"
										placeholder={t("providers.keys.copilotTokenInputPlaceholder")}
										type="text"
										{...field}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<Separator />
					<div className="space-y-1.5">
						<p className="text-muted-foreground text-sm">{t("providers.keys.copilotSignInHint")}</p>
						<GithubCopilotSignIn
							provider={providerName}
							onToken={(token) => form.setValue("key.value", { value: token, ref: "" }, { shouldDirty: true, shouldValidate: true })}
						/>
					</div>
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
						{bedrockAuthType === "iam_role" && <p className="text-muted-foreground text-sm">{t("providers.keys.iamRoleHelp")}</p>}
						{bedrockAuthType === "api_key" && <p className="text-muted-foreground text-sm">{t("providers.keys.bearerApiKeyHelp")}</p>}
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
										<FormDescription>{t("providers.keys.assumeRoleHelp")}</FormDescription>
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
						{bedrockMantleAuthType === "iam_role" && <p className="text-muted-foreground text-sm">{t("providers.keys.iamRoleHelp")}</p>}
						{bedrockMantleAuthType === "api_key" && <p className="text-muted-foreground text-sm">{t("providers.keys.mantleApiKeyHelp")}</p>}
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
										<FormDescription>{t("providers.keys.assumeRoleHelp")}</FormDescription>
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

			{isSageMaker && (
				<div className="space-y-4" data-testid="apikey-sagemaker-section">
					<Separator className="my-6" />
					<div className="space-y-2">
						<FormLabel>{t("providers.keys.authMethod")}</FormLabel>
						<Tabs
							value={sageMakerAuthType}
							onValueChange={(v) => {
								setSageMakerAuthType(v as "iam_role" | "explicit" | "api_key");
								form.setValue("key.sagemaker_key_config._auth_type", v, { shouldDirty: true, shouldValidate: true });
								if (v === "iam_role") {
									// Clear explicit credentials and the pre-minted token when switching to the default chain (an empty value, not undefined: updates keep omitted fields)
									form.setValue("key.sagemaker_key_config.access_key", undefined, { shouldDirty: true });
									form.setValue("key.sagemaker_key_config.secret_key", undefined, { shouldDirty: true });
									form.setValue("key.sagemaker_key_config.session_token", undefined, { shouldDirty: true });
									form.setValue("key.value", { value: "", ref: "" }, { shouldDirty: true });
								} else if (v === "explicit") {
									form.setValue("key.value", { value: "", ref: "" }, { shouldDirty: true });
								} else if (v === "api_key") {
									// A pre-minted token carries its own identity: clear AWS credentials and assume-role fields
									form.setValue("key.sagemaker_key_config.access_key", undefined, { shouldDirty: true });
									form.setValue("key.sagemaker_key_config.secret_key", undefined, { shouldDirty: true });
									form.setValue("key.sagemaker_key_config.session_token", undefined, { shouldDirty: true });
									form.setValue("key.sagemaker_key_config.role_arn", undefined, { shouldDirty: true });
									form.setValue("key.sagemaker_key_config.external_id", undefined, { shouldDirty: true });
									form.setValue("key.sagemaker_key_config.session_name", undefined, { shouldDirty: true });
								}
							}}
						>
							<TabsList className="flex w-full justify-start">
								<TabsTrigger data-testid="apikey-sagemaker-iam-role-tab" value="iam_role">
									{t("providers.keys.iamRoleInherited")}
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-sagemaker-explicit-credentials-tab" value="explicit">
									{t("providers.keys.explicitCredentials")}
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-sagemaker-api-key-tab" value="api_key">
									{t("providers.keys.bearerToken")}
								</TabsTrigger>
							</TabsList>
						</Tabs>
						{sageMakerAuthType === "iam_role" && (
							<p className="text-muted-foreground text-sm">{t("providers.keys.sagemakerIamRoleHelp")}</p>
						)}
						{sageMakerAuthType === "explicit" && (
							<p className="text-muted-foreground text-sm">{t("providers.keys.sagemakerExplicitHelp")}</p>
						)}
						{sageMakerAuthType === "api_key" && (
							<p className="text-muted-foreground text-sm">{t("providers.keys.sagemakerApiKeyHelp")}</p>
						)}
					</div>

					{sageMakerAuthType === "explicit" && (
						<>
							<FormField
								control={control}
								name={`key.sagemaker_key_config.access_key`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers.keys.accessKeyRequired")}</FormLabel>
										<FormControl>
											<SecretVarInput
												data-testid="apikey-sagemaker-access-key-input"
												placeholder={t("providers.keys.awsAccessKeyPlaceholder")}
												{...field}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.sagemaker_key_config.secret_key`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers.keys.secretKeyRequired")}</FormLabel>
										<FormControl>
											<SecretVarInput
												data-testid="apikey-sagemaker-secret-key-input"
												placeholder={t("providers.keys.awsSecretKeyPlaceholder")}
												{...field}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
							<FormField
								control={control}
								name={`key.sagemaker_key_config.session_token`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers.keys.sessionTokenOptional")}</FormLabel>
										<FormControl>
											<SecretVarInput
												data-testid="apikey-sagemaker-session-token-input"
												placeholder={t("providers.keys.awsSessionTokenPlaceholder")}
												{...field}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</>
					)}

					{sageMakerAuthType === "api_key" && (
						<FormField
							control={control}
							name={`key.value`}
							render={({ field }) => (
								<FormItem>
									<FormLabel>{t("providers.keys.bearerToken")}</FormLabel>
									<FormControl>
										<SecretVarInput
											data-testid="apikey-sagemaker-api-key-input"
											placeholder={t("providers.keys.sagemakerBearerPlaceholder")}
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
						name={`key.sagemaker_key_config.region`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers.keys.regionRequired")}</FormLabel>
								<FormDescription>{t("providers.keys.sagemakerRegionHelp")}</FormDescription>
								<FormControl>
									<SecretVarInput data-testid="apikey-sagemaker-region-input" placeholder={t("providers.keys.awsRegionPlaceholder")} {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>

					{sageMakerAuthType !== "api_key" && (
						<>
							<FormField
								control={control}
								name={`key.sagemaker_key_config.role_arn`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers.keys.assumeRoleArnOptional")}</FormLabel>
										<FormDescription>{t("providers.keys.sagemakerAssumeRoleHelp")}</FormDescription>
										<FormControl>
											<SecretVarInput
												data-testid="apikey-sagemaker-role-arn-input"
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
								name={`key.sagemaker_key_config.external_id`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers.keys.externalIdOptional")}</FormLabel>
										<FormDescription>{t("providers.keys.externalIdHelpPeriod")}</FormDescription>
										<FormControl>
											<SecretVarInput
												data-testid="apikey-sagemaker-external-id-input"
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
								name={`key.sagemaker_key_config.session_name`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("providers.keys.sessionNameOptional")}</FormLabel>
										<FormDescription>{t("providers.keys.sessionNameHelpPeriod")}</FormDescription>
										<FormControl>
											<SecretVarInput
												data-testid="apikey-sagemaker-session-name-input"
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
					<VPCEndpointsFormField control={control} configKey="key.sagemaker_key_config" services={SAGEMAKER_VPC_ENDPOINT_SERVICES} />
				</div>
			)}
		</div>
	);
}