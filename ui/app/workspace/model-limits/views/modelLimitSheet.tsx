import { Alert, AlertDescription } from "@/components/ui/alert";
import BudgetUsageResetDialog from "@/components/ui/budgetUsageResetDialog";
import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Label } from "@/components/ui/label";
import { ALL_MODELS_OPTION, ModelSelector } from "@/components/ui/modelSelector";
import BudgetScopeBadge from "@/components/ui/budgetScopeBadge";
import MultiBudgetLines, { RequestLimitLines } from "@/components/ui/multibudgets";
import NumberAndSelect from "@/components/ui/numberAndSelect";
import { ProviderSelector } from "@/components/ui/providerSelector";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { DottedSeparator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { TokenLimitLabel } from "@/components/ui/tokenLimitLabel";
import { useBudgetUsageResetPrompt } from "@/hooks/useBudgetUsageResetPrompt";
import { hasDuplicateBudgetPeriods } from "@/lib/budgetOutline";
import { BUDGET_SCOPE_VALUES, resetDurationLabels, resetDurationOptions } from "@/lib/constants/governance";
import { ProviderLabels, ProviderName } from "@/lib/constants/logs";
import { getModelLimitScope, getModelLimitScopes } from "@/lib/registries/modelLimitScopes";
import { budgetSignature } from "@/lib/utils/governance";
import { saveHasNoLimit, shouldClearModelOnProviderChange } from "./modelLimitSheet.utils";
// Side-effect import: pulls in downstream scope registrations (e.g. enterprise
// registers "user" + user picker). The OSS-build fallback is an empty module.
import {
	getErrorMessage,
	useCreateModelConfigMutation,
	useDeleteModelConfigMutation,
	useGetModelConfigsQuery,
	useLazyGetModelsQuery,
	useUpdateModelConfigMutation,
} from "@/lib/store";
import { Budget, LimitScope, ModelConfig } from "@/lib/types/governance";
import {
	budgetScopeChargedBy,
	formatCurrency,
	isHolderLevelModelConfig,
	LimitTraffic,
	requestLimitLinesOf,
	requestLimitsOf,
	toRequestLimitRequests,
} from "@/lib/utils/governance";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import "@enterprise/lib/registrations/modelLimitScopes";
import { zodResolver } from "@hookform/resolvers/zod";
import { Lock } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";
import { useTranslation } from "react-i18next";
import i18n, { getLocale } from "@/lib/i18n";
import { msg } from "@/lib/i18n/validationMessage";

interface ModelLimitSheetProps {
	modelConfig?: ModelConfig | null;
	onSave: () => void;
	onCancel: () => void;
	/** The page the sheet edits for: "mcp" edits the MCP and Shared lines of a holder-level row. */
	traffic?: LimitTraffic;
}

const formSchema = z
	.object({
		modelName: z.string().min(1, { error: () => i18n.t("modelLimits.modelNameRequired", { ns: "models" }) }),
		provider: z.string().optional(),
		scope: z.string().optional(),
		scopeId: z.string().optional(),
		budgets: z
			.array(
				z.object({
					id: z.string().optional(),
					max_limit: z.number().nonnegative().optional(),
					reset_duration: z.string().optional(),
					reset_config: z.object({ quarter_start_month: z.number().int().min(1).max(12).optional() }).optional(),
					scope: z.enum(BUDGET_SCOPE_VALUES).optional(),
				}),
			)
			.optional(),
		tokenMaxLimit: z.number().int().nonnegative().optional(),
		tokenResetDuration: z.string().optional(),
		// One per reset period and scope; saving is blocked while two collide (the lines editor says why)
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
					scope: z.enum(BUDGET_SCOPE_VALUES).optional(),
				}),
			)
			.optional(),
	})
	.refine((data) => data.scope !== "virtual_key" || !!data.scopeId, {
		message: i18n.t("modelLimits.vkRequiredForScope", { ns: "models" }),
		path: ["scopeId"],
	});

type FormData = z.infer<typeof formSchema>;

// A limit with no provider applies across all of them; the form spells that absence as a
// sentinel so the control has something to show. Module level so its identity is stable.
const ALL_PROVIDERS_VALUE = "all";

// A limit covering every model on every provider is the holder's own (holder-level) row, the one MCP
// tool executions are charged by and the one an add looks up and saves onto.
const isHolderLevelSelection = (modelName: string | undefined, provider: string | undefined) => modelName === "*" && !provider;

// The scopes each page offers on a holder-level row. Lines of the other page's scope stay on the row
// untouched (see rowLines), so the two pages edit one set of limits without dropping each other's.
const SCOPE_OPTIONS: Record<LimitTraffic, readonly LimitScope[]> = { model: ["shared", "model"], mcp: ["shared", "mcp"] };

// The MCP page holds the deployment's and virtual keys' own limits only.
const MCP_LIMIT_SCOPES = ["global", "virtual_key"];

// The lines of a row a page shows (shown) or hides (!shown): a narrower row's lines are Shared or Model
// and all shown; on a holder-level row a page shows the lines its traffic is charged by and hides the
// others, which a save sends back unchanged.
const linesOn = <T extends { scope?: LimitScope }>(
	config: ModelConfig | null | undefined,
	lines: T[],
	traffic: LimitTraffic,
	shown: boolean,
): T[] => {
	if (!config || !isHolderLevelModelConfig(config)) return shown ? lines : [];
	return lines.filter((line) => budgetScopeChargedBy(line.scope, traffic) === shown);
};
const rowLines = (config: ModelConfig | null | undefined, traffic: LimitTraffic, shown: boolean): Budget[] =>
	linesOn(config, config?.budgets ?? [], traffic, shown);
const rowRequestLines = (config: ModelConfig | null | undefined, traffic: LimitTraffic, shown: boolean) =>
	linesOn(config, requestLimitLinesOf(config), traffic, shown);

// The request lines as saved, each with its scope. Duplicates are judged on these, as the editor does.
function effectiveRequestLimits(data: FormData) {
	return toRequestLimitRequests(data.requestLimits ?? []);
}

const toFormBudget = (b: Budget) => ({
	id: b.id,
	max_limit: b.max_limit,
	reset_duration: b.reset_duration,
	reset_config: b.reset_config,
	scope: b.scope,
});

export default function ModelLimitSheet({ modelConfig, onSave, onCancel, traffic = "model" }: ModelLimitSheetProps) {
	const isMCPPage = traffic === "mcp";
	// As in the limits table: only a holder-level row marks a rule, and only a Shared one; an unmarked
	// rule is this page's own traffic.
	const marksSharedRule = (scope?: LimitScope) =>
		!!modelConfig && isHolderLevelModelConfig(modelConfig) && (scope || "shared") === "shared";
	const { t } = useTranslation("models");
	const { t: tc } = useTranslation("common");
	const allProvidersOption = useMemo(() => ({ value: ALL_PROVIDERS_VALUE, label: t("modelLimits.allProviders") }), [t]);
	const allModelsOption = useMemo(() => [{ value: "*", label: tc("modelAccess.allModels") }], [tc]);
	const [isOpen, setIsOpen] = useState(true);
	const isEditing = !!modelConfig;
	// A readOnly-registered scope (e.g. enterprise's access_profile) is
	// system-generated: no field here is ever user-editable, and it must be
	// changed by editing its owner instead.
	const scopeEntry = getModelLimitScope(modelConfig?.scope || "global");
	const isManagedReadOnly = isEditing && scopeEntry?.readOnly === true;

	const hasCreateAccess = useRbac(RbacResource.Governance, RbacOperation.Create);
	const hasUpdateAccess = useRbac(RbacResource.Governance, RbacOperation.Update);
	const hasDeleteAccess = useRbac(RbacResource.Governance, RbacOperation.Delete);

	const handleClose = () => {
		setIsOpen(false);
		setTimeout(() => {
			onCancel();
		}, 150);
	};

	const [createModelConfig, { isLoading: isCreating }] = useCreateModelConfigMutation();
	// Defers the save until the operator says whether to clear accumulated spend.
	const resetPrompt = useBudgetUsageResetPrompt<FormData>();
	const [updateModelConfig, { isLoading: isUpdating }] = useUpdateModelConfigMutation();
	const [deleteModelConfig, { isLoading: isDeleting }] = useDeleteModelConfigMutation();
	const [getModels] = useLazyGetModelsQuery();
	const isLoading = isCreating || isUpdating || isDeleting;

	// Handle provider change - clear model if it doesn't exist for the new provider
	const handleProviderChange = async (newProvider: string, currentModel: string, onChange: (value: string) => void) => {
		onChange(newProvider);
		// "*" is provider-agnostic, so it needs no lookup and must survive the switch.
		if (!currentModel || currentModel === "*") return;

		try {
			const response = await getModels({
				provider: newProvider || undefined,
				query: currentModel,
				limit: 50,
			}).unwrap();

			if (
				shouldClearModelOnProviderChange(
					currentModel,
					response.models.map((model) => model.name),
				)
			) {
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
			// A new limit starts on all models and all providers (the holder-level row, the only one the MCP
			// page edits).
			modelName: modelConfig?.model_name || "*",
			provider: modelConfig?.provider || "",
			scope: modelConfig?.scope || "global",
			scopeId: modelConfig?.scope_id || "",
			budgets: rowLines(modelConfig, traffic, true).map(toFormBudget),
			tokenMaxLimit: modelConfig?.rate_limit?.token_max_limit ?? undefined,
			tokenResetDuration: modelConfig?.rate_limit?.token_reset_duration || "1h",
			requestLimits: rowRequestLines(modelConfig, traffic, true),
		},
	});

	const watchedBudgets = form.watch("budgets");
	// The token limit is the Models page's; the MCP page edits budgets and request limits only.
	const hasAnyLimit =
		(watchedBudgets?.some((b) => b.max_limit !== undefined && b.max_limit !== null) ?? false) ||
		(form.watch("requestLimits")?.some((l) => l.max_limit !== undefined && l.max_limit !== null) ?? false) ||
		(!isMCPPage && form.watch("tokenMaxLimit") !== undefined && form.watch("tokenMaxLimit") !== null);

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
				budgets: rowLines(modelConfig, traffic, true).map(toFormBudget),
				tokenMaxLimit: modelConfig.rate_limit?.token_max_limit ?? undefined,
				tokenResetDuration: modelConfig.rate_limit?.token_reset_duration || "1h",
				requestLimits: rowRequestLines(modelConfig, traffic, true),
			});
		}
	}, [modelConfig, form, traffic]);

	// Adding a holder-level limit (always so on the MCP page; on the Models page when all models and all
	// providers are picked) targets a holder that may already have that row (a key's own limits live
	// there), so the sheet looks it up and saves onto it rather than creating a second one.
	const watchedScope = form.watch("scope") || "global";
	const watchedScopeId = form.watch("scopeId");
	const addsHolderLevel = isMCPPage || isHolderLevelSelection(form.watch("modelName"), form.watch("provider"));
	const lookupTarget = !isEditing && addsHolderLevel && (watchedScope === "global" || !!watchedScopeId);
	// currentData belongs to the query now selected, so a lookup that failed or is still running for the
	// chosen target never leaves the previous target's row to be saved onto.
	const {
		currentData: targetRows,
		isFetching: isFetchingTarget,
		isError: targetLookupFailed,
		error: targetLookupError,
	} = useGetModelConfigsQuery(
		{ scope: watchedScope, scope_id: watchedScope === "global" ? undefined : watchedScopeId, search: "*", limit: 100 },
		{ skip: !lookupTarget },
	);
	const targetRow = useMemo(
		() => (lookupTarget ? (targetRows?.model_configs.find((mc) => isHolderLevelModelConfig(mc)) ?? null) : null),
		[lookupTarget, targetRows],
	);
	// The row being saved: the one edited, or the target's existing row when adding.
	const baseConfig = modelConfig ?? targetRow;

	// The permission a save needs is the one of the mutation it will run: an add onto the target's existing
	// row is an update, and an MCP page save that leaves nothing on the row deletes it.
	const remainingLines =
		(watchedBudgets ?? []).filter((b) => b.max_limit !== undefined && b.max_limit !== null).length +
		rowLines(baseConfig, traffic, false).length +
		toRequestLimitRequests(form.watch("requestLimits") ?? []).length +
		rowRequestLines(baseConfig, traffic, false).length;
	const deletesRow = isMCPPage && !!baseConfig && remainingLines === 0 && baseConfig.rate_limit?.token_max_limit == null;
	const canSubmit = deletesRow ? hasDeleteAccess : baseConfig ? hasUpdateAccess : hasCreateAccess;

	// The id-less request lines the last target put in the form (a legacy limit has no id).
	const loadedTargetRequestLines = useRef<ReturnType<typeof rowRequestLines>>([]);

	// Show the target row's lines beside any typed so far; picking another target swaps them.
	useEffect(() => {
		if (isEditing) return;
		const typed = (form.getValues("budgets") ?? []).filter((b) => !b.id);
		const existing = rowLines(targetRow, traffic, true).map(toFormBudget);
		if (existing.length > 0 || typed.length !== (form.getValues("budgets") ?? []).length) {
			form.setValue("budgets", [...existing, ...typed], { shouldDirty: true });
		}
		// A legacy request limit comes without an id, so the lines loaded from the previous target are
		// told apart from typed ones by what they say; they are replaced by the new target's lines.
		const loaded = loadedTargetRequestLines.current;
		const typedRequestLines = (form.getValues("requestLimits") ?? []).filter(
			(l) => !l.id && !loaded.some((p) => p.reset_duration === l.reset_duration && p.scope === l.scope && p.max_limit === l.max_limit),
		);
		const existingRequestLines = rowRequestLines(targetRow, traffic, true);
		loadedTargetRequestLines.current = existingRequestLines.filter((l) => !l.id);
		if (existingRequestLines.length > 0 || typedRequestLines.length !== (form.getValues("requestLimits") ?? []).length) {
			form.setValue("requestLimits", [...existingRequestLines, ...typedRequestLines], { shouldDirty: true });
		}
	}, [targetRow, isEditing, traffic, form]);

	// A budget config change on an existing limit is when clearing accumulated
	// spend becomes a meaningful choice; creating one has no usage to reset.
	// Compared without ids on purpose: form rows carry none while persisted rows do,
	// so including them would report a change on every save. The shared signature
	// folds in the fiscal quarter, which a limit-and-duration comparison misses -
	// moving Q1 from April to July reschedules the reset without touching either.
	// An Add onto the target's existing row changes that row, so it is compared the same way.
	const budgetsChanged = (data: FormData) => {
		const existing = modelConfig ?? targetRow;
		if (!existing) return false;
		const next = (data.budgets ?? [])
			.filter((b) => b.max_limit !== undefined && b.max_limit !== null)
			.map((b) => ({
				max_limit: b.max_limit,
				reset_duration: b.reset_duration,
				reset_config: b.reset_config,
				scope: b.scope,
			}));
		const current = rowLines(existing, traffic, true).map((b) => ({
			max_limit: b.max_limit ?? undefined,
			reset_duration: b.reset_duration,
			reset_config: b.reset_config,
			scope: b.scope,
		}));
		return budgetSignature(next) !== budgetSignature(current);
	};

	const onSubmit = async (data: FormData) => {
		if (!canSubmit) {
			toast.error(t("modelLimits.noPermission"));
			return;
		}

		if (budgetsChanged(data)) {
			resetPrompt.ask(data);
			return;
		}
		await saveModelLimit(data, false);
	};

	const saveModelLimit = async (data: FormData, resetBudgetUsage: boolean) => {
		// Removing every line of an MCP limit removes the limit (see below), so only adding one needs a line.
		// Lines the other page owns stay on the row, so a row that keeps one is not left empty.
		const keepsOtherPagesLines = rowLines(baseConfig, traffic, false).length > 0 || rowRequestLines(baseConfig, traffic, false).length > 0;
		if (saveHasNoLimit(hasAnyLimit, keepsOtherPagesLines, isMCPPage && isEditing)) {
			form.setError("root", { message: t("modelLimits.atLeastOneRequired") });
			return;
		}
		// Two lines on one reset period and scope are flagged by the lines editor; never send them.
		if (hasDuplicateBudgetPeriods(effectiveRequestLimits(data))) {
			return;
		}

		try {
			const provider = data.provider && data.provider.trim() !== "" ? data.provider : undefined;

			// Full desired set of budgets (kept lines with a max_limit), each with its scope. For updates
			// this is reconciled server-side; an empty array removes all budgets.
			// The lines this page hides on a holder-level row are sent back as they are, so saving here
			// never drops what the other page set.
			const budgetsPayload = [
				...(data.budgets ?? [])
					.filter((b) => b.max_limit !== undefined && b.max_limit !== null)
					.map((b) => ({
						id: b.id,
						max_limit: b.max_limit as number,
						reset_duration: b.reset_duration || "1M",
						reset_config: b.reset_config,
						scope: b.scope,
					})),
				...rowLines(baseConfig, traffic, false).map(toFormBudget),
			];
			// The full desired set of request limits, built the same way: [] removes every one.
			const requestLimitsPayload = [
				...effectiveRequestLimits(data),
				...toRequestLimitRequests(rowRequestLines(baseConfig, traffic, false)),
			];
			const tokenRateLimit =
				data.tokenMaxLimit !== undefined && data.tokenMaxLimit !== null
					? { token_max_limit: data.tokenMaxLimit, token_reset_duration: data.tokenResetDuration || "1h" }
					: undefined;

			if (
				isMCPPage &&
				baseConfig &&
				budgetsPayload.length === 0 &&
				requestLimitsPayload.length === 0 &&
				baseConfig.rate_limit?.token_max_limit == null
			) {
				// Nothing of the Models page's is left on the row either, so the row itself goes.
				await deleteModelConfig(baseConfig.id).unwrap();
				toast.success(t("modelLimits.deletedSuccess"));
			} else if (isMCPPage && baseConfig) {
				await updateModelConfig({
					id: baseConfig.id,
					data: { budgets: budgetsPayload, request_limits: requestLimitsPayload, reset_budget_usage: resetBudgetUsage || undefined },
				}).unwrap();
				toast.success(isEditing ? t("modelLimits.updatedSuccess") : t("modelLimits.createdSuccess"));
			} else if (isMCPPage) {
				await createModelConfig({
					model_name: "*",
					scope: data.scope || "global",
					scope_id: getModelLimitScope(data.scope || "global")?.PickerComponent ? data.scopeId : undefined,
					budgets: budgetsPayload,
					request_limits: requestLimitsPayload,
				}).unwrap();
				toast.success(t("modelLimits.createdSuccess"));
			} else if (baseConfig) {
				// The rate limit carries the token limit only; {} removes it. A legacy row's request fields are
				// replaced by request_limits on the backend. Adding onto the target's existing row keeps its
				// token limit unless one is entered here.
				const rateLimitPayload = tokenRateLimit ?? (isEditing && baseConfig.rate_limit ? {} : undefined);

				await updateModelConfig({
					id: baseConfig.id,
					data: {
						model_name: data.modelName,
						provider: provider,
						budgets: budgetsPayload,
						rate_limit: rateLimitPayload,
						request_limits: requestLimitsPayload,
						// Only sent when the operator explicitly chose to clear spend.
						reset_budget_usage: resetBudgetUsage || undefined,
					},
				}).unwrap();
				toast.success(isEditing ? t("modelLimits.updatedSuccess") : t("modelLimits.createdSuccess"));
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
					rate_limit: tokenRateLimit,
					request_limits: requestLimitsPayload.length > 0 ? requestLimitsPayload : undefined,
				}).unwrap();
				toast.success(t("modelLimits.createdSuccess"));
			}

			onSave();
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	if (isManagedReadOnly && modelConfig) {
		const budgets = modelConfig.budgets ?? (modelConfig.budget ? [modelConfig.budget] : []);
		return (
			<Sheet open={isOpen} onOpenChange={(open) => !open && handleClose()}>
				<SheetContent className="flex w-full flex-col overflow-x-hidden pt-4" data-testid="model-limit-sheet">
					<SheetHeader className="flex flex-col items-start p-0 px-4 py-4 md:px-8" headerClassName="mb-0 sticky -top-4 bg-card z-10">
						<SheetTitle>{t("modelLimits.viewLimit")}</SheetTitle>
						<SheetDescription>{t("modelLimits.managedDescription")}</SheetDescription>
					</SheetHeader>

					<div className="grow space-y-4 px-4 md:px-8">
						<Alert variant="info">
							<Lock className="h-4 w-4" />
							<AlertDescription>
								{scopeEntry?.ReadOnlyNotice ? (
									<scopeEntry.ReadOnlyNotice modelConfig={modelConfig} />
								) : (
									<p>{t("modelLimits.readOnlyFallback")}</p>
								)}
							</AlertDescription>
						</Alert>

						<div className="space-y-1">
							<Label className="text-muted-foreground text-xs font-normal">{t("modelLimits.provider")}</Label>
							<p className="text-sm">
								{modelConfig.provider
									? ProviderLabels[modelConfig.provider as ProviderName] || modelConfig.provider
									: t("modelLimits.allProviders")}
							</p>
						</div>
						<div className="space-y-1">
							<Label className="text-muted-foreground text-xs font-normal">{t("modelLimits.modelName")}</Label>
							<p className="text-sm">{modelConfig.model_name === "*" ? t("modelLimits.allModels") : modelConfig.model_name}</p>
						</div>
						<div className="space-y-1">
							<Label className="text-muted-foreground text-xs font-normal">{t("modelLimits.scope")}</Label>
							<p className="text-sm">{scopeEntry?.displayAsScope ?? scopeEntry?.label}</p>
						</div>
						{modelConfig.scope_name ? (
							<div className="space-y-1">
								<Label className="text-muted-foreground text-xs font-normal">{t("modelLimits.target")}</Label>
								<p className="text-sm">{modelConfig.scope_name}</p>
							</div>
						) : null}
						{scopeEntry?.ManagedByComponent ? (
							<div className="space-y-1">
								<Label className="text-muted-foreground text-xs font-normal">{t("modelLimits.managedBy")}</Label>
								<scopeEntry.ManagedByComponent modelConfig={modelConfig} labelled />
							</div>
						) : null}

						<DottedSeparator />

						<div className="space-y-3">
							<Label className="text-sm font-medium">{t("modelLimits.budgetSection")}</Label>
							{budgets.length > 0 ? (
								<div className="space-y-2">
									{budgets.map((b) => (
										<div key={b.id} className="bg-muted/50 rounded-lg p-3 text-sm">
											<p className="font-medium">
												{formatCurrency(b.current_usage)} / {formatCurrency(b.max_limit)}
											</p>
											<p className="text-muted-foreground flex items-center gap-1.5 text-xs">
												{t("modelLimits.resets", {
													period: resetDurationLabels[b.reset_duration] || b.reset_duration,
												})}
												<BudgetScopeBadge scope={b.scope} />
											</p>
										</div>
									))}
								</div>
							) : (
								<p className="text-muted-foreground text-sm">{t("modelLimits.noBudgetConfigured")}</p>
							)}
						</div>

						<DottedSeparator />

						<div className="space-y-3">
							<Label className="text-sm font-medium">{t("modelLimits.rateLimitsSection")}</Label>
							{modelConfig.rate_limit?.token_max_limit != null || requestLimitsOf(modelConfig).length > 0 ? (
								<div className="bg-muted/50 grid grid-cols-1 gap-4 rounded-lg p-4 md:grid-cols-2">
									{modelConfig.rate_limit?.token_max_limit != null ? (
										<div className="space-y-1">
											<p className="text-muted-foreground text-xs">{t("modelLimits.tokens")}</p>
											<p className="text-sm font-medium">
												{modelConfig.rate_limit.token_current_usage.toLocaleString(getLocale())} /{" "}
												{modelConfig.rate_limit.token_max_limit.toLocaleString(getLocale())} (
												{resetDurationLabels[modelConfig.rate_limit.token_reset_duration || "1h"] ||
													modelConfig.rate_limit.token_reset_duration}
												)
											</p>
										</div>
									) : null}
									{requestLimitsOf(modelConfig).map((rule) => (
										<div key={rule.id} className="space-y-1">
											<p className="text-muted-foreground flex items-center gap-1.5 text-xs">
												{t("modelLimits.requests")}
												{marksSharedRule(rule.scope) ? <BudgetScopeBadge scope="shared" /> : null}
											</p>
											<p className="text-sm font-medium">
												{rule.request_current_usage.toLocaleString(getLocale())} /{" "}
												{(rule.request_max_limit ?? 0).toLocaleString(getLocale())} (
												{resetDurationLabels[rule.request_reset_duration || "1h"] || rule.request_reset_duration})
											</p>
										</div>
									))}
								</div>
							) : (
								<p className="text-muted-foreground text-sm">{t("modelLimits.noRateLimitsConfigured")}</p>
							)}
						</div>
					</div>

					<div className="bg-card sticky bottom-0 shrink-0 border-t px-4 py-4 md:px-8">
						<div className="flex items-center justify-end">
							<Button type="button" variant="outline" onClick={handleClose}>
								{tc("close")}
							</Button>
						</div>
					</div>
				</SheetContent>
			</Sheet>
		);
	}

	const sheetDescription = {
		mcp: { edit: t("mcpLimits.editDescription"), create: t("mcpLimits.createDescription") },
		models: { edit: t("modelLimits.editDescriptionShort"), create: t("modelLimits.createDescriptionShort") },
	};

	return (
		<Sheet open={isOpen} onOpenChange={(open) => !open && handleClose()}>
			<SheetContent
				className="flex w-full flex-col overflow-x-hidden pt-4"
				onInteractOutside={(e) => {
					if (form.formState.isDirty) e.preventDefault();
				}}
				onEscapeKeyDown={(e) => {
					if (form.formState.isDirty) e.preventDefault();
				}}
				data-testid="model-limit-sheet"
			>
				<SheetHeader className="flex flex-col items-start p-0 py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10 px-4 md:px-8">
					<SheetTitle>{isEditing ? t("modelLimits.editLimit") : t("modelLimits.createLimit")}</SheetTitle>
					<SheetDescription>{sheetDescription[isMCPPage ? "mcp" : "models"][isEditing ? "edit" : "create"]}</SheetDescription>
				</SheetHeader>

				<Form {...form}>
					<form onSubmit={form.handleSubmit(onSubmit)} className="flex h-full flex-col gap-6">
						<div className="grow space-y-4 px-4 md:px-8">
							{/* Provider and model: the MCP page edits holder-level rows, which name neither */}
							{!isMCPPage && (
								<>
									{/* Provider */}
									<FormField
										control={form.control}
										name="provider"
										render={({ field }) => (
											<FormItem>
												<FormLabel>{t("modelLimits.provider")}</FormLabel>
												<FormControl>
													<ProviderSelector
														data-testid="model-limit-provider-select"
														allOption={allProvidersOption}
														value={field.value || ALL_PROVIDERS_VALUE}
														onChange={(value: string) =>
															handleProviderChange(value === ALL_PROVIDERS_VALUE ? "" : value, form.getValues("modelName"), field.onChange)
														}
														disabled={isEditing}
													/>
												</FormControl>
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
												<FormLabel>{t("modelLimits.modelName")}</FormLabel>
												<FormControl>
													{isEditing ? (
														<Select value={field.value} disabled>
															<SelectTrigger className="w-full" data-testid="model-limit-model-select">
																<SelectValue />
															</SelectTrigger>
															<SelectContent>
																<SelectItem value={field.value}>
																	{field.value === "*" ? t("modelLimits.allModels") : field.value}
																</SelectItem>
															</SelectContent>
														</Select>
													) : (
														<div data-testid="model-limit-model-select">
															<ModelSelector
																provider={form.watch("provider") || undefined}
																value={field.value}
																onChange={field.onChange}
																placeholder={t("modelLimits.searchForModel")}
																baseModelsWithoutProvider
																extraOptions={allModelsOption}
																allowCustomModel
																unfiltered
															/>
														</div>
													)}
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
								</>
							)}

							{/* Scope */}
							<FormField
								control={form.control}
								name="scope"
								render={({ field }) => (
									<FormItem>
										<FormLabel>{t("modelLimits.scope")}</FormLabel>
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
													<SelectValue placeholder={t("modelLimits.global")} />
												</SelectTrigger>
											</FormControl>
											<SelectContent>
												{getModelLimitScopes()
													// Always keep the currently selected option in the list, even if it's
													// readOnly/non-creatable — otherwise editing an existing row whose scope
													// isn't creatable (e.g. a legacy scope=user row) has no matching
													// SelectItem for its value, and the trigger can render blank.
													.filter((option) => option.value === field.value || (!option.readOnly && option.creatable !== false))
													.filter((option) => !isMCPPage || MCP_LIMIT_SCOPES.includes(option.value))
													.map((option) => (
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
									label={t("modelLimits.budgetSection")}
									lines={(form.watch("budgets") ?? []).map((b) => ({
										id: b.id,
										max_limit: b.max_limit,
										reset_duration: b.reset_duration ?? "1M",
										reset_config: b.reset_config,
										scope: b.scope,
									}))}
									onChange={(lines) => form.setValue("budgets", lines, { shouldDirty: true })}
									showScope
									scopeOptions={SCOPE_OPTIONS[traffic]}
									defaultScope={isMCPPage ? "mcp" : undefined}
								/>
							</div>

							<DottedSeparator />

							{/* Rate Limiting Configuration: request limits, one line per reset period and scope, then the
							    token limit. MCP tool executions are counted as requests and carry no tokens, so the MCP
							    page has no token limit to set. */}
							<div className="space-y-5">
								<Label className="text-sm font-medium">{t("modelLimits.rateLimitsSection")}</Label>
								<RequestLimitLines
									data-testid="model-limit-request-limit-lines"
									lines={form.watch("requestLimits") ?? []}
									onChange={(lines) => form.setValue("requestLimits", lines, { shouldDirty: true })}
									showScope
									scopeOptions={SCOPE_OPTIONS[traffic]}
									defaultScope={isMCPPage ? "mcp" : undefined}
								/>
								{!isMCPPage && (
									<FormField
										control={form.control}
										name="tokenMaxLimit"
										render={({ field }) => (
											<FormItem>
												<NumberAndSelect
													id="modelTokenMaxLimit"
													labelClassName="font-normal"
													label={<TokenLimitLabel label={t("modelLimits.maximumTokens")} data-testid="model-limit-token-limit-info" />}
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
								)}
							</div>
							{form.formState.errors.root && <p className="text-destructive text-sm">{form.formState.errors.root.message}</p>}

							{/* Current Usage Display (for editing) */}
							{isEditing &&
								(rowLines(modelConfig, traffic, true).length > 0 ||
									rowRequestLines(modelConfig, traffic, true).length > 0 ||
									(!isMCPPage && modelConfig?.rate_limit?.token_max_limit)) && (
									<>
										<DottedSeparator />
										<div className="space-y-3">
											<Label className="text-sm font-medium">{t("modelLimits.currentUsage")}</Label>
											<div className="bg-muted/50 grid grid-cols-1 gap-4 rounded-lg p-4 md:grid-cols-2">
												{rowLines(modelConfig, traffic, true).map((b) => (
													<div key={b.id} className="space-y-1">
														<p className="text-muted-foreground flex items-center gap-1.5 text-xs">
															{t("modelLimits.budgetWithDuration", { duration: b.reset_duration })}
															<BudgetScopeBadge scope={b.scope} />
														</p>
														<p className="text-sm font-medium">
															${b.current_usage.toFixed(2)} / ${b.max_limit.toFixed(2)}
														</p>
													</div>
												))}
												{!isMCPPage && modelConfig?.rate_limit?.token_max_limit && (
													<div className="space-y-1">
														<p className="text-muted-foreground text-xs">{t("modelLimits.tokens")}</p>
														<p className="text-sm font-medium">
															{modelConfig.rate_limit.token_current_usage.toLocaleString(getLocale())} /{" "}
															{modelConfig.rate_limit.token_max_limit.toLocaleString(getLocale())}
														</p>
													</div>
												)}
												{linesOn(modelConfig, requestLimitsOf(modelConfig), traffic, true).map((rule) => (
													<div key={rule.id} className="space-y-1" data-testid="model-limit-request-limit-usage">
														<p className="text-muted-foreground flex items-center gap-1.5 text-xs">
															{t("modelLimits.requests")}
															{rule.request_reset_duration
																? ` · ${resetDurationLabels[rule.request_reset_duration] || rule.request_reset_duration}`
																: null}
															{marksSharedRule(rule.scope) ? <BudgetScopeBadge scope="shared" /> : null}
														</p>
														<p className="text-sm font-medium">
															{rule.request_current_usage.toLocaleString(getLocale())} /{" "}
															{(rule.request_max_limit ?? 0).toLocaleString(getLocale())}
														</p>
													</div>
												))}
											</div>
										</div>
									</>
								)}
						</div>

						{lookupTarget && targetLookupFailed && (
							<p className="text-destructive px-4 text-sm md:px-8" data-testid="model-limit-target-lookup-error">
								{getErrorMessage(targetLookupError)}
							</p>
						)}

						{/* Footer */}
						<div className="bg-card sticky bottom-0 shrink-0 border-t px-4 py-4 md:px-8">
							<div className="flex items-center justify-end gap-3">
								{!canSubmit && <p className="text-destructive text-sm">{t("modelLimits.noPermission")}</p>}
								<Button type="button" variant="outline" onClick={handleClose}>
									{tc("cancel")}
								</Button>
								<Button
									type="submit"
									data-testid="model-limit-button-submit"
									disabled={isLoading || isFetchingTarget || targetLookupFailed || !form.formState.isDirty || !canSubmit}
								>
									{isLoading ? t("modelLimits.saving") : isEditing ? t("modelLimits.saveChanges") : t("modelLimits.createLimit")}
								</Button>
							</div>
						</div>
					</form>
				</Form>
				<BudgetUsageResetDialog
					data-testid="model-limit-budget-reset-dialog"
					ownerLabel="limit"
					open={resetPrompt.isOpen}
					onOpenChange={resetPrompt.setOpen}
					onChoice={(resetUsage) => resetPrompt.resolve((data) => saveModelLimit(data, resetUsage))}
				/>
			</SheetContent>
		</Sheet>
	);
}