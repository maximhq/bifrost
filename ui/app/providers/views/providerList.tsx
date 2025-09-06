"use client";

import Provider from "@/components/provider";
import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
	AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { CardHeader, CardTitle } from "@/components/ui/card";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuLabel,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { getProviderLabel, PROVIDERS } from "@/lib/constants/logs";
import { setSelectedProvider, useAppDispatch, useGetProvidersQuery } from "@/lib/store";
import { ProviderResponse } from "@/lib/types/config";
import { ChevronDownIcon, EllipsisIcon, PlusIcon } from "lucide-react";
import { useState } from "react";
import AddNewKeyDialog from "./addNewKeyDialog";

interface ProvidersListProps {
	providers: ProviderResponse[];
}

export default function ConfiguredKeys({ providers }: ProvidersListProps) {
	const dispatch = useAppDispatch();
	const [showAddNewKeyDialog, setShowAddNewKeyDialog] = useState(false);
	const { data: allProviders } = useGetProvidersQuery();

	function handleAddKey(provider?: string) {
		dispatch(setSelectedProvider(allProviders?.providers.find((p) => p.name === provider) ?? null));
		setShowAddNewKeyDialog(true);
	}

	return (
		<>
			<AddNewKeyDialog show={showAddNewKeyDialog} onCancel={() => setShowAddNewKeyDialog(false)} />
			<CardHeader className="mb-4 px-0">
				<CardTitle className="flex items-center justify-between">
					<div className="flex items-center gap-2">Configured providers</div>
					<DropdownMenu>
						<DropdownMenuTrigger asChild>
							<Button>
								<PlusIcon className="h-4 w-4" />
								Add new key
								<ChevronDownIcon className="h-4 w-4" />
							</Button>
						</DropdownMenuTrigger>
						<DropdownMenuContent align="end" className="w-56">
							<DropdownMenuLabel>Choose Provider</DropdownMenuLabel>
							<DropdownMenuSeparator />
							{PROVIDERS.map((provider) => (
								<DropdownMenuItem key={provider} onClick={() => handleAddKey(provider)} className="flex items-center gap-2">
									<Provider provider={provider} />
								</DropdownMenuItem>
							))}
							<DropdownMenuSeparator />
							<DropdownMenuItem onClick={() => handleAddKey("custom")}>
								<PlusIcon className="h-4 w-4" />
								<span>Custom Provider</span>
							</DropdownMenuItem>
						</DropdownMenuContent>
					</DropdownMenu>
				</CardTitle>
			</CardHeader>
			<div className="rounded-sm border">
				<Table>
					<TableHeader>
						<TableRow>
							<TableHead>Provider</TableHead>
							<TableHead>Concurrency</TableHead>
							<TableHead>Buffer Size</TableHead>
							<TableHead>Max Retries</TableHead>
							<TableHead>API Key</TableHead>
							<TableHead className="text-right"></TableHead>
						</TableRow>
					</TableHeader>
					<TableBody>
						{providers.length === 0 && (
							<TableRow>
								<TableCell colSpan={6} className="py-6 text-center">
									No providers found.
								</TableCell>
							</TableRow>
						)}
						{providers.map((provider) => {
							const baseType = provider.custom_provider_config?.base_provider_type ?? provider.name;
							// Get all keys
							const allKeys = provider.keys.map((key) => key.value);
							return allKeys.map((key, index) => {
								return (
									<TableRow key={index} className="text-sm transition-colors hover:bg-white" onClick={() => {}}>
										<TableCell>
											<div className="flex items-center space-x-2">
												<RenderProviderIcon provider={baseType as ProviderIconType} size={16} />
												<p className="font-medium">{getProviderLabel(provider.name)}</p>
											</div>
										</TableCell>
										<TableCell>
											<div className="flex items-center space-x-2 font-mono">{provider.concurrency_and_buffer_size?.concurrency ?? 1}</div>
										</TableCell>
										<TableCell>
											<div className="flex items-center space-x-2 font-mono">{provider.concurrency_and_buffer_size?.buffer_size ?? 10}</div>
										</TableCell>
										<TableCell>
											<div className="flex items-center space-x-2 font-mono">{provider.network_config?.max_retries ?? 0}</div>
										</TableCell>
										<TableCell>
											<div className="flex items-center space-x-2">
												<span className="font-mono text-sm">{key}</span>
											</div>
										</TableCell>
										<TableCell className="text-right">
											<div className="flex items-center justify-end space-x-2">
												<AlertDialog>
													<AlertDialogTrigger asChild>
														<Button onClick={(e) => e.stopPropagation()} variant="ghost">
															<EllipsisIcon className="h-5 w-5" />
														</Button>
													</AlertDialogTrigger>
													<AlertDialogContent onClick={(e) => e.stopPropagation()}>
														<AlertDialogHeader>
															<AlertDialogTitle>Delete Key</AlertDialogTitle>
															<AlertDialogDescription>
																Are you sure you want to delete this key. This action cannot be undone.
															</AlertDialogDescription>
														</AlertDialogHeader>
														<AlertDialogFooter>
															<AlertDialogCancel>Cancel</AlertDialogCancel>
															<AlertDialogAction onClick={() => {}}>Delete</AlertDialogAction>
														</AlertDialogFooter>
													</AlertDialogContent>
												</AlertDialog>
											</div>
										</TableCell>
									</TableRow>
								);
							});
						})}
					</TableBody>
				</Table>
			</div>
		</>
	);
}
