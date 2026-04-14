import { Button } from "@/components/ui/button";
import { Form, FormField, FormItem } from "@/components/ui/form";
import { Label } from "@/components/ui/label";
import NumberAndSelect from "@/components/ui/numberAndSelect";
import { DottedSeparator } from "@/components/ui/separator";
import { resetDurationOptions } from "@/lib/constants/governance";
import {
	getErrorMessage,
	useDeleteProviderGovernanceMutation,
	useGetProviderGovernanceQuery,
	useUpdateProviderGovernanceMutation,
} from "@/lib/store";
import { ModelProvider } from "@/lib/types/config";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";

interface GovernanceFormFragmentProps {
	provider: ModelProvider;
}

const formSchema = z.object({
	// Budget
	budgetMaxLimit: z.number().nonnegative().optional(),
	budgetResetDuration: z.string().optional(),
	// Token limits
	tokenMaxLimit: z.number().int().nonnegative().optional(),
	tokenResetDuration: z.string().optional(),
	// Request limits
	requestMaxLimit: z.number().int().nonnegative().optional(),
	requestResetDuration: z.string().optional(),
});

type FormData = z.infer<typeof formSchema>;

const DEFAULT_GOVERNANCE_FORM_VALUES: FormData = {
	budgetMaxLimit: undefined,
	budgetResetDuration: "1M",
	tokenMaxLimit: undefined,
	tokenResetDuration: "1h",
	requestMaxLimit: undefined,
	requestResetDuration: "1h",
};

export function GovernanceFormFragment({ provider }: GovernanceFormFragmentProps) {
	const hasUpdateProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const hasViewAccess = useRbac(RbacResource.Governance, RbacOperation.View);

	const { data: providerGovernanceData } = useGetProviderGovernanceQuery(undefined, {
		skip: !hasViewAccess,
		pollingInterval: 5000,
	});
	const [updateProviderGovernance, { isLoading: isUpdating }] = useUpdateProviderGovernanceMutation();
	const [deleteProviderGovernance, { isLoading: isDeleting }] = useDeleteProviderGovernanceMutation();

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
				budgetMaxLimit: providerGovernance.budget?.max_limit ?? undefined,
				budgetResetDuration: providerGovernance.budget?.reset_duration || "1M",
				tokenMaxLimit: providerGovernance.rate_limit?.token_max_limit ?? undefined,
				tokenResetDuration: providerGovernance.rate_limit?.token_reset_duration || "1h",
				requestMaxLimit: providerGovernance.rate_limit?.request_max_limit ?? undefined,
				requestResetDuration: providerGovernance.rate_limit?.request_reset_duration || "1h",
			});
		}
	}, [providerGovernance, form]);

	// Reset form when provider changes
	useEffect(() => {
		// Never reset form if user is editing - just skip the reset
		if (form.formState.isDirty) {
			return;
		}
		const newProvGov = providerGovernanceData?.providers?.find((p) => p.provider === provider.name);
		form.reset({
			budgetMaxLimit: newProvGov?.budget?.max_limit ?? undefined,
			budgetResetDuration: newProvGov?.budget?.reset_duration || "1M",
			tokenMaxLimit: newProvGov?.rate_limit?.token_max_limit ?? undefined,
			tokenResetDuration: newProvGov?.rate_limit?.token_reset_duration || "1h",
			requestMaxLimit: newProvGov?.rate_limit?.request_max_limit ?? undefined,
			requestResetDuration: newProvGov?.rate_limit?.request_reset_duration || "1h",
		});
	}, [provider.name, form]);

	const onSubmit = async (data: FormData) => {
		try {
			// Determine if we need to send empty objects to signal removal
			const hadBudget = !!providerGovernance?.budget;
			const hasBudget = data.budgetMaxLimit !== undefined;
			const hadRateLimit = !!providerGovernance?.rate_limit;
			const hasRateLimit = data.tokenMaxLimit !== undefined || data.requestMaxLimit !== undefined;

			let budgetPayload: { max_limit?: number; reset_duration?: string } | undefined;
			if (hasBudget) {
				budgetPayload = {
					max_limit: data.budgetMaxLimit,
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
					token_max_limit: data.tokenMaxLimit ?? null,
					token_reset_duration: data.tokenMaxLimit !== undefined ? data.tokenResetDuration || "1h" : null,
					request_max_limit: data.requestMaxLimit ?? null,
					request_reset_duration: data.requestMaxLimit !== undefined ? data.requestResetDuration || "1h" : null,
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

			toast.success("Governance configuration saved successfully");

			// Reset form with the saved values to update the initial state for change detection
			form.reset(data);
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
			form.reset(DEFAULT_GOVERNANCE_FORM_VALUES);
		} catch (error) {
			toast.error("Failed to remove governance", {
				description: getErrorMessage(error),
			});
		}
	};

	// Always show the form
	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6 px-6">
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
									value={field.value}
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
									value={field.value}
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
									value={field.value}
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
				<div className="flex justify-end space-x-2 pb-6">
					<Button
						type="button"
						variant="outline"
						onClick={handleDelete}
						disabled={!hasUpdateProviderAccess || isDeleting || !hasExistingGovernance}
					>
						Remove configuration
					</Button>
					<Button
						type="submit"
						disabled={!form.formState.isDirty || !form.formState.isValid || !hasUpdateProviderAccess || isUpdating}
						isLoading={isUpdating}
					>
						Save Governance Configuration
					</Button>
				</div>
			</form>
		</Form>
	);
}