import FullPageLoader from "@/components/fullPageLoader";
import { LoadErrorView } from "@/components/loadErrorView";
import PageTitle from "@/components/pageTitle";
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
import { ResizableHandle, ResizablePanel, ResizablePanelGroup } from "@/components/ui/resizable";
import { useToast } from "@/hooks/use-toast";
import {
	getErrorMessage,
	useDeleteEvalDatasetFolderMutation,
	useDeleteEvalDatasetMutation,
	useDuplicateEvalDatasetMutation,
	useGetEvalDatasetFoldersQuery,
	useGetEvalDatasetsQuery,
	useMoveEvalDatasetFolderMutation,
	useMoveEvalDatasetMutation,
} from "@/lib/store";
import type { DatasetFolder, DatasetSummary } from "@/lib/types/evalDatasets";
import { showLoadError } from "@/lib/utils/loadError";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Database, Plus } from "lucide-react";
import { parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { DatasetDetail } from "./views/datasetDetail";
import { DatasetFolderSheet } from "./views/datasetFolderSheet";
import { DatasetFormSheet } from "./views/datasetFormSheet";
import { DatasetSidebar } from "./views/datasetSidebar";

// The most datasets the sidebar lists. It is the largest page the API serves.
const SIDEBAR_LIMIT = 200;

// DatasetsPage is the datasets screen, laid out like the prompt repository: the
// datasets and their folders on the left, and the selected dataset on the right.
// Nothing is selected until the user picks one; the selection lives in the URL.
export default function DatasetsPage() {
	const { t } = useTranslation("config");
	const { t: tCommon } = useTranslation("common");
	const { toast } = useToast();
	const canCreate = useRbac(RbacResource.Evaluators, RbacOperation.Create);
	const canUpdate = useRbac(RbacResource.Evaluators, RbacOperation.Update);
	const canDelete = useRbac(RbacResource.Evaluators, RbacOperation.Delete);

	// version, split and import belong to the dataset on the right. They are listed
	// here so that picking another dataset can clear them.
	const [urlState, setUrlState] = useQueryStates(
		{ id: parseAsString.withDefault(""), version: parseAsInteger, split: parseAsString, import: parseAsString },
		{ history: "push" },
	);
	const selectedId = urlState.id;

	const { data, error, isLoading, refetch } = useGetEvalDatasetsQuery({ limit: SIDEBAR_LIMIT });
	const { data: foldersData, error: foldersError, isLoading: foldersLoading, refetch: refetchFolders } = useGetEvalDatasetFoldersQuery();
	const [deleteDataset] = useDeleteEvalDatasetMutation();
	const [duplicateDataset] = useDuplicateEvalDatasetMutation();
	const [moveDataset] = useMoveEvalDatasetMutation();
	const [moveFolder] = useMoveEvalDatasetFolderMutation();
	const [deleteFolder] = useDeleteEvalDatasetFolderMutation();

	const [datasetForm, setDatasetForm] = useState<{ folderId?: string } | null>(null);
	const [folderSheet, setFolderSheet] = useState<{ folder?: DatasetFolder; parentId?: string } | null>(null);
	const [datasetToDelete, setDatasetToDelete] = useState<DatasetSummary | null>(null);
	const [folderToDelete, setFolderToDelete] = useState<DatasetFolder | null>(null);

	const datasets = useMemo(() => data?.datasets ?? [], [data]);
	const folders = useMemo(() => foldersData?.folders ?? [], [foldersData]);

	const select = (id: string, importNext = false) =>
		void setUrlState({ id: id || null, version: null, split: null, import: importNext ? "1" : null });

	const attempt = async (action: () => Promise<unknown>, done?: string) => {
		try {
			await action();
			if (done) toast({ title: done });
			return true;
		} catch (actionError) {
			toast({ title: tCommon("error"), description: getErrorMessage(actionError), variant: "destructive" });
			return false;
		}
	};

	const confirmDeleteDataset = async () => {
		const dataset = datasetToDelete;
		setDatasetToDelete(null);
		if (!dataset) return;
		const deleted = await attempt(() => deleteDataset(dataset.id).unwrap(), t("evals.datasets.list.deleted", { name: dataset.name }));
		if (deleted && dataset.id === selectedId) select("");
	};

	const confirmDeleteFolder = async () => {
		const folder = folderToDelete;
		setFolderToDelete(null);
		if (folder) await attempt(() => deleteFolder(folder.id).unwrap(), t("promptRepo.toastFolderDeleted"));
	};

	if (isLoading || foldersLoading) {
		return <FullPageLoader />;
	}
	if (showLoadError(error, data)) {
		return (
			<LoadErrorView entity={t("evals.datasets.list.title")} error={error} onRetry={() => void refetch()} testId="datasets-load-error" />
		);
	}
	if (showLoadError(foldersError, foldersData)) {
		return (
			<LoadErrorView
				entity={t("evals.datasets.list.title")}
				error={foldersError}
				onRetry={() => void refetchFolders()}
				testId="datasets-load-error"
			/>
		);
	}

	const sheets = (
		<>
			<DatasetFormSheet
				open={datasetForm !== null}
				onOpenChange={(open) => !open && setDatasetForm(null)}
				folderId={datasetForm?.folderId}
				onCreated={(dataset, importNext) => select(dataset.id, importNext)}
			/>
			<DatasetFolderSheet
				open={folderSheet !== null}
				onOpenChange={(open) => !open && setFolderSheet(null)}
				folder={folderSheet?.folder}
				parentId={folderSheet?.parentId}
			/>
		</>
	);

	if (datasets.length === 0 && folders.length === 0 && !selectedId) {
		return (
			<div
				className="flex min-h-[80vh] w-full flex-col items-center justify-center gap-4 py-16 text-center"
				data-testid="datasets-empty-state"
			>
				<PageTitle title={t("evals.datasets.list.title")} />
				<Database className="text-muted-foreground h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
				<h1 className="text-muted-foreground text-xl font-medium">{t("evals.datasets.list.emptyTitle")}</h1>
				<p className="text-muted-foreground mx-auto max-w-[600px] text-sm">{t("evals.datasets.list.emptyDescription")}</p>
				{canCreate && (
					<Button onClick={() => setDatasetForm({})} data-testid="datasets-empty-create-btn">
						<Plus className="h-4 w-4" />
						{t("evals.datasets.list.newDataset")}
					</Button>
				)}
				{sheets}
			</div>
		);
	}

	return (
		<div className="no-padding-parent no-border-parent bg-background h-[calc(var(--app-content-viewport)_-_var(--app-bottom-padding))] w-full">
			{/* A selected dataset names itself in the topbar. */}
			{!selectedId && <PageTitle title={t("evals.datasets.list.title")}>{t("evals.datasets.list.description")}</PageTitle>}
			{sheets}

			<ResizablePanelGroup direction="horizontal" className="h-full">
				<ResizablePanel defaultSize={20} minSize={12} className="bg-card mr-1 overflow-hidden rounded-sm border">
					<DatasetSidebar
						folders={folders}
						datasets={datasets}
						selectedId={selectedId}
						canCreate={canCreate}
						canUpdate={canUpdate}
						canDelete={canDelete}
						onSelect={(id) => select(id)}
						onCreateDataset={(folderId) => setDatasetForm({ folderId })}
						onCreateFolder={(parentId) => setFolderSheet({ parentId })}
						onEditFolder={(folder) => setFolderSheet({ folder })}
						onDeleteFolder={setFolderToDelete}
						onDuplicate={(dataset) =>
							void attempt(async () => {
								const copy = await duplicateDataset({ id: dataset.id }).unwrap();
								// The copy is filed next to the dataset it was made from.
								if (dataset.folder_id) await moveDataset({ id: copy.dataset.id, folderId: dataset.folder_id }).unwrap();
								toast({ title: t("evals.datasets.list.duplicated", { name: copy.dataset.name }) });
							})
						}
						onDelete={setDatasetToDelete}
						onMove={(dataset, folderId) => void attempt(() => moveDataset({ id: dataset.id, folderId }).unwrap())}
						onMoveFolder={(folder, parentId) => void attempt(() => moveFolder({ id: folder.id, parentId }).unwrap())}
					/>
				</ResizablePanel>

				<ResizableHandle className="mr-1 bg-transparent" />

				<ResizablePanel defaultSize={80} minSize={50} className="bg-card overflow-hidden rounded-sm border">
					{selectedId ? (
						<DatasetDetail key={selectedId} />
					) : (
						<div className="text-muted-foreground flex h-full items-center justify-center" data-testid="datasets-none-selected">
							<div className="text-center">
								<p className="text-lg font-medium">{t("evals.datasets.folders.noneSelected")}</p>
								<p className="text-sm">{t("evals.datasets.folders.selectFromSidebar")}</p>
							</div>
						</div>
					)}
				</ResizablePanel>
			</ResizablePanelGroup>

			<AlertDialog open={datasetToDelete !== null} onOpenChange={(open) => !open && setDatasetToDelete(null)}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>{t("evals.datasets.list.deleteTitle", { name: datasetToDelete?.name ?? "" })}</AlertDialogTitle>
						<AlertDialogDescription>{t("evals.datasets.list.deleteDescription")}</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel data-testid="dataset-delete-cancel-btn">{tCommon("cancel")}</AlertDialogCancel>
						<AlertDialogAction onClick={() => void confirmDeleteDataset()} data-testid="dataset-delete-confirm-btn">
							{tCommon("delete")}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>

			<AlertDialog open={folderToDelete !== null} onOpenChange={(open) => !open && setFolderToDelete(null)}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>{t("promptRepo.deleteFolder")}</AlertDialogTitle>
						<AlertDialogDescription>
							{t("evals.datasets.folders.deleteConfirm", { name: folderToDelete?.name ?? "" })}
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel data-testid="dataset-folder-delete-cancel-btn">{tCommon("cancel")}</AlertDialogCancel>
						<AlertDialogAction onClick={() => void confirmDeleteFolder()} data-testid="dataset-folder-delete-confirm-btn">
							{tCommon("delete")}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</div>
	);
}