import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ModelMultiselect } from "@/components/ui/modelMultiselect";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { getProviderLabel } from "@/lib/constants/logs";
import {
	MAX_LLM_MESSAGE_HISTORY,
	MAX_SEMANTIC_MESSAGE_HISTORY,
	MIN_LLM_MESSAGE_HISTORY,
	MIN_SEMANTIC_MESSAGE_HISTORY,
	SEMANTIC_FALLBACK_OPTIONS,
	SEMANTIC_VECTOR_STORE_OPTIONS,
} from "@/lib/types/complexityRouter";
import { ModelProvider, ModelProviderName } from "@/lib/types/config";
import { cn } from "@/lib/utils";
import { Link } from "@tanstack/react-router";
import { ArrowRight, Info, LoaderCircle, Save, TriangleAlert } from "lucide-react";
import type { ReactNode } from "react";
import { Controller, type Control, type FieldErrors, type UseFormRegister, type UseFormSetValue } from "react-hook-form";
import type { AnalyzerFormValues, LLMFormValues, SemanticFormValues } from "../formSchema";
import { llmTimeoutFieldValue, semanticTimeoutFieldValue } from "../formSchema";
import { FieldLabel, InfoTip } from "./formPrimitives";

interface Props {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	control: Control<AnalyzerFormValues>;
	register: UseFormRegister<AnalyzerFormValues>;
	setValue: UseFormSetValue<AnalyzerFormValues>;
	errors: FieldErrors<AnalyzerFormValues>["semantic"];
	semantic: SemanticFormValues | undefined;
	// The llm block's own errors and live values. Its fields render inline here,
	// below the fallback selector, rather than in a sheet of their own: picking
	// "LLM classifier" as the fallback is the only thing that makes them
	// meaningful, so they appear and disappear with that choice instead of
	// living behind a second button an operator has to discover separately.
	llmErrors: FieldErrors<AnalyzerFormValues>["llm"];
	llm: LLMFormValues | undefined;
	canUpdate: boolean;
	providers: ModelProvider[];
	// Ids of the selected provider's enabled keys. Handed to ModelMultiselect so
	// the model list is narrowed to what those keys are allowed to serve.
	providerKeyIds: string[];
	// Chat-capable providers/keys for the inline llm fallback fields — a
	// separate pool from the embedding providers above, since the two calls
	// need different provider capabilities.
	llmProviders: ModelProvider[];
	llmProviderKeyIds: string[];
	providersLoading: boolean;
	isVectorStoreConnected: boolean;
	// Rendered above the footer, and scoped to the full re-embed that only the
	// fields in here can trigger. Phrase-list edits cost the provider too, but
	// they are made on the page and are reported there — repeating that here
	// would attribute a page-level cost to these controls.
	warning?: ReactNode;
	canSave: boolean;
	isSaving: boolean;
	onSave: () => void;
	// The page renders this too, but saving from here leaves the sheet open on top of it,
	// so a failed submit would otherwise report itself entirely out of sight.
	submitError?: string | null;
}

// EmbeddingConfigSheet holds every field of the semantic block. It is a sheet
// rather than a page section because these are set once and then left alone,
// while the phrase lists behind it are the thing operators actually tune.
//
// Its fields are bound to the page's form, so closing the sheet keeps edits
// pending rather than dropping them; the page footer can still save or discard
// them. Saving from here submits the whole configuration, which is what the
// endpoint takes — there is no semantic-only write.
export default function EmbeddingConfigSheet({
	open,
	onOpenChange,
	control,
	register,
	setValue,
	errors,
	semantic,
	llmErrors,
	llm,
	canUpdate,
	providers,
	providerKeyIds,
	llmProviders,
	llmProviderKeyIds,
	providersLoading,
	isVectorStoreConnected,
	warning,
	canSave,
	isSaving,
	onSave,
	submitError,
}: Props) {
	const noProviders = !providersLoading && providers.length === 0;
	const isConfigured = Boolean(semantic?.provider && semantic?.embedding_model);
	// A provider saved earlier can drop out of the selectable list — its keys get
	// disabled or deleted. The Select then falls back to its placeholder while the
	// form still holds the old name, which reads as "nothing is selected" rather
	// than "what you selected stopped working". Say which it is.
	const savedProviderUnavailable =
		!providersLoading && !noProviders && Boolean(semantic?.provider) && !providers.some((provider) => provider.name === semantic?.provider);

	const isLLMFallbackSelected = (semantic?.fallback ?? "none") === "llm";
	const noLLMProviders = !providersLoading && llmProviders.length === 0;
	const isLLMConfigured = Boolean(llm?.provider && llm?.model);
	const savedLLMProviderUnavailable =
		!providersLoading && !noLLMProviders && Boolean(llm?.provider) && !llmProviders.some((provider) => provider.name === llm?.provider);

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="flex flex-col p-0" data-testid="complexity-router-embedding-sheet">
				<SheetHeader className="flex flex-col items-start gap-1 px-6 py-4" headerClassName="bg-card z-10 mb-0 border-b">
					<SheetTitle>Embedding configuration</SheetTitle>
					<SheetDescription className="text-xs">
						The model that embeds requests and reference phrases. API keys are inherited from the provider&apos;s main configuration.
					</SheetDescription>
				</SheetHeader>

				<div className="custom-scrollbar min-h-0 flex-1 space-y-5 overflow-y-auto px-6 py-5">
					{noProviders && (
						<Alert variant="warning" data-testid="complexity-router-no-embedding-providers">
							<TriangleAlert className="h-4 w-4" />
							<AlertDescription className="gap-2">
								<span>No embedding-capable provider is configured. There is nothing to select here until one exists.</span>
								<Button asChild variant="outline" size="sm" data-testid="complexity-router-sheet-add-provider-link">
									<Link to="/workspace/providers">
										Add an embedding provider
										<ArrowRight className="size-3.5" />
									</Link>
								</Button>
							</AlertDescription>
						</Alert>
					)}

					{savedProviderUnavailable && (
						<Alert variant="warning" data-testid="complexity-router-saved-provider-unavailable">
							<TriangleAlert className="h-4 w-4" />
							<AlertDescription className="gap-2">
								<span>
									<span className="font-medium">{getProviderLabel(semantic?.provider ?? "")}</span> is saved here but has no enabled key, so
									it cannot embed anything. Re-enable a key for it, or select another provider below.
								</span>
								<Button asChild variant="outline" size="sm" data-testid="complexity-router-saved-provider-link">
									<Link to="/workspace/providers" search={{ provider: semantic?.provider }}>
										Review provider keys
										<ArrowRight className="size-3.5" />
									</Link>
								</Button>
							</AlertDescription>
						</Alert>
					)}

					{/* Everything below the provider and model is part of the semantic
					    block, which is only persisted once both are set. Without this
					    hint the remaining fields look editable but silently reset. */}
					{!providersLoading && !noProviders && !isConfigured && (
						<Alert variant="info" data-testid="complexity-router-classifier-required-callout">
							<Info className="h-4 w-4" />
							<AlertDescription>
								Pick an embedding provider and model to configure the rest. Until then the classifier is off and only the phrase lists are
								saved.
							</AlertDescription>
						</Alert>
					)}

					{providersLoading ? (
						<div className="flex items-center justify-center py-6">
							<LoaderCircle className="text-muted-foreground size-4 animate-spin" />
						</div>
					) : (
						<>
							<div className="space-y-2">
								<FieldLabel htmlFor="semantic-provider">Embedding provider</FieldLabel>
								<Controller
									control={control}
									name="semantic.provider"
									render={({ field }) => (
										<Select
											value={field.value || undefined}
											onValueChange={(value: ModelProviderName) => {
												if (value === field.value) return;
												field.onChange(value);
												// A model name is only meaningful for its own provider.
												setValue("semantic.embedding_model", "", { shouldDirty: true });
											}}
											disabled={!canUpdate || noProviders}
										>
											<SelectTrigger className="w-full" id="semantic-provider" data-testid="complexity-router-semantic-provider-select">
												<SelectValue placeholder="Select provider" />
											</SelectTrigger>
											<SelectContent>
												{providers
													.filter((provider) => provider.name)
													.map((provider) => (
														<SelectItem key={provider.name} value={provider.name}>
															<div className="flex items-center gap-2">
																<RenderProviderIcon provider={provider.name as ProviderIconType} size="sm" className="h-4 w-4" />
																<span>{getProviderLabel(provider.name)}</span>
															</div>
														</SelectItem>
													))}
											</SelectContent>
										</Select>
									)}
								/>
								{errors?.provider && <p className="text-destructive text-xs">{errors.provider.message}</p>}
							</div>

							<div className="space-y-2">
								<FieldLabel htmlFor="semantic-embedding-model">Embedding model</FieldLabel>
								<Controller
									control={control}
									name="semantic.embedding_model"
									render={({ field }) => (
										<ModelMultiselect
											inputId="semantic-embedding-model"
											data-testid="complexity-router-semantic-model-select"
											isSingleSelect
											provider={semantic?.provider || undefined}
											keys={providerKeyIds}
											value={field.value ?? ""}
											onChange={(model) => {
												field.onChange(model);
											}}
											placeholder={semantic?.provider ? "Search or type an embedding model…" : "Select a provider first"}
											disabled={!canUpdate || !semantic?.provider}
										/>
									)}
								/>
								{errors?.embedding_model ? <p className="text-destructive text-xs">{errors.embedding_model.message}</p> : null}
							</div>

							{/* Similarity floor + conversation window */}
							<div className="grid gap-4 sm:grid-cols-2">
								<div className="space-y-2">
									<FieldLabel htmlFor="semantic-min-similarity">Minimum similarity threshold</FieldLabel>
									<Input
										id="semantic-min-similarity"
										data-testid="complexity-router-semantic-min-similarity-input"
										type="number"
										min={0}
										max={0.99}
										step={0.05}
										disabled={!canUpdate || !isConfigured}
										aria-invalid={errors?.min_similarity ? true : undefined}
										className={cn("font-mono", errors?.min_similarity && "border-destructive focus-visible:ring-destructive")}
										{...register("semantic.min_similarity", { valueAsNumber: true })}
									/>
									{errors?.min_similarity ? (
										<p className="text-destructive text-xs">{errors.min_similarity.message}</p>
									) : (
										<>
											<p className="text-muted-foreground text-xs leading-relaxed">
												Between 0 and 1. How close the nearest phrase must be before its tier is used.
											</p>
											<p className="flex items-start gap-1.5 text-xs leading-relaxed text-amber-700 dark:text-amber-400">
												<TriangleAlert className="mt-0.5 size-3 shrink-0" />
												Lower values raise the risk of false positives.
											</p>
										</>
									)}
								</div>

								<div className="space-y-2">
									<FieldLabel
										htmlFor="semantic-message-history"
										tooltip={
											<>
												The most recent user messages are joined oldest to newest and embedded as one text. Widening this lets a short
												follow-up like &ldquo;and make it faster&rdquo; inherit earlier intent, but dilutes the latest message and embeds
												more tokens. System prompts and assistant replies are never embedded.
											</>
										}
									>
										Max messages to embed
									</FieldLabel>
									<Input
										id="semantic-message-history"
										data-testid="complexity-router-semantic-message-history-input"
										type="number"
										min={MIN_SEMANTIC_MESSAGE_HISTORY}
										max={MAX_SEMANTIC_MESSAGE_HISTORY}
										step={1}
										disabled={!canUpdate || !isConfigured}
										aria-invalid={errors?.message_history_count ? true : undefined}
										className={cn("font-mono", errors?.message_history_count && "border-destructive focus-visible:ring-destructive")}
										{...register("semantic.message_history_count", { valueAsNumber: true })}
									/>
									{errors?.message_history_count && <p className="text-destructive text-xs">{errors.message_history_count.message}</p>}
								</div>
							</div>

							{/* Timeout + phrase storage */}
							<div className="grid gap-4 sm:grid-cols-2">
								<div className="space-y-2">
									<FieldLabel
										htmlFor="semantic-timeout"
										tooltip="Ceiling on the embedding call, which runs inline on the request path. Exceeding it skips complexity tier based routing for that request."
									>
										Embedding timeout (ms)
									</FieldLabel>
									<Controller
										control={control}
										name="semantic.timeout"
										render={({ field }) => (
											<Input
												id="semantic-timeout"
												data-testid="complexity-router-semantic-timeout-input"
												type="number"
												min={1}
												step={10}
												disabled={!canUpdate || !isConfigured}
												value={semanticTimeoutFieldValue(field.value)}
												onChange={(event) => {
													const raw = event.target.value;
													field.onChange(raw === "" ? "" : `${raw}ms`);
												}}
												aria-invalid={errors?.timeout ? true : undefined}
												className={cn("font-mono", errors?.timeout && "border-destructive focus-visible:ring-destructive")}
											/>
										)}
									/>
									{errors?.timeout && <p className="text-destructive text-xs">{errors.timeout.message}</p>}
								</div>

								<div className="space-y-2">
									<FieldLabel
										htmlFor="semantic-vector-store"
										tooltip={
											<span className="space-y-1.5">
												{SEMANTIC_VECTOR_STORE_OPTIONS.map((option) => (
													<span key={option.value} className="block">
														<b>{option.label}</b>: {option.tooltip}
													</span>
												))}
											</span>
										}
									>
										Reference phrase storage
									</FieldLabel>
									<Controller
										control={control}
										name="semantic.vector_store"
										render={({ field }) => (
											<Select value={field.value ?? "embedded"} onValueChange={field.onChange} disabled={!canUpdate || !isConfigured}>
												<SelectTrigger
													className="w-full"
													id="semantic-vector-store"
													data-testid="complexity-router-semantic-vector-store-select"
												>
													<SelectValue />
												</SelectTrigger>
												<SelectContent>
													{SEMANTIC_VECTOR_STORE_OPTIONS.map((option) => (
														<SelectItem key={option.value} value={option.value}>
															{option.label}
														</SelectItem>
													))}
												</SelectContent>
											</Select>
										)}
									/>
									{semantic?.vector_store === "vector_store" && !isVectorStoreConnected && (
										<p className="text-muted-foreground text-xs leading-relaxed">
											No vector store is connected, so phrases stay in the embedded store until one is configured.
										</p>
									)}
								</div>
							</div>

							{/* Fallback classifier. Lives here rather than in its own sheet
							    because it is a property of semantic classification — what to
							    do when it cannot answer — and is meaningless without it. Its
							    own settings appear inline, directly below, once this is
							    switched on; its prompt stays on the page (see the tooltip on
							    the section below), because that text needs width and
							    iteration room a sheet cannot give it. */}
							<div className="space-y-2 border-t pt-4">
								<FieldLabel
									htmlFor="semantic-fallback"
									tooltip={
										<span className="space-y-1.5">
											{SEMANTIC_FALLBACK_OPTIONS.map((option) => (
												<span key={option.value} className="block">
													<b>{option.label}</b>: {option.description}
												</span>
											))}
										</span>
									}
								>
									When no phrase matches confidently
								</FieldLabel>
								<Controller
									control={control}
									name="semantic.fallback"
									render={({ field }) => (
										<Select value={field.value ?? "none"} onValueChange={field.onChange} disabled={!canUpdate || !isConfigured}>
											<SelectTrigger className="w-full" id="semantic-fallback" data-testid="complexity-router-semantic-fallback-select">
												<SelectValue />
											</SelectTrigger>
											<SelectContent>
												{SEMANTIC_FALLBACK_OPTIONS.map((option) => (
													<SelectItem key={option.value} value={option.value}>
														{option.label}
													</SelectItem>
												))}
											</SelectContent>
										</Select>
									)}
								/>
							</div>

							{/* Fallback classifier fields. Rendered inline rather than in a
							    sheet of their own, and only while "LLM classifier" is the
							    selected fallback — a dormant llm block still keeps its saved
							    settings, but they have nothing to configure until this is on. */}
							{isLLMFallbackSelected && (
								<div className="space-y-5 border-t pt-4" data-testid="complexity-router-llm-fallback-section">
									<div className="flex items-center gap-1.5">
										<h3 className="text-sm font-medium">Fallback classifier</h3>
										<InfoTip label="About the fallback classifier prompt">
											This configures the model and its request settings only. The classification prompt itself is edited on the Complexity
											Router page, in the Fallback Classification Prompt section below the phrase lists.
										</InfoTip>
									</div>
									<p className="text-muted-foreground -mt-3 text-xs leading-relaxed">
										The chat model asked to name a tier when semantic classification cannot. API keys are inherited from the provider&apos;s
										main configuration.
									</p>

									{/* The cost of this classifier is latency, and it is paid on every
									    classified request, so it is stated up front rather than
									    discovered in production. */}
									<Alert variant="warning" data-testid="complexity-router-llm-latency-callout">
										<TriangleAlert className="h-4 w-4" />
										<AlertDescription>
											A request that reaches this fallback waits on one completion from this model before it is routed. Pick a small, fast
											model; the timeout below caps the wait, and a timed-out classification skips complexity routing for that request.
										</AlertDescription>
									</Alert>

									{noLLMProviders && (
										<Alert variant="warning" data-testid="complexity-router-no-llm-providers">
											<TriangleAlert className="h-4 w-4" />
											<AlertDescription className="gap-2">
												<span>No provider with an enabled key is configured. There is nothing to select here until one exists.</span>
												<Button asChild variant="outline" size="sm" data-testid="complexity-router-llm-add-provider-link">
													<Link to="/workspace/providers">
														Add a provider
														<ArrowRight className="size-3.5" />
													</Link>
												</Button>
											</AlertDescription>
										</Alert>
									)}

									{savedLLMProviderUnavailable && (
										<Alert variant="warning" data-testid="complexity-router-llm-saved-provider-unavailable">
											<TriangleAlert className="h-4 w-4" />
											<AlertDescription className="gap-2">
												<span>
													<span className="font-medium">{getProviderLabel(llm?.provider ?? "")}</span> is saved here but has no enabled key,
													so it cannot classify anything. Re-enable a key for it, or select another provider below.
												</span>
												<Button asChild variant="outline" size="sm" data-testid="complexity-router-llm-saved-provider-link">
													<Link to="/workspace/providers" search={{ provider: llm?.provider }}>
														Review provider keys
														<ArrowRight className="size-3.5" />
													</Link>
												</Button>
											</AlertDescription>
										</Alert>
									)}

									{!providersLoading && !noLLMProviders && !isLLMConfigured && (
										<Alert variant="info" data-testid="complexity-router-llm-required-callout">
											<Info className="h-4 w-4" />
											<AlertDescription>
												Pick a provider and model to configure the rest. Until then the LLM classifier cannot run.
											</AlertDescription>
										</Alert>
									)}

									<div className="space-y-2">
										<FieldLabel htmlFor="llm-provider">Fallback provider</FieldLabel>
										<Controller
											control={control}
											name="llm.provider"
											render={({ field }) => (
												<Select
													value={field.value || undefined}
													onValueChange={(value: ModelProviderName) => {
														if (value === field.value) return;
														field.onChange(value);
														// A model name is only meaningful for its own provider.
														setValue("llm.model", "", { shouldDirty: true });
													}}
													disabled={!canUpdate || noLLMProviders}
												>
													<SelectTrigger className="w-full" id="llm-provider" data-testid="complexity-router-llm-provider-select">
														<SelectValue placeholder="Select provider" />
													</SelectTrigger>
													<SelectContent>
														{llmProviders
															.filter((provider) => provider.name)
															.map((provider) => (
																<SelectItem key={provider.name} value={provider.name}>
																	<div className="flex items-center gap-2">
																		<RenderProviderIcon provider={provider.name as ProviderIconType} size="sm" className="h-4 w-4" />
																		<span>{getProviderLabel(provider.name)}</span>
																	</div>
																</SelectItem>
															))}
													</SelectContent>
												</Select>
											)}
										/>
										{llmErrors?.provider && <p className="text-destructive text-xs">{llmErrors.provider.message}</p>}
									</div>

									<div className="space-y-2">
										<FieldLabel htmlFor="llm-model">Fallback model</FieldLabel>
										<Controller
											control={control}
											name="llm.model"
											render={({ field }) => (
												<ModelMultiselect
													inputId="llm-model"
													data-testid="complexity-router-llm-model-select"
													isSingleSelect
													provider={llm?.provider || undefined}
													keys={llmProviderKeyIds}
													value={field.value ?? ""}
													onChange={(model) => {
														field.onChange(model);
													}}
													placeholder={llm?.provider ? "Search or type a chat model…" : "Select a provider first"}
													disabled={!canUpdate || !llm?.provider}
												/>
											)}
										/>
										{llmErrors?.model ? <p className="text-destructive text-xs">{llmErrors.model.message}</p> : null}
									</div>

									{/* Timeout + conversation window */}
									<div className="grid gap-4 sm:grid-cols-2">
										<div className="space-y-2">
											<FieldLabel
												htmlFor="llm-timeout"
												tooltip="Ceiling on the classification completion, which runs inline on the request path. Exceeding it skips complexity tier based routing for that request."
											>
												Classification timeout (ms)
											</FieldLabel>
											<Controller
												control={control}
												name="llm.timeout"
												render={({ field }) => (
													<Input
														id="llm-timeout"
														data-testid="complexity-router-llm-timeout-input"
														type="number"
														min={1}
														step={100}
														disabled={!canUpdate || !isLLMConfigured}
														value={llmTimeoutFieldValue(field.value)}
														onChange={(event) => {
															const raw = event.target.value;
															field.onChange(raw === "" ? "" : `${raw}ms`);
														}}
														aria-invalid={llmErrors?.timeout ? true : undefined}
														className={cn("font-mono", llmErrors?.timeout && "border-destructive focus-visible:ring-destructive")}
													/>
												)}
											/>
											{llmErrors?.timeout && <p className="text-destructive text-xs">{llmErrors.timeout.message}</p>}
										</div>

										<div className="space-y-2">
											<FieldLabel
												htmlFor="llm-message-history"
												tooltip={
													<>
														The most recent user messages are sent to the classifier oldest to newest. Widening this lets a short follow-up
														like &ldquo;and make it faster&rdquo; inherit earlier intent, but sends more input tokens per request. System
														prompts and assistant replies are never sent.
													</>
												}
											>
												Max messages to send
											</FieldLabel>
											<Input
												id="llm-message-history"
												data-testid="complexity-router-llm-message-history-input"
												type="number"
												min={MIN_LLM_MESSAGE_HISTORY}
												max={MAX_LLM_MESSAGE_HISTORY}
												step={1}
												disabled={!canUpdate || !isLLMConfigured}
												aria-invalid={llmErrors?.message_history_count ? true : undefined}
												className={cn(
													"font-mono",
													llmErrors?.message_history_count && "border-destructive focus-visible:ring-destructive",
												)}
												{...register("llm.message_history_count", { valueAsNumber: true })}
											/>
											{llmErrors?.message_history_count && (
												<p className="text-destructive text-xs">{llmErrors.message_history_count.message}</p>
											)}
										</div>
									</div>

									{/* Fallback classifier budget attribution */}
									<div className="flex items-center justify-between gap-6">
										<FieldLabel
											htmlFor="llm-count-toward-budgets"
											tooltip="Bills each classification completion to the same budgets as the request that triggered it. Cost is always reported to telemetry either way."
										>
											Count classification cost toward budgets
										</FieldLabel>
										<Controller
											control={control}
											name="llm.count_toward_budgets"
											render={({ field }) => (
												<Switch
													id="llm-count-toward-budgets"
													data-testid="complexity-router-llm-budgets-switch"
													checked={field.value ?? false}
													onCheckedChange={field.onChange}
													disabled={!canUpdate || !isLLMConfigured}
												/>
											)}
										/>
									</div>
								</div>
							)}

							{/* Embedding budget attribution */}
							<div className="flex items-center justify-between gap-6 border-t pt-4">
								<FieldLabel
									htmlFor="semantic-count-toward-budgets"
									tooltip="Bills each classification embedding to the same budgets as the request that triggered it, and warmup embeddings to the provider and model budgets. Cost is always reported to telemetry either way."
								>
									Count embedding cost toward budgets
								</FieldLabel>
								<Controller
									control={control}
									name="semantic.count_toward_budgets"
									render={({ field }) => (
										<Switch
											id="semantic-count-toward-budgets"
											data-testid="complexity-router-semantic-budgets-switch"
											checked={field.value ?? false}
											onCheckedChange={field.onChange}
											disabled={!canUpdate || !isConfigured}
										/>
									)}
								/>
							</div>

							{warning}
						</>
					)}
				</div>

				{submitError && (
					<div
						role="alert"
						data-testid="complexity-router-embedding-sheet-error"
						className="border-destructive/40 bg-destructive/10 text-destructive mx-6 mb-2 rounded-sm border px-3 py-2 font-mono text-sm"
					>
						{submitError}
					</div>
				)}

				<SheetFooter className="bg-card flex-row items-center justify-end gap-2 border-t px-6 py-4">
					<Button
						type="button"
						variant="outline"
						size="sm"
						onClick={() => onOpenChange(false)}
						data-testid="complexity-router-embedding-sheet-close-button"
					>
						Close
					</Button>
					<Button
						type="button"
						size="sm"
						onClick={onSave}
						disabled={!canSave || isSaving}
						data-testid="complexity-router-embedding-sheet-save-button"
					>
						{isSaving ? <LoaderCircle className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />}
						{isSaving ? "Saving…" : "Save changes"}
					</Button>
				</SheetFooter>
			</SheetContent>
		</Sheet>
	);
}