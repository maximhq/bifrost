import { CalendarAlignmentNotice } from "@/components/calendarAlignmentNotice";
import { calendarWindowDurations, isMultiUnitDuration } from "@/lib/constants/governance";
import { Button } from "@/components/ui/button";
import { Form } from "@/components/ui/form";
import { Label } from "@/components/ui/label";
import BudgetUsageResetDialog from "@/components/ui/budgetUsageResetDialog";
import { useBudgetUsageResetPrompt } from "@/hooks/useBudgetUsageResetPrompt";
import MultiBudgetLines, { BudgetLineEntry, RequestLimitLines } from "@/components/ui/multibudgets";
import NumberAndSelect from "@/components/ui/numberAndSelect";
import { TokenLimitLabel } from "@/components/ui/tokenLimitLabel";
import { hasDuplicateBudgetPeriods } from "@/lib/budgetOutline";
import { DottedSeparator } from "@/components/ui/separator";
import { Switch } from "@/components/ui/switch";
import { resetDurationLabels, supportsCalendarAlignment } from "@/lib/constants/governance";
import {
	getErrorMessage,
	useDeleteProviderGovernanceMutation,
	useGetProviderGovernanceQuery,
	useUpdateProviderGovernanceMutation,
} from "@/lib/store";
import { ModelProvider } from "@/lib/types/config";
import { CreateBudgetRequest, ProviderGovernance } from "@/lib/types/governance";
import { budgetSignature, requestLimitLinesOf, requestLimitsOf, toRequestLimitRequests } from "@/lib/utils/governance";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { z } from "zod";
import i18n, { getLocale } from "@/lib/i18n";
import { msg } from "@/lib/i18n/validationMessage";

interface GovernanceFormFragmentProps {
	provider: ModelProvider;
}

const budgetLineSchema = z.object({
	id: z.string().optional(),
	max_limit: z
		.number({ error: () => i18n.t("providers.governanceForm.budgetLimitNumber", { ns: "models" }) })
		.nonnegative({ error: () => i18n.t("providers.governanceForm.budgetLimitNonnegative", { ns: "models" }) })
		.optional(),
	reset_duration: z.string().min(1, { error: () => i18n.t("providers.governanceForm.resetDurationRequired", { ns: "models" }) }),
	reset_config: z.object({ quarter_start_month: z.number().optional() }).optional(),
});

const formSchema = z.object({
	budgets: z.array(budgetLineSchema),
	calendarAligned: z.boolean(),
	tokenMaxLimit: z.number().int().nonnegative().optional(),
	tokenResetDuration: z.string().optional(),
	// Shared request limits, one per reset period; saving is blocked while two collide (the lines editor says why)
	requestLimits: z
		.array(
			z.object({
				id: z.string().optional(),
				max_limit: z
					.number()
					.int(msg("validation.valueMustBeAPositiveWholeNumber"))
					.positive(msg("validation.valueMustBeAPositiveWholeNumber"))
					.optional(),
				reset_duration: z.string(),
			}),
		)
		// z.object drops scope, so every line is compared as shared, which is all this tier allows.
		.refine((lines) => !hasDuplicateBudgetPeriods(lines)),
});

type FormData = z.infer<typeof formSchema>;

const DEFAULT_GOVERNANCE_FORM_VALUES: FormData = {
	budgets: [],
	calendarAligned: false,
	tokenMaxLimit: undefined,
	tokenResetDuration: "1h",
	requestLimits: [],
};

function governanceToFormValues(provGov: ProviderGovernance | undefined): FormData {
	if (!provGov) return DEFAULT_GOVERNANCE_FORM_VALUES;
	return {
		budgets: (provGov.budgets ?? []).map((b) => ({
			id: b.id,
			max_limit: b.max_limit,
			reset_duration: b.reset_duration,
			reset_config: b.reset_config,
		})),
		calendarAligned: provGov.calendar_aligned ?? false,
		tokenMaxLimit: provGov.rate_limit?.token_max_limit ?? undefined,
		tokenResetDuration: provGov.rate_limit?.token_reset_duration || "1h",
		requestLimits: requestLimitLinesOf(provGov),
	};
}

export function GovernanceFormFragment({ provider }: GovernanceFormFragmentProps) {
	const { t } = useTranslation("models");
	const hasUpdateProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	// Defers the save until the operator says whether to clear accumulated spend.
	const resetPrompt = useBudgetUsageResetPrompt<FormData>();
	const hasViewAccess = useRbac(RbacResource.Governance, RbacOperation.View);

	const { data: providerGovernanceData } = useGetProviderGovernanceQuery(undefined, {
		skip: !hasViewAccess,
		pollingInterval: 5000,
	});
	const [updateProviderGovernance, { isLoading: isUpdating }] = useUpdateProviderGovernanceMutation();
	const [deleteProviderGovernance, { isLoading: isDeleting }] = useDeleteProviderGovernanceMutation();

	const providerGovernance = providerGovernanceData?.providers?.find((p) => p.provider === provider.name);
	const hasExistingGovernance = !!(
		(providerGovernance?.budgets?.length ?? 0) > 0 ||
		providerGovernance?.rate_limit ||
		(providerGovernance?.request_limits?.length ?? 0) > 0
	);

	const form = useForm<FormData>({
		resolver: zodResolver(formSchema),
		defaultValues: DEFAULT_GOVERNANCE_FORM_VALUES,
	});

	const watchedBudgets = form.watch("budgets");
	const watchedCalendarAligned = form.watch("calendarAligned");
	const configuredBudgets = watchedBudgets.filter((b) => b.max_limit !== undefined && b.max_limit > 0);
	const showCalendarAlignment = configuredBudgets.some(
		(b) => supportsCalendarAlignment(b.reset_duration) || isMultiUnitDuration(b.reset_duration),
	);

	useEffect(() => {
		if (providerGovernance && !form.formState.isDirty) {
			form.reset(governanceToFormValues(providerGovernance));
		}
	}, [providerGovernance, form]);

	useEffect(() => {
		if (form.formState.isDirty) return;
		const newProvGov = providerGovernanceData?.providers?.find((p) => p.provider === provider.name);
		form.reset(governanceToFormValues(newProvGov));
	}, [provider.name, form]);

	// Drop a stale calendarAligned when no configured budget supports alignment, so the
	// toggle never reappears pre-enabled if an alignable budget is added back later.
	useEffect(() => {
		if (!showCalendarAlignment && watchedCalendarAligned) {
			form.setValue("calendarAligned", false, { shouldDirty: true });
		}
	}, [showCalendarAlignment, watchedCalendarAligned, form]);

	// A budget config change on existing provider governance is when clearing
	// accumulated spend becomes a meaningful choice.
	const budgetsChanged = (data: FormData) => {
		const existing = providerGovernance?.budgets ?? [];
		if (existing.length === 0) return false;
		const next = data.budgets.filter((b) => b.max_limit !== undefined && b.max_limit > 0);
		return budgetSignature(next) !== budgetSignature(existing);
	};

	const onSubmit = async (data: FormData) => {
		if (budgetsChanged(data)) {
			resetPrompt.ask(data);
			return;
		}
		await saveGovernance(data, false);
	};

	const saveGovernance = async (data: FormData, resetBudgetUsage: boolean) => {
		try {
			const validBudgets = data.budgets.filter((b) => b.max_limit !== undefined && b.max_limit > 0);
			const hasAlignableBudget = validBudgets.some(
				(b) => supportsCalendarAlignment(b.reset_duration) || isMultiUnitDuration(b.reset_duration),
			);
			const hadBudgets = (providerGovernance?.budgets?.length ?? 0) > 0;
			const hadRateLimit = !!providerGovernance?.rate_limit;

			let budgetsPayload: CreateBudgetRequest[] | undefined;
			if (validBudgets.length > 0) {
				budgetsPayload = validBudgets.map((b) => ({
					id: b.id,
					max_limit: b.max_limit!,
					reset_duration: b.reset_duration,
					reset_config: b.reset_config,
				}));
			} else if (hadBudgets) {
				budgetsPayload = [];
			}

			// The rate limit carries the token limit only; {} removes it. A legacy row's request fields are
			// replaced by request_limits on the backend.
			const rateLimitPayload =
				data.tokenMaxLimit !== undefined
					? { token_max_limit: data.tokenMaxLimit, token_reset_duration: data.tokenResetDuration || "1h" }
					: hadRateLimit
						? {}
						: undefined;

			await updateProviderGovernance({
				provider: provider.name,
				data: {
					budgets: budgetsPayload,
					...(budgetsPayload !== undefined ? { calendar_aligned: hasAlignableBudget && data.calendarAligned } : {}),
					rate_limit: rateLimitPayload,
					// The full desired set: [] removes every request limit.
					request_limits: toRequestLimitRequests(data.requestLimits),
					// Only sent when the operator explicitly chose to clear spend.
					reset_budget_usage: resetBudgetUsage || undefined,
				},
			}).unwrap();

			toast.success(t("providers.governanceSaved"));
			form.reset(data);
		} catch (error) {
			toast.error(t("providers.failedUpdateGovernance"), {
				description: getErrorMessage(error),
			});
		}
	};

	const handleDelete = async () => {
		try {
			await deleteProviderGovernance(provider.name).unwrap();
			toast.success(t("providers.governanceRemoved"));
			form.reset(DEFAULT_GOVERNANCE_FORM_VALUES);
		} catch (error) {
			toast.error(t("providers.failedRemoveGovernance"), {
				description: getErrorMessage(error),
			});
		}
	};

	return (
		<Form {...form}>
			<form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6 px-4 md:px-6">
				{/* Budget Configuration */}
				<MultiBudgetLines
					data-testid="provider-governance-budgets"
					lines={watchedBudgets as BudgetLineEntry[]}
					onChange={(lines) => form.setValue("budgets", lines, { shouldDirty: true })}
				/>

				{/* Calendar Alignment — only shown when a budget uses a calendar-alignable period (day/week/month/year) */}
				<CalendarAlignmentNotice enabled={watchedCalendarAligned} durations={calendarWindowDurations([{ budgets: configuredBudgets }])} />
				{showCalendarAlignment && (
					<div className="flex items-center justify-between gap-4">
						<div className="space-y-1">
							<Label className="text-sm" htmlFor="provider-calendar-aligned">
								{t("providers.alignCalendarCycle")}
							</Label>
							<p className="text-muted-foreground text-xs">{t("providers.alignCalendarCycleHelp")}</p>
						</div>
						<Switch
							id="provider-calendar-aligned"
							data-testid="provider-governance-calendar-aligned-switch"
							checked={watchedCalendarAligned}
							onCheckedChange={(checked) => form.setValue("calendarAligned", checked, { shouldDirty: true })}
						/>
					</div>
				)}

				<DottedSeparator />

				{/* Rate Limiting Configuration: request limits, shared by every request to this provider and one
				    per reset period, then the token limit */}
				<div className="space-y-5">
					<Label className="text-sm font-medium">{t("providers.rateLimitingConfiguration")}</Label>
					<RequestLimitLines
						data-testid="provider-governance-request-limits"
						lines={form.watch("requestLimits")}
						onChange={(lines) => form.setValue("requestLimits", lines, { shouldDirty: true })}
					/>
					<NumberAndSelect
						id="providerTokenMaxLimit"
						labelClassName="font-normal"
						label={<TokenLimitLabel label={t("modelLimits.maximumTokens")} data-testid="provider-governance-token-limit-info" />}
						value={form.watch("tokenMaxLimit")}
						selectValue={form.watch("tokenResetDuration") || "1h"}
						onChangeNumber={(value) => form.setValue("tokenMaxLimit", value, { shouldDirty: true })}
						onChangeSelect={(value) => form.setValue("tokenResetDuration", value, { shouldDirty: true })}
					/>
				</div>

				{/* Current Usage — only shown when governance exists */}
				{hasExistingGovernance && (
					<>
						<DottedSeparator />
						<div className="space-y-4">
							<Label className="text-sm font-medium">{t("providers.governanceForm.currentUsage")}</Label>
							<div className="bg-muted/50 grid grid-cols-1 gap-4 rounded-lg p-4 md:grid-cols-2">
								{providerGovernance?.budgets?.map((b) => (
									<div key={b.id} className="space-y-1">
										<p className="text-muted-foreground text-xs">{t("providers.budgetParenDuration", { duration: b.reset_duration })}</p>
										<p className="text-sm font-medium">
											${b.current_usage.toFixed(2)} / ${b.max_limit.toFixed(2)}
										</p>
									</div>
								))}
								{providerGovernance?.rate_limit?.token_max_limit && (
									<div className="space-y-1">
										<p className="text-muted-foreground text-xs">{t("providers.tokenUsage")}</p>
										<p className="text-sm font-medium">
											{providerGovernance.rate_limit.token_current_usage.toLocaleString(getLocale())} /{" "}
											{providerGovernance.rate_limit.token_max_limit.toLocaleString(getLocale())}
										</p>
									</div>
								)}
								{requestLimitsOf(providerGovernance).map((rule) => (
									<div key={rule.id} className="space-y-1" data-testid="provider-governance-request-limit-usage">
										<p className="text-muted-foreground text-xs">
											{t("providers.requestUsage")}
											{rule.request_reset_duration
												? ` · ${resetDurationLabels[rule.request_reset_duration] || rule.request_reset_duration}`
												: null}
										</p>
										<p className="text-sm font-medium">
											{rule.request_current_usage.toLocaleString(getLocale())} / {(rule.request_max_limit ?? 0).toLocaleString(getLocale())}
										</p>
									</div>
								))}
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
						{t("providers.removeConfiguration")}
					</Button>
					<Button type="submit" disabled={!form.formState.isDirty || !hasUpdateProviderAccess || isUpdating} isLoading={isUpdating}>
						{t("providers.saveGovernanceConfig")}
					</Button>
				</div>
			</form>
			<BudgetUsageResetDialog
				data-testid="provider-governance-budget-reset-dialog"
				ownerLabel="provider"
				open={resetPrompt.isOpen}
				onOpenChange={resetPrompt.setOpen}
				onChoice={(resetUsage) => resetPrompt.resolve((data) => saveGovernance(data, resetUsage))}
			/>
		</Form>
	);
}