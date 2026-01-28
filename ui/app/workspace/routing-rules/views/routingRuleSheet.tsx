/**
 * Routing Rule Dialog (Sheet)
 * Create/Edit form for routing rules
 */

"use client";

import { useState, useEffect, useCallback } from "react";
import { useForm } from "react-hook-form";
import { RuleGroupType } from "react-querybuilder";
import {
	Sheet,
	SheetContent,
	SheetDescription,
	SheetHeader,
	SheetTitle,
} from "@/components/ui/sheet";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { ModelMultiselect } from "@/components/ui/modelMultiselect";
import { X, Save, AlertCircle, Plus, Trash2 } from "lucide-react";
import { RoutingRule, RoutingRuleFormData, DEFAULT_ROUTING_RULE_FORM_DATA, ROUTING_RULE_SCOPES } from "@/lib/types/routingRules";
import {
	useCreateRoutingRuleMutation,
	useUpdateRoutingRuleMutation,
	useGetRoutingRulesQuery,
} from "@/lib/store/apis/routingRulesApi";
import {
	useGetVirtualKeysQuery,
	useGetTeamsQuery,
	useGetCustomersQuery,
} from "@/lib/store/apis/governanceApi";
import { useGetProvidersQuery } from "@/lib/store/apis/providersApi";
import { toast } from "sonner";
import dynamic from "next/dynamic";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { getProviderLabel } from "@/lib/constants/logs";
import { Separator } from "@/components/ui/separator";
import { getErrorMessage } from "@/lib/store";
import {
	validateRoutingRules,
	validateRateLimitAndBudgetRules
} from "@/lib/utils/celConverterRouting";

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

// Dynamically import CEL builder to avoid SSR issues
const CELRuleBuilder = dynamic(
	() => import("@/app/workspace/routing-rules/components/celBuilder/celRuleBuilder").then((mod) => ({
		default: mod.CELRuleBuilder,
	})),
	{
		loading: () => <div className="text-sm text-gray-500">Loading CEL builder...</div>,
		ssr: false,
	},
);

export function RoutingRuleSheet({
	open,
	onOpenChange,
	editingRule,
	onSuccess,
}: RoutingRuleDialogProps) {
	const { data: rules = [] } = useGetRoutingRulesQuery();
	const { data: providersData = [] } = useGetProvidersQuery();
	const { data: vksData = { virtual_keys: [] } } = useGetVirtualKeysQuery();
	const { data: teamsData = { teams: [] } } = useGetTeamsQuery({});
	const { data: customersData = { customers: [] } } = useGetCustomersQuery();
	const [createRoutingRule, { isLoading: isCreating }] = useCreateRoutingRuleMutation();
	const [updateRoutingRule, { isLoading: isUpdating }] = useUpdateRoutingRuleMutation();

	// State to track the query structure for the rule builder
	const [query, setQuery] = useState<RuleGroupType>(defaultQuery);
	const [builderKey, setBuilderKey] = useState(0);
	const [showCELFallbackNotice, setShowCELFallbackNotice] = useState(false);

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
	const enabled = watch("enabled");
	const scope = watch("scope");
	const scopeId = watch("scope_id");
	const provider = watch("provider");
	const model = watch("model");
	const fallbacks = watch("fallbacks");

	// Get available providers from configured providers, fallback to providers in rules
	const availableProviders = providersData.length > 0
		? providersData.map((p) => p.name)
		: Array.from(
			new Set([...rules.map((r) => r.provider).filter((p) => p !== "")]),
		);
	const availableModels = Array.from(
		new Set([...rules.map((r) => r.model).filter((m) => m !== "" && m !== undefined)]),
	) as string[];

	// Initialize form data when editing rule changes
	useEffect(() => {
		if (editingRule) {
			setValue("id", editingRule.id);
			setValue("name", editingRule.name);
			setValue("description", editingRule.description);
			setValue("cel_expression", editingRule.cel_expression);
			setValue("provider", editingRule.provider);
			setValue("model", editingRule.model || "");
			setValue("fallbacks", editingRule.fallbacks || []);
			setValue("scope", editingRule.scope);
			setValue("scope_id", editingRule.scope_id || "");
			setValue("priority", editingRule.priority);
			setValue("enabled", editingRule.enabled);
			// Restore the query object if it exists, otherwise use default
			if (editingRule.query) {
				setQuery(editingRule.query);
			} else {
				setQuery(defaultQuery);
			}
			setBuilderKey((prev) => prev + 1);
		} else {
			reset();
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

	const onSubmit = (data: RoutingRuleFormData) => {
		// Validate scope_id is required when scope is not global
		if (data.scope !== "global" && !data.scope_id?.trim()) {
			toast.error(`${data.scope === "team" ? "Team" : data.scope === "customer" ? "Customer" : "Virtual Key"} is required`);
			return;
		}

		// Validate regex patterns in routing rules
		const regexErrors = validateRoutingRules(query);
		if (regexErrors.length > 0) {
			const errorMessage = regexErrors.join("\n");
			toast.error(`Invalid regex pattern:\n${errorMessage}`);
			return;
		}

		// Validate rate limit and budget rules
		const rateLimitErrors = validateRateLimitAndBudgetRules(query);
		if (rateLimitErrors.length > 0) {
			const errorMessage = rateLimitErrors.join("\n");
			toast.error(`Invalid rule configuration:\n${errorMessage}`);
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
			provider: data.provider,
			model: data.model,
			fallbacks: validFallbacks,
			scope: data.scope,
			scope_id: data.scope_id || undefined,
			priority: data.priority,
			enabled: data.enabled,
			query: query,
		};

		const submitPromise = isEditing && editingRule
			? updateRoutingRule({
				id: editingRule.id,
				data: payload,
			}).unwrap()
			: createRoutingRule(payload).unwrap();

		submitPromise
			.then(() => {
				toast.success(
					isEditing
						? "Routing rule updated successfully"
						: "Routing rule created successfully",
				);
				reset();
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
		setQuery(defaultQuery);
		setBuilderKey((prev) => prev + 1);
		onOpenChange(false);
	};

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="dark:bg-card flex w-full flex-col min-w-1/2 gap-4 overflow-x-hidden bg-white p-8">
				<SheetHeader className="flex flex-col items-start">
					<SheetTitle>
						{isEditing ? "Edit Routing Rule" : "Create New Routing Rule"}
					</SheetTitle>
					<SheetDescription>
						{isEditing
							? "Update the routing rule configuration"
							: "Create a new CEL-based routing rule for intelligent request routing"}
					</SheetDescription>
				</SheetHeader>

				<form onSubmit={handleSubmit(onSubmit)} className="space-y-6">
					{/* Rule Name */}
					<div className="space-y-3">
						<Label htmlFor="name">
							Rule Name <span className="text-red-500">*</span>
						</Label>
						<Input
							id="name"
							placeholder="e.g., Route GPT-4 to Azure"
							{...register("name", { required: "Rule name is required", maxLength: 255 })}
						/>
						{errors.name && <p className="text-destructive text-sm">{errors.name.message}</p>}
					</div>

					{/* Description */}
					<div className="space-y-3">
						<Label htmlFor="description">Description</Label>
						<Textarea
							id="description"
							placeholder="Describe what this rule does..."
							rows={2}
							{...register("description")}
						/>
					</div>

					{/* Enabled Switch */}
					<div className="flex items-center justify-between rounded-lg border p-4">
						<div className="space-y-0.5">
							<Label htmlFor="enabled">Enable Rule</Label>
							<p className="text-muted-foreground text-sm">Rule will be active and applied to matching requests</p>
						</div>
						<Switch
							id="enabled"
							checked={enabled}
							onCheckedChange={(checked) => setValue("enabled", checked)}
						/>
					</div>

					{/* Scope and Priority - Side by Side */}
					<div className="grid grid-cols-2 gap-4">
						<div className="space-y-3">
							<Label htmlFor="scope">Scope</Label>
							<Select value={scope} onValueChange={(value) => {
								setValue("scope", value as any);
								// Clear scope_id when scope changes
								setValue("scope_id", "");
							}}>
								<SelectTrigger className="w-full">
									<SelectValue placeholder="Select scope..." />
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
								Priority <span className="text-red-500">*</span>
							</Label>
							<Input
								id="priority"
								type="number"
								min={0}
								max={1000}
								{...register("priority", {
									required: "Priority is required",
									min: { value: 0, message: "Priority must be ≥ 0" },
									max: { value: 1000, message: "Priority must be ≤ 1000" },
									valueAsNumber: true,
								})}
							/>
							<p className="text-muted-foreground text-xs">Lower numbers = higher priority (0 is highest)</p>
							{errors.priority && <p className="text-destructive text-sm">{errors.priority.message}</p>}
						</div>
					</div>

					{scope !== "global" && (
						<div className="space-y-2">
							<Label htmlFor="scope_id">
								{scope === "team" ? "Team" : scope === "customer" ? "Customer" : "Virtual Key"} <span className="text-red-500">*</span>
							</Label>
							{scope === "team" && teamsData.teams.length > 0 && (
								<Select value={scopeId || ""} onValueChange={(value) => setValue("scope_id", value)}>
									<SelectTrigger className="w-full">
										<SelectValue placeholder="Select a team..." />
									</SelectTrigger>
									<SelectContent>
										{teamsData.teams.map((team) => (
											<SelectItem key={team.id} value={team.id}>
												{team.name}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
							)}
							{scope === "customer" && customersData.customers.length > 0 && (
								<Select value={scopeId || ""} onValueChange={(value) => setValue("scope_id", value)}>
									<SelectTrigger className="w-full">
										<SelectValue placeholder="Select a customer..." />
									</SelectTrigger>
									<SelectContent>
										{customersData.customers.map((customer) => (
											<SelectItem key={customer.id} value={customer.id}>
												{customer.name}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
							)}
							{scope === "virtual_key" && vksData.virtual_keys.length > 0 && (
								<Select value={scopeId || ""} onValueChange={(value) => setValue("scope_id", value)}>
									<SelectTrigger className="w-full">
										<SelectValue placeholder="Select a virtual key..." />
									</SelectTrigger>
									<SelectContent>
										{vksData.virtual_keys.map((vk) => (
											<SelectItem key={vk.id} value={vk.id}>
												{vk.name}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
							)}
							{((scope === "team" && teamsData.teams.length === 0) ||
								(scope === "customer" && customersData.customers.length === 0) ||
								(scope === "virtual_key" && vksData.virtual_keys.length === 0)) && (
									<p className="text-sm text-muted-foreground">No {scope === "team" ? "teams" : scope === "customer" ? "customers" : "virtual keys"} available</p>
								)}
							{errors.scope_id && <p className="text-destructive text-sm">{errors.scope_id.message}</p>}
						</div>
					)}

					<Separator />

					{/* CEL Rule Builder */}
					<div className="space-y-3">
						<Label>Rule Builder</Label>
						<p className="text-muted-foreground text-sm">
							Build conditions to determine when this rule should apply. Leave empty to apply this rule to all requests.
						</p>
						<CELRuleBuilder
							key={builderKey}
							initialQuery={query}
							onChange={handleQueryChange}
							providers={availableProviders}
							models={availableModels}
						/>
					</div>

					{/* Note about Token/Request Limits and Budget Configuration */}
					<p className="text-xs text-muted-foreground">
						Note: Ensure token limits, request limits, and budget are configured in <strong>Model Providers → Configurations → {'{provider}'} → Governance</strong> (provider-level) or <strong>Model Providers → Budgets & Limits</strong> section (model-level) before using them in routing rules.
					</p>

					<Separator />

					{/* Routing Target */}
					<div className="space-y-3">
						<Label>Routing Target</Label>
						<p className="text-muted-foreground text-xs">Leave provider or model empty to use the incoming request value</p>
						<div className="grid grid-cols-2 gap-4">
							<div className="space-y-2">
								<Label htmlFor="provider" className="text-sm">
									Provider
								</Label>
								<div className="flex gap-2">
									<Select value={provider} onValueChange={(value) => setValue("provider", value)}>
										<SelectTrigger className="flex-1">
											<SelectValue placeholder="Select provider (optional)" />
										</SelectTrigger>
										<SelectContent>
											{availableProviders.map((provider) => (
												<SelectItem key={provider} value={provider}>
													<div className="flex items-center gap-2">
														<RenderProviderIcon
															provider={provider as ProviderIconType}
															size="sm"
															className="h-4 w-4"
														/>
														<span>{getProviderLabel(provider)}</span>
													</div>
												</SelectItem>
											))}
										</SelectContent>
									</Select>
									{provider && (
										<Button
											type="button"
											variant="outline"
											size="sm"
											onClick={() => setValue("provider", "")}
											className="h-9 px-2"
											title="Clear provider selection"
										>
											<X className="h-4 w-4" />
										</Button>
									)}
								</div>
								{errors.provider && <p className="text-destructive text-sm">{errors.provider.message}</p>}
							</div>

							<div className="space-y-2">
								<Label htmlFor="model" className="text-sm">
									Model
								</Label>
								<ModelMultiselect
									provider={provider || undefined}
									value={model}
									onChange={(value) => setValue("model", value)}
									placeholder="Select a model... (optional)"
									isSingleSelect
									loadModelsOnEmptyProvider
								/>
								{errors.model && <p className="text-destructive text-sm">{errors.model.message}</p>}
							</div>
						</div>
					</div>

					{/* Fallbacks */}
					<div className="space-y-3">
						<div className="flex items-center justify-between">
							<Label>Fallbacks</Label>
							<Button
								type="button"
								variant="outline"
								size="sm"
								onClick={() => setValue("fallbacks", [...(fallbacks || []), ""])}
								className="gap-2"
							>
								<Plus className="h-4 w-4" />
								Add Fallback
							</Button>
						</div>
						<div className="space-y-2">
							{(fallbacks || []).length === 0 ? (
								<p className="text-muted-foreground text-sm">No fallbacks configured</p>
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
														<SelectValue placeholder="Select provider..." />
													</SelectTrigger>
													<SelectContent>
														{availableProviders.map((prov) => (
															<SelectItem key={prov} value={prov}>
																<div className="flex items-center gap-2">
																	<RenderProviderIcon
																		provider={prov as ProviderIconType}
																		size="sm"
																		className="h-4 w-4"
																	/>
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
													placeholder="Select model..."
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
											>
												<Trash2 className="h-4 w-4" />
											</Button>
										</div>
									);
								})
							)}
						</div>
						<p className="text-muted-foreground text-xs">Fallbacks will be used in the order they are defined</p>
					</div>

					{/* Action Buttons */}
					<div className="flex justify-end gap-3">
						<Button type="button" variant="outline" onClick={handleCancel} disabled={isLoading}>
							<X className="h-4 w-4" />
							Cancel
						</Button>
						<Button type="submit" disabled={isLoading}>
							<Save className="h-4 w-4" />
							{isEditing ? "Update Rule" : "Save Rule"}
						</Button>
					</div>
				</form>
			</SheetContent>
		</Sheet>
	);
}
