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
import {
	useInitiateAnthropicOAuthMutation,
	useExchangeAnthropicOAuthCodeMutation,
	useRefreshAnthropicOAuthTokenMutation,
	useLogoutAnthropicOAuthMutation,
	getErrorMessage,
} from "@/lib/store";
import { isRedacted } from "@/lib/utils/validation";
import { CheckCircle2, ExternalLink, Info, Loader2, Plus, RefreshCw, Trash2, Unplug } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { Control, UseFormReturn } from "react-hook-form";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";

// Providers that support batch APIs
const BATCH_SUPPORTED_PROVIDERS = ["openai", "bedrock", "anthropic", "gemini", "azure"];

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
	const isAnthropic = providerName === "anthropic";
	const supportsBatchAPI = BATCH_SUPPORTED_PROVIDERS.includes(providerName);

	// Auth type state for Azure: 'api_key', 'entra_id', or 'default_credential'
	const [azureAuthType, setAzureAuthType] = useState<'api_key' | 'entra_id' | 'default_credential'>('api_key')

	// Auth type state for Bedrock: 'iam_role', 'explicit', or 'api_key'
	const [bedrockAuthType, setBedrockAuthType] = useState<'iam_role' | 'explicit' | 'api_key'>('iam_role')

	// Auth type state for Anthropic: 'api_key' or 'oauth'
	const [anthropicAuthType, setAnthropicAuthType] = useState<'api_key' | 'oauth'>('api_key')

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
		if (isAnthropic) {
			const oauthConfigId = form.getValues('key.anthropic_oauth_key_config.oauth_config_id')
			if (oauthConfigId) {
				setAnthropicAuthType('oauth')
			}
		}
	}, [isAnthropic, form])

	useEffect(() => {
		if (form.formState.isDirty) return
		if (isBedrock) {
			const accessKey = form.getValues('key.bedrock_key_config.access_key')?.value
			const secretKey = form.getValues('key.bedrock_key_config.secret_key')?.value
			const apiKey = form.getValues('key.value')?.value
			if (accessKey || secretKey) {
				setBedrockAuthType('explicit')
			} else if (apiKey) {
				setBedrockAuthType('api_key')
			}
		}
	}, [isBedrock, form])

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
			{/* Hide API Key field for Azure when using Entra ID/Default Credential, Bedrock, and Anthropic when using OAuth */}
			{!isBedrock &&
				!(isAzure && (azureAuthType === "entra_id" || azureAuthType === "default_credential")) &&
				!(isAnthropic && anthropicAuthType === "oauth") && (
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
				<>
				<FormField
					control={control}
					name={`key.models`}
					render={({ field }) => (
						<FormItem>
							<div className="flex items-center gap-2">
								<FormLabel>Allowed Models</FormLabel>
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
								<ModelMultiselect provider={providerName} value={field.value || []} onChange={field.onChange} unfiltered={true} />
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
							<FormLabel>Blacklisted models</FormLabel>
							<TooltipProvider>
								<Tooltip>
									<TooltipTrigger asChild>
										<span>
											<Info className="text-muted-foreground h-3 w-3" />
										</span>
									</TooltipTrigger>
									<TooltipContent className="max-w-sm">
										<p>
											Comma-separated list of models this key must never use. If a model appears in both Allowed Models and here, the blacklist wins. Leave
											empty if none.
										</p>
									</TooltipContent>
								</Tooltip>
							</TooltipProvider>
						</div>
						<FormControl>
							<ModelMultiselect provider={providerName} value={field.value || []} onChange={field.onChange} unfiltered={true} />
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
								setBedrockAuthType(v as "iam_role" | "explicit" | "api_key");
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
								<TabsTrigger data-testid="apikey-bedrock-iam-role-tab" value="iam_role">IAM Role (Inherited)</TabsTrigger>
								<TabsTrigger data-testid="apikey-bedrock-explicit-credentials-tab" value="explicit">Explicit Credentials</TabsTrigger>
								<TabsTrigger data-testid="apikey-bedrock-api-key-tab" value="api_key">API Key</TabsTrigger>
							</TabsList>
						</Tabs>
						{bedrockAuthType === "iam_role" && (
							<p className="text-muted-foreground text-sm">Uses IAM roles attached to your environment (EC2, Lambda, ECS, EKS).</p>
						)}
						{bedrockAuthType === "api_key" && (
							<p className="text-muted-foreground text-sm">Uses a Bearer token for API key authentication.</p>
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

					{bedrockAuthType === "api_key" && (
						<FormField
							control={control}
							name={`key.value`}
							render={({ field }) => (
								<FormItem>
									<FormLabel>API Key</FormLabel>
									<FormControl>
										<EnvVarInput data-testid="apikey-bedrock-api-key-input" placeholder="API Key or env.BEDROCK_API_KEY" type="text" {...field} />
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
								<FormLabel>Region (Required)</FormLabel>
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
						</>
					)}
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
			{isAnthropic && (
				<div className="space-y-4">
					<Separator className="my-6" />
					<div className="space-y-2">
						<FormLabel>Authentication Method</FormLabel>
						<Tabs value={anthropicAuthType} onValueChange={(v) => {
							setAnthropicAuthType(v as 'api_key' | 'oauth')
							if (v === 'oauth') {
								// Clear API key when switching to OAuth
								form.setValue('key.value', undefined)
							}
							// Note: switching to api_key does NOT clear OAuth config here.
							// The AnthropicOAuthSection's Disconnect button handles server-side
							// logout before clearing the config, preventing orphaned refresh tokens.
						}}>

							<TabsList className="grid w-full grid-cols-2">
								<TabsTrigger value="api_key" data-testid="anthropic-auth-tab-api-key">API Key</TabsTrigger>
								<TabsTrigger value="oauth" data-testid="anthropic-auth-tab-oauth">OAuth (Claude Pro/Max)</TabsTrigger>
							</TabsList>
						</Tabs>
					</div>

					{anthropicAuthType === 'oauth' && (
						<AnthropicOAuthSection form={form} />
					)}
				</div>
			)}
		</div>
	);
}

// Anthropic OAuth section component
type OAuthFlowState = 'idle' | 'initiated' | 'connected' | 'error';

function AnthropicOAuthSection({ form }: { form: UseFormReturn<any> }) {
	const [flowState, setFlowState] = useState<OAuthFlowState>('idle')
	const [oauthConfigId, setOauthConfigId] = useState<string>('')
	const [authCode, setAuthCode] = useState('')
	const [errorMessage, setErrorMessage] = useState('')
	const oauthConfigIdRef = useRef<string>('')

	const [initiateOAuth, { isLoading: isInitiating }] = useInitiateAnthropicOAuthMutation()
	const [exchangeCode, { isLoading: isExchanging }] = useExchangeAnthropicOAuthCodeMutation()
	const [refreshToken, { isLoading: isRefreshing }] = useRefreshAnthropicOAuthTokenMutation()
	const [logoutOAuth, { isLoading: isLoggingOut }] = useLogoutAnthropicOAuthMutation()

	// Keep ref in sync with state
	useEffect(() => {
		oauthConfigIdRef.current = oauthConfigId
	}, [oauthConfigId])

	// Detect existing OAuth config on mount
	useEffect(() => {
		const existingConfigId = form.getValues('key.anthropic_oauth_key_config.oauth_config_id')
		if (existingConfigId) {
			setOauthConfigId(existingConfigId)
			setFlowState('connected')
		}
	}, [form])

	// Cleanup pending OAuth flows on unmount (e.g. tab switch)
	useEffect(() => {
		return () => {
			const pendingConfigId = oauthConfigIdRef.current
			if (pendingConfigId && !form.getValues('key.anthropic_oauth_key_config.oauth_config_id')) {
				// Fire-and-forget: revoke the pending config that was never completed
				logoutOAuth({ oauth_config_id: pendingConfigId })
			}
		}
	}, [logoutOAuth, form])

	const handleInitiate = useCallback(async () => {
		// Open blank tab synchronously to avoid popup blockers
		const authWindow = window.open('about:blank', '_blank')
		if (authWindow) {
			authWindow.opener = null
		}
		try {
			const result = await initiateOAuth().unwrap()
			setOauthConfigId(result.oauth_config_id)
			if (authWindow && !authWindow.closed) {
				authWindow.location.href = result.authorize_url
			} else {
				// Popup was blocked — clean up the initiated config and show error
				logoutOAuth({ oauth_config_id: result.oauth_config_id })
				setErrorMessage('Popup was blocked by your browser. Please allow popups for this site and try again.')
				setFlowState('error')
				return
			}
			setFlowState('initiated')
		} catch (err) {
			authWindow?.close()
			setErrorMessage(getErrorMessage(err))
			setFlowState('error')
		}
	}, [initiateOAuth])

	const handleExchange = useCallback(async () => {
		if (!authCode.trim()) return
		try {
			await exchangeCode({ code: authCode.trim(), oauth_config_id: oauthConfigId }).unwrap()
			form.setValue('key.anthropic_oauth_key_config', { oauth_config_id: oauthConfigId }, { shouldDirty: true, shouldValidate: true })
			setFlowState('connected')
			setAuthCode('')
			toast.success('Successfully connected with Anthropic OAuth')
		} catch (err) {
			setErrorMessage(getErrorMessage(err))
			setFlowState('error')
		}
	}, [authCode, oauthConfigId, exchangeCode, form])

	const handleRefresh = useCallback(async () => {
		try {
			await refreshToken({ oauth_config_id: oauthConfigId }).unwrap()
			toast.success('Token refreshed successfully')
		} catch (err) {
			toast.error('Failed to refresh token', { description: getErrorMessage(err) })
		}
	}, [oauthConfigId, refreshToken])

	const handleDisconnect = useCallback(async () => {
		try {
			await logoutOAuth({ oauth_config_id: oauthConfigId }).unwrap()
			form.setValue('key.anthropic_oauth_key_config', undefined, { shouldDirty: true })
			setOauthConfigId('')
			setFlowState('idle')
			toast.success('Disconnected from Anthropic OAuth')
		} catch (err) {
			toast.error('Failed to disconnect', { description: getErrorMessage(err) })
		}
	}, [oauthConfigId, logoutOAuth, form])

	const handleCancel = useCallback(async () => {
		// Clean up server-side OAuth config for the initiated but uncompleted flow
		if (oauthConfigId) {
			try {
				await logoutOAuth({ oauth_config_id: oauthConfigId }).unwrap()
			} catch {
				// Best-effort cleanup; stale pending configs expire after 15 minutes
			}
		}
		setFlowState('idle')
		setAuthCode('')
		setOauthConfigId('')
	}, [oauthConfigId, logoutOAuth])

	if (flowState === 'connected') {
		return (
			<div className="rounded-sm border border-green-200 bg-green-50 p-4 dark:border-green-800 dark:bg-green-950">
				<div className="flex items-center justify-between">
					<div className="flex items-center gap-3">
						<CheckCircle2 className="h-5 w-5 text-green-600 dark:text-green-400" />
						<div>
							<div className="flex items-center gap-2">
								<span className="text-sm font-medium">Anthropic OAuth</span>
								<Badge variant="secondary" className="text-xs bg-green-100 text-green-700 dark:bg-green-900 dark:text-green-300">Connected</Badge>
							</div>
							<p className="text-xs text-muted-foreground font-mono mt-0.5">{oauthConfigId}</p>
						</div>
					</div>
					<div className="flex items-center gap-2">
						<Button type="button" variant="outline" size="sm" onClick={handleRefresh} disabled={isRefreshing} data-testid="anthropic-oauth-refresh">
							{isRefreshing ? <Loader2 className="h-4 w-4 animate-spin" /> : <RefreshCw className="h-4 w-4" />}
							Refresh
						</Button>
						<Button type="button" variant="outline" size="sm" onClick={handleDisconnect} disabled={isLoggingOut} data-testid="anthropic-oauth-disconnect">
							{isLoggingOut ? <Loader2 className="h-4 w-4 animate-spin" /> : <Unplug className="h-4 w-4" />}
							Disconnect
						</Button>
					</div>
				</div>
			</div>
		)
	}

	if (flowState === 'error') {
		return (
			<div className="rounded-sm border border-red-200 bg-red-50 p-4 dark:border-red-800 dark:bg-red-950">
				<div className="space-y-3">
					<p className="text-sm text-red-700 dark:text-red-300">{errorMessage || 'An error occurred during the OAuth flow.'}</p>
					<Button type="button" variant="outline" size="sm" onClick={() => { setFlowState('idle'); setErrorMessage(''); }} data-testid="anthropic-oauth-retry">
						Try Again
					</Button>
				</div>
			</div>
		)
	}

	if (flowState === 'initiated') {
		return (
			<div className="space-y-4">
				<Alert variant="default" className="-z-10">
					<Info className="mt-0.5 h-4 w-4 flex-shrink-0 text-blue-600" />
					<AlertTitle>Complete Authorization</AlertTitle>
					<AlertDescription>
						<ol className="list-decimal list-inside space-y-1 mt-2 text-sm">
							<li>A new tab has been opened to Anthropic&apos;s authorization page</li>
							<li>Sign in with your Claude Pro/Max account</li>
							<li>Copy the authorization code from the callback page</li>
							<li>Paste the code below and click &quot;Exchange Code&quot;</li>
						</ol>
					</AlertDescription>
				</Alert>
				<div className="space-y-2">
					<FormLabel>Authorization Code</FormLabel>
					<div className="flex gap-2">
						<Input
							placeholder="Paste the authorization code here"
							value={authCode}
							onChange={(e) => setAuthCode(e.target.value)}
							className="font-mono text-sm"
							data-testid="anthropic-oauth-code-input"
						/>
						<Button type="button" onClick={handleExchange} disabled={isExchanging || !authCode.trim()} data-testid="anthropic-oauth-exchange">
							{isExchanging && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
							Exchange Code
						</Button>
					</div>
					<button type="button" className="text-xs text-muted-foreground underline hover:text-foreground" onClick={handleCancel} data-testid="anthropic-oauth-cancel">
						Cancel
					</button>
				</div>
			</div>
		)
	}

	// Idle state
	return (
		<div className="space-y-4">
			<Alert variant="default" className="-z-10">
				<Info className="mt-0.5 h-4 w-4 flex-shrink-0 text-blue-600" />
				<AlertTitle>Claude Pro/Max OAuth</AlertTitle>
				<AlertDescription>
					Connect your Claude Pro or Max subscription to use as a provider key via OAuth. You&apos;ll be redirected to Anthropic&apos;s authorization page to grant access.
				</AlertDescription>
			</Alert>
			<Button type="button" variant="outline" onClick={handleInitiate} disabled={isInitiating} data-testid="anthropic-oauth-connect">
				{isInitiating ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <ExternalLink className="mr-2 h-4 w-4" />}
				Connect with Claude
			</Button>
		</div>
	)
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
