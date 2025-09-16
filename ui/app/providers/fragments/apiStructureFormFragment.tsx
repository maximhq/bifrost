"use client";

import { Button } from "@/components/ui/button";
import { Form, FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { AllowedRequestsFields } from "./allowedRequestsFields";
import { getErrorMessage, setProviderFormDirtyState, useAppDispatch } from "@/lib/store";
import { useUpdateProviderMutation } from "@/lib/store/apis/providersApi";
import { KnownProvider, ModelProvider } from "@/lib/types/config";
import { formCustomProviderConfigSchema } from "@/lib/types/schemas";
import { zodResolver } from "@hookform/resolvers/zod";
import { use, useEffect } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";
import { fi } from "zod/v4/locales";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { AlertTriangle } from "lucide-react";

// Type for form data
type FormCustomProviderConfig = z.infer<typeof formCustomProviderConfigSchema>;

// Standalone usage (for provider configuration tabs)
interface Props {
	provider: ModelProvider;
	showRestartAlert?: boolean;
}

// Standalone component for provider configuration tabs
export function ApiStructureFormFragment({ provider, showRestartAlert }: Props) {
	const dispatch = useAppDispatch();
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
		dispatch(setProviderFormDirtyState(form.formState.isDirty));
	}, [form.formState.isDirty]);

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
			.then(() => {
				toast.success("Provider configuration updated successfully");
			})
			.catch((err) => {
				toast.error("Failed to update provider configuration", {
					description: getErrorMessage(err),
				});
			});
	};

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6 px-6">
				{showRestartAlert && form.formState.isDirty && (
					<Alert>
						<AlertTriangle className="h-4 w-4" />
						<AlertDescription>
							The settings below require a Bifrost service restart to take effect. Current connections will continue with existing settings
							until restart.
						</AlertDescription>
					</Alert>
				)}

				<div className="space-y-4">
					<FormField
						control={form.control}
						name="base_provider_type"
						render={({ field }) => (
							<FormItem>
								<FormLabel>Base Provider Type</FormLabel>
								<Select onValueChange={field.onChange} value={field.value}>
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
				<AllowedRequestsFields control={form.control} />

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
