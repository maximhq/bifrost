import { BaseProviderSelector } from "@/components/ui/baseProviderSelector";
import { Button } from "@/components/ui/button";
import { Form, FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
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
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { z } from "zod";
import { buildProviderUpdatePayload } from "../views/utils";
import { AllowedRequestsFields } from "./allowedRequestsFields";

// Type for form data
type FormCustomProviderConfig = z.infer<typeof formCustomProviderConfigSchema>;

// Standalone usage (for provider configuration tabs)
interface Props {
	provider: ModelProvider;
}

// Standalone component for provider configuration tabs
export function ApiStructureFormFragment({ provider }: Props) {
	const { t } = useTranslation("models");
	const hasUpdateProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const dispatch = useAppDispatch();
	const [updateProvider, { isLoading: isUpdatingProvider }] = useUpdateProviderMutation();
	const form = useForm<FormCustomProviderConfig>({
		resolver: zodResolver(formCustomProviderConfigSchema),
		mode: "onChange",
		defaultValues: {
			base_provider_type: provider.custom_provider_config?.base_provider_type ?? "openai",
			is_key_less: provider.custom_provider_config?.is_key_less ?? false,
			does_not_send_done_marker: provider.custom_provider_config?.does_not_send_done_marker ?? false,
			wait_for_usage: provider.custom_provider_config?.wait_for_usage ?? false,
			allowed_requests: {
				text_completion: provider.custom_provider_config?.allowed_requests?.text_completion ?? true,
				text_completion_stream: provider.custom_provider_config?.allowed_requests?.text_completion_stream ?? true,
				chat_completion: provider.custom_provider_config?.allowed_requests?.chat_completion ?? true,
				chat_completion_stream: provider.custom_provider_config?.allowed_requests?.chat_completion_stream ?? true,
				responses: provider.custom_provider_config?.allowed_requests?.responses ?? true,
				responses_stream: provider.custom_provider_config?.allowed_requests?.responses_stream ?? true,
				responses_retrieve: provider.custom_provider_config?.allowed_requests?.responses_retrieve ?? false,
				responses_delete: provider.custom_provider_config?.allowed_requests?.responses_delete ?? false,
				responses_cancel: provider.custom_provider_config?.allowed_requests?.responses_cancel ?? false,
				responses_input_items: provider.custom_provider_config?.allowed_requests?.responses_input_items ?? false,
				embedding: provider.custom_provider_config?.allowed_requests?.embedding ?? true,
				speech: provider.custom_provider_config?.allowed_requests?.speech ?? true,
				speech_stream: provider.custom_provider_config?.allowed_requests?.speech_stream ?? true,
				transcription: provider.custom_provider_config?.allowed_requests?.transcription ?? true,
				transcription_stream: provider.custom_provider_config?.allowed_requests?.transcription_stream ?? true,
				count_tokens: provider.custom_provider_config?.allowed_requests?.count_tokens ?? true,
				list_models: provider.custom_provider_config?.allowed_requests?.list_models ?? true,
				ocr: provider.custom_provider_config?.allowed_requests?.ocr ?? true,
				ocr_stream: provider.custom_provider_config?.allowed_requests?.ocr_stream ?? true,
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
		updateProvider(
			buildProviderUpdatePayload(provider, {
				custom_provider_config: {
					base_provider_type: data.base_provider_type as unknown as BaseProvider,
					is_key_less: data.is_key_less ?? false,
					does_not_send_done_marker: data.does_not_send_done_marker ?? false,
					wait_for_usage: data.wait_for_usage ?? false,
					allowed_requests: data.allowed_requests,
					request_path_overrides: cleanPathOverrides(data.request_path_overrides),
				},
			}),
		)
			.unwrap()
			.then(() => {
				toast.success(t("providers.configUpdated"));
				form.reset(data);
			})
			.catch((err) => {
				toast.error(t("providers.failedUpdateConfig"), {
					description: getErrorMessage(err),
				});
			});
	};

	const isKeyLessDisabled = useMemo(
		() => provider.custom_provider_config?.base_provider_type === "bedrock",
		[provider.custom_provider_config?.base_provider_type],
	);

	// Only the OpenAI stream loops read this flag; every other base format ignores it.
	const isDoneMarkerToggleDisabled = useMemo(
		() => provider.custom_provider_config?.base_provider_type !== "openai",
		[provider.custom_provider_config?.base_provider_type],
	);

	// wait_for_usage only has meaning once the stream ends on finish_reason, so the
	// toggle is nested under does_not_send_done_marker rather than offered on its own.
	const doesNotSendDoneMarker = form.watch("does_not_send_done_marker");

	// Clear the nested flag whenever its parent goes away: the update replaces
	// custom_provider_config wholesale, so a stale true would otherwise be persisted.
	useEffect(() => {
		if (isDoneMarkerToggleDisabled || !doesNotSendDoneMarker) {
			form.setValue("wait_for_usage", false);
		}
	}, [isDoneMarkerToggleDisabled, doesNotSendDoneMarker, form]);

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6 px-4 pb-6 md:px-6">
				<div className="flex flex-col gap-4">
					<FormField
						control={form.control}
						name="base_provider_type"
						render={({ field }) => (
							<FormItem>
								<FormLabel>{t("providers.baseProviderType")}</FormLabel>
								<FormControl>
									<BaseProviderSelector placeholder={t("providers.selectBaseProvider")} value={field.value} onChange={field.onChange} disabled />
								</FormControl>
								<FormDescription>{t("providers.baseProviderTypeHelp")}</FormDescription>
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
									<div className="bg-muted/50 flex items-center justify-between space-x-2 rounded-sm border p-3">
										<div className="space-y-0.5">
											<label htmlFor="drop-excess-requests" className="text-sm font-medium">
												{t("providers.isKeyless")}
											</label>
											<p className="text-muted-foreground text-sm">{t("providers.requiresKey")}</p>
										</div>
										<Switch
											id="drop-excess-requests"
											size="md"
											checked={field.value}
											onCheckedChange={field.onChange}
											disabled={!hasUpdateProviderAccess}
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
												{t("providers.doesNotSendDoneMarker")}
											</label>
											<p className="text-muted-foreground text-sm">{t("providers.doesNotSendDoneMarkerHelp")}</p>
										</div>
										<Switch
											id="does-not-send-done-marker"
											size="md"
											checked={field.value}
											onCheckedChange={field.onChange}
											disabled={!hasUpdateProviderAccess}
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
												{t("providers.custom.waitForUsage")}
											</label>
											<p className="text-muted-foreground text-sm">{t("providers.custom.waitForUsageHelp")}</p>
										</div>
										<Switch
											id="wait-for-usage"
											size="md"
											checked={field.value}
											onCheckedChange={field.onChange}
											disabled={!hasUpdateProviderAccess}
										/>
									</div>
								</FormItem>
							)}
						/>
					)}
				</div>

				{/* Allowed Requests Configuration */}
				<AllowedRequestsFields
					control={form.control}
					providerType={form.watch("base_provider_type") as BaseProvider}
					disabled={!hasUpdateProviderAccess}
				/>

				{/* Form Actions */}
				<div className="flex justify-end gap-2 py-2">
					<Button type="button" variant="outline" onClick={() => form.reset()} disabled={!hasUpdateProviderAccess}>
						{t("providers.reset")}
					</Button>
					<TooltipProvider>
						<Tooltip>
							<TooltipTrigger asChild>
								<Button type="submit" disabled={!form.formState.isDirty || !hasUpdateProviderAccess} isLoading={isUpdatingProvider}>
									{t("providers.saveApiStructureConfig")}
								</Button>
							</TooltipTrigger>
							{!form.formState.isValid && (
								<TooltipContent>
									<p>{form.formState.errors.root?.message || t("providers.pleaseFixValidation")}</p>
								</TooltipContent>
							)}
						</Tooltip>
					</TooltipProvider>
				</div>
			</form>
		</Form>
	);
}