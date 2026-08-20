import { VirtualKeySelector } from "@/components/entitySelectors/virtualKeySelector";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { CodeEditor } from "@/components/ui/codeEditor";
import { ComboboxSelect } from "@/components/ui/combobox";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { getProviderLabel, RequestTypeLabels } from "@/lib/constants/logs";
import {
	getErrorMessage,
	useCreatePricingOverrideMutation,
	useGetProvidersQuery,
	useUpdatePricingOverrideMutation,
} from "@/lib/store";
import { useGetAllKeysQuery } from "@/lib/store/apis/providersApi";
import { getUserPicker } from "@/lib/registries/userPicker";
import { ModelProvider, RequestType } from "@/lib/types/config";
import {
	CreatePricingOverrideRequest,
	PricingOverride,
	PricingOverrideMatchType,
	PricingOverridePatch,
	PricingOverrideScopeKind,
} from "@/lib/types/governance";
import { cn } from "@/lib/utils";
import { ChevronDown, Save, X } from "lucide-react";
import { Dispatch, SetStateAction, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { PricingFieldSelector } from "./pricingFieldSelector";
// Side-effect import: registers the enterprise user picker (no-op in OSS builds).
import "@enterprise/lib/registrations/userPicker";

// Field metadata lives in ./pricingFields; re-exported here so existing
// importers of this module keep working unchanged.
export {
	fieldLabelByKey,
	getRequestTypeGroup,
	patchKeys,
	PRICING_FIELDS,
	REQUEST_TYPE_GROUPS,
	REQUEST_TYPE_OPTIONS,
} from "./pricingFields";
export type { FieldErrors, PricingFieldKey } from "./pricingFields";
import { fieldLabelByKey, patchKeys, PRICING_FIELDS, REQUEST_TYPE_GROUPS, REQUEST_TYPE_OPTIONS } from "./pricingFields";
import type { FieldErrors, PricingFieldKey } from "./pricingFields";

type ScopeRoot = "global" | "virtual_key" | "user";

export interface FormState {
	name: string;
	scopeRoot: ScopeRoot;
	userID: string;
	virtualKeyID: string;
	providerID: string;
	providerKeyID: string;
	matchType: PricingOverrideMatchType;
	pattern: string;
	requestTypes: RequestType[];
	pricingValues: Partial<Record<PricingFieldKey, string>>;
}

export const defaultFormState: FormState = {
	name: "",
	scopeRoot: "global",
	userID: "",
	virtualKeyID: "",
	providerID: "",
	providerKeyID: "",
	matchType: "exact",
	pattern: "",
	requestTypes: [],
	pricingValues: {},
};

export function patternError(matchType: PricingOverrideMatchType, pattern: string): string | undefined {
	const trimmed = pattern.trim();
	if (!trimmed) return "Pattern is required";
	if (matchType === "exact") {
		if (trimmed.includes("*")) return "Exact pattern cannot contain *";
	} else if (matchType === "wildcard") {
		const starCount = (trimmed.match(/\*/g) || []).length;
		if (starCount === 0) return "Wildcard pattern must end with * (example: gpt-5*)";
		if (starCount > 1) return "Wildcard pattern can include only one *";
		if (!trimmed.endsWith("*")) return "Wildcard supports prefix-only trailing *";
	}
	return undefined;
}

export function buildPatchFromForm(form: FormState): { patch: PricingOverridePatch; errors: FieldErrors } {
	const errors: FieldErrors = {};
	const patch: PricingOverridePatch = {};

	for (const key of patchKeys) {
		const raw = form.pricingValues[key];
		if (raw == null || raw.trim() === "") continue;
		const parsed = Number(raw);
		if (!Number.isFinite(parsed)) {
			errors[key] = "Must be a number";
			continue;
		}
		if (parsed < 0) {
			errors[key] = "Must be >= 0";
			continue;
		}
		(patch as Record<string, number>)[key] = parsed;
	}

	return { patch, errors };
}

function toFormState(override: PricingOverride): FormState {
	const values: Partial<Record<PricingFieldKey, string>> = {};
	let parsedPatch: Record<string, unknown> = {};
	try {
		if (override.pricing_patch) parsedPatch = JSON.parse(override.pricing_patch);
	} catch {
		// malformed patch — leave values empty
	}
	for (const key of patchKeys) {
		const val = parsedPatch[key];
		if (typeof val === "number") values[key] = String(val);
	}
	const scopeKind = resolveScopeKind(override);

	const scopeRoot: ScopeRoot =
		scopeKind === "virtual_key" || scopeKind === "virtual_key_provider" || scopeKind === "virtual_key_provider_key"
			? "virtual_key"
			: scopeKind === "user" || scopeKind === "user_provider" || scopeKind === "user_provider_key"
				? "user"
				: "global";

	return {
		name: override.name ?? "",
		scopeRoot,
		userID: override.user_id ?? "",
		virtualKeyID: override.virtual_key_id ?? "",
		providerID: override.provider_id ?? "",
		providerKeyID: override.provider_key_id ?? "",
		matchType: override.match_type,
		pattern: override.pattern,
		requestTypes: override.request_types ?? [],
		pricingValues: values,
	};
}

function resolveScopeKind(override: PricingOverride): PricingOverrideScopeKind {
	if (
		override.scope_kind === "global" ||
		override.scope_kind === "provider" ||
		override.scope_kind === "provider_key" ||
		override.scope_kind === "virtual_key" ||
		override.scope_kind === "virtual_key_provider" ||
		override.scope_kind === "virtual_key_provider_key" ||
		override.scope_kind === "user" ||
		override.scope_kind === "user_provider" ||
		override.scope_kind === "user_provider_key"
	) {
		return override.scope_kind;
	}
	if (override.virtual_key_id) {
		if (override.provider_key_id) return "virtual_key_provider_key";
		if (override.provider_id) return "virtual_key_provider";
		return "virtual_key";
	}
	if (override.user_id) {
		if (override.provider_key_id) return "user_provider_key";
		if (override.provider_id) return "user_provider";
		return "user";
	}
	if (override.provider_key_id) return "provider_key";
	if (override.provider_id) return "provider";
	return "global";
}

function deriveScopeKind(form: FormState): PricingOverrideScopeKind {
	if (form.scopeRoot === "virtual_key") {
		if (form.providerKeyID) return "virtual_key_provider_key";
		if (form.providerID) return "virtual_key_provider";
		return "virtual_key";
	}
	if (form.scopeRoot === "user") {
		if (form.providerKeyID) return "user_provider_key";
		if (form.providerID) return "user_provider";
		return "user";
	}
	if (form.providerKeyID) return "provider_key";
	if (form.providerID) return "provider";
	return "global";
}

export function patchSummary(override: PricingOverride): string {
	let parsed: Record<string, unknown> = {};
	try {
		if (override.pricing_patch) parsed = JSON.parse(override.pricing_patch);
	} catch {
		// ignore
	}
	const keys = Object.keys(parsed) as PricingFieldKey[];
	if (keys.length === 0) return "None";
	const labels = keys.map((key) => fieldLabelByKey[key] || key);
	if (labels.length <= 2) return labels.join(", ");
	return `${labels.slice(0, 2).join(", ")} +${labels.length - 2} more`;
}

export function renderFields(
	fields: ReadonlyArray<{ key: PricingFieldKey; label: string }>,
	form: FormState,
	setForm: Dispatch<SetStateAction<FormState>>,
	errors: FieldErrors,
	onFieldChange?: () => void,
) {
	return (
		<div className="grid grid-cols-1 gap-4 md:grid-cols-2">
			{fields.map((field) => (
				<div key={field.key} className="space-y-2 pb-1">
					<Label>{field.label}</Label>
					<Input
						data-testid={`pricing-override-field-input-${field.key}`}
						type="text"
						inputMode="decimal"
						className={cn(form.pricingValues[field.key]?.trim() && "ring-primary/40 ring-1")}
						value={form.pricingValues[field.key] ?? ""}
						onChange={(e) => {
							onFieldChange?.();
							setForm((prev) => ({
								...prev,
								pricingValues: { ...prev.pricingValues, [field.key]: e.target.value },
							}));
						}}
					/>
					{errors[field.key] && <p className="text-destructive text-xs">{errors[field.key]}</p>}
				</div>
			))}
		</div>
	);
}

interface PricingOverrideDrawerProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	editingOverride?: PricingOverride | null;
	scopeLock?: {
		scopeKind: PricingOverrideScopeKind;
		userID?: string;
		virtualKeyID?: string;
		providerID?: string;
		providerKeyID?: string;
		label?: string;
	};
	onSaved?: () => void;
}

function isCompleteScopeLock(scopeLock?: PricingOverrideDrawerProps["scopeLock"]): boolean {
	if (!scopeLock) return false;
	switch (scopeLock.scopeKind) {
		case "global":
			return true;
		case "provider":
			return Boolean(scopeLock.providerID);
		case "provider_key":
			return Boolean(scopeLock.providerKeyID);
		case "virtual_key":
			return Boolean(scopeLock.virtualKeyID);
		case "virtual_key_provider":
			return Boolean(scopeLock.virtualKeyID && scopeLock.providerID);
		case "virtual_key_provider_key":
			return Boolean(scopeLock.virtualKeyID && scopeLock.providerID && scopeLock.providerKeyID);
		case "user":
			return Boolean(scopeLock.userID);
		case "user_provider":
			return Boolean(scopeLock.userID && scopeLock.providerID);
		case "user_provider_key":
			return Boolean(scopeLock.userID && scopeLock.providerID && scopeLock.providerKeyID);
		default:
			return false;
	}
}

export default function PricingOverrideSheet({ open, onOpenChange, editingOverride, scopeLock, onSaved }: PricingOverrideDrawerProps) {
	const { data: providersData, isLoading: isProvidersLoading, error: providersError } = useGetProvidersQuery();
	const { data: allKeysData = [] } = useGetAllKeysQuery();
	const [createOverride, { isLoading: isCreating }] = useCreatePricingOverrideMutation();
	const [updateOverride, { isLoading: isPatching }] = useUpdatePricingOverrideMutation();

	const methods = useForm<FormState>({ defaultValues: defaultFormState });
	const { control, handleSubmit, setValue, watch, reset, getValues, setError, clearErrors } = methods;

	const [jsonPatch, setJSONPatch] = useState("");
	const [jsonError, setJSONError] = useState<string>();
	const jsonEditingRef = useRef(false);
	const prevOpenRef = useRef(false);
	const [requestTypePopoverOpen, setRequestTypePopoverOpen] = useState(false);

	const isSaving = isCreating || isPatching;
	const providers = useMemo<ModelProvider[]>(() => (providersError ? [] : (providersData ?? [])), [providersData, providersError]);

	const scopeRoot = watch("scopeRoot");
	const providerID = watch("providerID");
	const providerKeyID = watch("providerKeyID");
	const virtualKeyID = watch("virtualKeyID");
	const userID = watch("userID");
	const matchType = watch("matchType");
	const requestTypes = watch("requestTypes");
	const pricingValues = watch("pricingValues");

	const shouldLockScope = useMemo(() => !editingOverride && isCompleteScopeLock(scopeLock), [editingOverride, scopeLock]);

	// Registered by the downstream build at module load; undefined in builds
	// without a user directory, which hides the "User" scope root.
	const UserPicker = getUserPicker();

	const providerKeyOptions = useMemo(
		() =>
			allKeysData.map((key) => ({
				id: key.key_id,
				providerName: key.provider,
				label: key.name || key.key_id,
			})),
		[allKeysData],
	);
	const providerScopedKeyOptions = useMemo(
		() => providerKeyOptions.filter((key) => key.providerName === providerID),
		[providerKeyOptions, providerID],
	);

	// Hydrate the form only when the sheet transitions from closed → open.
	// This prevents providerKeyOptions refetches from resetting unsaved edits.
	useEffect(() => {
		const wasOpen = prevOpenRef.current;
		prevOpenRef.current = open;
		if (!open || wasOpen) return;

		jsonEditingRef.current = false;
		setJSONError(undefined);
		if (editingOverride) {
			const state = toFormState(editingOverride);
			// For provider_key scopes, provider_id is not stored in the DB (it's implicit from
			// the key). Derive it from providerKeyOptions so the provider selector renders and
			// the filtered key list shows the pre-selected key correctly.
			if (!state.providerID && state.providerKeyID) {
				const match = providerKeyOptions.find((k) => k.id === state.providerKeyID);
				if (match) state.providerID = match.providerName;
			}
			reset(state);
			return;
		}
		if (shouldLockScope && scopeLock) {
			reset({
				...defaultFormState,
				userID: scopeLock.userID ?? "",
				virtualKeyID: scopeLock.virtualKeyID ?? "",
				providerID: scopeLock.providerID ?? "",
				providerKeyID: scopeLock.providerKeyID ?? "",
				scopeRoot:
					scopeLock.scopeKind === "virtual_key" ||
						scopeLock.scopeKind === "virtual_key_provider" ||
						scopeLock.scopeKind === "virtual_key_provider_key"
						? "virtual_key"
						: scopeLock.scopeKind === "user" || scopeLock.scopeKind === "user_provider" || scopeLock.scopeKind === "user_provider_key"
							? "user"
							: "global",
			});
			return;
		}
		reset(defaultFormState);
	}, [open, editingOverride, scopeLock, shouldLockScope, providerKeyOptions, reset]);

	// When providerKeyOptions loads after the sheet is already open in edit mode,
	// backfill the derived providerID without resetting the rest of the form.
	useEffect(() => {
		if (!open || !editingOverride) return;
		const currentProviderID = getValues("providerID");
		const currentProviderKeyID = getValues("providerKeyID");
		if (currentProviderID || !currentProviderKeyID) return;
		const match = providerKeyOptions.find((k) => k.id === currentProviderKeyID);
		if (!match) return;
		setValue("providerID", match.providerName);
	}, [providerKeyOptions, open, editingOverride, getValues, setValue]);

	const resolvedScopeKind = useMemo(() => {
		if (shouldLockScope && scopeLock?.scopeKind) return scopeLock.scopeKind;
		return deriveScopeKind({ scopeRoot, providerID, providerKeyID } as FormState);
	}, [scopeLock, shouldLockScope, scopeRoot, providerID, providerKeyID]);

	const resolvedVirtualKeyID = useMemo(() => {
		if (shouldLockScope) return scopeLock?.virtualKeyID;
		return scopeRoot === "virtual_key" ? virtualKeyID || undefined : undefined;
	}, [scopeLock, shouldLockScope, scopeRoot, virtualKeyID]);

	const resolvedUserID = useMemo(() => {
		if (shouldLockScope) return scopeLock?.userID;
		return scopeRoot === "user" ? userID.trim() || undefined : undefined;
	}, [scopeLock, shouldLockScope, scopeRoot, userID]);

	const resolvedProviderID = useMemo(() => {
		if (shouldLockScope) return scopeLock?.providerID;
		return providerID || undefined;
	}, [scopeLock, shouldLockScope, providerID]);

	const resolvedProviderKeyID = useMemo(() => {
		if (shouldLockScope) return scopeLock?.providerKeyID;
		return providerKeyID || undefined;
	}, [scopeLock, shouldLockScope, providerKeyID]);

	const pricingFieldErrors = useMemo<FieldErrors>(() => {
		const errs: FieldErrors = {};
		for (const key of patchKeys) {
			const raw = pricingValues[key];
			if (!raw || raw.trim() === "") continue;
			const parsed = Number(raw);
			if (!Number.isFinite(parsed)) errs[key] = "Must be a number";
			else if (parsed < 0) errs[key] = "Must be >= 0";
		}
		return errs;
	}, [pricingValues]);

	useEffect(() => {
		if (!jsonEditingRef.current) {
			const { patch } = buildPatchFromForm(getValues());
			const json = Object.keys(patch).length > 0 ? JSON.stringify(patch, null, 2) : "";
			setJSONPatch(json);
			setJSONError(undefined);
		}
	}, [pricingValues, getValues]);

	const handleJSONChange = useCallback(
		(value: string) => {
			jsonEditingRef.current = true;
			setJSONPatch(value);
			const trimmed = value.trim();
			if (!trimmed) {
				setJSONError(undefined);
				setValue("pricingValues", {});
				return;
			}
			try {
				const parsed = JSON.parse(trimmed);
				if (parsed == null || typeof parsed !== "object" || Array.isArray(parsed)) {
					setJSONError("Patch must be a JSON object");
					return;
				}
				const newPricingValues: Partial<Record<PricingFieldKey, string>> = {};
				for (const [key, val] of Object.entries(parsed)) {
					if (!patchKeys.includes(key as PricingFieldKey)) {
						setJSONError(`Unknown field: ${key}`);
						return;
					}
					if (typeof val !== "number" || Number.isNaN(val) || val < 0) {
						setJSONError(`${key} must be a non-negative number`);
						return;
					}
					newPricingValues[key as PricingFieldKey] = String(val);
				}
				setJSONError(undefined);
				setValue("pricingValues", newPricingValues);
			} catch {
				setJSONError("Invalid JSON");
			}
		},
		[setValue],
	);

	const handleFieldChange = useCallback(() => {
		jsonEditingRef.current = false;
	}, []);

	const handleCloseDrawer = () => {
		onOpenChange(false);
		setRequestTypePopoverOpen(false);
	};

	const onSubmit = async (data: FormState) => {
		let hasErrors = false;

		if (
			!shouldLockScope &&
			(resolvedScopeKind === "virtual_key" ||
				resolvedScopeKind === "virtual_key_provider" ||
				resolvedScopeKind === "virtual_key_provider_key") &&
			!resolvedVirtualKeyID
		) {
			setError("virtualKeyID", { message: "Virtual key is required" });
			hasErrors = true;
		}

		if (
			!shouldLockScope &&
			(resolvedScopeKind === "user" || resolvedScopeKind === "user_provider" || resolvedScopeKind === "user_provider_key") &&
			!resolvedUserID
		) {
			setError("userID", { message: "User ID is required" });
			hasErrors = true;
		}

		const pError = patternError(data.matchType, data.pattern);
		if (pError) {
			setError("pattern", { message: pError });
			hasErrors = true;
		}

		if (data.requestTypes.length === 0) {
			setError("requestTypes", { message: "At least one request type must be selected" });
			hasErrors = true;
		}

		if (Object.keys(pricingFieldErrors).length > 0) {
			setError("pricingValues", { message: "Fix the pricing field errors above" });
			hasErrors = true;
		} else {
			const { patch } = buildPatchFromForm(data);
			if (Object.keys(patch).length === 0) {
				setError("pricingValues", { message: "At least one pricing field must be overridden" });
				hasErrors = true;
			}
		}

		if (hasErrors || jsonError) return;

		const { patch } = buildPatchFromForm(data);
		let scopedUserID: string | undefined;
		let scopedVirtualKeyID: string | undefined;
		let scopedProviderID: string | undefined;
		let scopedProviderKeyID: string | undefined;

		switch (resolvedScopeKind) {
			case "global":
				break;
			case "provider":
				scopedProviderID = resolvedProviderID;
				break;
			case "provider_key":
				scopedProviderKeyID = resolvedProviderKeyID;
				break;
			case "virtual_key":
				scopedVirtualKeyID = resolvedVirtualKeyID;
				break;
			case "virtual_key_provider":
				scopedVirtualKeyID = resolvedVirtualKeyID;
				scopedProviderID = resolvedProviderID;
				break;
			case "virtual_key_provider_key":
				scopedVirtualKeyID = resolvedVirtualKeyID;
				scopedProviderID = resolvedProviderID;
				scopedProviderKeyID = resolvedProviderKeyID;
				break;
			case "user":
				scopedUserID = resolvedUserID;
				break;
			case "user_provider":
				scopedUserID = resolvedUserID;
				scopedProviderID = resolvedProviderID;
				break;
			case "user_provider_key":
				scopedUserID = resolvedUserID;
				scopedProviderID = resolvedProviderID;
				scopedProviderKeyID = resolvedProviderKeyID;
				break;
		}

		const requestPayload: CreatePricingOverrideRequest = {
			name: data.name.trim(),
			scope_kind: resolvedScopeKind,
			user_id: scopedUserID,
			virtual_key_id: scopedVirtualKeyID,
			provider_id: scopedProviderID,
			provider_key_id: scopedProviderKeyID,
			match_type: data.matchType,
			pattern: data.pattern.trim(),
			request_types: data.requestTypes,
			patch,
		};

		try {
			if (editingOverride) {
				await updateOverride({ id: editingOverride.id, data: requestPayload }).unwrap();
				toast.success("Pricing override updated");
			} else {
				await createOverride(requestPayload).unwrap();
				toast.success("Pricing override created");
			}
			handleCloseDrawer();
			onSaved?.();
		} catch (error) {
			toast.error("Failed to save pricing override", { description: getErrorMessage(error) });
		}
	};

	return (
		<Sheet open={open} onOpenChange={(o) => (o ? onOpenChange(true) : handleCloseDrawer())}>
			<SheetContent side="right" className="dark:bg-card flex w-full flex-col overflow-x-hidden bg-white p-0 pt-4 sm:max-w-2xl">
				<SheetHeader className="flex flex-col items-start px-8 py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10">
					<SheetTitle className="">{editingOverride ? "Edit Pricing Override" : "Create Pricing Override"}</SheetTitle>
				</SheetHeader>

				<Form {...methods}>
					<form onSubmit={handleSubmit(onSubmit)} className="flex min-h-0 flex-1 flex-col">
						<div className="flex-1 space-y-6 px-8 pb-4">
							<div className="space-y-4">
								<FormField
									control={control}
									name="name"
									rules={{ required: "Name is required" }}
									render={({ field }) => (
										<FormItem>
											<FormLabel>
												Name <span className="text-red-500">*</span>
											</FormLabel>
											<FormControl>
												<Input data-testid="pricing-override-name-input" placeholder="e.g., GPT-4 Negotiated Rate" {...field} />
											</FormControl>
											<FormMessage />
										</FormItem>
									)}
								/>

								{shouldLockScope && scopeLock ? (
									<div className="space-y-2">
										<Label htmlFor="pricing-override-scope-lock-input">Scope</Label>
										<Input
											id="pricing-override-scope-lock-input"
											data-testid="pricing-override-scope-lock-input"
											value={scopeLock.label ?? scopeLock.scopeKind}
											readOnly
										/>
									</div>
								) : (
									<>
										<FormField
											control={control}
											name="scopeRoot"
											render={({ field }) => (
												<FormItem>
													<FormLabel>Scope root</FormLabel>
													<Select
														value={field.value}
														onValueChange={(value: ScopeRoot) => {
															field.onChange(value);
															setValue("virtualKeyID", "");
															setValue("userID", "");
															clearErrors("virtualKeyID");
															clearErrors("userID");
														}}
													>
														<FormControl>
															<SelectTrigger data-testid="pricing-override-scope-root-select" className="w-full">
																<SelectValue />
															</SelectTrigger>
														</FormControl>
														<SelectContent>
															<SelectItem value="global">Global</SelectItem>
															<SelectItem value="virtual_key">Virtual key</SelectItem>
															{(UserPicker || scopeRoot === "user") && <SelectItem value="user">User</SelectItem>}
														</SelectContent>
													</Select>
												</FormItem>
											)}
										/>

										{scopeRoot === "user" && (
											<FormField
												control={control}
												name="userID"
												render={({ field }) => (
													<FormItem>
														<FormLabel>
															User <span className="text-red-500">*</span>
														</FormLabel>
														<FormControl>
															{UserPicker ? (
																<UserPicker
																	value={field.value}
																	onChange={(value) => {
																		field.onChange(value);
																		clearErrors("userID");
																	}}
																	fallbackOption={
																		editingOverride?.user_id ? { value: editingOverride.user_id, label: editingOverride.user_id } : null
																	}
																/>
															) : (
																// No user directory in this build: keep a plain input so
																// existing user-scoped overrides remain editable.
																<Input
																	data-testid="pricing-override-user-id-input"
																	placeholder="Governance user ID"
																	{...field}
																	onChange={(e) => {
																		field.onChange(e);
																		clearErrors("userID");
																	}}
																/>
															)}
														</FormControl>
														<FormMessage />
													</FormItem>
												)}
											/>
										)}

										{scopeRoot === "virtual_key" && (
											<FormField
												control={control}
												name="virtualKeyID"
												render={({ field }) => (
													<FormItem>
														<FormLabel>
															Virtual key <span className="text-red-500">*</span>
														</FormLabel>
														<FormControl>
															<VirtualKeySelector
																value={field.value}
																onChange={(value) => {
																	field.onChange(value);
																	setValue("providerID", "");
																	setValue("providerKeyID", "");
																	clearErrors("virtualKeyID");
																}}
																fallbackOption={
																	editingOverride?.virtual_key_id
																		? { value: editingOverride.virtual_key_id, label: editingOverride.virtual_key_id }
																		: null
																}
																placeholder="Select virtual key"
															/>
														</FormControl>
														<FormMessage />
													</FormItem>
												)}
											/>
										)}

										<div className="grid grid-cols-2 gap-2">
											<FormField
												control={control}
												name="providerID"
												render={({ field }) => (
													<FormItem>
														<FormLabel>Provider</FormLabel>
														<Select
															value={field.value || "__none__"}
															onValueChange={(value) => {
																field.onChange(value === "__none__" ? "" : value);
																setValue("providerKeyID", "");
															}}
														>
															<FormControl>
																<SelectTrigger
																	data-testid="pricing-override-provider-select"
																	className="w-full"
																	disabled={isProvidersLoading || !!providersError}
																>
																	{isProvidersLoading ? (
																		<span className="text-muted-foreground">Loading...</span>
																	) : field.value ? (
																		<div className="flex items-center gap-1.5">
																			<RenderProviderIcon
																				provider={field.value as ProviderIconType}
																				size="sm"
																				className="h-4 w-4 shrink-0"
																			/>
																			<span>{getProviderLabel(field.value)}</span>
																		</div>
																	) : (
																		<span className="text-muted-foreground">All providers</span>
																	)}
																</SelectTrigger>
															</FormControl>
															<SelectContent>
																<SelectItem value="__none__">All providers</SelectItem>
																{providers.map((provider) => (
																	<SelectItem key={provider.name} value={provider.name}>
																		<div className="flex items-center gap-1.5">
																			<RenderProviderIcon
																				provider={provider.name as ProviderIconType}
																				size="sm"
																				className="h-4 w-4 shrink-0"
																			/>
																			<span>{getProviderLabel(provider.name)}</span>
																		</div>
																	</SelectItem>
																))}
															</SelectContent>
														</Select>
														{providersError ? (
															<p className="text-destructive mt-1 text-xs">Failed to load providers: {getErrorMessage(providersError)}</p>
														) : null}
													</FormItem>
												)}
											/>

											{providerID ? (
												<FormField
													control={control}
													name="providerKeyID"
													render={({ field }) => (
														<FormItem>
															<FormLabel>Provider key</FormLabel>
															<FormControl>
																<ComboboxSelect
																	data-testid="pricing-override-provider-key-select"
																	options={providerScopedKeyOptions.map((option) => ({ label: option.label, value: option.id }))}
																	value={field.value || null}
																	onValueChange={(value) => field.onChange(value ?? "")}
																	placeholder="All provider keys"
																	noPortal
																	className="h-9"
																/>
															</FormControl>
														</FormItem>
													)}
												/>
											) : (
												<div />
											)}
										</div>
									</>
								)}
							</div>

							<div className="space-y-2">
								<div className="grid grid-cols-[1fr_2fr] gap-2">
									<FormField
										control={control}
										name="matchType"
										render={({ field }) => (
											<FormItem>
												<FormLabel>Match type</FormLabel>
												<Select
													value={field.value}
													onValueChange={(value: PricingOverrideMatchType) => {
														field.onChange(value);
														clearErrors("pattern");
													}}
												>
													<FormControl>
														<SelectTrigger data-testid="pricing-override-match-type-select" className="w-full">
															<SelectValue placeholder="Select match type" />
														</SelectTrigger>
													</FormControl>
													<SelectContent>
														<SelectItem value="exact">Exact</SelectItem>
														<SelectItem value="wildcard">Wildcard</SelectItem>
													</SelectContent>
												</Select>
											</FormItem>
										)}
									/>
									<FormField
										control={control}
										name="pattern"
										render={({ field }) => (
											<FormItem>
												<FormLabel>
													Pattern <span className="text-red-500">*</span>
												</FormLabel>
												<FormControl>
													<Input
														data-testid="pricing-override-pattern-input"
														placeholder={matchType === "exact" ? "e.g., gpt-4o" : "e.g., gpt-4*"}
														{...field}
														onChange={(e) => {
															field.onChange(e);
															clearErrors("pattern");
														}}
													/>
												</FormControl>
												<FormMessage />
											</FormItem>
										)}
									/>
								</div>
							</div>

							<FormField
								control={control}
								name="requestTypes"
								render={({ field }) => (
									<FormItem>
										<FormLabel>
											Request types <span className="text-red-500">*</span>
										</FormLabel>
										<Popover open={requestTypePopoverOpen} onOpenChange={setRequestTypePopoverOpen} modal={false}>
											<PopoverTrigger asChild>
												<FormControl>
													<Button
														data-testid="pricing-override-request-types-btn"
														type="button"
														variant="outline"
														className="h-10 w-full justify-between"
													>
														<span className="truncate text-left">
															{field.value.length > 0 ? (
																field.value.map((rt) => RequestTypeLabels[rt as keyof typeof RequestTypeLabels] ?? rt).join(", ")
															) : (
																<span className="text-muted-foreground">Select request types...</span>
															)}
														</span>
														<ChevronDown className="h-4 w-4 shrink-0" />
													</Button>
												</FormControl>
											</PopoverTrigger>
											<PopoverContent align="start" className="w-[320px] p-2">
												<div className="max-h-72 space-y-1 overflow-y-auto" onWheel={(e) => e.stopPropagation()}>
													{REQUEST_TYPE_GROUPS.map((group) => (
														<div key={group.label}>
															<div className="text-muted-foreground px-2 py-1 text-xs font-medium">{group.label}</div>
															{group.types.map((requestType) => {
																const checked = field.value.includes(requestType as RequestType);
																return (
																	<label
																		key={requestType}
																		className="hover:bg-muted flex cursor-pointer items-center gap-2 rounded-sm px-2 py-1.5 text-sm"
																	>
																		<Checkbox
																			data-testid={`pricing-override-request-type-checkbox-${requestType}`}
																			checked={checked}
																			onCheckedChange={() => {
																				const current = field.value;
																				const next = current.includes(requestType as RequestType)
																					? current.filter((item) => item !== requestType)
																					: [...current, requestType as RequestType];
																				field.onChange(next);
																				if (next.length > 0) clearErrors("requestTypes");
																			}}
																		/>
																		<span>{RequestTypeLabels[requestType as keyof typeof RequestTypeLabels] ?? requestType}</span>
																	</label>
																);
															})}
														</div>
													))}
												</div>
												<div className="mt-2 flex justify-end">
													<Button
														data-testid="pricing-override-request-types-clear-btn"
														type="button"
														size="sm"
														variant="ghost"
														onClick={() => field.onChange([])}
													>
														Clear
													</Button>
												</div>
											</PopoverContent>
										</Popover>
										<FormMessage />
									</FormItem>
								)}
							/>

							<FormField
								control={control}
								name="pricingValues"
								render={({ field }) => (
									<FormItem>
										<FormLabel>
											Pricing fields <span className="text-red-500">*</span>{" "}
											<span className="text-muted-foreground text-xs font-normal">(USD per unit)</span>
										</FormLabel>
										<PricingFieldSelector
											key={open ? (editingOverride?.id ?? "new") : "closed"}
											values={field.value}
											errors={pricingFieldErrors}
											selectedRequestTypes={requestTypes}
											onChange={(key, value) => {
												handleFieldChange();
												field.onChange({ ...field.value, [key]: value });
												clearErrors("pricingValues");
											}}
											onFieldInteraction={handleFieldChange}
										/>
										<FormMessage />
									</FormItem>
								)}
							/>

							<div className="space-y-2">
								<Label className="text-muted-foreground text-xs">JSON</Label>
								<div className={cn("bg-muted/50 overflow-hidden rounded-md border", jsonError && "border-destructive")}>
									<CodeEditor
										lang="json"
										code={jsonPatch}
										onChange={handleJSONChange}
										minHeight={40}
										maxHeight={200}
										autoResize
										shouldAdjustInitialHeight
										options={{ lineNumbers: "off", scrollBeyondLastLine: false }}
									/>
								</div>
								{jsonError && <p className="text-destructive text-xs">{jsonError}</p>}
							</div>
						</div>

						<div className="bg-card sticky bottom-0 flex justify-end gap-3 border-t px-7 py-4">
							<Button
								data-testid="pricing-override-cancel-btn"
								type="button"
								variant="outline"
								onClick={handleCloseDrawer}
								disabled={isSaving}
							>
								<X className="h-4 w-4" />
								Cancel
							</Button>
							<Button data-testid="pricing-override-save-btn" type="submit" disabled={isSaving}>
								<Save className="h-4 w-4" />
								{editingOverride ? "Update Override" : "Save Override"}
							</Button>
						</div>
					</form>
				</Form>
			</SheetContent>
		</Sheet>
	);
}