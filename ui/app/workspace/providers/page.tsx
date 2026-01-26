"use client";

import ModelProviderConfig from "@/app/workspace/providers/views/modelProviderConfig";
import FullPageLoader from "@/components/fullPageLoader";
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
import { AlertCircle, PlusIcon, Trash } from "lucide-react";
import { useQueryState } from "nuqs";
import { useEffect, useState } from "react";
import { toast } from "sonner";
import AddCustomProviderSheet from "./dialogs/addNewCustomProviderSheet";
import ConfirmDeleteProviderDialog from "./dialogs/confirmDeleteProviderDialog";
import ConfirmRedirectionDialog from "./dialogs/confirmRedirection";

export default function Providers() {
	const dispatch = useAppDispatch();
	const selectedProvider = useAppSelector((state) => state.provider.selectedProvider);
	const providerFormIsDirty = useAppSelector((state) => state.provider.isDirty);
	const hasProviderCreateAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Create);
	const hasProviderDeleteAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Delete);

	const [showRedirectionDialog, setShowRedirectionDialog] = useState(false);
	const [showDeleteProviderDialog, setShowDeleteProviderDialog] = useState(false);
	const [pendingRedirection, setPendingRedirection] = useState<string | undefined>(undefined);
	const [showCustomProviderDialog, setShowCustomProviderDialog] = useState(false);
	const [provider, setProvider] = useQueryState("provider");

	const { data: savedProviders, isLoading: isLoadingProviders } = useGetProvidersQuery();
	const [getProvider, { isLoading: isLoadingProvider }] = useLazyGetProviderQuery();

	const allProviders = ProviderNames.map(
		(p) => savedProviders?.find((provider) => provider.name === p) ?? { name: p, keys: [], status: "active" as ProviderStatus },
	).sort((a, b) => a.name.localeCompare(b.name));
	const customProviders =
		savedProviders
			?.filter((provider) => !ProviderNames.includes(provider.name as KnownProvider))
			.sort((a, b) => a.name.localeCompare(b.name)) ?? [];

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
							status: "error",
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

	if (isLoadingProviders) {
		return <FullPageLoader />;
	}

	return (
		<div className="mx-auto flex h-full w-full max-w-7xl flex-row gap-4">
			<ConfirmDeleteProviderDialog
				provider={selectedProvider!}
				show={showDeleteProviderDialog}
				onCancel={() => setShowDeleteProviderDialog(false)}
				onDelete={() => {
					setProvider(allProviders[0].name);
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
				show={showCustomProviderDialog}
				onSave={(id) => {
					setTimeout(() => {
						setProvider(id);
					}, 300);
					setShowCustomProviderDialog(false);
				}}
				onClose={() => {
					setShowCustomProviderDialog(false);
				}}
			/>
			<div className="flex flex-col" style={{ maxHeight: "calc(100vh - 70px)", width: "300px" }}>
				<TooltipProvider>
					<div className="custom-scrollbar flex-1 overflow-y-auto">
						<div className="rounded-md bg-zinc-50/50 p-4 dark:bg-zinc-800/20">
							{/* Standard Providers */}
							<div>
								<div className="text-muted-foreground mb-2 text-xs font-medium">Standard Providers</div>
								{allProviders.map((p) => {
									return (
										<Tooltip key={p.name}>
											<TooltipTrigger
												className={cn(
													"mb-1 flex w-full items-center gap-2 rounded-sm border px-3 py-1.5 text-sm",
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
												<div className="flex items-center gap-2">
													<RenderProviderIcon provider={p.name as ProviderIconType} size="sm" className="h-4 w-4" />
													<div className="text-sm">{ProviderLabels[p.name as keyof typeof ProviderLabels]}</div>
													<ProviderStatusBadge status={p.status} />
												</div>
											</TooltipTrigger>
										</Tooltip>
									);
								})}
								{customProviders.length > 0 && <div className="text-muted-foreground mt-3 mb-2 text-xs font-medium">Custom Providers</div>}
								{customProviders.map((p) => (
									<Tooltip key={p.name}>
										<TooltipTrigger
											className={cn(
												"mb-1 flex w-full items-center gap-2 rounded-sm border px-3 py-1.5 text-sm",
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
											<div className="group flex w-full items-center gap-2">
												<div className="flex w-full items-center gap-2">
													<RenderProviderIcon
														provider={p.custom_provider_config?.base_provider_type as ProviderIconType}
														size="sm"
														className="h-4 w-4"
													/>
													<div className="text-sm">{p.name}</div>
													<ProviderStatusBadge status={p.status} />
												</div>
												{selectedProvider?.name === p.name && hasProviderDeleteAccess && (
													<Trash
														className="text-muted-foreground hover:text-destructive ml-auto hidden h-4 w-4 cursor-pointer group-hover:block"
														onClick={(event) => {
															event.preventDefault();
															event.stopPropagation();
															setShowDeleteProviderDialog(true);
														}}
													/>
												)}
											</div>
										</TooltipTrigger>
									</Tooltip>
								))}
							</div>
						</div>
					</div>
					<div className="sticky bottom-0 z-10 bg-zinc-50/80 p-2 backdrop-blur-sm dark:bg-zinc-900/80">
						<Button
							variant="outline"
							size="sm"
							className="w-full justify-start"
							disabled={!hasProviderCreateAccess}
							onClick={(e) => {
								e.preventDefault();
								e.stopPropagation();
								setShowCustomProviderDialog(true);
							}}
						>
							<PlusIcon className="h-4 w-4" />
							<div className="text-xs">Add New Custom Provider</div>
						</Button>
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
			{!isLoadingProvider && selectedProvider && <ModelProviderConfig provider={selectedProvider} />}
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
