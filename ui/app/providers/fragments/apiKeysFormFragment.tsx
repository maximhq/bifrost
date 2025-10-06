"use client";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Separator } from "@/components/ui/separator";
import { Switch } from "@/components/ui/switch";
import { TagInput } from "@/components/ui/tagInput";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { ModelPlaceholders } from "@/lib/constants/config";
import { isRedacted } from "@/lib/utils/validation";
import { Info } from "lucide-react";
import { Control, UseFormReturn } from "react-hook-form";

interface Props {
	control: Control<any>;
	providerName: string;
	form: UseFormReturn<any>;
}

export function ApiKeyFormFragment({ control, providerName, form }: Props) {
	const isBedrock = providerName === "bedrock";
	const isVertex = providerName === "vertex";
	const isAzure = providerName === "azure";
	const modelsPlaceholder = isAzure
		? ModelPlaceholders.azure
		: isBedrock
			? ModelPlaceholders.bedrock
			: isVertex
				? ModelPlaceholders.vertex
				: ModelPlaceholders.openai;
	const isOpenAI = providerName === "openai";

	return (
		<div data-tab="api-keys" className="space-y-4 overflow-hidden">
			{isBedrock && (
				<Alert variant="default" className="-z-10">
					<Info className="mt-0.5 h-4 w-4 flex-shrink-0 text-blue-600" />
					<AlertTitle>Authentication Methods</AlertTitle>
					<AlertDescription>
						You can either use IAM role authentication or API key authentication. Please leave API Key empty when using IAM role
						authentication.
					</AlertDescription>
				</Alert>
			)}
			<div className="flex gap-4">
				{!isVertex && (
					<div className="flex-1">
						<FormField
							control={control}
							name={`key.value`}
							render={({ field }) => (
								<FormItem>
									<FormLabel>API Key</FormLabel>
									<FormControl>
										<Input placeholder="API Key or env.MY_KEY" type="text" {...field} />
									</FormControl>
									<FormMessage />
								</FormItem>
							)}
						/>
					</div>
				)}
				<div className="h-[80px]">
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
										className="w-[220px]"
										value={field.value === undefined || field.value === null ? "" : String(field.value)}
										onChange={(e) => {
											// Clear error while typing
											form.clearErrors("key.weight");
											// Keep as string during typing to allow partial input
											field.onChange(e.target.value === "" ? "" : e.target.value);
										}}
										onBlur={(e) => {
											const v = e.target.value.trim();
											if (v !== "") {
												const num = parseFloat(v);
												if (!isNaN(num)) {
													field.onChange(num);
												} else {
													form.setError("key.weight", { message: "Weight must be a valid number" });
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
			</div>
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
							<TagInput placeholder={modelsPlaceholder} value={field.value || []} onValueChange={field.onChange} />
						</FormControl>
						<FormMessage />
					</FormItem>
				)}
			/>
			{isOpenAI && (
				<div className="space-y-4">
					<FormField
						control={control}
						name={`key.openai_key_config.use_responses_api`}
						render={({ field }) => (
							<FormItem>
								<FormControl>
									<div className="flex items-center justify-between space-x-2 rounded-lg border p-4">
										<div className="space-y-0.5">
											<label htmlFor="enforce-governance" className="text-sm font-medium">
												Use Responses API
											</label>
											<p className="text-muted-foreground text-sm">Use the Responses API instead of the Chat Completion API.</p>
										</div>
										<Switch id="enforce-governance" size="md" checked={field.value} onCheckedChange={field.onChange} />
									</div>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
				</div>
			)}
			{isAzure && (
				<div className="space-y-4">
					<FormField
						control={control}
						name={`key.azure_key_config.endpoint`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Endpoint (Required)</FormLabel>
								<FormControl>
									<Input placeholder="https://your-resource.openai.azure.com or env.AZURE_ENDPOINT" {...field} />
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
									<Input placeholder="2024-02-01 or env.AZURE_API_VERSION" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
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
			{isVertex && (
				<div className="space-y-4">
					<FormField
						control={control}
						name={`key.vertex_key_config.project_id`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Project ID (Required)</FormLabel>
								<FormControl>
									<Input placeholder="your-gcp-project-id or env.VERTEX_PROJECT_ID" {...field} />
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
									<Input placeholder="us-central1 or env.VERTEX_REGION" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name={`key.vertex_key_config.auth_credentials`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Auth Credentials (Required)</FormLabel>
								<FormDescription>Service account JSON object or env.VAR_NAME</FormDescription>
								<FormControl>
									<Textarea
										placeholder='{"type":"service_account","project_id":"your-gcp-project",...} or env.VERTEX_CREDENTIALS'
										value={typeof field.value === "string" ? field.value : JSON.stringify(field.value || {}, null, 2)}
										onChange={(e) => field.onChange(e.target.value)}
										onBlur={(e) => {
											const value = e.target.value.trim();
											if (value.startsWith("env.")) {
												field.onChange(value);
											} else if (value) {
												try {
													try {
														if (value) JSON.parse(value);
													} catch {}
													field.onChange(value);
												} catch {
													// leave as string; validation will catch malformed JSON
												}
											}
											field.onBlur();
										}}
										rows={4}
										className="max-w-full font-mono text-sm wrap-anywhere"
									/>
								</FormControl>
								{isRedacted(typeof field.value === "string" ? field.value : "") && (
									<div className="text-muted-foreground mt-1 flex items-center gap-1 text-xs">
										<Info className="h-3 w-3" />
										<span>Credentials are stored securely. Edit to update.</span>
									</div>
								)}
							</FormItem>
						)}
					/>
				</div>
			)}
			{isBedrock && (
				<div className="space-y-4">
					<Separator className="my-6" />
					<Alert variant="default" className="-z-10">
						<Info className="mt-0.5 h-4 w-4 flex-shrink-0 text-blue-600" />
						<AlertTitle>IAM Role Authentication</AlertTitle>
						<AlertDescription>
							Leave both Access Key and Secret Key empty to use IAM roles attached to your environment (EC2, Lambda, ECS, EKS).
						</AlertDescription>
					</Alert>
					<FormField
						control={control}
						name={`key.bedrock_key_config.access_key`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Access Key</FormLabel>
								<FormControl>
									<Input
										placeholder="your-aws-access-key or env.AWS_ACCESS_KEY_ID"
										value={field.value ?? ""}
										onChange={field.onChange}
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
						name={`key.bedrock_key_config.secret_key`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Secret Key</FormLabel>
								<FormControl>
									<Input
										placeholder="your-aws-secret-key or env.AWS_SECRET_ACCESS_KEY"
										value={field.value ?? ""}
										onChange={field.onChange}
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
						name={`key.bedrock_key_config.session_token`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Session Token (Optional)</FormLabel>
								<FormControl>
									<Input
										placeholder="your-aws-session-token or env.AWS_SESSION_TOKEN"
										value={field.value ?? ""}
										onChange={field.onChange}
										onBlur={field.onBlur}
										name={field.name}
										ref={field.ref}
									/>
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<Separator className="my-6" />
					<FormField
						control={control}
						name={`key.bedrock_key_config.region`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Region (Required)</FormLabel>
								<FormControl>
									<Input
										placeholder="us-east-1 or env.AWS_REGION"
										value={field.value ?? ""}
										onChange={field.onChange}
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
						name={`key.bedrock_key_config.arn`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>ARN</FormLabel>
								<FormControl>
									<Input
										placeholder="arn:aws:bedrock:us-east-1:123:inference-profile or env.AWS_ARN"
										value={field.value ?? ""}
										onChange={field.onChange}
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
				</div>
			)}
		</div>
	);
}
