import { Input } from "@/components/ui/input";
import { ModelSelector } from "@/components/ui/modelSelector";
import { ProviderSelector } from "@/components/ui/providerSelector";
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useGetModelsQuery } from "@/lib/store/apis/providersApi";
import type { ModelProvider } from "@/lib/types/config";
import {
	clefModelFromProvider,
	decisionProviderIconKey,
	defaultDecisionModel,
	isDecisionProvider,
	listsDecisionModels,
	selfHostedModelGroups,
} from "@/lib/utils/decisionModelProviders";
import { useEffect, useMemo } from "react";

// The decision-model pickers. Every surface that picks a decision model (the
// Complexity Router, Prompt Guardrails' decision judge) uses these, so what
// counts as a decision provider and which models it offers stays one decision.

interface DecisionProviderSelectorProps {
	value: string;
	// Every configured provider; the selector offers only decision providers. A
	// newly picked provider is handed back with its default decision model, since
	// a model name only means something on its own provider.
	providers: ModelProvider[];
	onChange: (provider: string, defaultModel: string) => void;
	disabled?: boolean;
	inputId?: string;
	"data-testid"?: string;
}

// DecisionProviderSelector lists the providers that answer decisions natively,
// with the Laya, Nimble, and Clef logos on custom providers that serve them.
export function DecisionProviderSelector({
	value,
	providers,
	onChange,
	disabled,
	inputId,
	"data-testid": testId,
}: DecisionProviderSelectorProps) {
	return (
		<ProviderSelector
			inputId={inputId}
			data-testid={testId}
			filter={isDecisionProvider}
			resolveIconKey={decisionProviderIconKey}
			value={value}
			onChange={(next: string) => {
				if (next === value) return;
				onChange(next, defaultDecisionModel(providers.find((candidate) => candidate.name === next)));
			}}
			disabled={disabled}
		/>
	);
}

interface DecisionModelSelectorProps {
	providerName: string;
	// The selected provider's configuration, when it is configured.
	provider: ModelProvider | undefined;
	value: string;
	onChange: (model: string) => void;
	// Ids of the provider's enabled keys, narrowing its model list to what those keys may serve.
	keys?: string[];
	disabled?: boolean;
	inputId?: string;
	"data-testid"?: string;
	// Test id of the read-only field shown when the provider fixes the model; defaults to data-testid.
	fixedModelTestId?: string;
	className?: string;
}

// DecisionModelSelector follows the provider: providers whose catalogs mix in
// chat models (OpenAI, OpenRouter, Cloudflare, Perplexity, Ollama, Azure, Databricks)
// list only the models the datasheet marks as decision models, Typesafe offers Jev
// releases, a Cloudflare URL on a custom provider fixes its Clef model, and a
// self-hosted provider offers the models it lists, or the known Laya, Nimble,
// and Clef checkpoints when it lists none.
export function DecisionModelSelector({
	providerName,
	provider,
	value,
	onChange,
	keys = [],
	disabled,
	inputId,
	"data-testid": testId,
	fixedModelTestId = testId,
	className,
}: DecisionModelSelectorProps) {
	const listsDecisions = listsDecisionModels(provider, providerName);
	const servesJev = providerName === "typesafe";
	const fixedModel = clefModelFromProvider(provider) !== undefined;
	const selfHosted = provider !== undefined && !servesJev && !listsDecisions && !fixedModel;

	// currentData, not data: data keeps the previous provider's list while the new one loads.
	const { currentData: listed } = useGetModelsQuery(
		{ provider: providerName, keys: keys.length > 0 ? keys : undefined, limit: 50 },
		{ skip: !selfHosted },
	);
	const listedModels = useMemo(() => (listed?.models ?? []).map((entry) => entry.name), [listed]);
	// A self-hosted provider that lists its models starts on the first one.
	useEffect(() => {
		if (selfHosted && !value && listedModels.length > 0) onChange(listedModels[0]);
	}, [selfHosted, value, listedModels, onChange]);

	if (listsDecisions) {
		return (
			<ModelSelector
				inputId={inputId}
				data-testid={testId}
				hideSearchIcon
				openWithEmptySearch
				provider={providerName}
				keys={keys}
				decisions
				value={value}
				onChange={onChange}
				placeholder="Search decision models…"
				emptyMessage="This provider lists no decision models"
				disabled={disabled}
				className={className}
			/>
		);
	}
	if (servesJev || !provider) {
		return (
			<ModelSelector
				inputId={inputId}
				data-testid={testId}
				hideSearchIcon
				openWithEmptySearch
				provider={providerName || undefined}
				keys={keys}
				value={value}
				onChange={onChange}
				allowCustomModel
				placeholder={providerName ? "Search or type a Jev model…" : "Select a provider first"}
				disabled={disabled || !providerName}
				className={className}
			/>
		);
	}
	if (fixedModel) {
		// Cloudflare binds the model to the provider's URL and rejects any other in the
		// body, so it is shown, not chosen: selecting the provider fills it in.
		return <Input id={inputId} data-testid={fixedModelTestId} value={value} readOnly disabled className="font-mono" />;
	}
	if (listedModels.length > 0) {
		// A listing is one page of a possibly longer catalog, so search and typed
		// names stay available instead of a fixed list that hides later models.
		return (
			<ModelSelector
				inputId={inputId}
				data-testid={testId}
				hideSearchIcon
				openWithEmptySearch
				provider={providerName || undefined}
				keys={keys}
				value={value}
				onChange={onChange}
				allowCustomModel
				placeholder="Search or type a model…"
				disabled={disabled}
				className={className}
			/>
		);
	}
	return (
		<Select value={value || undefined} onValueChange={onChange} disabled={disabled}>
			<SelectTrigger className="w-full" id={inputId} data-testid={testId}>
				<SelectValue placeholder="Select a model" />
			</SelectTrigger>
			<SelectContent>
				{selfHostedModelGroups(providerName).map((group) => (
					<SelectGroup key={group.label}>
						<SelectLabel>{group.label}</SelectLabel>
						{group.models.map((name) => (
							<SelectItem key={name} value={name}>
								{name}
							</SelectItem>
						))}
					</SelectGroup>
				))}
			</SelectContent>
		</Select>
	);
}