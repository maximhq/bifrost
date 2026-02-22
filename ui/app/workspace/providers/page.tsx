"use client";

import ModelProviderConfig from "@/app/workspace/providers/views/modelProviderConfig";
import FullPageLoader from "@/components/fullPageLoader";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { DefaultNetworkConfig, DefaultPerformanceConfig } from "@/lib/constants/config";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderLabels, ProviderNames } from "@/lib/constants/logs";
import {
	getErrorMessage,
	setSelectedProvider,
	useAppDispatch,
	useAppSelector,
	useGetProvidersQuery,
	useLazyGetProviderQuery,
} from "@/lib/store";
import { KnownProvider, ModelProviderName, ProviderStatus } from "@/lib/types/config";
import { cn } from "@/lib/utils";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { AlertCircle } from "lucide-react";
import { useRouter } from "next/navigation";
import { useQueryState } from "nuqs";
import { useEffect, useState } from "react";
import { toast } from "sonner";
import AddCustomProviderSheet from "./dialogs/addNewCustomProviderSheet";
import ConfirmDeleteProviderDialog from "./dialogs/confirmDeleteProviderDialog";
import ConfirmRedirectionDialog from "./dialogs/confirmRedirection";
import { AddProviderDropdown } from "./views/addProviderDropdown";
import { ProvidersEmptyState } from "./views/providersEmptyState";

export default function Providers() {
	const dispatch = useAppDispatch();
	const router = useRouter();
	const hasProvidersAccess = useRbac(RbacResource.ModelProvider, RbacOperation.View);
	const hasSettingsOnly = useRbac(RbacResource.Settings, RbacOperation.View);

	// Redirect Settings-only users to Custom pricing tab
	useEffect(() => {
		if (!hasProvidersAccess && hasSettingsOnly) {
			router.replace("/workspace/providers/custom-pricing");
		}
	}, [hasProvidersAccess, hasSettingsOnly, router]);

	const selectedProvider = useAppSelector((state) => state.provider.selectedProvider);
	const providerFormIsDirty = useAppSelector((state) => state.provider.isDirty);
	const hasProviderCreateAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Create);
	const hasProviderDeleteAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Delete);

	const [showRedirectionDialog, setShowRedirectionDialog] = useState(false);
	const [showDeleteProviderDialog, setShowDeleteProviderDialog] = useState(false);
	const [pendingRedirection, setPendingRedirection] = useState<string | undefined>(undefined);
	const [showCustomProviderSheet, setShowCustomProviderSheet] = useState(false);
	const [addedProviderNames, setAddedProviderNames] = useState<Set<string>>(new Set());
	const [provider, setProvider] = useQueryState("provider");

	const { data: savedProviders, isLoading: isLoadingProviders } = useGetProvidersQuery();
	const [getProvider, { isLoading: isLoadingProvider }] = useLazyGetProviderQuery();

	const allProviders = ProviderNames.map(
		(p) => savedProviders?.find((provider) => provider.name === p) ?? { name: p, keys: [], provider_status: "active" as ProviderStatus },
	).sort((a, b) => a.name.localeCompare(b.name));

	const popularProviderNames = ["openai", "bedrock", "azure", "gemini", "xai", "anthropic"] as const;
	const popularProviders = allProviders.filter((p) => popularProviderNames.includes(p.name as (typeof popularProviderNames)[number]));
	const standardProviders = allProviders.filter((p) => !popularProviderNames.includes(p.name as (typeof popularProviderNames)[number]));

	const isProviderConfigured = (p: (typeof allProviders)[number]) =>
		(p.keys?.length ?? 0) > 0 || (p.network_config?.base_url ?? "").trim() !== "";
	const configuredStandardProviders = allProviders.filter(isProviderConfigured);
	const unconfiguredPopular = popularProviders.filter((p) => !isProviderConfigured(p));
	const unconfiguredStandard = standardProviders.filter((p) => !isProviderConfigured(p));

	const customProviders =
		savedProviders
			?.filter((provider) => !ProviderNames.includes(provider.name as KnownProvider))
			.sort((a, b) => a.name.localeCompare(b.name)) ?? [];

	// Providers added from dropdown but not yet configured (keys/base_url); show in sidebar so user can configure
	const addedOnly = allProviders.filter(
		(p) => addedProviderNames.has(p.name) && !isProviderConfigured(p),
	);
	const configuredProviders = [...configuredStandardProviders, ...customProviders, ...addedOnly];
	// Stable string key derived from configured providers to avoid excessive useEffect firing
	const configuredProviderNames = configuredProviders.map((p) => p.name).join(",");
	const existingInSidebarNames = new Set(configuredProviders.map((p) => p.name));

	useEffect(() => {
		if (!provider) return;
		const newSelectedProvider = allProviders.find((p) => p.name === provider) ?? customProviders.find((p) => p.name === provider);
		if (newSelectedProvider) {
			dispatch(setSelectedProvider(newSelectedProvider));
		}
		// We also try to fetch the latest version
		getProvider(provider)
			.unwrap()
			.then((providerInfo) => {
				dispatch(setSelectedProvider(providerInfo));
			})
			.catch((err) => {
				if (err.status === 404) {
					// Initializing provider config with default values
					dispatch(
						setSelectedProvider({
							name: provider as ModelProviderName,
							keys: [],
							concurrency_and_buffer_size: DefaultPerformanceConfig,
							network_config: DefaultNetworkConfig,
							custom_provider_config: undefined,
							proxy_config: undefined,
							send_back_raw_request: undefined,
							send_back_raw_response: undefined,
							provider_status: "error",
						}),
					);
					return;
				}
				toast.error("Something went wrong", {
					description: `We encountered an error while getting provider config: ${getErrorMessage(err)}`,
				});
			});
		return;
	}, [provider, isLoadingProviders]);

	useEffect(() => {
		if (selectedProvider || !allProviders || allProviders.length === 0 || provider) return;
		setProvider(allProviders[0].name);
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [selectedProvider, allProviders]);

	// When current provider is no longer configured (e.g. all keys deleted), switch to another configured provider
	useEffect(() => {
		if (!provider || configuredProviderNames === "") return;
		const names = configuredProviderNames.split(",");
		const isCurrentConfigured = names.includes(provider);
		if (!isCurrentConfigured) {
			setProvider(names[0]);
		}
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [provider, configuredProviderNames]);

	if (!hasProvidersAccess && hasSettingsOnly) {
		return <FullPageLoader />;
	}
	if (isLoadingProviders) {
		return <FullPageLoader />;
	}

	const handleSelectKnownProvider = (name: string) => {
		setAddedProviderNames((prev) => new Set(prev).add(name));
		setTimeout(() => setProvider(name), 0);
	};

	if (configuredProviders.length === 0) {
		return (
			<div className="mx-auto w-full max-w-7xl">
				<ProvidersEmptyState
					addProviderDropdown={
						<AddProviderDropdown
							existingInSidebar={existingInSidebarNames}
							knownProviders={allProviders}
							onSelectKnownProvider={handleSelectKnownProvider}
							onAddCustomProvider={() => setShowCustomProviderSheet(true)}
							variant="empty"
						/>
					}
				/>
				<AddCustomProviderSheet
					show={showCustomProviderSheet}
					onClose={() => setShowCustomProviderSheet(false)}
					onSave={(providerName) => {
						setTimeout(() => setProvider(providerName), 300);
						setShowCustomProviderSheet(false);
					}}
				/>
			</div>
		);
	}

	return (
		<div className="flex h-full w-full flex-row gap-4">
			<ConfirmDeleteProviderDialog
				provider={selectedProvider!}
				show={showDeleteProviderDialog}
				onCancel={() => setShowDeleteProviderDialog(false)}
				onDelete={() => {
					const next = configuredProviders.filter((p) => p.name !== selectedProvider?.name)[0];
					setProvider(next?.name ?? null);
					setShowDeleteProviderDialog(false);
				}}
			/>
			<ConfirmRedirectionDialog
				show={showRedirectionDialog}
				onCancel={() => setShowRedirectionDialog(false)}
				onContinue={() => {
					setShowRedirectionDialog(false);
					if (pendingRedirection) setProvider(pendingRedirection);
					setPendingRedirection(undefined);
				}}
			/>
			<AddCustomProviderSheet
				show={showCustomProviderSheet}
				onClose={() => setShowCustomProviderSheet(false)}
				onSave={(providerName) => {
					setTimeout(() => setProvider(providerName), 300);
					setShowCustomProviderSheet(false);
				}}
			/>
			<div className="flex flex-col" style={{ maxHeight: "calc(100vh - 70px)", width: "300px" }}>
				<TooltipProvider>
					<div className="custom-scrollbar flex-1 overflow-y-auto">
						<div className="rounded-md bg-zinc-50/50 p-4 dark:bg-zinc-800/20">
							{/* Configured Providers (standard with keys + custom) */}
							{configuredProviders.length > 0 && (
								<div className="mb-4">
									<div className="text-muted-foreground mb-2 text-xs font-medium">Configured Providers</div>
									{configuredProviders.map((p) => {
										const isCustom = !ProviderNames.includes(p.name as KnownProvider);
										return (
											<Tooltip key={p.name}>
												<TooltipTrigger
													data-testid={`provider-${p.name}`}
													className={cn(
														"mb-1 flex h-8 w-full items-center gap-2 rounded-sm border px-3 text-sm",
														selectedProvider?.name === p.name
															? "bg-secondary opacity-100 hover:opacity-100"
															: "hover:bg-secondary cursor-pointer border-transparent opacity-100 hover:border",
													)}
													onClick={(e) => {
														e.preventDefault();
														e.stopPropagation();
														if (providerFormIsDirty) {
															setPendingRedirection(p.name);
															setShowRedirectionDialog(true);
															return;
														}
														setProvider(p.name);
													}}
													asChild
												>
													<div className="flex w-full items-center gap-2">
														<RenderProviderIcon
															provider={(isCustom ? p.custom_provider_config?.base_provider_type : p.name) as ProviderIconType}
															size="sm"
															className="h-4 w-4"
														/>
														<div className="text-sm">
															{isCustom ? p.name : ProviderLabels[p.name as keyof typeof ProviderLabels]}
														</div>
														<KeyDiscoveryFailedBadge provider={p} />
														<ProviderStatusBadge status={p.provider_status} />
														{isCustom && (
															<Badge variant="secondary" className="ml-auto px-1.5 py-0.5 text-[10px] font-bold text-muted-foreground">
																CUSTOM
															</Badge>
														)}
													</div>
												</TooltipTrigger>
											</Tooltip>
										);
									})}
								</div>
							)}
							<div className="pb-4">
								<AddProviderDropdown
									existingInSidebar={existingInSidebarNames}
									knownProviders={allProviders}
									onSelectKnownProvider={handleSelectKnownProvider}
									onAddCustomProvider={() => setShowCustomProviderSheet(true)}
								/>
							</div>
						</div>
					</div>
				</TooltipProvider>
			</div>
			{isLoadingProvider && (
				<div className="bg-muted/10 flex w-full items-center justify-center rounded-md" style={{ maxHeight: "calc(100vh - 300px)" }}>
					<FullPageLoader />
				</div>
			)}
			{!selectedProvider && (
				<div className="bg-muted/10 flex w-full items-center justify-center rounded-md" style={{ maxHeight: "calc(100vh - 300px)" }}>
					<div className="text-muted-foreground text-sm">Select a provider</div>
				</div>
			)}
			{!isLoadingProvider && selectedProvider && (
				<ModelProviderConfig
					provider={selectedProvider}
					onRequestDelete={() => setShowDeleteProviderDialog(true)}
				/>
			)}
		</div>
	);
}

function ProviderStatusBadge({ status }: { status: ProviderStatus }) {
	return status != "active" ? (
		<Tooltip>
			<TooltipTrigger>
				<AlertCircle className="h-3 w-3" />
			</TooltipTrigger>
			<TooltipContent>{status === "error" ? "Provider could not be initialized" : "Provider is deleted"}</TooltipContent>
		</Tooltip>
	) : null;
}

function KeyDiscoveryFailedBadge({ 
	provider 
}: { 
	provider: { 
		keys: Array<{ status?: string }>;
		status?: string;
		description?: string;
	} 
}) {
	const hasFailedKeys = provider.keys?.some((key) => key.status === "list_models_failed");
	const providerFailed = provider.status === "list_models_failed";
	const hasFailed = hasFailedKeys || providerFailed;

	if (!hasFailed) return null;

	// Determine the tooltip message
	let tooltipMessage = "";
	if (providerFailed && hasFailedKeys) {
		tooltipMessage = "Provider and one or more keys have failed model discovery.";
	} else if (providerFailed) {
		tooltipMessage = provider.description || "Provider model discovery failed.";
	} else if (hasFailedKeys) {
		tooltipMessage = "One or more keys have failed list models. Check keys for details.";
	}

	return (
		<Tooltip>
			<TooltipTrigger>
				<AlertCircle className="h-3 w-3" />
			</TooltipTrigger>
			<TooltipContent>{tooltipMessage}</TooltipContent>
		</Tooltip>
	);
}
