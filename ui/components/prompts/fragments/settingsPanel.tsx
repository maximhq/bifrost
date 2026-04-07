import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from "@/components/ui/accordion";
import { ComboboxSelect } from "@/components/ui/combobox";
import ModelParameters from "@/components/ui/custom/modelParameters";
import { Label } from "@/components/ui/label";
import { ModelMultiselect } from "@/components/ui/modelMultiselect";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import { getProviderLabel } from "@/lib/constants/logs";
import { useGetVirtualKeysQuery } from "@/lib/store";
import { useGetAllKeysQuery, useGetProvidersQuery } from "@/lib/store/apis/providersApi";
import { ModelProviderName } from "@/lib/types/config";
import { ModelParams } from "@/lib/types/prompts";
import { cn } from "@/lib/utils";
import { PromptDeploymentsAccordionItem } from "@enterprise/components/prompt-deployments/promptDeploymentsAccordionItem";
import { useCallback, useMemo, useState } from "react";
import { ApiKeySelectorView } from "../components/apiKeySelectorView";
import { VariablesTableView } from "../components/variablesTableView";
import { usePromptContext } from "../context";

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
		selectedPromptId,
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

	// Separate keys/vks to pass to model fetch for filtering.
	const filterKeys = useMemo(() => {
		const isProviderKey = providerKeys.some((k) => k.key_id === apiKeyId);
		if (isProviderKey) return [apiKeyId];
		const isVirtualKey = providerVirtualKeys.some((vk) => vk.id === apiKeyId);
		if (isVirtualKey) return undefined;
		// Auto: pass all provider key IDs
		return providerKeys.map((k) => k.key_id);
	}, [apiKeyId, providerKeys, providerVirtualKeys]);

	const filterVks = useMemo(() => {
		const isVirtualKey = providerVirtualKeys.some((vk) => vk.id === apiKeyId);
		if (isVirtualKey) return [apiKeyId];
		return undefined;
	}, [apiKeyId, providerVirtualKeys]);

	const handleModelParamsChange = useCallback(
		(params: Record<string, any>) => {
			onModelParamsChange(params as ModelParams);
		},
		[onModelParamsChange],
	);

	const hasModel = Boolean(model);

	type SettingsSection = "parameters" | "deployments";
	const [openSection, setOpenSection] = useState<SettingsSection | undefined>("parameters");

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
		<div className="flex h-full min-h-0 flex-col">
			<div className="flex min-h-0 flex-1 flex-col px-4 pb-4 pt-2">
				<Accordion
					type="single"
					collapsible
					value={openSection ?? ""}
					onValueChange={(v) => {
						if (v === "parameters" || v === "deployments") {
							setOpenSection(v);
						} else {
							setOpenSection(undefined);
						}
					}}
					className="flex min-h-0 flex-1 flex-col"
				>
					<AccordionItem
						value="parameters"
						className={cn(
							"flex min-h-0 flex-col border-b-0",
							openSection === "parameters" ? "flex-1" : "shrink-0 overflow-hidden",
						)}
					>
						<AccordionTrigger data-testid="prompts-configuration-trigger" className="text-muted-foreground shrink-0 py-3 pr-1 text-xs font-medium uppercase hover:no-underline">
							<span className="min-w-0 flex-1 text-left font-semibold">Configuration</span>
						</AccordionTrigger>
						<AccordionContent
							containerClassName="data-[state=open]:flex data-[state=open]:min-h-0 data-[state=open]:flex-1 data-[state=open]:flex-col"
							className="min-h-0 flex-1 overflow-y-auto pb-2 pt-0"
						>
							<div className="space-y-6">
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
										keys={filterKeys && filterKeys.length > 0 ? filterKeys : undefined}
										vks={filterVks}
										value={model}
										onChange={(v) => onModelChange(v)}
										isSingleSelect
										placeholder={!provider ? "Select a provider first" : "Select model"}
										disabled={!provider}
										unfiltered={true}
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

								{Object.keys(variables).length > 0 && (
									<>
										<Separator />
										<VariablesTableView variables={variables} onChange={setVariables} />
									</>
								)}

								{hasModel && (
									<>
										<Separator />
										<div className="flex flex-col gap-4">
											<ModelParameters
												model={model}
												config={modelParams}
												onChange={handleModelParamsChange}
												hideFields={["promptTools"]}
											/>
										</div>
									</>
								)}
							</div>
						</AccordionContent>
					</AccordionItem>
					{selectedPromptId && <PromptDeploymentsAccordionItem activeSection={openSection} />}
				</Accordion>
			</div>
		</div>
	);
}