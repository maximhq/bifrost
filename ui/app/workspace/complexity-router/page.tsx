import PageTitle from "@/components/pageTitle";
import FullPageLoader from "@/components/fullPageLoader";
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
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { ScrollArea } from "@/components/ui/scrollArea";
import { EmbeddingSupportedProviders } from "@/lib/constants/logs";
import { getErrorMessage, useGetCoreConfigQuery, useGetProvidersQuery } from "@/lib/store";
import { useGetAllKeysQuery } from "@/lib/store/apis/providersApi";
import {
	useGetComplexityAnalyzerConfigQuery,
	useGetComplexitySemanticStatusQuery,
	useRetryComplexitySemanticWarmupMutation,
	useResetComplexityAnalyzerConfigMutation,
	useUpdateComplexityAnalyzerConfigMutation,
} from "@/lib/store/apis/governanceApi";
import { MAX_SEMANTIC_PHRASES } from "@/lib/types/complexityRouter";
import { ModelProvider } from "@/lib/types/config";
import { DBKey } from "@/lib/types/governance";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { ArrowLeft, ArrowRight, ExternalLink, LoaderCircle, RotateCcw, Save, Settings2, TriangleAlert } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { useForm, useWatch } from "react-hook-form";
import { toast } from "sonner";
import {
	AnalyzerFormValues,
	analyzerConfigSchema,
	countCanonicalSemanticPhrases,
	DEFAULT_FORM_VALUES,
	getTypesafeState,
	isJevGuidanceDefault,
	isJevGuidanceEmpty,
	jevCriteriaFromDefaults,
	isRouterConfigured,
	jevGuidanceFormValues,
	toAnalyzerPayload,
	toFormValues,
	shouldSeedLLMPrompt,
} from "./formSchema";
import { ClassifierStatusBadge } from "./views/classifierStatusBadge";
import EmbeddingConfigSheet from "./views/embeddingConfigSheet";
import JevSettingsSheet from "./views/jevSettingsSheet";
import { SectionHeading } from "./views/formPrimitives";
import {
	ClassifierChoice,
	JevFields,
	JevGuidanceSection,
	LLMPromptSection,
	PhraseTierGrid,
	SessionRoutingCard,
	StepRail,
	TypesafeAlert,
} from "./views/sections";

// Embedding-capable providers gate this page, matching the local cache screen's
// rule: built-ins are listed in EmbeddingSupportedProviders, custom providers
// declare support through allowed_requests.embedding. A custom provider with no
// allowed_requests block at all is unrestricted, which is how the Go side reads
// a nil AllowedRequests.
const supportsEmbedding = (provider: ModelProvider): boolean => {
	if (provider.custom_provider_config) {
		const allowed = provider.custom_provider_config.allowed_requests;
		return !allowed || allowed.embedding === true;
	}
	return (EmbeddingSupportedProviders as readonly string[]).includes(provider.name);
};

// Supporting embeddings is not enough to be selectable: every embedding call
// this page makes — warmup and each classification —
// needs a key that is actually serving. A provider whose keys are all disabled
// looks configured on the providers screen but fails at request time, so
// offering it here only produces a configuration failure the operator has to decode.
// A key omits `enabled` when unset, which the Go side reads as enabled.
const hasEnabledKey = (provider: ModelProvider, keys: DBKey[]): boolean =>
	keys.some((key) => key.provider === provider.name && key.enabled !== false);

// The llm classifier needs chat completions rather than embeddings. Built-in
// providers all serve chat; custom providers declare support through
// allowed_requests.chat_completion, and no allowed_requests block at all means
// unrestricted, matching how the Go side reads a nil AllowedRequests.
const supportsChat = (provider: ModelProvider): boolean => {
	if (provider.custom_provider_config) {
		const allowed = provider.custom_provider_config.allowed_requests;
		return !allowed || allowed.chat_completion === true;
	}
	return true;
};

export default function ComplexityRouterPage() {
	const canUpdate = useRbac(RbacResource.RoutingRules, RbacOperation.Update);
	const { data, isLoading, isFetching, error, refetch } = useGetComplexityAnalyzerConfigQuery(undefined, {
		refetchOnMountOrArgChange: true,
	});
	const [updateConfig, { isLoading: isSaving }] = useUpdateComplexityAnalyzerConfigMutation();
	const [resetConfig, { isLoading: isResetting }] = useResetComplexityAnalyzerConfigMutation();
	const [retrySemanticWarmup, { isLoading: isRetryingWarmup }] = useRetryComplexitySemanticWarmupMutation();

	const [submitError, setSubmitError] = useState<string | null>(null);
	const [restoreDialogOpen, setRestoreDialogOpen] = useState(false);
	const [embeddingSheetOpen, setEmbeddingSheetOpen] = useState(false);
	const [jevSheetOpen, setJevSheetOpen] = useState(false);
	// The open step. Null follows the saved config: a router with nothing set
	// up opens on the classifier choice, an existing one straight on its setup.
	const [openStep, setOpenStep] = useState<"classifier" | "setup" | null>(null);
	// Whether a classifier has been picked on this visit. A new router's form
	// defaults to "semantic", which is not a choice the operator made.
	const [picked, setPicked] = useState(false);
	// An intentionally empty draft can equal the saved default and be non-dirty.
	// Track interaction separately so status refreshes never refill it.
	const promptEdited = useRef(false);

	// Refetched on every visit: provider and key fixes happen on the providers
	// page, and a cached list would keep describing the state before them.
	const { data: providersData, isLoading: providersLoading } = useGetProvidersQuery(undefined, { refetchOnMountOrArgChange: true });
	const { data: allKeys, isLoading: keysLoading } = useGetAllKeysQuery(undefined, { refetchOnMountOrArgChange: true });
	const embeddingProviders = useMemo(
		() => (providersData || []).filter((provider) => supportsEmbedding(provider) && hasEnabledKey(provider, allKeys || [])),
		[providersData, allKeys],
	);
	const chatProviders = useMemo(
		() => (providersData || []).filter((provider) => supportsChat(provider) && hasEnabledKey(provider, allKeys || [])),
		[providersData, allKeys],
	);
	// Jev authenticates through the Typesafe provider, so its state is what Jev
	// can be expected to do.
	const typesafe = useMemo(() => getTypesafeState(providersData, allKeys), [providersData, allKeys]);
	const typesafeReady = typesafe === "configured";

	const { data: coreConfig } = useGetCoreConfigQuery({ fromDB: true });
	const isVectorStoreConnected = coreConfig?.is_cache_connected ?? false;

	const {
		register,
		handleSubmit,
		reset,
		control,
		watch,
		setValue,
		getValues,
		formState: { errors, dirtyFields, isDirty, isSubmitted },
	} = useForm<AnalyzerFormValues>({
		resolver: zodResolver(analyzerConfigSchema),
		defaultValues: DEFAULT_FORM_VALUES,
		mode: "onSubmit",
		reValidateMode: "onChange",
	});

	// Both queries feed the provider list, so the empty state has to wait for
	// both: gating on one alone flashes "no provider configured" on every load.
	const isProviderListLoading = providersLoading || keysLoading;

	const liveClassifier = watch("classifier");
	const liveSemantic = watch("semantic");
	const liveLLM = watch("llm");
	const liveSession = watch("session");
	const liveKeywords = watch("keywords");

	// Narrows the model list to what this provider's enabled keys can actually
	// serve. /api/models only applies per-key allow-lists and blacklists when it
	// is handed key ids; without them it returns the whole provider pool, so the
	// dropdown offers models every key would reject. Memoized because
	// ModelSelector refetches whenever this array's identity changes.
	const enabledKeyIdsForProvider = useMemo(
		() => (allKeys || []).filter((key) => key.provider === liveSemantic?.provider && key.enabled !== false).map((key) => key.key_id),
		[allKeys, liveSemantic?.provider],
	);
	const enabledKeyIdsForLLMProvider = useMemo(
		() => (allKeys || []).filter((key) => key.provider === liveLLM?.provider && key.enabled !== false).map((key) => key.key_id),
		[allKeys, liveLLM?.provider],
	);

	const isClassifierConfigured = Boolean(liveSemantic?.provider && liveSemantic?.embedding_model);
	const isLLMFallbackEnabled = liveClassifier === "semantic" && liveSemantic?.fallback === "llm";
	const isJevFallbackEnabled = liveClassifier === "semantic" && liveSemantic?.fallback === "jev";
	const isJevInUse = liveClassifier === "jev" || isJevFallbackEnabled;
	// The embedding and llm fallback fields both live behind the same sheet, so a
	// pending edit to either would otherwise be invisible from the page.
	// react-hook-form keeps reverted fields in dirtyFields with a false value, so
	// the flags are what matter, not the key count.
	const hasUnsavedJevSettingsChanges = Boolean(dirtyFields.jev?.previous_message_count || dirtyFields.jev?.timeout);
	const hasUnsavedEmbeddingConfigChanges =
		Object.values(dirtyFields.semantic ?? {}).some(Boolean) || Object.values(dirtyFields.llm ?? {}).some(Boolean);
	const hasClassifier = isRouterConfigured(data) || picked;
	// Read-only operators cannot choose, so they always land on the setup step.
	const step = openStep ?? (canUpdate && !hasClassifier ? "classifier" : "setup");

	// Only the unsettled states are polled. Ready and disabled are steady until
	// the next save, which refetches through the cache tag anyway.
	//
	// Failed is polled because it is no longer terminal: the gateway re-arms the
	// classifier by itself when the provider it embeds through is fixed, and that
	// fix happens somewhere else entirely — the providers screen, often another
	// tab. Without this the badge would sit on "failed" describing a classifier
	// that had already recovered. It polls slowly because it is waiting on a
	// human, where warming is polled fast to keep the progress bar moving.
	const [statusPollInterval, setStatusPollInterval] = useState(0);
	// Also fetched as soon as the llm fallback or Jev is enabled in the form,
	// before any save: the endpoint carries llm_default_prompt and jev_defaults,
	// which seed those editors and power "Reset to default" — gating on the
	// saved config alone left a newly enabled classifier with no defaults until
	// after the first save.
	const {
		data: semanticStatus,
		isLoading: statusLoading,
		isFetching: statusFetching,
		isError: statusIsError,
		refetch: refetchStatus,
	} = useGetComplexitySemanticStatusQuery(undefined, {
		skip: !data?.semantic && !data?.llm && !isLLMFallbackEnabled && !isJevInUse,
		pollingInterval: statusPollInterval,
	});
	useEffect(() => {
		setStatusPollInterval(semanticStatus?.state === "warming" ? 2000 : semanticStatus?.state === "failed" ? 10000 : 0);
	}, [semanticStatus?.state]);

	const totalPhrases = useMemo(
		() =>
			countCanonicalSemanticPhrases({
				simple_keywords: liveKeywords?.simple_keywords ?? [],
				medium_keywords: liveKeywords?.medium_keywords ?? [],
				complex_keywords: liveKeywords?.complex_keywords ?? [],
			}).total,
		[liveKeywords],
	);

	// Every embedding-cost warning below is about what the pending save will do,
	// so it is gated on there being a pending save at all. Without this the page
	// compares the form against a stale `data` and bills a save that cannot
	// happen: Restore defaults persists server-side and resets the form, leaving
	// it clean while the config query has not refetched yet — the exact window
	// where a "saving will embed N phrases" line appears next to a disabled Save.
	const hasPendingSave = isDirty;

	// Shipped guidance initializes untouched drafts; an empty edited prompt is
	// valid and means "use default guidance" when saved.
	const defaultLLMPrompt = semanticStatus?.llm_default_prompt ?? "";
	const jevDefaults = semanticStatus?.jev_defaults;
	const liveJevCriteria = useWatch({ control, name: "jev.criteria" });
	// Jev's Restore defaults only refills the form: it saves like any other edit
	// and Discard undoes it, so unlike the semantic restore it needs no dialog.
	const isJevGuidanceAtDefaults = !jevDefaults || isJevGuidanceDefault(liveJevCriteria, jevDefaults);
	const restoreJevDefaults = () => {
		if (!jevDefaults) return;
		setValue("jev.criteria", jevCriteriaFromDefaults(jevDefaults), { shouldDirty: true, shouldValidate: true });
	};
	const livePrompt = liveLLM?.prompt ?? "";

	// Saving re-runs warmup, but what it costs depends on what changed, because
	// the gateway caches a vector per phrase (semanticEmbeddingCache).
	//
	// Provider and model are the cache's identity: changing either invalidates
	// every vector at once, so the whole list is re-embedded.
	//
	// An empty cache means the same thing. It lives only in the gateway's memory
	// — a stored vector cannot be read back out of a vector store — so a restart
	// drops every vector while the saved phrases look untouched. Inferring reuse
	// from the persisted config alone promised "N reused" for phrases the gateway
	// no longer holds, so the count comes from the status payload instead.
	const cachedPhrases = semanticStatus?.cached_phrases;
	const willReembedAll = useMemo(() => {
		if (!data || !isClassifierConfigured || !hasPendingSave) return false;
		const saved = data.semantic;
		if (!saved) return true;
		if (cachedPhrases !== undefined && cachedPhrases === 0) return true;
		return saved.provider !== liveSemantic?.provider || saved.embedding_model !== liveSemantic?.embedding_model;
	}, [data, isClassifierConfigured, hasPendingSave, liveSemantic, cachedPhrases]);

	// Editing the lists only pays for phrase text the gateway has not embedded
	// before. The cache is keyed by phrase alone, so moving a phrase between
	// tiers costs nothing — only genuinely new text reaches the provider.
	//
	// Both sides are compared in the gateway's own phrase space, not as typed:
	// normalizeComplexityKeywordList (framework/configstore) lowercases, trims,
	// and dedupes before anything is embedded or cached, so "Give me the SQL"
	// and "give me the sql" are one phrase and one embedding. Comparing raw text
	// counted every mixed-case phrase as new, which is most of the defaults.
	const { newPhraseCount, reusedPhraseCount } = useMemo(() => {
		if (!data || !isClassifierConfigured || !hasPendingSave || willReembedAll) {
			return { newPhraseCount: 0, reusedPhraseCount: 0 };
		}
		const normalize = (phrase: string) => phrase.trim().toLowerCase();
		const savedPhrases = new Set(
			[
				...(data.keywords?.simple_keywords ?? []),
				...(data.keywords?.medium_keywords ?? []),
				...(data.keywords?.complex_keywords ?? []),
			].map(normalize),
		);
		// A Set because the gateway dedupes too: the same phrase in two tiers is
		// one embedding, so counting it twice would overstate the bill.
		const live = new Set(
			[...(liveKeywords?.simple_keywords ?? []), ...(liveKeywords?.medium_keywords ?? []), ...(liveKeywords?.complex_keywords ?? [])]
				.map(normalize)
				.filter(Boolean),
		);
		let added = 0;
		live.forEach((phrase) => {
			if (!savedPhrases.has(phrase)) added += 1;
		});
		// The saved lists are what the gateway *last warmed*, not necessarily what
		// it still holds: retain only prunes to the active phrases after a warmup
		// that finished, so a run that failed partway leaves fewer vectors cached
		// than there are saved phrases. Cap reuse at what the gateway reports so
		// the two counts still sum to the live list rather than promising vectors
		// that are not there.
		const carried = Math.min(live.size - added, cachedPhrases ?? live.size - added);
		return { newPhraseCount: live.size - carried, reusedPhraseCount: carried };
	}, [data, isClassifierConfigured, hasPendingSave, willReembedAll, liveKeywords, cachedPhrases]);

	const willReembed = willReembedAll || newPhraseCount > 0;

	useEffect(() => {
		if (!data || isDirty || promptEdited.current) return;
		reset(toFormValues(data, jevDefaults));
		setSubmitError(null);
	}, [data, isDirty, reset, jevDefaults]);

	// Status usually lands after the config, so guidance the form hydrated
	// without defaults is filled in once they arrive. Only still-empty guidance
	// is touched, so an operator's edits are never overwritten.
	useEffect(() => {
		if (!data || !jevDefaults || !isJevGuidanceEmpty(getValues("jev"))) return;
		const seeded = jevGuidanceFormValues(data.jev, jevDefaults);
		setValue("jev.criteria", seeded.criteria, { shouldDirty: false });
	}, [data, jevDefaults, getValues, setValue]);

	// Run after saved-data hydration and read the current form value, not the
	// previous render's value, when config and status arrive together.
	useEffect(() => {
		if (!data || !shouldSeedLLMPrompt(isLLMFallbackEnabled, defaultLLMPrompt, getValues("llm.prompt") ?? "", promptEdited.current)) return;
		setValue("llm.prompt", defaultLLMPrompt, { shouldDirty: false });
	}, [data, isLLMFallbackEnabled, defaultLLMPrompt, livePrompt, getValues, setValue]);

	const handleDiscard = () => {
		promptEdited.current = false;
		if (data) reset(toFormValues(data, jevDefaults));
		setSubmitError(null);
	};

	const handleRestoreDefaults = () => {
		if (!canUpdate) return;
		setSubmitError(null);
		resetConfig()
			.unwrap()
			.then((defaults) => {
				promptEdited.current = false;
				reset(toFormValues(defaults, jevDefaults));
				toast.success("Reset to defaults", { position: "top-right" });
			})
			.catch((err) => {
				setSubmitError(`Couldn’t restore the default phrases. ${getErrorMessage(err)}`);
			});
	};

	const handleRetrySemanticWarmup = () => {
		if (!canUpdate) return;
		retrySemanticWarmup()
			.unwrap()
			.then(() => {
				toast.success("Semantic warmup restarted", { position: "top-right" });
				void refetchStatus();
			})
			.catch((err) => {
				toast.error(`Couldn’t retry semantic warmup. ${getErrorMessage(err)}`, { position: "top-right" });
			});
	};

	const onValid = (values: AnalyzerFormValues) => {
		if (!canUpdate) return;
		setSubmitError(null);
		// The endpoint replaces the whole record and rejects a semantic block
		// without a provider and model, so an unconfigured classifier omits it
		// entirely and saves the phrase lists alone.
		//
		// A half-filled form block falls back to what is stored rather than
		// omitting the block. The embedding controls live in a sheet, so they are
		// unmounted for the whole of a phrase-only save — the exact case where
		// dropping the block would silently clear a working classifier the
		// operator never opened. Nothing here removes it on purpose: the provider
		// select has no clear option, and Restore defaults goes through its own
		// endpoint.
		// The helper preserves saved sheet-only settings and omits session when
		// disabled. Nil is already the wire-level disabled state; avoiding the
		// additive field keeps unrelated edits compatible with older gateways.
		const payload = toAnalyzerPayload(values, data);
		updateConfig(payload)
			.unwrap()
			.then((res) => {
				promptEdited.current = false;
				reset(toFormValues(res, jevDefaults));
				setEmbeddingSheetOpen(false);
				setJevSheetOpen(false);
				toast.success("Configuration saved", { position: "top-right" });
			})
			.catch((err) => {
				setSubmitError(`Couldn’t save the Complexity Router configuration. ${getErrorMessage(err)}`);
			});
	};

	// Saving from inside the sheet still submits the whole configuration, so a
	// phrase error would report behind it. Close the sheet in that case,
	// otherwise the message is hidden under the overlay. An llm or Jev-fallback
	// error opens the sheet instead: those fields live in it, and the operator
	// may never have opened it.
	const submit = handleSubmit(onValid, (formErrors) => {
		// Jev guidance errors sit on the page, so only the sheet's Jev fields open it.
		const jevSheetError = Boolean(formErrors.jev?.previous_message_count || formErrors.jev?.timeout);
		setEmbeddingSheetOpen(Boolean(formErrors.semantic || formErrors.llm || (jevSheetError && liveClassifier === "semantic")));
		setJevSheetOpen(jevSheetError && liveClassifier === "jev");
	});

	if (isLoading && !data) {
		return <FullPageLoader />;
	}

	if (error && !data) {
		return (
			<div className="mx-auto w-full max-w-7xl space-y-4 px-4 pt-6 sm:px-6 sm:pt-8 lg:px-14">
				<p className="text-sm font-medium">Couldn’t load the Complexity Router configuration.</p>
				<p className="text-muted-foreground text-sm">{getErrorMessage(error)}</p>
				<Button data-testid="complexity-router-fetch-retry-button" type="button" variant="outline" size="sm" onClick={() => refetch()}>
					Retry
				</Button>
			</div>
		);
	}

	if (!data) {
		return (
			<div className="mx-auto w-full max-w-7xl space-y-4 px-4 pt-6 sm:px-6 sm:pt-8 lg:px-14">
				<p className="text-muted-foreground font-mono text-sm">No complexity router configuration is available.</p>
				<Button data-testid="complexity-router-fetch-retry-button" type="button" variant="outline" size="sm" onClick={() => refetch()}>
					Retry
				</Button>
			</div>
		);
	}

	const hasErrors = Boolean(errors.keywords || errors.semantic || errors.jev || errors.llm || errors.session || errors.classifier);
	const isSemantic = liveClassifier === "semantic";
	// Saving Jev as the classifier or the fallback while Typesafe cannot serve it
	// would route every classified request into a failing call, so it waits
	// until Typesafe is fixed.
	const blockedOnTypesafe = isJevInUse && !isProviderListLoading && !typesafeReady;
	const canSave = canUpdate && isDirty && !isResetting && !(isSubmitted && hasErrors) && !blockedOnTypesafe;

	// Rendered on the page and again inside the sheet: the re-embed cost is a
	// consequence of saving, and either surface can trigger the save.
	// Only a full re-embed is worth warning about in the sheet: every field that
	// can cause one lives there, and the sheet has its own Save. Adding phrases
	// is a page-level edit and is reported on the page instead, so the two
	// surfaces no longer repeat the same sentence at each other.
	const reembedAllWarning =
		isSemantic && willReembedAll ? (
			<Alert variant="warning" data-testid="complexity-router-reembed-warning">
				<TriangleAlert className="h-4 w-4" />
				<AlertDescription>
					Saving will embed all {totalPhrases} reference phrases through the selected provider. Changing the provider or model invalidates
					every stored vector, so the whole list is embedded again. This uses embedding tokens and may take a short time.
				</AlertDescription>
			</Alert>
		) : null;

	// Phrases already embedded on this gateway are reused, so the bill is the
	// new text alone rather than the whole list.
	const newPhraseWarning =
		isSemantic && !willReembedAll && newPhraseCount > 0 ? (
			<Alert variant="warning" data-testid="complexity-router-new-phrase-warning">
				<TriangleAlert className="h-4 w-4" />
				<AlertDescription>
					Saving will embed {newPhraseCount} new reference phrase
					{newPhraseCount === 1 ? "" : "s"} through the selected provider. The other {reusedPhraseCount} reuse the embeddings this gateway
					already holds.
				</AlertDescription>
			</Alert>
		) : null;

	const reembedWarning = reembedAllWarning ?? newPhraseWarning;

	// Rendered wherever Jev runs: under its settings as the primary classifier,
	// or in the fallback slot the llm prompt otherwise uses.
	const jevGuidance = (variant: "primary" | "fallback") => (
		<JevGuidanceSection
			control={control}
			setValue={setValue}
			errors={errors.jev}
			canUpdate={canUpdate}
			defaults={jevDefaults}
			defaultsLoading={statusLoading || statusFetching}
			variant={variant}
		/>
	);

	const jevSettings = (
		<div className="space-y-4" data-testid="complexity-router-jev-settings">
			{!isProviderListLoading && <TypesafeAlert state={typesafe} />}
			<JevFields control={control} register={register} errors={errors.jev} canUpdate={canUpdate} />
		</div>
	);

	// Selecting a card only selects it; Next moves on, as in the Virtual MCP
	// wizard. The switch is an unsaved edit like any other, so Save commits it
	// and Discard reverts it.
	const selectClassifier = (next: AnalyzerFormValues["classifier"]) => {
		if (next !== liveClassifier) setValue("classifier", next, { shouldDirty: true });
		setPicked(true);
	};
	// Jev cannot run without Typesafe, so Next waits for it rather than opening
	// a configuration that can only fail.
	const canContinue = hasClassifier && (isSemantic || typesafeReady || isProviderListLoading);
	const goToConfiguration = () => {
		setOpenStep("setup");
		// Semantic cannot classify without an embedding model, so go straight to
		// the one place that sets it.
		if (isSemantic && !isClassifierConfigured) setEmbeddingSheetOpen(true);
	};

	const steps = [
		{ key: "classifier", title: "Classifier", enabled: canUpdate },
		{ key: "setup", title: "Configuration", enabled: hasClassifier },
	];
	const stepIndex = step === "classifier" ? 0 : 1;

	return (
		<>
			{/* no-padding-parent drops the shell's wide side padding so the step
			    rail sits near the card's left edge, and the width it frees goes
			    to the step content on the right. */}
			<form className="no-padding-parent flex h-full min-h-0 w-full flex-col p-4" onSubmit={submit} noValidate>
				{/* PageTitle renders nothing inline; its badge and description are
				    portalled into the topbar. */}
				<PageTitle title="Complexity Router" beta>
					{liveClassifier === "jev"
						? "Typesafe Jev classifies each new human request, filling the"
						: "Each request takes the tier of its nearest semantic reference phrase, filling the"}{" "}
					<code className="bg-muted rounded-sm px-1 py-0.5 font-mono text-xs">complexity_tier</code> field that routing rules target.
					{isLLMFallbackEnabled ? " Requests matching no phrase confidently fall back to the LLM classifier." : ""}
					{isSemantic && liveSemantic?.fallback === "jev" ? " Requests matching no phrase confidently fall back to Jev." : ""}
					{liveSession.enabled ? " Session-aware routing keeps the highest tier reached during the active session." : ""}
				</PageTitle>

				{/* Same shape as the Edge inventory page: a slim step nav on the
				    left, the open step on the right. It spans the full content width
				    rather than a centred max-width, which on wide screens left an
				    empty band beside the nav and squeezed the phrase lists. */}
				<div className="flex min-h-0 w-full grow flex-col gap-4 md:flex-row md:gap-8">
					<StepRail steps={steps} currentIndex={stepIndex} onSelect={(index) => setOpenStep(index === 0 ? "classifier" : "setup")} />

					<div className="flex min-h-0 min-w-0 flex-1 flex-col">
						{/* The footer is a sibling of the scroll area rather than a sticky
						    child of it. Radix wraps scrolled content in a display:table
						    element, and position:sticky is unreliable inside table boxes. */}
						<ScrollArea className="min-h-0 flex-1">
							<div className="space-y-6 pb-8">
								{/* ── Step header ── */}
								{/* Status and embedding setup ride on the right: both are
								    checked occasionally, while the phrase lists below are the
								    real work surface. The row never wraps on wide screens: the
								    status badge changes width as the classifier warms, and
								    wrapping made the buttons jump below the title and back. */}
								<div className="flex w-full flex-col gap-3 md:flex-row md:items-start md:justify-between">
									<div className="min-w-0 md:flex-1">
										<h2 className="text-lg font-semibold">{steps[stepIndex].title}</h2>
										<p className="text-muted-foreground text-sm">
											{step === "classifier"
												? "Choose how each request is assigned a complexity tier. Switching later keeps the other classifier's settings, including your reference phrases."
												: isSemantic
													? "Each request takes the tier of its nearest reference phrase."
													: "A decision model scores each new request into a tier."}
										</p>
									</div>
									<div className="flex shrink-0 flex-wrap items-center gap-2 md:flex-nowrap">
										{step === "setup" && isSemantic && (
											<>
												<ClassifierStatusBadge
													status={semanticStatus}
													isLoading={statusLoading}
													isNotConfigured={!isClassifierConfigured}
													isNotSaved={isClassifierConfigured && !data.semantic}
													hasUnsavedChanges={willReembed}
													hasEmbeddingProviders={embeddingProviders.length > 0}
													statusUnavailable={statusIsError && !semanticStatus}
													statusRefreshFailed={statusIsError && Boolean(semanticStatus)}
													isRetryingStatus={statusFetching}
													canRetryWarmup={canUpdate}
													isRetryingWarmup={isRetryingWarmup}
													onConfigure={() => setEmbeddingSheetOpen(true)}
													onRetryStatus={() => void refetchStatus()}
													onRetryWarmup={handleRetrySemanticWarmup}
												/>
												<Button
													type="button"
													variant="outline"
													size="sm"
													onClick={() => setEmbeddingSheetOpen(true)}
													data-testid="complexity-router-embedding-config-button"
												>
													<Settings2 className="size-3.5" />
													{isClassifierConfigured ? "Edit embedding configuration" : "Configure embedding"}
													{hasUnsavedEmbeddingConfigChanges && (
														<span
															className="size-1.5 rounded-full bg-amber-500"
															role="status"
															aria-label="Unsaved embedding configuration changes"
														/>
													)}
												</Button>
											</>
										)}
										{step === "setup" && !isSemantic && (
											<Button
												type="button"
												variant="outline"
												size="sm"
												onClick={() => setJevSheetOpen(true)}
												data-testid="complexity-router-jev-settings-button"
											>
												<Settings2 className="size-3.5" />
												Jev settings
												{hasUnsavedJevSettingsChanges && (
													<span className="size-1.5 rounded-full bg-amber-500" role="status" aria-label="Unsaved Jev settings changes" />
												)}
											</Button>
										)}
										<Button asChild variant="outline" size="sm" data-testid="complexity-router-docs-link">
											<a href={"https://docs.getbifrost.ai/features/complexity-router"} target="_blank" rel="noopener noreferrer">
												<ExternalLink className="size-3.5" />
												Docs
											</a>
										</Button>
									</div>
								</div>

								{/* ── Step 1: classifier ── */}
								{step === "classifier" && (
									<>
										<ClassifierChoice value={hasClassifier ? liveClassifier : undefined} onChange={selectClassifier} />
										{/* One warning, and only once Jev is the pick: it is the only
										    time a missing Typesafe provider matters here. */}
										{hasClassifier && !isSemantic && !isProviderListLoading && <TypesafeAlert state={typesafe} />}
									</>
								)}

								{/* ── Step 2: semantic ── */}
								{step === "setup" && isSemantic && (
									<div className="space-y-3">
										<SectionHeading
											title="Phrase to Tier Mapping"
											aside={
												<span
													className="text-muted-foreground font-mono text-[11px] tabular-nums"
													data-testid="complexity-router-phrase-total"
												>
													{isClassifierConfigured ? `${totalPhrases} / ${MAX_SEMANTIC_PHRASES} phrases` : `${totalPhrases} phrases`}
												</span>
											}
										/>
										<PhraseTierGrid control={control} errors={errors.keywords} />
									</div>
								)}

								{/* ── Step 2: Jev ── */}
								{/* The page holds the tier guidance; set-once settings live in the
								    Jev settings sheet, as embedding settings do for semantic. The
								    Typesafe problem stays on the page because nothing runs until
								    it is fixed. */}
								{step === "setup" && !isSemantic && !isProviderListLoading && <TypesafeAlert state={typesafe} />}
								{step === "setup" && !isSemantic && jevGuidance("primary")}

								{step === "setup" && <SessionRoutingCard control={control} errors={errors.session} canUpdate={canUpdate} />}

								{/* ── Fallback Classification Prompt ── */}
								{/* Below the phrase lists rather than in the sheet: prompt text
								    needs width and iteration, and the sheet holds set-once
								    plumbing. Visible only while the fallback is on, because that
								    is the only time it runs. */}
								{step === "setup" && isLLMFallbackEnabled && (
									<LLMPromptSection
										control={control}
										error={errors.llm?.prompt}
										canUpdate={canUpdate}
										defaultPrompt={defaultLLMPrompt}
										livePrompt={livePrompt}
										onEdit={() => {
											promptEdited.current = true;
										}}
										onReset={() => {
											promptEdited.current = true;
											setValue("llm.prompt", defaultLLMPrompt, { shouldDirty: true });
										}}
									/>
								)}

								{/* Same slot as the llm prompt: only one fallback can be on. */}
								{step === "setup" && isJevFallbackEnabled && jevGuidance("fallback")}

								{step === "setup" && reembedWarning}

								{submitError && (
									<div
										role="alert"
										className="border-destructive/40 bg-destructive/10 text-destructive rounded-sm border px-3 py-2 font-mono text-sm"
									>
										{submitError}
									</div>
								)}
							</div>
						</ScrollArea>

						{/* ── Action footer ── */}
						<div className="flex flex-wrap items-center justify-end gap-2.5 border-t pt-4">
							{step === "classifier" ? (
								<Button
									type="button"
									size="sm"
									onClick={goToConfiguration}
									disabled={!canContinue}
									data-testid="complexity-router-next-button"
								>
									Next
									<ArrowRight className="h-3.5 w-3.5" />
								</Button>
							) : (
								<>
									{canUpdate && (
										<Button
											type="button"
											variant="ghost"
											size="sm"
											onClick={() => setOpenStep("classifier")}
											data-testid="complexity-router-back-button"
										>
											<ArrowLeft className="h-3.5 w-3.5" />
											Back
										</Button>
									)}
									{/* The Jev warning is already on screen when Jev is primary; as
									    the fallback it sits inside the sheet, so the page says why
									    Save is off. Otherwise an unsaved classifier switch is
									    called out, since the page has no leave guard. */}
									<p
										className="mr-auto text-xs text-amber-700 dark:text-amber-400"
										role="status"
										data-testid="complexity-router-footer-note"
									>
										{blockedOnTypesafe && isSemantic
											? "Jev is the fallback, and it needs a working Typesafe provider before this can be saved."
											: !blockedOnTypesafe && liveClassifier !== toFormValues(data).classifier
												? `Classifier changed to ${isSemantic ? "Semantic" : "Jev"}. Not saved yet.`
												: ""}
									</p>
									{/* Each classifier restores only its own defaults: the semantic
									    phrase lists, or Jev's tier guidance. */}
									{!isSemantic && (
										<Button
											data-testid="complexity-router-jev-restore-defaults-button"
											type="button"
											variant="ghost"
											size="sm"
											onClick={restoreJevDefaults}
											disabled={!canUpdate || isSaving || isJevGuidanceAtDefaults}
										>
											<RotateCcw className="h-3.5 w-3.5" />
											Restore defaults
										</Button>
									)}
									{isSemantic && (
										<Button
											data-testid="complexity-router-restore-defaults-button"
											type="button"
											variant="ghost"
											size="sm"
											onClick={() => setRestoreDialogOpen(true)}
											disabled={!canUpdate || isSaving || isResetting}
										>
											{isResetting ? <LoaderCircle className="h-3.5 w-3.5 animate-spin" /> : <RotateCcw className="h-3.5 w-3.5" />}
											Restore defaults
										</Button>
									)}
									<Button
										data-testid="complexity-router-discard-changes-button"
										type="button"
										variant="outline"
										size="sm"
										onClick={handleDiscard}
										disabled={!isDirty || isSaving || isResetting || isFetching}
									>
										Discard changes
									</Button>
									<Button data-testid="complexity-router-save-changes-button" type="submit" size="sm" disabled={!canSave || isSaving}>
										{isSaving ? <LoaderCircle className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />}
										{isSaving ? "Saving…" : "Save changes"}
									</Button>
								</>
							)}
						</div>
					</div>
				</div>
			</form>

			<EmbeddingConfigSheet
				open={embeddingSheetOpen}
				onOpenChange={setEmbeddingSheetOpen}
				control={control}
				register={register}
				setValue={setValue}
				errors={errors.semantic}
				semantic={liveSemantic}
				llmErrors={errors.llm}
				llm={liveLLM}
				canUpdate={canUpdate}
				providers={embeddingProviders}
				providerKeyIds={enabledKeyIdsForProvider}
				llmProviders={chatProviders}
				llmProviderKeyIds={enabledKeyIdsForLLMProvider}
				providersLoading={isProviderListLoading}
				isVectorStoreConnected={isVectorStoreConnected}
				warning={reembedAllWarning}
				canSave={canSave}
				isSaving={isSaving}
				onSave={() => void submit()}
				submitError={submitError}
				jevSettings={jevSettings}
			/>

			<JevSettingsSheet
				open={jevSheetOpen && !isSemantic}
				onOpenChange={setJevSheetOpen}
				jevSettings={jevSettings}
				canSave={canSave}
				isSaving={isSaving}
				onSave={() => void submit()}
				submitError={submitError}
			/>

			<AlertDialog open={restoreDialogOpen} onOpenChange={setRestoreDialogOpen}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Restore defaults</AlertDialogTitle>
						<AlertDialogDescription>
							This will replace the phrase to tier mapping with the default reference phrases. Your current phrases will be lost and this
							action cannot be undone. Your embedding configuration is kept, so classification keeps running and the restored phrases are
							embedded through the configured provider straight away.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel
							data-testid="complexity-router-restore-cancel-button"
							onClick={() => setRestoreDialogOpen(false)}
							disabled={isResetting}
						>
							Cancel
						</AlertDialogCancel>
						<AlertDialogAction
							data-testid="complexity-router-restore-confirm-button"
							onClick={() => {
								setRestoreDialogOpen(false);
								handleRestoreDefaults();
							}}
							disabled={!canUpdate || isResetting}
						>
							Restore defaults
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</>
	);
}