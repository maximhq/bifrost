import { Label } from "@/components/ui/label";
import { ProviderConfigCard, ProviderConfigCardValue } from "@/components/ui/providerConfigCard";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderLabels, ProviderName } from "@/lib/constants/logs";
import { useGetAllKeysQuery, useGetProvidersQuery } from "@/lib/store";
import { KnownProvider } from "@/lib/types/config";
import { Info } from "lucide-react";
import { useEffect, useState } from "react";

// Shared provider-configuration editor for the Virtual Key (core) and Access
// Profile / Project (enterprise) forms. It owns the "Allow all providers" toggle,
// the add-provider dropdown, and the per-provider cards; callers drive it with the
// card's normalized ProviderConfigCardValue and adapt their own storage shape.
export interface ProviderConfigsEditorProps {
	value: ProviderConfigCardValue[];
	onChange: (next: ProviderConfigCardValue[]) => void;
	allowAllProviders: boolean;
	onAllowAllProvidersChange: (checked: boolean) => void;
	/** Prefix for data-testids, e.g. "vk" or "ap". */
	testIdPrefix?: string;
	/** Show the per-model budgets tree inside each card. Defaults to true. */
	showModelBudgets?: boolean;
	/** Read-only global provider cap per provider, shown on the Provider budget row. */
	getGlobalProviderCap?: (providerName: string) => { max_limit: number; reset_duration?: string } | undefined;
	/** When set, the "no providers left" dropdown row becomes a clickable link (e.g. route to provider management). */
	onManageProviders?: () => void;
	/** Validation error rendered under the list. */
	error?: string;
}

// An unrestricted row: all models, all keys, no budgets/limits.
const makeDefaultEntry = (providerName: string): ProviderConfigCardValue => ({
	providerName,
	allowedModels: ["*"],
	blacklistedModels: [],
	weight: undefined,
	keyIds: ["*"],
	budgets: [],
	rateLimit: null,
	modelBudgets: [],
});

// A row is "untouched" while it still carries only the defaults above, so turning
// "Allow all providers" off can drop it while keeping customized rows.
const isDefaultEntry = (e: ProviderConfigCardValue): boolean =>
	(e.allowedModels || []).length === 1 &&
	e.allowedModels?.[0] === "*" &&
	(e.blacklistedModels || []).length === 0 &&
	(e.keyIds || []).length === 1 &&
	e.keyIds?.[0] === "*" &&
	(e.budgets || []).length === 0 &&
	!e.rateLimit &&
	(e.modelBudgets || []).length === 0 &&
	(e.weight === undefined || e.weight === null);

export function ProviderConfigsEditor({
	value,
	onChange,
	allowAllProviders,
	onAllowAllProvidersChange,
	testIdPrefix = "provider",
	showModelBudgets = true,
	getGlobalProviderCap,
	onManageProviders,
	error,
}: ProviderConfigsEditorProps) {
	const { data: providersData, isLoading: isLoadingProviders, isError: isProvidersError } = useGetProvidersQuery();
	const { data: keysData } = useGetAllKeysQuery();
	const availableProviders = providersData || [];
	// null = the keys query is still loading or failed (the card keeps its key
	// control visible); [] = loaded, none exist.
	const availableKeys = keysData ?? null;
	const [selectedProvider, setSelectedProvider] = useState("");

	const handleAddProvider = (providerName: string) => {
		if (!providerName || value.some((e) => e.providerName === providerName)) return;
		onChange([...value, makeDefaultEntry(providerName)]);
	};

	const handleRemoveProvider = (index: number) => {
		// Removing a provider while "Allow all providers" is on means "all except this
		// one": turn the flag off so the remaining rows become the explicit allowlist.
		if (allowAllProviders) onAllowAllProvidersChange(false);
		onChange(value.filter((_, i) => i !== index));
	};

	const handleAllowAllChange = (checked: boolean) => {
		onAllowAllProvidersChange(checked);
		// Turning it off keeps customized rows and drops the untouched defaults.
		if (!checked) onChange(value.filter((e) => !isDefaultEntry(e)));
	};

	// While on, keep a row for every available provider so each can be given
	// budgets/limits or excluded. Providers added later show up here too. Keyed on
	// the joined names, not the array identity, so it converges without looping.
	const providerNamesKey = availableProviders.map((p) => p.name).join(" ");
	useEffect(() => {
		if (!allowAllProviders || availableProviders.length === 0) return;
		const missing = availableProviders.filter((p) => p.name && !value.some((e) => e.providerName === p.name));
		if (missing.length === 0) return;
		onChange([...value, ...missing.map((p) => makeDefaultEntry(p.name))]);
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [allowAllProviders, providerNamesKey]);

	const unconfiguredProviders = availableProviders.filter((provider) => !value.some((e) => e.providerName === provider.name));
	const baseProviders = unconfiguredProviders.filter((p) => p.name && !p.custom_provider_config);
	const customProviders = unconfiguredProviders.filter((p) => p.name && p.custom_provider_config);

	return (
		<div className="space-y-2">
			<div className="flex items-center gap-2">
				<Label className="text-sm font-medium">Provider Configurations</Label>
				<TooltipProvider>
					<Tooltip>
						<TooltipTrigger asChild>
							<span>
								<Info className="text-muted-foreground h-3 w-3" />
							</span>
						</TooltipTrigger>
						<TooltipContent className="max-w-sm">
							<p>
								Configure which providers this can use and their specific settings. Leave empty to block all providers. Add providers to
								allow them.
							</p>
						</TooltipContent>
					</Tooltip>
				</TooltipProvider>
			</div>

			{/* Allow all providers */}
			<div className="flex w-full items-center justify-between gap-2 py-2 text-sm">
				<div className="flex items-center gap-1.5">
					<span>Allow all providers</span>
					<TooltipProvider>
						<Tooltip>
							<TooltipTrigger asChild>
								<span>
									<Info className="text-muted-foreground h-3 w-3" />
								</span>
							</TooltipTrigger>
							<TooltipContent className="max-w-sm">
								<p>
									Grant access to every provider, including ones added later. Set budgets or limits on specific providers below, or remove a
									provider to allow all except that one.
								</p>
							</TooltipContent>
						</Tooltip>
					</TooltipProvider>
				</div>
				<Switch
					checked={allowAllProviders}
					onCheckedChange={handleAllowAllChange}
					data-testid={`${testIdPrefix}-allow-all-providers-toggle`}
				/>
			</div>

			{/* Add Provider Dropdown */}
			<div className="flex gap-2">
				<Select
					value={selectedProvider}
					onValueChange={(provider) => {
						if (provider === "__manage_providers__") {
							onManageProviders?.();
							setSelectedProvider("");
							return;
						}
						handleAddProvider(provider);
						setSelectedProvider("");
					}}
				>
					<SelectTrigger className="flex-1" data-testid={`${testIdPrefix}-provider-select`}>
						<SelectValue placeholder="Select a provider to add" />
					</SelectTrigger>
					<SelectContent>
						{isLoadingProviders ? (
							<div className="text-muted-foreground px-2 py-1.5 text-sm">Loading providers...</div>
						) : isProvidersError ? (
							<div className="text-destructive px-2 py-1.5 text-sm">Failed to load providers. Please retry.</div>
						) : unconfiguredProviders.length === 0 ? (
							onManageProviders ? (
								<SelectItem
									value="__manage_providers__"
									className="text-muted-foreground hover:text-foreground"
									data-testid={`${testIdPrefix}-provider-config-link`}
								>
									<span>
										No providers left to configure. <span className="text-primary font-medium underline">Click to add</span>
									</span>
								</SelectItem>
							) : (
								<div className="text-muted-foreground px-2 py-1.5 text-sm">No providers left to configure</div>
							)
						) : (
							<>
								{baseProviders.map((provider, index) => (
									<SelectItem key={`base-${index}`} value={provider.name}>
										<RenderProviderIcon provider={provider.name as KnownProvider} size="sm" className="h-4 w-4" />
										{ProviderLabels[provider.name as ProviderName] || provider.name}
									</SelectItem>
								))}
								{customProviders.map((provider, index) => (
									<SelectItem key={`custom-${index}`} value={provider.name}>
										<RenderProviderIcon
											provider={provider.custom_provider_config?.base_provider_type || (provider.name as KnownProvider)}
											size="sm"
											className="h-4 w-4"
										/>
										{provider.name}
									</SelectItem>
								))}
							</>
						)}
					</SelectContent>
				</Select>
			</div>

			{/* Provider cards */}
			{value.length > 0 && (
				<div className="space-y-2.5">
					{value.map((entry, index) => {
						const providerConfig = availableProviders.find((provider) => provider.name === entry.providerName);
						const providerLabel = providerConfig?.custom_provider_config
							? providerConfig.name
							: ProviderLabels[entry.providerName as ProviderName] || entry.providerName;
						const iconProvider = (providerConfig?.custom_provider_config?.base_provider_type || entry.providerName) as ProviderIconType;
						const providerKeys = availableKeys === null ? null : availableKeys.filter((key) => key.provider === entry.providerName);
						return (
							<ProviderConfigCard
								key={entry.providerName}
								index={index}
								testIdPrefix={testIdPrefix}
								providerLabel={providerLabel}
								iconProvider={iconProvider}
								providerKeys={providerKeys}
								showModelBudgets={showModelBudgets}
								globalProviderCap={getGlobalProviderCap?.(entry.providerName)}
								onRemove={() => handleRemoveProvider(index)}
								value={entry}
								onChange={(next) => {
									const updated = [...value];
									updated[index] = next;
									onChange(updated);
								}}
							/>
						);
					})}
				</div>
			)}
			{error && <div className="text-destructive text-sm">{error}</div>}
		</div>
	);
}