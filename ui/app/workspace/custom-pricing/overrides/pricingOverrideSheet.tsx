import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { CodeEditor } from "@/components/ui/codeEditor";
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
	useGetVirtualKeysQuery,
	useUpdatePricingOverrideMutation,
} from "@/lib/store";
import { useGetAllKeysQuery } from "@/lib/store/apis/providersApi";
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

export const REQUEST_TYPE_GROUPS = [
	{
		label: "Chat / Text / Responses",
		types: ["chat_completion", "text_completion", "responses"],
	},
	{
		label: "Embedding",
		types: ["embedding"],
	},
	{
		label: "Rerank",
		types: ["rerank"],
	},
	{
		label: "Audio",
		types: ["speech", "transcription"],
	},
	{
		label: "Image",
		types: ["image_generation", "image_variation", "image_edit"],
	},
	{
		label: "Video",
		types: ["video_generation", "video_remix"],
	},
	{
		label: "OCR",
		types: ["ocr"],
	},
] as const;

export const REQUEST_TYPE_OPTIONS = REQUEST_TYPE_GROUPS.flatMap((g) => g.types);

export function getRequestTypeGroup(rt: string): string | undefined {
	return REQUEST_TYPE_GROUPS.find((g) => (g.types as readonly string[]).includes(rt))?.label;
}

export const PRICING_FIELDS = [
	// Chat / Text / Responses fields
	{
		key: "input_cost_per_token",
		label: "Input / token",
		group: "chat",
		requestTypeGroups: ["chat", "embedding", "rerank", "audio", "image", "video"],
	},
	{
		key: "output_cost_per_token",
		label: "Output / token",
		group: "chat",
		requestTypeGroups: ["chat", "rerank", "audio", "image", "video"],
	},
	{ key: "input_cost_per_token_batches", label: "Input / token (batch)", group: "chat", requestTypeGroups: ["chat"] },
	{ key: "output_cost_per_token_batches", label: "Output / token (batch)", group: "chat", requestTypeGroups: ["chat"] },
	{ key: "input_cost_per_token_priority", label: "Input / token (priority)", group: "chat", requestTypeGroups: ["chat"] },
	{ key: "output_cost_per_token_priority", label: "Output / token (priority)", group: "chat", requestTypeGroups: ["chat"] },
	{ key: "input_cost_per_token_flex", label: "Input / token (flex)", group: "chat", requestTypeGroups: ["chat"] },
	{ key: "output_cost_per_token_flex", label: "Output / token (flex)", group: "chat", requestTypeGroups: ["chat"] },
	{
		key: "input_cost_per_token_above_128k_tokens",
		label: "Input / token (>128k)",
		group: "chat",
		requestTypeGroups: ["chat", "embedding", "rerank"],
	},
	{
		key: "output_cost_per_token_above_128k_tokens",
		label: "Output / token (>128k)",
		group: "chat",
		requestTypeGroups: ["chat", "rerank", "audio"],
	},
	{
		key: "input_cost_per_token_above_200k_tokens",
		label: "Input / token (>200k)",
		group: "chat",
		requestTypeGroups: ["chat", "embedding", "rerank"],
	},
	{
		key: "input_cost_per_token_above_200k_tokens_priority",
		label: "Input / token (>200k, priority)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_above_200k_tokens",
		label: "Output / token (>200k)",
		group: "chat",
		requestTypeGroups: ["chat", "rerank", "audio"],
	},
	{
		key: "output_cost_per_token_above_200k_tokens_priority",
		label: "Output / token (>200k, priority)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "input_cost_per_token_above_272k_tokens",
		label: "Input / token (>272k)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "input_cost_per_token_above_272k_tokens_priority",
		label: "Input / token (>272k, priority)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_above_272k_tokens",
		label: "Output / token (>272k)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_above_272k_tokens_priority",
		label: "Output / token (>272k, priority)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{ key: "cache_creation_input_token_cost", label: "Cache creation / token", group: "chat", requestTypeGroups: ["chat"] },
	{ key: "cache_read_input_token_cost", label: "Cache read / token", group: "chat", requestTypeGroups: ["chat"] },
	{
		key: "cache_creation_input_token_cost_above_200k_tokens",
		label: "Cache creation / token (>200k)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{ key: "cache_read_input_token_cost_above_200k_tokens", label: "Cache read / token (>200k)", group: "chat", requestTypeGroups: ["chat"] },
	{ key: "cache_creation_input_token_cost_above_1hr", label: "Cache creation / token (>1hr)", group: "chat", requestTypeGroups: ["chat"] },
	{
		key: "cache_creation_input_token_cost_above_1hr_above_200k_tokens",
		label: "Cache creation / token (>1hr, >200k)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{ key: "cache_read_input_token_cost_priority", label: "Cache read / token (priority)", group: "chat", requestTypeGroups: ["chat"] },
	{ key: "cache_read_input_token_cost_flex", label: "Cache read / token (flex)", group: "chat", requestTypeGroups: ["chat"] },
	{
		key: "cache_read_input_token_cost_above_200k_tokens_priority",
		label: "Cache read / token (>200k, priority)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{ key: "cache_read_input_token_cost_above_272k_tokens", label: "Cache read / token (>272k)", group: "chat", requestTypeGroups: ["chat"] },
	{
		key: "cache_read_input_token_cost_above_272k_tokens_priority",
		label: "Cache read / token (>272k, priority)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{ key: "search_context_cost_per_query", label: "Search context / query", group: "chat", requestTypeGroups: ["chat", "rerank"] },
	{ key: "code_interpreter_cost_per_session", label: "Code interpreter / session", group: "chat", requestTypeGroups: ["chat"] },
	// Audio fields
	{ key: "input_cost_per_character", label: "Input / character", group: "audio", requestTypeGroups: ["audio"] },
	{ key: "input_cost_per_audio_token", label: "Input / audio token", group: "audio", requestTypeGroups: ["audio"] },
	{ key: "input_cost_per_audio_per_second", label: "Input / audio second", group: "audio", requestTypeGroups: ["audio"] },
	{
		key: "input_cost_per_audio_per_second_above_128k_tokens",
		label: "Input / audio second (>128k)",
		group: "audio",
		requestTypeGroups: ["audio"],
	},
	{ key: "input_cost_per_second", label: "Input / second", group: "audio", requestTypeGroups: ["audio", "video"] },
	{ key: "output_cost_per_audio_token", label: "Output / audio token", group: "audio", requestTypeGroups: ["audio"] },
	{ key: "output_cost_per_second", label: "Output / second", group: "audio", requestTypeGroups: ["audio", "video"] },
	{ key: "cache_creation_input_audio_token_cost", label: "Cache creation / audio token", group: "audio", requestTypeGroups: ["audio"] },
	// Image fields
	{ key: "input_cost_per_image_token", label: "Input / image token", group: "image", requestTypeGroups: ["image"] },
	{ key: "input_cost_per_image", label: "Input / image", group: "image", requestTypeGroups: ["image"] },
	{ key: "input_cost_per_image_above_128k_tokens", label: "Input / image (>128k)", group: "image", requestTypeGroups: ["image"] },
	{ key: "input_cost_per_pixel", label: "Input / pixel", group: "image", requestTypeGroups: ["image"] },
	{ key: "output_cost_per_image_token", label: "Output / image token", group: "image", requestTypeGroups: ["image"] },
	{ key: "output_cost_per_image", label: "Output / image", group: "image", requestTypeGroups: ["image"] },
	{ key: "output_cost_per_pixel", label: "Output / pixel", group: "image", requestTypeGroups: ["image"] },
	{ key: "output_cost_per_image_premium_image", label: "Output / image (premium)", group: "image", requestTypeGroups: ["image"] },
	{ key: "output_cost_per_image_above_512_and_512_pixels", label: "Output / image (>512px)", group: "image", requestTypeGroups: ["image"] },
	{
		key: "output_cost_per_image_above_512_and_512_pixels_and_premium_image",
		label: "Output / image (>512px, premium)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1024_and_1024_pixels",
		label: "Output / image (>1024px)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1024_and_1024_pixels_and_premium_image",
		label: "Output / image (>1024px, premium)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{ key: "output_cost_per_image_low_quality", label: "Output / image (low quality)", group: "image", requestTypeGroups: ["image"] },
	{ key: "output_cost_per_image_medium_quality", label: "Output / image (medium quality)", group: "image", requestTypeGroups: ["image"] },
	{ key: "output_cost_per_image_high_quality", label: "Output / image (high quality)", group: "image", requestTypeGroups: ["image"] },
	{ key: "output_cost_per_image_auto_quality", label: "Output / image (auto quality)", group: "image", requestTypeGroups: ["image"] },
	{ key: "cache_read_input_image_token_cost", label: "Cache read / image token", group: "image", requestTypeGroups: ["image"] },
	// Video fields
	{ key: "input_cost_per_video_per_second", label: "Input / video second", group: "video", requestTypeGroups: ["video"] },
	{
		key: "input_cost_per_video_per_second_above_128k_tokens",
		label: "Input / video second (>128k)",
		group: "video",
		requestTypeGroups: ["video"],
	},
	{ key: "output_cost_per_video_per_second", label: "Output / video second", group: "video", requestTypeGroups: ["video"] },
	// OCR fields
	{ key: "ocr_cost_per_page", label: "OCR / page", group: "ocr", requestTypeGroups: ["ocr"] },
	{ key: "annotation_cost_per_page", label: "Annotation / page", group: "ocr", requestTypeGroups: ["ocr"] },
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
	requestTypes: RequestType[];
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
		default:
			return false;
	}
}

export default function PricingOverrideSheet({ open, onOpenChange, editingOverride, scopeLock, onSaved }: PricingOverrideDrawerProps) {
	const { data: providersData, isLoading: isProvidersLoading, error: providersError } = useGetProvidersQuery();
	const { data: virtualKeysData, isLoading: isVirtualKeysLoading, error: virtualKeysError } = useGetVirtualKeysQuery();
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
	const virtualKeys = useMemo(() => (virtualKeysError ? [] : (virtualKeysData?.virtual_keys ?? [])), [virtualKeysData, virtualKeysError]);

	const scopeRoot = watch("scopeRoot");
	const providerID = watch("providerID");
	const providerKeyID = watch("providerKeyID");
	const virtualKeyID = watch("virtualKeyID");
	const matchType = watch("matchType");
	const requestTypes = watch("requestTypes");
	const pricingValues = watch("pricingValues");

	const shouldLockScope = useMemo(() => !editingOverride && isCompleteScopeLock(scopeLock), [editingOverride, scopeLock]);

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
				virtualKeyID: scopeLock.virtualKeyID ?? "",
				providerID: scopeLock.providerID ?? "",
				providerKeyID: scopeLock.providerKeyID ?? "",
				scopeRoot:
					scopeLock.scopeKind === "virtual_key" ||
					scopeLock.scopeKind === "virtual_key_provider" ||
					scopeLock.scopeKind === "virtual_key_provider_key"
						? "virtual_key"
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
		}

		const requestPayload: CreatePricingOverrideRequest = {
			name: data.name.trim(),
			scope_kind: resolvedScopeKind,
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
						<div className="flex-1 space-y-6 overflow-y-auto px-8 pb-4">
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
															clearErrors("virtualKeyID");
														}}
													>
														<FormControl>
															<SelectTrigger
																data-testid="pricing-override-scope-root-select"
																className="w-full"
															>
																<SelectValue />
															</SelectTrigger>
														</FormControl>
														<SelectContent>
															<SelectItem value="global">Global</SelectItem>
															<SelectItem value="virtual_key">Virtual key</SelectItem>
														</SelectContent>
													</Select>
												</FormItem>
											)}
										/>

										{scopeRoot === "virtual_key" && (
											<FormField
												control={control}
												name="virtualKeyID"
												render={({ field }) => (
													<FormItem>
														<FormLabel>
															Virtual key <span className="text-red-500">*</span>
														</FormLabel>
														<Select
															value={field.value || "__none__"}
															onValueChange={(value) => {
																field.onChange(value === "__none__" ? "" : value);
																setValue("providerID", "");
																setValue("providerKeyID", "");
																clearErrors("virtualKeyID");
															}}
														>
															<FormControl>
																<SelectTrigger
																	data-testid="pricing-override-virtual-key-select"
																	className="w-full"
																	disabled={isVirtualKeysLoading || !!virtualKeysError}
																>
																	<SelectValue placeholder={isVirtualKeysLoading ? "Loading..." : "Select virtual key"} />
																</SelectTrigger>
															</FormControl>
															<SelectContent>
																<SelectItem value="__none__">Select virtual key</SelectItem>
																{virtualKeys.map((vk) => (
																	<SelectItem key={vk.id} value={vk.id}>
																		{vk.name}
																	</SelectItem>
																))}
															</SelectContent>
														</Select>
														{virtualKeysError ? (
															<p className="text-destructive mt-1 text-xs">
																Failed to load virtual keys: {getErrorMessage(virtualKeysError)}
															</p>
														) : (
															<FormMessage />
														)}
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
																			<RenderProviderIcon provider={field.value as ProviderIconType} size="sm" className="h-4 w-4 shrink-0" />
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
																			<RenderProviderIcon provider={provider.name as ProviderIconType} size="sm" className="h-4 w-4 shrink-0" />
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
															<Select
																value={field.value || "__none__"}
																onValueChange={(value) => field.onChange(value === "__none__" ? "" : value)}
															>
																<FormControl>
																	<SelectTrigger
																		data-testid="pricing-override-provider-key-select"
																		className="w-full"
																	>
																		<SelectValue placeholder="All provider keys" />
																	</SelectTrigger>
																</FormControl>
																<SelectContent>
																	<SelectItem value="__none__">All provider keys</SelectItem>
																	{providerScopedKeyOptions.map((option) => (
																		<SelectItem key={option.id} value={option.id}>
																			{option.label}
																		</SelectItem>
																	))}
																</SelectContent>
															</Select>
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
														<SelectTrigger
															data-testid="pricing-override-match-type-select"
															className="w-full"
														>
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
											<PopoverContent align="start" className="w-[320px] p-2" onWheel={(e) => e.stopPropagation()}>
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
							<Button data-testid="pricing-override-cancel-btn" type="button" variant="outline" onClick={handleCloseDrawer} disabled={isSaving}>
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
