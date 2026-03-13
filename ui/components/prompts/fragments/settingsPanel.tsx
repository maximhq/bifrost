import { ComboboxSelect } from "@/components/ui/combobox";
import { Label } from "@/components/ui/label";
import { ScrollArea } from "@/components/ui/scrollArea";
import { Separator } from "@/components/ui/separator";
import ModelParameters from "@/components/ui/custom/modelParameters";
import { ModelParams } from "@/lib/types/prompts";
import { getProviderLabel } from "@/lib/constants/logs";
import { useGetAllKeysQuery, useGetProvidersQuery, useLazyGetModelsQuery } from "@/lib/store/apis/providersApi";
import { useGetVirtualKeysQuery } from "@/lib/store";
import { useCallback, useEffect, useMemo } from "react";
import { ModelProviderName } from "@/lib/types/config";
import { usePromptContext } from "../context";
import { VariablesTableView } from "../components/variablesTableView";
import { ApiKeySelectorView } from "../components/apiKeySelectorView";

export function SettingsPanel() {
	const {
		provider,
		setProvider,
		model,
		setModel: onModelChange,
		modelParams,
		setModelParams: onModelParamsChange,
		apiKeyId,
		setApiKeyId,
		variables,
		setVariables,
	} = usePromptContext();

	const onProviderChange = useCallback(
		(p: string) => {
			setProvider(p);
			setApiKeyId("__auto__");
			onModelChange("");
			onModelParamsChange({} as ModelParams);
		},
		[setProvider, setApiKeyId, onModelChange, onModelParamsChange],
	);

	const onApiKeyIdChange = useCallback(
		(id: string) => {
			setApiKeyId(id);
		},
		[setApiKeyId],
	);
	// Dynamic providers
	const { data: providers } = useGetProvidersQuery();
	const { data: virtualKeysData } = useGetVirtualKeysQuery();
	const configuredProviders = useMemo(() => {
		const activeVirtualKeys = virtualKeysData?.virtual_keys?.filter((vk) => vk.is_active) ?? [];
		return (providers ?? []).filter((p) => {
			if (p.keys && p.keys.length > 0) return true;
			// Include providers that have active virtual keys (wildcard or explicitly targeting this provider)
			return activeVirtualKeys.some(
				(vk) => !vk.provider_configs || vk.provider_configs.length === 0 || vk.provider_configs.some((pc) => pc.provider === p.name),
			);
		});
	}, [providers, virtualKeysData]);

	// Ensure current provider always has a label-resolved option (even before providers query loads)
	const providerOptions = useMemo(() => {
		const opts = configuredProviders.map((p) => ({ label: getProviderLabel(p.name), value: p.name }));
		if (provider && !opts.find((o) => o.value === provider)) {
			opts.unshift({ label: getProviderLabel(provider), value: provider as ModelProviderName });
		}
		return opts;
	}, [configuredProviders, provider]);

	// Get keys from the provider config (has models[] per key)
	const selectedProvider = useMemo(() => configuredProviders.find((p) => p.name === provider), [configuredProviders, provider]);
	const providerKeyConfigs = useMemo(() => selectedProvider?.keys ?? [], [selectedProvider]);

	// Keys for the API Key selector (from /api/keys endpoint, provider-filtered)
	const { data: allKeys } = useGetAllKeysQuery();
	const providerKeys = useMemo(() => (allKeys ?? []).filter((k) => k.provider === provider), [allKeys, provider]);

	// Virtual keys filtered by selected provider
	const providerVirtualKeys = useMemo(() => {
		const vks = virtualKeysData?.virtual_keys ?? [];
		return vks.filter((vk) => {
			if (!vk.is_active) return false;
			// No provider configs means all providers are allowed (wildcard)
			if (!vk.provider_configs || vk.provider_configs.length === 0) return true;
			// Check if selected provider is in the configured providers
			return vk.provider_configs.some((pc) => pc.provider === provider);
		});
	}, [virtualKeysData, provider]);

	// Fallback: fetch all models for this provider (used when any key has no models restriction)
	const [fetchModels, { data: modelsData }] = useLazyGetModelsQuery();
	useEffect(() => {
		if (provider) {
			fetchModels({ provider, limit: 100, unfiltered: true });
		}
	}, [provider, fetchModels]);
	const allProviderModels = useMemo(() => (modelsData?.models ?? []).map((m) => m.name), [modelsData]);

	// Build model list based on key selection
	const availableModels = useMemo(() => {
		if (apiKeyId !== "__auto__") {
			// Specific key selected — find it in provider config
			const key = providerKeyConfigs.find((k) => k.id === apiKeyId);
			if (key?.models && key.models.length > 0) {
				return key.models;
			}
			// Key has no model restriction → show all
			return allProviderModels;
		}

		// Auto mode — blend models from all keys
		// If any key has empty models (no restriction), show all models
		const hasUnrestrictedKey = providerKeyConfigs.some((k) => !k.models || k.models.length === 0);
		if (hasUnrestrictedKey || providerKeyConfigs.length === 0) {
			return allProviderModels;
		}

		// All keys have specific models — show unique union
		const modelSet = new Set<string>();
		for (const k of providerKeyConfigs) {
			for (const m of k.models ?? []) {
				modelSet.add(m);
			}
		}
		return Array.from(modelSet);
	}, [apiKeyId, providerKeyConfigs, allProviderModels]);

	const handleModelParamsChange = useCallback(
		(params: Record<string, any>) => {
			onModelParamsChange(params as ModelParams);
		},
		[onModelParamsChange],
	);

	return (
		<div className="flex h-full flex-col">
			<ScrollArea className="grow overflow-y-auto" viewportClassName="no-table">
				<div className="space-y-6 p-4">
					<div className="flex flex-col gap-2">
						<Label className="text-muted-foreground text-xs font-medium uppercase">Provider</Label>
						<ComboboxSelect
							options={providerOptions}
							value={provider}
							onValueChange={(v) => v && onProviderChange(v)}
							placeholder="Select provider"
							hideClear
						/>
					</div>

					<div className="flex flex-col gap-2">
						<Label className="text-muted-foreground text-xs font-medium uppercase">Model</Label>
						<ComboboxSelect
							options={availableModels.map((m) => ({ label: m, value: m }))}
							value={model}
							onValueChange={(v) => v && onModelChange(v)}
							placeholder={!provider ? "Select a provider first" : "Select model"}
							hideClear
							disabled={!provider}
						/>
					</div>

					{(providerKeys.length > 0 || providerVirtualKeys.length > 0) && !!provider && (
						<ApiKeySelectorView
							providerKeys={providerKeys}
							virtualKeys={providerVirtualKeys}
							value={apiKeyId}
							onValueChange={(v) => onApiKeyIdChange(v ?? "__auto__")}
							disabled={!provider}
						/>
					)}

					{/* {Object.keys(variables).length > 0 && (
						<>
							<Separator />
							<VariablesTableView variables={variables} onChange={setVariables} />
						</>
					)} */}

					{model && (
						<>
							<Separator />

							<div className="flex flex-col gap-4">
								<Label className="text-muted-foreground text-xs font-medium uppercase">Model Parameters</Label>
								<ModelParameters model={model} config={modelParams} onChange={handleModelParamsChange} hideFields={["promptTools"]} />
							</div>
						</>
					)}
				</div>
			</ScrollArea>
		</div>
	);
}
