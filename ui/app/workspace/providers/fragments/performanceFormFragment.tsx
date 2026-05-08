import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { DefaultPerformanceConfig } from "@/lib/constants/config";
import { getErrorMessage, setProviderFormDirtyState, useAppDispatch } from "@/lib/store";
import { useUpdateProviderMutation } from "@/lib/store/apis/providersApi";
import { ModelProvider } from "@/lib/types/config";
import { performanceFormSchema, type PerformanceFormSchema } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect } from "react";
import { useForm, type Resolver } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { buildProviderUpdatePayload } from "../views/utils";

interface PerformanceFormFragmentProps {
	provider: ModelProvider;
}

export function PerformanceFormFragment({ provider }: PerformanceFormFragmentProps) {
	const { t } = useTranslation();
	const dispatch = useAppDispatch();
	const hasUpdateProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const [updateProvider, { isLoading: isUpdatingProvider }] = useUpdateProviderMutation();
	const form = useForm<PerformanceFormSchema, any, PerformanceFormSchema>({
		resolver: zodResolver(performanceFormSchema) as Resolver<PerformanceFormSchema, any, PerformanceFormSchema>,
		mode: "onChange",
		reValidateMode: "onChange",
		defaultValues: {
			concurrency_and_buffer_size: {
				concurrency: provider.concurrency_and_buffer_size?.concurrency ?? DefaultPerformanceConfig.concurrency,
				buffer_size: provider.concurrency_and_buffer_size?.buffer_size ?? DefaultPerformanceConfig.buffer_size,
			},
		},
	});

	useEffect(() => {
		dispatch(setProviderFormDirtyState(form.formState.isDirty));
	}, [form.formState.isDirty]);

	useEffect(() => {
		// Reset form with new provider's concurrency_and_buffer_size when provider changes
		form.reset({
			concurrency_and_buffer_size: {
				concurrency: provider.concurrency_and_buffer_size?.concurrency ?? DefaultPerformanceConfig.concurrency,
				buffer_size: provider.concurrency_and_buffer_size?.buffer_size ?? DefaultPerformanceConfig.buffer_size,
			},
		});
	}, [form, provider.name, provider.concurrency_and_buffer_size]);

	const onSubmit = (data: PerformanceFormSchema) => {
		// Create updated provider configuration (raw request/response are in Debugging tab)
		const updatedProvider = buildProviderUpdatePayload(provider, {
			concurrency_and_buffer_size: {
				concurrency: data.concurrency_and_buffer_size.concurrency,
				buffer_size: data.concurrency_and_buffer_size.buffer_size,
			},
		});
		updateProvider(updatedProvider)
			.unwrap()
			.then(() => {
				toast.success(t("workspace.providers.providerConfigUpdated"));
				form.reset(data);
			})
			.catch((err) => {
				toast.error(t("workspace.providers.providerConfigUpdateFailed"), {
					description: getErrorMessage(err),
				});
			});
	};

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6 px-6">
				{/* Performance Configuration */}
				<div className="space-y-4">
					<div className="flex flex-row gap-4">
						<div className="flex-1">
							<FormField
								control={form.control}
								name="concurrency_and_buffer_size.concurrency"
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("workspace.providers.concurrency")}</FormLabel>
										<FormControl>
											<Input
												type="number"
												placeholder="10"
												{...field}
												value={field.value === undefined || Number.isNaN(field.value) ? "" : field.value}
												disabled={!hasUpdateProviderAccess}
												onChange={(e) => {
													const value = e.target.value;
													if (value === "") {
														field.onChange(undefined);
														return;
													}
													const parsed = Number.parseInt(value);
													if (!Number.isNaN(parsed)) {
														field.onChange(parsed);
													}
													form.trigger("concurrency_and_buffer_size");
												}}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</div>
						<div className="flex-1">
							<FormField
								control={form.control}
								name="concurrency_and_buffer_size.buffer_size"
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("workspace.providers.bufferSize")}</FormLabel>
										<FormControl>
											<Input
												type="number"
												placeholder="10"
												{...field}
												value={field.value === undefined || Number.isNaN(field.value) ? "" : field.value}
												disabled={!hasUpdateProviderAccess}
												onChange={(e) => {
													const value = e.target.value;
													if (value === "") {
														field.onChange(undefined);
														return;
													}
													const parsed = Number.parseInt(value);
													if (!Number.isNaN(parsed)) {
														field.onChange(parsed);
													}
													form.trigger("concurrency_and_buffer_size");
												}}
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>
						</div>
					</div>
				</div>

				{/* Form Actions */}
				<div className="flex justify-end space-x-2 mb-6">
					<Button
						type="submit"
						disabled={!form.formState.isDirty || !hasUpdateProviderAccess || isUpdatingProvider}
						isLoading={isUpdatingProvider}
					>
						{t("workspace.providers.savePerformanceConfiguration")}
					</Button>
				</div>
			</form>
		</Form>
	);
}