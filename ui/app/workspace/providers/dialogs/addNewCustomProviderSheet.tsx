import { BaseProviderSelector } from "@/components/ui/baseProviderSelector";
import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { DefaultNetworkConfig } from "@/lib/constants/config";
import { getErrorMessage, useCreateProviderMutation } from "@/lib/store";
import { BaseProvider, GenericAuthType, GenericAuthTypeLabels, GenericAuthTypes, ModelProviderName } from "@/lib/types/config";
import { allowedRequestsSchema, genericAuthConfigSchema } from "@/lib/types/schemas";
import { cleanPathOverrides } from "@/lib/utils/validation";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { AllowedRequestsFields } from "../fragments/allowedRequestsFields";

const formSchema = z
	.object({
		name: z.string().min(1),
		baseFormat: z.string().min(1),
		base_url: z.string().min(1, "Base URL is required").url("Must be a valid URL"),
		allowed_requests: allowedRequestsSchema,
		request_path_overrides: z.record(z.string(), z.string().optional()).optional(),
		is_key_less: z.boolean().optional(),
		does_not_send_done_marker: z.boolean().optional(),
		wait_for_usage: z.boolean().optional(),
		allow_private_network: z.boolean().optional(),
		// generic base type only: how the relay presents its key to the upstream
		generic_auth: genericAuthConfigSchema.optional(),
	})
	.superRefine((data, ctx) => {
		if (data.baseFormat === "generic" && data.is_key_less && data.generic_auth?.type !== "none") {
			ctx.addIssue({ code: "custom", message: "A keyless generic provider must use auth type none", path: ["generic_auth", "type"] });
		}
	});

type FormData = z.infer<typeof formSchema>;

export interface AddCustomProviderSheetContentProps {
	show?: boolean;
	onSave: (id: string) => void;
	onClose: () => void;
}

interface Props extends AddCustomProviderSheetContentProps {
	show: boolean;
}

export function AddCustomProviderSheetContent({ show = true, onClose, onSave }: AddCustomProviderSheetContentProps) {
	const hasProviderCreateAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Create);
	const [addProvider, { isLoading: isAddingProvider }] = useCreateProviderMutation();
	const form = useForm<FormData>({
		resolver: zodResolver(formSchema),
		defaultValues: {
			name: "",
			baseFormat: "",
			base_url: "",
			allowed_requests: {
				text_completion: true,
				text_completion_stream: true,
				chat_completion: true,
				chat_completion_stream: true,
				responses: true,
				responses_stream: true,
				responses_retrieve: true,
				responses_delete: true,
				responses_cancel: true,
				responses_input_items: true,
				embedding: true,
				speech: true,
				speech_stream: true,
				transcription: true,
				transcription_stream: true,
				image_generation: true,
				image_generation_stream: true,
				image_edit: true,
				image_edit_stream: true,
				image_variation: true,
				rerank: true,
				decisions: true,
				ocr: true,
				ocr_stream: true,
				video_generation: true,
				video_edit: true,
				video_retrieve: true,
				video_download: true,
				video_delete: true,
				video_list: true,
				video_remix: true,
				passthrough: true,
				passthrough_stream: true,
				count_tokens: true,
				list_models: true,
				websocket_responses: true,
				realtime: false,
				live: false,
			},
			request_path_overrides: undefined,
			is_key_less: false,
			does_not_send_done_marker: false,
			wait_for_usage: false,
			allow_private_network: false,
			generic_auth: { type: "bearer", header_name: "", query_param: "", value_prefix: "", aws: { service: "", region: "" } },
		},
	});

	useEffect(() => {
		if (show) {
			form.clearErrors();
		}
	}, [show]);

	const onSubmit = (data: FormData) => {
		const isGeneric = data.baseFormat === "generic";
		const auth = data.generic_auth ?? { type: "bearer" as GenericAuthType };
		// A generic relay serves only the two passthrough operations and has no provider paths to
		// override, so those fields are fixed rather than taken from the form.
		const customProviderConfig = isGeneric
			? {
					base_provider_type: "generic" as const,
					allowed_requests: { ...data.allowed_requests, passthrough: true, passthrough_stream: true },
					is_key_less: data.is_key_less ?? false,
					does_not_send_done_marker: false,
					wait_for_usage: false,
					generic_config: {
						auth: {
							type: auth.type,
							...(auth.type === "header" ? { header_name: auth.header_name, value_prefix: auth.value_prefix || undefined } : {}),
							...(auth.type === "query" ? { query_param: auth.query_param } : {}),
							...(auth.type === "aws_sigv4" ? { aws: { service: auth.aws?.service ?? "", region: auth.aws?.region || undefined } } : {}),
						},
					},
				}
			: {
					base_provider_type: data.baseFormat as BaseProvider,
					allowed_requests: data.allowed_requests,
					request_path_overrides: cleanPathOverrides(data.request_path_overrides),
					is_key_less: data.is_key_less ?? false,
					does_not_send_done_marker: data.does_not_send_done_marker ?? false,
					wait_for_usage: data.wait_for_usage ?? false,
				};
		const payload = {
			provider: data.name as ModelProviderName,
			custom_provider_config: customProviderConfig,
			network_config: {
				base_url: data.base_url,
				allow_private_network: data.allow_private_network ?? false,
				default_request_timeout_in_seconds: DefaultNetworkConfig.default_request_timeout_in_seconds,
				max_retries: 0,
				retry_backoff_initial: 500,
				retry_backoff_max: 5000,
			},
		};

		addProvider(payload)
			.unwrap()
			.then((provider) => {
				onSave(provider.name);
				form.reset();
			})
			.catch((err) => {
				toast.error("Failed to add provider", {
					description: getErrorMessage(err),
				});
			});
	};

	const baseFormat = form.watch("baseFormat") as BaseProvider;
	const isGeneric = baseFormat === "generic";
	const genericAuthType = form.watch("generic_auth.type");
	const isKeyLessDisabled = baseFormat === "bedrock";
	// Only the OpenAI stream loops read this flag; every other base format ignores it.
	const isDoneMarkerToggleDisabled = baseFormat !== "openai";
	// wait_for_usage only has meaning once the stream ends on finish_reason, so the
	// toggle is nested under does_not_send_done_marker rather than offered on its own.
	const doesNotSendDoneMarker = form.watch("does_not_send_done_marker");

	useEffect(() => {
		if (isDoneMarkerToggleDisabled) {
			form.setValue("does_not_send_done_marker", false);
		}
	}, [isDoneMarkerToggleDisabled, form]);

	// A generic relay has no typed operations: clear the grid so the stored allowed_requests
	// carries only the passthrough pair, and keep is_key_less in step with auth type none.
	useEffect(() => {
		if (!isGeneric) return;
		const current = form.getValues("allowed_requests");
		const cleared = Object.fromEntries(Object.keys(current).map((k) => [k, false])) as typeof current;
		form.setValue("allowed_requests", { ...cleared, passthrough: true, passthrough_stream: true });
	}, [isGeneric, form]);

	useEffect(() => {
		if (isGeneric && genericAuthType === "none") {
			form.setValue("is_key_less", true);
		}
	}, [isGeneric, genericAuthType, form]);

	// Clear the nested flag whenever its parent goes away: the provider update replaces
	// custom_provider_config wholesale, so a stale true would otherwise be persisted.
	useEffect(() => {
		if (isDoneMarkerToggleDisabled || !doesNotSendDoneMarker) {
			form.setValue("wait_for_usage", false);
		}
	}, [isDoneMarkerToggleDisabled, doesNotSendDoneMarker, form]);

	return (
		<>
			<SheetHeader className="flex shrink-0 flex-col items-start py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10 px-4 md:px-8">
				<SheetTitle>Add Custom Provider</SheetTitle>
				<SheetDescription>Enter the details of your custom provider.</SheetDescription>
			</SheetHeader>
			<Form {...form}>
				<form onSubmit={form.handleSubmit(onSubmit)} className="flex flex-col">
					<div className="min-h-0 flex-1 space-y-4 px-4 pb-4 md:px-8">
						<FormField
							control={form.control}
							name="name"
							render={({ field }) => (
								<FormItem className="flex flex-col gap-3">
									<FormLabel className="text-right">Name</FormLabel>
									<div className="col-span-3">
										<FormControl>
											<Input placeholder="Name" data-testid="custom-provider-name" disabled={!hasProviderCreateAccess} {...field} />
										</FormControl>
										<FormMessage />
									</div>
								</FormItem>
							)}
						/>
						<FormField
							control={form.control}
							name="baseFormat"
							render={({ field }) => (
								<FormItem className="flex flex-col gap-3">
									<FormLabel>Base Format</FormLabel>
									<div>
										<FormControl>
											<BaseProviderSelector
												data-testid="base-provider-select"
												value={field.value}
												onChange={field.onChange}
												disabled={!hasProviderCreateAccess}
											/>
										</FormControl>
										<FormMessage />
									</div>
								</FormItem>
							)}
						/>
						<FormField
							control={form.control}
							name="base_url"
							render={({ field }) => (
								<FormItem className="flex flex-col gap-3">
									<FormLabel>Base URL</FormLabel>
									<div>
										<FormControl>
											<Input
												placeholder={"https://api.your-provider.com"}
												data-testid="base-url-input"
												disabled={!hasProviderCreateAccess}
												{...field}
												value={field.value || ""}
											/>
										</FormControl>
										<FormMessage />
									</div>
								</FormItem>
							)}
						/>
						<FormField
							control={form.control}
							name="allow_private_network"
							render={({ field }) => (
								<FormItem>
									<div className="bg-muted/50 flex items-center justify-between space-x-2 rounded-sm border p-3">
										<div className="space-y-0.5">
											<label htmlFor="allow-private-network" className="text-sm font-medium">
												Allow Private Network
											</label>
											<p className="text-muted-foreground text-sm">
												Allow connecting to private network IPs (e.g. 192.168.x.x, 10.x.x.x). Link-local addresses remain blocked.
											</p>
										</div>
										<Switch
											id="allow-private-network"
											size="md"
											checked={field.value}
											onCheckedChange={field.onChange}
											disabled={!hasProviderCreateAccess}
											data-testid="custom-provider-allow-private-network-switch"
										/>
									</div>
								</FormItem>
							)}
						/>
						{isGeneric && (
							<div className="space-y-4 rounded-sm border p-3" data-testid="generic-auth-section">
								<div>
									<p className="text-sm font-medium">Upstream authentication</p>
									<p className="text-muted-foreground text-sm">
										How Bifrost presents the selected key to this API. Requests are relayed at{" "}
										<code className="font-mono text-xs">/passthrough/{form.watch("name") || "<name>"}/…</code>
									</p>
								</div>
								<FormField
									control={form.control}
									name="generic_auth.type"
									render={({ field }) => (
										<FormItem className="flex flex-col gap-2">
											<FormLabel>Auth type</FormLabel>
											<Select value={field.value} onValueChange={field.onChange} disabled={!hasProviderCreateAccess}>
												<FormControl>
													<SelectTrigger data-testid="generic-auth-type-select">
														<SelectValue placeholder="Select auth type" />
													</SelectTrigger>
												</FormControl>
												<SelectContent>
													{GenericAuthTypes.map((t) => (
														<SelectItem key={t} value={t} data-testid={`generic-auth-type-option-${t}`}>
															{GenericAuthTypeLabels[t]}
														</SelectItem>
													))}
												</SelectContent>
											</Select>
											<FormMessage />
										</FormItem>
									)}
								/>
								{genericAuthType === "header" && (
									<>
										<FormField
											control={form.control}
											name="generic_auth.header_name"
											render={({ field }) => (
												<FormItem className="flex flex-col gap-2">
													<FormLabel>Header name</FormLabel>
													<FormControl>
														<Input
															placeholder="x-api-key"
															data-testid="generic-auth-header-name-input"
															disabled={!hasProviderCreateAccess}
															{...field}
														/>
													</FormControl>
													<FormMessage />
												</FormItem>
											)}
										/>
										<FormField
											control={form.control}
											name="generic_auth.value_prefix"
											render={({ field }) => (
												<FormItem className="flex flex-col gap-2">
													<FormLabel>Value prefix (optional)</FormLabel>
													<FormControl>
														<Input
															placeholder="e.g. Token "
															data-testid="generic-auth-value-prefix-input"
															disabled={!hasProviderCreateAccess}
															{...field}
														/>
													</FormControl>
													<FormMessage />
												</FormItem>
											)}
										/>
									</>
								)}
								{genericAuthType === "query" && (
									<FormField
										control={form.control}
										name="generic_auth.query_param"
										render={({ field }) => (
											<FormItem className="flex flex-col gap-2">
												<FormLabel>Query parameter</FormLabel>
												<FormControl>
													<Input
														placeholder="api_key"
														data-testid="generic-auth-query-param-input"
														disabled={!hasProviderCreateAccess}
														{...field}
													/>
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
								)}
								{genericAuthType === "aws_sigv4" && (
									<>
										<FormField
											control={form.control}
											name="generic_auth.aws.service"
											render={({ field }) => (
												<FormItem className="flex flex-col gap-2">
													<FormLabel>AWS service</FormLabel>
													<FormControl>
														<Input
															placeholder="textract"
															data-testid="generic-aws-service-input"
															disabled={!hasProviderCreateAccess}
															{...field}
														/>
													</FormControl>
													<FormMessage />
												</FormItem>
											)}
										/>
										<FormField
											control={form.control}
											name="generic_auth.aws.region"
											render={({ field }) => (
												<FormItem className="flex flex-col gap-2">
													<FormLabel>AWS region (optional, falls back to the key's region)</FormLabel>
													<FormControl>
														<Input
															placeholder="us-east-1"
															data-testid="generic-aws-region-input"
															disabled={!hasProviderCreateAccess}
															{...field}
														/>
													</FormControl>
													<FormMessage />
												</FormItem>
											)}
										/>
										<p className="text-muted-foreground text-xs">
											Keys for this provider carry AWS credentials (access key, secret, region or a role ARN) instead of a value.
										</p>
									</>
								)}
							</div>
						)}
						{!isKeyLessDisabled && (
							<FormField
								control={form.control}
								name="is_key_less"
								render={({ field }) => (
									<FormItem>
										<div className="bg-muted/50 flex items-center justify-between space-x-2 rounded-sm border p-3">
											<div className="space-y-0.5">
												<label htmlFor="drop-excess-requests" className="text-sm font-medium">
													Is Keyless?
												</label>
												<p className="text-muted-foreground text-sm">Whether the custom provider requires a key</p>
											</div>
											<Switch
												id="drop-excess-requests"
												size="md"
												checked={field.value}
												onCheckedChange={field.onChange}
												disabled={!hasProviderCreateAccess || (isGeneric && genericAuthType !== "none")}
												data-testid="custom-provider-keyless-switch"
											/>
										</div>
									</FormItem>
								)}
							/>
						)}
						{!isDoneMarkerToggleDisabled && (
							<FormField
								control={form.control}
								name="does_not_send_done_marker"
								render={({ field }) => (
									<FormItem>
										<div className="bg-muted/50 flex items-center justify-between space-x-2 rounded-sm border p-3">
											<div className="space-y-0.5">
												<label htmlFor="does-not-send-done-marker" className="text-sm font-medium">
													Does Not Send [DONE] Marker?
												</label>
												<p className="text-muted-foreground text-sm">
													Whether the provider ends streams on finish_reason without sending a [DONE] marker
												</p>
											</div>
											<Switch
												id="does-not-send-done-marker"
												size="md"
												checked={field.value}
												onCheckedChange={field.onChange}
												disabled={!hasProviderCreateAccess}
												data-testid="custom-provider-does-not-send-done-marker-switch"
											/>
										</div>
									</FormItem>
								)}
							/>
						)}
						{!isDoneMarkerToggleDisabled && doesNotSendDoneMarker && (
							<FormField
								control={form.control}
								name="wait_for_usage"
								render={({ field }) => (
									<FormItem>
										<div className="bg-muted/50 flex items-center justify-between space-x-2 rounded-sm border p-3">
											<div className="space-y-0.5">
												<label htmlFor="wait-for-usage" className="text-sm font-medium">
													Wait For Trailing Usage Chunk?
												</label>
												<p className="text-muted-foreground text-sm">
													Keep reading after finish_reason so the trailing usage chunk is collected. Without this the request records zero
													tokens and zero cost
												</p>
											</div>
											<Switch
												id="wait-for-usage"
												size="md"
												checked={field.value}
												onCheckedChange={field.onChange}
												disabled={!hasProviderCreateAccess}
												data-testid="custom-provider-wait-for-usage-switch"
											/>
										</div>
									</FormItem>
								)}
							/>
						)}
						{/* Allowed Requests Configuration: a generic relay serves only passthrough, so the grid is not offered */}
						{!isGeneric && (
							<AllowedRequestsFields
								control={form.control}
								providerType={form.watch("baseFormat") as BaseProvider}
								disabled={!hasProviderCreateAccess}
							/>
						)}
					</div>
					<div className="bg-card sticky bottom-0 ml-auto flex w-full flex-row gap-2 border-t px-4 py-4 md:px-8">
						<Button type="button" variant="outline" onClick={onClose} className="ml-auto" data-testid="custom-provider-cancel-btn">
							Cancel
						</Button>
						<Button type="submit" isLoading={isAddingProvider} disabled={!hasProviderCreateAccess} data-testid="custom-provider-save-btn">
							Add
						</Button>
					</div>
				</form>
			</Form>
		</>
	);
}

export default function AddCustomProviderSheet(props: Props) {
	return (
		<Sheet open={props.show} onOpenChange={(open) => !open && props.onClose()}>
			<SheetContent data-testid="custom-provider-sheet" className="p-0 pt-4">
				<AddCustomProviderSheetContent {...props} />
			</SheetContent>
		</Sheet>
	);
}