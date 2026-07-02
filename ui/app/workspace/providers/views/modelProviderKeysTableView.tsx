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
import { getErrorMessage } from "@/lib/store";
import { useDeleteProviderKeyMutation, useGetProviderKeysQuery, useUpdateProviderKeyMutation } from "@/lib/store/apis/providersApi";
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

function ProviderKeyActionsMenu({
	keyId,
	hasUpdateAccess,
	hasDeleteAccess,
	onEdit,
	onDelete,
}: {
	keyId: string;
	hasUpdateAccess: boolean;
	hasDeleteAccess: boolean;
	onEdit: (keyId: string) => void;
	onDelete: (keyId: string) => void;
}) {
	const [isOpen, setIsOpen] = useState(false);

	return (
		<DropdownMenu open={isOpen} onOpenChange={setIsOpen}>
			<DropdownMenuTrigger asChild>
				<Button onClick={(e) => e.stopPropagation()} variant="ghost">
					<EllipsisIcon className="h-5 w-5" />
				</Button>
			</DropdownMenuTrigger>
			<DropdownMenuContent align="end">
				<DropdownMenuItem
					onSelect={(e) => {
						e.preventDefault();
						onEdit(keyId);
						setIsOpen(false);
					}}
					disabled={!hasUpdateAccess}
				>
					<PencilIcon className="mr-1 h-4 w-4" />
					Edit
				</DropdownMenuItem>
				<DropdownMenuItem
					variant="destructive"
					onSelect={(e) => {
						e.preventDefault();
						onDelete(keyId);
						setIsOpen(false);
					}}
					disabled={!hasDeleteAccess}
				>
					<TrashIcon className="mr-1 h-4 w-4" />
					Delete
				</DropdownMenuItem>
			</DropdownMenuContent>
		</DropdownMenu>
	);
}

export default function ModelProviderKeysTableView({ provider, className, headerActions, isKeyless }: Props) {
	const providerName = provider.name?.toLowerCase() ?? "";
	const isVLLM = providerName === "vllm";
	const isOllamaOrSGL = providerName === "ollama" || providerName === "sgl";
	const isGigaChat = providerName === "gigachat";
	const entityLabel = isVLLM ? "model" : isOllamaOrSGL ? "server" : isGigaChat ? "credential" : "key";
	const entityLabelPlural = isVLLM ? "models" : isOllamaOrSGL ? "servers" : isGigaChat ? "credentials" : "keys";
	const EntityLabel = entityLabel.charAt(0).toUpperCase() + entityLabel.slice(1);
	const hasUpdateProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const hasDeleteProviderAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Delete);
	const [updateProviderKey, { isLoading: isUpdatingProviderKey }] = useUpdateProviderKeyMutation();
	const [deleteProviderKey, { isLoading: isDeletingProviderKey }] = useDeleteProviderKeyMutation();
	const { data: keys = [] } = useGetProviderKeysQuery(provider.name);
	const isMutatingProviderKey = isUpdatingProviderKey || isDeletingProviderKey;
	const [togglingKeyIds, setTogglingKeyIds] = useState<Set<string>>(new Set());
	const [showAddNewKeyDialog, setShowAddNewKeyDialog] = useState<{ show: boolean; keyId: string | null } | undefined>(undefined);
	const [showDeleteKeyDialog, setShowDeleteKeyDialog] = useState<{ show: boolean; keyId: string } | undefined>(undefined);

	function handleAddKey() {
		setShowAddNewKeyDialog({ show: true, keyId: null });
	}

	return (
		<div className={cn("w-full", className)}>
			{showDeleteKeyDialog && (
				<AlertDialog open={showDeleteKeyDialog.show}>
					<AlertDialogContent onClick={(e) => e.stopPropagation()}>
						<AlertDialogHeader>
							<AlertDialogTitle>Delete {EntityLabel}</AlertDialogTitle>
							<AlertDialogDescription>
								Are you sure you want to delete this {entityLabel}. This action cannot be undone.
							</AlertDialogDescription>
						</AlertDialogHeader>
						<AlertDialogFooter className="pt-4">
							<AlertDialogCancel onClick={() => setShowDeleteKeyDialog(undefined)} disabled={isMutatingProviderKey}>
								Cancel
							</AlertDialogCancel>
							<AlertDialogAction
								disabled={isMutatingProviderKey || !hasDeleteProviderAccess}
								onClick={() => {
									deleteProviderKey({
										provider: provider.name,
										keyId: showDeleteKeyDialog.keyId,
									})
										.unwrap()
										.then(() => {
											toast.success(`${EntityLabel} deleted successfully`);
											setShowDeleteKeyDialog(undefined);
										})
										.catch((err) => {
											toast.error(`Failed to delete ${entityLabel}`, {
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
					keyId={showAddNewKeyDialog.keyId}
					providerName={providerName}
				/>
			)}
			<CardHeader className="mb-4 px-0">
				<CardTitle className="flex items-center justify-between">
					<div className="flex items-center gap-2">Configured {entityLabelPlural}</div>
					<div className="flex items-center gap-2">
						{headerActions}
						{!isKeyless && hasUpdateProviderAccess ? (
							<Button
								disabled={!hasUpdateProviderAccess}
								data-testid="add-key-btn"
								onClick={() => {
									handleAddKey();
								}}
							>
								<PlusIcon className="h-4 w-4" />
								Add new {entityLabel}
							</Button>
						) : null}
					</div>
				</CardTitle>
			</CardHeader>
			{isKeyless ? (
				<div className="text-muted-foreground flex flex-col items-center justify-center gap-2 rounded-sm border py-10 text-center text-sm">
					<p>This is a keyless provider - no API keys are required.</p>
					<p>You can edit the provider configuration using the button above.</p>
				</div>
			) : (
				<div className="flex w-full flex-col gap-2 rounded-sm border">
					<Table className="w-full table-fixed" data-testid="keys-table">
						<colgroup>
							<col className="w-[64%]" />
							<col className="w-[12%]" />
							<col className="w-[12%]" />
							<col className="w-[12%]" />
						</colgroup>
						<TableHeader className="w-full">
							<TableRow>
								<TableHead>{isVLLM ? "Model" : isOllamaOrSGL ? "Server" : isGigaChat ? "Credential" : "API Key"}</TableHead>
								<TableHead>Weight</TableHead>
								<TableHead>Enabled</TableHead>
								<TableHead className="text-right"></TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{keys.length === 0 && (
								<TableRow data-testid="keys-table-empty-state">
									<TableCell colSpan={4} className="py-6 text-center">
										No {entityLabelPlural} found.
									</TableCell>
								</TableRow>
							)}
							{keys.map((key) => {
								const isKeyEnabled = key.enabled ?? true;
								return (
									<TableRow
										key={key.id}
										data-testid={`key-row-${key.name}`}
										className="text-sm transition-colors hover:bg-white"
										onClick={() => {}}
									>
										<TableCell className="overflow-hidden">
											<div className="flex min-w-0 items-center space-x-2">
												{key.status === "success" && (
													<Tooltip>
														<TooltipTrigger asChild>
															<button
																type="button"
																aria-label="Key status: list models working"
																data-testid={`key-status-success-${key.name}`}
																className="inline-flex"
															>
																<CheckCircle2 aria-hidden className="h-4 w-4 flex-shrink-0 text-green-600" />
															</button>
														</TooltipTrigger>
														<TooltipContent>List models working</TooltipContent>
													</Tooltip>
												)}
												{key.status === "list_models_failed" &&
													(() => {
														// Check if the failure might be due to a secret reference that the server couldn't resolve
														const hasSecretVarConfig =
															(key.azure_key_config?.endpoint?.type && key.azure_key_config.endpoint.type !== "plain_text") ||
															(key.vertex_key_config?.project_id?.type && key.vertex_key_config.project_id.type !== "plain_text") ||
															(key.vertex_key_config?.region?.type && key.vertex_key_config.region.type !== "plain_text") ||
															(key.bedrock_key_config?.region?.type && key.bedrock_key_config.region.type !== "plain_text") ||
															(key.bedrock_mantle_key_config?.region?.type && key.bedrock_mantle_key_config.region.type !== "plain_text") ||
															(key.vllm_key_config?.url?.type && key.vllm_key_config.url.type !== "plain_text") ||
															(key.gigachat_key_config?.credentials?.type && key.gigachat_key_config.credentials.type !== "plain_text") ||
															(key.gigachat_key_config?.access_token?.type && key.gigachat_key_config.access_token.type !== "plain_text") ||
															(key.gigachat_key_config?.user?.type && key.gigachat_key_config.user.type !== "plain_text") ||
															(key.gigachat_key_config?.password?.type && key.gigachat_key_config.password.type !== "plain_text") ||
															(key.value?.type && key.value.type !== "plain_text");
														const isSecretResolutionError =
															hasSecretVarConfig && key.description && /not set|empty|missing/i.test(key.description);

														return isSecretResolutionError ? (
															<Tooltip>
																<TooltipTrigger asChild>
																	<button
																		type="button"
																		aria-label="Key status: secret reference may not be resolved"
																		data-testid={`key-status-warning-${key.name}`}
																		className="inline-flex"
																	>
																		<AlertCircle aria-hidden className="h-4 w-4 flex-shrink-0 text-orange-500" />
																	</button>
																</TooltipTrigger>
																<TooltipContent className="max-w-xs break-words">
																	{key.description} — verify the secret reference is configured on the server
																</TooltipContent>
															</Tooltip>
														) : (
															<Tooltip>
																<TooltipTrigger asChild>
																	<button
																		type="button"
																		aria-label="Key status: list models failed"
																		data-testid={`key-status-error-${key.name}`}
																		className="inline-flex"
																	>
																		<AlertCircle aria-hidden className="text-destructive h-4 w-4 flex-shrink-0" />
																	</button>
																</TooltipTrigger>
																<TooltipContent className="max-w-xs break-words">
																	{key.description || "Model discovery failed for this key"}
																</TooltipContent>
															</Tooltip>
														);
													})()}
												<span className="truncate font-mono text-sm">{key.name}</span>
											</div>
										</TableCell>
										<TableCell data-testid="key-weight-value">
											<div className="flex items-center space-x-2">
												<span className="font-mono text-sm">{key.weight}</span>
											</div>
										</TableCell>
										<TableCell>
											<Switch
												data-testid="key-enabled-switch"
												checked={isKeyEnabled}
												size="md"
												disabled={!hasUpdateProviderAccess || togglingKeyIds.has(key.id)}
												onAsyncCheckedChange={async (checked) => {
													setTogglingKeyIds((prev) => new Set(prev).add(key.id));
													await updateProviderKey({
														provider: provider.name,
														keyId: key.id,
														key: { ...key, enabled: checked },
													})
														.unwrap()
														.then(() => {
															toast.success(`${EntityLabel} ${checked ? "enabled" : "disabled"} successfully`);
														})
														.catch((err) => {
															toast.error(`Failed to update ${entityLabel}`, { description: getErrorMessage(err) });
														})
														.finally(() => {
															setTogglingKeyIds((prev) => {
																const next = new Set(prev);
																next.delete(key.id);
																return next;
															});
														});
												}}
											/>
										</TableCell>
										<TableCell className="text-right">
											<div className="flex items-center justify-end space-x-2">
												{hasUpdateProviderAccess || hasDeleteProviderAccess ? (
													<ProviderKeyActionsMenu
														keyId={key.id}
														hasUpdateAccess={hasUpdateProviderAccess}
														hasDeleteAccess={hasDeleteProviderAccess}
														onEdit={(keyId) => setShowAddNewKeyDialog({ show: true, keyId })}
														onDelete={(keyId) => setShowDeleteKeyDialog({ show: true, keyId })}
													/>
												) : null}
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