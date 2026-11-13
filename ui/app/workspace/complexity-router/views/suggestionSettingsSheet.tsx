import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ModelSelector } from "@/components/ui/modelSelector";
import { ProviderSelector } from "@/components/ui/providerSelector";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { getLocale } from "@/lib/i18n";
import {
	MAX_SUGGESTION_INSTRUCTIONS_CHARACTERS,
	MAX_SUGGESTION_LOOKBACK_DAYS,
	SUGGESTION_EVERY_OPTIONS,
} from "@/lib/types/complexityRouter";
import { ModelProvider, ModelProviderName } from "@/lib/types/config";
import { DBKey } from "@/lib/types/governance";
import { LoaderCircle, RotateCcw, Save } from "lucide-react";
import { useCallback, useMemo } from "react";
import { Control, Controller, FieldErrors, UseFormRegister, UseFormSetValue, useWatch } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { AnalyzerFormValues, fromLocalDateTime, nextSuggestionRun } from "../formSchema";
import { FieldLabel } from "./formPrimitives";

interface Props {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	control: Control<AnalyzerFormValues>;
	register: UseFormRegister<AnalyzerFormValues>;
	setValue: UseFormSetValue<AnalyzerFormValues>;
	errors: FieldErrors<AnalyzerFormValues>["suggestions"];
	canUpdate: boolean;
	// Chat-capable providers with a serving key, the same pool the llm
	// fallback picks from.
	chatProviders: ModelProvider[];
	// Every key, so the model list can be narrowed to what the chosen
	// provider's enabled keys may serve.
	keys: DBKey[];
	// The llm fallback's model, which writes suggestions when none is set here.
	fallbackModel?: string;
	defaultInstructions: string;
	canSave: boolean;
	isSaving: boolean;
	onSave: () => void;
	submitError: string | null;
}

// The browser's time zone, which the first-run field is entered in.
function localTimeZone(): string {
	try {
		return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
	} catch {
		return "UTC";
	}
}

// SuggestionSettingsSheet edits the suggestions block: whether traffic is
// sampled for suggestions, when runs happen, how much they cluster, and which
// model and instructions write the phrases. Its fields are part of the page's
// form, so Save here saves everything, as the other sheets do.
export default function SuggestionSettingsSheet({
	open,
	onOpenChange,
	control,
	register,
	setValue,
	errors,
	canUpdate,
	chatProviders,
	keys,
	fallbackModel,
	defaultInstructions,
	canSave,
	isSaving,
	onSave,
	submitError,
}: Props) {
	const { t } = useTranslation("models", { keyPrefix: "routing.complexityUi.suggestions" });
	const { t: tCommon } = useTranslation("common");
	const every = useWatch({ control, name: "suggestions.every" });
	const provider = useWatch({ control, name: "suggestions.provider" });
	const instructions = useWatch({ control, name: "suggestions.instructions" });
	const enabled = useWatch({ control, name: "suggestions.enabled" });
	const startAt = useWatch({ control, name: "suggestions.start_at" });
	const zone = useMemo(localTimeZone, []);
	// The next run the two schedule fields give, as the gateway counts it once
	// the settings are saved. Unknown while the first run is invalid.
	const firstRun = fromLocalDateTime(startAt ?? "");
	const nextRun = firstRun ? nextSuggestionRun(every, firstRun, new Date()) : undefined;
	// Memoized: ModelSelector refetches whenever this array's identity changes.
	const providerKeyIds = useMemo(
		() => keys.filter((key) => key.provider === provider && key.enabled !== false).map((key) => key.key_id),
		[keys, provider],
	);
	const isChatProvider = useCallback((candidate: ModelProvider) => chatProviders.some((p) => p.name === candidate.name), [chatProviders]);
	const disabled = !canUpdate;
	// Each message has a stable id that its control points at, only while the
	// error is shown.
	const errorId = (field: string) => `suggestions-${field}-error`;
	const fieldError = (field: string, message?: string) =>
		message ? (
			<p id={errorId(field)} className="text-destructive text-xs">
				{message}
			</p>
		) : null;
	const errorAria = (field: string, message?: string) => (message ? { "aria-invalid": true, "aria-describedby": errorId(field) } : {});

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="flex flex-col p-0" data-testid="complexity-suggestions-settings-sheet">
				<SheetHeader className="flex flex-col items-start gap-1 py-4" headerClassName="bg-card z-10 mb-0 border-b px-4 md:px-6">
					<SheetTitle>{t("settingsTitle")}</SheetTitle>
					<SheetDescription>{t("settingsDescription")}</SheetDescription>
				</SheetHeader>

				<div className="custom-scrollbar min-h-0 flex-1 space-y-6 overflow-y-auto px-6 py-5">
					<div className="flex items-start justify-between gap-4">
						<div className="space-y-1">
							<FieldLabel htmlFor="suggestions-enabled">{t("enabledLabel")}</FieldLabel>
							<p className="text-muted-foreground text-xs leading-relaxed">{t("enabledDescription")}</p>
						</div>
						<Controller
							control={control}
							name="suggestions.enabled"
							render={({ field }) => (
								<Switch
									id="suggestions-enabled"
									data-testid="complexity-suggestions-enabled-switch"
									checked={field.value}
									onCheckedChange={field.onChange}
									disabled={disabled}
								/>
							)}
						/>
					</div>

					<fieldset className="space-y-4" disabled={disabled}>
						<legend className="text-sm font-semibold">{t("captureHeading")}</legend>
						<div className="grid gap-4 sm:grid-cols-2">
							<div className="space-y-1.5">
								<FieldLabel htmlFor="suggestions-sample" tooltip={t("sampleTip")}>
									{t("sampleLabel")}
								</FieldLabel>
								<Input
									id="suggestions-sample"
									data-testid="complexity-suggestions-sample-input"
									type="number"
									// Any precision: a small saved rate (0.0001%) is valid, and
									// the schema, not the browser, says what is not.
									step="any"
									max={100}
									{...errorAria("sample", errors?.sample_percent?.message)}
									{...register("suggestions.sample_percent", { valueAsNumber: true })}
								/>
								{fieldError("sample", errors?.sample_percent?.message)}
							</div>
							<div className="space-y-1.5">
								<FieldLabel htmlFor="suggestions-lookback" tooltip={t("lookbackTip")}>
									{t("lookbackLabel")}
								</FieldLabel>
								<Input
									id="suggestions-lookback"
									data-testid="complexity-suggestions-lookback-input"
									type="number"
									min={1}
									max={MAX_SUGGESTION_LOOKBACK_DAYS}
									{...errorAria("lookback", errors?.lookback_days?.message)}
									{...register("suggestions.lookback_days", { valueAsNumber: true })}
								/>
								{fieldError("lookback", errors?.lookback_days?.message)}
							</div>
						</div>
					</fieldset>

					<fieldset className="space-y-4" disabled={disabled}>
						<legend className="text-sm font-semibold">{t("scheduleHeading")}</legend>
						<div className="grid gap-4 sm:grid-cols-2">
							<div className="space-y-1.5">
								<FieldLabel htmlFor="suggestions-every">{t("everyLabel")}</FieldLabel>
								<Controller
									control={control}
									name="suggestions.every"
									render={({ field }) => (
										<Select value={field.value} onValueChange={field.onChange} disabled={disabled}>
											<SelectTrigger id="suggestions-every" data-testid="complexity-suggestions-every-select" className="w-full">
												<SelectValue />
											</SelectTrigger>
											<SelectContent>
												{SUGGESTION_EVERY_OPTIONS.map((option) => (
													<SelectItem key={option} value={option} data-testid={`complexity-suggestions-every-option-${option}`}>
														{t(`every.${option}`)}
													</SelectItem>
												))}
											</SelectContent>
										</Select>
									)}
								/>
							</div>
							<div className="space-y-1.5">
								<FieldLabel htmlFor="suggestions-start-at" tooltip={t("firstRunTip")}>
									{t("firstRunLabel", { zone })}
								</FieldLabel>
								<Input
									id="suggestions-start-at"
									data-testid="complexity-suggestions-start-at-input"
									type="datetime-local"
									{...errorAria("start-at", errors?.start_at?.message)}
									{...register("suggestions.start_at")}
								/>
								{fieldError("start-at", errors?.start_at?.message)}
							</div>
						</div>
						<p className="text-muted-foreground text-xs leading-relaxed" data-testid="complexity-suggestions-schedule-next-run">
							{nextRun
								? t("scheduleNextRun", { time: nextRun.toLocaleString(getLocale(), { dateStyle: "medium", timeStyle: "short" }) })
								: t("scheduleNextRunUnknown")}
						</p>
					</fieldset>

					<fieldset className="space-y-4" disabled={disabled}>
						<legend className="text-sm font-semibold">{t("modelHeading")}</legend>
						<div className="grid gap-4 sm:grid-cols-2">
							<div className="space-y-1.5">
								<FieldLabel htmlFor="suggestions-provider">{t("providerLabel")}</FieldLabel>
								<Controller
									control={control}
									name="suggestions.provider"
									render={({ field }) => (
										<ProviderSelector
											inputId="suggestions-provider"
											data-testid="complexity-suggestions-provider-select"
											filter={isChatProvider}
											ariaInvalid={errors?.provider?.message ? true : undefined}
											ariaDescribedBy={errors?.provider?.message ? errorId("provider") : undefined}
											value={field.value || ""}
											onChange={(value: string) => {
												if (value === field.value) return;
												field.onChange(value as ModelProviderName);
												setValue("suggestions.model", "", { shouldDirty: true });
											}}
											disabled={disabled}
										/>
									)}
								/>
								{fieldError("provider", errors?.provider?.message)}
							</div>
							<div className="space-y-1.5">
								<FieldLabel htmlFor="suggestions-model">{t("modelLabel")}</FieldLabel>
								<Controller
									control={control}
									name="suggestions.model"
									render={({ field }) => (
										<ModelSelector
											inputId="suggestions-model"
											data-testid="complexity-suggestions-model-select"
											provider={provider || undefined}
											keys={providerKeyIds}
											ariaInvalid={errors?.model?.message ? true : undefined}
											ariaDescribedBy={errors?.model?.message ? errorId("model") : undefined}
											value={field.value ?? ""}
											onChange={field.onChange}
											allowCustomModel
											placeholder={provider ? t("searchModel") : t("selectProviderFirst")}
											disabled={disabled || !provider}
										/>
									)}
								/>
								{fieldError("model", errors?.model?.message)}
							</div>
						</div>
						<div className="flex flex-wrap items-center justify-between gap-2">
							<p className="text-muted-foreground text-xs" data-testid="complexity-suggestions-model-hint">
								{provider ? t("modelOwnHint") : fallbackModel ? t("modelFallbackHint", { model: fallbackModel }) : t("modelMissingHint")}
							</p>
							{provider && (
								<Button
									type="button"
									variant="ghost"
									size="sm"
									data-testid="complexity-suggestions-model-clear-button"
									onClick={() => {
										setValue("suggestions.provider", "", { shouldDirty: true });
										setValue("suggestions.model", "", { shouldDirty: true });
									}}
								>
									{t("useFallbackModel")}
								</Button>
							)}
						</div>
					</fieldset>

					<fieldset className="space-y-2" disabled={disabled}>
						<div className="flex items-center justify-between gap-2">
							<legend className="text-sm font-semibold">{t("instructionsHeading")}</legend>
							<div className="flex gap-1">
								{!instructions && defaultInstructions && (
									<Button
										type="button"
										variant="ghost"
										size="sm"
										data-testid="complexity-suggestions-instructions-default-button"
										onClick={() => setValue("suggestions.instructions", defaultInstructions, { shouldDirty: true })}
									>
										{t("editDefaultInstructions")}
									</Button>
								)}
								{instructions && (
									<Button
										type="button"
										variant="ghost"
										size="sm"
										data-testid="complexity-suggestions-instructions-reset-button"
										onClick={() => setValue("suggestions.instructions", "", { shouldDirty: true })}
									>
										<RotateCcw className="size-3.5" />
										{t("resetInstructions")}
									</Button>
								)}
							</div>
						</div>
						<p className="text-muted-foreground text-xs leading-relaxed">{t("instructionsDescription")}</p>
						<Textarea
							data-testid="complexity-suggestions-instructions-input"
							aria-label={t("instructionsHeading")}
							rows={10}
							maxLength={MAX_SUGGESTION_INSTRUCTIONS_CHARACTERS}
							placeholder={t("instructionsPlaceholder")}
							className="font-mono text-xs"
							{...errorAria("instructions", errors?.instructions?.message)}
							{...register("suggestions.instructions")}
						/>
						<p className="text-muted-foreground text-right font-mono text-[11px] tabular-nums">
							{t("instructionsCount", { n: instructions.length, max: MAX_SUGGESTION_INSTRUCTIONS_CHARACTERS })}
						</p>
						{fieldError("instructions", errors?.instructions?.message)}
					</fieldset>

					{!enabled && (
						<p className="text-muted-foreground text-xs" data-testid="complexity-suggestions-disabled-note">
							{t("disabledNote")}
						</p>
					)}
					{submitError && (
						<div
							role="alert"
							className="border-destructive/40 bg-destructive/10 text-destructive rounded-sm border px-3 py-2 font-mono text-sm"
						>
							{submitError}
						</div>
					)}
				</div>

				<SheetFooter className="bg-card flex-row items-center justify-end gap-2 border-t px-6 py-4">
					<Button
						type="button"
						variant="outline"
						size="sm"
						onClick={() => onOpenChange(false)}
						data-testid="complexity-suggestions-settings-close-button"
					>
						{tCommon("close")}
					</Button>
					<Button
						type="button"
						size="sm"
						onClick={onSave}
						disabled={!canSave || isSaving}
						data-testid="complexity-suggestions-settings-save-button"
					>
						{isSaving ? <LoaderCircle className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />}
						{t("saveSettings")}
					</Button>
				</SheetFooter>
			</SheetContent>
		</Sheet>
	);
}