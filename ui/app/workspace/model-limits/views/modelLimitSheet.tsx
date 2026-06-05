import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Label } from "@/components/ui/label";
import { ModelMultiselect } from "@/components/ui/modelMultiselect";
import NumberAndSelect from "@/components/ui/numberAndSelect";
import MultiBudgetLines from "@/components/ui/multibudgets";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { DottedSeparator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { resetDurationOptions } from "@/lib/constants/governance";
import { RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderLabels, ProviderName } from "@/lib/constants/logs";
import { getModelLimitScope, getModelLimitScopes } from "@/lib/registries/modelLimitScopes";
// Side-effect import: pulls in downstream scope registrations (e.g. enterprise
// registers "user" + user picker). The OSS-build fallback is an empty module.
import "@enterprise/lib/registrations/modelLimitScopes";
import {
	getErrorMessage,
	useCreateModelConfigMutation,
	useGetProvidersQuery,
	useLazyGetModelsQuery,
	useUpdateModelConfigMutation,
} from "@/lib/store";
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

const formSchema = z
	.object({
		modelName: z.string().min(1, "Model name is required"),
		provider: z.string().optional(),
		scope: z.string().optional(),
		scopeId: z.string().optional(),
		budgets: z
			.array(
				z.object({
					id: z.string().optional(),
					max_limit: z.number().nonnegative().optional(),
					reset_duration: z.string().optional(),
				}),
			)
			.optional(),
		tokenMaxLimit: z.number().int().nonnegative().optional(),
		tokenResetDuration: z.string().optional(),
		requestMaxLimit: z.number().int().nonnegative().optional(),
		requestResetDuration: z.string().optional(),
	})
	.refine((data) => data.scope !== "virtual_key" || !!data.scopeId, {
		message: "Virtual key is required for the Virtual Key scope",
		path: ["scopeId"],
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
		mode: "onChange",
		resolver: zodResolver(formSchema),
		defaultValues: {
			modelName: modelConfig?.model_name || "",
			provider: modelConfig?.provider || "",
			scope: modelConfig?.scope || "global",
			scopeId: modelConfig?.scope_id || "",
			budgets: (modelConfig?.budgets ?? []).map((b) => ({
				id: b.id,
				max_limit: b.max_limit,
				reset_duration: b.reset_duration,
			})),
			tokenMaxLimit: modelConfig?.rate_limit?.token_max_limit ?? undefined,
			tokenResetDuration: modelConfig?.rate_limit?.token_reset_duration || "1h",
			requestMaxLimit: modelConfig?.rate_limit?.request_max_limit ?? undefined,
			requestResetDuration: modelConfig?.rate_limit?.request_reset_duration || "1h",
		},
	});

	const watchedBudgets = form.watch("budgets");
	const hasAnyLimit =
		(watchedBudgets?.some((b) => b.max_limit !== undefined && b.max_limit !== null) ?? false) ||
		(form.watch("tokenMaxLimit") !== undefined && form.watch("tokenMaxLimit") !== null) ||
		(form.watch("requestMaxLimit") !== undefined && form.watch("requestMaxLimit") !== null);

	useEffect(() => {
		if (hasAnyLimit) form.clearErrors("root");
	}, [hasAnyLimit, form]);

	useEffect(() => {
		if (modelConfig) {
			// Never reset form if user is editing - skip reset entirely
			if (form.formState.isDirty) {
				return;
			}
			form.reset({
				modelName: modelConfig.model_name || "",
				provider: modelConfig.provider || "",
				scope: modelConfig.scope || "global",
				scopeId: modelConfig.scope_id || "",
				budgets: (modelConfig.budgets ?? []).map((b) => ({
					id: b.id,
					max_limit: b.max_limit,
					reset_duration: b.reset_duration,
				})),
				tokenMaxLimit: modelConfig.rate_limit?.token_max_limit ?? undefined,
				tokenResetDuration: modelConfig.rate_limit?.token_reset_duration || "1h",
				requestMaxLimit: modelConfig.rate_limit?.request_max_limit ?? undefined,
				requestResetDuration: modelConfig.rate_limit?.request_reset_duration || "1h",
			});
		}
	}, [modelConfig, form]);

	const onSubmit = async (data: FormData) => {
		if (!canSubmit) {
			toast.error("You don't have permission to perform this action");
			return;
		}

		if (!hasAnyLimit) {
			form.setError("root", { message: "At least one budget or rate limit is required" });
			return;
		}

		try {
			const provider = data.provider && data.provider.trim() !== "" ? data.provider : undefined;

			// Full desired set of budgets (kept lines with a max_limit). For updates this is
			// reconciled server-side; an empty array removes all budgets.
			const budgetsPayload = (data.budgets ?? [])
				.filter((b) => b.max_limit !== undefined && b.max_limit !== null)
				.map((b) => ({ id: b.id, max_limit: b.max_limit as number, reset_duration: b.reset_duration || "1M" }));

			if (isEditing && modelConfig) {
				const hadRateLimit = !!modelConfig.rate_limit;
				const hasRateLimit =
					(data.tokenMaxLimit !== undefined && data.tokenMaxLimit !== null) ||
					(data.requestMaxLimit !== undefined && data.requestMaxLimit !== null);

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
						token_max_limit: data.tokenMaxLimit ?? null,
						token_reset_duration: data.tokenMaxLimit !== undefined && data.tokenMaxLimit !== null ? data.tokenResetDuration || "1h" : null,
						request_max_limit: data.requestMaxLimit ?? null,
						request_reset_duration:
							data.requestMaxLimit !== undefined && data.requestMaxLimit !== null ? data.requestResetDuration || "1h" : null,
					};
				} else if (hadRateLimit) {
					rateLimitPayload = {};
				}

				await updateModelConfig({
					id: modelConfig.id,
					data: {
						model_name: data.modelName,
						provider: provider,
						budgets: budgetsPayload,
						rate_limit: rateLimitPayload,
					},
				}).unwrap();
				toast.success("Model limit updated successfully");
			} else {
				await createModelConfig({
					model_name: data.modelName,
					provider,
					scope: data.scope || "global",
					// Any scope with a registered PickerComponent carries a target;
					// global (no picker) sends no scope_id. Mirrors the registry
					// shape, so adding a new scope (e.g. enterprise's "user")
					// doesn't need a branch here.
					scope_id: getModelLimitScope(data.scope || "global")?.PickerComponent ? data.scopeId : undefined,
					budgets: budgetsPayload.length > 0 ? budgetsPayload : undefined,
					rate_limit:
						(data.tokenMaxLimit !== undefined && data.tokenMaxLimit !== null) ||
						(data.requestMaxLimit !== undefined && data.requestMaxLimit !== null)
							? {
									token_max_limit: data.tokenMaxLimit,
									token_reset_duration:
										data.tokenMaxLimit !== undefined && data.tokenMaxLimit !== null ? data.tokenResetDuration || "1h" : undefined,
									request_max_limit: data.requestMaxLimit,
									request_reset_duration:
										data.requestMaxLimit !== undefined && data.requestMaxLimit !== null ? data.requestResetDuration || "1h" : undefined,
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
				className="flex w-full flex-col overflow-x-hidden pt-4"
				onInteractOutside={(e) => {
					if (isEditing ? form.formState.isDirty : !!form.watch("modelName") || hasAnyLimit) e.preventDefault();
				}}
				onEscapeKeyDown={(e) => {
					if (isEditing ? form.formState.isDirty : !!form.watch("modelName") || hasAnyLimit) e.preventDefault();
				}}
				data-testid="model-limit-sheet"
			>
				<SheetHeader className="flex flex-col items-start p-0 px-8 py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10">
					<SheetTitle>{isEditing ? "Edit Model Limit" : "Create Model Limit"}</SheetTitle>
					<SheetDescription>
						{isEditing ? "Update budget and rate limit configuration." : "Set up budget and rate limits for a model."}
					</SheetDescription>
				</SheetHeader>

				<Form {...form}>
					<form onSubmit={form.handleSubmit(onSubmit)} className="flex h-full flex-col gap-6">
						<div className="grow space-y-4 px-8">
							{/* Provider */}
							<FormField
								control={form.control}
								name="provider"
								render={({ field }) => (
									<FormItem>
										<FormLabel>Provider</FormLabel>
										<Select
											value={field.value || "all"}
											onValueChange={(value) =>
												handleProviderChange(value === "all" ? "" : value, form.getValues("modelName"), field.onChange)
											}
											disabled={isEditing}
										>
											<FormControl>
												<SelectTrigger className="w-full" data-testid="model-limit-provider-select">
													<SelectValue placeholder="All Providers" />
												</SelectTrigger>
											</FormControl>
											<SelectContent>
												<SelectItem value="all">All Providers</SelectItem>
												{availableProviders
													.filter((p) => p.name)
													.map((provider) => (
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

							{/* Model Name */}
							<FormField
								control={form.control}
								name="modelName"
								render={({ field }) => (
									<FormItem>
										<FormLabel>Model Name</FormLabel>
										<FormControl>
											{isEditing ? (
												<Select value={field.value} disabled>
													<SelectTrigger className="w-full" data-testid="model-limit-model-select">
														<SelectValue />
													</SelectTrigger>
													<SelectContent>
														<SelectItem value={field.value}>{field.value === "*" ? "All Models" : field.value}</SelectItem>
													</SelectContent>
												</Select>
											) : (
												<div data-testid="model-limit-model-select">
													<ModelMultiselect
														provider={form.watch("provider") || undefined}
														value={field.value}
														onChange={field.onChange}
														placeholder="Search for a model..."
														isSingleSelect
														loadModelsOnEmptyProvider="base_models"
														allowAllOption
													/>
												</div>
											)}
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>

							{/* Scope */}
							<FormField
								control={form.control}
								name="scope"
								render={({ field }) => (
									<FormItem>
										<FormLabel>Scope</FormLabel>
										<Select
											value={field.value || "global"}
											onValueChange={(value) => {
												field.onChange(value);
												// Reset the scope target when switching scopes
												form.setValue("scopeId", "", { shouldDirty: true });
											}}
											disabled={isEditing}
										>
											<FormControl>
												<SelectTrigger className="w-full" data-testid="model-limit-scope-select">
													<SelectValue placeholder="Global" />
												</SelectTrigger>
											</FormControl>
											<SelectContent>
												{getModelLimitScopes().map((option) => (
													<SelectItem key={option.value} value={option.value}>
														{option.label}
													</SelectItem>
												))}
											</SelectContent>
										</Select>
										<FormMessage />
									</FormItem>
								)}
							/>

							{/* Scope-target picker — driven by the scope registry.
								Each non-global scope (virtual_key, user, …) registers its
								own PickerComponent; we render whichever the current scope
								provides. */}
							{(() => {
								const scopeEntry = getModelLimitScope(form.watch("scope") || "global");
								const Picker = scopeEntry?.PickerComponent;
								if (!Picker) return null;
								return (
									<FormField
										control={form.control}
										name="scopeId"
										render={({ field }) => (
											<FormItem>
												<FormLabel>{scopeEntry.label}</FormLabel>
												<FormControl>
													<div data-testid="model-limit-scope-id-select">
														<Picker
															value={field.value || ""}
															onChange={(v) => field.onChange(v ?? "")}
															disabled={isEditing}
															fallbackOption={
																modelConfig?.scope === scopeEntry.value && modelConfig?.scope_id && modelConfig?.scope_name
																	? { value: modelConfig.scope_id, label: modelConfig.scope_name }
																	: null
															}
														/>
													</div>
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
								);
							})()}

							<DottedSeparator />

							{/* Budget Configuration (multi-budget) */}
							<div className="space-y-4">
								<MultiBudgetLines
									data-testid="model-limit-budget-lines"
									label="Budget"
									lines={(form.watch("budgets") ?? []).map((b) => ({
										id: b.id,
										max_limit: b.max_limit,
										reset_duration: b.reset_duration ?? "1M",
									}))}
									onChange={(lines) => form.setValue("budgets", lines, { shouldDirty: true })}
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
												value={field.value}
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
												value={field.value}
												selectValue={form.watch("requestResetDuration") || "1h"}
												onChangeNumber={(value) => field.onChange(value)}
												onChangeSelect={(value) => form.setValue("requestResetDuration", value, { shouldDirty: true })}
												options={resetDurationOptions}
											/>
											<FormMessage />
										</FormItem>
									)}
								/>
								{form.formState.errors.root && <p className="text-destructive text-sm">{form.formState.errors.root.message}</p>}
							</div>

							{/* Current Usage Display (for editing) */}
							{isEditing && ((modelConfig?.budgets?.length ?? 0) > 0 || modelConfig?.rate_limit) && (
								<>
									<DottedSeparator />
									<div className="space-y-3">
										<Label className="text-sm font-medium">Current Usage</Label>
										<div className="bg-muted/50 grid grid-cols-2 gap-4 rounded-lg p-4">
											{(modelConfig?.budgets ?? []).map((b) => (
												<div key={b.id} className="space-y-1">
													<p className="text-muted-foreground text-xs">Budget ({b.reset_duration})</p>
													<p className="text-sm font-medium">
														${b.current_usage.toFixed(2)} / ${b.max_limit.toFixed(2)}
													</p>
												</div>
											))}
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
						<div className="bg-card sticky bottom-0 shrink-0 border-t px-8 py-4">
							<div className="flex items-center justify-end gap-3">
								{!canSubmit && <p className="text-destructive text-sm">You don't have permission to perform this action</p>}
								<Button type="button" variant="outline" onClick={handleClose}>
									Cancel
								</Button>
								<Button type="submit" data-testid="model-limit-button-submit" disabled={isLoading || !form.formState.isDirty || !canSubmit}>
									{isLoading ? "Saving..." : isEditing ? "Save Changes" : "Create Limit"}
								</Button>
							</div>
						</div>
					</form>
				</Form>
			</SheetContent>
		</Sheet>
	);
}