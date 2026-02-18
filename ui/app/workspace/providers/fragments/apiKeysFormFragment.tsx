"use client";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { EnvVarInput } from "@/components/ui/envVarInput";
import { FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { ModelMultiselect } from "@/components/ui/modelMultiselect";
import { TagInput } from "@/components/ui/tagInput";
import { Separator } from "@/components/ui/separator";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { isRedacted } from "@/lib/utils/validation";
import { Info, Plus, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { Control, UseFormReturn } from "react-hook-form";

// Providers that support batch APIs
const BATCH_SUPPORTED_PROVIDERS = ["openai", "bedrock", "anthropic", "gemini"];

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
	const isSAPAICore = providerName === "sapaicore";
	const supportsBatchAPI = BATCH_SUPPORTED_PROVIDERS.includes(providerName);

	// Auth type state for Azure: 'api_key' or 'entra_id'
	const [azureAuthType, setAzureAuthType] = useState<'api_key' | 'entra_id'>('api_key')

	// Auth type state for Bedrock: 'iam_role' or 'explicit'
	const [bedrockAuthType, setBedrockAuthType] = useState<'iam_role' | 'explicit'>('iam_role')

	// Detect auth type from existing form values when editing
	useEffect(() => {
		if (form.formState.isDirty) return
		if (isAzure) {
			const clientId = form.getValues('key.azure_key_config.client_id')?.value
			const clientSecret = form.getValues('key.azure_key_config.client_secret')?.value
			const tenantId = form.getValues('key.azure_key_config.tenant_id')?.value
			if (clientId || clientSecret || tenantId) {
				setAzureAuthType('entra_id')
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
		{/* Hide API Key field for Azure when using Entra ID, for Bedrock when using IAM Role, and for SAP AI Core (uses OAuth2) */}
		{!(isAzure && azureAuthType === 'entra_id') && !(isBedrock && bedrockAuthType === 'iam_role') && !isSAPAICore && (
				<FormField
					control={control}
					name={`key.value`}
					render={({ field }) => (
						<FormItem>
							<FormLabel>API Key {isVertex ? "(Supported only for gemini and fine-tuned models)" : ""}</FormLabel>
							<FormControl>
								<EnvVarInput placeholder="API Key or env.MY_KEY" type="text" {...field} />
							</FormControl>
							<FormMessage />
						</FormItem>
					)}
				/>
			)}
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
							<ModelMultiselect provider={providerName} value={field.value || []} onChange={field.onChange} />
						</FormControl>
						<FormMessage />
					</FormItem>
				)}
			/>
			{supportsBatchAPI && !isBedrock && !isAzure && <BatchAPIFormField control={control} form={form} />}
			{isAzure && (
				<div className="space-y-4">
					<Separator className="my-6" />
					<div className="space-y-2">
						<FormLabel>Authentication Method</FormLabel>
						<Tabs value={azureAuthType} onValueChange={(v) => {
							setAzureAuthType(v as 'api_key' | 'entra_id')
							if (v === 'entra_id') {
								// Clear API key when switching to Entra ID
								form.setValue('key.value', undefined)
							} else {
								// Clear Entra ID fields when switching to API Key
								form.setValue('key.azure_key_config.client_id', undefined)
								form.setValue('key.azure_key_config.client_secret', undefined)
								form.setValue('key.azure_key_config.tenant_id', undefined)
								form.setValue('key.azure_key_config.scopes', undefined)
							}
						}}>
							<TabsList className="grid w-full grid-cols-2">
								<TabsTrigger value="api_key">API Key</TabsTrigger>
								<TabsTrigger value="entra_id">Entra ID (Service Principal)</TabsTrigger>
							</TabsList>
						</Tabs>
					</div>

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

					{azureAuthType === 'entra_id' && (
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
														<p>Optional OAuth scopes for token requests. By default we use
														https://cognitiveservices.azure.com/.default — add additional scopes here if your setup requires extra permissions.</p>
													</TooltipContent>
												</Tooltip>
											</TooltipProvider>
										</div>
										<FormControl>
											<TagInput
												placeholder="Add scope (Enter or comma)"
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
		{isSAPAICore && (
			<div className="space-y-4">
				<Separator className="my-6" />
				<Alert variant="default" className="-z-10">
					<Info className="mt-0.5 h-4 w-4 flex-shrink-0 text-blue-600" />
					<AlertTitle>SAP AI Core Authentication</AlertTitle>
					<AlertDescription>
						SAP AI Core uses OAuth2 client credentials authentication. Deployment IDs are automatically resolved from model names.
					</AlertDescription>
				</Alert>
				<FormField
					control={control}
					name={`key.sapaicore_key_config.client_id`}
					render={({ field }) => (
						<FormItem>
							<FormLabel>Client ID (Required)</FormLabel>
							<FormControl>
								<EnvVarInput placeholder="your-client-id or env.SAP_AI_CORE_CLIENT_ID" {...field} />
							</FormControl>
							<FormMessage />
						</FormItem>
					)}
				/>
				<FormField
					control={control}
					name={`key.sapaicore_key_config.client_secret`}
					render={({ field }) => (
						<FormItem>
							<FormLabel>Client Secret (Required)</FormLabel>
							<FormControl>
								<EnvVarInput placeholder="your-client-secret or env.SAP_AI_CORE_CLIENT_SECRET" {...field} />
							</FormControl>
							<FormMessage />
						</FormItem>
					)}
				/>
				<FormField
					control={control}
					name={`key.sapaicore_key_config.auth_url`}
					render={({ field }) => (
						<FormItem>
							<FormLabel>Auth URL (Required)</FormLabel>
							<FormControl>
								<EnvVarInput placeholder="https://your-tenant.authentication.region.hana.ondemand.com or env.SAP_AI_CORE_AUTH_URL" {...field} />
							</FormControl>
							<FormDescription>OAuth2 token endpoint URL</FormDescription>
							<FormMessage />
						</FormItem>
					)}
				/>
				<FormField
					control={control}
					name={`key.sapaicore_key_config.base_url`}
					render={({ field }) => (
						<FormItem>
							<FormLabel>Base URL (Required)</FormLabel>
							<FormControl>
								<EnvVarInput placeholder="https://api.ai.your-region.aws.ml.hana.ondemand.com or env.SAP_AI_CORE_BASE_URL" {...field} />
							</FormControl>
							<FormDescription>SAP AI Core API base URL</FormDescription>
							<FormMessage />
						</FormItem>
					)}
				/>
				<FormField
					control={control}
					name={`key.sapaicore_key_config.resource_group`}
					render={({ field }) => (
						<FormItem>
							<FormLabel>Resource Group (Required)</FormLabel>
							<FormControl>
								<EnvVarInput placeholder="default or env.SAP_AI_CORE_RESOURCE_GROUP" {...field} />
							</FormControl>
							<FormDescription>SAP AI Core resource group name</FormDescription>
							<FormMessage />
						</FormItem>
					)}
				/>
				<FormField
					control={control}
					name={`key.sapaicore_key_config.deployments`}
					render={({ field }) => (
						<FormItem>
							<FormLabel>Deployments (Optional)</FormLabel>
							<FormDescription>JSON object mapping model names to deployment IDs. If not provided, deployment IDs are auto-resolved.</FormDescription>
							<FormControl>
								<Textarea
									placeholder='{"gpt-4o": "d123456", "anthropic--claude-3-5-sonnet": "d789012"}'
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
		{isBedrock && (
				<div className="space-y-4">
					<Separator className="my-6" />
					<div className="space-y-2">
						<FormLabel>Authentication Method</FormLabel>
						<Tabs value={bedrockAuthType} onValueChange={(v) => {
							setBedrockAuthType(v as 'iam_role' | 'explicit')
							if (v === 'iam_role') {
								// Clear explicit credentials when switching to IAM Role
								form.setValue('key.bedrock_key_config.access_key', undefined)
								form.setValue('key.bedrock_key_config.secret_key', undefined)
								form.setValue('key.bedrock_key_config.session_token', undefined)
							}
						}}>
							<TabsList className="grid w-full grid-cols-2">
								<TabsTrigger value="iam_role">IAM Role (Inherited)</TabsTrigger>
								<TabsTrigger value="explicit">Explicit Credentials</TabsTrigger>
							</TabsList>
						</Tabs>
						{bedrockAuthType === 'iam_role' && (
							<p className="text-muted-foreground text-sm">
								Uses IAM roles attached to your environment (EC2, Lambda, ECS, EKS).
							</p>
						)}
					</div>

					{bedrockAuthType === 'explicit' && (
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
