import { EmbeddingSupportedProviders } from "@/lib/constants/logs";
import { getErrorMessage, useGetCoreConfigQuery, useGetProvidersQuery } from "@/lib/store";
import { useGetAllKeysQuery } from "@/lib/store/apis/providersApi";
import {
	useGetComplexitySuggestionsQuery,
	useGetModelRouterStatusQuery,
	useResetModelRouterMutation,
	useRetryModelRouterWarmupMutation,
	useUpdateComplexitySuggestionStatusMutation,
} from "@/lib/store/apis/governanceApi";
import {
	AnalyzerConfig,
	DecisionGuidanceDefaults,
	DecisionTier,
	MAX_SEMANTIC_PHRASES,
	ModelRouter,
	Suggestion,
	SuggestionTierDefaults,
} from "@/lib/types/complexityRouter";
import { ModelProvider } from "@/lib/types/config";
import { DBKey } from "@/lib/types/governance";
import { zodResolver } from "@hookform/resolvers/zod";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Resolver, useForm, useWatch } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import {
	AnalyzerFormValues,
	analyzerConfigSchema,
	withGeneratedTierCriteriaCleared,
	countCanonicalSemanticPhrases,
	decisionCriteriaFromDefaults,
	decisionGuidanceFormValues,
	DEFAULT_FORM_VALUES,
	getDecisionProviderState,
	isDecisionGuidanceDefault,
	isDecisionGuidanceEmpty,
	KnownTierDefaults,
	lastKnownTierDefaults,
	shouldSeedLLMPrompt,
	tierCriteriaReseedValues,
	toAnalyzerPayload,
	toFormValues,
} from "@/app/workspace/complexity-router/formSchema";
import {
	addPhrases,
	newPhrases,
	removePhrases,
	replacePhrase,
	StagedSuggestion,
	stagedPhraseCount,
	stillStaged,
} from "@/app/workspace/complexity-router/views/suggestions.utils";

// Embedding-capable providers gate the semantic classifier, matching the local
// cache screen's rule: built-ins are listed in EmbeddingSupportedProviders,
// custom providers declare support through allowed_requests.embedding. A custom
// provider with no allowed_requests block at all is unrestricted, which is how
// the Go side reads a nil AllowedRequests.
const supportsEmbedding = (provider: ModelProvider): boolean => {
	if (provider.custom_provider_config) {
		const allowed = provider.custom_provider_config.allowed_requests;
		return !allowed || allowed.embedding === true;
	}
	return (EmbeddingSupportedProviders as readonly string[]).includes(provider.name);
};

// Supporting a request type is not enough to be selectable: every call the
// classifier makes needs a key that is actually serving. A provider whose keys
// are all disabled looks configured on the providers screen but fails at
// request time. A key omits `enabled` when unset, which the Go side reads as enabled.
const hasEnabledKey = (provider: ModelProvider, keys: DBKey[]): boolean =>
	keys.some((key) => key.provider === provider.name && key.enabled !== false);

// The llm classifier needs chat completions. Built-in providers all serve chat;
// custom providers declare support through allowed_requests.chat_completion,
// and no allowed_requests block at all means unrestricted.
const supportsChat = (provider: ModelProvider): boolean => {
	if (provider.custom_provider_config) {
		const allowed = provider.custom_provider_config.allowed_requests;
		return !allowed || allowed.chat_completion === true;
	}
	return true;
};

const enabledKeyIds = (keys: DBKey[] | undefined, provider: string | undefined) =>
	(keys || []).filter((key) => key.provider === provider && key.enabled !== false).map((key) => key.key_id);

type ConfigSheet = "embedding" | "decision" | "llm" | "suggestions" | null;

interface UseRouterConfigFormOptions {
	// The saved router; while creating, the built-in config a new router starts from.
	saved: ModelRouter | AnalyzerConfig | undefined;
	// The router being edited. Absent while creating: nothing is served yet, so
	// there is no status to read and no suggestions to stage.
	routerId?: string;
	canUpdate: boolean;
	// The shipped guidance a new router starts from. A saved router reads it
	// from its status instead.
	createDefaults?: { decision: DecisionGuidanceDefaults; llmPrompt: string };
	// Writes the payload and returns what was stored.
	save: (payload: AnalyzerConfig) => Promise<AnalyzerConfig>;
}

// useRouterConfigForm holds a router's classifier configuration form and
// everything derived from it, so the creation wizard, the Configuration tab
// and the Suggestions tab edit the same draft: phrases staged from a
// suggestion stay in it while the operator switches tabs, and save with it.
export function useRouterConfigForm({ saved, routerId, canUpdate, createDefaults, save }: UseRouterConfigFormOptions) {
	const { t } = useTranslation("models");
	const { t: tcpx } = useTranslation("models", { keyPrefix: "routing.complexityUi" });
	const [resetConfig, { isLoading: isResetting }] = useResetModelRouterMutation();
	const [retrySemanticWarmup, { isLoading: isRetryingWarmup }] = useRetryModelRouterWarmupMutation();
	const [updateSuggestionStatus] = useUpdateComplexitySuggestionStatusMutation();
	const [isSaving, setIsSaving] = useState(false);
	const [submitError, setSubmitError] = useState<string | null>(null);
	// Fields the last submit found invalid; 0 once a submit gets through.
	const [invalidFields, setInvalidFields] = useState(0);
	// Counts the saves that succeeded, so a caller can tell its own save went
	// through: submit resolves the same way whether it did or not.
	const [saveCount, setSaveCount] = useState(0);
	const [openSheet, setOpenSheet] = useState<ConfigSheet>(null);
	const [restoreDialogOpen, setRestoreDialogOpen] = useState(false);
	// Suggestions whose phrases are in the draft lists, waiting for Save.
	const [staged, setStaged] = useState<StagedSuggestion[]>([]);
	// An intentionally empty draft can equal the saved default and be non-dirty.
	// Track interaction separately so status refreshes never refill it.
	const promptEdited = useRef(false);
	const isCreating = !routerId;

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
	// Both queries feed the provider lists, so empty states wait for both.
	const isProviderListLoading = providersLoading || keysLoading;
	const { data: coreConfig } = useGetCoreConfigQuery({ fromDB: true });
	const isVectorStoreConnected = coreConfig?.is_cache_connected ?? false;

	// The generated tier criteria, for the resolver: it is built before they
	// are known, and reads them when it runs.
	const generatedTierCriteria = useRef<SuggestionTierDefaults["criteria"] | undefined>(undefined);
	const resolver = useCallback<Resolver<AnalyzerFormValues>>(async (values, context, options) => {
		const checked = withGeneratedTierCriteriaCleared(values, generatedTierCriteria.current);
		const result = await zodResolver(analyzerConfigSchema)(checked, context, options);
		if (checked === values || Object.keys(result.errors).length > 0) return result;
		// Validated with the generated tiers cleared; saved as written.
		const parsed = result.values as AnalyzerFormValues;
		return { values: { ...parsed, suggestions: { ...parsed.suggestions, tier_criteria: values.suggestions.tier_criteria } }, errors: {} };
	}, []);
	const form = useForm<AnalyzerFormValues>({
		resolver,
		defaultValues: DEFAULT_FORM_VALUES,
		mode: "onSubmit",
		reValidateMode: "onChange",
	});
	const {
		reset,
		control,
		watch,
		setValue,
		getValues,
		handleSubmit,
		formState: { errors, dirtyFields, isDirty, isSubmitted },
	} = form;

	const liveClassifier = watch("classifier");
	const liveSemantic = watch("semantic");
	const liveLLM = watch("llm");
	const liveDecisionProvider = watch("decision.provider");
	const liveSession = watch("session");
	const liveKeywords = watch("keywords");
	const liveSuggestions = watch("suggestions");
	const liveDecisionModel = watch("decision.model");
	const liveDecisionCriteria = useWatch({ control, name: "decision.criteria" });

	// Each model list is narrowed to what its provider's enabled keys can serve:
	// /api/models only applies per-key allow-lists when it is handed key ids.
	// Memoized because ModelSelector refetches when this array's identity changes.
	const keyIds = {
		semantic: useMemo(() => enabledKeyIds(allKeys, liveSemantic?.provider), [allKeys, liveSemantic?.provider]),
		llm: useMemo(() => enabledKeyIds(allKeys, liveLLM?.provider), [allKeys, liveLLM?.provider]),
		decision: useMemo(() => enabledKeyIds(allKeys, liveDecisionProvider), [allKeys, liveDecisionProvider]),
	};
	// The decision model authenticates through its provider, so that provider's
	// state is what the decision model can be expected to do.
	const decisionProviderState = useMemo(
		() => getDecisionProviderState(providersData, allKeys, liveDecisionProvider),
		[providersData, allKeys, liveDecisionProvider],
	);

	const isSemantic = liveClassifier === "semantic";
	const isClassifierConfigured = Boolean(liveSemantic?.provider && liveSemantic?.embedding_model);
	const isLLMFallbackEnabled = isSemantic && liveSemantic?.fallback === "llm";
	const isDecisionFallbackEnabled = isSemantic && liveSemantic?.fallback === "decision";
	const isDecisionInUse = liveClassifier === "decision" || isDecisionFallbackEnabled;
	const isLLMInUse = liveClassifier === "llm" || isLLMFallbackEnabled;
	// react-hook-form keeps reverted fields in dirtyFields with a false value, so
	// the flags are what matter, not the key count.
	const hasUnsavedDecisionSettingsChanges = Boolean(
		dirtyFields.decision?.provider ||
		dirtyFields.decision?.model ||
		dirtyFields.decision?.previous_message_count ||
		dirtyFields.decision?.timeout ||
		dirtyFields.decision?.min_confidence,
	);
	const hasUnsavedLLMSettingsChanges = Object.entries(dirtyFields.llm ?? {}).some(([field, dirty]) => field !== "prompt" && Boolean(dirty));
	const hasUnsavedEmbeddingConfigChanges = Object.values(dirtyFields.semantic ?? {}).some(Boolean) || hasUnsavedLLMSettingsChanges;

	// Only the unsettled states are polled. Failed is polled too, slowly: the
	// gateway re-arms the classifier by itself once the provider it embeds
	// through is fixed, usually on another page. The status endpoint also serves
	// the shipped prompts that seed the llm and decision-model editors, so it is
	// read as soon as either is in use, before any save.
	const [statusPollInterval, setStatusPollInterval] = useState(0);
	const savedRouter = saved as ModelRouter | undefined;
	const {
		data: semanticStatus,
		isLoading: statusLoading,
		isFetching: statusFetching,
		isError: statusIsError,
		refetch: refetchStatus,
	} = useGetModelRouterStatusQuery(routerId ?? "", {
		// Suggestions read the tier step's generated text from it too.
		skip:
			isCreating ||
			(!savedRouter?.semantic &&
				!savedRouter?.llm &&
				!isLLMInUse &&
				!isDecisionInUse &&
				!savedRouter?.suggestions &&
				!liveSuggestions?.enabled),
		pollingInterval: statusPollInterval,
	});
	useEffect(() => {
		setStatusPollInterval(semanticStatus?.state === "warming" ? 2000 : semanticStatus?.state === "failed" ? 10000 : 0);
	}, [semanticStatus?.state]);

	// The suggestions tab's pending count: the counts alone, so one row is read.
	const { data: openSuggestions } = useGetComplexitySuggestionsQuery(
		{ statuses: ["open"], routerId: routerId ?? "", limit: 1 },
		{ skip: isCreating || liveClassifier !== "semantic" },
	);

	const totalPhrases = useMemo(
		() =>
			countCanonicalSemanticPhrases({
				simple_keywords: liveKeywords?.simple_keywords ?? [],
				medium_keywords: liveKeywords?.medium_keywords ?? [],
				complex_keywords: liveKeywords?.complex_keywords ?? [],
			}).total,
		[liveKeywords],
	);

	// A staged phrase removed from its list on the configuration tab means the
	// suggestion is no longer being accepted, so staging follows the lists.
	const activeStaged = useMemo(() => (liveKeywords ? stillStaged(staged, liveKeywords) : []), [staged, liveKeywords]);
	const pendingSuggestions = Math.max(0, (openSuggestions?.counts.open ?? 0) - activeStaged.length);

	// stageSuggestion adds a suggestion's phrases to its tier's draft list and
	// reports how many were new; phrases the bank already holds are skipped.
	// Staging is closed while a save is in flight: the save sends the lists as
	// they were when it started, and the reset after it would drop anything
	// staged since.
	const stageSuggestion = (suggestion: Suggestion, tier: DecisionTier, phrases: string[]): number => {
		if (isSaving) return 0;
		// Read from the form, not the render: a drop of several rows stages them
		// one after another before the page renders again.
		const keywords = getValues("keywords");
		const room = Math.max(0, MAX_SEMANTIC_PHRASES - countCanonicalSemanticPhrases(keywords).total);
		const adding = newPhrases(keywords, phrases).slice(0, room);
		if (adding.length === 0) return 0;
		setValue("keywords", addPhrases(keywords, tier, adding), { shouldDirty: true, shouldValidate: isSubmitted });
		setStaged((current) => [...current.filter((entry) => entry.id !== suggestion.id), { id: suggestion.id, tier, phrases: adding }]);
		return adding.length;
	};
	const unstageSuggestion = (id: string) => {
		if (isSaving) return;
		const entry = activeStaged.find((candidate) => candidate.id === id);
		if (entry) setValue("keywords", removePhrases(getValues("keywords"), entry.tier, entry.phrases), { shouldDirty: true });
		setStaged((current) => current.filter((candidate) => candidate.id !== id));
	};
	// editStagedPhrase reports whether the edit was taken, so a refused one
	// can put its input back to the phrase that will be saved.
	const editStagedPhrase = (id: string, from: string, to: string): boolean => {
		const entry = activeStaged.find((candidate) => candidate.id === id);
		if (!entry || isSaving) return false;
		const keywords = getValues("keywords");
		// An edit that collides with a phrase already in the bank is refused, as
		// the gateway would refuse the duplicate on save.
		if (newPhrases(removePhrases(keywords, entry.tier, [from]), [to]).length === 0) {
			toast.info(tcpx("suggestions.alreadyInBank"), { position: "top-right" });
			return false;
		}
		setValue("keywords", replacePhrase(keywords, entry.tier, from, to.trim()), { shouldDirty: true });
		setStaged((current) =>
			current.map((candidate) =>
				candidate.id === id
					? { ...candidate, phrases: candidate.phrases.map((phrase) => (phrase === from ? to.trim() : phrase)) }
					: candidate,
			),
		);
		return true;
	};

	// discardStaged takes every staged group's phrases back out of the draft
	// lists, leaving the rest of the draft as it is.
	const discardStaged = () => {
		if (isSaving) return;
		let keywords = getValues("keywords");
		for (const entry of activeStaged) keywords = removePhrases(keywords, entry.tier, entry.phrases);
		setValue("keywords", keywords, { shouldDirty: true, shouldValidate: isSubmitted });
		setStaged([]);
		setSubmitError(null);
		setInvalidFields(0);
	};

	// Shipped guidance initializes untouched drafts; an empty edited prompt is
	// valid and means "use default guidance" when saved.
	const defaultLLMPrompt = semanticStatus?.llm_default_prompt ?? createDefaults?.llmPrompt ?? "";
	const decisionDefaults = semanticStatus?.decision_defaults ?? createDefaults?.decision;
	// What the suggestions tier step sends while nothing is edited, rendered
	// by the gateway from the saved router. A router being created has none.
	const tierDefaultPrompt = semanticStatus?.suggestion_default_tier_prompt;
	const tierDefaultCriteria = semanticStatus?.suggestion_default_tier_criteria;
	const liveTierDefaults = useMemo<SuggestionTierDefaults | undefined>(
		() =>
			tierDefaultPrompt !== undefined && tierDefaultCriteria ? { prompt: tierDefaultPrompt, criteria: tierDefaultCriteria } : undefined,
		[tierDefaultPrompt, tierDefaultCriteria],
	);
	// A status refresh can leave the generated text out (the gateway could not
	// read the router to render it); edits are still compared with the text
	// they were made against, the last known for this router.
	const knownTierDefaults = useRef<KnownTierDefaults | undefined>(undefined);
	knownTierDefaults.current = lastKnownTierDefaults(knownTierDefaults.current, routerId, liveTierDefaults);
	const tierDefaults = knownTierDefaults.current?.defaults;
	generatedTierCriteria.current = tierDefaults?.criteria;
	const livePrompt = liveLLM?.prompt ?? "";
	// The decision model's Restore defaults only refills the form: it saves like
	// any other edit and Discard undoes it.
	const isDecisionGuidanceAtDefaults = !decisionDefaults || isDecisionGuidanceDefault(liveDecisionCriteria, decisionDefaults);
	const restoreDecisionDefaults = () => {
		if (!decisionDefaults) return;
		setValue("decision.criteria", decisionCriteriaFromDefaults(decisionDefaults), { shouldDirty: true, shouldValidate: true });
	};

	// Every embedding-cost warning is about what the pending save will do, so
	// it is gated on there being a pending save at all.
	const hasPendingSave = isDirty || isCreating;
	// Saving re-runs warmup, but what it costs depends on what changed, because
	// the gateway caches a vector per phrase. Provider and model are the cache's
	// identity, so changing either re-embeds every phrase. An empty cache (the
	// gateway restarted) means the same, so the count comes from the status
	// payload rather than the persisted config.
	const cachedPhrases = semanticStatus?.cached_phrases;
	const willReembedAll = useMemo(() => {
		if (!saved || !isClassifierConfigured || !hasPendingSave) return false;
		if (isCreating) return true;
		const savedSemantic = saved.semantic;
		if (!savedSemantic) return true;
		if (cachedPhrases !== undefined && cachedPhrases === 0) return true;
		return savedSemantic.provider !== liveSemantic?.provider || savedSemantic.embedding_model !== liveSemantic?.embedding_model;
	}, [saved, isClassifierConfigured, hasPendingSave, isCreating, liveSemantic, cachedPhrases]);

	// Editing the lists only pays for phrase text the gateway has not embedded
	// before. Both sides are compared in the gateway's own phrase space —
	// lowercased, trimmed and deduped — and reuse is capped at what the gateway
	// reports holding, since a warmup that failed partway caches fewer vectors.
	const { newPhraseCount, reusedPhraseCount } = useMemo(() => {
		if (!saved || !isClassifierConfigured || !hasPendingSave || willReembedAll) {
			return { newPhraseCount: 0, reusedPhraseCount: 0 };
		}
		const normalize = (phrase: string) => phrase.trim().toLowerCase();
		const savedPhrases = new Set(
			[
				...(saved.keywords?.simple_keywords ?? []),
				...(saved.keywords?.medium_keywords ?? []),
				...(saved.keywords?.complex_keywords ?? []),
			].map(normalize),
		);
		const live = new Set(
			[...(liveKeywords?.simple_keywords ?? []), ...(liveKeywords?.medium_keywords ?? []), ...(liveKeywords?.complex_keywords ?? [])]
				.map(normalize)
				.filter(Boolean),
		);
		let added = 0;
		live.forEach((phrase) => {
			if (!savedPhrases.has(phrase)) added += 1;
		});
		const carried = Math.min(live.size - added, cachedPhrases ?? live.size - added);
		return { newPhraseCount: live.size - carried, reusedPhraseCount: carried };
	}, [saved, isClassifierConfigured, hasPendingSave, willReembedAll, liveKeywords, cachedPhrases]);

	useEffect(() => {
		if (!saved || isDirty || promptEdited.current) return;
		reset(toFormValues(saved, decisionDefaults, tierDefaults));
		setSubmitError(null);
	}, [saved, isDirty, reset, decisionDefaults, tierDefaults]);

	// Status usually lands after the config, so guidance the form hydrated
	// without defaults is filled in once they arrive. Only still-empty guidance
	// is touched, so an operator's edits are never overwritten.
	useEffect(() => {
		if (!saved || !decisionDefaults || !isDecisionGuidanceEmpty(getValues("decision"))) return;
		const seeded = decisionGuidanceFormValues(saved.decision, decisionDefaults);
		setValue("decision.criteria", seeded.criteria, { shouldDirty: false });
	}, [saved, decisionDefaults, getValues, setValue]);

	// The tier step's criteria editors are seeded the same way once the
	// generated criteria arrive: every tier still empty gets its saved edit or
	// the generated criteria. When newer generated criteria arrive, a tier
	// still showing the ones it was seeded from moves with them, so it keeps
	// matching what Save compares against; an edited tier is never overwritten
	// (tierCriteriaReseedValues).
	const seededTierCriteria = useRef<SuggestionTierDefaults["criteria"] | undefined>(undefined);
	useEffect(() => {
		if (!saved || !tierDefaults) return;
		const current = getValues("suggestions.tier_criteria");
		const reseed = tierCriteriaReseedValues(current, seededTierCriteria.current, saved.suggestions?.tier, tierDefaults.criteria);
		for (const [tier, criteria] of reseed) {
			setValue(`suggestions.tier_criteria.${tier}`, criteria, { shouldDirty: false });
		}
		seededTierCriteria.current = tierDefaults.criteria;
	}, [saved, tierDefaults, getValues, setValue]);

	// Run after saved-data hydration and read the current form value, not the
	// previous render's value, when config and status arrive together.
	useEffect(() => {
		if (!saved || !shouldSeedLLMPrompt(isLLMInUse, defaultLLMPrompt, getValues("llm.prompt") ?? "", promptEdited.current)) return;
		setValue("llm.prompt", defaultLLMPrompt, { shouldDirty: false });
	}, [saved, isLLMInUse, defaultLLMPrompt, livePrompt, getValues, setValue]);

	const discard = () => {
		promptEdited.current = false;
		setStaged([]);
		if (saved) reset(toFormValues(saved, decisionDefaults, tierDefaults));
		setSubmitError(null);
		setInvalidFields(0);
	};

	// Restoring phrases persists through its own endpoint for a saved router;
	// a router being created has nothing saved, so its built-in phrases are
	// simply put back in the draft.
	const restorePhraseDefaults = () => {
		if (!canUpdate) return;
		setSubmitError(null);
		if (isCreating) {
			if (saved) setValue("keywords", saved.keywords, { shouldDirty: true, shouldValidate: isSubmitted });
			return;
		}
		resetConfig(routerId)
			.unwrap()
			.then((defaults) => {
				promptEdited.current = false;
				setStaged([]);
				reset(toFormValues(defaults, decisionDefaults, tierDefaults));
				toast.success(t("routing.resetToDefaults"), { position: "top-right" });
			})
			.catch((err) => setSubmitError(tcpx("restoreFailed", { error: getErrorMessage(err) })));
	};

	const retryWarmup = () => {
		if (!canUpdate || !routerId) return;
		retrySemanticWarmup(routerId)
			.unwrap()
			.then(() => {
				toast.success(tcpx("warmupRestarted"), { position: "top-right" });
				void refetchStatus();
			})
			.catch((err) => toast.error(tcpx("warmupRetryFailed", { error: getErrorMessage(err) }), { position: "top-right" }));
	};

	const onValid = async (values: AnalyzerFormValues) => {
		if (!canUpdate) return;
		setSubmitError(null);
		setInvalidFields(0);
		// The endpoint replaces the whole record. A half-filled block falls back
		// to what is stored rather than being omitted, so a save from the page
		// never clears a working classifier whose sheet was never opened.
		const payload = toAnalyzerPayload(values, isCreating ? undefined : saved, tierDefaults);
		// Taken before the save: staging is cleared with the form afterwards.
		const accepting = activeStaged;
		setIsSaving(true);
		try {
			const stored = await save(payload);
			promptEdited.current = false;
			reset(toFormValues(stored, decisionDefaults, tierDefaults));
			setOpenSheet(null);
			setStaged([]);
			setSaveCount((count) => count + 1);
			// The phrases are saved; recording the suggestions as accepted is what
			// stops later runs proposing them again. A failure here is reported
			// but harmless: the next run finds them covered.
			if (routerId && accepting.length > 0) {
				void Promise.allSettled(
					accepting.map((entry) => updateSuggestionStatus({ id: entry.id, status: "accepted", routerId }).unwrap()),
				).then((results) => {
					const failed = results.filter((result) => result.status === "rejected").length;
					if (failed > 0) toast.warning(tcpx("suggestions.acceptFailed", { count: failed }), { position: "top-right" });
				});
			}
			return stored;
		} catch (err) {
			setSubmitError(tcpx("saveFailed", { error: getErrorMessage(err) }));
			return undefined;
		} finally {
			setIsSaving(false);
		}
	};

	// A field error inside a sheet opens that sheet, since the operator may
	// never have opened it; errors on the page close it so they are not hidden
	// under the overlay.
	const submit = handleSubmit(onValid, (formErrors) => {
		setInvalidFields(countFieldErrors(formErrors));
		const decisionSheetError = Boolean(
			formErrors.decision?.provider ||
			formErrors.decision?.model ||
			formErrors.decision?.previous_message_count ||
			formErrors.decision?.timeout ||
			formErrors.decision?.min_confidence,
		);
		const llmSheetError = Boolean(formErrors.llm && Object.keys(formErrors.llm).some((field) => field !== "prompt"));
		if (formErrors.semantic || (isSemantic && (llmSheetError || decisionSheetError))) setOpenSheet("embedding");
		else if (liveClassifier === "decision" && decisionSheetError) setOpenSheet("decision");
		else if (liveClassifier === "llm" && llmSheetError) setOpenSheet("llm");
		else if (formErrors.suggestions) setOpenSheet("suggestions");
		else setOpenSheet(null);
	});

	const hasErrors = Boolean(
		errors.keywords || errors.semantic || errors.decision || errors.llm || errors.session || errors.classifier || errors.suggestions,
	);
	// Saving the decision model as the classifier or the fallback while its
	// provider cannot serve it would route every classified request into a
	// failing call, so it waits until that provider is fixed.
	const blockedOnDecisionProvider = isDecisionInUse && !isProviderListLoading && decisionProviderState !== "configured";
	const canSave = canUpdate && (isDirty || isCreating) && !isResetting && !(isSubmitted && hasErrors) && !blockedOnDecisionProvider;

	return {
		form,
		errors,
		isCreating,
		saved,
		canUpdate,
		live: {
			classifier: liveClassifier,
			semantic: liveSemantic,
			llm: liveLLM,
			keywords: liveKeywords,
			suggestions: liveSuggestions,
			decisionProvider: liveDecisionProvider,
			decisionModel: liveDecisionModel,
			prompt: livePrompt,
		},
		providers: {
			all: providersData ?? [],
			keys: allKeys ?? [],
			embedding: embeddingProviders,
			chat: chatProviders,
			loading: isProviderListLoading,
			keyIds,
			decisionState: decisionProviderState,
		},
		status: {
			semantic: semanticStatus,
			loading: statusLoading,
			fetching: statusFetching,
			isError: statusIsError,
			refetch: refetchStatus,
			retryWarmup,
			isRetryingWarmup,
		},
		defaults: { llmPrompt: defaultLLMPrompt, decision: decisionDefaults, tier: tierDefaults },
		derived: {
			isClassifierConfigured,
			isLLMFallbackEnabled,
			isDecisionFallbackEnabled,
			isVectorStoreConnected,
			totalPhrases,
			willReembedAll,
			willReembed: willReembedAll || newPhraseCount > 0,
			newPhraseCount,
			reusedPhraseCount,
			hasUnsavedEmbeddingConfigChanges,
			hasUnsavedDecisionSettingsChanges,
			hasUnsavedLLMSettingsChanges,
			isDecisionGuidanceAtDefaults,
			blockedOnDecisionProvider,
			isDirty,
		},
		prompt: {
			markEdited: () => {
				promptEdited.current = true;
			},
			reset: () => {
				promptEdited.current = true;
				setValue("llm.prompt", defaultLLMPrompt, { shouldDirty: true });
			},
		},
		sheets: { open: openSheet, setOpen: setOpenSheet },
		staging: {
			active: activeStaged,
			count: stagedPhraseCount(activeStaged),
			pendingSuggestions,
			stage: stageSuggestion,
			unstage: unstageSuggestion,
			edit: editStagedPhrase,
			discard: discardStaged,
		},
		restore: {
			dialogOpen: restoreDialogOpen,
			setDialogOpen: setRestoreDialogOpen,
			phrases: restorePhraseDefaults,
			decision: restoreDecisionDefaults,
			isResetting,
		},
		submit,
		discard,
		isSaving,
		canSave,
		submitError,
		invalidFields,
		saveCount,
		selectClassifier: (next: AnalyzerFormValues["classifier"]) => {
			if (next !== getValues("classifier")) setValue("classifier", next, { shouldDirty: true });
		},
	};
}

export type RouterConfigForm = ReturnType<typeof useRouterConfigForm>;

// countFieldErrors counts the fields a react-hook-form error tree marks
// invalid: each node carrying a message is one field.
export function countFieldErrors(errors: unknown): number {
	if (!errors || typeof errors !== "object") return 0;
	if ("message" in errors && typeof (errors as { message?: unknown }).message === "string") return 1;
	return Object.entries(errors).reduce((total, [key, value]) => (key === "ref" ? total : total + countFieldErrors(value)), 0);
}