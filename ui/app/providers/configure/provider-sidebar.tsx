"use client";

import { Button } from "@/components/ui/button";
import { Tooltip, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { PROVIDER_LABELS, PROVIDERS as Providers } from "@/lib/constants/logs";
import { ProviderResponse } from "@/lib/types/config";
import { cn } from "@/lib/utils";
import { Plus } from "lucide-react";

interface ProviderSidebarProps {
	selectedProvider: string;
	allProviders: ProviderResponse[];
	onProviderSelect: (provider: ProviderResponse | null, providerName?: string) => void;
	onCreateCustomProvider: () => void;
}

export function ProviderSidebar({ selectedProvider, allProviders, onProviderSelect, onCreateCustomProvider }: ProviderSidebarProps) {
	return (
		<TooltipProvider>
			<div className="flex w-[250px] flex-col gap-1 pb-10">
				<div className="rounded-md bg-zinc-50/50 p-4 dark:bg-zinc-800/20">
					{/* Standard Providers */}
					<div className="mb-4">
						<div className="text-muted-foreground mb-2 text-xs font-medium">Standard Providers</div>
						{Providers.map((p) => {
							const existingProvider = allProviders.find((provider) => provider.name === p);
							return (
								<Tooltip key={p}>
									<TooltipTrigger
										className={cn(
											"mb-1 flex w-full items-center gap-2 rounded-lg border px-3 py-1 text-sm",
											selectedProvider === p
												? "bg-secondary opacity-100 hover:opacity-100"
												: "hover:bg-secondary cursor-pointer border-transparent opacity-100 hover:border",
										)}
										onClick={(e) => {
											e.preventDefault();
											onProviderSelect(existingProvider || null, p);
										}}
										asChild
									>
										<span>
											<RenderProviderIcon provider={p as ProviderIconType} size="sm" className="h-4 w-4" />
											<div className="text-sm">{PROVIDER_LABELS[p as keyof typeof PROVIDER_LABELS]}</div>
										</span>
									</TooltipTrigger>
								</Tooltip>
							);
						})}
					</div>

					{/* Custom Providers */}
					{allProviders.filter((p) => !Providers.includes(p.name as any)).length > 0 && (
						<div>
							<div className="text-muted-foreground mb-2 text-xs font-medium">Custom Providers</div>
							{allProviders
								.filter((p) => !Providers.includes(p.name as any))
								.map((provider) => (
									<Tooltip key={provider.name}>
										<TooltipTrigger
											className={cn(
												"mb-1 flex w-full items-center gap-2 rounded-lg border px-3 py-1 text-sm",
												selectedProvider === provider.name
													? "bg-secondary opacity-100 hover:opacity-100"
													: "hover:bg-secondary cursor-pointer border-transparent opacity-100 hover:border",
											)}
											onClick={(e) => {
												e.preventDefault();
												onProviderSelect(provider);
											}}
											asChild
										>
											<span>
												<RenderProviderIcon
													provider={provider.custom_provider_config?.base_provider_type as ProviderIconType}
													size="sm"
													className="h-4 w-4"
												/>
												<div className="text-sm">{provider.name}</div>
											</span>
										</TooltipTrigger>
									</Tooltip>
								))}
						</div>
					)}

					<div className="my-4">
						<Button
							variant="outline"
							size="sm"
							className="w-full justify-start"
							onClick={(e) => {
								e.preventDefault();
								e.stopPropagation();
								onCreateCustomProvider();
							}}
						>
							<Plus className="h-4 w-4" />
							<div className="text-xs">Add new custom provider</div>
						</Button>
					</div>
				</div>
			</div>
		</TooltipProvider>
	);
}
