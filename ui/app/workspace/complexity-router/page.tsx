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
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ScrollArea } from "@/components/ui/scrollArea";
import { TagInput } from "@/components/ui/tagInput";
import { EmbeddingSupportedProviders } from "@/lib/constants/logs";
import { getErrorMessage, useGetCoreConfigQuery, useGetProvidersQuery } from "@/lib/store";
import { useGetAllKeysQuery } from "@/lib/store/apis/providersApi";
import {
	useGetComplexityAnalyzerConfigQuery,
	useGetComplexitySemanticStatusQuery,
	useResetComplexityAnalyzerConfigMutation,
	useUpdateComplexityAnalyzerConfigMutation,
} from "@/lib/store/apis/governanceApi";
import { AnalyzerConfig, KeywordListKey, TIER_PHRASE_LIST_DEFINITIONS } from "@/lib/types/complexityRouter";
import { ModelProvider } from "@/lib/types/config";
import { DBKey } from "@/lib/types/governance";
import { cn } from "@/lib/utils";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { ExternalLink, Info, LoaderCircle, RotateCcw, Save, Settings2, TriangleAlert } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { toast } from "sonner";
import {
	AnalyzerFormValues,
	analyzerConfigSchema,
	DEFAULT_FORM_VALUES,
	toFormValues,
} from "./formSchema";
import { ClassifierStatusBadge } from "./views/classifierStatusBadge";
import EmbeddingConfigSheet from "./views/embeddingConfigSheet";
import { SectionHeading } from "./views/formPrimitives";

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

// The three tier lists sit side by side, so they collapse to a fixed height
// rather than to a fixed number of phrases: phrases wrap to different numbers of
// lines, and equal counts would leave the columns visibly uneven.
const PHRASE_LIST_COLLAPSED_HEIGHT = 260;

function testIdPart(value: string) {
	return value.replace(/_/g, "-");
}

export default function ComplexityRouterPage() {
	const canUpdate = useRbac(RbacResource.RoutingRules, RbacOperation.Update);
	const { data, isLoading, isFetching, error, refetch } = useGetComplexityAnalyzerConfigQuery();
	const [updateConfig, { isLoading: isSaving }] = useUpdateComplexityAnalyzerConfigMutation();
	const [resetConfig, { isLoading: isResetting }] = useResetComplexityAnalyzerConfigMutation();

	const [submitError, setSubmitError] = useState<string | null>(null);
	const [restoreDialogOpen, setRestoreDialogOpen] = useState(false);
	const [embeddingSheetOpen, setEmbeddingSheetOpen] = useState(false);

	const { data: providersData, isLoading: providersLoading } = useGetProvidersQuery();
	const { data: allKeys, isLoading: keysLoading } = useGetAllKeysQuery();
	const embeddingProviders = useMemo(
		() => (providersData || []).filter((provider) => supportsEmbedding(provider) && hasEnabledKey(provider, allKeys || [])),
		[providersData, allKeys],
	);

	const { data: coreConfig } = useGetCoreConfigQuery({ fromDB: true });
	const isVectorStoreConnected = coreConfig?.is_cache_connected ?? false;

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
	const { data: semanticStatus, isLoading: statusLoading } = useGetComplexitySemanticStatusQuery(undefined, {
		skip: !data?.semantic,
		pollingInterval: statusPollInterval,
	});
	useEffect(() => {
		setStatusPollInterval(semanticStatus?.state === "warming" ? 2000 : semanticStatus?.state === "failed" ? 10000 : 0);
	}, [semanticStatus?.state]);

	const {
		register,
		handleSubmit,
		reset,
		control,
		watch,
		setValue,
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

	const liveSemantic = watch("semantic");
	const liveKeywords = watch("keywords");

	// Narrows the model list to what this provider's enabled keys can actually
	// serve. /api/models only applies per-key allow-lists and blacklists when it
	// is handed key ids; without them it returns the whole provider pool, so the
	// dropdown offers models every key would reject. Memoized because
	// ModelMultiselect refetches whenever this array's identity changes.
	const enabledKeyIdsForProvider = useMemo(
		() => (allKeys || []).filter((key) => key.provider === liveSemantic?.provider && key.enabled !== false).map((key) => key.key_id),
		[allKeys, liveSemantic?.provider],
	);

	const isClassifierConfigured = Boolean(liveSemantic?.provider && liveSemantic?.embedding_model);
	// The embedding fields live behind a sheet, so a pending edit to them would
	// otherwise be invisible from the page.
	// react-hook-form keeps reverted fields in dirtyFields with a false value, so
	// the flags are what matter, not the key count.
	const hasUnsavedEmbeddingChanges = Object.values(dirtyFields.semantic ?? {}).some(Boolean);

	const totalPhrases = useMemo(
		() =>
			(liveKeywords?.simple_keywords?.length ?? 0) +
			(liveKeywords?.medium_keywords?.length ?? 0) +
			(liveKeywords?.complex_keywords?.length ?? 0),
		[liveKeywords],
	);

	// Every embedding-cost warning below is about what the pending save will do,
	// so it is gated on there being a pending save at all. Without this the page
	// compares the form against a stale `data` and bills a save that cannot
	// happen: Restore defaults persists server-side and resets the form, leaving
	// it clean while the config query has not refetched yet — the exact window
	// where a "saving will embed N phrases" line appears next to a disabled Save.
	const hasPendingSave = isDirty;

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
		if (!data || isDirty) return;
		reset(toFormValues(data));
		setSubmitError(null);
	}, [data, isDirty, reset]);

	const handleDiscard = () => {
		if (data) reset(toFormValues(data));
		setSubmitError(null);
	};

	const handleRestoreDefaults = () => {
		if (!canUpdate) return;
		setSubmitError(null);
		resetConfig()
			.unwrap()
			.then((defaults) => {
				reset(toFormValues(defaults));
				toast.success("Reset to defaults", { position: "top-right" });
			})
			.catch((err) => {
				setSubmitError(getErrorMessage(err));
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
		const semantic = values.semantic.provider && values.semantic.embedding_model ? values.semantic : (data?.semantic ?? undefined);
		const payload: AnalyzerConfig = {
			keywords: values.keywords,
			...(semantic ? { semantic } : {}),
		};
		updateConfig(payload)
			.unwrap()
			.then((res) => {
				reset(toFormValues(res));
				setEmbeddingSheetOpen(false);
				toast.success("Configuration saved", { position: "top-right" });
			})
			.catch((err) => {
				setSubmitError(getErrorMessage(err));
			});
	};

	// Saving from inside the sheet still submits the whole configuration, so a
	// phrase error would report behind it. Close the sheet in that case, otherwise
	// the message is hidden under the overlay.
	const submit = handleSubmit(onValid, (formErrors) => {
		if (!formErrors.semantic) setEmbeddingSheetOpen(false);
	});

	if (isLoading && !data) {
		return <FullPageLoader />;
	}

	if (error && !data) {
		return (
			<div className="mx-auto w-full max-w-7xl space-y-4 px-4 pt-6 sm:px-6 sm:pt-8 lg:px-14">
				<p className="text-destructive font-mono text-sm">{getErrorMessage(error)}</p>
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

	const keywordErrors = errors.keywords;
	const hasErrors = Boolean(keywordErrors || errors.semantic);
	const canSave = canUpdate && isDirty && !isResetting && !(isSubmitted && hasErrors);

	// Rendered on the page and again inside the sheet: the re-embed cost is a
	// consequence of saving, and either surface can trigger the save.
	// Only a full re-embed is worth warning about in the sheet: every field that
	// can cause one lives there, and the sheet has its own Save. Adding phrases
	// is a page-level edit and is reported on the page instead, so the two
	// surfaces no longer repeat the same sentence at each other.
	const reembedAllWarning = willReembedAll ? (
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
		!willReembedAll && newPhraseCount > 0 ? (
			<Alert variant="warning" data-testid="complexity-router-new-phrase-warning">
				<TriangleAlert className="h-4 w-4" />
				<AlertDescription>
					Saving will embed {newPhraseCount} new reference phrase{newPhraseCount === 1 ? "" : "s"} through the selected provider. The other{" "}
					{reusedPhraseCount} reuse the embeddings this gateway already holds.
				</AlertDescription>
			</Alert>
		) : null;

	const reembedWarning = reembedAllWarning ?? newPhraseWarning;

	return (
		<>
			<form className="no-padding-parent own-scroll-parent flex h-full min-h-0 w-full flex-col" onSubmit={submit} noValidate>
				{/* The footer is a sibling of the scroll area rather than a sticky child
				    of it. Radix wraps scrolled content in a display:table element, and
				    position:sticky is unreliable inside table boxes: it parked the footer
				    partway up the scrollport and left dead space beneath it. */}
				<ScrollArea className="min-h-0 flex-1 px-14 pt-4">
					<div className="mx-auto w-full max-w-7xl space-y-6 pb-8">
						{/* ── Page header ── */}
						<div className="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
							<div className="space-y-1.5">
								<div className="flex items-center gap-2">
									<h2 className="text-lg font-semibold tracking-tight">Complexity Router</h2>
									<Badge aria-label="Complexity Router is in beta">Beta</Badge>
								</div>
								<p className="text-muted-foreground max-w-2xl text-sm leading-relaxed">
									Each request is embedded and takes the tier of the nearest reference phrase, filling the{" "}
									<code className="bg-muted rounded-sm px-1 py-0.5 font-mono text-xs">complexity_tier</code> field that routing rules
									target.
								</p>
							</div>

							{/* Status and embedding setup ride in the header rather than as
							    sections of their own: both are checked occasionally, while the
							    phrase lists below are the page's actual work surface. */}
							<div className="flex shrink-0 flex-wrap items-center gap-2">
								<ClassifierStatusBadge
									status={semanticStatus}
									isLoading={statusLoading}
									isNotConfigured={!isClassifierConfigured}
									isNotSaved={isClassifierConfigured && !data.semantic}
									hasUnsavedChanges={willReembed}
									hasEmbeddingProviders={embeddingProviders.length > 0}
									onConfigure={() => setEmbeddingSheetOpen(true)}
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
									{hasUnsavedEmbeddingChanges && (
										<span className="size-1.5 rounded-full bg-amber-500" role="status" aria-label="Unsaved embedding changes" />
									)}
								</Button>
								<Button asChild variant="outline" size="sm" data-testid="complexity-router-docs-link">
									<a href={"https://docs.getbifrost.ai/features/governance/complexity-router"} target="_blank" rel="noopener noreferrer">
										<ExternalLink className="size-3.5" />
										Docs
									</a>
								</Button>
							</div>
						</div>

						{/* The missing-provider warning lives in the embedding sheet, next to
						    the control it is about. On the page it pushed the phrase lists —
						    the only thing here an operator can act on without leaving — below
						    the fold; the header badge already carries the state. */}

						{/* ── Phrase to Tier Mapping ── */}
						<div className="space-y-3">
							<SectionHeading
								title="Phrase to Tier Mapping"
								description="A request takes the tier of its nearest phrase."
								aside={
									<span className="text-muted-foreground font-mono text-[11px] tabular-nums" data-testid="complexity-router-phrase-total">
										{totalPhrases} phrases
									</span>
								}
							/>

							<Alert variant="info" data-testid="complexity-router-phrase-defaults-callout">
								<Info className="h-4 w-4" />
								<AlertDescription>
									The added reference phrases are examples to help you get started. We recommend auditing, refining and adding your own
									reference phrases.
								</AlertDescription>
							</Alert>

							{/* Root-level phrase issues such as cross-tier duplicates have no single
							    field to attach to, so they render above the lists. */}
							{keywordErrors?.message && (
								<p className="text-destructive text-xs" data-testid="complexity-router-keywords-error">
									{keywordErrors.message}
								</p>
							)}

							{/* One column per tier, side by side: the three lists are read against
							    each other, and equal-width columns keep a phrase's tier obvious
							    from its position. */}
							<div className="grid gap-3 md:grid-cols-3">
								{TIER_PHRASE_LIST_DEFINITIONS.map(({ key, label, description }) => {
									const fieldError = keywordErrors?.[key as KeywordListKey];
									const errorId = `keywords-${key}-error`;
									return (
										<div key={key} className="bg-card relative overflow-hidden rounded-sm border">
											<Controller
												control={control}
												name={`keywords.${key}` as const}
												rules={{ validate: (value) => (value.length > 0 ? true : `${label} phrases cannot be empty`) }}
												render={({ field }) => (
													<div className="space-y-2 p-4 pl-5">
														<div className="flex items-center justify-between">
															<span className="text-xs font-medium">{label}</span>
															<span className="text-muted-foreground font-mono text-[11px] tabular-nums">
																{field.value.length} {field.value.length === 1 ? "phrase" : "phrases"}
															</span>
														</div>
														<p className="text-muted-foreground text-xs leading-relaxed">{description}</p>
														<TagInput
															data-testid={`complexity-router-keywords-${testIdPart(key)}-input`}
															value={field.value}
															onValueChange={field.onChange}
															collapsedMaxHeight={PHRASE_LIST_COLLAPSED_HEIGHT}
															expandButtonTestId={`complexity-router-keywords-${testIdPart(key)}-expand-button`}
															placeholder="Type a reference phrase and press Enter"
															aria-invalid={fieldError ? true : undefined}
															aria-describedby={fieldError ? errorId : undefined}
															className={cn(fieldError && "border-destructive")}
														/>
														{fieldError && (
															<p id={errorId} className="text-destructive text-xs">
																{fieldError.message}
															</p>
														)}
													</div>
												)}
											/>
										</div>
									);
								})}
							</div>
						</div>

						{reembedWarning}

						{/* ── Submit error ── */}
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
				<div className="bg-card border-t px-14 py-4">
					<div className="mx-auto flex w-full max-w-7xl flex-wrap items-center justify-end gap-2.5">
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
				canUpdate={canUpdate}
				providers={embeddingProviders}
				providerKeyIds={enabledKeyIdsForProvider}
				providersLoading={isProviderListLoading}
				isVectorStoreConnected={isVectorStoreConnected}
				warning={reembedAllWarning}
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
