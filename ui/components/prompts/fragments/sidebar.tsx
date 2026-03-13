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

	const getPromptHref = useCallback(
		(promptId: string) => {
			return `${pathname}?promptId=${encodeURIComponent(promptId)}`;
		},
		[pathname],
	);

	// Prompt lookup for drag events
	const promptMap = useMemo(() => {
		const map = new Map<string, Prompt>();
		for (const p of prompts) map.set(p.id, p);
		return map;
	}, [prompts]);

	return (
		<DragDropProvider
			onDragOver={(event) => {
				const targetId = event.operation.target?.id as string | undefined;
				setDragOverTarget(targetId ?? null);
			}}
			onDragEnd={(event) => {
				setDragOverTarget(null);
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
							className="h-8 pl-8"
						/>
					</div>
					<DropdownMenu>
						<DropdownMenuTrigger asChild>
							<Button variant="outline" className="h-8 w-8 shrink-0 bg-transparent" aria-label="Create prompt or folder">
								<PlusIcon className="h-3.5 w-3.5" />
							</Button>
						</DropdownMenuTrigger>
						<DropdownMenuContent align="end">
							<DropdownMenuItem
								onClick={(e) => {
									e.stopPropagation();
									onCreatePrompt();
								}}
							>
								New Prompt
							</DropdownMenuItem>
							<DropdownMenuItem
								onClick={(e) => {
									e.stopPropagation();
									onCreateFolder();
								}}
							>
								New Folder
							</DropdownMenuItem>
						</DropdownMenuContent>
					</DropdownMenu>
				</div>

				{/* Tree content */}
				<ScrollArea className="grow overflow-y-auto" viewportClassName="no-table viewport-table-height-full">
					<div className="flex h-full flex-col p-2">
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
										getPromptHref={getPromptHref}
										onEdit={() => onEditFolder(folder)}
										onDelete={() => onDeleteFolder(folder)}
										onCreatePrompt={() => onCreatePrompt(folder.id)}
										onEditPrompt={onEditPrompt}
										onDeletePrompt={onDeletePrompt}
									/>
								))}
								<RootDropZone
									isDragOver={dragOverTarget === "root-drop-zone"}
									rootPrompts={filteredData.rootPrompts}
									selectedPromptId={selectedPromptId}
									getPromptHref={getPromptHref}
									onSelectPrompt={onSelectPrompt}
									onEditPrompt={onEditPrompt}
									onDeletePrompt={onDeletePrompt}
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
	getPromptHref: (promptId: string) => string;
	onSelectPrompt: (promptId: string) => void;
	onEditPrompt: (prompt: Prompt) => void;
	onDeletePrompt: (prompt: Prompt) => void;
}

function RootDropZone({
	isDragOver,
	rootPrompts,
	selectedPromptId,
	getPromptHref,
	onSelectPrompt,
	onEditPrompt,
	onDeletePrompt,
}: RootDropZoneProps) {
	const { ref } = useDroppable({ id: "root-drop-zone" });

	return (
		<div ref={ref} className={cn("min-h-[8px] grow rounded-sm transition-colors", isDragOver && "bg-primary/10 ring-primary/30 ring-1")}>
			{rootPrompts.map((prompt) => (
				<DraggablePromptItem
					key={prompt.id}
					prompt={prompt}
					isSelected={selectedPromptId === prompt.id}
					href={getPromptHref(prompt.id)}
					onSelect={() => onSelectPrompt(prompt.id)}
					onEdit={() => onEditPrompt(prompt)}
					onDelete={() => onDeletePrompt(prompt)}
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
	getPromptHref: (promptId: string) => string;
	onEdit: () => void;
	onDelete: () => void;
	onCreatePrompt: () => void;
	onEditPrompt: (prompt: Prompt) => void;
	onDeletePrompt: (prompt: Prompt) => void;
}

function DroppableFolder({
	folder,
	prompts,
	isExpanded,
	isDragOver,
	selectedPromptId,
	onToggle,
	onSelectPrompt,
	getPromptHref,
	onEdit,
	onDelete,
	onCreatePrompt,
	onEditPrompt,
	onDeletePrompt,
}: DroppableFolderProps) {
	const { ref } = useDroppable({ id: `folder-${folder.id}` });

	return (
		<div ref={ref} className="mb-1 last:mb-0">
			<div
				className={cn(
					"hover:bg-muted/50 group relative flex cursor-pointer items-center gap-1 rounded-sm px-2 h-[30px] transition-colors",
					isDragOver && "bg-primary/10 ring-primary/30 ring-1",
				)}
				onClick={onToggle}
			>
				<button className="flex shrink-0 items-center" aria-label="Toggle folder">
					{isExpanded ? (
						<ChevronDown className="text-muted-foreground h-4 w-4" />
					) : (
						<ChevronRight className="text-muted-foreground h-4 w-4" />
					)}
				</button>
				{isExpanded ? (
					<FolderOpen className="text-muted-foreground mr-2 h-4 w-4 shrink-0" />
				) : (
					<FolderIcon className="text-muted-foreground mr-2 h-4 w-4 shrink-0" />
				)}
				<span className="flex-1 truncate text-sm font-medium">{folder.name}</span>
				<span className="text-muted-foreground mr-1 shrink-0 text-xs">{prompts.length}</span>
				<DropdownMenu>
					<DropdownMenuTrigger asChild onClick={(e) => e.stopPropagation()} className="bg-card absolute top-1/2 right-2 -translate-y-1/2">
						<Button variant="ghost" size="icon" className="h-6 w-6 shrink-0 opacity-0 group-hover:opacity-100 focus-visible:opacity-100 group-focus-within:opacity-100" aria-label="Folder actions">
							<MoreHorizontal className="h-4 w-4" />
						</Button>
					</DropdownMenuTrigger>
					<DropdownMenuContent align="end">
						<DropdownMenuItem
							onClick={(e) => {
								e.stopPropagation();
								onCreatePrompt();
							}}
						>
							<Plus className="mr-2 h-4 w-4" />
							New Prompt
						</DropdownMenuItem>
						<DropdownMenuSeparator />
						<DropdownMenuItem
							onClick={(e) => {
								e.stopPropagation();
								onEdit();
							}}
						>
							<Pencil className="mr-2 h-4 w-4" />
							Edit Folder
						</DropdownMenuItem>
						<DropdownMenuItem
							className="text-destructive"
							onClick={(e) => {
								e.stopPropagation();
								onDelete();
							}}
						>
							<Trash2 className="mr-2 h-4 w-4" />
							Delete Folder
						</DropdownMenuItem>
					</DropdownMenuContent>
				</DropdownMenu>
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
								href={getPromptHref(prompt.id)}
								onSelect={() => onSelectPrompt(prompt.id)}
								onEdit={() => onEditPrompt(prompt)}
								onDelete={() => onDeletePrompt(prompt)}
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
	href: string;
	onSelect: () => void;
	onEdit: () => void;
	onDelete: () => void;
}

function DraggablePromptItem({ prompt, isSelected, href, onSelect, onEdit, onDelete }: DraggablePromptItemProps) {
	const { ref, isDragging } = useDraggable({ id: `prompt-${prompt.id}` });

	return (
		<div
			ref={ref}
			className={cn(
				"group flex cursor-pointer items-center gap-2 rounded-sm px-2 h-[30px] mb-1 last:mb-0",
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
			<DropdownMenu>
				<DropdownMenuTrigger asChild onClick={(e) => e.stopPropagation()}>
					<Button variant="ghost" size="icon" className="h-6 w-6 opacity-0 group-hover:opacity-100 focus-visible:opacity-100 group-focus-within:opacity-100" aria-label="Prompt actions">
						<MoreHorizontal className="h-4 w-4" />
					</Button>
				</DropdownMenuTrigger>
				<DropdownMenuContent align="end">
					<DropdownMenuItem
						className="cursor-pointer"
						onClick={(e) => {
							e.stopPropagation();
							onEdit();
						}}
					>
						<Pencil className="h-4 w-4" />
						Rename
					</DropdownMenuItem>
					<DropdownMenuItem
						className="text-destructive hover:text-destructive cursor-pointer"
						onClick={(e) => {
							e.stopPropagation();
							onDelete();
						}}
					>
						<Trash2 className="text-destructive h-4 w-4" />
						Delete
					</DropdownMenuItem>
				</DropdownMenuContent>
			</DropdownMenu>
		</div>
	);
}
