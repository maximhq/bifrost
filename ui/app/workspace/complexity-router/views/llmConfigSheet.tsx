import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ModelMultiselect } from "@/components/ui/modelMultiselect";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { getProviderLabel } from "@/lib/constants/logs";
import { MAX_LLM_MESSAGE_HISTORY, MIN_LLM_MESSAGE_HISTORY } from "@/lib/types/complexityRouter";
import { ModelProvider, ModelProviderName } from "@/lib/types/config";
import { cn } from "@/lib/utils";
import { Link } from "@tanstack/react-router";
import { ArrowRight, Info, LoaderCircle, Save, TriangleAlert } from "lucide-react";
import { Controller, type Control, type FieldErrors, type UseFormRegister, type UseFormSetValue } from "react-hook-form";
import type { AnalyzerFormValues, LLMFormValues } from "../formSchema";
import { llmTimeoutFieldValue } from "../formSchema";
import { FieldLabel } from "./formPrimitives";

interface Props {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	control: Control<AnalyzerFormValues>;
	register: UseFormRegister<AnalyzerFormValues>;
	setValue: UseFormSetValue<AnalyzerFormValues>;
	errors: FieldErrors<AnalyzerFormValues>["llm"];
	llm: LLMFormValues | undefined;
	canUpdate: boolean;
	providers: ModelProvider[];
	// Ids of the selected provider's enabled keys, narrowing the model list to
	// what those keys can actually serve — same contract as the embedding sheet.
	providerKeyIds: string[];
	providersLoading: boolean;
	canSave: boolean;
	isSaving: boolean;
	onSave: () => void;
}

// LLMConfigSheet holds every field of the llm block, mirroring
// EmbeddingConfigSheet's relationship to the semantic block: bound to the
// page's form, so closing keeps edits pending, and saving submits the whole
// configuration because there is no llm-only write.
export default function LLMConfigSheet({
	open,
	onOpenChange,
	control,
	register,
	setValue,
	errors,
	llm,
	canUpdate,
	providers,
	providerKeyIds,
	providersLoading,
	canSave,
	isSaving,
	onSave,
}: Props) {
	const noProviders = !providersLoading && providers.length === 0;
	const isConfigured = Boolean(llm?.provider && llm?.model);
	const savedProviderUnavailable =
		!providersLoading && !noProviders && Boolean(llm?.provider) && !providers.some((provider) => provider.name === llm?.provider);

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent className="flex flex-col p-0" data-testid="complexity-router-llm-sheet">
				<SheetHeader className="flex flex-col items-start gap-1 px-6 py-4" headerClassName="bg-card z-10 mb-0 border-b">
					<SheetTitle>LLM fallback configuration</SheetTitle>
					<SheetDescription className="text-xs">
						The chat model asked to name a tier when semantic classification cannot. API keys are inherited from the provider&apos;s main
						configuration.
					</SheetDescription>
				</SheetHeader>

				<div className="custom-scrollbar min-h-0 flex-1 space-y-5 overflow-y-auto px-6 py-5">
					{/* The cost of this classifier is latency, and it is paid on every
					    classified request, so it is stated up front rather than
					    discovered in production. */}
					<Alert variant="warning" data-testid="complexity-router-llm-latency-callout">
						<TriangleAlert className="h-4 w-4" />
						<AlertDescription>
							A request that reaches this fallback waits on one completion from this model before it is routed. Pick a small, fast model;
							the timeout below caps the wait, and a timed-out classification skips complexity routing for that request.
						</AlertDescription>
					</Alert>

					{noProviders && (
						<Alert variant="warning" data-testid="complexity-router-no-llm-providers">
							<TriangleAlert className="h-4 w-4" />
							<AlertDescription className="gap-2">
								<span>No provider with an enabled key is configured. There is nothing to select here until one exists.</span>
								<Button asChild variant="outline" size="sm" data-testid="complexity-router-llm-sheet-add-provider-link">
									<Link to="/workspace/providers">
										Add a provider
										<ArrowRight className="size-3.5" />
									</Link>
								</Button>
							</AlertDescription>
						</Alert>
					)}

					{savedProviderUnavailable && (
						<Alert variant="warning" data-testid="complexity-router-llm-saved-provider-unavailable">
							<TriangleAlert className="h-4 w-4" />
							<AlertDescription className="gap-2">
								<span>
									<span className="font-medium">{getProviderLabel(llm?.provider ?? "")}</span> is saved here but has no enabled key, so it
									cannot classify anything. Re-enable a key for it, or select another provider below.
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

					{!providersLoading && !noProviders && !isConfigured && (
						<Alert variant="info" data-testid="complexity-router-llm-required-callout">
							<Info className="h-4 w-4" />
							<AlertDescription>
								Pick a provider and model to configure the rest. Until then the LLM classifier cannot run.
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
											disabled={!canUpdate || noProviders}
										>
											<SelectTrigger className="w-full" id="llm-provider" data-testid="complexity-router-llm-provider-select">
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
											keys={providerKeyIds}
											value={field.value ?? ""}
											onChange={(model) => {
												field.onChange(model);
											}}
											placeholder={llm?.provider ? "Search or type a chat model…" : "Select a provider first"}
											disabled={!canUpdate || !llm?.provider}
										/>
									)}
								/>
								{errors?.model ? <p className="text-destructive text-xs">{errors.model.message}</p> : null}
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
												disabled={!canUpdate || !isConfigured}
												value={llmTimeoutFieldValue(field.value)}
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
										htmlFor="llm-message-history"
										tooltip={
											<>
												The most recent user messages are sent to the classifier oldest to newest. Widening this lets a short follow-up like
												&ldquo;and make it faster&rdquo; inherit earlier intent, but sends more input tokens per request. System prompts and
												assistant replies are never sent.
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
										disabled={!canUpdate || !isConfigured}
										aria-invalid={errors?.message_history_count ? true : undefined}
										className={cn("font-mono", errors?.message_history_count && "border-destructive focus-visible:ring-destructive")}
										{...register("llm.message_history_count", { valueAsNumber: true })}
									/>
									{errors?.message_history_count && <p className="text-destructive text-xs">{errors.message_history_count.message}</p>}
								</div>
							</div>

							{/* Budget attribution */}
							<div className="flex items-center justify-between gap-6 border-t pt-4">
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
											disabled={!canUpdate || !isConfigured}
										/>
									)}
								/>
							</div>
						</>
					)}
				</div>

				<SheetFooter className="bg-card flex-row items-center justify-end gap-2 border-t px-6 py-4">
					<Button
						type="button"
						variant="outline"
						size="sm"
						onClick={() => onOpenChange(false)}
						data-testid="complexity-router-llm-sheet-close-button"
					>
						Close
					</Button>
					<Button
						type="button"
						size="sm"
						onClick={onSave}
						disabled={!canSave || isSaving}
						data-testid="complexity-router-llm-sheet-save-button"
					>
						{isSaving ? <LoaderCircle className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />}
						{isSaving ? "Saving…" : "Save changes"}
					</Button>
				</SheetFooter>
			</SheetContent>
		</Sheet>
	);
}