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
} from "@/components/ui/alertDialog";
import { Button } from "@/components/ui/button";
import { CardHeader, CardTitle } from "@/components/ui/card";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { getErrorMessage, useUpdateProviderMutation } from "@/lib/store";
import { ModelProvider } from "@/lib/types/config";
import { cn } from "@/lib/utils";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { AlertCircle, CheckCircle2, EllipsisIcon, PencilIcon, PlusIcon, TrashIcon } from "lucide-react";
import { ReactNode, useState } from "react";
import { toast } from "sonner";
import AddNewKeySheet from "../dialogs/addNewKeySheet";

interface Props {
	className?: string;
	provider: ModelProvider;
	headerActions?: ReactNode;
	isKeyless?: boolean;
}

export default function ModelProviderKeysTableView({ provider, className, headerActions, isKeyless }: Props) {
	const hasUpdateProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const hasDeleteProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Delete);
	const [updateProvider, { isLoading: isUpdatingProvider }] = useUpdateProviderMutation();
	const [showAddNewKeyDialog, setShowAddNewKeyDialog] = useState<{ show: boolean; keyIndex: number } | undefined>(undefined);
	const [showDeleteKeyDialog, setShowDeleteKeyDialog] = useState<{ show: boolean; keyIndex: number } | undefined>(undefined);

	function handleAddKey(keyIndex: number) {
		setShowAddNewKeyDialog({ show: true, keyIndex: keyIndex });
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
							<AlertDialogCancel onClick={() => setShowDeleteKeyDialog(undefined)} disabled={isUpdatingProvider}>
								Cancel
							</AlertDialogCancel>
							<AlertDialogAction
								disabled={isUpdatingProvider || !hasDeleteProviderAccess}
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
				<AddNewKeySheet
					show={showAddNewKeyDialog.show}
					onCancel={() => setShowAddNewKeyDialog(undefined)}
					provider={provider}
					keyIndex={showAddNewKeyDialog.keyIndex}
				/>
			)}
			<CardHeader className="mb-4 px-0">
				<CardTitle className="flex items-center justify-between">
					<div className="flex items-center gap-2">Configured keys</div>
					<div className="flex items-center gap-2">
						{headerActions}
						{!isKeyless && (
							<Button
								disabled={!hasUpdateProviderAccess}
								data-testid="add-key-btn"
								onClick={() => {
									handleAddKey(provider.keys.length);
								}}
							>
								<PlusIcon className="h-4 w-4" />
								Add new key
							</Button>
						)}
					</div>
				</CardTitle>
			</CardHeader>
			{isKeyless ? (
				<div className="text-muted-foreground flex flex-col items-center justify-center gap-2 rounded-sm border py-10 text-center text-sm">
					<p>This is a keyless provider - no API keys are required.</p>
					<p>You can edit the provider configuration using the button above.</p>
				</div>
			) : (
				<div className="w-full rounded-sm border flex flex-col gap-2">
					<Table className="w-full" data-testid="keys-table">
						<TableHeader className="w-full">
							<TableRow>
								<TableHead>API Key</TableHead>
								<TableHead>Weight</TableHead>
								<TableHead>Enabled</TableHead>
								<TableHead className="text-right"></TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{provider.keys.length === 0 && (
								<TableRow>
									<TableCell colSpan={4} className="py-6 text-center">
										No keys found.
									</TableCell>
								</TableRow>
							)}
							{provider.keys.map((key, index) => {
								const isKeyEnabled = key.enabled ?? true;
								return (
									<TableRow key={index} data-testid={`key-row-${key.name}`} className="text-sm transition-colors hover:bg-white" onClick={() => {}}>
										<TableCell>
											<div className="flex items-center space-x-2">
												{key.status === "success" && (
													<CheckCircle2 className="text-green-600 h-4 w-4 flex-shrink-0" />
												)}
												{key.status === "list_models_failed" && (
													<Tooltip>
														<TooltipTrigger>
															<AlertCircle className="text-destructive h-4 w-4 flex-shrink-0" />
														</TooltipTrigger>
														<TooltipContent className="max-w-xs break-words">
															{key.description || "Model discovery failed for this key"}
														</TooltipContent>
													</Tooltip>
												)}
												<span className="font-mono text-sm">{key.name}</span>
											</div>
										</TableCell>
										<TableCell>
											<div className="flex items-center space-x-2">
												<span className="font-mono text-sm">{key.weight}</span>
											</div>
										</TableCell>
										<TableCell>
											<Switch
												checked={isKeyEnabled}
												size="md"
												disabled={!hasUpdateProviderAccess}
												onCheckedChange={(checked) => {
													updateProvider({
														...provider,
														keys: provider.keys.map((k, i) => (i === index ? { ...k, enabled: checked } : k)),
													})
														.unwrap()
														.then(() => {
															toast.success(`Key ${checked ? "enabled" : "disabled"} successfully`);
														})
														.catch((err) => {
															toast.error("Failed to update key", { description: getErrorMessage(err) });
														});
												}}
											/>
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
															disabled={!hasUpdateProviderAccess || !isKeyEnabled}
														>
															<PencilIcon className="mr-1 h-4 w-4" />
															Edit
														</DropdownMenuItem>
														<DropdownMenuItem
															onClick={() => {
																setShowDeleteKeyDialog({ show: true, keyIndex: index });
															}}
															disabled={!hasDeleteProviderAccess}
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
			)}
		</div>
	);
}
