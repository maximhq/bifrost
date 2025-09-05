"use client";

import { Button } from "@/components/ui/button";
import { Form, FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { getErrorMessage } from "@/lib/store";
import { useUpdateProviderMutation } from "@/lib/store/apis/providersApi";
import { KnownProvider, ModelProvider } from "@/lib/types/config";
import { formCustomProviderConfigSchema } from "@/lib/types/schemas";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";

// Type for form data
type FormCustomProviderConfig = z.infer<typeof formCustomProviderConfigSchema>;

// Standalone usage (for provider configuration tabs)
interface Props {
	provider: ModelProvider;
	showRestartAlert?: boolean;
}

// Standalone component for provider configuration tabs
export function ApiStructureFormFragment({ provider, showRestartAlert }: Props) {
	const [updateProvider, { isLoading: isUpdatingProvider }] = useUpdateProviderMutation();
	const form = useForm<FormCustomProviderConfig>({
		resolver: zodResolver(formCustomProviderConfigSchema),
		mode: "onChange",
		defaultValues: {
			base_provider_type: provider.custom_provider_config?.base_provider_type ?? "openai",
			allowed_requests: {
				text_completion: provider.custom_provider_config?.allowed_requests?.text_completion ?? true,
				chat_completion: provider.custom_provider_config?.allowed_requests?.chat_completion ?? true,
				chat_completion_stream: provider.custom_provider_config?.allowed_requests?.chat_completion_stream ?? true,
				embedding: provider.custom_provider_config?.allowed_requests?.embedding ?? true,
				speech: provider.custom_provider_config?.allowed_requests?.speech ?? true,
				speech_stream: provider.custom_provider_config?.allowed_requests?.speech_stream ?? true,
				transcription: provider.custom_provider_config?.allowed_requests?.transcription ?? true,
				transcription_stream: provider.custom_provider_config?.allowed_requests?.transcription_stream ?? true,
			},
		},
	});

	useEffect(() => {
		form.reset(provider.custom_provider_config);
	}, [form, provider.name, provider.custom_provider_config]);

	const onSubmit = (data: FormCustomProviderConfig) => {
		// Create updated provider configuration
		updateProvider({
			...provider,
			custom_provider_config: {
				base_provider_type: data.base_provider_type as unknown as KnownProvider,
				allowed_requests: data.allowed_requests,
			},
		})
			.unwrap()
			.catch((err) => {
				toast.error("Failed to update provider configuration", {
					description: getErrorMessage(err),
				});
			});
	};

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6 px-6">
				{showRestartAlert && (
					<div className="rounded-md bg-yellow-50 p-4 text-sm text-yellow-800">
						⚠️ The settings below require a Bifrost service restart to take effect. Current connections will continue with existing settings
						until restart.
					</div>
				)}

				<div className="space-y-4">
					<FormField
						control={form.control}
						name="base_provider_type"
						render={({ field }) => (
							<FormItem>
								<FormLabel>Base Provider Type</FormLabel>
								<Select onValueChange={field.onChange} defaultValue={field.value}>
									<FormControl>
										<SelectTrigger disabled={true}>
											<SelectValue placeholder="Select base provider" />
										</SelectTrigger>
									</FormControl>
									<SelectContent>
										<SelectItem value="openai">OpenAI</SelectItem>
										<SelectItem value="anthropic">Anthropic</SelectItem>
										<SelectItem value="bedrock">AWS Bedrock</SelectItem>
										<SelectItem value="cohere">Cohere</SelectItem>
										<SelectItem value="gemini">Gemini</SelectItem>
									</SelectContent>
								</Select>
								<FormDescription>The underlying provider this custom provider will use</FormDescription>
								<FormMessage />
							</FormItem>
						)}
					/>
				</div>

				{/* Allowed Requests Configuration */}
				<div className="space-y-4">
					<div>
						<div className="text-sm font-medium">Allowed Request Types</div>
						<p className="text-muted-foreground text-xs">Select which request types this custom provider can handle</p>
					</div>

					<div className="grid grid-cols-2 gap-4">
						<div className="space-y-3">
							<FormField
								control={form.control}
								name="allowed_requests.text_completion"
								render={({ field }) => (
									<FormItem className="flex flex-row items-center justify-between rounded-lg border p-3 shadow-sm">
										<div className="space-y-0.5">
											<FormLabel>Text Completion</FormLabel>
										</div>
										<FormControl>
											<Switch checked={field.value} onCheckedChange={field.onChange} />
										</FormControl>
									</FormItem>
								)}
							/>
							<FormField
								control={form.control}
								name="allowed_requests.chat_completion"
								render={({ field }) => (
									<FormItem className="flex flex-row items-center justify-between rounded-lg border p-3 shadow-sm">
										<div className="space-y-0.5">
											<FormLabel>Chat Completion</FormLabel>
										</div>
										<FormControl>
											<Switch checked={field.value} onCheckedChange={field.onChange} />
										</FormControl>
									</FormItem>
								)}
							/>
							<FormField
								control={form.control}
								name="allowed_requests.chat_completion_stream"
								render={({ field }) => (
									<FormItem className="flex flex-row items-center justify-between rounded-lg border p-3 shadow-sm">
										<div className="space-y-0.5">
											<FormLabel>Chat Completion Stream</FormLabel>
										</div>
										<FormControl>
											<Switch checked={field.value} onCheckedChange={field.onChange} />
										</FormControl>
									</FormItem>
								)}
							/>
							<FormField
								control={form.control}
								name="allowed_requests.embedding"
								render={({ field }) => (
									<FormItem className="flex flex-row items-center justify-between rounded-lg border p-3 shadow-sm">
										<div className="space-y-0.5">
											<FormLabel>Embedding</FormLabel>
										</div>
										<FormControl>
											<Switch checked={field.value} onCheckedChange={field.onChange} />
										</FormControl>
									</FormItem>
								)}
							/>
						</div>
						<div className="space-y-3">
							<FormField
								control={form.control}
								name="allowed_requests.speech"
								render={({ field }) => (
									<FormItem className="flex flex-row items-center justify-between rounded-lg border p-3 shadow-sm">
										<div className="space-y-0.5">
											<FormLabel>Speech</FormLabel>
										</div>
										<FormControl>
											<Switch checked={field.value} onCheckedChange={field.onChange} />
										</FormControl>
									</FormItem>
								)}
							/>
							<FormField
								control={form.control}
								name="allowed_requests.speech_stream"
								render={({ field }) => (
									<FormItem className="flex flex-row items-center justify-between rounded-lg border p-3 shadow-sm">
										<div className="space-y-0.5">
											<FormLabel>Speech Stream</FormLabel>
										</div>
										<FormControl>
											<Switch checked={field.value} onCheckedChange={field.onChange} />
										</FormControl>
									</FormItem>
								)}
							/>
							<FormField
								control={form.control}
								name="allowed_requests.transcription"
								render={({ field }) => (
									<FormItem className="flex flex-row items-center justify-between rounded-lg border p-3 shadow-sm">
										<div className="space-y-0.5">
											<FormLabel>Transcription</FormLabel>
										</div>
										<FormControl>
											<Switch checked={field.value} onCheckedChange={field.onChange} />
										</FormControl>
									</FormItem>
								)}
							/>
							<FormField
								control={form.control}
								name="allowed_requests.transcription_stream"
								render={({ field }) => (
									<FormItem className="flex flex-row items-center justify-between rounded-lg border p-3 shadow-sm">
										<div className="space-y-0.5">
											<FormLabel>Transcription Stream</FormLabel>
										</div>
										<FormControl>
											<Switch checked={field.value} onCheckedChange={field.onChange} />
										</FormControl>
									</FormItem>
								)}
							/>
						</div>
					</div>
				</div>

				{/* Form Actions */}
				<div className="flex justify-end space-x-2 py-2">
					<Button type="button" variant="outline" onClick={() => form.reset()}>
						Reset
					</Button>
					<TooltipProvider>
						<Tooltip>
							<TooltipTrigger asChild>
								<Button type="submit" disabled={!form.formState.isDirty || !form.formState.isValid} isLoading={isUpdatingProvider}>
									Save API Structure Configuration
								</Button>
							</TooltipTrigger>
							{!form.formState.isValid && (
								<TooltipContent>
									<p>{form.formState.errors.root?.message || "Please fix validation errors"}</p>
								</TooltipContent>
							)}
						</Tooltip>
					</TooltipProvider>
				</div>
			</form>
		</Form>
	);
}
