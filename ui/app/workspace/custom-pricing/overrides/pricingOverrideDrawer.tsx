"use client";

import { CodeEditor } from "@/app/workspace/logs/views/codeEditor";
import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from "@/components/ui/accordion";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { DottedSeparator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import {
	getErrorMessage,
	useCreatePricingOverrideMutation,
	useGetProvidersQuery,
	useGetVirtualKeysQuery,
	usePatchPricingOverrideMutation,
} from "@/lib/store";
import { RequestTypeLabels } from "@/lib/constants/logs";
import { ModelProvider } from "@/lib/types/config";
import {
	CreatePricingOverrideRequest,
	PatchPricingOverrideRequest,
	PricingOverride,
	PricingOverrideMatchType,
	PricingOverridePatch,
	PricingOverrideScopeKind,
} from "@/lib/types/governance";
import { cn } from "@/lib/utils";
import { ChevronDown } from "lucide-react";
import { Dispatch, SetStateAction, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";

export const REQUEST_TYPE_GROUPS = [
	{
		label: "Text & Embeddings",
		types: [
			"text_completion",
			"text_completion_stream",
			"chat_completion",
			"chat_completion_stream",
			"responses",
			"responses_stream",
			"embedding",
			"rerank",
		],
	},
	{
		label: "Audio",
		types: ["speech", "speech_stream", "transcription", "transcription_stream"],
	},
	{
		label: "Image",
		types: ["image_generation", "image_generation_stream", "image_edit", "image_edit_stream", "image_variation"],
	},
	{
		label: "Video",
		types: ["video_generation"],
	},
] as const;

export const REQUEST_TYPE_OPTIONS = REQUEST_TYPE_GROUPS.flatMap((g) => g.types);

export function getRequestTypeGroup(rt: string): string | undefined {
	return REQUEST_TYPE_GROUPS.find((g) => (g.types as readonly string[]).includes(rt))?.label;
}

export const PRICING_FIELDS = [
	{ key: "input_cost_per_token", label: "Input / token", group: "token" },
	{ key: "output_cost_per_token", label: "Output / token", group: "token" },
	{ key: "input_cost_per_character", label: "Input / character", group: "token" },
	{ key: "output_cost_per_character", label: "Output / character", group: "token" },
	{ key: "input_cost_per_token_batches", label: "Input / token (batch)", group: "token" },
	{ key: "output_cost_per_token_batches", label: "Output / token (batch)", group: "token" },
	{ key: "input_cost_per_token_priority", label: "Input / token (priority)", group: "token" },
	{ key: "output_cost_per_token_priority", label: "Output / token (priority)", group: "token" },
	{ key: "input_cost_per_token_above_128k_tokens", label: "Input / token (>128k)", group: "token" },
	{ key: "output_cost_per_token_above_128k_tokens", label: "Output / token (>128k)", group: "token" },
	{ key: "input_cost_per_character_above_128k_tokens", label: "Input / character (>128k)", group: "token" },
	{ key: "output_cost_per_character_above_128k_tokens", label: "Output / character (>128k)", group: "token" },
	{ key: "input_cost_per_token_above_200k_tokens", label: "Input / token (>200k)", group: "token" },
	{ key: "output_cost_per_token_above_200k_tokens", label: "Output / token (>200k)", group: "token" },
	{ key: "cache_creation_input_token_cost", label: "Cache creation / token", group: "cache" },
	{ key: "cache_read_input_token_cost", label: "Cache read / token", group: "cache" },
	{ key: "cache_creation_input_token_cost_above_200k_tokens", label: "Cache creation / token (>200k)", group: "cache" },
	{ key: "cache_read_input_token_cost_above_200k_tokens", label: "Cache read / token (>200k)", group: "cache" },
	{ key: "cache_creation_input_token_cost_above_1hr", label: "Cache creation / token (>1hr)", group: "cache" },
	{ key: "cache_creation_input_token_cost_above_1hr_above_200k_tokens", label: "Cache creation / token (>1hr, >200k)", group: "cache" },
	{ key: "cache_creation_input_audio_token_cost", label: "Cache creation / audio token", group: "cache" },
	{ key: "cache_read_input_token_cost_priority", label: "Cache read / token (priority)", group: "cache" },
	{ key: "input_cost_per_image_token", label: "Input / image token", group: "image" },
	{ key: "output_cost_per_image_token", label: "Output / image token", group: "image" },
	{ key: "input_cost_per_image", label: "Input / image", group: "image" },
	{ key: "input_cost_per_image_above_128k_tokens", label: "Input / image (>128k)", group: "image" },
	{ key: "input_cost_per_pixel", label: "Input / pixel", group: "image" },
	{ key: "output_cost_per_image", label: "Output / image", group: "image" },
	{ key: "output_cost_per_pixel", label: "Output / pixel", group: "image" },
	{ key: "output_cost_per_image_premium_image", label: "Output / image (premium)", group: "image" },
	{ key: "output_cost_per_image_above_512_and_512_pixels", label: "Output / image (>512px)", group: "image" },
	{ key: "output_cost_per_image_above_512_and_512_pixels_and_premium_image", label: "Output / image (>512px, premium)", group: "image" },
	{ key: "output_cost_per_image_above_1024_and_1024_pixels", label: "Output / image (>1024px)", group: "image" },
	{ key: "output_cost_per_image_above_1024_and_1024_pixels_and_premium_image", label: "Output / image (>1024px, premium)", group: "image" },
	{ key: "cache_read_input_image_token_cost", label: "Cache read / image token", group: "image" },
	{ key: "input_cost_per_audio_token", label: "Input / audio token", group: "av" },
	{ key: "input_cost_per_audio_per_second", label: "Input / audio second", group: "av" },
	{ key: "input_cost_per_audio_per_second_above_128k_tokens", label: "Input / audio second (>128k)", group: "av" },
	{ key: "input_cost_per_second", label: "Input / second", group: "av" },
	{ key: "input_cost_per_video_per_second", label: "Input / video second", group: "av" },
	{ key: "input_cost_per_video_per_second_above_128k_tokens", label: "Input / video second (>128k)", group: "av" },
	{ key: "output_cost_per_audio_token", label: "Output / audio token", group: "av" },
	{ key: "output_cost_per_video_per_second", label: "Output / video second", group: "av" },
	{ key: "output_cost_per_second", label: "Output / second", group: "av" },
	{ key: "search_context_cost_per_query", label: "Search context / query", group: "other" },
	{ key: "code_interpreter_cost_per_session", label: "Code interpreter / session", group: "other" },
] as const;

export type PricingFieldKey = (typeof PRICING_FIELDS)[number]["key"];
export type FieldErrors = Partial<Record<PricingFieldKey | "name" | "scope" | "pattern" | "patch", string>>;

type ScopeRoot = "global" | "virtual_key";

export interface FormState {
	name: string;
	scopeRoot: ScopeRoot;
	virtualKeyID: string;
	providerID: string;
	providerKeyID: string;
	matchType: PricingOverrideMatchType;
	pattern: string;
	requestTypes: string[];
	pricingValues: Partial<Record<PricingFieldKey, string>>;
}

export const defaultFormState: FormState = {
	name: "",
	scopeRoot: "global",
	virtualKeyID: "",
	providerID: "",
	providerKeyID: "",
	matchType: "exact",
	pattern: "",
	requestTypes: [],
	pricingValues: {},
};

export const fieldLabelByKey = Object.fromEntries(PRICING_FIELDS.map((field) => [field.key, field.label])) as Record<
	PricingFieldKey,
	string
>;
export const patchKeys = PRICING_FIELDS.map((field) => field.key) as PricingFieldKey[];

export function patternError(matchType: PricingOverrideMatchType, pattern: string): string | undefined {
	const trimmed = pattern.trim();
	if (!trimmed) return "Pattern is required";
	if (matchType === "wildcard") {
		const starCount = (trimmed.match(/\*/g) || []).length;
		if (starCount === 0) return "Wildcard pattern must end with * (example: gpt-5*)";
		if (starCount > 1) return "Wildcard pattern can include only one *";
		if (!trimmed.endsWith("*")) return "Wildcard supports prefix-only trailing *";
		if (trimmed === "*") return "Pattern cannot be just *";
		const withoutTrailing = trimmed.slice(0, -1);
		if (withoutTrailing.includes("*")) return "Wildcard cannot include * in the middle";
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
	for (const key of patchKeys) {
		const val = override.patch?.[key];
		if (typeof val === "number") values[key] = String(val);
	}
	const scopeKind = resolveScopeKind(override);

	const scopeRoot: ScopeRoot =
		scopeKind === "virtual_key" || scopeKind === "virtual_key_provider" || scopeKind === "virtual_key_provider_key"
			? "virtual_key"
			: "global";

	return {
		name: override.name ?? "",
		scopeRoot,
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
		override.scope_kind === "virtual_key_provider_key"
	) {
		return override.scope_kind;
	}
	if (override.virtual_key_id) {
		if (override.provider_key_id) return "virtual_key_provider_key";
		if (override.provider_id) return "virtual_key_provider";
		return "virtual_key";
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
	if (form.providerKeyID) return "provider_key";
	if (form.providerID) return "provider";
	return "global";
}

export function patchSummary(override: PricingOverride): string {
	const keys = Object.keys(override.patch || {}) as PricingFieldKey[];
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
) {
	return (
		<div className="grid grid-cols-1 gap-4 md:grid-cols-2">
			{fields.map((field) => (
				<div key={field.key} className="space-y-2 pb-1">
					<Label>{field.label}</Label>
					<Input
						type="text"
						inputMode="decimal"
						className={cn(form.pricingValues[field.key]?.trim() && "ring-primary/40 ring-1")}
						value={form.pricingValues[field.key] ?? ""}
						onChange={(e) =>
							setForm((prev) => ({
								...prev,
								pricingValues: { ...prev.pricingValues, [field.key]: e.target.value },
							}))
						}
					/>
					{errors[field.key] && <p className="text-destructive text-xs">{errors[field.key]}</p>}
				</div>
			))}
		</div>
	);
}

function countFieldsWithValues(fields: ReadonlyArray<{ key: PricingFieldKey }>, form: FormState): number {
	return fields.filter((f) => form.pricingValues[f.key]?.trim()).length;
}

interface PricingOverrideDrawerProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	editingOverride?: PricingOverride | null;
	scopeLock?: {
		scopeKind: PricingOverrideScopeKind;
		virtualKeyID?: string;
		providerID?: string;
		providerKeyID?: string;
		label?: string;
	};
	onSaved?: () => void;
}

export default function PricingOverrideDrawer({ open, onOpenChange, editingOverride, scopeLock, onSaved }: PricingOverrideDrawerProps) {
	const { data: providersData } = useGetProvidersQuery();
	const { data: virtualKeysData } = useGetVirtualKeysQuery();
	const [createOverride, { isLoading: isCreating }] = useCreatePricingOverrideMutation();
	const [patchOverride, { isLoading: isPatching }] = usePatchPricingOverrideMutation();

	const [form, setForm] = useState<FormState>(defaultFormState);
	const [jsonPatch, setJSONPatch] = useState("{}");
	const [jsonError, setJSONError] = useState<string>();
	const jsonEditingRef = useRef(false);
	const [requestTypePopoverOpen, setRequestTypePopoverOpen] = useState(false);

	const isSaving = isCreating || isPatching;
	const providers = useMemo<ModelProvider[]>(() => providersData ?? [], [providersData]);
	const virtualKeys = useMemo(() => virtualKeysData?.virtual_keys ?? [], [virtualKeysData]);

	const providerKeyOptions = useMemo(
		() =>
			providers.flatMap((provider) =>
				(provider.keys || []).map((key) => ({
					id: key.id,
					providerName: provider.name,
					label: key.name || key.id,
				})),
			),
		[providers],
	);
	const providerScopedKeyOptions = useMemo(
		() => providerKeyOptions.filter((key) => key.providerName === form.providerID),
		[providerKeyOptions, form.providerID],
	);

	useEffect(() => {
		if (!open) return;
		jsonEditingRef.current = false;
		setJSONError(undefined);
		if (editingOverride) {
			setForm(toFormState(editingOverride));
			return;
		}
		if (scopeLock) {
			const scopedForm: FormState = {
				...defaultFormState,
				virtualKeyID: scopeLock.virtualKeyID ?? "",
				providerID: scopeLock.providerID ?? "",
				providerKeyID: scopeLock.providerKeyID ?? "",
				scopeRoot:
					scopeLock.scopeKind === "virtual_key" ||
					scopeLock.scopeKind === "virtual_key_provider" ||
					scopeLock.scopeKind === "virtual_key_provider_key"
						? "virtual_key"
						: "global",
			};
			setForm(scopedForm);
			return;
		}
		setForm(defaultFormState);
	}, [open, editingOverride, scopeLock]);

	const resolvedScopeKind = useMemo(() => {
		if (scopeLock?.scopeKind) return scopeLock.scopeKind;
		return deriveScopeKind(form);
	}, [scopeLock, form]);

	const resolvedVirtualKeyID = useMemo(() => {
		if (scopeLock?.scopeKind) return scopeLock.virtualKeyID;
		return form.scopeRoot === "virtual_key" ? form.virtualKeyID || undefined : undefined;
	}, [scopeLock, form.scopeRoot, form.virtualKeyID]);

	const resolvedProviderID = useMemo(() => {
		if (scopeLock?.scopeKind) return scopeLock.providerID;
		return form.providerID || undefined;
	}, [scopeLock, form.providerID]);

	const resolvedProviderKeyID = useMemo(() => {
		if (scopeLock?.scopeKind) return scopeLock.providerKeyID;
		return form.providerKeyID || undefined;
	}, [scopeLock, form.providerKeyID]);

	const validation = useMemo(() => {
		const errors: FieldErrors = {};
		if (!form.name.trim()) {
			errors.name = "Name is required";
		}
		if (
			(resolvedScopeKind === "virtual_key" ||
				resolvedScopeKind === "virtual_key_provider" ||
				resolvedScopeKind === "virtual_key_provider_key") &&
			!resolvedVirtualKeyID
		) {
			errors.scope = "Virtual key is required";
		}
		if ((resolvedScopeKind === "provider" || resolvedScopeKind === "virtual_key_provider") && !resolvedProviderID) {
			errors.scope = "Provider is required";
		}
		if (
			(resolvedScopeKind === "provider_key" || resolvedScopeKind === "virtual_key_provider_key") &&
			(!resolvedProviderID || !resolvedProviderKeyID)
		) {
			errors.scope = "Provider and provider key are required";
		}

		const pError = patternError(form.matchType, form.pattern);
		if (pError) errors.pattern = pError;

		const built = buildPatchFromForm(form);
		Object.assign(errors, built.errors);
		if (Object.keys(built.patch).length === 0) errors.patch = "At least one pricing field must be overridden";

		return { errors, patch: built.patch };
	}, [form, resolvedScopeKind, resolvedVirtualKeyID, resolvedProviderID, resolvedProviderKeyID]);

	useEffect(() => {
		if (!jsonEditingRef.current) {
			const json = Object.keys(validation.patch).length > 0 ? JSON.stringify(validation.patch, null, 2) : "{}";
			setJSONPatch(json);
			setJSONError(undefined);
		}
	}, [validation.patch]);

	const handleJSONChange = useCallback((value: string) => {
		jsonEditingRef.current = true;
		setJSONPatch(value);
		try {
			const parsed = JSON.parse(value || "{}");
			if (parsed == null || typeof parsed !== "object" || Array.isArray(parsed)) {
				setJSONError("Patch must be a JSON object");
				return;
			}
			const pricingValues: Partial<Record<PricingFieldKey, string>> = {};
			for (const [key, val] of Object.entries(parsed)) {
				if (!patchKeys.includes(key as PricingFieldKey)) {
					setJSONError(`Unknown field: ${key}`);
					return;
				}
				if (typeof val !== "number" || Number.isNaN(val) || val < 0) {
					setJSONError(`${key} must be a non-negative number`);
					return;
				}
				pricingValues[key as PricingFieldKey] = String(val);
			}
			setJSONError(undefined);
			setForm((prev) => ({ ...prev, pricingValues }));
			jsonEditingRef.current = false;
		} catch {
			setJSONError("Invalid JSON");
		}
	}, []);

	const isFormValid = Object.keys(validation.errors).length === 0 && !jsonError;

	const handleCloseDrawer = () => {
		onOpenChange(false);
		setRequestTypePopoverOpen(false);
	};

	const toggleRequestType = (requestType: string) => {
		setForm((prev) => ({
			...prev,
			requestTypes: prev.requestTypes.includes(requestType)
				? prev.requestTypes.filter((item) => item !== requestType)
				: [...prev.requestTypes, requestType],
		}));
	};

	const handleSave = async () => {
		if (!isFormValid) return;
		const requestPayload: CreatePricingOverrideRequest = {
			name: form.name.trim(),
			scope_kind: resolvedScopeKind,
			virtual_key_id: resolvedVirtualKeyID,
			provider_id: resolvedProviderID,
			provider_key_id: resolvedProviderKeyID,
			match_type: form.matchType,
			pattern: form.pattern.trim(),
			request_types: form.requestTypes.length > 0 ? form.requestTypes : undefined,
			patch: validation.patch,
		};

		try {
			if (editingOverride) {
				const payload: PatchPricingOverrideRequest = {
					name: requestPayload.name,
					scope_kind: requestPayload.scope_kind,
					virtual_key_id: requestPayload.virtual_key_id ?? "",
					provider_id: requestPayload.provider_id ?? "",
					provider_key_id: requestPayload.provider_key_id ?? "",
					match_type: requestPayload.match_type,
					pattern: requestPayload.pattern,
					request_types: requestPayload.request_types,
					patch: requestPayload.patch,
				};
				await patchOverride({ id: editingOverride.id, data: payload }).unwrap();
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

	const advancedSections = {
		cache: PRICING_FIELDS.filter((field) => field.group === "cache"),
		image: PRICING_FIELDS.filter((field) => field.group === "image"),
		av: PRICING_FIELDS.filter((field) => field.group === "av"),
		other: PRICING_FIELDS.filter((field) => field.group === "other"),
	};
	const tokenFields = PRICING_FIELDS.filter((field) => field.group === "token");

	return (
		<Sheet open={open} onOpenChange={(o) => (o ? onOpenChange(true) : handleCloseDrawer())}>
			<SheetContent side="right" className="dark:bg-card flex w-full flex-col overflow-x-hidden bg-white px-4 pb-6 sm:max-w-2xl">
				<SheetHeader className="flex flex-col items-start px-3 pt-8">
					<SheetTitle>{editingOverride ? "Edit Pricing Override" : "Create Pricing Override"}</SheetTitle>
				</SheetHeader>

				<div className="custom-scrollbar flex-1 space-y-6 overflow-y-auto px-3 pb-4">
					<div className="space-y-4">
						<div className="space-y-2">
							<Label>Name</Label>
							<Input value={form.name} onChange={(e) => setForm((prev) => ({ ...prev, name: e.target.value }))} />
							{validation.errors.name && <p className="text-destructive text-xs">{validation.errors.name}</p>}
						</div>

						{scopeLock ? (
							<div className="space-y-2">
								<Label>Scope</Label>
								<Input value={scopeLock.label ?? scopeLock.scopeKind} readOnly />
							</div>
						) : (
							<>
								<div className="space-y-2">
									<Label>Scope root</Label>
									<Select
										value={form.scopeRoot}
										onValueChange={(value: ScopeRoot) =>
											setForm((prev) => ({ ...prev, scopeRoot: value, virtualKeyID: "", providerID: "", providerKeyID: "" }))
										}
									>
										<SelectTrigger>
											<SelectValue />
										</SelectTrigger>
										<SelectContent>
											<SelectItem value="global">Global</SelectItem>
											<SelectItem value="virtual_key">Virtual key</SelectItem>
										</SelectContent>
									</Select>
								</div>

								{form.scopeRoot === "virtual_key" && (
									<div className="space-y-2">
										<Label>Virtual key</Label>
										<Select
											value={form.virtualKeyID || "__none__"}
											onValueChange={(value) =>
												setForm((prev) => ({ ...prev, virtualKeyID: value === "__none__" ? "" : value, providerID: "", providerKeyID: "" }))
											}
										>
											<SelectTrigger>
												<SelectValue placeholder="Select virtual key" />
											</SelectTrigger>
											<SelectContent>
												<SelectItem value="__none__">Select virtual key</SelectItem>
												{virtualKeys.map((vk) => (
													<SelectItem key={vk.id} value={vk.id}>
														{vk.name}
													</SelectItem>
												))}
											</SelectContent>
										</Select>
									</div>
								)}

								<div className="space-y-2">
									<Label>Provider (optional)</Label>
									<Select
										value={form.providerID || "__none__"}
										onValueChange={(value) =>
											setForm((prev) => ({ ...prev, providerID: value === "__none__" ? "" : value, providerKeyID: "" }))
										}
									>
										<SelectTrigger>
											<SelectValue placeholder="All providers" />
										</SelectTrigger>
										<SelectContent>
											<SelectItem value="__none__">All providers</SelectItem>
											{providers.map((provider) => (
												<SelectItem key={provider.name} value={provider.name}>
													{provider.name}
												</SelectItem>
											))}
										</SelectContent>
									</Select>
								</div>

								{form.providerID && (
									<div className="space-y-2">
										<Label>Provider key (optional)</Label>
										<Select
											value={form.providerKeyID || "__none__"}
											onValueChange={(value) => setForm((prev) => ({ ...prev, providerKeyID: value === "__none__" ? "" : value }))}
										>
											<SelectTrigger>
												<SelectValue placeholder="All provider keys" />
											</SelectTrigger>
											<SelectContent>
												<SelectItem value="__none__">All provider keys</SelectItem>
												{providerScopedKeyOptions.map((option) => (
													<SelectItem key={option.id} value={option.id}>
														{option.label}
													</SelectItem>
												))}
											</SelectContent>
										</Select>
									</div>
								)}

								<Badge variant="outline" className="text-muted-foreground text-xs">
									{resolvedScopeKind.replaceAll("_", " ")}
								</Badge>
							</>
						)}
						{validation.errors.scope && <p className="text-destructive text-xs">{validation.errors.scope}</p>}
					</div>

					<DottedSeparator />

					<div className="space-y-2">
						<div className="grid grid-cols-[160px_1fr] gap-3">
							<div className="space-y-2">
								<Label>Match type</Label>
								<Select
									value={form.matchType}
									onValueChange={(value: PricingOverrideMatchType) => setForm((prev) => ({ ...prev, matchType: value }))}
								>
									<SelectTrigger>
										<SelectValue placeholder="Select match type" />
									</SelectTrigger>
									<SelectContent>
										<SelectItem value="exact">exact</SelectItem>
										<SelectItem value="wildcard">wildcard</SelectItem>
									</SelectContent>
								</Select>
							</div>
							<div className="space-y-2">
								<Label>Pattern</Label>
								<Input
									value={form.pattern}
									onChange={(e) => setForm((prev) => ({ ...prev, pattern: e.target.value }))}
									placeholder={form.matchType === "exact" ? "gpt-5-mini" : "gpt-5*"}
								/>
							</div>
						</div>
						{validation.errors.pattern && <p className="text-destructive text-xs">{validation.errors.pattern}</p>}
					</div>

					<DottedSeparator />

					<div className="space-y-2">
						<Label>Request types</Label>
						<Popover open={requestTypePopoverOpen} onOpenChange={setRequestTypePopoverOpen} modal={false}>
							<PopoverTrigger asChild>
								<Button type="button" variant="outline" className="w-full justify-between">
									<span className="truncate">
										{form.requestTypes.length > 0
											? `${getRequestTypeGroup(form.requestTypes[0])} (${form.requestTypes.length})`
											: "All request types"}
									</span>
									<ChevronDown className="h-4 w-4" />
								</Button>
							</PopoverTrigger>
							<PopoverContent align="start" className="w-[320px] p-2" onWheel={(e) => e.stopPropagation()}>
								<div className="max-h-72 space-y-1 overflow-y-auto" onWheel={(e) => e.stopPropagation()}>
									{(() => {
										const activeGroup = form.requestTypes.length > 0 ? getRequestTypeGroup(form.requestTypes[0]) : undefined;
										return REQUEST_TYPE_GROUPS.map((group) => {
											const isGroupDisabled = activeGroup != null && group.label !== activeGroup;
											return (
												<div key={group.label}>
													<div className="text-muted-foreground flex items-center gap-1 px-2 py-1 text-xs font-medium">
														{group.label}
														{isGroupDisabled && <span className="text-muted-foreground/60 italic">(clear selection first)</span>}
													</div>
													{group.types.map((requestType) => {
														const checked = form.requestTypes.includes(requestType);
														return (
															<label
																key={requestType}
																className={cn(
																	"flex items-center gap-2 rounded-sm px-2 py-1.5 text-sm",
																	isGroupDisabled ? "cursor-not-allowed opacity-50" : "hover:bg-muted cursor-pointer",
																)}
															>
																<Checkbox
																	checked={checked}
																	disabled={isGroupDisabled}
																	onCheckedChange={() => toggleRequestType(requestType)}
																/>
																<span>{RequestTypeLabels[requestType as keyof typeof RequestTypeLabels] ?? requestType}</span>
															</label>
														);
													})}
												</div>
											);
										});
									})()}
								</div>
								<div className="mt-2 flex justify-end">
									<Button type="button" size="sm" variant="ghost" onClick={() => setForm((prev) => ({ ...prev, requestTypes: [] }))}>
										Clear (All)
									</Button>
								</div>
							</PopoverContent>
						</Popover>
					</div>

					<DottedSeparator />

					<div className="space-y-4">
						<Label>Pricing fields</Label>
						<Accordion type="multiple" defaultValue={["token"]} className="rounded-md border px-3">
							<AccordionItem value="token">
								<AccordionTrigger>
									<span className="flex items-center gap-2">
										Token
										{countFieldsWithValues(tokenFields, form) > 0 && (
											<Badge variant="secondary" className="px-1.5 py-0 text-[10px]">
												{countFieldsWithValues(tokenFields, form)}
											</Badge>
										)}
									</span>
								</AccordionTrigger>
								<AccordionContent>{renderFields(tokenFields, form, setForm, validation.errors)}</AccordionContent>
							</AccordionItem>
							<AccordionItem value="cache">
								<AccordionTrigger>
									<span className="flex items-center gap-2">
										Cache
										{countFieldsWithValues(advancedSections.cache, form) > 0 && (
											<Badge variant="secondary" className="px-1.5 py-0 text-[10px]">
												{countFieldsWithValues(advancedSections.cache, form)}
											</Badge>
										)}
									</span>
								</AccordionTrigger>
								<AccordionContent>{renderFields(advancedSections.cache, form, setForm, validation.errors)}</AccordionContent>
							</AccordionItem>
							<AccordionItem value="image">
								<AccordionTrigger>
									<span className="flex items-center gap-2">
										Image
										{countFieldsWithValues(advancedSections.image, form) > 0 && (
											<Badge variant="secondary" className="px-1.5 py-0 text-[10px]">
												{countFieldsWithValues(advancedSections.image, form)}
											</Badge>
										)}
									</span>
								</AccordionTrigger>
								<AccordionContent>{renderFields(advancedSections.image, form, setForm, validation.errors)}</AccordionContent>
							</AccordionItem>
							<AccordionItem value="audio-video">
								<AccordionTrigger>
									<span className="flex items-center gap-2">
										Audio and Video
										{countFieldsWithValues(advancedSections.av, form) > 0 && (
											<Badge variant="secondary" className="px-1.5 py-0 text-[10px]">
												{countFieldsWithValues(advancedSections.av, form)}
											</Badge>
										)}
									</span>
								</AccordionTrigger>
								<AccordionContent>{renderFields(advancedSections.av, form, setForm, validation.errors)}</AccordionContent>
							</AccordionItem>
							<AccordionItem value="other">
								<AccordionTrigger>
									<span className="flex items-center gap-2">
										Other
										{countFieldsWithValues(advancedSections.other, form) > 0 && (
											<Badge variant="secondary" className="px-1.5 py-0 text-[10px]">
												{countFieldsWithValues(advancedSections.other, form)}
											</Badge>
										)}
									</span>
								</AccordionTrigger>
								<AccordionContent>{renderFields(advancedSections.other, form, setForm, validation.errors)}</AccordionContent>
							</AccordionItem>
						</Accordion>
						{validation.errors.patch && <p className="text-destructive text-xs">{validation.errors.patch}</p>}
					</div>

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

				<SheetFooter className="gap-2 border-t px-3 pt-4">
					<Button type="button" variant="outline" onClick={handleCloseDrawer} disabled={isSaving}>
						Cancel
					</Button>
					<Button type="button" onClick={handleSave} disabled={!isFormValid || isSaving}>
						Save Override
					</Button>
				</SheetFooter>
			</SheetContent>
		</Sheet>
	);
}
