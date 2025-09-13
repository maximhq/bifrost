"use client";

import ModelProviderConfig from "@/app/providers/views/modelProviderConfig";
import FullPageLoader from "@/components/fullPageLoader";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderLabels, ProviderNames } from "@/lib/constants/logs";
import { toast } from "sonner";
import {
	getErrorMessage,
	setSelectedProvider,
	useAppDispatch,
	useAppSelector,
	useCreateProviderMutation,
	useGetProvidersQuery,
	useLazyGetProviderQuery,
} from "@/lib/store";
import { cn } from "@/lib/utils";
import { PlusIcon } from "lucide-react";
import { useEffect, useState } from "react";
import { DefaultNetworkConfig, DefaultPerformanceConfig } from "@/lib/constants/config";
import AddCustomProviderDialog from "./dialogs/addNewCustomProviderDialog";
import { KnownProvider } from "@/lib/types/config";

export default function Providers() {
	const dispatch = useAppDispatch();
	const { data: savedProviders, isLoading: isLoadingProviders } = useGetProvidersQuery();
	const [createProvider, { isLoading: creatingProvider }] = useCreateProviderMutation();
	const [showCustomProviderDialog, setShowCustomProviderDialog] = useState(false);
	const [getProvider, { isLoading: isLoadingProvider }] = useLazyGetProviderQuery();
	const selectedProvider = useAppSelector((state) => state.provider.selectedProvider);
	const allProviders = ProviderNames.map((p) => savedProviders?.find((provider) => provider.name === p) ?? { name: p, keys: [] });
	const customProviders = savedProviders?.filter((provider) => !ProviderNames.includes(provider.name as KnownProvider)) ?? [];

	useEffect(() => {
		if (!selectedProvider) return;
		// Checking if the provider already exists
		// If not, then we will create the provider first and then move on
		getProvider(selectedProvider.name)
			.unwrap()
			.then(() => {})
			.catch((err) => {
				if (err.status === 404) {
					// Here we create default object
					// We will create the provider first
					createProvider({
						provider: selectedProvider.name,
						concurrency_and_buffer_size: DefaultPerformanceConfig,
						network_config: DefaultNetworkConfig,
						...selectedProvider,
					})
						.unwrap()
						.then(() => {})
						.catch((err) => {
							toast.error("Error while updating provider", {
								description: getErrorMessage(err),
							});
						});
					return;
				}
				toast.error("Something went wrong", {
					description: `We encountered an error while getting provider config: ${getErrorMessage(err)}`,
				});
			});
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [selectedProvider]);

	useEffect(() => {
		if (selectedProvider || !savedProviders || (savedProviders?.length ?? 0) === 0) return;
		dispatch(setSelectedProvider(savedProviders[0]));
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [selectedProvider, savedProviders]);

	if (isLoadingProviders) {
		return <FullPageLoader />;
	}

	return (
		<div className="flex h-full flex-row gap-4">
			<AddCustomProviderDialog
				show={showCustomProviderDialog}
				onSave={() => {
					setShowCustomProviderDialog(false);
				}}
				onClose={() => {
					setShowCustomProviderDialog(false);
				}}
			/>
			<div className="flex flex-col">
				<TooltipProvider>
					<div className="flex w-[250px] flex-col gap-2 pb-10">
						<div className="rounded-md bg-zinc-50/50 p-4 dark:bg-zinc-800/20">
							{/* Standard Providers */}
							<div className="mb-4">
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
													dispatch(setSelectedProvider(p));
												}}
												asChild
											>
												<span>
													{p.custom_provider_config ? (
														<>
															<RenderProviderIcon
																provider={p.custom_provider_config?.base_provider_type as ProviderIconType}
																size="sm"
																className="h-4 w-4"
															/>
															<div className="text-sm">{p.name}</div>
														</>
													) : (
														<>
															<RenderProviderIcon provider={p.name as ProviderIconType} size="sm" className="h-4 w-4" />
															<div className="text-sm">{ProviderLabels[p.name as keyof typeof ProviderLabels]}</div>
														</>
													)}
												</span>
											</TooltipTrigger>
										</Tooltip>
									);
								})}
								{customProviders.length > 0 && <div className="text-muted-foreground mb-2 text-xs font-medium">Custom Providers</div>}
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
												dispatch(setSelectedProvider(p));
											}}
											asChild
										>
											<span>
												{p.custom_provider_config ? (
													<>
														<RenderProviderIcon
															provider={p.custom_provider_config?.base_provider_type as ProviderIconType}
															size="sm"
															className="h-4 w-4"
														/>
														<div className="text-sm">{p.name}</div>
													</>
												) : (
													<>
														<RenderProviderIcon provider={p.name as ProviderIconType} size="sm" className="h-4 w-4" />
														<div className="text-sm">{ProviderLabels[p.name as keyof typeof ProviderLabels]}</div>
													</>
												)}
											</span>
										</TooltipTrigger>
									</Tooltip>
								))}
							</div>
							<div className="my-4">
								<Button
									variant="outline"
									size="sm"
									className="w-full justify-start"
									onClick={(e) => {
										e.preventDefault();
										e.stopPropagation();
										setShowCustomProviderDialog(true);
									}}
								>
									<PlusIcon className="h-4 w-4" />
									<div className="text-xs">Add New Provider</div>
								</Button>
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
			{!isLoadingProvider && selectedProvider && <ModelProviderConfig provider={selectedProvider} />}
		</div>
	);
}
