"use client";

import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Label } from "@/components/ui/label";
import { ModelMultiselect } from "@/components/ui/modelMultiselect";
import NumberAndSelect from "@/components/ui/numberAndSelect";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { DottedSeparator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { resetDurationOptions } from "@/lib/constants/governance";
import { RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderLabels, ProviderName } from "@/lib/constants/logs";
import { getErrorMessage, useCreateModelConfigMutation, useGetProvidersQuery, useLazyGetModelsQuery, useUpdateModelConfigMutation } from "@/lib/store";
import { KnownProvider } from "@/lib/types/config";
import { ModelConfig } from "@/lib/types/governance";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";

interface ModelLimitSheetProps {
	modelConfig?: ModelConfig | null;
	onSave: () => void;
	onCancel: () => void;
}

const formSchema = z.object({
	modelName: z.string().min(1, "Model name is required"),
	provider: z.string().optional(),
	budgetMaxLimit: z.string().optional(),
	budgetResetDuration: z.string().optional(),
	tokenMaxLimit: z.string().optional(),
	tokenResetDuration: z.string().optional(),
	requestMaxLimit: z.string().optional(),
	requestResetDuration: z.string().optional(),
});

type FormData = z.infer<typeof formSchema>;

export default function ModelLimitSheet({ modelConfig, onSave, onCancel }: ModelLimitSheetProps) {
	const [isOpen, setIsOpen] = useState(true);
	const isEditing = !!modelConfig;

	const hasCreateAccess = useRbac(RbacResource.Governance, RbacOperation.Create);
	const hasUpdateAccess = useRbac(RbacResource.Governance, RbacOperation.Update);
	const canSubmit = isEditing ? hasUpdateAccess : hasCreateAccess;

	const handleClose = () => {
		setIsOpen(false);
		setTimeout(() => {
			onCancel();
		}, 150);
	};

	const { data: providersData } = useGetProvidersQuery();
	const [createModelConfig, { isLoading: isCreating }] = useCreateModelConfigMutation();
	const [updateModelConfig, { isLoading: isUpdating }] = useUpdateModelConfigMutation();
	const [getModels] = useLazyGetModelsQuery();
	const isLoading = isCreating || isUpdating;

	const availableProviders = providersData || [];

	// Handle provider change - clear model if it doesn't exist for the new provider
	const handleProviderChange = async (newProvider: string, currentModel: string, onChange: (value: string) => void) => {
		onChange(newProvider);
		if (!currentModel) return;

		try {
			const response = await getModels({
				provider: newProvider || undefined,
				query: currentModel,
				limit: 50,
			}).unwrap();

			const modelExists = response.models.some((model) => model.name === currentModel);
			if (!modelExists) {
				form.setValue("modelName", "", { shouldDirty: true });
			}
		} catch {
			// On error, don't clear the model
		}
	};

	const form = useForm<FormData>({
		resolver: zodResolver(formSchema),
		defaultValues: {
			modelName: modelConfig?.model_name || "",
			provider: modelConfig?.provider || "",
			budgetMaxLimit: modelConfig?.budget ? String(modelConfig.budget.max_limit) : "",
			budgetResetDuration: modelConfig?.budget?.reset_duration || "1M",
			tokenMaxLimit: modelConfig?.rate_limit?.token_max_limit ? String(modelConfig.rate_limit.token_max_limit) : "",
			tokenResetDuration: modelConfig?.rate_limit?.token_reset_duration || "1h",
			requestMaxLimit: modelConfig?.rate_limit?.request_max_limit ? String(modelConfig.rate_limit.request_max_limit) : "",
			requestResetDuration: modelConfig?.rate_limit?.request_reset_duration || "1h",
		},
	});

	useEffect(() => {
		if (modelConfig) {
			// Never reset form if user is editing - skip reset entirely
			if (form.formState.isDirty) {
				return;
			}
			form.reset({
				modelName: modelConfig.model_name || "",
				provider: modelConfig.provider || "",
				budgetMaxLimit: modelConfig.budget ? String(modelConfig.budget.max_limit) : "",
				budgetResetDuration: modelConfig.budget?.reset_duration || "1M",
				tokenMaxLimit: modelConfig.rate_limit?.token_max_limit ? String(modelConfig.rate_limit.token_max_limit) : "",
				tokenResetDuration: modelConfig.rate_limit?.token_reset_duration || "1h",
				requestMaxLimit: modelConfig.rate_limit?.request_max_limit ? String(modelConfig.rate_limit.request_max_limit) : "",
				requestResetDuration: modelConfig.rate_limit?.request_reset_duration || "1h",
			});
		}
	}, [modelConfig, form]);

	const onSubmit = async (data: FormData) => {
		if (!canSubmit) {
			toast.error("You don't have permission to perform this action");
			return;
		}

		try {
			const budgetMaxLimit = data.budgetMaxLimit ? parseFloat(data.budgetMaxLimit) : undefined;
			const tokenMaxLimit = data.tokenMaxLimit ? parseInt(data.tokenMaxLimit) : undefined;
			const requestMaxLimit = data.requestMaxLimit ? parseInt(data.requestMaxLimit) : undefined;
			const provider = data.provider && data.provider.trim() !== "" ? data.provider : undefined;

			if (isEditing && modelConfig) {
				const hadBudget = !!modelConfig.budget;
				const hasBudget = !!budgetMaxLimit;
				const hadRateLimit = !!modelConfig.rate_limit;
				const hasRateLimit = !!tokenMaxLimit || !!requestMaxLimit;

				let budgetPayload: { max_limit?: number; reset_duration?: string } | undefined;
				if (hasBudget) {
					budgetPayload = {
						max_limit: budgetMaxLimit,
						reset_duration: data.budgetResetDuration || "1M",
					};
				} else if (hadBudget) {
					budgetPayload = {};
				}

				let rateLimitPayload:
					| {
						token_max_limit?: number | null;
						token_reset_duration?: string | null;
						request_max_limit?: number | null;
						request_reset_duration?: string | null;
					}
					| undefined;
				if (hasRateLimit) {
					rateLimitPayload = {
						token_max_limit: tokenMaxLimit ?? null,
						token_reset_duration: tokenMaxLimit ? data.tokenResetDuration || "1h" : null,
						request_max_limit: requestMaxLimit ?? null,
						request_reset_duration: requestMaxLimit ? data.requestResetDuration || "1h" : null,
					};
				} else if (hadRateLimit) {
					rateLimitPayload = {};
				}

				await updateModelConfig({
					id: modelConfig.id,
					data: {
						model_name: data.modelName,
						provider: provider,
						budget: budgetPayload,
						rate_limit: rateLimitPayload,
					},
				}).unwrap();
				toast.success("Model limit updated successfully");
			} else {
				await createModelConfig({
					model_name: data.modelName,
					provider,
					budget: budgetMaxLimit
						? {
							max_limit: budgetMaxLimit,
							reset_duration: data.budgetResetDuration || "1M",
						}
						: undefined,
					rate_limit:
						tokenMaxLimit || requestMaxLimit
							? {
								token_max_limit: tokenMaxLimit,
								token_reset_duration: data.tokenResetDuration || "1h",
								request_max_limit: requestMaxLimit,
								request_reset_duration: data.requestResetDuration || "1h",
							}
							: undefined,
				}).unwrap();
				toast.success("Model limit created successfully");
			}

			onSave();
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	return (
		<Sheet open={isOpen} onOpenChange={(open) => !open && handleClose()}>
			<SheetContent
				className="dark:bg-card flex w-full flex-col overflow-x-hidden bg-white p-8"
				onInteractOutside={(e) => e.preventDefault()}
				onEscapeKeyDown={(e) => e.preventDefault()}
			>
				<SheetHeader className="flex flex-col items-start p-0">
					<SheetTitle>{isEditing ? "Edit Model Limit" : "Create Model Limit"}</SheetTitle>
					<SheetDescription>
						{isEditing ? "Update budget and rate limit configuration." : "Set up budget and rate limits for a model."}
					</SheetDescription>
				</SheetHeader>

				<Form {...form}>
					<form onSubmit={form.handleSubmit(onSubmit)} className="flex h-full flex-col gap-6">
						<div className="space-y-4">
							{/* Model Name */}
							<FormField
								control={form.control}
								name="modelName"
								render={({ field }) => (
									<FormItem>
										<FormLabel>Model Name</FormLabel>
										<FormControl>
											<ModelMultiselect
												provider={form.watch("provider") || undefined}
												value={field.value}
												onChange={field.onChange}
												placeholder="Select a model..."
												isSingleSelect
												loadModelsOnEmptyProvider
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>

							{/* Provider */}
							<FormField
								control={form.control}
								name="provider"
								render={({ field }) => (
									<FormItem>
										<FormLabel>Provider</FormLabel>
										<Select
											value={field.value || "all"}
											onValueChange={(value) => handleProviderChange(value === "all" ? "" : value, form.getValues("modelName"), field.onChange)}
										>
											<FormControl>
												<SelectTrigger className="w-full">
													<SelectValue placeholder="All Providers" />
												</SelectTrigger>
											</FormControl>
											<SelectContent>
												<SelectItem value="all">All Providers</SelectItem>
												{availableProviders.map((provider) => (
													<SelectItem key={provider.name} value={provider.name}>
														<RenderProviderIcon
															provider={provider.custom_provider_config?.base_provider_type || (provider.name as KnownProvider)}
															size="sm"
															className="h-4 w-4"
														/>
														{provider.custom_provider_config
															? provider.name
															: ProviderLabels[provider.name as ProviderName] || provider.name}
													</SelectItem>
												))}
											</SelectContent>
										</Select>
										<FormMessage />
									</FormItem>
								)}
							/>

							<DottedSeparator />

							{/* Budget Configuration */}
							<div className="space-y-4">
								<Label className="text-sm font-medium">Budget</Label>
								<FormField
									control={form.control}
									name="budgetMaxLimit"
									render={({ field }) => (
										<FormItem>
											<NumberAndSelect
												id="modelBudgetMaxLimit"
												labelClassName="font-normal"
												label="Maximum Spend (USD)"
												value={field.value || ""}
												selectValue={form.watch("budgetResetDuration") || "1M"}
												onChangeNumber={(value) => field.onChange(value)}
												onChangeSelect={(value) => form.setValue("budgetResetDuration", value, { shouldDirty: true })}
												options={resetDurationOptions}
											/>
											<FormMessage />
										</FormItem>
									)}
								/>
							</div>

							<DottedSeparator />

							{/* Rate Limiting Configuration */}
							<div className="space-y-4">
								<Label className="text-sm font-medium">Rate Limits</Label>

								<FormField
									control={form.control}
									name="tokenMaxLimit"
									render={({ field }) => (
										<FormItem>
											<NumberAndSelect
												id="modelTokenMaxLimit"
												labelClassName="font-normal"
												label="Maximum Tokens"
												value={field.value || ""}
												selectValue={form.watch("tokenResetDuration") || "1h"}
												onChangeNumber={(value) => field.onChange(value)}
												onChangeSelect={(value) => form.setValue("tokenResetDuration", value, { shouldDirty: true })}
												options={resetDurationOptions}
											/>
											<FormMessage />
										</FormItem>
									)}
								/>

								<FormField
									control={form.control}
									name="requestMaxLimit"
									render={({ field }) => (
										<FormItem>
											<NumberAndSelect
												id="modelRequestMaxLimit"
												labelClassName="font-normal"
												label="Maximum Requests"
												value={field.value || ""}
												selectValue={form.watch("requestResetDuration") || "1h"}
												onChangeNumber={(value) => field.onChange(value)}
												onChangeSelect={(value) => form.setValue("requestResetDuration", value, { shouldDirty: true })}
												options={resetDurationOptions}
											/>
											<FormMessage />
										</FormItem>
									)}
								/>
							</div>

							{/* Current Usage Display (for editing) */}
							{isEditing && (modelConfig?.budget || modelConfig?.rate_limit) && (
								<>
									<DottedSeparator />
									<div className="space-y-3">
										<Label className="text-sm font-medium">Current Usage</Label>
										<div className="bg-muted/50 grid grid-cols-2 gap-4 rounded-lg p-4">
											{modelConfig?.budget && (
												<div className="space-y-1">
													<p className="text-muted-foreground text-xs">Budget</p>
													<p className="text-sm font-medium">
														${modelConfig.budget.current_usage.toFixed(2)} / ${modelConfig.budget.max_limit.toFixed(2)}
													</p>
												</div>
											)}
											{modelConfig?.rate_limit?.token_max_limit && (
												<div className="space-y-1">
													<p className="text-muted-foreground text-xs">Tokens</p>
													<p className="text-sm font-medium">
														{modelConfig.rate_limit.token_current_usage.toLocaleString()} /{" "}
														{modelConfig.rate_limit.token_max_limit.toLocaleString()}
													</p>
												</div>
											)}
											{modelConfig?.rate_limit?.request_max_limit && (
												<div className="space-y-1">
													<p className="text-muted-foreground text-xs">Requests</p>
													<p className="text-sm font-medium">
														{modelConfig.rate_limit.request_current_usage.toLocaleString()} /{" "}
														{modelConfig.rate_limit.request_max_limit.toLocaleString()}
													</p>
												</div>
											)}
										</div>
									</div>
								</>
							)}
						</div>

						{/* Footer */}
						<div className="py-4">
							<div className="flex justify-end gap-3">
								<Button type="button" variant="outline" onClick={handleClose}>
									Cancel
								</Button>
								<TooltipProvider>
									<Tooltip>
										<TooltipTrigger asChild>
											<span className="inline-block">
												<Button type="submit" disabled={isLoading || !form.formState.isDirty || !form.formState.isValid || !canSubmit || !form.watch("modelName")}>
													{isLoading ? "Saving..." : isEditing ? "Save Changes" : "Create Limit"}
												</Button>
											</span>
										</TooltipTrigger>
										{(isLoading || !form.formState.isDirty || !form.formState.isValid || !canSubmit || !form.watch("modelName")) && (
											<TooltipContent>
												<p>
													{!canSubmit
														? "You don't have permission"
														: isLoading
															? "Saving..."
															: !form.formState.isDirty
																? "No changes made"
																: !form.watch("modelName")
																	? "Model name is required"
																	: "Please fix validation errors"}
												</p>
											</TooltipContent>
										)}
									</Tooltip>
								</TooltipProvider>
							</div>
						</div>
					</form>
				</Form>
			</SheetContent>
		</Sheet>
	);
}
