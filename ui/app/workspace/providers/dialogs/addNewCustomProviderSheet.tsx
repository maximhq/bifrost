import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { getErrorMessage, useCreateProviderMutation } from "@/lib/store";
import { BaseProvider, ModelProviderName } from "@/lib/types/config";
import { allowedRequestsSchema } from "@/lib/types/schemas";
import { cleanPathOverrides } from "@/lib/utils/validation";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";
import { AllowedRequestsFields } from "../fragments/allowedRequestsFields";

const formSchema = z.object({
	name: z.string().min(1),
	baseFormat: z.string().min(1),
	base_url: z.string().min(1, "Base URL is required").url("Must be a valid URL"),
	allowed_requests: allowedRequestsSchema,
	request_path_overrides: z.record(z.string(), z.string().optional()).optional(),
	is_key_less: z.boolean().optional(),
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
				embedding: true,
				speech: true,
				speech_stream: true,
				transcription: true,
				transcription_stream: true,
				image_generation: true,
				image_generation_stream: true,
				count_tokens: true,
				list_models: true,
			},
			request_path_overrides: undefined,
			is_key_less: false,
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
				base_url: data.base_url,
				default_request_timeout_in_seconds: 30,
				max_retries: 0,
				retry_backoff_initial: 500,
				retry_backoff_max: 5000,
			},
			keys: [],
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
	const isKeyLessDisabled = baseFormat === "bedrock";

	return (
		<>
			<SheetHeader className="flex shrink-0 flex-col items-start">
				<SheetTitle>Add Custom Provider</SheetTitle>
				<SheetDescription>Enter the details of your custom provider.</SheetDescription>
			</SheetHeader>
			<Form {...form}>
				<form onSubmit={form.handleSubmit(onSubmit)} className="flex min-h-0 flex-1 flex-col overflow-hidden">
					<div className="custom-scrollbar min-h-0 flex-1 space-y-4 overflow-y-auto">
							<FormField
								control={form.control}
								name="name"
								render={({ field }) => (
									<FormItem className="flex flex-col gap-3">
										<FormLabel className="text-right">Name</FormLabel>
										<div className="col-span-3">
											<FormControl>
												<Input placeholder="Name" data-testid="custom-provider-name" {...field} />
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
												<Select onValueChange={field.onChange} defaultValue={field.value}>
													<SelectTrigger className="w-full" data-testid="base-provider-select">
														<SelectValue placeholder="Select base format" />
													</SelectTrigger>
													<SelectContent>
														<SelectItem value="openai">OpenAI</SelectItem>
														<SelectItem value="anthropic">Anthropic</SelectItem>
														<SelectItem value="gemini">Gemini</SelectItem>
														<SelectItem value="cohere">Cohere</SelectItem>
														<SelectItem value="bedrock">AWS Bedrock</SelectItem>
														<SelectItem value="replicate">Replicate</SelectItem>
													</SelectContent>
												</Select>
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
											<Input placeholder={"https://api.your-provider.com"} data-testid="base-url-input" {...field} value={field.value || ""} />
										</FormControl>
										<FormMessage />
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
												<Switch id="drop-excess-requests" size="md" checked={field.value} onCheckedChange={field.onChange} />
											</div>
										</FormItem>
									)}
								/>
							)}
							{/* Allowed Requests Configuration */}
							<AllowedRequestsFields control={form.control} providerType={form.watch("baseFormat") as BaseProvider} />
					</div>
					<SheetFooter className="mt-4 flex shrink-0 flex-row gap-2 pt-4">
							<div className="ml-auto flex flex-row gap-2">
								<Button type="button" variant="outline" onClick={onClose} data-testid="custom-provider-cancel-btn">
									Cancel
								</Button>
								<Button type="submit" isLoading={isAddingProvider} data-testid="custom-provider-save-btn">
									Add
								</Button>
							</div>
					</SheetFooter>
				</form>
			</Form>
		</>
	);
}

export default function AddCustomProviderSheet(props: Props) {
	return (
		<Sheet open={props.show} onOpenChange={(open) => !open && props.onClose()}>
			<SheetContent className="custom-scrollbar dark:bg-card flex flex-col bg-white p-8 sm:max-w-lg" data-testid="custom-provider-sheet">
				<AddCustomProviderSheetContent {...props} />
			</SheetContent>
		</Sheet>
	);
}
