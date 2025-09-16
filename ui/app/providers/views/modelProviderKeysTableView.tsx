"use client";

import { Button } from "@/components/ui/button";
import { CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { ModelProvider, ModelProviderKey } from "@/lib/types/config";
import { EllipsisIcon, PencilIcon, PlusIcon, TrashIcon } from "lucide-react";
import { useState } from "react";
import AddNewKeyDialog from "../dialogs/addNewKeyDialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
} from "@/components/ui/alertDialog";
import { getErrorMessage, useUpdateProviderMutation } from "@/lib/store";
import { toast } from "sonner";
import { cn } from "@/lib/utils";
import { KnownProvidersNames } from "@/lib/constants/logs";

interface Props {
	className?: string;
	provider: ModelProvider;
}

export default function ModelProviderKeysTableView({ provider, className }: Props) {
	const [updateProvider, { isLoading: isUpdatingProvider }] = useUpdateProviderMutation();
	const [showAddNewKeyDialog, setShowAddNewKeyDialog] = useState<{ show: boolean; keyIndex: number } | undefined>(undefined);
	const [showDeleteKeyDialog, setShowDeleteKeyDialog] = useState<{ show: boolean; keyIndex: number } | undefined>(undefined);

	function handleAddKey(keyIndex: number) {
		setShowAddNewKeyDialog({ show: true, keyIndex: keyIndex });
	}

	function getKey(provider: ModelProvider, key: ModelProviderKey) {
		switch (provider.name) {
			case KnownProvidersNames[5]:
				return key.vertex_key_config?.auth_credentials || "unknown";
			case KnownProvidersNames[3]:
				return key.value || key.bedrock_key_config?.access_key || "system IAM";
			default:
				return key.value;
		}
	}

	return (
		<div className={cn("w-full", className)}>
			{showDeleteKeyDialog && (
				<AlertDialog open={showDeleteKeyDialog.show}>
					<AlertDialogContent onClick={(e) => e.stopPropagation()}>
						<AlertDialogHeader>
							<AlertDialogTitle>Delete Key</AlertDialogTitle>
							<AlertDialogDescription>Are you sure you want to delete this key. This action cannot be undone.</AlertDialogDescription>
						</AlertDialogHeader>
						<AlertDialogFooter className="pt-4">
							<AlertDialogCancel disabled={isUpdatingProvider}>Cancel</AlertDialogCancel>
							<AlertDialogAction
								disabled={isUpdatingProvider}
								onClick={() => {
									updateProvider({
										...provider,
										keys: provider.keys.filter((_, index) => index !== showDeleteKeyDialog.keyIndex),
									})
										.unwrap()
										.then(() => {
											toast.success("Key deleted successfully");
											setShowDeleteKeyDialog(undefined);
										})
										.catch((err) => {
											toast.error("Failed to delete key", {
												description: getErrorMessage(err),
											});
										});
								}}
							>
								Delete
							</AlertDialogAction>
						</AlertDialogFooter>
					</AlertDialogContent>
				</AlertDialog>
			)}
			{showAddNewKeyDialog && (
				<AddNewKeyDialog
					show={showAddNewKeyDialog.show}
					onCancel={() => setShowAddNewKeyDialog(undefined)}
					provider={provider}
					keyIndex={showAddNewKeyDialog.keyIndex}
				/>
			)}
			<CardHeader className="mb-4 px-0">
				<CardTitle className="flex items-center justify-between">
					<div className="flex items-center gap-2">Configured keys</div>
					<Button
						onClick={() => {
							handleAddKey(provider.keys.length);
						}}
					>
						<PlusIcon className="h-4 w-4" />
						Add new key
					</Button>
				</CardTitle>
			</CardHeader>
			<div className="w-full rounded-sm border">
				<Table className="w-full">
					<TableHeader className="w-full">
						<TableRow>
							<TableHead>API Key</TableHead>
							<TableHead>Weight</TableHead>
							<TableHead className="text-right"></TableHead>
						</TableRow>
					</TableHeader>
					<TableBody>
						{provider.keys.length === 0 && (
							<TableRow>
								<TableCell colSpan={3} className="py-6 text-center">
									No keys found.
								</TableCell>
							</TableRow>
						)}
						{provider.keys.map((key, index) => {
							return (
								<TableRow key={index} className="text-sm transition-colors hover:bg-white" onClick={() => {}}>
									<TableCell>
										<div className="flex items-center space-x-2">
											<span className="font-mono text-sm">{getKey(provider, key)}</span>
										</div>
									</TableCell>
									<TableCell>
										<div className="flex items-center space-x-2">
											<span className="font-mono text-sm">{key.weight}</span>
										</div>
									</TableCell>
									<TableCell className="text-right">
										<div className="flex items-center justify-end space-x-2">
											<DropdownMenu>
												<DropdownMenuTrigger asChild>
													<Button onClick={(e) => e.stopPropagation()} variant="ghost">
														<EllipsisIcon className="h-5 w-5" />
													</Button>
												</DropdownMenuTrigger>
												<DropdownMenuContent align="end">
													<DropdownMenuItem
														onClick={() => {
															setShowAddNewKeyDialog({ show: true, keyIndex: index });
														}}
													>
														<PencilIcon className="mr-1 h-4 w-4" />
														Edit
													</DropdownMenuItem>
													<DropdownMenuItem
														onClick={() => {
															setShowDeleteKeyDialog({ show: true, keyIndex: index });
														}}
													>
														<TrashIcon className="mr-1 h-4 w-4" />
														Delete
													</DropdownMenuItem>
												</DropdownMenuContent>
											</DropdownMenu>
										</div>
									</TableCell>
								</TableRow>
							);
						})}
					</TableBody>
				</Table>
			</div>
		</div>
	);
}
