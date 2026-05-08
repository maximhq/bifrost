/**
 * Routing Rule Dialog (Sheet)
 * Create/Edit form for routing rules
 */

import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Button } from "@/components/ui/button";
import { ComboboxSelect } from "@/components/ui/combobox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ModelMultiselect } from "@/components/ui/modelMultiselect";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { getProviderLabel } from "@/lib/constants/logs";
import { getErrorMessage } from "@/lib/store";
import { useGetCustomersQuery, useGetTeamsQuery, useGetVirtualKeysQuery } from "@/lib/store/apis/governanceApi";
import { useGetAllKeysQuery, useGetProvidersQuery } from "@/lib/store/apis/providersApi";
import { useCreateRoutingRuleMutation, useGetRoutingRulesQuery, useUpdateRoutingRuleMutation } from "@/lib/store/apis/routingRulesApi";
import {
	DEFAULT_ROUTING_RULE_FORM_DATA,
	DEFAULT_ROUTING_TARGET,
	ROUTING_RULE_SCOPES,
	RoutingRule,
	RoutingRuleFormData,
	RoutingTargetFormData,
} from "@/lib/types/routingRules";
import { validateRateLimitAndBudgetRules, validateRoutingRules } from "@/lib/utils/celConverterRouting";
import { normalizeRoutingRuleGroupQuery } from "@/lib/utils/routingRuleGroupQuery";
import i18n from "@/lib/i18n";
import { lazy, Suspense, useCallback, useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { RuleGroupType } from "react-querybuilder";
import { toast } from "sonner";
import { Plus, Save, Trash2, X } from "lucide-react";

interface RoutingRuleDialogProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	editingRule?: RoutingRule | null;
	onSuccess?: () => void;
}

const defaultQuery: RuleGroupType = {
	combinator: "and",
	rules: [],
};

// Lazy-load CEL builder (heavy dependency tree).
const CELRuleBuilderLazy = lazy(() =>
	import("@/app/workspace/routing-rules/components/celBuilder/celRuleBuilder").then((mod) => ({
		default: mod.CELRuleBuilder,
	})),
);
const CELRuleBuilder = (props: React.ComponentProps<typeof CELRuleBuilderLazy>) => (
	<Suspense fallback={<div className="text-sm text-gray-500">{i18n.t("workspace.routingRules.loadingCelBuilder")}</div>}>
		<CELRuleBuilderLazy {...props} />
	</Suspense>
);

export function RoutingRuleSheet({ open, onOpenChange, editingRule, onSuccess }: RoutingRuleDialogProps) {
	const { data: rulesData } = useGetRoutingRulesQuery();
	const rules = rulesData?.rules || [];
	const { data: providersData = [] } = useGetProvidersQuery();
	const { data: allKeysData = [] } = useGetAllKeysQuery();
	const { data: vksData = { virtual_keys: [] } } = useGetVirtualKeysQuery();
	const { data: teamsData = { teams: [], count: 0, total_count: 0, limit: 0, offset: 0 } } = useGetTeamsQuery();
	const { data: customersData = { customers: [] } } = useGetCustomersQuery();
	const [createRoutingRule, { isLoading: isCreating }] = useCreateRoutingRuleMutation();
	const [updateRoutingRule, { isLoading: isUpdating }] = useUpdateRoutingRuleMutation();

	// State for targets and query (managed outside react-hook-form for complex nested structures)
	const [targets, setTargets] = useState<RoutingTargetFormData[]>([{ ...DEFAULT_ROUTING_TARGET }]);
	const [query, setQuery] = useState<RuleGroupType>(defaultQuery);
	const [builderKey, setBuilderKey] = useState(0);

	const {
		register,
		handleSubmit,
		setValue,
		watch,
		reset,
		formState: { errors },
	} = useForm<RoutingRuleFormData>({
		defaultValues: DEFAULT_ROUTING_RULE_FORM_DATA,
	});

	const isEditing = !!editingRule;
	const isLoading = isCreating || isUpdating;
	const canCreate = useRbac(RbacResource.RoutingRules, RbacOperation.Create);
	const canUpdate = useRbac(RbacResource.RoutingRules, RbacOperation.Update);
	const hasRequiredAccess = isEditing ? canUpdate : canCreate;
	const enabled = watch("enabled");
	const chainRule = watch("chain_rule");
	const scope = watch("scope");
	const scopeId = watch("scope_id");
	const fallbacks = watch("fallbacks");

	// Get available providers from configured providers, plus any provider already
	// referenced by the current targets, existing rules' targets, or rules' fallbacks
	// so edited/removed providers are still visible in the dropdown.
	const availableProviders = Array.from(
		new Set([
			...providersData.map((p) => p.name),
			...(targets.map((t) => t.provider).filter(Boolean) as string[]),
			...(rules.flatMap((r) => r.targets?.map((t) => t.provider).filter(Boolean) ?? []) as string[]),
			...rules.flatMap((r) => (r.fallbacks ?? []).map((f) => f.split("/")[0]?.trim()).filter(Boolean)),
		]),
	);

	// Initialize form data when editing rule changes
	useEffect(() => {
		if (editingRule) {
			setValue("id", editingRule.id);
			setValue("name", editingRule.name);
			setValue("description", editingRule.description);
			setValue("cel_expression", editingRule.cel_expression);
			setValue("fallbacks", editingRule.fallbacks || []);
			setValue("scope", editingRule.scope);
			setValue("scope_id", editingRule.scope_id || "");
			setValue("priority", editingRule.priority);
			setValue("enabled", editingRule.enabled);
			setValue("chain_rule", editingRule.chain_rule ?? false);
			if (editingRule.targets && editingRule.targets.length > 0) {
				setTargets(
					editingRule.targets.map((t) => ({
						...DEFAULT_ROUTING_TARGET,
						provider: t.provider || "",
						model: t.model || "",
						key_id: t.key_id || "",
						weight: t.weight,
					})),
				);
			} else {
				setTargets([{ ...DEFAULT_ROUTING_TARGET }]);
			}
			// Only react-querybuilder-shaped queries are valid; config may store other JSON under `query`.
			setQuery(normalizeRoutingRuleGroupQuery(editingRule.query));
			setBuilderKey((prev) => prev + 1);
		} else {
			reset();
			setTargets([{ ...DEFAULT_ROUTING_TARGET }]);
			setQuery(defaultQuery);
			setBuilderKey((prev) => prev + 1);
		}
	}, [editingRule, open, setValue, reset]);

	const handleQueryChange = useCallback(
		(expression: string, newQuery: RuleGroupType) => {
			setValue("cel_expression", expression);
			setQuery(newQuery);
		},
		[setValue],
	);

	const addTarget = () => {
		const remaining = 1 - targets.reduce((sum, t) => sum + (t.weight || 0), 0);
		setTargets((prev) => [...prev, { ...DEFAULT_ROUTING_TARGET, weight: Math.max(0, parseFloat(remaining.toFixed(4))) }]);
	};

	const removeTarget = (index: number) => {
		setTargets((prev) => prev.filter((_, i) => i !== index));
	};

	const updateTarget = (index: number, field: keyof RoutingTargetFormData, value: string | number) => {
		setTargets((prev) => prev.map((t, i) => (i === index ? { ...t, [field]: value } : t)));
	};

	const totalWeight = targets.reduce((sum, t) => sum + (t.weight || 0), 0);

	const onSubmit = (data: RoutingRuleFormData) => {
		// Validate scope_id is required when scope is not global
		if (data.scope !== "global" && !data.scope_id?.trim()) {
			toast.error(
				data.scope === "team"
					? i18n.t("workspace.routingRules.teamIsRequired")
					: data.scope === "customer"
						? i18n.t("workspace.routingRules.customerIsRequired")
						: i18n.t("workspace.routingRules.virtualKeyIsRequired"),
			);
			return;
		}

		// Validate targets
		if (targets.length === 0) {
			toast.error(i18n.t("workspace.routingRules.atLeastOneRoutingTargetRequired"));
			return;
		}
		for (const t of targets) {
			if (t.weight <= 0) {
				toast.error(i18n.t("workspace.routingRules.eachTargetWeightMustBeGreaterThanZero"));
				return;
			}
		}
		if (Math.abs(totalWeight - 1) > 0.001) {
			toast.error(i18n.t("workspace.routingRules.targetWeightsMustSumToOne", { total: totalWeight.toFixed(4) }));
			return;
		}

		// Validate regex patterns in routing rules
		const regexErrors = validateRoutingRules(query);
		if (regexErrors.length > 0) {
			toast.error(`${i18n.t("workspace.routingRules.invalidRegexPattern")}\n${regexErrors.join("\n")}`);
			return;
		}

		// Validate rate limit and budget rules
		const rateLimitErrors = validateRateLimitAndBudgetRules(query);
		if (rateLimitErrors.length > 0) {
			toast.error(`Invalid rule configuration:\n${rateLimitErrors.join("\n")}`);
			return;
		}

		// Filter out incomplete fallbacks (empty provider)
		const validFallbacks = (data.fallbacks || []).filter((fb) => {
			const provider = fb.split("/")[0]?.trim();
			return provider && provider.length > 0;
		});

		const payload = {
			name: data.name,
			description: data.description,
			cel_expression: data.cel_expression,
			targets: targets.map(({ provider, model, key_id, weight }) => ({
				provider: provider || undefined,
				model: model || undefined,
				key_id: key_id || undefined,
				weight,
			})),
			fallbacks: validFallbacks,
			scope: data.scope,
			scope_id: data.scope === "global" ? undefined : data.scope_id || undefined,
			priority: data.priority,
			enabled: data.enabled,
			chain_rule: data.chain_rule,
			query: query,
		};

		const submitPromise =
			isEditing && editingRule
				? updateRoutingRule({
						id: editingRule.id,
						data: payload,
					}).unwrap()
				: createRoutingRule(payload).unwrap();

		submitPromise
			.then(() => {
				toast.success(
					isEditing
						? i18n.t("workspace.routingRules.routingRuleUpdatedSuccessfully")
						: i18n.t("workspace.routingRules.routingRuleCreatedSuccessfully"),
				);
				reset();
				setTargets([{ ...DEFAULT_ROUTING_TARGET }]);
				setQuery(defaultQuery);
				setBuilderKey((prev) => prev + 1);
				onOpenChange(false);
				onSuccess?.();
			})
			.catch((error: any) => {
				toast.error(getErrorMessage(error));
			});
	};

	const handleCancel = () => {
		reset();
		setTargets([{ ...DEFAULT_ROUTING_TARGET }]);
		setQuery(defaultQuery);
		setBuilderKey((prev) => prev + 1);
		onOpenChange(false);
	};

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="flex w-full min-w-1/2 flex-col gap-4 overflow-x-hidden p-0 pt-4">
				<SheetHeader className="flex flex-col items-start px-8 py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10">
					<SheetTitle>
						{isEditing ? i18n.t("workspace.routingRules.editRoutingRule") : i18n.t("workspace.routingRules.createNewRoutingRule")}
					</SheetTitle>
					<SheetDescription>
						{isEditing
							? i18n.t("workspace.routingRules.editRoutingRuleDescription")
							: i18n.t("workspace.routingRules.createNewRoutingRuleDescription")}
					</SheetDescription>
				</SheetHeader>

				<form onSubmit={handleSubmit(onSubmit)}>
					<div className="flex flex-col gap-6 px-8 pb-6">
						{/* Rule Name */}
						<div className="space-y-3">
							<Label htmlFor="name">
								{i18n.t("workspace.routingRules.ruleName")} <span className="text-red-500">*</span>
							</Label>
							<Input
								id="name"
								placeholder={i18n.t("workspace.routingRules.ruleNamePlaceholder")}
								{...register("name", { required: i18n.t("workspace.routingRules.ruleNameErrorRequired"), maxLength: 255 })}
							/>
							{errors.name && <p className="text-destructive text-sm">{errors.name.message}</p>}
						</div>

						{/* Description */}
						<div className="space-y-3">
							<Label htmlFor="description">{i18n.t("workspace.routingRules.descriptionField")}</Label>
							<Textarea
								id="description"
								placeholder={i18n.t("workspace.routingRules.descriptionFieldPlaceholder")}
								rows={2}
								{...register("description")}
							/>
						</div>

						{/* Enabled Switch */}
						<div className="flex items-center justify-between rounded-lg border p-4">
							<div className="space-y-0.5">
								<Label htmlFor="enabled">{i18n.t("workspace.routingRules.enableRule")}</Label>
								<p className="text-muted-foreground text-sm">{i18n.t("workspace.routingRules.enableRuleDescription")}</p>
							</div>
							<Switch id="enabled" checked={enabled} onCheckedChange={(checked) => setValue("enabled", checked)} />
						</div>

						{/* Chain Rule Switch */}
						<div className="flex items-center justify-between rounded-lg border p-4">
							<div className="space-y-0.5">
								<Label htmlFor="chain_rule">{i18n.t("workspace.routingRules.chainRule")}</Label>
								<p className="text-muted-foreground text-sm">{i18n.t("workspace.routingRules.chainRuleDescription")}</p>
							</div>
							<Switch
								id="chain_rule"
								checked={chainRule}
								onCheckedChange={(checked) => setValue("chain_rule", checked)}
								data-testid="routing-rule-chain-rule-switch"
							/>
						</div>

						{/* Scope and Priority - Side by Side */}
						<div className="grid grid-cols-2 gap-4">
							<div className="space-y-3">
								<Label htmlFor="scope">{i18n.t("workspace.routingRules.scope")}</Label>
								<Select>
									<SelectTrigger className="w-full">
										<SelectValue placeholder={i18n.t("workspace.routingRules.selectScope")} />
									</SelectTrigger>
									<SelectContent>
										{ROUTING_RULE_SCOPES.map((scopeOption) => (
											<SelectItem key={scopeOption.value} value={scopeOption.value}>
												{scopeOption.label}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
							</div>

							<div className="space-y-3">
								<Label htmlFor="priority">
									{i18n.t("workspace.routingRules.priority")} <span className="text-red-500">*</span>
								</Label>
								<Input
									id="priority"
									type="number"
									min={0}
									max={1000}
									{...register("priority", {
										required: i18n.t("workspace.routingRules.priorityRequired"),
										min: { value: 0, message: i18n.t("workspace.routingRules.priorityMustBeAtLeast") },
										max: { value: 1000, message: i18n.t("workspace.routingRules.priorityMustBeAtMost") },
										valueAsNumber: true,
									})}
								/>
								<p className="text-muted-foreground text-xs">{i18n.t("workspace.routingRules.priorityDescription")}</p>
								{errors.priority && <p className="text-destructive text-sm">{errors.priority.message}</p>}
							</div>
						</div>

						{scope !== "global" && (
							<div className="space-y-2">
								<Label htmlFor="scope_id">
									{scope === "team"
										? i18n.t("workspace.routingRules.team")
										: scope === "customer"
											? i18n.t("workspace.routingRules.customer")
											: i18n.t("workspace.routingRules.virtualKey")}{" "}
									<span className="text-red-500">*</span>
								</Label>
								{scope === "team" && teamsData.teams.length > 0 && (
									<ComboboxSelect
										options={teamsData.teams.map((team) => ({ label: team.name, value: team.id }))}
										value={scopeId || null}
										onValueChange={(value) => setValue("scope_id", value ?? "")}
										placeholder={i18n.t("workspace.routingRules.selectTeam")}
										noPortal
									/>
								)}
								{scope === "customer" && customersData.customers.length > 0 && (
									<ComboboxSelect
										options={customersData.customers.map((customer) => ({ label: customer.name, value: customer.id }))}
										value={scopeId || null}
										onValueChange={(value) => setValue("scope_id", value ?? "")}
										placeholder={i18n.t("workspace.routingRules.selectCustomer")}
										noPortal
									/>
								)}
								{scope === "virtual_key" && vksData.virtual_keys.length > 0 && (
									<ComboboxSelect
										options={vksData.virtual_keys.map((vk) => ({ label: vk.name, value: vk.id }))}
										value={scopeId || null}
										onValueChange={(value) => setValue("scope_id", value ?? "")}
										placeholder={i18n.t("workspace.routingRules.selectVirtualKey")}
										noPortal
									/>
								)}
								{((scope === "team" && teamsData.teams.length === 0) ||
									(scope === "customer" && customersData.customers.length === 0) ||
									(scope === "virtual_key" && vksData.virtual_keys.length === 0)) && (
									<p className="text-muted-foreground text-sm">
										{scope === "team"
											? i18n.t("workspace.routingRules.noTeamsAvailable")
											: scope === "customer"
												? i18n.t("workspace.routingRules.noCustomersAvailable")
												: i18n.t("workspace.routingRules.noVirtualKeysAvailable")}
									</p>
								)}
								{errors.scope_id && <p className="text-destructive text-sm">{errors.scope_id.message}</p>}
							</div>
						)}

						<Separator />

						{/* CEL Rule Builder */}
						<div className="space-y-3">
							<Label>{i18n.t("workspace.routingRules.ruleBuilder")}</Label>
							<p className="text-muted-foreground text-sm">{i18n.t("workspace.routingRules.ruleBuilderDescription")}</p>
							<CELRuleBuilder
								key={builderKey}
								initialQuery={query}
								onChange={handleQueryChange}
								providers={availableProviders}
								models={[]}
								allowCustomModels={true}
							/>
						</div>

						{/* Note about Token/Request Limits and Budget Configuration */}
						<p
							className="text-muted-foreground text-xs"
							dangerouslySetInnerHTML={{ __html: i18n.t("workspace.routingRules.ruleBuilderNote") }}
						/>

						<Separator />

						{/* Routing Targets */}
						<div className="space-y-3">
							<div className="flex items-center justify-between">
								<div>
									<Label>{i18n.t("workspace.routingRules.routingTargets")}</Label>
									<p className="text-muted-foreground mt-0.5 text-xs">{i18n.t("workspace.routingRules.routingTargetsDescription")}</p>
								</div>
								<Button
									type="button"
									variant="outline"
									size="sm"
									onClick={addTarget}
									className="shrink-0 gap-2"
									data-testid="routing-rule-target-add"
								>
									<Plus className="h-4 w-4" />
									{i18n.t("workspace.routingRules.addTarget")}
								</Button>
							</div>

							<div className="space-y-3">
								{targets.map((target, index) => (
									<TargetRow
										key={index}
										target={target}
										index={index}
										availableProviders={availableProviders}
										allKeys={allKeysData}
										showRemove={targets.length > 1}
										onUpdate={updateTarget}
										onRemove={removeTarget}
									/>
								))}
							</div>

							{/* Weight sum indicator */}
							<div
								className={`flex items-center justify-end gap-2 text-xs font-medium ${Math.abs(totalWeight - 1) > 0.001 ? "text-destructive" : "text-muted-foreground"}`}
							>
								{i18n.t("workspace.routingRules.totalWeight")} {totalWeight.toFixed(4)}
								{Math.abs(totalWeight - 1) > 0.001 && (
									<span className="text-destructive">({i18n.t("workspace.routingRules.mustEqualOne")})</span>
								)}
							</div>
						</div>

						{/* Fallbacks */}
						<div className="space-y-3">
							<div className="flex items-center justify-between">
								<div>
									<Label>{i18n.t("workspace.routingRules.fallbacks")}</Label>{" "}
									<p className="text-muted-foreground mt-0.5 text-xs">{i18n.t("workspace.routingRules.fallbacksDescription")}</p>
								</div>
								<Button
									type="button"
									variant="outline"
									size="sm"
									onClick={() => setValue("fallbacks", [...(fallbacks || []), ""])}
									className="gap-2"
								>
									<Plus className="h-4 w-4" />
									{i18n.t("workspace.routingRules.addFallback")}
								</Button>
							</div>
							<div className="space-y-2">
								{(fallbacks || []).length === 0 ? (
									<p className="text-muted-foreground text-sm">{i18n.t("workspace.routingRules.noFallbacksConfigured")}</p>
								) : (
									(fallbacks || []).map((fallback, index) => {
										// Parse provider/model from fallback string
										const parts = fallback.split("/");
										const fbProvider = parts[0] || "";
										const fbModel = parts[1] || "";

										const handleProviderChange = (newProvider: string) => {
											const model = fbModel || "";
											const newFallback = `${newProvider}/${model}`;
											const newFallbacks = [...fallbacks];
											newFallbacks[index] = newFallback;
											setValue("fallbacks", newFallbacks);
										};

										const handleModelChange = (newModel: string) => {
											const prov = fbProvider || "";
											const newFallback = `${prov}/${newModel}`;
											const newFallbacks = [...fallbacks];
											newFallbacks[index] = newFallback;
											setValue("fallbacks", newFallbacks);
										};

										const handleRemove = () => {
											const newFallbacks = fallbacks.filter((_: string, i: number) => i !== index);
											setValue("fallbacks", newFallbacks);
										};

										return (
											<div key={index} className="flex items-center gap-2">
												<div className="flex-1">
													<Select value={fbProvider} onValueChange={handleProviderChange}>
														<SelectTrigger className="w-full">
															<SelectValue placeholder={i18n.t("workspace.routingRules.selectProvider")} />
														</SelectTrigger>
														<SelectContent>
															{availableProviders.map((prov) => (
																<SelectItem key={prov} value={prov}>
																	<div className="flex items-center gap-2">
																		<RenderProviderIcon provider={prov as ProviderIconType} size="sm" className="h-4 w-4" />
																		<span>{getProviderLabel(prov)}</span>
																	</div>
																</SelectItem>
															))}
														</SelectContent>
													</Select>
												</div>
												<div className="flex-1">
													<ModelMultiselect
														provider={fbProvider || undefined}
														value={fbModel}
														onChange={handleModelChange}
														placeholder={i18n.t("workspace.routingRules.incoming")}
														isSingleSelect
														disabled={!fbProvider}
														className="!h-9 !min-h-9 w-full"
													/>
												</div>
												<Button
													type="button"
													variant="ghost"
													size="sm"
													onClick={handleRemove}
													className="h-9 px-2"
													aria-label={`Remove fallback ${index + 1}`}
												>
													<Trash2 className="h-4 w-4" />
												</Button>
											</div>
										);
									})
								)}
							</div>
							<p className="text-muted-foreground text-xs">{i18n.t("workspace.routingRules.fallbacksOrderNote")}</p>
						</div>
					</div>
					{/* Action Buttons */}
					<div className="bg-card sticky bottom-0 flex justify-end gap-3 border-t px-8 py-4">
						<Button type="button" variant="outline" onClick={handleCancel} disabled={isLoading}>
							<X className="h-4 w-4" />
							{i18n.t("workspace.routingRules.cancel")}
						</Button>
						<Button type="submit" disabled={isLoading || !hasRequiredAccess}>
							<Save className="h-4 w-4" />
							{isEditing ? i18n.t("workspace.routingRules.updateRule") : i18n.t("workspace.routingRules.saveRule")}
						</Button>
					</div>
				</form>
			</SheetContent>
		</Sheet>
	);
}

interface TargetRowProps {
	target: RoutingTargetFormData;
	index: number;
	availableProviders: string[];
	allKeys: Array<{ key_id: string; name: string; provider: string }>;
	showRemove: boolean;
	onUpdate: (index: number, field: keyof RoutingTargetFormData, value: string | number) => void;
	onRemove: (index: number) => void;
}

function TargetRow({ target, index, availableProviders, allKeys, showRemove, onUpdate, onRemove }: TargetRowProps) {
	const availableKeys = target.provider
		? allKeys.filter((k) => k.provider === target.provider).map((k) => ({ id: k.key_id, name: k.name }))
		: [];

	return (
		<div className="space-y-3 rounded-lg border p-3" data-testid={`routing-target-${index}`}>
			<div className="flex items-center justify-between">
				<span className="text-muted-foreground text-sm font-medium">
					{i18n.t("workspace.routingRules.targetNumber", { index: index + 1 })}
				</span>
				<div className="flex items-center gap-2">
					<div className="flex items-center gap-1.5">
						<Label htmlFor={`routing-target-${index}-weight-input`} className="text-muted-foreground shrink-0 text-xs">
							{i18n.t("workspace.routingRules.weight")}
						</Label>
						<Input
							id={`routing-target-${index}-weight-input`}
							type="number"
							min={0.001}
							max={1}
							step={0.001}
							value={target.weight}
							onChange={(e) => onUpdate(index, "weight", parseFloat(e.target.value) || 0)}
							className="h-8 w-24 text-sm"
							data-testid={`routing-target-${index}-weight-input`}
						/>
					</div>
					{showRemove && (
						<Button
							type="button"
							variant="ghost"
							size="sm"
							onClick={() => onRemove(index)}
							className="h-8 w-8 p-0"
							aria-label={`Remove target ${index + 1}`}
							data-testid={`routing-target-${index}-remove-button`}
						>
							<Trash2 className="h-3.5 w-3.5" />
						</Button>
					)}
				</div>
			</div>

			<div className="grid grid-cols-2 gap-3">
				<div className="space-y-1.5">
					<Label id={`routing-target-${index}-provider-label`} className="text-xs">
						{i18n.t("workspace.routingRules.provider")}
					</Label>
					<div className="flex gap-1.5">
						<Select
							value={target.provider}
							onValueChange={(value) => {
								onUpdate(index, "provider", value);
								onUpdate(index, "model", "");
								onUpdate(index, "key_id", "");
							}}
						>
							<SelectTrigger
								id={`routing-target-${index}-provider-select`}
								aria-labelledby={`routing-target-${index}-provider-label`}
								className="h-9 flex-1 text-sm"
								data-testid={`routing-target-${index}-provider-select`}
							>
								<SelectValue placeholder={i18n.t("workspace.routingRules.incoming")} />
							</SelectTrigger>
							<SelectContent>
								{availableProviders.map((prov) => (
									<SelectItem key={prov} value={prov}>
										<div className="flex items-center gap-2">
											<RenderProviderIcon provider={prov as ProviderIconType} size="sm" className="h-4 w-4" />
											<span>{getProviderLabel(prov)}</span>
										</div>
									</SelectItem>
								))}
							</SelectContent>
						</Select>
						{target.provider && (
							<Button
								type="button"
								variant="outline"
								size="sm"
								onClick={() => {
									onUpdate(index, "provider", "");
									onUpdate(index, "model", "");
									onUpdate(index, "key_id", "");
								}}
								className="h-9 w-9 p-0"
								aria-label={`Clear provider for target ${index + 1}`}
								data-testid={`routing-target-${index}-provider-clear`}
							>
								<X className="h-3.5 w-3.5" />
							</Button>
						)}
					</div>
				</div>

				<div className="space-y-1.5">
					<Label id={`routing-target-${index}-model-label`} className="text-xs">
						{i18n.t("workspace.routingRules.model")}
					</Label>
					<div className="flex gap-1.5">
						<div className="flex-1" data-testid={`routing-target-${index}-model-select`}>
							<ModelMultiselect
								provider={target.provider || undefined}
								value={target.model}
								onChange={(value) => onUpdate(index, "model", value)}
								placeholder={i18n.t("workspace.routingRules.incoming")}
								isSingleSelect
								loadModelsOnEmptyProvider
								className="!h-9 !min-h-9"
								inputId={`routing-target-${index}-model-input`}
								ariaLabelledBy={`routing-target-${index}-model-label`}
							/>
						</div>
						{target.model && (
							<Button
								type="button"
								variant="outline"
								size="sm"
								onClick={() => onUpdate(index, "model", "")}
								className="h-9 w-9 p-0"
								aria-label={`Clear model for target ${index + 1}`}
								data-testid={`routing-target-${index}-model-clear`}
							>
								<X className="h-3.5 w-3.5" />
							</Button>
						)}
					</div>
				</div>
			</div>

			{target.provider && (availableKeys.length > 0 || target.key_id) && (
				<div className="space-y-1.5">
					<Label id={`routing-target-${index}-apikey-label`} className="text-xs">
						{i18n.t("workspace.routingRules.apiKeyOptional")}
					</Label>
					<div className="flex gap-1.5">
						<Select value={target.key_id || ""} onValueChange={(value) => onUpdate(index, "key_id", value)}>
							<SelectTrigger
								id={`routing-target-${index}-apikey-select`}
								aria-labelledby={`routing-target-${index}-apikey-label`}
								className="h-9 flex-1 text-sm"
								data-testid={`routing-target-${index}-apikey-select`}
							>
								<SelectValue placeholder={i18n.t("workspace.routingRules.selectKey")} />
							</SelectTrigger>
							<SelectContent>
								{availableKeys.map((key) => (
									<SelectItem key={key.id} value={key.id}>
										{key.name}
									</SelectItem>
								))}
								{target.key_id && !availableKeys.some((k) => k.id === target.key_id) && (
									<SelectItem key={`pinned-${target.key_id}`} value={target.key_id}>
										(pinned) {target.key_id}
									</SelectItem>
								)}
							</SelectContent>
						</Select>
						{target.key_id && (
							<Button
								type="button"
								variant="outline"
								size="sm"
								onClick={() => onUpdate(index, "key_id", "")}
								className="h-9 w-9 p-0"
								aria-label={`Clear API key for target ${index + 1}`}
								data-testid={`routing-target-${index}-apikey-clear`}
							>
								<X className="h-3.5 w-3.5" />
							</Button>
						)}
					</div>
				</div>
			)}
		</div>
	);
}