import { PricingFieldSelector } from "@/app/workspace/custom-pricing/overrides/pricingFieldSelector";
import {
	type FieldErrors as PricingFieldErrors,
	type PricingFieldKey,
	pricingFieldError,
} from "@/app/workspace/custom-pricing/overrides/pricingFields";
import FullPageLoader from "@/components/fullPageLoader";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { CodeEditor } from "@/components/ui/codeEditor";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ProviderSelector } from "@/components/ui/providerSelector";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { DottedSeparator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderLabels, ProviderName } from "@/lib/constants/logs";
import { getLocale } from "@/lib/i18n";
import {
	type DatasheetEntryDetail,
	type DatasheetEntryTarget,
	type DatasheetOverride,
	getErrorMessage,
	useDeleteDatasheetOverrideMutation,
	useGetDatasheetEntryQuery,
	useSaveDatasheetOverrideMutation,
} from "@/lib/store";
import { KnownProvider } from "@/lib/types/config";
import { datasheetOverrideFormSchema } from "@/lib/types/schemas";
import { zodResolver } from "@hookform/resolvers/zod";
import { AlertTriangle, RotateCcw } from "lucide-react";
import { useState } from "react";
import { Controller, type Resolver, useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import {
	buildSaveRequest,
	CAPABILITY_FLAGS,
	DATASHEET_MODES,
	type DatasheetFormState,
	formatDatasheetValue,
	formStateFromOverride,
	LIMIT_FIELDS,
	type LimitField,
	overrideForMode,
	splitDriftedFields,
	type TriState,
} from "./datasheetFields";

const LIMIT_LABEL_KEYS = {
	context_length: "datasheet.contextLength",
	max_input_tokens: "datasheet.maxInputTokens",
	max_output_tokens: "datasheet.maxOutputTokens",
} as const satisfies Record<LimitField, string>;

interface DatasheetEntrySheetProps {
	/** The entry to edit; null opens the sheet for a new custom entry. */
	target: DatasheetEntryTarget | null;
	canEdit: boolean;
	onClose: () => void;
}

/**
 * Loads the entry, then mounts the form with its saved override as the
 * default values, so the form initialises once instead of syncing in effects.
 */
export default function DatasheetEntrySheet({ target, canEdit, onClose }: DatasheetEntrySheetProps) {
	const { t } = useTranslation("models");
	const { data, isLoading, error } = useGetDatasheetEntryQuery(target ?? { provider: "", model: "", mode: "" }, { skip: target === null });

	return (
		<Sheet open onOpenChange={(open) => !open && onClose()}>
			<SheetContent className="flex w-full flex-col overflow-x-hidden pt-4 sm:max-w-2xl" data-testid="datasheet-sheet">
				<SheetHeader className="flex flex-col items-start p-0 py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10 px-4 md:px-8">
					<SheetTitle>{target ? t("datasheet.editEntry") : t("datasheet.addCustomModel")}</SheetTitle>
					<SheetDescription>{target ? t("datasheet.editEntryDescription") : t("datasheet.addCustomModelDescription")}</SheetDescription>
				</SheetHeader>
				{target && isLoading ? (
					<FullPageLoader />
				) : target && (error || !data) ? (
					<p className="text-destructive px-4 text-sm md:px-8">{error ? getErrorMessage(error) : t("datasheet.entryNotFound")}</p>
				) : (
					<DatasheetEntryForm key={formKey(target, data)} target={target} detail={data} canEdit={canEdit} onClose={onClose} />
				)}
			</SheetContent>
		</Sheet>
	);
}

// Remount the form when a save lands a new version of the override.
const formKey = (target: DatasheetEntryTarget | null, detail?: DatasheetEntryDetail) =>
	target ? `${target.provider}|${target.model}|${target.mode}|${overrideForMode(detail?.overrides, target.mode)?.updated_at ?? ""}` : "new";

interface DatasheetEntryFormProps {
	target: DatasheetEntryTarget | null;
	detail?: DatasheetEntryDetail;
	canEdit: boolean;
	onClose: () => void;
}

function DatasheetEntryForm({ target, detail, canEdit, onClose }: DatasheetEntryFormProps) {
	const { t } = useTranslation("models");
	const { t: tc } = useTranslation("common");
	const isNew = target === null;
	const existing = target ? overrideForMode(detail?.overrides, target.mode) : undefined;
	const modeWide = detail?.overrides?.filter((o) => o.mode === "") ?? [];
	const isCustom = isNew || (detail?.is_custom ?? false);
	const upstream: Record<string, unknown> = detail?.upstream ?? {};
	const upstreamParams: Record<string, unknown> = detail?.upstream_params ?? {};
	const drift = splitDriftedFields(detail?.drifted_fields);

	const {
		control,
		register,
		handleSubmit,
		setValue,
		watch,
		formState: { errors, isDirty },
	} = useForm<DatasheetFormState>({
		resolver: zodResolver(datasheetOverrideFormSchema) as Resolver<DatasheetFormState>,
		defaultValues: formStateFromOverride(
			target ? { ...target, isCustom } : { provider: "", model: "", mode: "chat", isCustom: true },
			existing,
		),
	});
	const [pricingErrors, setPricingErrors] = useState<PricingFieldErrors>({});
	const [saveOverride, { isLoading: isSaving }] = useSaveDatasheetOverrideMutation();
	const [deleteOverride, { isLoading: isDeleting }] = useDeleteDatasheetOverrideMutation();
	const pricingValues = watch("pricingValues");

	const onSubmit = async (values: DatasheetFormState) => {
		const nextPricingErrors: PricingFieldErrors = {};
		for (const [key, raw] of Object.entries(values.pricingValues)) {
			const fieldError = pricingFieldError(key as PricingFieldKey, raw);
			if (fieldError) nextPricingErrors[key as PricingFieldKey] = fieldError;
		}
		setPricingErrors(nextPricingErrors);
		if (Object.keys(nextPricingErrors).length > 0) return;
		try {
			const result = await saveOverride(buildSaveRequest(values)).unwrap();
			toast.success(result.override ? t("datasheet.saved") : t("datasheet.resetToUpstream"));
			onClose();
		} catch (err) {
			toast.error(getErrorMessage(err));
		}
	};

	const onResetAll = async (override: DatasheetOverride) => {
		try {
			await deleteOverride(override.id).unwrap();
			toast.success(t("datasheet.resetToUpstream"));
			onClose();
		} catch (err) {
			toast.error(getErrorMessage(err));
		}
	};

	// "Upstream: …" under a field, plus a warning when upstream moved since the save.
	const hint = (patch: "pricing" | "params", key: string, current: unknown) => {
		const drifted = (patch === "pricing" ? drift.pricing : drift.params).has(key);
		const baseline = existing?.baseline?.[patch]?.[key];
		return (
			<div className="mt-1 space-y-0.5 text-xs">
				{!isCustom && (
					<p className="text-muted-foreground">{t("datasheet.upstreamValue", { value: formatDatasheetValue(current, getLocale()) })}</p>
				)}
				{drifted && (
					<p className="flex items-center gap-1 text-amber-600 dark:text-amber-400" data-testid={`datasheet-drift-${patch}-${key}`}>
						<AlertTriangle className="h-3 w-3 shrink-0" />
						{t("datasheet.upstreamChanged", { value: formatDatasheetValue(baseline, getLocale()) })}
					</p>
				)}
			</div>
		);
	};

	const triStateLabel = (state: TriState, current: unknown) =>
		state === "upstream"
			? t("datasheet.followUpstream", { value: formatDatasheetValue(current, getLocale()) })
			: state === "on"
				? t("datasheet.on")
				: t("datasheet.off");

	return (
		<form onSubmit={handleSubmit(onSubmit)} className="flex h-full flex-col gap-6">
			<div className="grow space-y-5 px-4 md:px-8">
				{isNew ? (
					<div className="grid grid-cols-1 gap-4 md:grid-cols-3">
						<div>
							<Label className="text-sm font-medium">{t("datasheet.provider")}</Label>
							<Controller
								control={control}
								name="provider"
								render={({ field }) => (
									<ProviderSelector
										source="catalog"
										noPortal
										className="mt-2 w-full"
										value={field.value}
										onChange={field.onChange}
										data-testid="datasheet-custom-provider"
										optionTestId={(value) => `datasheet-custom-provider-option-${value}`}
									/>
								)}
							/>
							{errors.provider && <p className="text-destructive mt-1 text-xs">{errors.provider.message}</p>}
						</div>
						<div>
							<Label className="text-sm font-medium" htmlFor="datasheet-custom-model">
								{t("datasheet.model")}
							</Label>
							<Input
								id="datasheet-custom-model"
								className="mt-2 font-mono"
								placeholder={t("datasheet.customModelPlaceholder")}
								{...register("model")}
								data-testid="datasheet-custom-model-input"
							/>
							{errors.model && <p className="text-destructive mt-1 text-xs">{errors.model.message}</p>}
						</div>
						<div>
							<Label className="text-sm font-medium">{t("datasheet.mode")}</Label>
							<Controller
								control={control}
								name="mode"
								render={({ field }) => (
									<Select value={field.value} onValueChange={field.onChange}>
										<SelectTrigger className="mt-2 w-full" data-testid="datasheet-custom-mode-select">
											<SelectValue />
										</SelectTrigger>
										<SelectContent>
											{DATASHEET_MODES.map((mode) => (
												<SelectItem key={mode} value={mode} className="font-mono">
													{mode}
												</SelectItem>
											))}
										</SelectContent>
									</Select>
								)}
							/>
							{errors.mode && <p className="text-destructive mt-1 text-xs">{errors.mode.message}</p>}
						</div>
					</div>
				) : (
					target && (
						<div className="flex flex-wrap items-center gap-2" data-testid="datasheet-sheet-target">
							<RenderProviderIcon provider={target.provider as KnownProvider} size="sm" className="h-4 w-4" />
							<span className="text-sm">{ProviderLabels[target.provider as ProviderName] || target.provider}</span>
							<span className="font-mono text-sm">{target.model}</span>
							<Badge variant="outline" className="font-mono">
								{target.mode}
							</Badge>
							{isCustom && <Badge variant="secondary">{t("datasheet.custom")}</Badge>}
						</div>
					)
				)}

				{existing?.config_hash && <p className="text-muted-foreground text-xs">{t("datasheet.managedByConfigFile")}</p>}
				{modeWide.length > 0 && <p className="text-muted-foreground text-xs">{t("datasheet.modeWideOverride")}</p>}

				<DottedSeparator />

				<section className="space-y-3">
					<div>
						<h3 className="text-sm font-medium">{t("datasheet.limits")}</h3>
						<p className="text-muted-foreground text-xs">{t("datasheet.emptyKeepsUpstream")}</p>
					</div>
					<div className="grid grid-cols-1 gap-4 md:grid-cols-3">
						{LIMIT_FIELDS.map((field) => (
							<div key={field}>
								<Label className="text-sm" htmlFor={`datasheet-limit-${field}`}>
									{t(LIMIT_LABEL_KEYS[field])}
								</Label>
								<div className="mt-1 flex items-center gap-1">
									<Input
										id={`datasheet-limit-${field}`}
										inputMode="numeric"
										className="h-8 font-mono"
										placeholder={isCustom ? "" : formatDatasheetValue(upstream[field], getLocale())}
										{...register(`limits.${field}`)}
										data-testid={`datasheet-limit-input-${field}`}
									/>
									<Button
										type="button"
										variant="ghost"
										size="icon"
										className="h-8 w-8 shrink-0"
										onClick={() => setValue(`limits.${field}`, "", { shouldDirty: true })}
										aria-label={t("datasheet.resetField")}
										data-testid={`datasheet-limit-reset-${field}`}
									>
										<RotateCcw className="h-3.5 w-3.5" />
									</Button>
								</div>
								{hint("pricing", field, upstream[field])}
								{errors.limits?.[field] && <p className="text-destructive mt-1 text-xs">{errors.limits[field]?.message}</p>}
							</div>
						))}
					</div>
					<div className="max-w-xs">
						<Label className="text-sm">{t("datasheet.deprecated")}</Label>
						<Controller
							control={control}
							name="deprecated"
							render={({ field }) => (
								<Select value={field.value} onValueChange={field.onChange}>
									<SelectTrigger className="mt-1 h-8 w-full" data-testid="datasheet-deprecated-select">
										<SelectValue />
									</SelectTrigger>
									<SelectContent>
										<SelectItem value="upstream">{triStateLabel("upstream", upstream.is_deprecated ?? false)}</SelectItem>
										<SelectItem value="on">{t("datasheet.markDeprecated")}</SelectItem>
										<SelectItem value="off">{t("datasheet.markActive")}</SelectItem>
									</SelectContent>
								</Select>
							)}
						/>
						{drift.pricing.has("is_deprecated") && hint("pricing", "is_deprecated", upstream.is_deprecated)}
					</div>
				</section>

				<DottedSeparator />

				<section className="space-y-3">
					<div>
						<h3 className="text-sm font-medium">{t("datasheet.basePricing")}</h3>
						<p className="text-muted-foreground text-xs">{t("datasheet.basePricingDescription")}</p>
					</div>
					<PricingFieldSelector
						values={pricingValues}
						errors={pricingErrors}
						onChange={(key, value) => setValue(`pricingValues.${key}`, value, { shouldDirty: true })}
						renderFieldHint={(key) => hint("pricing", key, upstream[key])}
					/>
				</section>

				<DottedSeparator />

				<section className="space-y-3">
					<div>
						<h3 className="text-sm font-medium">{t("datasheet.capabilities")}</h3>
						<p className="text-muted-foreground text-xs">{t("datasheet.capabilitiesDescription")}</p>
					</div>
					<div className="grid grid-cols-1 gap-x-4 gap-y-3 md:grid-cols-2">
						{CAPABILITY_FLAGS.map((flag) => (
							<div key={flag}>
								{/* i18n-ignore: model-parameters field names are API identifiers */}
								<Label className="font-mono text-xs">{flag}</Label>
								<Controller
									control={control}
									name={`flags.${flag}`}
									render={({ field }) => (
										<Select value={field.value} onValueChange={field.onChange}>
											<SelectTrigger className="mt-1 h-8 w-full" data-testid={`datasheet-flag-select-${flag}`}>
												<SelectValue />
											</SelectTrigger>
											<SelectContent>
												<SelectItem value="upstream">{triStateLabel("upstream", upstreamParams[flag])}</SelectItem>
												<SelectItem value="on">{t("datasheet.on")}</SelectItem>
												<SelectItem value="off">{t("datasheet.off")}</SelectItem>
											</SelectContent>
										</Select>
									)}
								/>
								{drift.params.has(flag) && hint("params", flag, upstreamParams[flag])}
							</div>
						))}
					</div>
					<div>
						<Label className="text-sm">{t("datasheet.advancedParams")}</Label>
						<p className="text-muted-foreground mb-2 text-xs">{t("datasheet.advancedParamsDescription")}</p>
						<Controller
							control={control}
							name="otherParamsJSON"
							render={({ field }) => (
								<CodeEditor
									id="datasheet-params-json"
									className="rounded-sm border"
									lang="json"
									code={field.value}
									onChange={field.onChange}
									height={160}
								/>
							)}
						/>
						{errors.otherParamsJSON && <p className="text-destructive mt-1 text-xs">{errors.otherParamsJSON.message}</p>}
						{[...drift.params]
							.filter((key) => !(CAPABILITY_FLAGS as readonly string[]).includes(key))
							.map((key) => (
								<div key={key}>{hint("params", key, upstreamParams[key])}</div>
							))}
					</div>
				</section>
			</div>

			<div className="bg-card sticky bottom-0 shrink-0 border-t px-4 py-4 md:px-8">
				<div className="flex items-center justify-between gap-3">
					<div>
						{existing && (
							<Button
								type="button"
								variant="ghost"
								disabled={!canEdit || isDeleting}
								onClick={() => onResetAll(existing)}
								data-testid="datasheet-reset-all-btn"
							>
								<RotateCcw className="h-4 w-4" />
								{isCustom ? t("datasheet.deleteCustom") : t("datasheet.resetAll")}
							</Button>
						)}
					</div>
					<div className="flex items-center gap-3">
						{!canEdit && <p className="text-destructive text-sm">{t("datasheet.noPermission")}</p>}
						<Button type="button" variant="outline" onClick={onClose} data-testid="datasheet-cancel-btn">
							{tc("cancel")}
						</Button>
						<Button type="submit" disabled={!canEdit || isSaving || (!isNew && !isDirty)} data-testid="datasheet-save-btn">
							{isSaving ? t("datasheet.saving") : tc("save")}
						</Button>
					</div>
				</div>
			</div>
		</form>
	);
}