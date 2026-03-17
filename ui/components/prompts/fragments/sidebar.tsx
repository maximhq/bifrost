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
import { cn } from "@/lib/utils";
import { Folder, Prompt } from "@/lib/types/prompts";
import {
	ChevronDown,
	ChevronRight,
	FileText,
	Folder as FolderIcon,
	FolderOpen,
	MoreHorizontal,
	Pencil,
	Plus,
	PlusIcon,
	Search,
	Trash2,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { usePathname } from "next/navigation";
import { DragDropProvider, useDraggable, useDroppable } from "@dnd-kit/react";
import { usePromptContext } from "../context";

export function PromptSidebar() {
	const {
		folders,
		prompts,
		selectedPromptId,
		handleSelectPrompt: onSelectPrompt,
		setFolderSheet,
		setDeleteFolderDialog,
		setPromptSheet,
		setDeletePromptDialog,
		handleMovePrompt: onMovePrompt,
		canCreate,
		canUpdate,
		canDelete,
	} = usePromptContext();

	const onCreateFolder = useCallback(() => setFolderSheet({ open: true }), [setFolderSheet]);
	const onEditFolder = useCallback((folder: Folder) => setFolderSheet({ open: true, folder }), [setFolderSheet]);
	const onDeleteFolder = useCallback((folder: Folder) => setDeleteFolderDialog({ open: true, folder }), [setDeleteFolderDialog]);
	const onCreatePrompt = useCallback((folderId?: string) => setPromptSheet({ open: true, folderId }), [setPromptSheet]);
	const onEditPrompt = useCallback((prompt: Prompt) => setPromptSheet({ open: true, prompt }), [setPromptSheet]);
	const onDeletePrompt = useCallback((prompt: Prompt) => setDeletePromptDialog({ open: true, prompt }), [setDeletePromptDialog]);
	const pathname = usePathname();
	const [expandedFolders, setExpandedFolders] = useState<Set<string>>(new Set());
	const [searchQuery, setSearchQuery] = useState("");
	const [dragOverTarget, setDragOverTarget] = useState<string | null>(null);

	// Auto-expand the folder containing the selected prompt
	useEffect(() => {
		if (!selectedPromptId) return;
		const prompt = prompts.find((p) => p.id === selectedPromptId);
		if (prompt?.folder_id) {
			setExpandedFolders((prev) => {
				if (prev.has(prompt.folder_id!)) return prev;
				const next = new Set(prev);
				next.add(prompt.folder_id!);
				return next;
			});
		}
	}, [selectedPromptId, prompts]);

	const toggleFolder = useCallback((folderId: string) => {
		setExpandedFolders((prev) => {
			const next = new Set(prev);
			if (next.has(folderId)) {
				next.delete(folderId);
			} else {
				next.add(folderId);
			}
			return next;
		});
	}, []);

	// Group prompts by folder, root prompts have no folder_id
	const { promptsByFolder, rootPrompts } = useMemo(() => {
		const map = new Map<string, Prompt[]>();
		const root: Prompt[] = [];
		for (const prompt of prompts) {
			if (!prompt.folder_id) {
				root.push(prompt);
			} else {
				const list = map.get(prompt.folder_id) || [];
				list.push(prompt);
				map.set(prompt.folder_id, list);
			}
		}
		return { promptsByFolder: map, rootPrompts: root };
	}, [prompts]);

	// Filter folders and prompts based on search
	const filteredData = useMemo(() => {
		if (!searchQuery.trim()) {
			return { folders, promptsByFolder, rootPrompts };
		}

		const query = searchQuery.toLowerCase();
		const matchedFolderIds = new Set<string>();
		const filteredPromptsByFolder = new Map<string, Prompt[]>();
		const filteredRootPrompts: Prompt[] = [];

		for (const prompt of prompts) {
			if (prompt.name.toLowerCase().includes(query)) {
				if (!prompt.folder_id) {
					filteredRootPrompts.push(prompt);
				} else {
					matchedFolderIds.add(prompt.folder_id);
					const list = filteredPromptsByFolder.get(prompt.folder_id) || [];
					list.push(prompt);
					filteredPromptsByFolder.set(prompt.folder_id, list);
				}
			}
		}

		const filteredFolders = folders.filter((folder) => folder.name.toLowerCase().includes(query) || matchedFolderIds.has(folder.id));

		return { folders: filteredFolders, promptsByFolder: filteredPromptsByFolder, rootPrompts: filteredRootPrompts };
	}, [folders, prompts, promptsByFolder, rootPrompts, searchQuery]);

	// Prompt lookup for drag events
	const promptMap = useMemo(() => {
		const map = new Map<string, Prompt>();
		for (const p of prompts) map.set(p.id, p);
		return map;
	}, [prompts]);

	return (
		<DragDropProvider
			onDragOver={(event) => {
				if (!canUpdate) return;
				const targetId = event.operation.target?.id as string | undefined;
				setDragOverTarget(targetId ?? null);
			}}
			onDragEnd={(event) => {
				setDragOverTarget(null);
				if (!canUpdate) return;
				if (event.canceled || !onMovePrompt) return;

				const sourceId = event.operation.source?.id as string | undefined;
				const targetId = event.operation.target?.id as string | undefined;
				if (!sourceId || !targetId) return;

				const promptId = sourceId.startsWith("prompt-") ? sourceId.slice(7) : null;
				if (!promptId) return;

				const prompt = promptMap.get(promptId);
				if (!prompt) return;

				let targetFolderId: string | null = null;
				if (targetId === "root-drop-zone") {
					targetFolderId = null;
				} else if (targetId.startsWith("folder-")) {
					targetFolderId = targetId.slice(7);
				} else {
					return;
				}
				if ((prompt.folder_id ?? null) === targetFolderId) return;
				onMovePrompt(promptId, targetFolderId);
			}}
		>
			<div className="flex h-full flex-col">
				{/* Search */}
				<div className="flex items-center gap-2 border-b p-3">
					<div className="relative grow">
						<Search className="text-muted-foreground absolute top-1/2 left-2.5 h-4 w-4 -translate-y-1/2" />
						<Input
							placeholder="Search prompts..."
							value={searchQuery}
							onChange={(e) => setSearchQuery(e.target.value)}
							data-testid="sidebar-search"
							className="h-8 pl-8"
						/>
					</div>
					{canCreate && (
						<DropdownMenu>
							<DropdownMenuTrigger asChild>
								<Button
									variant="outline"
									className="h-8 w-8 shrink-0 bg-transparent"
									data-testid="sidebar-create-menu"
									aria-label="Create prompt or folder"
								>
									<PlusIcon className="h-3.5 w-3.5" />
								</Button>
							</DropdownMenuTrigger>
							<DropdownMenuContent align="end">
								<DropdownMenuItem
									data-testid="sidebar-create-prompt"
									onClick={(e) => {
										e.stopPropagation();
										onCreatePrompt();
									}}
								>
									New Prompt
								</DropdownMenuItem>
								<DropdownMenuItem
									data-testid="sidebar-create-folder"
									onClick={(e) => {
										e.stopPropagation();
										onCreateFolder();
									}}
								>
									New Folder
								</DropdownMenuItem>
							</DropdownMenuContent>
						</DropdownMenu>
					)}
				</div>

				<ScrollArea className="grow overflow-y-auto" viewportClassName="no-table viewport-table-height-full">
					<div className="flex flex-col p-2 px-3">
						{filteredData.folders.length === 0 && filteredData.rootPrompts.length === 0 ? (
							<div className="text-muted-foreground py-8 text-center text-sm">{searchQuery ? "No results found" : "No prompts yet"}</div>
						) : (
							<>
								{filteredData.folders.map((folder) => (
									<DroppableFolder
										key={folder.id}
										folder={folder}
										prompts={filteredData.promptsByFolder.get(folder.id) || promptsByFolder.get(folder.id) || []}
										isExpanded={expandedFolders.has(folder.id) || !!searchQuery}
										isDragOver={dragOverTarget === `folder-${folder.id}`}
										selectedPromptId={selectedPromptId}
										onToggle={() => toggleFolder(folder.id)}
										onSelectPrompt={onSelectPrompt}
										onEdit={() => onEditFolder(folder)}
										onDelete={() => onDeleteFolder(folder)}
										onCreatePrompt={() => onCreatePrompt(folder.id)}
										onEditPrompt={onEditPrompt}
										onDeletePrompt={onDeletePrompt}
										canCreate={canCreate}
										canUpdate={canUpdate}
										canDelete={canDelete}
									/>
								))}
								<RootDropZone
									isDragOver={dragOverTarget === "root-drop-zone"}
									rootPrompts={filteredData.rootPrompts}
									selectedPromptId={selectedPromptId}
									onSelectPrompt={onSelectPrompt}
									onEditPrompt={onEditPrompt}
									onDeletePrompt={onDeletePrompt}
									canUpdate={canUpdate}
									canDelete={canDelete}
								/>
							</>
						)}
					</div>
				</ScrollArea>
			</div>
		</DragDropProvider>
	);
}

interface RootDropZoneProps {
	isDragOver: boolean;
	rootPrompts: Prompt[];
	selectedPromptId?: string | null;
	onSelectPrompt: (promptId: string) => void;
	onEditPrompt: (prompt: Prompt) => void;
	onDeletePrompt: (prompt: Prompt) => void;
	canUpdate: boolean;
	canDelete: boolean;
}

function RootDropZone({
	isDragOver,
	rootPrompts,
	selectedPromptId,
	onSelectPrompt,
	onEditPrompt,
	onDeletePrompt,
	canUpdate,
	canDelete,
}: RootDropZoneProps) {
	const { ref } = useDroppable({ id: "root-drop-zone" });

	return (
		<div ref={ref} className={cn("min-h-[8px] grow rounded-sm transition-colors", isDragOver && "bg-primary/10 ring-primary/30 ring-1")}>
			{rootPrompts.map((prompt) => (
				<DraggablePromptItem
					key={prompt.id}
					prompt={prompt}
					isSelected={selectedPromptId === prompt.id}
					onSelect={() => onSelectPrompt(prompt.id)}
					onEdit={() => onEditPrompt(prompt)}
					onDelete={() => onDeletePrompt(prompt)}
					canUpdate={canUpdate}
					canDelete={canDelete}
				/>
			))}
		</div>
	);
}

interface DroppableFolderProps {
	folder: Folder;
	prompts: Prompt[];
	isExpanded: boolean;
	isDragOver: boolean;
	selectedPromptId?: string | null;
	onToggle: () => void;
	onSelectPrompt: (promptId: string) => void;
	onEdit: () => void;
	onDelete: () => void;
	onCreatePrompt: () => void;
	onEditPrompt: (prompt: Prompt) => void;
	onDeletePrompt: (prompt: Prompt) => void;
	canCreate: boolean;
	canUpdate: boolean;
	canDelete: boolean;
}

function DroppableFolder({
	folder,
	prompts,
	isExpanded,
	isDragOver,
	selectedPromptId,
	onToggle,
	onSelectPrompt,
	onEdit,
	onDelete,
	onCreatePrompt,
	onEditPrompt,
	onDeletePrompt,
	canCreate,
	canUpdate,
	canDelete,
}: DroppableFolderProps) {
	const { ref } = useDroppable({ id: `folder-${folder.id}` });
	const showActions = canCreate || canUpdate || canDelete;

	return (
		<div ref={ref} className="mb-1 last:mb-0">
			<div
				className={cn(
					"hover:bg-muted/50 group relative flex h-[30px] cursor-pointer items-center gap-1 rounded-sm px-2 transition-colors",
					isDragOver && "bg-primary/10 ring-primary/30 ring-1",
				)}
				onClick={onToggle}
				data-testid={`sidebar-folder-${folder.id}`}
			>
				<button className="flex shrink-0 items-center" aria-label="Toggle folder">
					{isExpanded ? (
						<ChevronDown className="text-muted-foreground h-4 w-4" />
					) : (
						<ChevronRight className="text-muted-foreground h-4 w-4" />
					)}
				</button>
				{isExpanded ? (
					<FolderOpen className="text-muted-foreground h-4 w-4 shrink-0" />
				) : (
					<FolderIcon className="text-muted-foreground h-4 w-4 shrink-0" />
				)}
				<span className="flex-1 truncate text-sm font-medium">{folder.name}</span>
				<span className="text-muted-foreground mr-1 shrink-0 text-xs">{prompts.length}</span>
				{showActions && (
					<DropdownMenu>
						<DropdownMenuTrigger asChild onClick={(e) => e.stopPropagation()} className="bg-card absolute top-1/2 right-2 -translate-y-1/2">
							<Button
								variant="ghost"
								size="icon"
								className="h-6 w-6 shrink-0 opacity-0 group-focus-within:opacity-100 group-hover:opacity-100 focus-visible:opacity-100"
								data-testid={`sidebar-folder-actions-${folder.id}`}
								aria-label="Folder actions"
							>
								<MoreHorizontal className="h-4 w-4" />
							</Button>
						</DropdownMenuTrigger>
						<DropdownMenuContent align="end">
							{canCreate && (
								<DropdownMenuItem
									data-testid="folder-create-prompt"
									onClick={(e) => {
										e.stopPropagation();
										onCreatePrompt();
									}}
								>
									<Plus className="mr-2 h-4 w-4" />
									New Prompt
								</DropdownMenuItem>
							)}
							{canCreate && (canUpdate || canDelete) && <DropdownMenuSeparator />}
							{canUpdate && (
								<DropdownMenuItem
									data-testid="folder-action-edit"
									onClick={(e) => {
										e.stopPropagation();
										onEdit();
									}}
								>
									<Pencil className="h-4 w-4" />
									Edit Folder
								</DropdownMenuItem>
							)}
							{canDelete && (
								<DropdownMenuItem
									className="text-destructive"
									data-testid="folder-action-delete"
									onClick={(e) => {
										e.stopPropagation();
										onDelete();
									}}
								>
									<Trash2 className="h-4 w-4" />
									Delete Folder
								</DropdownMenuItem>
							)}
						</DropdownMenuContent>
					</DropdownMenu>
				)}
			</div>

			{isExpanded && (
				<div className="ml-4 border-l pl-2">
					{prompts.length === 0 ? (
						<div className="text-muted-foreground py-2 pl-4 text-xs">{isDragOver ? "Drop here" : "No prompts"}</div>
					) : (
						prompts.map((prompt) => (
							<DraggablePromptItem
								key={prompt.id}
								prompt={prompt}
								isSelected={selectedPromptId === prompt.id}
								onSelect={() => onSelectPrompt(prompt.id)}
								onEdit={() => onEditPrompt(prompt)}
								onDelete={() => onDeletePrompt(prompt)}
								canUpdate={canUpdate}
								canDelete={canDelete}
							/>
						))
					)}
				</div>
			)}
		</div>
	);
}

interface DraggablePromptItemProps {
	prompt: Prompt;
	isSelected: boolean;
	onSelect: () => void;
	onEdit: () => void;
	onDelete: () => void;
	canUpdate: boolean;
	canDelete: boolean;
}

function DraggablePromptItem({ prompt, isSelected, onSelect, onEdit, onDelete, canUpdate, canDelete }: DraggablePromptItemProps) {
	const { ref, isDragging } = useDraggable({ id: `prompt-${prompt.id}`, disabled: !canUpdate });
	const showActions = canUpdate || canDelete;

	return (
		<div
			ref={ref}
			data-testid={`sidebar-prompt-${prompt.id}`}
			className={cn(
				"group mb-1 flex h-[30px] cursor-pointer items-center gap-2 rounded-sm px-2 last:mb-0",
				isSelected ? "bg-primary/10 text-primary" : "hover:bg-muted/50",
				isDragging && "opacity-50",
			)}
			onClick={(e) => {
				// Don't navigate if this was a drag
				if (isDragging) return;
				onSelect();
			}}
		>
			<FileText className="h-4 w-4 shrink-0" />
			<span className="flex-1 truncate text-sm">{prompt.name}</span>
			{showActions && (
				<DropdownMenu>
					<DropdownMenuTrigger asChild onClick={(e) => e.stopPropagation()}>
						<Button
							variant="ghost"
							size="icon"
							className="h-6 w-6 opacity-0 group-focus-within:opacity-100 group-hover:opacity-100 focus-visible:opacity-100"
							data-testid={`sidebar-prompt-actions-${prompt.id}`}
							aria-label="Prompt actions"
						>
							<MoreHorizontal className="h-4 w-4" />
						</Button>
					</DropdownMenuTrigger>
					<DropdownMenuContent align="end">
						{canUpdate && (
							<DropdownMenuItem
								className="cursor-pointer"
								data-testid="prompt-action-rename"
								onClick={(e) => {
									e.stopPropagation();
									onEdit();
								}}
							>
								<Pencil className="h-4 w-4" />
								Rename
							</DropdownMenuItem>
						)}
						{canDelete && (
							<DropdownMenuItem
								className="text-destructive hover:text-destructive cursor-pointer"
								data-testid="prompt-action-delete"
								onClick={(e) => {
									e.stopPropagation();
									onDelete();
								}}
							>
								<Trash2 className="text-destructive h-4 w-4" />
								Delete
							</DropdownMenuItem>
						)}
					</DropdownMenuContent>
				</DropdownMenu>
			)}
		</div>
	);
}
