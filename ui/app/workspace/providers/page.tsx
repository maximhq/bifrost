"use client";

import ModelProviderConfig from "@/app/workspace/providers/views/modelProviderConfig";
import FullPageLoader from "@/components/fullPageLoader";
import { Badge } from "@/components/ui/badge";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { DefaultNetworkConfig, DefaultPerformanceConfig } from "@/lib/constants/config";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderLabels, ProviderNames } from "@/lib/constants/logs";
import {
	getErrorMessage,
	setSelectedProvider,
	useAppDispatch,
	useAppSelector,
	useCreateProviderMutation,
	useGetProvidersQuery,
	useLazyGetProviderQuery,
} from "@/lib/store";
import { KnownProvider, ModelProviderName, ProviderStatus } from "@/lib/types/config";
import { cn } from "@/lib/utils";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { AlertCircle } from "lucide-react";
import { useRouter } from "next/navigation";
import { useQueryState } from "nuqs";
import { useCallback, useEffect, useRef, useState } from "react";
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
	const hasProviderCreateAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Create);
	

	// Redirect Settings-only users to Custom pricing tab
	useEffect(() => {
		if (!hasProvidersAccess && hasSettingsOnly) {
			router.replace("/workspace/custom-pricing");
		}
	}, [hasProvidersAccess, hasSettingsOnly, router]);

	const selectedProvider = useAppSelector((state) => state.provider.selectedProvider);
	const providerFormIsDirty = useAppSelector((state) => state.provider.isDirty);

	const [showRedirectionDialog, setShowRedirectionDialog] = useState(false);
	const [showDeleteProviderDialog, setShowDeleteProviderDialog] = useState(false);
	const [pendingRedirection, setPendingRedirection] = useState<string | undefined>(undefined);
	const [showCustomProviderSheet, setShowCustomProviderSheet] = useState(false);
	const [addedProviderNames, setAddedProviderNames] = useState<Set<string>>(new Set());
	const [provider, setProvider] = useQueryState("provider");

	const { data: savedProviders, isLoading: isLoadingProviders } = useGetProvidersQuery();
	const [getProvider, { isLoading: isLoadingProvider }] = useLazyGetProviderQuery();
	const [createProvider] = useCreateProviderMutation();

	const allProviders = ProviderNames.map(
		(p) => savedProviders?.find((provider) => provider.name === p) ?? { name: p, keys: [], provider_status: "active" as ProviderStatus },
	).sort((a, b) => a.name.localeCompare(b.name));

	const isProviderConfigured = (p: (typeof allProviders)[number]) =>
		(p.keys?.length ?? 0) > 0 || (p.network_config?.base_url ?? "").trim() !== "";
	const configuredStandardProviders = allProviders.filter(isProviderConfigured);

	const customProviders =
		savedProviders
			?.filter((provider) => !ProviderNames.includes(provider.name as KnownProvider))
			.sort((a, b) => a.name.localeCompare(b.name)) ?? [];

	// Providers added from dropdown but not yet configured (keys/base_url); show in sidebar so user can configure
	const addedOnly = allProviders.filter((p) => addedProviderNames.has(p.name) && !isProviderConfigured(p));
	const configuredProviders = [...configuredStandardProviders, ...customProviders, ...addedOnly].sort((a, b) =>
		a.name.localeCompare(b.name),
	);
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

	const handleSelectKnownProvider = async (name: string) => {
		try {
			await createProvider({ provider: name as ModelProviderName, keys: [] }).unwrap();
			setAddedProviderNames((prev) => new Set(prev).add(name));
			setProvider(name);
		} catch (err: any) {
			if (err?.status === 409) {
				// Provider already exists — still add it to the sidebar and select it
				setAddedProviderNames((prev) => new Set(prev).add(name));
				setProvider(name);
				return;
			}
			toast.error("Failed to add provider", {
				description: getErrorMessage(err),
			});
		}
	};

	if (configuredProviders.length === 0) {
		return (
			<div className="mx-auto w-full max-w-7xl">
				<ProvidersEmptyState
					addProviderDropdown={
						<AddProviderDropdown
							disabled={!hasProviderCreateAccess}
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
										const label = isCustom ? p.name : ProviderLabels[p.name as keyof typeof ProviderLabels];
										return (
											<div
												key={p.name}
												data-testid={`provider-item-${p.name.replace(/[^a-z0-9]+/gi, "-").toLowerCase()}`}
												className={cn(
													"mb-1 flex h-8 w-full min-w-0 cursor-pointer items-center gap-2 rounded-sm border px-3 text-sm",
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
											>
												<RenderProviderIcon
													provider={(isCustom ? p.custom_provider_config?.base_provider_type : p.name) as ProviderIconType}
													size="sm"
													className="h-4 w-4 shrink-0"
												/>
												<TruncatedName name={label} />
												<KeyDiscoveryFailedBadge provider={p} />
												<ProviderStatusBadge status={p.provider_status} />
												{isCustom && (
													<Badge variant="secondary" className="text-muted-foreground ml-auto shrink-0 px-1.5 py-0.5 text-[10px] font-bold">
														CUSTOM
													</Badge>
												)}
											</div>
										);
									})}
								</div>
							)}
							<div className="pb-4">
								<AddProviderDropdown
									disabled={!hasProviderCreateAccess}
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
				<ModelProviderConfig provider={selectedProvider} onRequestDelete={() => setShowDeleteProviderDialog(true)} />
			)}
		</div>
	);
}

function TruncatedName({ name }: { name: string }) {
	const textRef = useRef<HTMLDivElement>(null);
	const [isTruncated, setIsTruncated] = useState(false);

	const checkTruncation = useCallback(() => {
		const el = textRef.current;
		if (el) {
			setIsTruncated(el.scrollWidth > el.clientWidth);
		}
	}, []);

	useEffect(() => {
		checkTruncation();
		window.addEventListener("resize", checkTruncation);
		return () => window.removeEventListener("resize", checkTruncation);
	}, [checkTruncation, name]);

	const inner = (
		<div ref={textRef} className="min-w-0 flex-1 truncate text-sm">
			{name}
		</div>
	);

	if (!isTruncated) return inner;

	return (
		<Tooltip>
			<TooltipTrigger asChild>{inner}</TooltipTrigger>
			<TooltipContent side="right">{name}</TooltipContent>
		</Tooltip>
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
	provider,
}: {
	provider: {
		keys: Array<{ status?: string }>;
		status?: string;
		description?: string;
	};
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
