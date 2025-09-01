"use client";

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
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { getProviderLabel } from "@/lib/constants/logs";
import { getErrorMessage, openConfigureDialog, useAppDispatch, useDeleteProviderMutation } from "@/lib/store";
import { ProviderResponse } from "@/lib/types/config";
import { Key, Loader2, Settings, Trash2 } from "lucide-react";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { toast } from "sonner";

interface ProvidersListProps {
	providers: ProviderResponse[];
	onRefresh: () => void;
}

export default function ProvidersList({ providers, onRefresh }: ProvidersListProps) {
	const router = useRouter();
	const dispatch = useAppDispatch();
	const [deletingProvider, setDeletingProvider] = useState<string | null>(null);

	// RTK Query mutation
	const [deleteProvider] = useDeleteProviderMutation();

	// Helper constant for keyless providers
	const isKeyless = (baseType: string) => {
		const t = baseType?.toLowerCase();
		return t === "ollama" || t === "sgl";
	};

	const handleDelete = async (providerKey: string) => {
		setDeletingProvider(providerKey);

		try {
			await deleteProvider(providerKey).unwrap();
			toast.success("Provider deleted successfully");
			onRefresh();
		} catch (error) {
			toast.error(getErrorMessage(error));
		} finally {
			setDeletingProvider(null);
		}
	};

	const handleAddProvider = () => {
		dispatch(openConfigureDialog(null));
		router.push("/providers/configure");
	};

	const handleEditProvider = (provider: ProviderResponse) => {
		dispatch(openConfigureDialog(provider));
		router.push("/providers/configure");
	};

	return (
		<>
			<CardHeader className="mb-4 px-0">
				<CardTitle className="flex items-center justify-between">
					<div className="flex items-center gap-2">Configured providers</div>
					<Button onClick={handleAddProvider}>
						<Settings className="h-4 w-4" />
						Manage Providers
					</Button>
				</CardTitle>
			</CardHeader>
			<div className="rounded-sm border">
				<Table>
					<TableHeader>
						<TableRow className="px-2">
							<TableHead>Provider</TableHead>
							<TableHead>Concurrency</TableHead>
							<TableHead>Buffer Size</TableHead>
							<TableHead>Max Retries</TableHead>
							<TableHead>API Keys</TableHead>
							<TableHead className="text-right">Actions</TableHead>
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
							return (
								<TableRow
									key={provider.name}
									className="hover:bg-muted/50 cursor-pointer px-2 transition-colors"
									onClick={() => handleEditProvider(provider)}
								>
									<TableCell>
										<div className="flex items-center space-x-2">
											<RenderProviderIcon provider={baseType as ProviderIconType} size={16} />
											<p className="font-medium">{getProviderLabel(provider.name)}</p>
										</div>
									</TableCell>
									<TableCell>
										<div className="flex items-center space-x-2">
											<Badge variant="outline">{provider.concurrency_and_buffer_size?.concurrency ?? 1}</Badge>
										</div>
									</TableCell>
									<TableCell>
										<div className="flex items-center space-x-2">
											<Badge variant="outline">{provider.concurrency_and_buffer_size?.buffer_size ?? 10}</Badge>
										</div>
									</TableCell>
									<TableCell>
										<div className="flex items-center space-x-2">
											<Badge variant="outline">{provider.network_config?.max_retries ?? 0}</Badge>
										</div>
									</TableCell>
									<TableCell>
										<div className="flex items-center space-x-2">
											{!isKeyless(baseType) ? (
												<>
													<Key className="text-muted-foreground h-4 w-4" />
													<span className="text-sm">{provider.keys?.length ?? 0} keys</span>
												</>
											) : (
												<span className="text-sm">N/A</span>
											)}
										</div>
									</TableCell>
									<TableCell className="text-right">
										<div className="flex items-center justify-end space-x-2">
											<AlertDialog>
												<AlertDialogTrigger asChild>
													<Button
														onClick={(e) => e.stopPropagation()}
														variant="outline"
														size="sm"
														disabled={deletingProvider === provider.name}
													>
														{deletingProvider === provider.name ? (
															<Loader2 className="h-4 w-4 animate-spin" />
														) : (
															<Trash2 className="h-4 w-4" />
														)}
													</Button>
												</AlertDialogTrigger>
												<AlertDialogContent className="bg-secondary" onClick={(e) => e.stopPropagation()}>
													<AlertDialogHeader>
														<AlertDialogTitle>Delete Provider</AlertDialogTitle>
														<AlertDialogDescription>
															Are you sure you want to delete provider {provider.name}? This action cannot be undone.
														</AlertDialogDescription>
													</AlertDialogHeader>
													<AlertDialogFooter>
														<AlertDialogCancel>Cancel</AlertDialogCancel>
														<AlertDialogAction onClick={() => handleDelete(provider.name)}>Delete</AlertDialogAction>
													</AlertDialogFooter>
												</AlertDialogContent>
											</AlertDialog>
										</div>
									</TableCell>
								</TableRow>
							);
						})}
					</TableBody>
				</Table>
			</div>
		</>
	);
}
