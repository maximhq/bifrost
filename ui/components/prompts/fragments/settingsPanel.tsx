import { ComboboxSelect } from "@/components/ui/combobox";
import { Label } from "@/components/ui/label";
import { ScrollArea } from "@/components/ui/scrollArea";
import { Separator } from "@/components/ui/separator";
import ModelParameters from "@/components/ui/custom/modelParameters";
import { ModelParams } from "@/lib/types/prompts";
import { getProviderLabel } from "@/lib/constants/logs";
import { useGetAllKeysQuery, useGetProvidersQuery } from "@/lib/store/apis/providersApi";
import { useGetVirtualKeysQuery } from "@/lib/store";
import { useCallback, useMemo } from "react";
import { ModelProviderName } from "@/lib/types/config";
import { ModelMultiselect } from "@/components/ui/modelMultiselect";
import { Skeleton } from "@/components/ui/skeleton";
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
	const { data: providers, isLoading: isLoadingProviders } = useGetProvidersQuery();
	const { data: virtualKeysData } = useGetVirtualKeysQuery();
	// Keys for the API Key selector (from /api/keys endpoint, provider-filtered)
	const { data: allKeys, isSuccess: hasLoadedAllKeys } = useGetAllKeysQuery();

	const isInitialLoading = isLoadingProviders;

	const configuredProviders = useMemo(() => {
		const activeVirtualKeys = virtualKeysData?.virtual_keys?.filter((vk) => vk.is_active) ?? [];
		if (!hasLoadedAllKeys) {
			return providers ?? [];
		}
		const keyedProviders = new Set((allKeys ?? []).map((k) => k.provider));
		return (providers ?? []).filter((p) => {
			if (keyedProviders.has(p.name)) return true;
			// Include providers that have active virtual keys (wildcard or explicitly targeting this provider)
			return activeVirtualKeys.some(
				(vk) => !vk.provider_configs || vk.provider_configs.length === 0 || vk.provider_configs.some((pc) => pc.provider === p.name),
			);
		});
	}, [providers, virtualKeysData, allKeys, hasLoadedAllKeys]);

	// Ensure current provider always has a label-resolved option (even before providers query loads)
	const providerOptions = useMemo(() => {
		const opts = configuredProviders.map((p) => ({ label: getProviderLabel(p.name), value: p.name }));
		if (provider && !opts.find((o) => o.value === provider)) {
			opts.unshift({ label: getProviderLabel(provider), value: provider as ModelProviderName });
		}
		return opts;
	}, [configuredProviders, provider]);

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

	// Keys to pass to model fetch for filtering.
	// Backend accepts provider key UUIDs or virtual key values.
	const modelFilterKeys = useMemo(() => {
		const isProviderKey = providerKeys.some((k) => k.key_id === apiKeyId);
		if (isProviderKey) return [apiKeyId];
		const isVirtualKey = providerVirtualKeys.some((vk) => vk.value === apiKeyId);
		if (isVirtualKey) return [apiKeyId];
		// Auto: pass all provider key IDs so API returns union of accessible models
		return providerKeys.map((k) => k.key_id);
	}, [apiKeyId, providerKeys, providerVirtualKeys]);

	const handleModelParamsChange = useCallback(
		(params: Record<string, any>) => {
			onModelParamsChange(params as ModelParams);
		},
		[onModelParamsChange],
	);

	if (isInitialLoading) {
		return (
			<div className="flex h-full flex-col">
				<div className="space-y-6 p-4">
					<div className="flex flex-col gap-2">
						<Skeleton className="h-4 w-16" />
						<Skeleton className="h-9 w-full rounded-sm" />
					</div>
					<div className="flex flex-col gap-2">
						<Skeleton className="h-4 w-12" />
						<Skeleton className="h-9 w-full rounded-sm" />
					</div>
				</div>
			</div>
		);
	}

	return (
		<div className="flex h-full flex-col">
			<ScrollArea className="grow overflow-y-auto" viewportClassName="no-table">
				<div className="space-y-6 p-4">
					<div className="flex flex-col gap-2" data-testid="settings-provider">
						<Label className="text-muted-foreground text-xs font-medium uppercase">Provider</Label>
						<ComboboxSelect
							options={providerOptions}
							value={provider}
							onValueChange={(v) => v && onProviderChange(v)}
							placeholder="Select provider"
							hideClear
						/>
					</div>

					<div className="flex flex-col gap-2" data-testid="settings-model">
						<Label className="text-muted-foreground text-xs font-medium uppercase">Model</Label>
						<ModelMultiselect
							provider={provider}
							keys={modelFilterKeys.length > 0 ? modelFilterKeys : undefined}
							value={model}
							onChange={(v) => onModelChange(v)}
							isSingleSelect
							placeholder={!provider ? "Select a provider first" : "Select model"}
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
