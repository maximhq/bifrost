import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ModelSelector } from "@/components/ui/modelSelector";
import { ProviderSelector } from "@/components/ui/providerSelector";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import {
	MAX_SUGGESTION_INSTRUCTIONS_CHARACTERS,
	MAX_SUGGESTION_LOOKBACK_DAYS,
	MAX_SUGGESTION_MAX_CLUSTERED,
	MAX_SUGGESTION_MIN_CLUSTER,
	MIN_SUGGESTION_MAX_CLUSTERED,
	SUGGESTION_EVERY_OPTIONS,
	SUGGESTION_WEEKDAYS,
} from "@/lib/types/complexityRouter";
import { ModelProvider, ModelProviderName } from "@/lib/types/config";
import { DBKey } from "@/lib/types/governance";
import { LoaderCircle, RotateCcw, Save } from "lucide-react";
import { useCallback, useMemo } from "react";
import { Control, Controller, FieldErrors, UseFormRegister, UseFormSetValue, useWatch } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { AnalyzerFormValues, suggestionScheduleUnit } from "../formSchema";
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

// IANA zones the browser knows, offered as suggestions in the timezone field.
function timezoneOptions(): string[] {
	const intl = Intl as typeof Intl & { supportedValuesOf?: (key: string) => string[] };
	try {
		return intl.supportedValuesOf ? intl.supportedValuesOf("timeZone") : [];
	} catch {
		return [];
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
	const unit = suggestionScheduleUnit(every);
	const zones = useMemo(timezoneOptions, []);
	// Memoized: ModelSelector refetches whenever this array's identity changes.
	const providerKeyIds = useMemo(
		() => keys.filter((key) => key.provider === provider && key.enabled !== false).map((key) => key.key_id),
		[keys, provider],
	);
	const isChatProvider = useCallback((candidate: ModelProvider) => chatProviders.some((p) => p.name === candidate.name), [chatProviders]);
	const disabled = !canUpdate;
	const fieldError = (message?: string) => (message ? <p className="text-destructive text-xs">{message}</p> : null);

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
									step="0.1"
									min={0.1}
									max={100}
									{...register("suggestions.sample_percent", { valueAsNumber: true })}
								/>
								{fieldError(errors?.sample_percent?.message)}
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
									{...register("suggestions.lookback_days", { valueAsNumber: true })}
								/>
								{fieldError(errors?.lookback_days?.message)}
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
							{unit !== "h" && (
								<div className="space-y-1.5">
									<FieldLabel htmlFor="suggestions-at">{t("atLabel")}</FieldLabel>
									<Input id="suggestions-at" data-testid="complexity-suggestions-at-input" type="time" {...register("suggestions.at")} />
									{fieldError(errors?.at?.message)}
								</div>
							)}
							{unit === "w" && (
								<div className="space-y-1.5">
									<FieldLabel htmlFor="suggestions-weekday">{t("weekdayLabel")}</FieldLabel>
									<Controller
										control={control}
										name="suggestions.weekday"
										render={({ field }) => (
											<Select value={field.value} onValueChange={field.onChange} disabled={disabled}>
												<SelectTrigger id="suggestions-weekday" data-testid="complexity-suggestions-weekday-select" className="w-full">
													<SelectValue />
												</SelectTrigger>
												<SelectContent>
													{SUGGESTION_WEEKDAYS.map((day) => (
														<SelectItem key={day} value={day}>
															{t(`weekday.${day}`)}
														</SelectItem>
													))}
												</SelectContent>
											</Select>
										)}
									/>
								</div>
							)}
							<div className="space-y-1.5">
								<FieldLabel htmlFor="suggestions-timezone" tooltip={t("timezoneTip")}>
									{t("timezoneLabel")}
								</FieldLabel>
								<Input
									id="suggestions-timezone"
									data-testid="complexity-suggestions-timezone-input"
									list="suggestions-timezones"
									autoComplete="off"
									{...register("suggestions.timezone")}
								/>
								<datalist id="suggestions-timezones">
									{zones.map((zone) => (
										<option key={zone} value={zone} />
									))}
								</datalist>
								{fieldError(errors?.timezone?.message)}
							</div>
						</div>
						<p className="text-muted-foreground text-xs leading-relaxed">{unit === "h" ? t("scheduleHourlyHint") : t("scheduleHint")}</p>
					</fieldset>

					<fieldset className="space-y-4" disabled={disabled}>
						<legend className="text-sm font-semibold">{t("clusteringHeading")}</legend>
						<div className="grid gap-4 sm:grid-cols-2">
							<div className="space-y-1.5">
								<FieldLabel htmlFor="suggestions-max-clustered" tooltip={t("maxClusteredTip")}>
									{t("maxClusteredLabel")}
								</FieldLabel>
								<Input
									id="suggestions-max-clustered"
									data-testid="complexity-suggestions-max-clustered-input"
									type="number"
									min={MIN_SUGGESTION_MAX_CLUSTERED}
									max={MAX_SUGGESTION_MAX_CLUSTERED}
									step={1000}
									{...register("suggestions.max_clustered_requests", { valueAsNumber: true })}
								/>
								{fieldError(errors?.max_clustered_requests?.message)}
							</div>
							<div className="space-y-1.5">
								<FieldLabel htmlFor="suggestions-min-cluster" tooltip={t("minClusterTip")}>
									{t("minClusterLabel")}
								</FieldLabel>
								<Input
									id="suggestions-min-cluster"
									data-testid="complexity-suggestions-min-cluster-input"
									type="number"
									min={2}
									max={MAX_SUGGESTION_MIN_CLUSTER}
									{...register("suggestions.min_cluster_requests", { valueAsNumber: true })}
								/>
								{fieldError(errors?.min_cluster_requests?.message)}
							</div>
						</div>
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
								{fieldError(errors?.provider?.message)}
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
											value={field.value ?? ""}
											onChange={field.onChange}
											allowCustomModel
											placeholder={provider ? t("searchModel") : t("selectProviderFirst")}
											disabled={disabled || !provider}
										/>
									)}
								/>
								{fieldError(errors?.model?.message)}
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
							rows={10}
							maxLength={MAX_SUGGESTION_INSTRUCTIONS_CHARACTERS}
							placeholder={t("instructionsPlaceholder")}
							className="font-mono text-xs"
							{...register("suggestions.instructions")}
						/>
						<p className="text-muted-foreground text-right font-mono text-[11px] tabular-nums">
							{t("instructionsCount", { n: instructions.length, max: MAX_SUGGESTION_INSTRUCTIONS_CHARACTERS })}
						</p>
						{fieldError(errors?.instructions?.message)}
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