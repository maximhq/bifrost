import { Button } from "@/components/ui/button";
import { Form, FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { getErrorMessage } from "@/lib/store";
import { useSetModelRouterRoutingMutation, useUpdateModelRouterMutation } from "@/lib/store/apis/governanceApi";
import { useGetProvidersQuery } from "@/lib/store/apis/providersApi";
import { ModelRouter } from "@/lib/types/complexityRouter";
import { RoutingRuleScope } from "@/lib/types/routingRules";
import { modelRouterSheetSchema, type ModelRouterSheetFormSchema } from "@/lib/types/schemas";
import { validateRateLimitAndBudgetRules, validateRoutingRules } from "@/lib/utils/celConverterRouting";
import { normalizeRoutingRuleGroupQuery } from "@/lib/utils/routingRuleGroupQuery";
import { zodResolver } from "@hookform/resolvers/zod";
import { LoaderCircle } from "lucide-react";
import { useCallback, useMemo, useState } from "react";
import { useForm, useWatch } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { RuleGroupType } from "react-querybuilder";
import { toast } from "sonner";
import { ConditionBuilder, type ConditionMode, initialConditionMode, ScopeEntityField, ScopeSelect } from "./formParts";
import { RouterIdentityFields } from "./routerIdentityFields";
import {
	hasRouterSheetChanges,
	type RouterCondition,
	routerSheetChanges,
	routerSheetDefaults,
	withSavedRouting,
} from "./routerSheet.utils";

interface RouterSheetProps {
	router: ModelRouter;
	onClose: () => void;
	// Opens with the switch on, for the "Turn on" button: the save that follows
	// turns the router on once its routing has been looked over.
	turnOn?: boolean;
	// Called after a save, with whether it turned the router on.
	onSaved?: (result: { turnedOn: boolean }) => void;
}

// RouterSheet edits what a router is and when it runs: its name and
// description, whether it is on, and its routing (scope, priority and
// condition), laid out like the routing rule sheet. Its classifier and its
// models are larger and keep their own tabs. Mount it only while it is open,
// so each opening starts from the saved router.
//
// Edits are compared with a baseline kept in state: the router as it was when
// the sheet opened, not the router prop, which is live query data and may
// refresh while the sheet is open (comparing with it would send untouched
// fields back over a change saved elsewhere meanwhile).
//
// A save sends only what changed: the routing first, on its own endpoint,
// since a taken priority or a bad condition is refused there and is shown
// under its field; then the name and description. A name is checked before
// anything is sent, so a save rarely stops between the two; if it does, the
// sheet stays open with the error, the routing already saved, and that
// routing taken into the baseline, so a field switched back before the retry
// is sent as the reversal it is. The fields are disabled while a save runs,
// so nothing edited then is lost when the sheet closes after it.
export function RouterSheet({ router, onClose, turnOn = false, onSaved }: RouterSheetProps) {
	const { t } = useTranslation("models");
	const { t: tc } = useTranslation("common");
	const { t: tmr } = useTranslation("models", { keyPrefix: "routing.modelRouters" });
	const { t: trt } = useTranslation("models", { keyPrefix: "routing.modelRouters.routing" });
	const [setRouting] = useSetModelRouterRoutingMutation();
	const [updateRouter] = useUpdateModelRouterMutation();
	const { data: providersData = [] } = useGetProvidersQuery();
	const availableProviders = useMemo(() => providersData.map((provider) => provider.name), [providersData]);

	// The router as the sheet opened: the form and the condition editor start
	// from it. The baseline starts there and takes in each routing save.
	const [opened] = useState(router);
	const [openedMode] = useState<ConditionMode>(() => initialConditionMode(opened.routing));
	const [baseline, setBaseline] = useState(opened);
	const [baselineMode, setBaselineMode] = useState(openedMode);
	const form = useForm<ModelRouterSheetFormSchema>({
		resolver: zodResolver(modelRouterSheetSchema),
		defaultValues: routerSheetDefaults(opened, turnOn),
	});
	const values = useWatch({ control: form.control }) as ModelRouterSheetFormSchema;
	const [condition, setCondition] = useState<RouterCondition>(() => ({
		mode: openedMode,
		celExpression: opened.routing.cel_expression ?? "",
		query: normalizeRoutingRuleGroupQuery(opened.routing.query),
	}));
	const [celError, setCelError] = useState<string | null>(null);
	const [submitError, setSubmitError] = useState<string | null>(null);
	const [isSaving, setIsSaving] = useState(false);

	const changes = routerSheetChanges(baseline, values, condition, baselineMode);
	const dirty = hasRouterSheetChanges(changes);

	const handleQueryChange = useCallback((expression: string, query: RuleGroupType) => {
		setCondition((current) => ({ ...current, celExpression: expression, query }));
		setCelError(null);
	}, []);

	const save = form.handleSubmit(async (checked) => {
		setSubmitError(null);
		// Builder-only checks: they read the visual query, which CEL mode does
		// not have. A typed CEL expression is checked by the gateway on save.
		// These are toasts, as in the routing rule sheet.
		if (condition.mode === "builder") {
			const regexErrors = validateRoutingRules(condition.query);
			if (regexErrors.length > 0) {
				toast.error(t("routing.invalidRegex", { errors: regexErrors.join("\n") }));
				return;
			}
			const ruleErrors = validateRateLimitAndBudgetRules(condition.query);
			if (ruleErrors.length > 0) {
				toast.error(t("routing.invalidRuleConfig", { errors: ruleErrors.join("\n") }));
				return;
			}
		}
		const toSend = routerSheetChanges(baseline, checked, condition, baselineMode);
		setIsSaving(true);
		try {
			if (toSend.routing) {
				try {
					await setRouting({ id: router.id, routing: toSend.routing }).unwrap();
					const sent = toSend.routing;
					setBaseline((current) => withSavedRouting(current, sent));
					if (sent.cel_expression !== undefined) setBaselineMode(condition.mode);
				} catch (err) {
					const message = getErrorMessage(err);
					// A malformed CEL expression shows under the CEL editor, which only
					// CEL mode has; a taken priority under the priority field.
					if (condition.mode === "cel" && /cel expression/i.test(message)) setCelError(message);
					else if (/priority/i.test(message)) form.setError("priority", { message });
					else setSubmitError(tmr("sheet.saveFailed", { error: message }));
					return;
				}
			}
			if (toSend.identity) {
				try {
					await updateRouter({ id: router.id, ...toSend.identity }).unwrap();
				} catch (err) {
					const message = getErrorMessage(err);
					if (/name/i.test(message)) form.setError("name", { message });
					else setSubmitError(tmr("sheet.saveFailed", { error: message }));
					return;
				}
			}
			const turnedOn = toSend.routing?.enabled === true;
			const name = toSend.identity?.name ?? router.name;
			toast.success(turnedOn ? tmr("turnedOn", { name }) : tmr("sheet.saved"), { position: "top-right" });
			onSaved?.({ turnedOn });
			onClose();
		} finally {
			setIsSaving(false);
		}
	});

	return (
		<Sheet open onOpenChange={(open) => !open && !isSaving && onClose()}>
			<SheetContent className="flex w-full min-w-1/2 flex-col gap-4 overflow-x-hidden p-0 pt-4" data-testid="model-router-sheet">
				<SheetHeader className="flex flex-col items-start py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10 px-4 md:px-8">
					<SheetTitle>{tmr("sheet.title")}</SheetTitle>
					<SheetDescription>{tmr("sheet.description")}</SheetDescription>
				</SheetHeader>

				<Form {...form}>
					<form onSubmit={save} className="flex grow flex-col" noValidate>
						{/* Disabled while a save runs: the sheet closes after it, and an edit
						    made meanwhile would be lost. */}
						<fieldset disabled={isSaving} className="m-0 flex min-w-0 grow flex-col gap-6 border-0 px-4 pt-0 pb-6 md:px-8">
							<RouterIdentityFields control={form.control} />

							<FormField
								control={form.control}
								name="enabled"
								render={({ field }) => (
									<FormItem className="flex items-center justify-between gap-4 rounded-lg border p-4">
										<div className="space-y-0.5">
											<FormLabel>{tmr("sheet.enabledLabel")}</FormLabel>
											<FormDescription>{tmr("sheet.enabledHelp")}</FormDescription>
										</div>
										<FormControl>
											<Switch checked={field.value} onCheckedChange={field.onChange} data-testid="model-router-sheet-enabled-switch" />
										</FormControl>
									</FormItem>
								)}
							/>

							<Separator />

							<div className="space-y-1">
								<Label>{tmr("tabs.routing")}</Label>
								<p className="text-muted-foreground text-sm">{trt("description")}</p>
							</div>

							<div className="grid grid-cols-1 gap-4 md:grid-cols-2">
								<FormField
									control={form.control}
									name="scope"
									render={({ field }) => (
										<ScopeSelect
											id="model-router-routing-scope"
											value={field.value}
											onChange={(scope) => {
												field.onChange(scope);
												form.setValue("scope_id", "");
												form.clearErrors("scope_id");
											}}
											testId="model-router-routing-scope-select"
										/>
									)}
								/>
								<FormField
									control={form.control}
									name="priority"
									render={({ field }) => (
										<FormItem className="gap-3">
											<FormLabel>{t("routing.priority")}</FormLabel>
											<FormControl>
												<Input {...field} type="number" min={0} max={1000} data-testid="model-router-routing-priority-input" />
											</FormControl>
											<FormDescription className="text-xs">{trt("priorityHelp")}</FormDescription>
											<FormMessage data-testid="model-router-routing-priority-error" />
										</FormItem>
									)}
								/>
							</div>

							{values.scope !== RoutingRuleScope.Global && (
								<FormField
									control={form.control}
									name="scope_id"
									render={({ field, fieldState }) => (
										<ScopeEntityField
											scope={values.scope}
											value={field.value}
											onChange={(scopeId) => {
												field.onChange(scopeId);
												form.clearErrors("scope_id");
											}}
											error={fieldState.error?.message}
											userInputTestId="model-router-routing-scope-user-input"
										/>
									)}
								/>
							)}

							<div className="space-y-3">
								<Label>{trt("conditionLabel")}</Label>
								<p className="text-muted-foreground text-sm">{trt("conditionHelp")}</p>
								<ConditionBuilder
									initialQuery={condition.query}
									onChange={handleQueryChange}
									providers={availableProviders}
									models={[]}
									allowCustomModels
									allowCelMode
									initialMode={openedMode}
									initialCel={opened.routing.cel_expression ?? ""}
									onModeChange={(mode) => {
										setCondition((current) => ({ ...current, mode }));
										setCelError(null);
									}}
									celError={celError}
								/>
							</div>
						</fieldset>

						<div className="bg-card sticky bottom-0 flex flex-wrap items-center justify-end gap-2 border-t px-4 py-3 md:px-8">
							{submitError && (
								<p className="text-destructive mr-auto text-sm" role="alert" data-testid="model-router-sheet-error">
									{submitError}
								</p>
							)}
							<Button type="button" variant="outline" onClick={onClose} disabled={isSaving} data-testid="model-router-sheet-cancel-button">
								{tc("cancel")}
							</Button>
							<Button type="submit" disabled={!dirty || isSaving} data-testid="model-router-sheet-save-button">
								{isSaving && <LoaderCircle className="h-4 w-4 animate-spin" />}
								{tc("save")}
							</Button>
						</div>
					</form>
				</Form>
			</SheetContent>
		</Sheet>
	);
}