import { Button } from "@/components/ui/button";
import { Form } from "@/components/ui/form";
import MultiBudgetLines, { BudgetLineEntry } from "@/components/ui/multibudgets";
import NumberAndSelect from "@/components/ui/numberAndSelect";
import { DottedSeparator } from "@/components/ui/separator";
import { resetDurationLabels } from "@/lib/constants/governance";
import {
	getErrorMessage,
	useDeleteProviderGovernanceMutation,
	useGetProviderGovernanceQuery,
	useUpdateProviderGovernanceMutation,
} from "@/lib/store";
import { ModelProvider } from "@/lib/types/config";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { Label } from "@radix-ui/react-label";
import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";

interface GovernanceFormFragmentProps {
	provider: ModelProvider;
}

const budgetLineSchema = z.object({
	id: z.string().optional(),
	max_limit: z.number({ invalid_type_error: "Budget limit must be a number" }).nonnegative("Budget limit cannot be negative").optional(),
	reset_duration: z.string({ required_error: "Reset duration is required" }),
});

const formSchema = z.object({
	budgets: z.array(budgetLineSchema).optional(),
	// Token limits
	tokenMaxLimit: z.number().int().nonnegative().optional(),
	tokenResetDuration: z.string().optional(),
	// Request limits
	requestMaxLimit: z.number().int().nonnegative().optional(),
	requestResetDuration: z.string().optional(),
});

type FormData = z.infer<typeof formSchema>;

const DEFAULT_GOVERNANCE_FORM_VALUES: FormData = {
	budgets: [],
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
	const hasExistingGovernance = !!(providerGovernance?.budgets?.length || providerGovernance?.rate_limit);

	const form = useForm<FormData>({
		resolver: zodResolver(formSchema),
		defaultValues: DEFAULT_GOVERNANCE_FORM_VALUES,
	});

	const governanceToFormValues = (pg: typeof providerGovernance): FormData => ({
		budgets: pg?.budgets?.map((b) => ({ id: b.id, max_limit: b.max_limit, reset_duration: b.reset_duration })) ?? [],
		tokenMaxLimit: pg?.rate_limit?.token_max_limit ?? undefined,
		tokenResetDuration: pg?.rate_limit?.token_reset_duration || "1h",
		requestMaxLimit: pg?.rate_limit?.request_max_limit ?? undefined,
		requestResetDuration: pg?.rate_limit?.request_reset_duration || "1h",
	});

	// Update form values when provider governance data is loaded (polling)
	useEffect(() => {
		if (providerGovernance && !form.formState.isDirty) {
			form.reset(governanceToFormValues(providerGovernance));
		}
	}, [providerGovernance, form]);

	// Reset form when provider changes
	useEffect(() => {
		if (form.formState.isDirty) return;
		const newProvGov = providerGovernanceData?.providers?.find((p) => p.provider === provider.name);
		form.reset(governanceToFormValues(newProvGov));
	}, [provider.name, form]);

	const onSubmit = async (data: FormData) => {
		try {
			const hadBudgets = (providerGovernance?.budgets?.length ?? 0) > 0;
			const validBudgets = (data.budgets ?? []).filter((b): b is BudgetLineEntry & { max_limit: number } => b.max_limit !== undefined);
			const hasBudgets = validBudgets.length > 0;
			const hadRateLimit = !!providerGovernance?.rate_limit;
			const hasRateLimit = data.tokenMaxLimit !== undefined || data.requestMaxLimit !== undefined;

			// absent = no change; [] = remove all; [...] = set to this list
			let budgetsPayload: { id?: string; max_limit: number; reset_duration: string }[] | undefined;
			if (hasBudgets) {
				budgetsPayload = validBudgets.map((b) => ({ id: b.id, max_limit: b.max_limit, reset_duration: b.reset_duration }));
			} else if (hadBudgets) {
				budgetsPayload = [];
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
					budgets: budgetsPayload,
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

	const watchedBudgets = form.watch("budgets") ?? [];

	// Always show the form
	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6 px-6">
				{/* Budget Configuration */}
				<MultiBudgetLines
					data-testid="provider-budgets"
					lines={watchedBudgets}
					onChange={(lines) => form.setValue("budgets", lines, { shouldDirty: true })}
				/>

				<DottedSeparator />

				{/* Rate Limiting Configuration */}
				<div className="space-y-4">
					<Label className="text-sm font-medium">Rate Limiting Configuration</Label>

					<NumberAndSelect
						id="providerTokenMaxLimit"
						labelClassName="font-normal"
						label="Maximum Tokens"
						value={form.watch("tokenMaxLimit")}
						selectValue={form.watch("tokenResetDuration") || "1h"}
						onChangeNumber={(value) => form.setValue("tokenMaxLimit", value, { shouldDirty: true })}
						onChangeSelect={(value) => form.setValue("tokenResetDuration", value, { shouldDirty: true })}
						options={[
							{ label: "per hour", value: "1h" },
							{ label: "per day", value: "1d" },
							{ label: "per week", value: "1w" },
							{ label: "per month", value: "1M" },
						]}
					/>

					<NumberAndSelect
						id="providerRequestMaxLimit"
						labelClassName="font-normal"
						label="Maximum Requests"
						value={form.watch("requestMaxLimit")}
						selectValue={form.watch("requestResetDuration") || "1h"}
						onChangeNumber={(value) => form.setValue("requestMaxLimit", value, { shouldDirty: true })}
						onChangeSelect={(value) => form.setValue("requestResetDuration", value, { shouldDirty: true })}
						options={[
							{ label: "per hour", value: "1h" },
							{ label: "per day", value: "1d" },
							{ label: "per week", value: "1w" },
							{ label: "per month", value: "1M" },
						]}
					/>
				</div>

				{/* Current Usage Display - only when editing existing */}
				{hasExistingGovernance && (
					<>
						<DottedSeparator />
						<div className="space-y-4">
							<Label className="text-sm font-medium">Current Usage</Label>
							<div className="bg-muted/50 grid grid-cols-2 gap-4 rounded-lg p-4">
								{[...(providerGovernance?.budgets ?? [])]
								.sort((a, b) => new Date(b.created_at ?? 0).getTime() - new Date(a.created_at ?? 0).getTime())
								.map((budget, i) => (
									<div key={budget.id ?? i} className="space-y-1">
										<p className="text-muted-foreground text-xs">
											Budget Usage
											{(providerGovernance.budgets?.length ?? 0) > 1 && ` (${resetDurationLabels[budget.reset_duration] ?? budget.reset_duration})`}
										</p>
										<p className="text-sm font-medium">
											${budget.current_usage.toFixed(2)} / ${budget.max_limit.toFixed(2)}
										</p>
									</div>
								))}
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
				<div className="mb-6 flex justify-end space-x-2">
					<Button
						type="button"
						variant="outline"
						onClick={handleDelete}
						disabled={!hasUpdateProviderAccess || isDeleting || !hasExistingGovernance}
					>
						Remove configuration
					</Button>
					<Button type="submit" disabled={!form.formState.isDirty || !hasUpdateProviderAccess || isUpdating} isLoading={isUpdating}>
						Save Governance Configuration
					</Button>
				</div>
			</form>
		</Form>
	);
}