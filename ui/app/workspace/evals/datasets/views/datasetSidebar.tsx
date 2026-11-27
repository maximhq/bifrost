import { Button } from "@/components/ui/button";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "@/components/ui/dropdownMenu";
import { Input } from "@/components/ui/input";
import { ScrollArea } from "@/components/ui/scrollArea";
import type { DatasetFolder, DatasetSummary } from "@/lib/types/evalDatasets";
import { cn } from "@/lib/utils";
import { DragDropProvider, useDraggable, useDroppable } from "@dnd-kit/react";
import {
	ChevronDown,
	ChevronRight,
	Copy,
	Database,
	Download,
	Folder as FolderIcon,
	FolderOpen,
	MoreHorizontal,
	Pencil,
	Plus,
	Search,
	Trash2,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { buildDatasetTree, datasetExportUrl, type DatasetTreeNode, folderWithDescendants } from "./datasetUtils";

const ROOT_DROP = "dataset-root";
const FOLDER_DROP = "dataset-folder-";
const DATASET_DRAG = "dataset-";
const FOLDER_DRAG = "folder-";

export interface DatasetSidebarProps {
	folders: DatasetFolder[];
	datasets: DatasetSummary[];
	selectedId: string;
	canCreate: boolean;
	canUpdate: boolean;
	canDelete: boolean;
	onSelect: (id: string) => void;
	// folderId is set when the dataset is started from a folder's menu.
	onCreateDataset: (folderId?: string) => void;
	// parentId is set when the folder is started from another folder's menu.
	onCreateFolder: (parentId?: string) => void;
	onEditFolder: (folder: DatasetFolder) => void;
	onDeleteFolder: (folder: DatasetFolder) => void;
	onDuplicate: (dataset: DatasetSummary) => void;
	onDelete: (dataset: DatasetSummary) => void;
	// folderId is null when the dataset is dropped outside every folder.
	onMove: (dataset: DatasetSummary, folderId: string | null) => void;
	// parentId is null when the folder is dropped at the top level.
	onMoveFolder: (folder: DatasetFolder, parentId: string | null) => void;
}

// DatasetSidebar lists the datasets in their folders, the way the prompt repository
// lists prompts: search on top, and a tree of folders that open and close. Both
// datasets and folders can be dragged: onto a folder to move them into it, or onto
// the area below the tree to move them to the top level.
export function DatasetSidebar(props: DatasetSidebarProps) {
	const { folders, datasets, selectedId, canCreate, canUpdate, onSelect, onCreateDataset, onCreateFolder, onMove, onMoveFolder } = props;
	const { t } = useTranslation("config");
	const [expanded, setExpanded] = useState<Set<string>>(new Set());
	const [search, setSearch] = useState("");
	const [dragOver, setDragOver] = useState<string | null>(null);

	const folderById = useMemo(() => new Map(folders.map((folder) => [folder.id, folder])), [folders]);

	// The folders on the way to the selected dataset open on their own.
	const selectedFolder = datasets.find((dataset) => dataset.id === selectedId)?.folder_id;
	useEffect(() => {
		if (!selectedFolder) return;
		setExpanded((current) => {
			const next = new Set(current);
			for (let at = folderById.get(selectedFolder); at && !next.has(at.id); at = at.parent_id ? folderById.get(at.parent_id) : undefined) {
				next.add(at.id);
			}
			return next.size === current.size ? current : next;
		});
	}, [selectedFolder, folderById]);

	const toggle = (folderId: string) =>
		setExpanded((current) => {
			const next = new Set(current);
			if (!next.delete(folderId)) next.add(folderId);
			return next;
		});

	const grouped = useMemo(() => buildDatasetTree(folders, datasets, search), [folders, datasets, search]);
	const byId = useMemo(() => new Map(datasets.map((dataset) => [dataset.id, dataset])), [datasets]);
	const searching = search.trim() !== "";

	return (
		<DragDropProvider
			onDragOver={(event) => {
				if (canUpdate) setDragOver((event.operation.target?.id as string | undefined) ?? null);
			}}
			onDragEnd={(event) => {
				setDragOver(null);
				if (!canUpdate || event.canceled) return;
				const source = event.operation.source?.id as string | undefined;
				const target = event.operation.target?.id as string | undefined;
				if (!source || !target) return;
				let folderId: string | null;
				if (target === ROOT_DROP) folderId = null;
				else if (target.startsWith(FOLDER_DROP)) folderId = target.slice(FOLDER_DROP.length);
				else return;
				// A folder is checked before a dataset: its drag ID is the more specific one.
				if (source.startsWith(FOLDER_DRAG)) {
					const folder = folderById.get(source.slice(FOLDER_DRAG.length));
					if (!folder || (folder.parent_id ?? null) === folderId) return;
					// A folder cannot go into itself or into a folder inside it.
					if (folderId && folderWithDescendants(folders, folder.id).has(folderId)) return;
					onMoveFolder(folder, folderId);
					return;
				}
				if (!source.startsWith(DATASET_DRAG)) return;
				const dataset = byId.get(source.slice(DATASET_DRAG.length));
				if (dataset && (dataset.folder_id ?? null) !== folderId) onMove(dataset, folderId);
			}}
		>
			<div className="flex h-full flex-col" data-testid="datasets-sidebar">
				<div className="flex items-center gap-2 border-b p-3">
					<div className="relative grow">
						<Search className="text-muted-foreground absolute top-1/2 left-2.5 h-4 w-4 -translate-y-1/2" />
						<Input
							className="h-8 pl-8"
							value={search}
							placeholder={t("evals.datasets.list.searchPlaceholder")}
							aria-label={t("evals.datasets.list.searchPlaceholder")}
							onChange={(event) => setSearch(event.target.value)}
							data-testid="datasets-search-input"
						/>
					</div>
					{canCreate && (
						<DropdownMenu>
							<DropdownMenuTrigger asChild>
								<Button
									variant="outline"
									className="h-8 w-8 shrink-0 bg-transparent"
									aria-label={t("evals.datasets.folders.addAria")}
									data-testid="datasets-add-menu"
								>
									<Plus className="h-3.5 w-3.5" />
								</Button>
							</DropdownMenuTrigger>
							<DropdownMenuContent align="end">
								<DropdownMenuItem onClick={() => onCreateDataset()} data-testid="datasets-create-btn">
									{t("evals.datasets.list.newDataset")}
								</DropdownMenuItem>
								<DropdownMenuItem onClick={() => onCreateFolder()} data-testid="datasets-create-folder-btn">
									{t("promptRepo.newFolder")}
								</DropdownMenuItem>
							</DropdownMenuContent>
						</DropdownMenu>
					)}
				</div>

				<ScrollArea className="grow overflow-y-auto" viewportClassName="no-table viewport-table-height-full">
					<div className="flex min-h-full flex-col p-2 px-3">
						{grouped.folders.length === 0 && grouped.root.length === 0 ? (
							<div className="text-muted-foreground py-8 text-center text-sm">
								{searching ? t("promptRepo.noResultsFound") : t("evals.datasets.list.emptyTitle")}
							</div>
						) : (
							<>
								{grouped.folders.map((node) => (
									<FolderRow
										key={node.folder.id}
										{...props}
										node={node}
										isOpen={(id) => expanded.has(id) || searching}
										dragOver={dragOver}
										onToggle={toggle}
									/>
								))}
								<RootZone dragOver={dragOver === ROOT_DROP}>
									{grouped.root.map((dataset) => (
										<DatasetRow
											key={dataset.id}
											{...props}
											dataset={dataset}
											selected={dataset.id === selectedId}
											onSelect={() => onSelect(dataset.id)}
										/>
									))}
								</RootZone>
							</>
						)}
					</div>
				</ScrollArea>
			</div>
		</DragDropProvider>
	);
}

// RootZone holds the datasets that are in no folder, and takes a dataset dropped out of one.
function RootZone({ dragOver, children }: { dragOver: boolean; children: React.ReactNode }) {
	const { ref } = useDroppable({ id: ROOT_DROP });
	return (
		<div
			ref={ref}
			className={cn("min-h-8 grow rounded-sm transition-colors", dragOver && "bg-primary/10 ring-primary/30 ring-1")}
			data-testid="datasets-root-zone"
		>
			{children}
		</div>
	);
}

type FolderRowProps = Omit<DatasetSidebarProps, "folders" | "datasets"> & {
	node: DatasetTreeNode<DatasetFolder, DatasetSummary>;
	isOpen: (folderId: string) => boolean;
	// The drop target the pointer is over, if any.
	dragOver: string | null;
	onToggle: (folderId: string) => void;
};

// FolderRow is one folder and, when open, the folders and datasets in it. The header
// row is both what gets dragged and what takes drops. The drop target is the row, not
// the whole open folder, so that it never overlaps the folders nested inside it.
function FolderRow(props: FolderRowProps) {
	const { node, isOpen, dragOver, onToggle, selectedId, onSelect, canCreate, canUpdate, canDelete } = props;
	const { folder, folders: nested, datasets } = node;
	const { t } = useTranslation("config");
	const { ref: dropRef } = useDroppable({ id: `${FOLDER_DROP}${folder.id}` });
	const { ref: dragRef, isDragging } = useDraggable({ id: `${FOLDER_DRAG}${folder.id}`, disabled: !canUpdate });
	const headerRef = useCallback(
		(element: HTMLDivElement | null) => {
			dropRef(element);
			dragRef(element);
		},
		[dropRef, dragRef],
	);
	const open = isOpen(folder.id);
	const over = dragOver === `${FOLDER_DROP}${folder.id}`;
	const Chevron = open ? ChevronDown : ChevronRight;
	const Icon = open ? FolderOpen : FolderIcon;

	return (
		<div className={cn("mb-1 last:mb-0", isDragging && "opacity-50")}>
			<div
				ref={headerRef}
				className={cn(
					"hover:bg-muted/50 group relative flex h-[30px] items-center gap-1 rounded-sm px-2 transition-colors",
					over && "bg-primary/10 ring-primary/30 ring-1",
				)}
			>
				<button
					type="button"
					className="flex min-w-0 flex-1 cursor-pointer items-center gap-1 text-left"
					aria-expanded={open}
					aria-label={t("promptRepo.toggleFolderAria")}
					onClick={() => {
						// A click that ends a drag does not open or close the folder.
						if (!isDragging) onToggle(folder.id);
					}}
					data-testid={`dataset-folder-${folder.name}`}
				>
					<Chevron className="text-muted-foreground h-4 w-4 shrink-0" />
					<Icon className="text-muted-foreground h-4 w-4 shrink-0" />
					<span className="flex-1 truncate text-sm font-medium">{folder.name}</span>
				</button>
				<span className="text-muted-foreground mr-1 shrink-0 text-xs group-focus-within:invisible group-hover:invisible">
					{datasets.length}
				</span>
				{(canCreate || canUpdate || canDelete) && (
					<DropdownMenu>
						<DropdownMenuTrigger asChild className="absolute top-1/2 right-2 -translate-y-1/2">
							<Button
								variant="ghost"
								size="icon"
								className="h-6 w-6 shrink-0 opacity-0 group-focus-within:opacity-100 group-hover:opacity-100 focus-visible:opacity-100"
								aria-label={t("promptRepo.folderActionsAria")}
								data-testid={`dataset-folder-actions-${folder.name}`}
							>
								<MoreHorizontal className="h-4 w-4" />
							</Button>
						</DropdownMenuTrigger>
						<DropdownMenuContent align="end">
							{canCreate && (
								<DropdownMenuItem onClick={() => props.onCreateDataset(folder.id)} data-testid="dataset-folder-new-dataset">
									<Plus className="h-4 w-4" />
									{t("evals.datasets.list.newDataset")}
								</DropdownMenuItem>
							)}
							{canCreate && (
								<DropdownMenuItem onClick={() => props.onCreateFolder(folder.id)} data-testid="dataset-folder-new-folder">
									<FolderIcon className="h-4 w-4" />
									{t("promptRepo.newFolder")}
								</DropdownMenuItem>
							)}
							{canCreate && (canUpdate || canDelete) && <DropdownMenuSeparator />}
							{canUpdate && (
								<DropdownMenuItem onClick={() => props.onEditFolder(folder)} data-testid="dataset-folder-edit">
									<Pencil className="h-4 w-4" />
									{t("promptRepo.editFolder")}
								</DropdownMenuItem>
							)}
							{canDelete && (
								<DropdownMenuItem variant="destructive" onClick={() => props.onDeleteFolder(folder)} data-testid="dataset-folder-delete">
									<Trash2 className="h-4 w-4" />
									{t("promptRepo.deleteFolder")}
								</DropdownMenuItem>
							)}
						</DropdownMenuContent>
					</DropdownMenu>
				)}
			</div>

			{open && (
				<div className="ml-4 border-l pl-2">
					{nested.map((child) => (
						<FolderRow key={child.folder.id} {...props} node={child} />
					))}
					{datasets.map((dataset) => (
						<DatasetRow
							key={dataset.id}
							{...props}
							dataset={dataset}
							selected={dataset.id === selectedId}
							onSelect={() => onSelect(dataset.id)}
						/>
					))}
					{nested.length === 0 && datasets.length === 0 && (
						<div className="text-muted-foreground py-2 pl-4 text-xs">
							{over ? t("promptRepo.dropHere") : t("evals.datasets.folders.empty")}
						</div>
					)}
				</div>
			)}
		</div>
	);
}

type DatasetRowProps = Pick<DatasetSidebarProps, "canCreate" | "canUpdate" | "canDelete" | "onDuplicate" | "onDelete"> & {
	dataset: DatasetSummary;
	selected: boolean;
	onSelect: () => void;
};

function DatasetRow({ dataset, selected, onSelect, canCreate, canUpdate, canDelete, onDuplicate, onDelete }: DatasetRowProps) {
	const { t, i18n } = useTranslation("config");
	const { t: tCommon } = useTranslation("common");
	const { ref, isDragging } = useDraggable({ id: `${DATASET_DRAG}${dataset.id}`, disabled: !canUpdate });

	return (
		<div
			ref={ref}
			className={cn(
				"group relative mb-1 flex h-[30px] items-center gap-2 rounded-sm px-2 last:mb-0",
				selected ? "bg-primary/10 text-primary" : "hover:bg-muted/50",
				isDragging && "opacity-50",
			)}
		>
			<button
				type="button"
				className="flex min-w-0 flex-1 cursor-pointer items-center gap-2 text-left"
				aria-current={selected ? "true" : undefined}
				onClick={() => {
					// A click that ends a drag is not a selection.
					if (!isDragging) onSelect();
				}}
				data-testid={`dataset-row-${dataset.name}`}
			>
				<Database className="h-4 w-4 shrink-0" />
				<span className="flex-1 truncate text-sm">{dataset.name}</span>
			</button>
			<span className="text-muted-foreground shrink-0 text-xs group-focus-within:invisible group-hover:invisible">
				{dataset.row_count.toLocaleString(i18n.language)}
			</span>
			<DropdownMenu>
				<DropdownMenuTrigger asChild className="absolute top-1/2 right-2 -translate-y-1/2">
					<Button
						variant="ghost"
						size="icon"
						className="h-6 w-6 opacity-0 group-focus-within:opacity-100 group-hover:opacity-100 focus-visible:opacity-100"
						aria-label={t("evals.datasets.list.actionsFor", { name: dataset.name })}
						data-testid={`dataset-actions-${dataset.name}`}
					>
						<MoreHorizontal className="h-4 w-4" />
					</Button>
				</DropdownMenuTrigger>
				<DropdownMenuContent align="end">
					{canCreate && (
						<DropdownMenuItem onClick={() => onDuplicate(dataset)} data-testid={`dataset-duplicate-${dataset.name}`}>
							<Copy className="h-4 w-4" />
							{t("evals.datasets.list.duplicate")}
						</DropdownMenuItem>
					)}
					<DropdownMenuItem asChild>
						<a href={datasetExportUrl(dataset.id, "csv")} download data-testid={`dataset-export-csv-${dataset.name}`}>
							<Download className="h-4 w-4" />
							{t("evals.datasets.export.csv")}
						</a>
					</DropdownMenuItem>
					<DropdownMenuItem asChild>
						<a href={datasetExportUrl(dataset.id, "jsonl")} download data-testid={`dataset-export-jsonl-${dataset.name}`}>
							<Download className="h-4 w-4" />
							{t("evals.datasets.export.jsonl")}
						</a>
					</DropdownMenuItem>
					{canDelete && (
						<DropdownMenuItem variant="destructive" onClick={() => onDelete(dataset)} data-testid={`dataset-delete-${dataset.name}`}>
							<Trash2 className="h-4 w-4" />
							{tCommon("delete")}
						</DropdownMenuItem>
					)}
				</DropdownMenuContent>
			</DropdownMenu>
		</div>
	);
}