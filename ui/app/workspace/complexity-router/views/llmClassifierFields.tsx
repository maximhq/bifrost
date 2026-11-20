import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ModelSelector } from "@/components/ui/modelSelector";
import { ProviderSelector } from "@/components/ui/providerSelector";
import { Switch } from "@/components/ui/switch";
import { getProviderLabel } from "@/lib/constants/logs";
import { MAX_LLM_MESSAGE_HISTORY, MIN_LLM_MESSAGE_HISTORY } from "@/lib/types/complexityRouter";
import { ModelProvider, ModelProviderName } from "@/lib/types/config";
import { cn } from "@/lib/utils";
import { Link } from "@tanstack/react-router";
import { ArrowRight, Info, TriangleAlert } from "lucide-react";
import { useCallback } from "react";
import { Controller, type Control, type FieldErrors, type UseFormRegister, type UseFormSetValue } from "react-hook-form";
import { useTranslation } from "react-i18next";
import type { AnalyzerFormValues, LLMFormValues } from "../formSchema";
import { llmTimeoutFieldValue } from "../formSchema";
import { FieldLabel, InfoTip } from "./formPrimitives";

interface LLMClassifierFieldsProps {
	// "primary" when the llm is the router's classifier, "fallback" when it
	// answers what semantic matching leaves without a tier.
	variant: "primary" | "fallback";
	control: Control<AnalyzerFormValues>;
	register: UseFormRegister<AnalyzerFormValues>;
	setValue: UseFormSetValue<AnalyzerFormValues>;
	errors: FieldErrors<AnalyzerFormValues>["llm"];
	llm: LLMFormValues | undefined;
	canUpdate: boolean;
	// Chat-capable providers with an enabled key, and the selected one's key
	// ids, which narrow the model list to what those keys may serve.
	providers: ModelProvider[];
	providerKeyIds: string[];
	providersLoading: boolean;
}

// LLMClassifierFields edits the llm block: the chat model that classifies,
// its timeout, how much of the conversation it reads, and whether its cost
// counts toward budgets. The same fields serve the llm as the primary
// classifier and as the semantic classifier's fallback.
export function LLMClassifierFields({
	variant,
	control,
	register,
	setValue,
	errors,
	llm,
	canUpdate,
	providers,
	providerKeyIds,
	providersLoading,
}: LLMClassifierFieldsProps) {
	const { t: tcpx } = useTranslation("models", { keyPrefix: "routing.complexityUi" });
	// The pool is narrowed by the parent, so the selector filters the full
	// provider list back down to it rather than re-deriving the capability rules.
	const isLLMProvider = useCallback((provider: ModelProvider) => providers.some((p) => p.name === provider.name), [providers]);
	const noLLMProviders = !providersLoading && providers.length === 0;
	const isLLMConfigured = Boolean(llm?.provider && llm?.model);
	// A provider saved earlier can drop out of the selectable list when its keys
	// are disabled; say so instead of showing an empty selector.
	const savedLLMProviderUnavailable =
		!providersLoading && !noLLMProviders && Boolean(llm?.provider) && !providers.some((provider) => provider.name === llm?.provider);
	const primary = variant === "primary";

	return (
		<div
			className="space-y-5"
			data-testid={primary ? "complexity-router-llm-classifier-section" : "complexity-router-llm-fallback-section"}
		>
			{!primary && (
				<>
					<div className="flex items-center gap-1.5">
						<h3 className="text-sm font-medium">{tcpx("fallbackClassifier")}</h3>
						<InfoTip label={tcpx("fallbackPromptTitle")}>{tcpx("fallbackClassifierPromptTip")}</InfoTip>
					</div>
					<p className="text-muted-foreground -mt-3 text-xs leading-relaxed">{tcpx("fallbackClassifierHelp")}</p>
				</>
			)}

			{/* The cost of this classifier is latency, and it is paid on every
			    classified request, so it is stated up front rather than
			    discovered in production. */}
			<Alert variant="warning" data-testid="complexity-router-llm-latency-callout">
				<TriangleAlert className="h-4 w-4" />
				<AlertDescription>{tcpx(primary ? "llmPrimaryLatencyCallout" : "llmLatencyCallout")}</AlertDescription>
			</Alert>

			{noLLMProviders && (
				<Alert variant="warning" data-testid="complexity-router-no-llm-providers">
					<TriangleAlert className="h-4 w-4" />
					<AlertDescription className="gap-2">
						<span>{tcpx("noLlmProviders")}</span>
						<Button asChild variant="outline" size="sm" data-testid="complexity-router-llm-add-provider-link">
							<Link to="/workspace/providers">
								{tcpx("addProvider")}
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
						<span>{tcpx("savedLlmProviderUnavailable", { provider: getProviderLabel(llm?.provider ?? "") })}</span>
						<Button asChild variant="outline" size="sm" data-testid="complexity-router-llm-saved-provider-link">
							<Link to="/workspace/providers" search={{ provider: llm?.provider }}>
								{tcpx("reviewProviderKeys")}
								<ArrowRight className="size-3.5" />
							</Link>
						</Button>
					</AlertDescription>
				</Alert>
			)}

			{!providersLoading && !noLLMProviders && !isLLMConfigured && (
				<Alert variant="info" data-testid="complexity-router-llm-required-callout">
					<Info className="h-4 w-4" />
					<AlertDescription>{tcpx(primary ? "llmPrimaryRequired" : "llmRequired")}</AlertDescription>
				</Alert>
			)}

			<div className="space-y-2">
				<FieldLabel htmlFor="llm-provider">{tcpx(primary ? "llmProvider" : "fallbackProvider")}</FieldLabel>
				<Controller
					control={control}
					name="llm.provider"
					render={({ field }) => (
						<ProviderSelector
							inputId="llm-provider"
							data-testid="complexity-router-llm-provider-select"
							filter={isLLMProvider}
							value={field.value || ""}
							onChange={(value: string) => {
								if (value === field.value) return;
								field.onChange(value as ModelProviderName);
								// A model name is only meaningful for its own provider.
								setValue("llm.model", "", { shouldDirty: true });
							}}
							disabled={!canUpdate || noLLMProviders}
						/>
					)}
				/>
				{errors?.provider && <p className="text-destructive text-xs">{errors.provider.message}</p>}
			</div>

			<div className="space-y-2">
				<FieldLabel htmlFor="llm-model">{tcpx(primary ? "llmModel" : "fallbackModel")}</FieldLabel>
				<Controller
					control={control}
					name="llm.model"
					render={({ field }) => (
						<ModelSelector
							inputId="llm-model"
							data-testid="complexity-router-llm-model-select"
							provider={llm?.provider || undefined}
							keys={providerKeyIds}
							value={field.value ?? ""}
							onChange={(model) => {
								field.onChange(model);
							}}
							allowCustomModel
							placeholder={llm?.provider ? tcpx("searchChatModel") : tcpx("selectProviderFirst")}
							disabled={!canUpdate || !llm?.provider}
						/>
					)}
				/>
				{errors?.model ? <p className="text-destructive text-xs">{errors.model.message}</p> : null}
			</div>

			<div className="grid gap-4 sm:grid-cols-2">
				<div className="space-y-2">
					<FieldLabel htmlFor="llm-timeout" tooltip={tcpx("classificationTimeoutTip")}>
						{tcpx("classificationTimeout")}
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
								aria-invalid={errors?.timeout ? true : undefined}
								className={cn("font-mono", errors?.timeout && "border-destructive focus-visible:ring-destructive")}
							/>
						)}
					/>
					{errors?.timeout && <p className="text-destructive text-xs">{errors.timeout.message}</p>}
				</div>

				<div className="space-y-2">
					<FieldLabel htmlFor="llm-message-history" tooltip={tcpx("maxMessagesSendTip")}>
						{tcpx("maxMessagesSend")}
					</FieldLabel>
					<Input
						id="llm-message-history"
						data-testid="complexity-router-llm-message-history-input"
						type="number"
						min={MIN_LLM_MESSAGE_HISTORY}
						max={MAX_LLM_MESSAGE_HISTORY}
						step={1}
						disabled={!canUpdate || !isLLMConfigured}
						aria-invalid={errors?.message_history_count ? true : undefined}
						className={cn("font-mono", errors?.message_history_count && "border-destructive focus-visible:ring-destructive")}
						{...register("llm.message_history_count", { valueAsNumber: true })}
					/>
					{errors?.message_history_count && <p className="text-destructive text-xs">{errors.message_history_count.message}</p>}
				</div>
			</div>

			<div className="flex items-center justify-between gap-6 border-t pt-4">
				<FieldLabel htmlFor="llm-count-toward-budgets" tooltip={tcpx("countClassificationBudgetsTip")}>
					{tcpx("countClassificationBudgets")}
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
	);
}