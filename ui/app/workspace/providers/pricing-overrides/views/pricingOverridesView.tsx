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
} from "@/components/ui/alertDialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { MultiSelect } from "@/components/ui/multiSelect";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { getErrorMessage } from "@/lib/store";
import { useGetVirtualKeysQuery } from "@/lib/store/apis/governanceApi";
import {
	useCreatePricingOverrideMutation,
	useDeletePricingOverrideMutation,
	useGetPricingOverridesQuery,
	useUpdatePricingOverrideMutation,
} from "@/lib/store/apis/pricingOverridesApi";
import { useGetAllKeysQuery, useGetProvidersQuery } from "@/lib/store/apis/providersApi";
import {
	PRICING_OVERRIDE_REQUEST_TYPES,
	PRICING_OVERRIDE_SCOPES,
	CreatePricingOverrideRequest,
	PricingOverride,
	PricingOverrideMatchType,
	PricingOverrideScope,
	PricingOverrideRequestType,
} from "@/lib/types/pricingOverrides";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Edit, Plus, Trash2 } from "lucide-react";
import { useMemo, useState } from "react";
import { toast } from "sonner";

const PRICING_PATCH_FIELDS = [
	"input_cost_per_token",
	"output_cost_per_token",
	"input_cost_per_video_per_second",
	"input_cost_per_audio_per_second",
	"input_cost_per_character",
	"output_cost_per_character",
	"input_cost_per_token_above_128k_tokens",
	"input_cost_per_character_above_128k_tokens",
	"input_cost_per_image_above_128k_tokens",
	"input_cost_per_video_per_second_above_128k_tokens",
	"input_cost_per_audio_per_second_above_128k_tokens",
	"output_cost_per_token_above_128k_tokens",
	"output_cost_per_character_above_128k_tokens",
	"input_cost_per_token_above_200k_tokens",
	"output_cost_per_token_above_200k_tokens",
	"cache_creation_input_token_cost_above_200k_tokens",
	"cache_read_input_token_cost_above_200k_tokens",
	"cache_read_input_token_cost",
	"cache_creation_input_token_cost",
	"input_cost_per_token_batches",
	"output_cost_per_token_batches",
	"input_cost_per_image_token",
	"output_cost_per_image_token",
	"input_cost_per_image",
	"output_cost_per_image",
	"cache_read_input_image_token_cost",
] as const;

type PricingPatchField = (typeof PRICING_PATCH_FIELDS)[number];
type PricingPatch = Partial<Record<PricingPatchField, number>>;

interface PricingOverrideFormState {
	name: string;
	enabled: boolean;
	scope: PricingOverrideScope;
	scope_id: string;
	model_pattern: string;
	match_type: PricingOverrideMatchType;
	request_types: PricingOverrideRequestType[];
	pricing_patch_json: string;
}

const defaultFormState: PricingOverrideFormState = {
	name: "",
	enabled: true,
	scope: "global",
	scope_id: "",
	model_pattern: "",
	match_type: "exact",
	request_types: [],
	pricing_patch_json: "{}",
};

function formatScope(scope: PricingOverrideScope) {
	return PRICING_OVERRIDE_SCOPES.find((item) => item.value === scope)?.label ?? scope;
}

function formatTimestamp(ts?: string) {
	if (!ts) return "-";
	const date = new Date(ts);
	if (Number.isNaN(date.getTime())) return "-";
	return date.toLocaleString();
}

function extractPricingPatch(override: PricingOverride): PricingPatch {
	const patch: PricingPatch = {};
	for (const field of PRICING_PATCH_FIELDS) {
		const value = (override as unknown as Record<string, unknown>)[field];
		if (typeof value === "number") {
			patch[field] = value;
		}
	}
	return patch;
}

function parsePricingPatch(raw: string): { patch?: PricingPatch; error?: string } {
	const trimmed = raw.trim();
	if (!trimmed || trimmed === "{}") {
		return { error: "Pricing patch must include at least one pricing field" };
	}

	let parsed: unknown;
	try {
		parsed = JSON.parse(trimmed);
	} catch {
		return { error: "Pricing patch must be valid JSON" };
	}

	if (!parsed || Array.isArray(parsed) || typeof parsed !== "object") {
		return { error: "Pricing patch must be a JSON object" };
	}

	const patch: PricingPatch = {};
	for (const [key, value] of Object.entries(parsed as Record<string, unknown>)) {
		if (!PRICING_PATCH_FIELDS.includes(key as PricingPatchField)) {
			return { error: `Unsupported pricing field: ${key}` };
		}
		if (typeof value !== "number" || Number.isNaN(value)) {
			return { error: `Pricing field ${key} must be a number` };
		}
		if (value < 0) {
			return { error: `Pricing field ${key} must be non-negative` };
		}
		patch[key as PricingPatchField] = value;
	}

	return { patch };
}

function toFormState(override: PricingOverride): PricingOverrideFormState {
	const patch = extractPricingPatch(override);
	return {
		name: override.name,
		enabled: override.enabled,
		scope: override.scope,
		scope_id: override.scope_id || "",
		model_pattern: override.model_pattern,
		match_type: override.match_type,
		request_types: (override.request_types || []) as PricingOverrideRequestType[],
		pricing_patch_json: JSON.stringify(patch, null, 2),
	};
}

function withCurrentOption(options: { label: string; value: string }[], currentValue: string) {
	if (!currentValue) return options;
	if (options.some((item) => item.value === currentValue)) return options;
	return [{ label: `Unknown (${currentValue})`, value: currentValue }, ...options];
}

export function PricingOverridesView() {
	const canCreate = useRbac(RbacResource.Governance, RbacOperation.Create);
	const canUpdate = useRbac(RbacResource.Governance, RbacOperation.Update);
	const canDelete = useRbac(RbacResource.Governance, RbacOperation.Delete);

	const { data: pricingOverrides = [], isLoading } = useGetPricingOverridesQuery();
	const { data: providers = [] } = useGetProvidersQuery();
	const { data: allKeys = [] } = useGetAllKeysQuery();
	const { data: virtualKeysData = { virtual_keys: [] } } = useGetVirtualKeysQuery();

	const [createPricingOverride, { isLoading: isCreating }] = useCreatePricingOverrideMutation();
	const [updatePricingOverride, { isLoading: isUpdating }] = useUpdatePricingOverrideMutation();
	const [deletePricingOverride, { isLoading: isDeleting }] = useDeletePricingOverrideMutation();

	const [editorOpen, setEditorOpen] = useState(false);
	const [editingOverride, setEditingOverride] = useState<PricingOverride | null>(null);
	const [formState, setFormState] = useState<PricingOverrideFormState>(defaultFormState);
	const [overrideToDelete, setOverrideToDelete] = useState<PricingOverride | null>(null);

	const sortedOverrides = useMemo(() => {
		return [...pricingOverrides].sort((a, b) => {
			const aTime = new Date(a.updated_at || a.created_at).getTime();
			const bTime = new Date(b.updated_at || b.created_at).getTime();
			return bTime - aTime;
		});
	}, [pricingOverrides]);

	const providerOptions = useMemo(
		() =>
			providers
				.map((provider) => ({ label: provider.name, value: provider.name }))
				.sort((a, b) => a.label.localeCompare(b.label)),
		[providers],
	);

	const providerKeyOptions = useMemo(
		() =>
			allKeys
				.map((key) => ({
					label: `${key.name} (${key.provider})`,
					value: key.key_id,
				}))
				.sort((a, b) => a.label.localeCompare(b.label)),
		[allKeys],
	);

	const virtualKeyOptions = useMemo(
		() =>
			(virtualKeysData.virtual_keys || [])
				.map((key) => ({
					label: key.name,
					value: key.id,
				}))
				.sort((a, b) => a.label.localeCompare(b.label)),
		[virtualKeysData.virtual_keys],
	);

	const activeScopeOptions = useMemo(() => {
		if (formState.scope === "provider") {
			return withCurrentOption(providerOptions, formState.scope_id);
		}
		if (formState.scope === "provider_key") {
			return withCurrentOption(providerKeyOptions, formState.scope_id);
		}
		if (formState.scope === "virtual_key") {
			return withCurrentOption(virtualKeyOptions, formState.scope_id);
		}
		return [];
	}, [formState.scope, formState.scope_id, providerOptions, providerKeyOptions, virtualKeyOptions]);

	const openCreateSheet = () => {
		setEditingOverride(null);
		setFormState(defaultFormState);
		setEditorOpen(true);
	};

	const openEditSheet = (override: PricingOverride) => {
		setEditingOverride(override);
		setFormState(toFormState(override));
		setEditorOpen(true);
	};

	const buildScopePayload = () => {
		if (formState.scope === "global") {
			return { scope: "global" as PricingOverrideScope, scope_id: undefined };
		}
		return {
			scope: formState.scope,
			scope_id: formState.scope_id.trim(),
		};
	};

	const validateForm = () => {
		if (!formState.name.trim()) {
			return "Name is required";
		}
		if (!formState.model_pattern.trim()) {
			return "Model pattern is required";
		}
		if (formState.scope !== "global") {
			const selectedScopeTarget = formState.scope_id.trim();
			if (!selectedScopeTarget) {
				return "Scope target is required for non-global scope";
			}
			let isValidScopeTarget = false;
			switch (formState.scope) {
			case "provider":
				isValidScopeTarget = providerOptions.some((option) => option.value === selectedScopeTarget);
				break;
			case "provider_key":
				isValidScopeTarget = providerKeyOptions.some((option) => option.value === selectedScopeTarget);
				break;
			case "virtual_key":
				isValidScopeTarget = virtualKeyOptions.some((option) => option.value === selectedScopeTarget);
				break;
			default:
				isValidScopeTarget = false;
			}
			if (!isValidScopeTarget) {
				return "Valid scope target is required";
			}
		}
		if (formState.match_type === "exact" && formState.model_pattern.includes("*")) {
			return "Exact match pattern cannot contain '*'";
		}
		if (formState.match_type === "wildcard" && !formState.model_pattern.includes("*")) {
			return "Wildcard pattern must contain '*'";
		}
		if (formState.match_type === "regex") {
			try {
				new RegExp(formState.model_pattern);
			} catch {
				return "Invalid regex pattern";
			}
		}
		return undefined;
	};

	const handleSave = async () => {
		const validationError = validateForm();
		if (validationError) {
			toast.error(validationError);
			return;
		}

		const parsedPatch = parsePricingPatch(formState.pricing_patch_json);
		if (parsedPatch.error || !parsedPatch.patch) {
			toast.error(parsedPatch.error || "Invalid pricing patch");
			return;
		}

		const scopePayload = buildScopePayload();
		const payload: CreatePricingOverrideRequest = {
			name: formState.name.trim(),
			enabled: formState.enabled,
			scope: scopePayload.scope,
			scope_id: scopePayload.scope_id,
			model_pattern: formState.model_pattern.trim(),
			match_type: formState.match_type,
			request_types: formState.request_types.length > 0 ? formState.request_types : undefined,
			...parsedPatch.patch,
		};

		try {
			if (editingOverride) {
				await updatePricingOverride({ id: editingOverride.id, data: payload }).unwrap();
				toast.success("Pricing override updated successfully");
			} else {
				await createPricingOverride(payload).unwrap();
				toast.success("Pricing override created successfully");
			}
			setEditorOpen(false);
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const handleDelete = async () => {
		if (!overrideToDelete) return;
		try {
			await deletePricingOverride(overrideToDelete.id).unwrap();
			toast.success("Pricing override deleted successfully");
			setOverrideToDelete(null);
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const isSaving = isCreating || isUpdating;

	return (
		<div className="space-y-4">
			<div className="flex items-center justify-between">
				<div>
					<h1 className="text-foreground text-lg font-semibold">Pricing Overrides</h1>
					<p className="text-muted-foreground text-sm">Define scoped per-model pricing patches across global, provider, provider key, and virtual key scopes</p>
				</div>
				{canCreate && (
					<Button
						onClick={openCreateSheet}
						disabled={isLoading}
						className="gap-2"
						data-testid="pricing-overrides-new-button"
					>
						<Plus className="h-4 w-4" />
						New Override
					</Button>
				)}
			</div>

			<div className="rounded-sm border overflow-hidden">
				<Table>
					<TableHeader>
						<TableRow className="bg-muted/50">
							<TableHead>Name</TableHead>
							<TableHead>Scope</TableHead>
							<TableHead>Model Pattern</TableHead>
							<TableHead>Match</TableHead>
							<TableHead>Request Types</TableHead>
							<TableHead>Status</TableHead>
							<TableHead>Updated</TableHead>
							<TableHead className="text-right">Actions</TableHead>
						</TableRow>
					</TableHeader>
					<TableBody>
						{isLoading &&
							[...Array(4)].map((_, index) => (
								<TableRow key={`pricing-override-loading-${index}`}>
									<TableCell colSpan={8}>
										<div className="h-2 w-40 rounded bg-muted animate-pulse" />
									</TableCell>
								</TableRow>
							))}
						{!isLoading && sortedOverrides.length === 0 && (
							<TableRow>
								<TableCell colSpan={8} className="py-12 text-center">
									<p className="font-medium">No pricing overrides yet</p>
									<p className="text-muted-foreground text-sm">Create an override to patch model pricing for a specific scope</p>
								</TableCell>
							</TableRow>
						)}
						{!isLoading &&
							sortedOverrides.map((override) => (
								<TableRow key={override.id} data-testid={`pricing-override-row-${override.id}`}>
									<TableCell className="font-medium">
										<span className="truncate max-w-[240px]">{override.name}</span>
									</TableCell>
									<TableCell>
										<div className="flex flex-col gap-1">
											<Badge variant="secondary">{formatScope(override.scope)}</Badge>
											{override.scope_id && <span className="font-mono text-xs text-muted-foreground">{override.scope_id}</span>}
										</div>
									</TableCell>
									<TableCell className="font-mono text-xs">{override.model_pattern}</TableCell>
									<TableCell className="capitalize">{override.match_type}</TableCell>
									<TableCell>
										{override.request_types && override.request_types.length > 0 ? (
											<span className="text-sm">{override.request_types.length} selected</span>
										) : (
											<span className="text-muted-foreground text-sm">All</span>
										)}
									</TableCell>
									<TableCell>
										<Badge variant={override.enabled ? "default" : "secondary"}>{override.enabled ? "Enabled" : "Disabled"}</Badge>
									</TableCell>
									<TableCell className="text-sm">{formatTimestamp(override.updated_at || override.created_at)}</TableCell>
									<TableCell className="text-right">
										<div className="flex items-center justify-end gap-2">
											{canUpdate && (
												<Button
													variant="ghost"
													size="sm"
													onClick={() => openEditSheet(override)}
													data-testid={`pricing-override-edit-${override.id}`}
												>
													<Edit className="h-4 w-4" />
												</Button>
											)}
											{canDelete && (
												<Button
													variant="ghost"
													size="sm"
													onClick={() => setOverrideToDelete(override)}
													data-testid={`pricing-override-delete-${override.id}`}
												>
													<Trash2 className="h-4 w-4" />
												</Button>
											)}
										</div>
									</TableCell>
								</TableRow>
							))}
					</TableBody>
				</Table>
			</div>

			<Sheet open={editorOpen} onOpenChange={setEditorOpen}>
				<SheetContent className="dark:bg-card flex w-full flex-col min-w-1/2 gap-4 overflow-x-hidden bg-white p-8">
					<SheetHeader className="flex flex-col items-start">
						<SheetTitle>{editingOverride ? "Edit Pricing Override" : "Create Pricing Override"}</SheetTitle>
						<SheetDescription>
							{editingOverride
								? "Update the scoped pricing override configuration"
								: "Create a scoped pricing override to patch pricing for matching models"}
						</SheetDescription>
					</SheetHeader>

					<div className="space-y-6">
						<div className="grid grid-cols-2 gap-4">
							<div className="space-y-2">
								<Label htmlFor="pricing-override-name">Name</Label>
								<Input
									id="pricing-override-name"
									value={formState.name}
									onChange={(event) => setFormState((prev) => ({ ...prev, name: event.target.value }))}
									data-testid="pricing-override-name-input"
								/>
							</div>
							<div className="space-y-2">
								<Label htmlFor="pricing-override-enabled">Enabled</Label>
								<div className="flex h-10 items-center rounded-sm border px-3">
									<Switch
										id="pricing-override-enabled"
										checked={formState.enabled}
										onCheckedChange={(checked) => setFormState((prev) => ({ ...prev, enabled: checked }))}
									/>
								</div>
							</div>
						</div>

						<div className="grid grid-cols-2 gap-4">
							<div className="space-y-2">
								<Label>Scope</Label>
								<Select
									value={formState.scope}
									onValueChange={(value) =>
										setFormState((prev) => ({
											...prev,
											scope: value as PricingOverrideScope,
											scope_id: "",
										}))
									}
								>
									<SelectTrigger data-testid="pricing-override-scope-select">
										<SelectValue placeholder="Select scope" />
									</SelectTrigger>
									<SelectContent>
										{PRICING_OVERRIDE_SCOPES.map((scopeOption) => (
											<SelectItem key={scopeOption.value} value={scopeOption.value}>
												{scopeOption.label}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
							</div>
							<div className="space-y-2">
								<Label>Scope Value</Label>
								{formState.scope === "global" ? (
									<div className="text-muted-foreground flex h-10 items-center rounded-sm border px-3 text-sm">Not required for global scope</div>
								) : (
									<Select
										value={formState.scope_id || undefined}
										onValueChange={(value) => setFormState((prev) => ({ ...prev, scope_id: value }))}
									>
										<SelectTrigger data-testid="pricing-override-scope-id-select">
											<SelectValue placeholder="Select value" />
										</SelectTrigger>
										<SelectContent>
											{activeScopeOptions.map((option) => (
												<SelectItem key={option.value} value={option.value}>
													{option.label}
												</SelectItem>
											))}
										</SelectContent>
									</Select>
								)}
							</div>
						</div>

						<div className="grid grid-cols-2 gap-4">
							<div className="space-y-2">
								<Label htmlFor="pricing-override-model-pattern">Model Pattern</Label>
								<Input
									id="pricing-override-model-pattern"
									value={formState.model_pattern}
									onChange={(event) => setFormState((prev) => ({ ...prev, model_pattern: event.target.value }))}
									placeholder="gpt-4o, gpt-4o-* or ^gpt-4o.*$"
									className="font-mono"
									data-testid="pricing-override-model-pattern-input"
								/>
							</div>
							<div className="space-y-2">
								<Label>Match Type</Label>
								<Select
									value={formState.match_type}
									onValueChange={(value) => setFormState((prev) => ({ ...prev, match_type: value as PricingOverrideMatchType }))}
								>
									<SelectTrigger data-testid="pricing-override-match-type-select">
										<SelectValue />
									</SelectTrigger>
									<SelectContent>
										<SelectItem value="exact">Exact</SelectItem>
										<SelectItem value="wildcard">Wildcard</SelectItem>
										<SelectItem value="regex">Regex</SelectItem>
									</SelectContent>
								</Select>
							</div>
						</div>

						<div className="space-y-2">
							<Label>Request Types (optional)</Label>
							<MultiSelect
								key={`${editingOverride?.id ?? "new"}-request-types`}
								options={PRICING_OVERRIDE_REQUEST_TYPES.map((type) => ({
									label: type.label,
									value: type.value,
								}))}
								defaultValue={formState.request_types}
								onValueChange={(values) =>
									setFormState((prev) => ({
										...prev,
										request_types: values as PricingOverrideRequestType[],
									}))
								}
								placeholder="All request types"
								variant="default"
								className="w-full bg-white dark:bg-zinc-800"
								commandClassName="w-full max-w-96 [&_[cmdk-item][data-selected=true]]:bg-muted [&_[cmdk-item][data-selected=true]]:text-foreground"
								animation={0}
								data-testid="pricing-override-request-types-select"
							/>
						</div>

						<div className="space-y-2">
							<Label htmlFor="pricing-override-patch-json">Pricing Patch JSON</Label>
							<Textarea
								id="pricing-override-patch-json"
								value={formState.pricing_patch_json}
								onChange={(event) => setFormState((prev) => ({ ...prev, pricing_patch_json: event.target.value }))}
								rows={12}
								className="font-mono text-xs"
								data-testid="pricing-override-patch-json-input"
							/>
							<p className="text-muted-foreground text-xs">
								Provide only fields you want to override, for example:{" "}
								<code>{`{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002}`}</code>
							</p>
						</div>

						<div className="flex justify-end gap-2 pt-2">
							<Button variant="outline" onClick={() => setEditorOpen(false)} disabled={isSaving}>
								Cancel
							</Button>
							<Button onClick={handleSave} disabled={isSaving || (!editingOverride && !canCreate) || (!!editingOverride && !canUpdate)} data-testid="pricing-override-save-button">
								{isSaving ? "Saving..." : editingOverride ? "Save Changes" : "Create Override"}
							</Button>
						</div>
					</div>
				</SheetContent>
			</Sheet>

			<AlertDialog open={!!overrideToDelete} onOpenChange={(open) => !open && setOverrideToDelete(null)}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Delete Pricing Override</AlertDialogTitle>
						<AlertDialogDescription>
							Are you sure you want to delete &quot;{overrideToDelete?.name}&quot;? This action cannot be undone.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel disabled={isDeleting}>Cancel</AlertDialogCancel>
						<AlertDialogAction onClick={handleDelete} disabled={isDeleting} className="bg-destructive hover:bg-destructive/90">
							{isDeleting ? "Deleting..." : "Delete"}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</div>
	);
}
