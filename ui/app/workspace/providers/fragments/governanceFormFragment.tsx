"use client";

import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
	AlertDialogTrigger,
} from "@/components/ui/alertDialog";
import { Button } from "@/components/ui/button";
import { Form, FormField, FormItem } from "@/components/ui/form";
import { Label } from "@/components/ui/label";
import NumberAndSelect from "@/components/ui/numberAndSelect";
import { DottedSeparator } from "@/components/ui/separator";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { resetDurationOptions } from "@/lib/constants/governance";
import {
	getErrorMessage,
	useDeleteProviderGovernanceMutation,
	useGetProviderGovernanceQuery,
	useLazyGetCoreConfigQuery,
	useUpdateProviderGovernanceMutation,
} from "@/lib/store";
import { ModelProvider } from "@/lib/types/config";
import { cn } from "@/lib/utils";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { ArrowLeft, Plus, Settings2, Shield, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";

interface GovernanceFormFragmentProps {
	provider: ModelProvider;
}

const formSchema = z.object({
	// Budget
	budgetMaxLimit: z.string().optional(),
	budgetResetDuration: z.string().optional(),
	// Token limits
	tokenMaxLimit: z.string().optional(),
	tokenResetDuration: z.string().optional(),
	// Request limits
	requestMaxLimit: z.string().optional(),
	requestResetDuration: z.string().optional(),
});

type FormData = z.infer<typeof formSchema>;

const DEFAULT_GOVERNANCE_FORM_VALUES: FormData = {
	budgetMaxLimit: "",
	budgetResetDuration: "1M",
	tokenMaxLimit: "",
	tokenResetDuration: "1h",
	requestMaxLimit: "",
	requestResetDuration: "1h",
};

export function GovernanceFormFragment({ provider }: GovernanceFormFragmentProps) {
	const hasUpdateProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const hasViewAccess = useRbac(RbacResource.Governance, RbacOperation.View);
	const [governanceEnabled, setGovernanceEnabled] = useState<boolean | null>(null);
	const [triggerGetConfig] = useLazyGetCoreConfigQuery();

	// Check if governance is enabled
	useEffect(() => {
		triggerGetConfig({ fromDB: true })
			.then((res) => {
				setGovernanceEnabled(!!res.data?.client_config?.enable_governance);
			})
			.catch(() => {
				setGovernanceEnabled(false);
			});
	}, [triggerGetConfig]);

	const { data: providerGovernanceData, isLoading: isLoadingGovernance } = useGetProviderGovernanceQuery(undefined, {
		skip: !hasViewAccess || !governanceEnabled,
		pollingInterval: hasViewAccess && governanceEnabled ? 10000 : 0,
		refetchOnFocus: true,
		skipPollingIfUnfocused: true,
	});
	const [updateProviderGovernance, { isLoading: isUpdating }] = useUpdateProviderGovernanceMutation();
	const [deleteProviderGovernance, { isLoading: isDeleting }] = useDeleteProviderGovernanceMutation();

	// Track if user is in "create" mode (clicked the add button)
	const [isCreating, setIsCreating] = useState(false);

	// Find governance data for this provider
	const providerGovernance = providerGovernanceData?.providers?.find((p) => p.provider === provider.name);
	const hasExistingGovernance = !!(providerGovernance?.budget || providerGovernance?.rate_limit);

	const form = useForm<FormData>({
		resolver: zodResolver(formSchema),
		defaultValues: DEFAULT_GOVERNANCE_FORM_VALUES,
	});

	// Update form values when provider governance data is loaded (polling)
	useEffect(() => {
		// Never reset form during polling if user is editing
		if (providerGovernance && !form.formState.isDirty) {
			form.reset({
				budgetMaxLimit: providerGovernance.budget?.max_limit ? String(providerGovernance.budget.max_limit) : "",
				budgetResetDuration: providerGovernance.budget?.reset_duration || "1M",
				tokenMaxLimit: providerGovernance.rate_limit?.token_max_limit ? String(providerGovernance.rate_limit.token_max_limit) : "",
				tokenResetDuration: providerGovernance.rate_limit?.token_reset_duration || "1h",
				requestMaxLimit: providerGovernance.rate_limit?.request_max_limit ? String(providerGovernance.rate_limit.request_max_limit) : "",
				requestResetDuration: providerGovernance.rate_limit?.request_reset_duration || "1h",
			});
		}
	}, [providerGovernance, form]);

	// Reset form and creation state when provider changes
	useEffect(() => {
		setIsCreating(false);
		// Never reset form if user is editing - just skip the reset
		if (form.formState.isDirty) {
			return;
		}
		const newProvGov = providerGovernanceData?.providers?.find((p) => p.provider === provider.name);
		form.reset({
			budgetMaxLimit: newProvGov?.budget?.max_limit ? String(newProvGov.budget.max_limit) : "",
			budgetResetDuration: newProvGov?.budget?.reset_duration || "1M",
			tokenMaxLimit: newProvGov?.rate_limit?.token_max_limit ? String(newProvGov.rate_limit.token_max_limit) : "",
			tokenResetDuration: newProvGov?.rate_limit?.token_reset_duration || "1h",
			requestMaxLimit: newProvGov?.rate_limit?.request_max_limit ? String(newProvGov.rate_limit.request_max_limit) : "",
			requestResetDuration: newProvGov?.rate_limit?.request_reset_duration || "1h",
		});
	}, [provider.name, form]);

	const onSubmit = async (data: FormData) => {
		try {
			const budgetMaxLimit = data.budgetMaxLimit ? parseFloat(data.budgetMaxLimit) : undefined;
			const tokenMaxLimit = data.tokenMaxLimit ? parseInt(data.tokenMaxLimit) : undefined;
			const requestMaxLimit = data.requestMaxLimit ? parseInt(data.requestMaxLimit) : undefined;

			// Determine if we need to send empty objects to signal removal
			const hadBudget = !!providerGovernance?.budget;
			const hasBudget = !!budgetMaxLimit;
			const hadRateLimit = !!providerGovernance?.rate_limit;
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

			await updateProviderGovernance({
				provider: provider.name,
				data: {
					budget: budgetPayload,
					rate_limit: rateLimitPayload,
				},
			}).unwrap();

			toast.success(isCreating ? "Governance configured successfully" : "Governance updated successfully");
			setIsCreating(false);
		} catch (error) {
			toast.error("Failed to update provider governance", {
				description: getErrorMessage(error),
			});
		}
	};

	const handleDelete = async () => {
		try {
			await deleteProviderGovernance(provider.name).unwrap();
			toast.success("Governance removed successfully");
			setIsCreating(false);
			form.reset(DEFAULT_GOVERNANCE_FORM_VALUES);
		} catch (error) {
			toast.error("Failed to remove governance", {
				description: getErrorMessage(error),
			});
		}
	};

	const handleCancel = () => {
		setIsCreating(false);
		form.reset(DEFAULT_GOVERNANCE_FORM_VALUES);
	};

	if (isLoadingGovernance || governanceEnabled === null) {
		return (
			<div className="flex items-center justify-center p-12">
				<div className="border-primary h-6 w-6 animate-spin rounded-full border-2 border-t-transparent" />
			</div>
		);
	}

	// Governance not enabled
	if (!governanceEnabled) {
		return (
			<div className="flex flex-col items-center px-6 py-12 text-center">
				<div className="bg-muted mb-4 rounded-full p-4">
					<Shield className="text-muted-foreground h-8 w-8" />
				</div>
				<h3 className="mb-2 text-lg font-semibold">Governance Not Enabled</h3>
				<p className="text-muted-foreground max-w-sm text-sm">
					Enable governance in your configuration to set up budget and rate limits for this provider.
				</p>
			</div>
		);
	}

	// Empty state - no governance configured and not in create mode
	if (!hasExistingGovernance && !isCreating) {
		return (
			<div className="px-6 pb-6">
				<div className="relative">
					<div className="flex flex-col items-center px-8 py-16 text-center">
						{/* Icon */}
						<div className="bg-muted from-muted to-muted/50 ring-border/50 mb-6 rounded-2xl bg-gradient-to-br p-4 shadow-lg ring-1 dark:bg-gradient-to-br dark:from-zinc-800 dark:to-zinc-900 dark:ring-white/10">
							<Shield className="text-muted-foreground h-8 w-8 dark:text-zinc-300" />
						</div>

						{/* Content */}
						<h3 className="mb-2 text-xl font-semibold tracking-tight">No Governance Configured</h3>
						<p className="text-muted-foreground mb-8 max-w-md text-sm leading-relaxed">
							Set up budget limits and rate controls to manage costs and prevent overuse of this provider&apos;s resources.
						</p>

						{/* CTA Button */}
						<TooltipProvider>
							<Tooltip>
								<TooltipTrigger asChild>
									<Button
										onClick={() => setIsCreating(true)}
										disabled={!hasUpdateProviderAccess}
										size="lg"
										className="group gap-2 px-6 shadow-lg transition-all hover:shadow-xl"
									>
										<Plus className="h-4 w-4" />
										Configure Governance
									</Button>
								</TooltipTrigger>
								{!hasUpdateProviderAccess && (
									<TooltipContent>
										<p>You don&apos;t have permission to configure governance</p>
									</TooltipContent>
								)}
							</Tooltip>
						</TooltipProvider>

						{/* Feature hints */}
						<div className="text-muted-foreground mt-8 flex items-center justify-center gap-2 text-xs">
							<span>Budget Limits</span>
							<span>•</span>
							<span>Token Rate Limiting</span>
							<span>•</span>
							<span>Request Throttling</span>
						</div>
					</div>
				</div>
			</div>
		);
	}

	// Form state - either creating new or editing existing
	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6 px-6">
				{/* Header with back button (only in create mode) or settings icon */}
				<div className="flex items-start justify-between">
					<div className="space-y-1">
						{isCreating && !hasExistingGovernance ? (
							<div className="flex items-center gap-3">
								<button
									type="button"
									onClick={handleCancel}
									className="text-muted-foreground hover:text-foreground -ml-1 rounded-lg p-1 transition-colors hover:bg-zinc-800"
								>
									<ArrowLeft className="h-4 w-4" />
								</button>
								<div>
									<h3 className="text-sm font-medium">Configure Governance</h3>
									<p className="text-muted-foreground text-sm">Set up budget and rate limits for this provider.</p>
								</div>
							</div>
						) : (
							<div className="flex items-center gap-3">
								<div className="bg-muted rounded-lg p-2 dark:bg-zinc-800">
									<Settings2 className="text-muted-foreground h-4 w-4" />
								</div>
								<div>
									<h3 className="text-sm font-medium">Provider-Level Governance</h3>
									<p className="text-muted-foreground text-sm">
										Configure budget and rate limits that apply to all requests made through this provider.
									</p>
								</div>
							</div>
						)}
					</div>
				</div>

				{/* Budget Configuration */}
				<div className="space-y-4">
					<Label className="text-sm font-medium">Budget Configuration</Label>
					<FormField
						control={form.control}
						name="budgetMaxLimit"
						render={({ field }) => (
							<FormItem>
								<NumberAndSelect
									id="providerBudgetMaxLimit"
									labelClassName="font-normal"
									label="Maximum Spend (USD)"
									value={field.value || ""}
									selectValue={form.watch("budgetResetDuration") || "1M"}
									onChangeNumber={(value) => field.onChange(value)}
									onChangeSelect={(value) => form.setValue("budgetResetDuration", value, { shouldDirty: true })}
									options={resetDurationOptions}
								/>
							</FormItem>
						)}
					/>
				</div>

				<DottedSeparator />

				{/* Rate Limiting Configuration */}
				<div className="space-y-4">
					<Label className="text-sm font-medium">Rate Limiting Configuration</Label>

					<FormField
						control={form.control}
						name="tokenMaxLimit"
						render={({ field }) => (
							<FormItem>
								<NumberAndSelect
									id="providerTokenMaxLimit"
									labelClassName="font-normal"
									label="Maximum Tokens"
									value={field.value || ""}
									selectValue={form.watch("tokenResetDuration") || "1h"}
									onChangeNumber={(value) => field.onChange(value)}
									onChangeSelect={(value) => form.setValue("tokenResetDuration", value, { shouldDirty: true })}
									options={resetDurationOptions}
								/>
							</FormItem>
						)}
					/>

					<FormField
						control={form.control}
						name="requestMaxLimit"
						render={({ field }) => (
							<FormItem>
								<NumberAndSelect
									id="providerRequestMaxLimit"
									labelClassName="font-normal"
									label="Maximum Requests"
									value={field.value || ""}
									selectValue={form.watch("requestResetDuration") || "1h"}
									onChangeNumber={(value) => field.onChange(value)}
									onChangeSelect={(value) => form.setValue("requestResetDuration", value, { shouldDirty: true })}
									options={resetDurationOptions}
								/>
							</FormItem>
						)}
					/>
				</div>

				{/* Current Usage Display - only when editing existing */}
				{hasExistingGovernance && (providerGovernance?.budget || providerGovernance?.rate_limit) && (
					<>
						<DottedSeparator />
						<div className="space-y-4">
							<Label className="text-sm font-medium">Current Usage</Label>
							<div className="bg-muted/50 grid grid-cols-2 gap-4 rounded-lg p-4">
								{providerGovernance?.budget && (
									<div className="space-y-1">
										<p className="text-muted-foreground text-xs">Budget Usage</p>
										<p className="text-sm font-medium">
											${providerGovernance.budget.current_usage.toFixed(2)} / ${providerGovernance.budget.max_limit.toFixed(2)}
										</p>
									</div>
								)}
								{providerGovernance?.rate_limit?.token_max_limit && (
									<div className="space-y-1">
										<p className="text-muted-foreground text-xs">Token Usage</p>
										<p className="text-sm font-medium">
											{providerGovernance.rate_limit.token_current_usage.toLocaleString()} /{" "}
											{providerGovernance.rate_limit.token_max_limit.toLocaleString()}
										</p>
									</div>
								)}
								{providerGovernance?.rate_limit?.request_max_limit && (
									<div className="space-y-1">
										<p className="text-muted-foreground text-xs">Request Usage</p>
										<p className="text-sm font-medium">
											{providerGovernance.rate_limit.request_current_usage.toLocaleString()} /{" "}
											{providerGovernance.rate_limit.request_max_limit.toLocaleString()}
										</p>
									</div>
								)}
							</div>
						</div>
					</>
				)}

				{/* Form Actions */}
				<div className={cn("flex items-center pb-6", hasExistingGovernance ? "justify-between" : "justify-end")}>
					{/* Delete button - only show when editing existing governance */}
					{hasExistingGovernance && (
						<AlertDialog>
							<AlertDialogTrigger asChild>
								<Button type="button" variant="ghost" size="sm" className="text-red-500 hover:bg-red-500/10 hover:text-red-500">
									<Trash2 className="mr-2 h-4 w-4" />
									Remove Governance
								</Button>
							</AlertDialogTrigger>
							<AlertDialogContent>
								<AlertDialogHeader>
									<AlertDialogTitle>Remove Governance Configuration?</AlertDialogTitle>
									<AlertDialogDescription>
										This will remove all budget limits and rate controls for this provider. Any accumulated usage data will be lost. This
										action cannot be undone.
									</AlertDialogDescription>
								</AlertDialogHeader>
								<AlertDialogFooter>
									<AlertDialogCancel>Cancel</AlertDialogCancel>
									<AlertDialogAction onClick={handleDelete} className="bg-red-600 text-white hover:bg-red-700" disabled={isDeleting}>
										{isDeleting ? "Removing..." : "Remove Governance"}
									</AlertDialogAction>
								</AlertDialogFooter>
							</AlertDialogContent>
						</AlertDialog>
					)}

					<div className="flex items-center gap-2">
						{isCreating && !hasExistingGovernance && (
							<Button type="button" variant="ghost" onClick={handleCancel}>
								Cancel
							</Button>
						)}
						<TooltipProvider>
							<Tooltip>
								<TooltipTrigger asChild>
									<Button
										type="submit"
										disabled={!form.formState.isDirty || !form.formState.isValid || !hasUpdateProviderAccess}
										isLoading={isUpdating}
									>
										{isCreating && !hasExistingGovernance ? "Save Configuration" : "Save Changes"}
									</Button>
								</TooltipTrigger>
								{(!form.formState.isDirty || !form.formState.isValid) && (
									<TooltipContent>
										<p>
											{!form.formState.isDirty && !form.formState.isValid
												? "No changes made and validation errors present"
												: !form.formState.isDirty
													? "No changes made"
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
	);
}
