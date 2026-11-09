import { Alert, AlertDescription } from "@/components/ui/alert";
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
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { ClassifierStatusBadge } from "@/app/workspace/complexity-router/views/classifierStatusBadge";
import ClassifierSettingsSheet from "@/app/workspace/complexity-router/views/classifierSettingsSheet";
import EmbeddingConfigSheet from "@/app/workspace/complexity-router/views/embeddingConfigSheet";
import { SectionHeading } from "@/app/workspace/complexity-router/views/formPrimitives";
import { LLMClassifierFields } from "@/app/workspace/complexity-router/views/llmClassifierFields";
import SuggestionSettingsSheet from "@/app/workspace/complexity-router/views/suggestionSettingsSheet";
import {
	ClassifierChoice,
	DecisionFields,
	DecisionGuidanceSection,
	DecisionProviderAlert,
	LLMPromptSection,
	PhraseTierGrid,
	SessionRoutingCard,
} from "@/app/workspace/complexity-router/views/sections";
import { MAX_SEMANTIC_PHRASES } from "@/lib/types/complexityRouter";
import { cn } from "@/lib/utils";
import { ChevronRight, ExternalLink, Info, LoaderCircle, RotateCcw, Settings2, TriangleAlert } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { RouterConfigForm } from "./useRouterConfigForm";

const DOCS_URL = "https://docs.getbifrost.ai/features/complexity-router";

// A dot on a settings button: its sheet holds edits that are not saved yet.
function UnsavedDot({ label }: { label: string }) {
	return <span className="size-1.5 rounded-full bg-amber-500" role="status" aria-label={label} />;
}

// RouterConfigurationActions are the buttons beside the configuration's title:
// the semantic classifier's readiness, the set-once settings of whichever
// classifier the router uses, and the docs.
export function RouterConfigurationActions({ config }: { config: RouterConfigForm }) {
	const { t } = useTranslation("models");
	const { t: tcpx } = useTranslation("models", { keyPrefix: "routing.complexityUi" });
	const { live, derived, status, providers, sheets, isCreating, saved, canUpdate } = config;
	const classifier = live.classifier;
	return (
		<>
			{classifier === "semantic" && !isCreating && (
				<ClassifierStatusBadge
					status={status.semantic}
					semanticFallback={saved?.semantic?.fallback ?? "none"}
					isLoading={status.loading}
					isNotConfigured={!derived.isClassifierConfigured}
					isNotSaved={derived.isClassifierConfigured && !saved?.semantic}
					hasUnsavedChanges={derived.willReembed}
					hasEmbeddingProviders={providers.embedding.length > 0}
					statusUnavailable={status.isError && !status.semantic}
					statusRefreshFailed={status.isError && Boolean(status.semantic)}
					isRetryingStatus={status.fetching}
					canRetryWarmup={canUpdate}
					isRetryingWarmup={status.isRetryingWarmup}
					onConfigure={() => sheets.setOpen("embedding")}
					onRetryStatus={() => void status.refetch()}
					onRetryWarmup={status.retryWarmup}
				/>
			)}
			{classifier === "semantic" && (
				<Button
					type="button"
					variant="outline"
					size="sm"
					onClick={() => sheets.setOpen("embedding")}
					data-testid="complexity-router-embedding-config-button"
				>
					<Settings2 className="size-3.5" />
					{derived.isClassifierConfigured ? tcpx("editEmbedding") : tcpx("configureEmbedding")}
					{derived.hasUnsavedEmbeddingConfigChanges && <UnsavedDot label={tcpx("unsavedEmbeddingAria")} />}
				</Button>
			)}
			{classifier === "decision" && (
				<Button
					type="button"
					variant="outline"
					size="sm"
					onClick={() => sheets.setOpen("decision")}
					data-testid="complexity-router-decision-settings-button"
				>
					<Settings2 className="size-3.5" />
					{tcpx("editModelConfiguration")}
					{derived.hasUnsavedDecisionSettingsChanges && <UnsavedDot label={tcpx("unsavedModelAria")} />}
				</Button>
			)}
			{classifier === "llm" && (
				<Button
					type="button"
					variant="outline"
					size="sm"
					onClick={() => sheets.setOpen("llm")}
					data-testid="complexity-router-llm-settings-button"
				>
					<Settings2 className="size-3.5" />
					{tcpx("editModelConfiguration")}
					{derived.hasUnsavedLLMSettingsChanges && <UnsavedDot label={tcpx("unsavedModelAria")} />}
				</Button>
			)}
			<Button asChild variant="outline" size="sm" data-testid="complexity-router-docs-link">
				<a href={DOCS_URL} target="_blank" rel="noopener noreferrer">
					<ExternalLink className="size-3.5" />
					{t("routing.complexityDocs")}
				</a>
			</Button>
		</>
	);
}

// CLASSIFIER_NAME_KEYS name each classifier in the collapsed summary.
export const CLASSIFIER_NAME_KEYS = {
	semantic: "classifierNameSemantic",
	decision: "classifierNameDecision",
	llm: "classifierNameLlm",
} as const;

// ClassifierSection shows which classifier a saved router uses as one summary
// line. The choice is made once, in the creation wizard, and switching it
// changes what everything below works from, so its cards stay folded away
// until the operator opens the section. It folds like the fallback tier
// guidance: the whole header is the toggle.
function ClassifierSection({ config }: { config: RouterConfigForm }) {
	const { t: tcpx } = useTranslation("models", { keyPrefix: "routing.complexityUi" });
	const [open, setOpen] = useState(false);
	const { classifier, semantic, llm } = config.live;
	const decisionModel = config.form.watch("decision.model");
	const model = classifier === "semantic" ? semantic?.embedding_model : classifier === "llm" ? llm?.model : decisionModel;
	const header = (
		<span className="min-w-0 space-y-1">
			<span className="block text-sm font-semibold">{tcpx("stepClassifier")}</span>
			<span className="text-muted-foreground block truncate text-xs" data-testid="model-router-classifier-summary">
				{tcpx(CLASSIFIER_NAME_KEYS[classifier])}
				{model && ` · ${model}`}
			</span>
		</span>
	);
	return (
		<Collapsible open={open} onOpenChange={setOpen} className="bg-card rounded-sm border" data-testid="model-router-classifier-section">
			<div className="flex items-center justify-between gap-2 px-4 py-3">
				{config.canUpdate ? (
					<CollapsibleTrigger asChild>
						<button type="button" className="flex min-w-0 items-start gap-2 text-left" data-testid="model-router-classifier-change-button">
							<ChevronRight className={cn("text-muted-foreground mt-0.5 size-4 shrink-0 transition-transform", open && "rotate-90")} />
							{header}
						</button>
					</CollapsibleTrigger>
				) : (
					header
				)}
			</div>
			<CollapsibleContent className="space-y-3 border-t p-4">
				<p className="text-muted-foreground text-sm">{tcpx("classifierSwitchHelp")}</p>
				<ClassifierChoice value={classifier} onChange={config.selectClassifier} includeLlm />
			</CollapsibleContent>
		</Collapsible>
	);
}

// RouterConfigurationEditor is what the router's classifier works from: the
// phrase lists for semantic, the per-tier guidance for the decision model, the
// prompt for the llm, and session-aware routing for all three. A saved router
// also shows its classifier, folded to a summary (see ClassifierSection); the
// creation wizard picks it on a step of its own.
export function RouterConfigurationEditor({ config, showClassifierChoice }: { config: RouterConfigForm; showClassifierChoice?: boolean }) {
	const { t: tcpx } = useTranslation("models", { keyPrefix: "routing.complexityUi" });
	const { form, errors, live, derived, defaults, status, prompt, canUpdate, isCreating, submitError } = config;
	const { control, setValue } = form;
	const classifier = live.classifier;

	// Only a full re-embed is reported in the embedding sheet, since every field
	// that causes one lives there; adding phrases is a page edit and is
	// reported here.
	const reembedWarning =
		classifier === "semantic" && derived.willReembedAll ? (
			<Alert variant="warning" data-testid="complexity-router-reembed-warning">
				<TriangleAlert className="h-4 w-4" />
				<AlertDescription>{tcpx("reembedAll", { count: derived.totalPhrases })}</AlertDescription>
			</Alert>
		) : classifier === "semantic" && derived.newPhraseCount > 0 ? (
			<Alert variant="warning" data-testid="complexity-router-new-phrase-warning">
				<TriangleAlert className="h-4 w-4" />
				<AlertDescription>
					{tcpx("newPhraseWarning", { count: derived.newPhraseCount, reused: derived.reusedPhraseCount })}
				</AlertDescription>
			</Alert>
		) : null;

	const decisionGuidance = (variant: "primary" | "fallback") => (
		<DecisionGuidanceSection
			control={control}
			setValue={setValue}
			errors={errors.decision}
			canUpdate={canUpdate}
			defaults={defaults.decision}
			defaultsLoading={status.loading || status.fetching}
			variant={variant}
		/>
	);
	const llmPrompt = (variant: "primary" | "fallback") => (
		<LLMPromptSection
			control={control}
			error={errors.llm?.prompt}
			canUpdate={canUpdate}
			defaultPrompt={defaults.llmPrompt}
			livePrompt={live.prompt}
			onEdit={prompt.markEdited}
			onReset={prompt.reset}
			variant={variant}
		/>
	);

	return (
		<div className="space-y-6" data-testid="model-router-configuration">
			{showClassifierChoice && <ClassifierSection config={config} />}

			{classifier === "semantic" && (
				<div className="space-y-3">
					<SectionHeading
						title={tcpx("phraseMappingTitle")}
						aside={
							<span className="text-muted-foreground font-mono text-[11px] tabular-nums" data-testid="complexity-router-phrase-total">
								{derived.isClassifierConfigured
									? tcpx("phrasesCap", { count: derived.totalPhrases, max: MAX_SEMANTIC_PHRASES })
									: tcpx("phraseCount", { count: derived.totalPhrases })}
							</span>
						}
					/>
					{isCreating && (
						<Alert variant="info" data-testid="model-router-starter-phrases-notice">
							<Info className="h-4 w-4" />
							<AlertDescription>{tcpx("starterPhrasesNotice")}</AlertDescription>
						</Alert>
					)}
					<PhraseTierGrid control={control} errors={errors.keywords} />
				</div>
			)}

			{classifier === "decision" && decisionGuidance("primary")}
			{classifier === "llm" && llmPrompt("primary")}

			<SessionRoutingCard control={control} errors={errors.session} canUpdate={canUpdate} />

			{/* Only one fallback can be on, so the two share a slot. */}
			{derived.isLLMFallbackEnabled && llmPrompt("fallback")}
			{derived.isDecisionFallbackEnabled && decisionGuidance("fallback")}

			{reembedWarning}

			{submitError && (
				<div
					role="alert"
					className="border-destructive/40 bg-destructive/10 text-destructive rounded-sm border px-3 py-2 font-mono text-sm"
				>
					{submitError}
				</div>
			)}
		</div>
	);
}

// RouterConfigurationRestoreButton puts the classifier's shipped starting
// point back: the built-in phrases for semantic, the shipped tier guidance for
// the decision model. The llm prompt resets from its own editor.
export function RouterConfigurationRestoreButton({ config }: { config: RouterConfigForm }) {
	const { t } = useTranslation("models");
	const { live, derived, restore, isSaving, canUpdate, isCreating } = config;
	if (live.classifier === "llm") return null;
	if (live.classifier === "decision") {
		return (
			<Button
				data-testid="complexity-router-decision-restore-defaults-button"
				type="button"
				variant="ghost"
				size="sm"
				onClick={restore.decision}
				disabled={!canUpdate || isSaving || derived.isDecisionGuidanceAtDefaults}
			>
				<RotateCcw className="h-3.5 w-3.5" />
				{t("routing.restoreDefaults")}
			</Button>
		);
	}
	return (
		<Button
			data-testid="complexity-router-restore-defaults-button"
			type="button"
			variant="ghost"
			size="sm"
			onClick={() => (isCreating ? restore.phrases() : restore.setDialogOpen(true))}
			disabled={!canUpdate || isSaving || restore.isResetting}
		>
			{restore.isResetting ? <LoaderCircle className="h-3.5 w-3.5 animate-spin" /> : <RotateCcw className="h-3.5 w-3.5" />}
			{t("routing.restoreDefaults")}
		</Button>
	);
}

// RouterConfigurationSheets are the configuration's sheets and dialogs. They
// are mounted with the page rather than with a tab, so the Suggestions tab can
// open the suggestion settings that save with the rest of the configuration.
export function RouterConfigurationSheets({ config }: { config: RouterConfigForm }) {
	const { t } = useTranslation("models");
	const { t: tc } = useTranslation("common");
	const { t: tcpx } = useTranslation("models", { keyPrefix: "routing.complexityUi" });
	const { form, errors, live, derived, providers, sheets, restore, status, canUpdate, canSave, isSaving, submitError } = config;
	const { control, register, setValue } = form;
	const save = () => void config.submit();
	const sheetProps = { canSave, isSaving, onSave: save, submitError };

	const decisionSettings = (
		<div className="space-y-4" data-testid="complexity-router-decision-settings">
			{!providers.loading && <DecisionProviderAlert state={providers.decisionState} provider={live.decisionProvider} />}
			<DecisionFields
				control={control}
				register={register}
				setValue={setValue}
				errors={errors.decision}
				canUpdate={canUpdate}
				providers={providers.all}
				providerKeyIds={providers.keyIds.decision}
			/>
		</div>
	);

	return (
		<>
			<EmbeddingConfigSheet
				open={sheets.open === "embedding"}
				onOpenChange={(open) => sheets.setOpen(open ? "embedding" : null)}
				control={control}
				register={register}
				setValue={setValue}
				errors={errors.semantic}
				semantic={live.semantic}
				llmErrors={errors.llm}
				llm={live.llm}
				canUpdate={canUpdate}
				providers={providers.embedding}
				providerKeyIds={providers.keyIds.semantic}
				llmProviders={providers.chat}
				llmProviderKeyIds={providers.keyIds.llm}
				providersLoading={providers.loading}
				isVectorStoreConnected={derived.isVectorStoreConnected}
				warning={
					derived.willReembedAll ? (
						<Alert variant="warning">
							<TriangleAlert className="h-4 w-4" />
							<AlertDescription>{tcpx("reembedAll", { count: derived.totalPhrases })}</AlertDescription>
						</Alert>
					) : null
				}
				decisionSettings={decisionSettings}
				{...sheetProps}
			/>

			<ClassifierSettingsSheet
				open={sheets.open === "decision" && live.classifier === "decision"}
				onOpenChange={(open) => sheets.setOpen(open ? "decision" : null)}
				title={tcpx("decisionSheetTitle")}
				description={tcpx("decisionSheetDescription")}
				testIdPrefix="complexity-router-decision"
				{...sheetProps}
			>
				{decisionSettings}
			</ClassifierSettingsSheet>

			<ClassifierSettingsSheet
				open={sheets.open === "llm" && live.classifier === "llm"}
				onOpenChange={(open) => sheets.setOpen(open ? "llm" : null)}
				title={tcpx("decisionSheetTitle")}
				description={tcpx("llmSheetDescription")}
				testIdPrefix="complexity-router-llm"
				{...sheetProps}
			>
				<LLMClassifierFields
					variant="primary"
					control={control}
					register={register}
					setValue={setValue}
					errors={errors.llm}
					llm={live.llm}
					canUpdate={canUpdate}
					providers={providers.chat}
					providerKeyIds={providers.keyIds.llm}
					providersLoading={providers.loading}
				/>
			</ClassifierSettingsSheet>

			<SuggestionSettingsSheet
				open={sheets.open === "suggestions"}
				onOpenChange={(open) => sheets.setOpen(open ? "suggestions" : null)}
				control={control}
				register={register}
				setValue={setValue}
				errors={errors.suggestions}
				canUpdate={canUpdate}
				chatProviders={providers.chat}
				keys={providers.keys}
				fallbackModel={live.llm?.provider && live.llm?.model ? `${live.llm.provider}/${live.llm.model}` : undefined}
				defaultInstructions={status.semantic?.suggestion_default_instructions ?? ""}
				{...sheetProps}
			/>

			<AlertDialog open={restore.dialogOpen} onOpenChange={restore.setDialogOpen}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>{t("routing.restoreDefaults")}</AlertDialogTitle>
						<AlertDialogDescription>{tcpx("restoreDefaultsDesc")}</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel
							data-testid="complexity-router-restore-cancel-button"
							onClick={() => restore.setDialogOpen(false)}
							disabled={restore.isResetting}
						>
							{tc("cancel")}
						</AlertDialogCancel>
						<AlertDialogAction
							data-testid="complexity-router-restore-confirm-button"
							onClick={() => {
								restore.setDialogOpen(false);
								restore.phrases();
							}}
							disabled={!canUpdate || restore.isResetting}
						>
							{t("routing.restoreDefaults")}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</>
	);
}