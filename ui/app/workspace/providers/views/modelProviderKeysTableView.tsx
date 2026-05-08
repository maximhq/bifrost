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
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import AddNewKeySheet from "../dialogs/addNewKeySheet";

interface Props {
	className?: string;
	provider: ModelProvider;
	headerActions?: ReactNode;
	isKeyless?: boolean;
}

export default function ModelProviderKeysTableView({ provider, className, headerActions, isKeyless }: Props) {
	const { t } = useTranslation();
	const providerName = provider.name?.toLowerCase() ?? "";
	const isVLLM = providerName === "vllm";
	const isOllamaOrSGL = providerName === "ollama" || providerName === "sgl";
	const entityLabel = isVLLM ? "model" : isOllamaOrSGL ? "server" : "key";
	const entityLabelPlural = isVLLM ? "models" : isOllamaOrSGL ? "servers" : "keys";
	const entityLabelLocalized = isVLLM
		? t("workspace.providers.keyTable.model")
		: isOllamaOrSGL
			? t("workspace.providers.keyTable.server")
			: t("workspace.providers.keyTable.apiKey");
	const entityLabelPluralLocalized = entityLabelLocalized;
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
							<AlertDialogTitle>{t("workspace.providers.keyTable.deleteItemTitle", { entity: entityLabelLocalized })}</AlertDialogTitle>
							<AlertDialogDescription>
								{t("workspace.providers.keyTable.deleteItemDescription", { entity: entityLabelLocalized })}
							</AlertDialogDescription>
						</AlertDialogHeader>
						<AlertDialogFooter className="pt-4">
							<AlertDialogCancel onClick={() => setShowDeleteKeyDialog(undefined)} disabled={isMutatingProviderKey}>
								{t("common.cancel")}
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
											toast.success(t("workspace.providers.keyTable.deleteItemSuccess", { entity: entityLabelLocalized }));
											setShowDeleteKeyDialog(undefined);
										})
										.catch((err) => {
											toast.error(t("workspace.providers.keyTable.deleteItemFailed", { entity: entityLabelLocalized }), {
												description: getErrorMessage(err),
											});
										});
								}}
							>
								{t("common.delete")}
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
					<div className="flex items-center gap-2">
						{t("workspace.providers.keyTable.configuredItems", { entity: entityLabelPluralLocalized })}
					</div>
					<div className="flex items-center gap-2">
						{headerActions}
						{!isKeyless && (
							<Button
								disabled={!hasUpdateProviderAccess}
								data-testid="add-key-btn"
								onClick={() => {
									handleAddKey();
								}}
							>
								<PlusIcon className="h-4 w-4" />
								{t("workspace.providers.keyTable.addNewItem", { entity: entityLabelLocalized })}
							</Button>
						)}
					</div>
				</CardTitle>
			</CardHeader>
			{isKeyless ? (
				<div className="text-muted-foreground flex flex-col items-center justify-center gap-2 rounded-sm border py-10 text-center text-sm">
					<p>{t("workspace.providers.keyTable.keylessProvider")}</p>
					<p>{t("workspace.providers.keyTable.keylessProviderHint")}</p>
				</div>
			) : (
				<div className="flex w-full flex-col gap-2 rounded-sm border">
					<Table className="w-full" data-testid="keys-table">
						<TableHeader className="w-full">
							<TableRow>
								<TableHead>{entityLabelLocalized}</TableHead>
								<TableHead>{t("workspace.providers.keyTable.weight")}</TableHead>
								<TableHead>{t("workspace.providers.keyTable.enabled")}</TableHead>
								<TableHead className="text-right"></TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{keys.length === 0 && (
								<TableRow data-testid="keys-table-empty-state">
									<TableCell colSpan={4} className="py-6 text-center">
										{t("workspace.providers.keyTable.noItemsFound", { entity: entityLabelPluralLocalized })}
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
										<TableCell>
											<div className="flex items-center space-x-2">
												{key.status === "success" && (
													<Tooltip>
														<TooltipTrigger asChild>
															<button
																type="button"
																aria-label={t("workspace.providers.keyTable.statusListModelsWorking")}
																data-testid={`key-status-success-${key.name}`}
																className="inline-flex"
															>
																<CheckCircle2 aria-hidden className="h-4 w-4 flex-shrink-0 text-green-600" />
															</button>
														</TooltipTrigger>
														<TooltipContent>{t("workspace.providers.keyTable.statusListModelsWorking")}</TooltipContent>
													</Tooltip>
												)}
												{key.status === "list_models_failed" &&
													(() => {
														// Check if the failure might be due to an env var that the server couldn't resolve
														const hasEnvVarConfig =
															key.azure_key_config?.endpoint?.from_env ||
															key.vertex_key_config?.project_id?.from_env ||
															key.vertex_key_config?.region?.from_env ||
															key.bedrock_key_config?.region?.from_env ||
															key.vllm_key_config?.url?.from_env ||
															key.value?.from_env;
														const isEnvResolutionError =
															hasEnvVarConfig && key.description && /not set|empty|missing/i.test(key.description);

														return isEnvResolutionError ? (
															<Tooltip>
																<TooltipTrigger asChild>
																	<button
																		type="button"
																		aria-label={t("workspace.providers.keyTable.statusEnvVarUnresolved")}
																		data-testid={`key-status-warning-${key.name}`}
																		className="inline-flex"
																	>
																		<AlertCircle aria-hidden className="h-4 w-4 flex-shrink-0 text-orange-500" />
																	</button>
																</TooltipTrigger>
																<TooltipContent className="max-w-xs break-words">
																	{t("workspace.providers.keyTable.envVarHint", { description: key.description })}
																</TooltipContent>
															</Tooltip>
														) : (
															<Tooltip>
																<TooltipTrigger asChild>
																	<button
																		type="button"
																		aria-label={t("workspace.providers.keyTable.statusListModelsFailed")}
																		data-testid={`key-status-error-${key.name}`}
																		className="inline-flex"
																	>
																		<AlertCircle aria-hidden className="text-destructive h-4 w-4 flex-shrink-0" />
																	</button>
																</TooltipTrigger>
																<TooltipContent className="max-w-xs break-words">
																	{key.description || t("workspace.providers.keyTable.discoveryFailed")}
																</TooltipContent>
															</Tooltip>
														);
													})()}
												<span className="font-mono text-sm">{key.name}</span>
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
															toast.success(
																t("workspace.providers.keyTable.itemToggledSuccess", {
																	entity: entityLabelLocalized,
																	status: checked
																		? t("workspace.providers.keyTable.itemEnabled")
																		: t("workspace.providers.keyTable.itemDisabled"),
																}),
															);
														})
														.catch((err) => {
															toast.error(t("workspace.providers.keyTable.itemUpdateFailed", { entity: entityLabelLocalized }), {
																description: getErrorMessage(err),
															});
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
												<DropdownMenu>
													<DropdownMenuTrigger asChild>
														<Button onClick={(e) => e.stopPropagation()} variant="ghost">
															<EllipsisIcon className="h-5 w-5" />
														</Button>
													</DropdownMenuTrigger>
													<DropdownMenuContent align="end">
														<DropdownMenuItem
															onClick={() => {
																setShowAddNewKeyDialog({ show: true, keyId: key.id });
															}}
															disabled={!hasUpdateProviderAccess}
														>
															<PencilIcon className="mr-1 h-4 w-4" />
															{t("common.edit")}
														</DropdownMenuItem>
														<DropdownMenuItem
															variant="destructive"
															onClick={() => {
																setShowDeleteKeyDialog({ show: true, keyId: key.id });
															}}
															disabled={!hasDeleteProviderAccess}
														>
															<TrashIcon className="mr-1 h-4 w-4" />
															{t("common.delete")}
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