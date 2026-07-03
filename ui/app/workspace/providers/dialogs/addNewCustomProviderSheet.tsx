import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { DefaultNetworkConfig } from "@/lib/constants/config";
import { getErrorMessage, useCreateProviderMutation } from "@/lib/store";
import { BaseProvider, ModelProviderName } from "@/lib/types/config";
import { allowedRequestsSchema } from "@/lib/types/schemas";
import { cleanPathOverrides } from "@/lib/utils/validation";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";
import { AllowedRequestsFields } from "../fragments/allowedRequestsFields";

// Base provider types whose provider implementation never reads NetworkConfig.BaseURL
// (host/routing is derived from per-key config instead) - Base URL is not a meaningful
// input for these and is hidden/optional in the form.
const BASE_URL_NOT_USED = ["vertex"];

const formSchema = z
	.object({
		name: z.string().min(1),
		baseFormat: z.string().min(1),
		base_url: z.string().url("Must be a valid URL").optional().or(z.literal("")),
		allowed_requests: allowedRequestsSchema,
		request_path_overrides: z.record(z.string(), z.string().optional()).optional(),
		is_key_less: z.boolean().optional(),
		allow_private_network: z.boolean().optional(),
	})
	.superRefine((data, ctx) => {
		if (!BASE_URL_NOT_USED.includes(data.baseFormat) && !data.base_url) {
			ctx.addIssue({
				code: "custom",
				message: "Base URL is required",
				path: ["base_url"],
			});
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
				ocr: true,
				ocr_stream: true,
				video_generation: true,
				video_retrieve: true,
				video_download: true,
				video_delete: true,
				video_list: true,
				video_remix: true,
				count_tokens: true,
				list_models: true,
				websocket_responses: true,
				realtime: false,
				batch_create: true,
				batch_list: true,
				batch_retrieve: true,
				batch_cancel: true,
				batch_results: true,
				file_upload: true,
				file_list: true,
				file_retrieve: true,
				file_delete: true,
				file_content: true,
				batch_delete: true,
				cached_content_create: true,
				cached_content_list: true,
				cached_content_retrieve: true,
				cached_content_update: true,
				cached_content_delete: true,
				passthrough: true,
				passthrough_stream: true,
			},
			request_path_overrides: undefined,
			is_key_less: false,
			allow_private_network: false,
		},
	});

	useEffect(() => {
		if (show) {
			form.clearErrors();
		}
	}, [show]);

	const onSubmit = (data: FormData) => {
		const payload = {
			provider: data.name as ModelProviderName,
			custom_provider_config: {
				base_provider_type: data.baseFormat as BaseProvider,
				allowed_requests: data.allowed_requests,
				request_path_overrides: cleanPathOverrides(data.request_path_overrides),
				is_key_less: data.is_key_less ?? false,
			},
			network_config: {
				base_url: BASE_URL_NOT_USED.includes(data.baseFormat) ? undefined : data.base_url,
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
	const isKeyLessDisabled = baseFormat === "bedrock" || baseFormat === "vertex";

	// Clear any stale is_key_less value left over from a previously-selected base
	// format when switching to one that doesn't allow keyless (Bedrock/Vertex) -
	// otherwise a true value set earlier can survive the switch and get submitted,
	// causing a server-side validation error the hidden toggle gives no indication of.
	useEffect(() => {
		if (isKeyLessDisabled && form.getValues("is_key_less")) {
			form.setValue("is_key_less", false, { shouldDirty: true, shouldValidate: true });
		}
	}, [isKeyLessDisabled, form]);

	return (
		<>
			<SheetHeader className="flex shrink-0 flex-col items-start px-8 py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10">
				<SheetTitle>Add Custom Provider</SheetTitle>
				<SheetDescription>Enter the details of your custom provider.</SheetDescription>
			</SheetHeader>
			<Form {...form}>
				<form onSubmit={form.handleSubmit(onSubmit)} className="flex flex-col">
					<div className="min-h-0 flex-1 space-y-4 px-8 pb-4">
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
											<Select onValueChange={field.onChange} value={field.value} disabled={!hasProviderCreateAccess}>
												<SelectTrigger className="w-full" data-testid="base-provider-select">
													<SelectValue placeholder="Select base format" />
												</SelectTrigger>
												<SelectContent>
													<SelectItem value="openai">OpenAI</SelectItem>
													<SelectItem value="anthropic">Anthropic</SelectItem>
													<SelectItem value="gemini">Gemini</SelectItem>
													<SelectItem value="cohere">Cohere</SelectItem>
													<SelectItem value="bedrock">AWS Bedrock</SelectItem>
													<SelectItem value="vertex">Vertex AI</SelectItem>
													<SelectItem value="replicate">Replicate</SelectItem>
												</SelectContent>
											</Select>
										</FormControl>
										<FormMessage />
									</div>
								</FormItem>
							)}
						/>
						{!BASE_URL_NOT_USED.includes(baseFormat) && (
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
						)}
						<FormField
							control={form.control}
							name="allow_private_network"
							render={({ field }) => (
								<FormItem>
									<div className="flex items-center justify-between space-x-2 rounded-lg border p-3">
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
						{!isKeyLessDisabled && (
							<FormField
								control={form.control}
								name="is_key_less"
								render={({ field }) => (
									<FormItem>
										<div className="flex items-center justify-between space-x-2 rounded-lg border p-3">
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
												disabled={!hasProviderCreateAccess}
												data-testid="custom-provider-keyless-switch"
											/>
										</div>
									</FormItem>
								)}
							/>
						)}
						{/* Allowed Requests Configuration */}
						<AllowedRequestsFields
							control={form.control}
							providerType={form.watch("baseFormat") as BaseProvider}
							disabled={!hasProviderCreateAccess}
						/>
					</div>
					<div className="bg-card sticky bottom-0 ml-auto flex w-full flex-row gap-2 border-t px-8 py-4">
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