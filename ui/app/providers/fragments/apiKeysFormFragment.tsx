"use client";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { TagInput } from "@/components/ui/tagInput";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { isRedacted } from "@/lib/utils/validation";
import { Info } from "lucide-react";
import { Control } from "react-hook-form";

interface Props {
	control: Control<any>;
	providerName: string;
}

export function ApiKeyFormFragment({ control, providerName }: Props) {
	const isBedrock = providerName === "bedrock";
	const isVertex = providerName === "vertex";
	const isAzure = providerName === "azure";

	return (
		<div data-tab="api-keys" className="max-h-[60vh] space-y-4 overflow-x-hidden overflow-y-auto">
			{isBedrock && (
				<Alert variant="default">
					<Info className="mt-0.5 h-4 w-4 flex-shrink-0 text-blue-600" />
					<AlertTitle>IAM Role Authentication</AlertTitle>
					<AlertDescription>
						Leave both Access Key and Secret Key empty to use IAM roles attached to your environment (EC2, Lambda, ECS, EKS). This is the
						recommended approach for production deployments.
					</AlertDescription>
				</Alert>
			)}
			<div className="flex gap-4">
				{!isVertex && !isBedrock && (
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
				<div>
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
										className="w-20"
										{...field}
										onChange={(e) => {
											const v = e.target.value.trim();
											field.onChange(v === "" ? undefined : Number(v));
										}}
										type="string"
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
							<TagInput
								placeholder="e.g. gpt-4, gpt-3.5-turbo. Leave blank for all models."
								value={field.value || []}
								onValueChange={field.onChange}
							/>
						</FormControl>
						<FormMessage />
					</FormItem>
				)}
			/>
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
													const parsed = JSON.parse(value);
													if (typeof parsed === "object" && parsed !== null) {
														field.onChange(parsed);
													}
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
					<FormField
						control={control}
						name={`key.bedrock_key_config.arn`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>ARN</FormLabel>
								<FormControl>
									<Input placeholder="ARN" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name={`key.bedrock_key_config.access_key`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Access Key</FormLabel>
								<FormControl>
									<Input placeholder="your-aws-access-key or env.AWS_ACCESS_KEY_ID" {...field} />
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
									<Input placeholder="your-aws-secret-key or env.AWS_SECRET_ACCESS_KEY" {...field} />
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
									<Input placeholder="your-aws-session-token or env.AWS_SESSION_TOKEN" {...field} />
								</FormControl>
								<FormMessage />
							</FormItem>
						)}
					/>
					<FormField
						control={control}
						name={`key.bedrock_key_config.region`}
						render={({ field }) => (
							<FormItem>
								<FormLabel>Region (Required)</FormLabel>
								<FormControl>
									<Input placeholder="us-east-1 or env.AWS_REGION" {...field} />
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
		</div>
	);
}
