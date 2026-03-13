"use client";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { EnvVarInput } from "@/components/ui/envVarInput";
import { FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { ModelMultiselect } from "@/components/ui/modelMultiselect";
import { Separator } from "@/components/ui/separator";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { TagInput } from "@/components/ui/tagInput";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { isRedacted } from "@/lib/utils/validation";
import { useRefreshModelsMutation } from "@/lib/store/apis/providersApi";
import { getApiBaseUrl } from "@/lib/utils/port";
import { CheckCircle2, Copy, ExternalLink, Info, Loader2, Plus, Trash2 } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { Control, UseFormReturn } from "react-hook-form";

// Providers that support batch APIs
const BATCH_SUPPORTED_PROVIDERS = ["openai", "bedrock", "anthropic", "gemini", "azure"];

// Providers that support live model refresh (dynamic model discovery)
const MODEL_REFRESH_PROVIDERS = ["copilot"];

interface Props {
	control: Control<any>;
	providerName: string;
	form: UseFormReturn<any>;
}

// Batch API form field for all providers
function BatchAPIFormField({ control, form }: { control: Control<any>; form: UseFormReturn<any> }) {
	return (
		<FormField
			control={control}
			name={`key.use_for_batch_api`}
			render={({ field }) => (
				<FormItem className="flex flex-row items-center justify-between rounded-sm border p-2">
					<div className="space-y-1.5">
						<FormLabel>Use for Batch APIs</FormLabel>
						<FormDescription>
							Enable this key for batch API operations. Only keys with this enabled will be used for batch requests.
						</FormDescription>
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
	const isBedrock = providerName === "bedrock";
	const isVertex = providerName === "vertex";
	const isAzure = providerName === "azure";
	const isReplicate = providerName === "replicate";
	const isVLLM = providerName === "vllm";
	const isCopilot = providerName === "copilot";
	const supportsBatchAPI = BATCH_SUPPORTED_PROVIDERS.includes(providerName);
	const supportsModelRefresh = MODEL_REFRESH_PROVIDERS.includes(providerName);
	const [refreshModels, { isLoading: isRefreshingModels }] = useRefreshModelsMutation();
	// For providers that support model refresh, enable the button only when a token
	// is available — either a freshly obtained local token (device-login / manual)
	// or a saved/redacted one from the server.
	const copilotKeyValue = supportsModelRefresh ? form.watch('key.value') : undefined;
	const hasToken = !!(copilotKeyValue?.value);

	// Auth type state for Azure: 'api_key', 'entra_id', or 'default_credential'
	const [azureAuthType, setAzureAuthType] = useState<'api_key' | 'entra_id' | 'default_credential'>('api_key')

	// Auth type state for Bedrock: 'iam_role' or 'explicit'
	const [bedrockAuthType, setBedrockAuthType] = useState<'iam_role' | 'explicit'>('iam_role')

	// Detect auth type from existing form values when editing
	useEffect(() => {
		if (form.formState.isDirty) return
		if (isAzure) {
			const clientId = form.getValues('key.azure_key_config.client_id')?.value
			const clientSecret = form.getValues('key.azure_key_config.client_secret')?.value
			const tenantId = form.getValues('key.azure_key_config.tenant_id')?.value
			const apiKey = form.getValues('key.value')?.value
			if (clientId || clientSecret || tenantId) {
				setAzureAuthType('entra_id')
			} else if (!apiKey) {
				setAzureAuthType('default_credential')
			}
		}
	}, [isAzure, form])

	useEffect(() => {
		if (form.formState.isDirty) return
		if (isBedrock) {
			const accessKey = form.getValues('key.bedrock_key_config.access_key')?.value
			const secretKey = form.getValues('key.bedrock_key_config.secret_key')?.value
			if (accessKey || secretKey) {
				setBedrockAuthType('explicit')
			}
		}
	}, [isBedrock, form])

	// Copilot auth type state: 'device_login' or 'manual_token'
	const [copilotAuthType, setCopilotAuthType] = useState<'device_login' | 'manual_token'>('device_login')

	// Detect copilot auth type from existing form values when editing
	useEffect(() => {
		if (form.formState.isDirty) return
		if (isCopilot) {
			const apiKey = form.getValues('key.value')?.value
			if (apiKey && !isRedacted(apiKey)) {
				setCopilotAuthType('manual_token')
			}
		}
	}, [isCopilot, form])

	return (
		<div data-tab="api-keys" className="space-y-4 overflow-hidden">
			{isVertex && (
				<Alert variant="default" className="-z-10">
					<Info className="mt-0.5 h-4 w-4 flex-shrink-0 text-blue-600" />
					<AlertTitle>Authentication Methods</AlertTitle>
					<AlertDescription>
						You can either use service account authentication or API key authentication. Please leave API Key empty when using service
						account authentication.
					</AlertDescription>
				</Alert>
			)}
			<div className="flex items-start gap-4">
				<div className="flex-1">
					<FormField
						control={control}
						name={`key.name`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Name</FormLabel>
								<FormControl>
									<Input placeholder="Production Key" type="text" {...field} />
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
								<FormLabel>Weight</FormLabel>
								<TooltipProvider>
									<Tooltip>
										<TooltipTrigger asChild>
											<span>
												<Info className="text-muted-foreground h-3 w-3" />
											</span>
										</TooltipTrigger>
										<TooltipContent>
											<p>Determines traffic distribution between keys. Higher weights receive more requests.</p>
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
			{/* Hide API Key field for Azure when using Entra ID, for Bedrock when using IAM Role, and for Copilot when using Device Login */}
			{!(isAzure && (azureAuthType === 'entra_id' || azureAuthType === 'default_credential')) && !(isBedrock && bedrockAuthType === 'iam_role') && !(isCopilot && copilotAuthType === 'device_login') && (
				<FormField
					control={control}
					name={`key.value`}
					render={({ field }) => (
						<FormItem>
							<FormLabel>API Key {isVertex ? "(Supported only for gemini and fine-tuned models)" : isVLLM ? "(Optional)" : ""}</FormLabel>
							<FormControl>
								<EnvVarInput placeholder="API Key or env.MY_KEY" type="text" {...field} />
							</FormControl>
							<FormMessage />
						</FormItem>
					)}
				/>
			)}
			{!isVLLM && (
				<FormField
					control={control}
					name={`key.models`}
					render={({ field }) => (
						<FormItem>
							<div className="flex items-center gap-2">
								<FormLabel>Models</FormLabel>
								<TooltipProvider>
									<Tooltip>
										<TooltipTrigger asChild>
											<span>
												<Info className="text-muted-foreground h-3 w-3" />
											</span>
										</TooltipTrigger>
										<TooltipContent>
											<p>Comma-separated list of models this key applies to. Leave blank for all models.</p>
										</TooltipContent>
									</Tooltip>
								</TooltipProvider>
							</div>
							<FormControl>
								<ModelMultiselect
									provider={providerName}
									value={field.value || []}
									onChange={field.onChange}
									unfiltered={true}
									{...(supportsModelRefresh ? {
										onRefresh: () => refreshModels(providerName),
										isRefreshing: isRefreshingModels,
										refreshDisabled: !hasToken,
									} : {})}
								/>
							</FormControl>
							<FormMessage />
						</FormItem>
					)}
				/>
			)}
			{supportsBatchAPI && !isBedrock && !isAzure && <BatchAPIFormField control={control} form={form} />}
			{isAzure && (
				<div className="space-y-4">
					<Separator className="my-6" />
					<div className="space-y-2">
						<FormLabel>Authentication Method</FormLabel>
						<Tabs
							value={azureAuthType}
							onValueChange={(v) => {
								setAzureAuthType(v as "api_key" | "entra_id" | "default_credential");
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
								<TabsTrigger data-testid="apikey-azure-api-key-tab" value="api_key">
									API Key
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-azure-entra-id-tab" value="entra_id">
									Entra ID (Service Principal)
								</TabsTrigger>
								<TabsTrigger data-testid="apikey-azure-default-credential-tab" value="default_credential">
									Default Credential
								</TabsTrigger>
							</TabsList>
						</Tabs>
					</div>
					{azureAuthType === "default_credential" && (
						<p className="text-muted-foreground text-sm">
							Uses DefaultAzureCredential — automatically detects managed identity on Azure VMs and containers, workload identity in AKS,
							environment variables, and Azure CLI. No credentials required.
						</p>
					)}

					<FormField
						control={control}
						name={`key.azure_key_config.endpoint`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Endpoint (Required)</FormLabel>
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
								<FormLabel>API Version (Optional)</FormLabel>
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
										<FormLabel>Client ID (Required)</FormLabel>
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
										<FormLabel>Client Secret (Required)</FormLabel>
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
										<FormLabel>Tenant ID (Required)</FormLabel>
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
											<FormLabel>Scopes (Optional)</FormLabel>
											<TooltipProvider>
												<Tooltip>
													<TooltipTrigger asChild>
														<span>
															<Info className="text-muted-foreground h-3 w-3" />
														</span>
													</TooltipTrigger>
													<TooltipContent>
														<p>
															Optional OAuth scopes for token requests. By default we use https://cognitiveservices.azure.com/.default - add
															additional scopes here if your setup requires extra permissions.
														</p>
													</TooltipContent>
												</Tooltip>
											</TooltipProvider>
										</div>
										<FormControl>
											<TagInput data-testid="apikey-azure-scopes-input" placeholder="Add scope (Enter or comma)" value={field.value ?? []} onValueChange={field.onChange} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</>
					)}

					<FormField
						control={control}
						name={`key.azure_key_config.deployments`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Deployments (Required)</FormLabel>
								<FormDescription>JSON object mapping model names to deployment names</FormDescription>
								<FormControl>
									<Textarea
										placeholder='{"gpt-4": "my-gpt4-deployment", "gpt-3.5-turbo": "my-gpt35-deployment"}'
										value={typeof field.value === "string" ? field.value : JSON.stringify(field.value || {}, null, 2)}
										onChange={(e) => {
											form.clearErrors("key.azure_key_config.deployments");
											// Store as string during editing to allow intermediate invalid states
											field.onChange(e.target.value);
										}}
										onBlur={(e) => {
											// Try to parse as JSON on blur, but keep as string if invalid
											const value = e.target.value.trim();
											if (value) {
												try {
													const parsed = JSON.parse(value);
													if (typeof parsed === "object" && parsed !== null) {
														field.onChange(parsed);
													}
												} catch {
													// Keep as string for validation on submit
												}
											}
											field.onBlur();
										}}
										rows={3}
										className="max-w-full font-mono text-sm wrap-anywhere"
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					{supportsBatchAPI && <BatchAPIFormField control={control} form={form} />}
				</div>
			)}
			{isVertex && (
				<div className="space-y-4">
					<Separator className="my-6" />
					<FormField
						control={control}
						name={`key.vertex_key_config.project_id`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Project ID (Required)</FormLabel>
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
								<FormLabel>Project Number (Required only for fine-tuned models)</FormLabel>
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
								<FormLabel>Region (Required)</FormLabel>
								<FormControl>
									<EnvVarInput placeholder="us-central1 or env.VERTEX_REGION" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<Alert variant="default" className="-z-10">
						<Info className="mt-0.5 h-4 w-4 flex-shrink-0 text-blue-600" />
						<AlertTitle>Service Account Authentication</AlertTitle>
						<AlertDescription>
							Leave both API Key and Auth Credentials empty to use service account attached to your environment.
						</AlertDescription>
					</Alert>
					<FormField
						control={control}
						name={`key.vertex_key_config.auth_credentials`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Auth Credentials</FormLabel>
								<FormDescription>Service account JSON object or env.VAR_NAME</FormDescription>
								<FormControl>
									<EnvVarInput
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
										<span>Credentials are stored securely. Edit to update.</span>
									</div>
								)}
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name={`key.vertex_key_config.deployments`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Deployments (Optional)</FormLabel>
								<FormDescription>JSON object mapping model names to custom fine-tuned model deployment ids</FormDescription>
								<FormControl>
									<Textarea
										placeholder='{"custom-gemini-2.5-pro": "123456789", "custom-gemini-2.0-flash-001": "987654321"}'
										value={typeof field.value === "string" ? field.value : JSON.stringify(field.value || {}, null, 2)}
										onChange={(e) => {
											// Store as string during editing to allow intermediate invalid states
											field.onChange(e.target.value);
										}}
										onBlur={(e) => {
											// Try to parse as JSON on blur, but keep as string if invalid
											const value = e.target.value.trim();
											if (value) {
												try {
													const parsed = JSON.parse(value);
													if (typeof parsed === "object" && parsed !== null) {
														field.onChange(parsed);
													}
												} catch {
													// Keep as string for validation on submit
												}
											}
											field.onBlur();
										}}
										rows={3}
										className="max-w-full font-mono text-sm wrap-anywhere"
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
				</div>
			)}
			{isReplicate && (
				<div className="space-y-4">
					<Separator className="my-6" />
					<FormField
						control={control}
						name={`key.replicate_key_config.deployments`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Deployments (Optional)</FormLabel>
								<FormDescription>JSON object mapping model names to deployment names</FormDescription>
								<FormControl>
									<Textarea
										placeholder='{"my-model": "my-deployment", "another-model": "another-deployment"}'
										value={typeof field.value === "string" ? field.value : JSON.stringify(field.value || {}, null, 2)}
										onChange={(e) => {
											// Store as string during editing to allow intermediate invalid states
											field.onChange(e.target.value);
										}}
										onBlur={(e) => {
											// Try to parse as JSON on blur, but keep as string if invalid
											const value = e.target.value.trim();
											if (value) {
												try {
													const parsed = JSON.parse(value);
													if (typeof parsed === "object" && parsed !== null) {
														field.onChange(parsed);
													}
												} catch {
													// Keep as string for validation on submit
												}
											}
											field.onBlur();
										}}
										rows={3}
										className="max-w-full font-mono text-sm wrap-anywhere"
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
				</div>
			)}
			{isCopilot && (
				<CopilotDeviceLoginSection control={control} form={form} copilotAuthType={copilotAuthType} setCopilotAuthType={setCopilotAuthType} />
			)}
			{isVLLM && (
				<div className="space-y-4">
					<Separator className="my-6" />
					<FormField
						control={control}
						name="key.vllm_key_config.url"
						render={({ field }) => (
							<FormItem>
								<FormLabel>Server URL (Required)</FormLabel>
								<FormDescription>Base URL of the vLLM server (e.g. http://vllm-server:8000 or env.VLLM_URL)</FormDescription>
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
								<FormLabel>Model Name (Required)</FormLabel>
								<FormDescription>Exact model name served on this vLLM instance</FormDescription>
								<FormControl>
									<Input data-testid="key-input-vllm-model-name" placeholder="meta-llama/Llama-3-70b-hf" {...field} />
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
						<FormLabel>Authentication Method</FormLabel>
						<Tabs
							value={bedrockAuthType}
							onValueChange={(v) => {
								setBedrockAuthType(v as "iam_role" | "explicit");
								if (v === "iam_role") {
									// Clear generic API key and explicit credentials when switching to IAM Role
									form.setValue("key.value", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_key_config.access_key", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_key_config.secret_key", undefined, { shouldDirty: true });
									form.setValue("key.bedrock_key_config.session_token", undefined, { shouldDirty: true });
								}
							}}
						>
							<TabsList className="grid w-full grid-cols-2">
								<TabsTrigger data-testid="apikey-bedrock-iam-role-tab" value="iam_role">IAM Role (Inherited)</TabsTrigger>
								<TabsTrigger data-testid="apikey-bedrock-explicit-credentials-tab" value="explicit">Explicit Credentials</TabsTrigger>
							</TabsList>
						</Tabs>
						{bedrockAuthType === "iam_role" && (
							<p className="text-muted-foreground text-sm">Uses IAM roles attached to your environment (EC2, Lambda, ECS, EKS).</p>
						)}
					</div>

					{bedrockAuthType === "explicit" && (
						<>
							<FormField
								control={control}
								name={`key.bedrock_key_config.access_key`}
								render={({ field }) => (
									<FormItem>
										<FormLabel>Access Key (Required)</FormLabel>
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
										<FormLabel>Secret Key (Required)</FormLabel>
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
										<FormLabel>Session Token (Optional)</FormLabel>
										<FormControl>
											<EnvVarInput placeholder="your-aws-session-token or env.AWS_SESSION_TOKEN" {...field} />
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</>
					)}

					<FormField
						control={control}
						name={`key.bedrock_key_config.region`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Region (Required)</FormLabel>
								<FormControl>
									<EnvVarInput placeholder="us-east-1 or env.AWS_REGION" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name={`key.bedrock_key_config.role_arn`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Assume Role ARN (Optional)</FormLabel>
								<FormDescription>
									Assume an IAM role before requests. Works with both explicit credentials and inherited IAM (EC2, ECS, EKS).
								</FormDescription>
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
								<FormLabel>External ID (Optional)</FormLabel>
								<FormDescription>Required by the role's trust policy when using cross-account access</FormDescription>
								<FormControl>
									<EnvVarInput data-testid="apikey-bedrock-external-id-input" placeholder="external-id or env.AWS_EXTERNAL_ID" {...field} />
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
								<FormLabel>Session Name (Optional)</FormLabel>
								<FormDescription>AssumeRole session name (defaults to bifrost-session)</FormDescription>
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
					<FormField
						control={control}
						name={`key.bedrock_key_config.arn`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>ARN (Optional)</FormLabel>
								<FormControl>
									<EnvVarInput placeholder="arn:aws:bedrock:us-east-1:123:inference-profile or env.AWS_ARN" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name={`key.bedrock_key_config.deployments`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Deployments (Optional)</FormLabel>
								<FormDescription>JSON object mapping model names to inference profile names</FormDescription>
								<FormControl>
									<Textarea
										placeholder='{"claude-3-sonnet": "us.anthropic.claude-3-sonnet-20240229-v1:0", "claude-v2": "us.anthropic.claude-v2:1"}'
										value={typeof field.value === "string" ? field.value : JSON.stringify(field.value || {}, null, 2)}
										onChange={(e) => {
											// Store as string during editing to allow intermediate invalid states
											field.onChange(e.target.value);
										}}
										onBlur={(e) => {
											// Try to parse as JSON on blur, but keep as string if invalid
											const value = e.target.value.trim();
											if (value) {
												try {
													const parsed = JSON.parse(value);
													if (typeof parsed === "object" && parsed !== null) {
														field.onChange(parsed);
													}
												} catch {
													// Keep as string for validation on submit
												}
											}
											field.onBlur();
										}}
										rows={3}
										className="max-w-full font-mono text-sm wrap-anywhere"
									/>
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

// Copilot GitHub OAuth device code login section
function CopilotDeviceLoginSection({
	control,
	form,
	copilotAuthType,
	setCopilotAuthType,
}: {
	control: Control<any>;
	form: UseFormReturn<any>;
	copilotAuthType: 'device_login' | 'manual_token';
	setCopilotAuthType: (v: 'device_login' | 'manual_token') => void;
}) {
	const [deviceState, setDeviceState] = useState<{
		status: 'idle' | 'awaiting_auth' | 'complete' | 'error';
		userCode?: string;
		verificationUri?: string;
		deviceCode?: string;
		interval?: number;
		error?: string;
	}>({ status: 'idle' });

	const [copied, setCopied] = useState(false);
	const [countdown, setCountdown] = useState<number | null>(null);
	const [isChecking, setIsChecking] = useState(false);
	const countdownRef = useRef<ReturnType<typeof setInterval> | null>(null);
	const authTypeRef = useRef(copilotAuthType);
	const pollSessionRef = useRef(0);

	useEffect(() => {
		authTypeRef.current = copilotAuthType;
	}, [copilotAuthType]);

	const clearCountdown = useCallback(() => {
		if (countdownRef.current) {
			clearInterval(countdownRef.current);
			countdownRef.current = null;
		}
		setCountdown(null);
	}, []);

	// Clean up on unmount
	useEffect(() => {
		return () => {
			if (countdownRef.current) clearInterval(countdownRef.current);
		};
	}, []);

	const invalidatePolling = useCallback(() => {
		pollSessionRef.current += 1;
	}, []);

	const initiateLogin = useCallback(async () => {
		invalidatePolling();
		setDeviceState({ status: 'awaiting_auth' });
		try {
			const baseUrl = getApiBaseUrl();
			const resp = await fetch(`${baseUrl}/providers/copilot/device-login/initiate`, {
				method: 'POST',
				credentials: 'include',
				headers: { 'Content-Type': 'application/json' },
			});
			if (!resp.ok) {
				const errData = await resp.json().catch(() => ({ error: { message: resp.statusText } }));
				setDeviceState({ status: 'error', error: errData?.error?.message || 'Failed to start device login' });
				return;
			}
			const data = await resp.json();
			setDeviceState({
				status: 'awaiting_auth',
				userCode: data.user_code,
				verificationUri: data.verification_uri,
				deviceCode: data.device_code,
				interval: data.interval || 5,
			});
		} catch (err) {
			setDeviceState({ status: 'error', error: 'Failed to connect to server' });
		}
	}, [invalidatePolling]);

	const startCountdown = useCallback((seconds?: number) => {
		clearCountdown();
		const interval = (seconds ?? deviceState.interval) || 5;
		setCountdown(interval);
		countdownRef.current = setInterval(() => {
			setCountdown(prev => {
				if (prev === null || prev <= 1) return 0;
				return prev - 1;
			});
		}, 1000);
	}, [clearCountdown, deviceState.interval]);

	const pollOnce = useCallback(async () => {
		if (!deviceState.deviceCode || authTypeRef.current !== 'device_login') return;

		clearCountdown();
		setIsChecking(true);
		const pollSession = pollSessionRef.current;

		try {
			const baseUrl = getApiBaseUrl();
			const resp = await fetch(`${baseUrl}/providers/copilot/device-login/poll`, {
				method: 'POST',
				credentials: 'include',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ device_code: deviceState.deviceCode }),
			});
			if (pollSession !== pollSessionRef.current || authTypeRef.current !== 'device_login') {
				return;
			}
			if (!resp.ok) {
				setDeviceState(prev => ({ ...prev, status: 'error', error: 'Failed to check authorization status' }));
				return;
			}
			const data = await resp.json();
			if (pollSession !== pollSessionRef.current || authTypeRef.current !== 'device_login') {
				return;
			}

			if (data.status === 'complete' && data.access_token) {
				form.setValue('key.value', { value: data.access_token, env_var: '', from_env: false }, { shouldDirty: true, shouldValidate: true });
				setDeviceState(prev => ({ ...prev, status: 'complete' }));
				return;
			}

			if (data.status === 'expired' || data.status === 'error') {
				setDeviceState(prev => ({ ...prev, status: 'error', error: data.error || 'Authorization failed' }));
				return;
			}

			// Still pending — restart countdown.
			// If GitHub sent "slow_down", increase the interval by 5s as per the device flow spec.
			let nextInterval = deviceState.interval || 5;
			if (data.status === 'slow_down') {
				nextInterval += 5;
				setDeviceState(prev => ({ ...prev, interval: nextInterval }));
			}
			startCountdown(nextInterval);
		} catch {
			if (pollSession === pollSessionRef.current && authTypeRef.current === 'device_login') {
				setDeviceState(prev => ({ ...prev, status: 'error', error: 'Failed to connect to server' }));
			}
		} finally {
			if (pollSession === pollSessionRef.current) {
				setIsChecking(false);
			}
		}
	}, [deviceState.deviceCode, deviceState.interval, form, clearCountdown, startCountdown]);

	// Trigger poll when countdown reaches 0 (only in device_login mode)
	useEffect(() => {
		if (countdown === 0 && !isChecking && copilotAuthType === 'device_login') {
			pollOnce();
		}
	}, [countdown, isChecking, pollOnce, copilotAuthType]);

	const copyCode = useCallback(() => {
		if (deviceState.userCode) {
			navigator.clipboard.writeText(deviceState.userCode);
			setCopied(true);
			setTimeout(() => setCopied(false), 2000);
		}
	}, [deviceState.userCode]);

	// Auto-start countdown when user code is obtained
	useEffect(() => {
		if (deviceState.status === 'awaiting_auth' && deviceState.userCode) {
			startCountdown();
		}
	}, [deviceState.status, deviceState.userCode, startCountdown]);

	const resetFlow = useCallback(() => {
		invalidatePolling();
		clearCountdown();
		setIsChecking(false);
		setDeviceState({ status: 'idle' });
	}, [clearCountdown, invalidatePolling]);

	return (
		<div className="space-y-4">
			<Separator className="my-6" />
			<div className="space-y-2">
				<FormLabel>Authentication Method</FormLabel>
				<Tabs value={copilotAuthType} onValueChange={(v) => {
					setCopilotAuthType(v as 'device_login' | 'manual_token');
					if (v === 'device_login') {
						resetFlow();
					} else {
						// Switching away from device login — stop any active polling
						invalidatePolling();
						clearCountdown();
						setIsChecking(false);
					}
				}}>
					<TabsList className="grid w-full grid-cols-2">
						<TabsTrigger data-testid="apikey-copilot-device-login-tab" value="device_login">GitHub Device Login</TabsTrigger>
						<TabsTrigger data-testid="apikey-copilot-manual-token-tab" value="manual_token">Manual Token</TabsTrigger>
					</TabsList>
				</Tabs>
			</div>

			{copilotAuthType === 'device_login' && (
				<div className="space-y-4">
					{deviceState.status === 'idle' && (
						<>
							<Alert variant="default">
								<Info className="mt-0.5 h-4 w-4 flex-shrink-0 text-blue-600" />
								<AlertTitle>GitHub Copilot Authentication</AlertTitle>
								<AlertDescription>
									Copilot requires a GitHub OAuth token obtained through the device login flow.
									Your GitHub account must have an active Copilot subscription.
								</AlertDescription>
							</Alert>
							<Button
								type="button"
								data-testid="copilot-device-login-button"
								onClick={initiateLogin}
								className="w-full"
							>
								Login with GitHub
							</Button>
						</>
					)}

					{deviceState.status === 'awaiting_auth' && deviceState.userCode && (
						<div className="space-y-4">
							<Alert variant="default">
								<Info className="mt-0.5 h-4 w-4 flex-shrink-0 text-blue-600" />
								<AlertTitle>Enter this code on GitHub</AlertTitle>
								<AlertDescription className="space-y-3">
									<div className="flex items-center gap-3 pt-2">
										<code className="bg-muted rounded-md px-4 py-2 text-2xl font-bold tracking-widest">
											{deviceState.userCode}
										</code>
										<Button
											type="button"
											variant="outline"
											size="sm"
											onClick={copyCode}
											data-testid="copilot-copy-code-button"
										>
											{copied ? <CheckCircle2 className="h-4 w-4 text-green-600" /> : <Copy className="h-4 w-4" />}
										</Button>
									</div>
									<p className="text-sm">
										Visit{" "}
										<a
											href={deviceState.verificationUri}
											target="_blank"
											rel="noopener noreferrer"
											className="text-blue-600 underline hover:text-blue-800 inline-flex items-center gap-1"
										>
											{deviceState.verificationUri}
											<ExternalLink className="h-3 w-3" />
										</a>
										{" "}and enter the code above to authorize Bifrost.
									</p>
								</AlertDescription>
							</Alert>

							<Button
								type="button"
								data-testid="copilot-confirm-auth-button"
								onClick={pollOnce}
								disabled={isChecking}
								className="w-full"
							>
								{isChecking ? (
									<><Loader2 className="h-4 w-4 animate-spin" /> Checking authorization...</>
								) : countdown !== null && countdown > 0 ? (
									<>Check authorization ({countdown}s)</>
								) : (
									<>Check authorization</>
								)}
							</Button>

							<Button
								type="button"
								variant="ghost"
								size="sm"
								onClick={resetFlow}
								className="w-full"
							>
								Cancel and start over
							</Button>
						</div>
					)}

					{deviceState.status === 'complete' && (
						<Alert variant="default">
							<CheckCircle2 className="mt-0.5 h-4 w-4 flex-shrink-0 text-green-600" />
							<AlertTitle>Authentication successful</AlertTitle>
							<AlertDescription>
								GitHub OAuth token has been set. You can now save the provider configuration.
							</AlertDescription>
						</Alert>
					)}

					{deviceState.status === 'error' && (
						<div className="space-y-3">
							<Alert variant="destructive">
								<AlertTitle>Authentication failed</AlertTitle>
								<AlertDescription>
									{deviceState.error || 'An unknown error occurred'}
								</AlertDescription>
							</Alert>
							<Button
								type="button"
								variant="outline"
								onClick={initiateLogin}
								className="w-full"
								data-testid="copilot-retry-login-button"
							>
								Try again
							</Button>
						</div>
					)}
				</div>
			)}

			{copilotAuthType === 'manual_token' && (
				<div className="space-y-2">
					<Alert variant="default">
						<Info className="mt-0.5 h-4 w-4 flex-shrink-0 text-blue-600" />
						<AlertTitle>Manual Token Entry</AlertTitle>
						<AlertDescription>
							Enter an OAuth access token obtained from the GitHub device code flow.
							Standard GitHub PATs will not work — you must use the device login flow token.
						</AlertDescription>
					</Alert>
					<FormField
						control={control}
						name={`key.value`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>OAuth Access Token</FormLabel>
								<FormControl>
									<EnvVarInput placeholder="ghu_xxxxxxxxxxxx or env.GITHUB_COPILOT_TOKEN" type="text" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
				</div>
			)}
		</div>
	);
}

// Bedrock S3 configuration section for batch operations
function BedrockBatchS3ConfigSection({ control, form }: { control: Control<any>; form: UseFormReturn<any> }) {
	const buckets = form.watch("key.bedrock_key_config.batch_s3_config.buckets") || [];

	const addBucket = () => {
		const currentBuckets = form.getValues("key.bedrock_key_config.batch_s3_config.buckets") || [];
		form.setValue(
			"key.bedrock_key_config.batch_s3_config.buckets",
			[...currentBuckets, { bucket_name: "", prefix: "", is_default: currentBuckets.length === 0 }],
			{ shouldDirty: true },
		);
	};

	const removeBucket = (index: number) => {
		const currentBuckets = form.getValues("key.bedrock_key_config.batch_s3_config.buckets") || [];
		const newBuckets = currentBuckets.filter((_: any, i: number) => i !== index);
		// If we removed the default bucket and there are still buckets, make the first one default
		if (currentBuckets[index]?.is_default && newBuckets.length > 0) {
			newBuckets[0].is_default = true;
		}
		form.setValue("key.bedrock_key_config.batch_s3_config.buckets", newBuckets, { shouldDirty: true });
	};

	const setDefaultBucket = (index: number) => {
		const currentBuckets = form.getValues("key.bedrock_key_config.batch_s3_config.buckets") || [];
		const newBuckets = currentBuckets.map((bucket: any, i: number) => ({
			...bucket,
			is_default: i === index,
		}));
		form.setValue("key.bedrock_key_config.batch_s3_config.buckets", newBuckets, { shouldDirty: true });
	};

	return (
		<div className="space-y-4">
			<Separator className="my-4" />
			<div className="flex items-center justify-between">
				<div>
					<FormLabel className="text-base">S3 Bucket Configuration</FormLabel>
					<FormDescription>Configure S3 buckets for Bedrock batch operations</FormDescription>
				</div>
				<Button type="button" variant="outline" size="sm" onClick={addBucket}>
					<Plus className="mr-2 h-4 w-4" />
					Add Bucket
				</Button>
			</div>
			{buckets.length === 0 && (
				<Alert variant="default" className="-z-10">
					<Info className="mt-0.5 h-4 w-4 flex-shrink-0 text-blue-600" />
					<AlertTitle>No S3 Buckets Configured</AlertTitle>
					<AlertDescription>
						Add at least one S3 bucket to store batch job input/output files for Bedrock batch operations.
					</AlertDescription>
				</Alert>
			)}
			{buckets.map((_: any, index: number) => (
				<div key={index} className="space-y-4 rounded-sm border p-2">
					<div className="flex items-center justify-between">
						<div className="flex items-center gap-2">
							<span className="text-sm font-medium">Bucket {index + 1}</span>
							{buckets[index]?.is_default && (
								<span className="bg-primary/10 text-primary rounded-full px-2 py-0.5 text-xs font-medium">Default</span>
							)}
						</div>
						<div className="flex items-center gap-2">
							{!buckets[index]?.is_default && buckets.length > 1 && (
								<Button type="button" variant="ghost" size="sm" onClick={() => setDefaultBucket(index)}>
									Set as Default
								</Button>
							)}
							<Button type="button" variant="ghost" size="sm" onClick={() => removeBucket(index)}>
								<Trash2 className="text-destructive h-4 w-4" />
							</Button>
						</div>
					</div>
					<FormField
						control={control}
						name={`key.bedrock_key_config.batch_s3_config.buckets.${index}.bucket_name`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Bucket Name</FormLabel>
								<FormControl>
									<Input placeholder="my-batch-bucket or env.S3_BUCKET_NAME" {...field} value={field.value ?? ""} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name={`key.bedrock_key_config.batch_s3_config.buckets.${index}.prefix`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Prefix (Optional)</FormLabel>
								<FormControl>
									<Input placeholder="batch-jobs/ or env.S3_PREFIX" {...field} value={field.value ?? ""} />
								</FormControl>
								<FormDescription>Optional path prefix for batch files in the bucket</FormDescription>
								<FormMessage />
							</FormItem>
						)}
					/>
				</div>
			))}
		</div>
	);
}
