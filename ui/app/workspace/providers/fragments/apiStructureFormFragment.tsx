"use client";

import { Button } from "@/components/ui/button";
import { Form, FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { getErrorMessage, setProviderFormDirtyState, useAppDispatch } from "@/lib/store";
import { useUpdateProviderMutation } from "@/lib/store/apis/providersApi";
import { BaseProvider, ModelProvider } from "@/lib/types/config";
import { formCustomProviderConfigSchema } from "@/lib/types/schemas";
import { cleanPathOverrides } from "@/lib/utils/validation";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect, useMemo } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";
import { AllowedRequestsFields } from "./allowedRequestsFields";

// Type for form data
type FormCustomProviderConfig = z.infer<typeof formCustomProviderConfigSchema>;

// Standalone usage (for provider configuration tabs)
interface Props {
	provider: ModelProvider;
}

// Standalone component for provider configuration tabs
export function ApiStructureFormFragment({ provider }: Props) {
	const hasUpdateProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const dispatch = useAppDispatch();
	const [updateProvider, { isLoading: isUpdatingProvider }] = useUpdateProviderMutation();
	const form = useForm<FormCustomProviderConfig>({
		resolver: zodResolver(formCustomProviderConfigSchema),
		mode: "onChange",
		defaultValues: {
			base_provider_type: provider.custom_provider_config?.base_provider_type ?? "openai",
			is_key_less: provider.custom_provider_config?.is_key_less ?? false,
			allowed_requests: {
				text_completion: provider.custom_provider_config?.allowed_requests?.text_completion ?? true,
				text_completion_stream: provider.custom_provider_config?.allowed_requests?.text_completion_stream ?? true,
				chat_completion: provider.custom_provider_config?.allowed_requests?.chat_completion ?? true,
				chat_completion_stream: provider.custom_provider_config?.allowed_requests?.chat_completion_stream ?? true,
				responses: provider.custom_provider_config?.allowed_requests?.responses ?? true,
				responses_stream: provider.custom_provider_config?.allowed_requests?.responses_stream ?? true,
				embedding: provider.custom_provider_config?.allowed_requests?.embedding ?? true,
				speech: provider.custom_provider_config?.allowed_requests?.speech ?? true,
				speech_stream: provider.custom_provider_config?.allowed_requests?.speech_stream ?? true,
				transcription: provider.custom_provider_config?.allowed_requests?.transcription ?? true,
				transcription_stream: provider.custom_provider_config?.allowed_requests?.transcription_stream ?? true,
				count_tokens: provider.custom_provider_config?.allowed_requests?.count_tokens ?? true,
				list_models: provider.custom_provider_config?.allowed_requests?.list_models ?? true,
			},
			request_path_overrides: provider.custom_provider_config?.request_path_overrides ?? undefined,
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
				base_provider_type: data.base_provider_type as unknown as BaseProvider,
				is_key_less: data.is_key_less ?? false,
				allowed_requests: data.allowed_requests,
				request_path_overrides: cleanPathOverrides(data.request_path_overrides),
			},
		})
			.unwrap()
			.then(() => {
				toast.success("Provider configuration updated successfully");
				form.reset(data);
			})
			.catch((err) => {
				toast.error("Failed to update provider configuration", {
					description: getErrorMessage(err),
				});
			});
	};

	const isKeyLessDisabled = useMemo(
		() => provider.custom_provider_config?.base_provider_type === "bedrock",
		[provider.custom_provider_config?.base_provider_type],
	);

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6 px-6">
				<div className="flex flex-col gap-4">
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
										<SelectItem value="replicate">Replicate</SelectItem>
									</SelectContent>
								</Select>
								<FormDescription>The underlying provider this custom provider will use</FormDescription>
								<FormMessage />
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
										<Switch id="drop-excess-requests" size="md" checked={field.value} onCheckedChange={field.onChange} disabled={!hasUpdateProviderAccess} />
									</div>
								</FormItem>
							)}
						/>
					)}
				</div>

				{/* Allowed Requests Configuration */}
				<AllowedRequestsFields control={form.control} providerType={form.watch("base_provider_type") as BaseProvider} disabled={!hasUpdateProviderAccess} />

				{/* Form Actions */}
				<div className="flex justify-end space-x-2 py-2">
					<Button type="button" variant="outline" onClick={() => form.reset()} disabled={!hasUpdateProviderAccess}>
						Reset
					</Button>
					<TooltipProvider>
						<Tooltip>
							<TooltipTrigger asChild>
								<Button type="submit" disabled={!form.formState.isDirty || !form.formState.isValid || !hasUpdateProviderAccess} isLoading={isUpdatingProvider}>
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
