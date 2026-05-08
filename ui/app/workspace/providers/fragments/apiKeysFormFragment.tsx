import { EnvVarInput } from "@/components/ui/envVarInput";
import { FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { HeadersTable, type CellRenderParams } from "@/components/ui/headersTable";
import { Input } from "@/components/ui/input";
import { ModelMultiselect } from "@/components/ui/modelMultiselect";
import { Separator } from "@/components/ui/separator";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { TagInput } from "@/components/ui/tagInput";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { isRedacted } from "@/lib/utils/validation";
import { Info } from "lucide-react";
import { useEffect, useState } from "react";
import { Control, UseFormReturn } from "react-hook-form";
import { useTranslation } from "react-i18next";

// Providers that support batch APIs
const BATCH_SUPPORTED_PROVIDERS = ["openai", "bedrock", "anthropic", "gemini", "azure"];

/** Normalize form value (object or legacy JSON string) for the alias map editor. */
function normalizeAliasesValue(v: Record<string, string> | string | undefined | null): Record<string, string> {
	if (v == null) {
		return {};
	}
	if (typeof v === "string") {
		const t = v.trim();
		if (!t) {
			return {};
		}
		try {
			const p = JSON.parse(t) as unknown;
			if (typeof p === "object" && p !== null && !Array.isArray(p)) {
				return Object.fromEntries(Object.entries(p as Record<string, unknown>).map(([k, val]) => [k, String(val ?? "")]));
			}
		} catch {
			return {};
		}
		return {};
	}
	if (typeof v === "object" && !Array.isArray(v)) {
		return Object.fromEntries(Object.entries(v).map(([k, val]) => [k, typeof val === "string" ? val : String(val ?? "")]));
	}
	return {};
}

interface Props {
	control: Control<any>;
	providerName: string;
	form: UseFormReturn<any>;
}

// Batch API form field for all providers
function BatchAPIFormField({ control }: { control: Control<any>; form: UseFormReturn<any> }) {
	const { t } = useTranslation();

	return (
		<FormField
			control={control}
			name={`key.use_for_batch_api`}
			render={({ field }) => (
				<FormItem className="flex flex-row items-center justify-between rounded-sm border p-2">
					<div className="space-y-1.5">
						<FormLabel>{t("workspace.providers.apiKeyForm.useForBatchApis")}</FormLabel>
						<FormDescription>{t("workspace.providers.apiKeyForm.useForBatchApisDesc")}</FormDescription>
					</div>
					<FormControl>
						<Switch checked={field.value ?? false} onCheckedChange={field.onChange} />
					</FormControl>
				</FormItem>
			)}
		/>
	);
}

export function ApiKeyFormFragment({ control, providerName, form }: Props) {
	const { t } = useTranslation();
	const isBedrock = providerName === "bedrock";
	const isVertex = providerName === "vertex";
	const isAzure = providerName === "azure";
	const isReplicate = providerName === "replicate";
	const isVLLM = providerName === "vllm";
	const isOllama = providerName === "ollama";
	const isSGL = providerName === "sgl";
	const isKeylessProvider = isOllama || isSGL;
	const supportsBatchAPI = BATCH_SUPPORTED_PROVIDERS.includes(providerName);

	// Auth type state for Azure: 'api_key', 'entra_id', or 'default_credential'
	const [azureAuthType, setAzureAuthType] = useState<"api_key" | "entra_id" | "default_credential">("api_key");

	// Auth type state for Bedrock: 'iam_role', 'explicit', or 'api_key'
	const [bedrockAuthType, setBedrockAuthType] = useState<"iam_role" | "explicit" | "api_key">("iam_role");

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
				clientId?.value || clientId?.env_var || clientSecret?.value || clientSecret?.env_var || tenantId?.value || tenantId?.env_var;
			const hasApiKey = apiKey?.value || apiKey?.env_var;
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
			const authCredentialsEnv = form.getValues("key.vertex_key_config.auth_credentials")?.env_var;
			const apiKey = form.getValues("key.value")?.value;
			const apiKeyEnv = form.getValues("key.value")?.env_var;
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

	useEffect(() => {
		if (form.formState.isDirty) return;
		if (isBedrock) {
			const accessKey = form.getValues("key.bedrock_key_config.access_key");
			const secretKey = form.getValues("key.bedrock_key_config.secret_key");
			const apiKey = form.getValues("key.value");
			const hasExplicitCreds = accessKey?.value || accessKey?.env_var || secretKey?.value || secretKey?.env_var;
			const hasApiKey = apiKey?.value || apiKey?.env_var;
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

	return (
		<div data-tab="api-keys" className="space-y-4 overflow-hidden">
			<div className="flex items-start gap-4">
				<div className="flex-1">
					<FormField
						control={control}
						name={`key.name`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("workspace.providers.apiKeyForm.name")}</FormLabel>
								<FormControl>
									<Input placeholder={t("workspace.providers.apiKeyForm.productionKeyPlaceholder")} type="text" {...field} />
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
								<FormLabel>{t("workspace.providers.keyTable.weight")}</FormLabel>
								<TooltipProvider>
									<Tooltip>
										<TooltipTrigger asChild>
											<span>
												<Info className="text-muted-foreground h-3 w-3" />
											</span>
										</TooltipTrigger>
										<TooltipContent>
											<p>{t("workspace.providers.apiKeyForm.weightDesc")}</p>
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
			{!isAzure && !isBedrock && !isVertex && (
				<FormField
					control={control}
					name={`key.value`}
					render={({ field }) => (
						<FormItem>
							<FormLabel>
								{t("workspace.providers.keyTable.apiKey")} {isVLLM ? t("workspace.providers.optionalSuffix") : ""}
							</FormLabel>
							<FormControl>
								<EnvVarInput placeholder={t("workspace.providers.apiKeyForm.apiKeyPlaceholder")} type="text" {...field} />
							</FormControl>
							<FormMessage />
						</FormItem>
					)}
				/>
			)}
			{!isVLLM && (
				<>
					<FormField
						control={control}
						name={`key.models`}
						render={({ field }) => (
							<FormItem>
								<div className="flex items-center gap-2">
									<FormLabel>{t("workspace.providers.apiKeyForm.allowedModels")}</FormLabel>
									<TooltipProvider>
										<Tooltip>
											<TooltipTrigger asChild>
												<span>
													<Info className="text-muted-foreground h-3 w-3" />
												</span>
											</TooltipTrigger>
											<TooltipContent>
												<p>{t("workspace.providers.apiKeyForm.allowedModelsDesc")}</p>
											</TooltipContent>
										</Tooltip>
									</TooltipProvider>
								</div>
								<FormControl>
									<ModelMultiselect
										data-testid="api-keys-models-multiselect"
										provider={providerName}
										allowAllOption={true}
										value={field.value || []}
										onChange={(models: string[]) => {
											const hadStar = (field.value || []).includes("*");
											const hasStar = models.includes("*");
											if (!hadStar && hasStar) {
												field.onChange(["*"]);
											} else if (hadStar && hasStar && models.length > 1) {
												field.onChange(models.filter((m: string) => m !== "*"));
											} else {
												field.onChange(models);
											}
										}}
										placeholder={
											(field.value || []).includes("*")
												? t("workspace.providers.apiKeyForm.allModelsAllowed")
												: (field.value || []).length === 0
													? t("workspace.providers.apiKeyForm.noModelsDenyAll")
													: t("workspace.providers.apiKeyForm.searchModels")
										}
										unfiltered={true}
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
								<div className="flex items-center gap-2">
									<FormLabel>{t("workspace.providers.apiKeyForm.blockedModels")}</FormLabel>
									<TooltipProvider>
										<Tooltip>
											<TooltipTrigger asChild>
												<span>
													<Info className="text-muted-foreground h-3 w-3" />
												</span>
											</TooltipTrigger>
											<TooltipContent className="max-w-sm">
												<p>{t("workspace.providers.apiKeyForm.blockedModelsDesc")}</p>
											</TooltipContent>
										</Tooltip>
									</TooltipProvider>
								</div>
								<FormControl>
									<ModelMultiselect
										data-testid="api-keys-blocked-models-multiselect"
										provider={providerName}
										allowAllOption={true}
										value={field.value || []}
										onChange={(models: string[]) => {
											const hadStar = (field.value || []).includes("*");
											const hasStar = models.includes("*");
											if (!hadStar && hasStar) {
												field.onChange(["*"]);
											} else if (hadStar && hasStar && models.length > 1) {
												field.onChange(models.filter((m: string) => m !== "*"));
											} else {
												field.onChange(models);
											}
										}}
										placeholder={
											(field.value || []).includes("*")
												? t("workspace.providers.apiKeyForm.allModelsBlocked")
												: (field.value || []).length === 0
													? t("workspace.providers.apiKeyForm.noModelsBlocked")
													: t("workspace.providers.apiKeyForm.searchModels")
										}
										unfiltered={true}
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
							<FormItem data-testid="apikey-aliases-field">
								<FormLabel>{t("workspace.providers.apiKeyForm.aliasesOptional")}</FormLabel>
								<FormDescription>{t("workspace.providers.apiKeyForm.aliasesDesc")}</FormDescription>
								<FormControl>
									<div data-testid="apikey-aliases-table">
										<HeadersTable
											label=""
											value={normalizeAliasesValue(field.value)}
											onChange={(next) => {
												form.clearErrors("key.aliases");
												field.onChange(Object.keys(next).length > 0 ? next : {});
											}}
											keyPlaceholder={t("workspace.providers.apiKeyForm.requestModelNamePlaceholder")}
											valuePlaceholder={t("workspace.providers.apiKeyForm.deploymentProfilePlaceholder")}
											renderValueInput={({ value: cellValue, onChange, placeholder, disabled }: CellRenderParams) => (
												<ModelMultiselect
													isSingleSelect
													provider={providerName}
													value={cellValue}
													onChange={onChange}
													placeholder={placeholder ?? t("workspace.providers.apiKeyForm.deploymentProfilePlaceholder")}
													disabled={disabled}
													unfiltered={true}
												/>
											)}
										/>
									</div>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
				</>
			)}
			{supportsBatchAPI && !isBedrock && !isAzure && <BatchAPIFormField control={control} form={form} />}
			{isAzure && (
				<div className="space-y-4">
					<Separator className="my-6" />
					<div className="space-y-2">
						<FormLabel>{t("workspace.providers.apiKeyForm.authenticationMethod")}</FormLabel>
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
							<TabsList className="grid w-full grid-cols-3">
								<TabsTrigger data-testid="apikey-azure-default-credential-tab" value="default_credential">
									{t("workspace.providers.apiKeyForm.defaultCredential")}
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-azure-api-key-tab" value="api_key">
									{t("workspace.providers.keyTable.apiKey")}
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-azure-entra-id-tab" value="entra_id">
									{t("workspace.providers.apiKeyForm.entraIdServicePrincipal")}
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
										{t("workspace.providers.keyTable.apiKey")}{" "}
										{isVertex
											? t("workspace.providers.apiKeyForm.vertexApiKeySupportedOnly")
											: isVLLM
												? t("workspace.providers.optionalSuffix")
												: ""}
									</FormLabel>
									<FormControl>
										<EnvVarInput placeholder={t("workspace.providers.apiKeyForm.apiKeyPlaceholder")} type="text" {...field} />
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
					)}
					{azureAuthType === "default_credential" && (
						<p className="text-muted-foreground text-sm">{t("workspace.providers.apiKeyForm.defaultAzureCredentialDesc")}</p>
					)}

					<FormField
						control={control}
						name={`key.azure_key_config.endpoint`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("workspace.providers.apiKeyForm.endpointRequired")}</FormLabel>
								<FormControl>
									<EnvVarInput placeholder="https://your-resource.openai.azure.com or env.AZURE_ENDPOINT" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name={`key.azure_key_config.api_version`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("workspace.providers.apiKeyForm.apiVersionOptional")}</FormLabel>
								<FormControl>
									<EnvVarInput placeholder="2024-02-01 or env.AZURE_API_VERSION" {...field} />
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
										<FormLabel>{t("workspace.providers.apiKeyForm.clientIdRequired")}</FormLabel>
										<FormControl>
											<EnvVarInput placeholder="your-client-id or env.AZURE_CLIENT_ID" {...field} />
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
										<FormLabel>{t("workspace.providers.apiKeyForm.clientSecretRequired")}</FormLabel>
										<FormControl>
											<EnvVarInput placeholder="your-client-secret or env.AZURE_CLIENT_SECRET" {...field} />
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
										<FormLabel>{t("workspace.providers.apiKeyForm.tenantIdRequired")}</FormLabel>
										<FormControl>
											<EnvVarInput placeholder="your-tenant-id or env.AZURE_TENANT_ID" {...field} />
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
											<FormLabel>{t("workspace.providers.apiKeyForm.scopesOptional")}</FormLabel>
											<TooltipProvider>
												<Tooltip>
													<TooltipTrigger asChild>
														<span>
															<Info className="text-muted-foreground h-3 w-3" />
														</span>
													</TooltipTrigger>
													<TooltipContent>
														<p>{t("workspace.providers.apiKeyForm.scopesDesc")}</p>
													</TooltipContent>
												</Tooltip>
											</TooltipProvider>
										</div>
										<FormControl>
											<TagInput
												data-testid="apikey-azure-scopes-input"
												placeholder={t("workspace.providers.apiKeyForm.addScopePlaceholder")}
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
						<FormLabel>{t("workspace.providers.apiKeyForm.authenticationMethod")}</FormLabel>
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
							<TabsList className="grid w-full grid-cols-3">
								<TabsTrigger data-testid="apikey-vertex-service-account-tab" value="service_account">
									{t("workspace.providers.apiKeyForm.serviceAccountAttached")}
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-vertex-service-account-json-tab" value="service_account_json">
									{t("workspace.providers.apiKeyForm.serviceAccountJson")}
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-vertex-api-key-tab" value="api_key">
									{t("workspace.providers.keyTable.apiKey")}
								</TabsTrigger>
							</TabsList>
						</Tabs>
						{vertexAuthType === "service_account" && (
							<p className="text-muted-foreground text-sm">{t("workspace.providers.apiKeyForm.serviceAccountAttachedDesc")}</p>
						)}
					</div>

					<FormField
						control={control}
						name={`key.vertex_key_config.project_id`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("workspace.providers.apiKeyForm.projectIdRequired")}</FormLabel>
								<FormControl>
									<EnvVarInput placeholder="your-gcp-project-id or env.VERTEX_PROJECT_ID" {...field} />
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
								<FormLabel>{t("workspace.providers.apiKeyForm.projectNumberRequiredFineTuned")}</FormLabel>
								<FormControl>
									<EnvVarInput placeholder="your-gcp-project-number or env.VERTEX_PROJECT_NUMBER" {...field} />
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
								<FormLabel>{t("workspace.providers.apiKeyForm.regionRequired")}</FormLabel>
								<FormControl>
									<EnvVarInput placeholder="us-central1 or env.VERTEX_REGION" {...field} />
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
									<FormLabel>{t("workspace.providers.apiKeyForm.authCredentialsRequired")}</FormLabel>
									<FormDescription>{t("workspace.providers.apiKeyForm.authCredentialsDesc")}</FormDescription>
									<FormControl>
										<EnvVarInput
											data-testid="apikey-vertex-auth-credentials-input"
											variant="textarea"
											rows={4}
											placeholder='{"type":"service_account","project_id":"your-gcp-project",...} or env.VERTEX_CREDENTIALS'
											inputClassName="font-mono text-sm"
											{...field}
										/>
									</FormControl>
									{isRedacted(field.value?.value ?? "") && (
										<div className="text-muted-foreground mt-1 flex items-center gap-1 text-xs">
											<Info className="h-3 w-3" />
											<span>{t("workspace.providers.apiKeyForm.credentialsStoredSecurely")}</span>
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
									<FormLabel>{t("workspace.providers.apiKeyForm.vertexApiKey")}</FormLabel>
									<FormControl>
										<EnvVarInput
											data-testid="apikey-vertex-api-key-input"
											placeholder={t("workspace.providers.apiKeyForm.apiKeyPlaceholder")}
											type="text"
											{...field}
										/>
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
					)}
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
									<FormLabel>{t("workspace.providers.apiKeyForm.useDeploymentsEndpoint")}</FormLabel>
									<FormDescription>{t("workspace.providers.apiKeyForm.useDeploymentsEndpointDesc")}</FormDescription>
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
								<FormLabel>{t("workspace.providers.apiKeyForm.serverUrlRequired")}</FormLabel>
								<FormDescription>{t("workspace.providers.apiKeyForm.vllmServerUrlDesc")}</FormDescription>
								<FormControl>
									<EnvVarInput data-testid="key-input-vllm-url" placeholder="http://vllm-server:8000" {...field} />
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
								<FormLabel>{t("workspace.providers.apiKeyForm.modelNameRequired")}</FormLabel>
								<FormDescription>{t("workspace.providers.apiKeyForm.vllmModelNameDesc")}</FormDescription>
								<FormControl>
									<Input data-testid="key-input-vllm-model-name" placeholder="meta-llama/Llama-3-70b-hf" {...field} />
								</FormControl>
								<FormMessage />
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
								<FormLabel>{t("workspace.providers.apiKeyForm.serverUrlRequired")}</FormLabel>
								<FormDescription>
									{t("workspace.providers.apiKeyForm.keylessServerUrlDescPrefix", { server: isOllama ? "Ollama" : "SGLang" })}{" "}
									{isOllama ? "http://localhost:11434" : "http://localhost:30000"} or {isOllama ? "env.OLLAMA_URL" : "env.SGL_URL"})
								</FormDescription>
								<FormControl>
									<EnvVarInput
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
			{isBedrock && (
				<div className="space-y-4">
					<Separator className="my-6" />
					<div className="space-y-2">
						<FormLabel>{t("workspace.providers.apiKeyForm.authenticationMethod")}</FormLabel>
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
							<TabsList className="grid w-full grid-cols-3">
								<TabsTrigger data-testid="apikey-bedrock-iam-role-tab" value="iam_role">
									{t("workspace.providers.apiKeyForm.iamRoleInherited")}
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-bedrock-explicit-credentials-tab" value="explicit">
									{t("workspace.providers.apiKeyForm.explicitCredentials")}
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-bedrock-api-key-tab" value="api_key">
									{t("workspace.providers.keyTable.apiKey")}
								</TabsTrigger>
							</TabsList>
						</Tabs>
						{bedrockAuthType === "iam_role" && (
							<p className="text-muted-foreground text-sm">{t("workspace.providers.apiKeyForm.iamRoleDesc")}</p>
						)}
						{bedrockAuthType === "api_key" && (
							<p className="text-muted-foreground text-sm">{t("workspace.providers.apiKeyForm.bearerTokenDesc")}</p>
						)}
					</div>

					{bedrockAuthType === "explicit" && (
						<>
							<FormField
								control={control}
								name={`key.bedrock_key_config.access_key`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("workspace.providers.apiKeyForm.accessKeyRequired")}</FormLabel>
										<FormControl>
											<EnvVarInput placeholder="your-aws-access-key or env.AWS_ACCESS_KEY_ID" {...field} />
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
										<FormLabel>{t("workspace.providers.apiKeyForm.secretKeyRequired")}</FormLabel>
										<FormControl>
											<EnvVarInput placeholder="your-aws-secret-key or env.AWS_SECRET_ACCESS_KEY" {...field} />
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
										<FormLabel>{t("workspace.providers.apiKeyForm.sessionTokenOptional")}</FormLabel>
										<FormControl>
											<EnvVarInput placeholder="your-aws-session-token or env.AWS_SESSION_TOKEN" {...field} />
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
									<FormLabel>{t("workspace.providers.keyTable.apiKey")}</FormLabel>
									<FormControl>
										<EnvVarInput
											data-testid="apikey-bedrock-api-key-input"
											placeholder={t("workspace.providers.apiKeyForm.bedrockApiKeyPlaceholder")}
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
								<FormLabel>{t("workspace.providers.apiKeyForm.regionRequired")}</FormLabel>
								<FormControl>
									<EnvVarInput placeholder="us-east-1 or env.AWS_REGION" {...field} />
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
										<FormLabel>{t("workspace.providers.apiKeyForm.assumeRoleArnOptional")}</FormLabel>
										<FormDescription>{t("workspace.providers.apiKeyForm.assumeRoleArnDesc")}</FormDescription>
										<FormControl>
											<EnvVarInput
												data-testid="apikey-bedrock-role-arn-input"
												placeholder="arn:aws:iam::123456789:role/MyRole or env.AWS_ROLE_ARN"
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
										<FormLabel>{t("workspace.providers.apiKeyForm.externalIdOptional")}</FormLabel>
										<FormDescription>{t("workspace.providers.apiKeyForm.externalIdDesc")}</FormDescription>
										<FormControl>
											<EnvVarInput
												data-testid="apikey-bedrock-external-id-input"
												placeholder="external-id or env.AWS_EXTERNAL_ID"
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
										<FormLabel>{t("workspace.providers.apiKeyForm.sessionNameOptional")}</FormLabel>
										<FormDescription>{t("workspace.providers.apiKeyForm.sessionNameDesc")}</FormDescription>
										<FormControl>
											<EnvVarInput
												data-testid="apikey-bedrock-session-name-input"
												placeholder="bifrost-session or env.AWS_SESSION_NAME"
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
								<FormLabel>{t("workspace.providers.apiKeyForm.arnOptional")}</FormLabel>
								<FormControl>
									<EnvVarInput placeholder="arn:aws:bedrock:us-east-1:123:inference-profile or env.AWS_ARN" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					{supportsBatchAPI && <BatchAPIFormField control={control} form={form} />}
				</div>
			)}
		</div>
	);
}