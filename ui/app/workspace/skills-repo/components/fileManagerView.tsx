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
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuLabel,
	DropdownMenuSeparator,
	DropdownMenuSub,
	DropdownMenuSubContent,
	DropdownMenuSubTrigger,
	DropdownMenuTrigger,
} from "@/components/ui/dropdownMenu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ScrollBar } from "@/components/ui/scrollArea";
import { Textarea } from "@/components/ui/textarea";
import { Tree, type BaseNodeData, type TreeNode } from "@/components/ui/treeView";
import { getErrorMessage } from "@/lib/store/apis/baseApi";
import { useUploadSkillFileMutation } from "@/lib/store/apis/skillsApi";
import { SkillFileEntry } from "@/lib/types/skills";
import { validateFilePath, validateFilename, validateSkillFileSize, validateSourceType } from "@/lib/validators/skills";
import {
	AlertCircle,
	BookOpen,
	Check,
	ChevronDown,
	ChevronRight,
	FileText,
	Folder,
	Info,
	Loader2,
	MoreHorizontal,
	Plus,
	X,
} from "lucide-react";
import { useEffect, useMemo, useRef, useState, type ChangeEvent } from "react";
import { formatFileSize } from "./helpers";
import { cn } from "@/lib/utils";
import { toast } from "sonner";

const FILE_SOURCE_OPTIONS = [
	{ value: "text", label: "Via text", shortLabel: "Text" },
	{ value: "url", label: "Via URL", shortLabel: "URL" },
	{ value: "dataurl", label: "Via data URL", shortLabel: "Data URL" },
	{ value: "upload", label: "Via upload", shortLabel: "Upload" },
];

function getSourceOption(sourceType: string) {
	return FILE_SOURCE_OPTIONS.find((option) => option.value === sourceType) ?? FILE_SOURCE_OPTIONS[0];
}

function joinPath(folder: string, filename: string) {
	return [folder, filename.trim()].filter(Boolean).join("/");
}

function basename(path: string) {
	return path.split("/").filter(Boolean).pop() ?? path;
}

function dirname(path: string) {
	const parts = path.split("/").filter(Boolean);
	parts.pop();
	return parts.join("/");
}

function normalizeFolderPath(path: string) {
	return path.trim().replace(/^\/+|\/+$/g, "");
}

function getFolderAncestors(path: string) {
	const parts = path.split("/").filter(Boolean);
	parts.pop();
	const folders: string[] = [];
	let currentPath = "";
	for (const part of parts) {
		currentPath = joinPath(currentPath, part);
		folders.push(currentPath);
	}
	return folders;
}

function getRelativeUploadPath(file: File) {
	return (file as File & { webkitRelativePath?: string }).webkitRelativePath || file.name;
}

interface FileAddFormProps {
	folderPath: string;
	initialSourceType: string;
	initialEntry?: SkillFileEntry;
	submitLabel?: string;
	className?: string;
	onAdd: (entry: SkillFileEntry) => void;
	onCancel: () => void;
}

function FileAddForm({ folderPath, initialSourceType, initialEntry, submitLabel, onAdd, onCancel, className }: FileAddFormProps) {
	const [uploadSkillFile, { isLoading: isUploading }] = useUploadSkillFileMutation();
	const sourceType = initialSourceType;
	const sourceOption = getSourceOption(sourceType);
	const initialPath = initialEntry?.path ?? "";
	const [filename, setFilename] = useState(initialEntry ? basename(initialPath) : "");
	const [url, setUrl] = useState(initialEntry?.source_url ?? "");
	const [content, setContent] = useState(initialEntry?.content ?? "");
	const [dataurl, setDataurl] = useState(initialEntry?.dataurl ?? "");
	const [selectedFile, setSelectedFile] = useState<File | null>(null);
	const [mimeType, setMimeType] = useState(initialEntry?.mime_type ?? "text/plain");
	const [error, setError] = useState<string | null>(null);

	const locationLabel = folderPath ? `${folderPath}/` : "root";
	// Adding a new file asks for its name in-tree first; source-specific fields
	// are edited in the right-hand pane after the file is inserted.
	const isNewFileNameOnly = !initialEntry;
	const nameInputRef = useRef<HTMLInputElement | null>(null);

	// Grab focus after the menu that opened this draft has returned focus to its trigger.
	useEffect(() => {
		if (!isNewFileNameOnly) return;
		const id = window.setTimeout(() => nameInputRef.current?.focus(), 0);
		return () => window.clearTimeout(id);
	}, [isNewFileNameOnly]);

	const handleUploadFileChange = (file: File | null) => {
		setSelectedFile(file);
		if (file) {
			const sizeErr = validateSkillFileSize(file.size);
			if (sizeErr) {
				setError(sizeErr);
				setSelectedFile(null);
				return;
			}
			setFilename((current) => current || file.name);
			setMimeType(file.type || "application/octet-stream");
		}
	};

	const handleSubmit = async () => {
		setError(null);
		const filenameErr = validateFilename(filename);
		if (filenameErr) return setError(filenameErr);

		const fullPath = joinPath(folderPath, filename);
		const pathErr = validateFilePath(fullPath);
		if (pathErr) return setError(pathErr);

		if (!isNewFileNameOnly) {
			const srcErr = validateSourceType(sourceType, {
				url,
				dataurl,
				content,
				upload_id: selectedFile?.name,
			});
			if (srcErr) return setError(srcErr);
		}

		const entry: SkillFileEntry = {
			path: fullPath,
			source_type: sourceType as SkillFileEntry["source_type"],
			mime_type: mimeType,
			file_size_bytes: selectedFile?.size ?? initialEntry?.file_size_bytes,
			__local: initialEntry?.__local ?? sourceType !== "upload",
		};

		if (sourceType === "upload") {
			if (!selectedFile) return setError("Select a file to upload");
			const sizeErr = validateSkillFileSize(selectedFile.size);
			if (sizeErr) return setError(sizeErr);
			try {
				const upload = await uploadSkillFile({ file: selectedFile }).unwrap();
				entry.upload_id = upload.upload_id;
				entry.storage_key = upload.storage_key;
				entry.blob_id = upload.blob_id;
				entry.mime_type = upload.mime_type || mimeType;
				entry.file_size_bytes = upload.file_size_bytes;
			} catch (err: unknown) {
				setError(getErrorMessage(err));
				return;
			}
		}

		if (sourceType === "url") entry.source_url = url;
		if (sourceType === "text") entry.content = content;
		if (sourceType === "dataurl") entry.dataurl = dataurl;
		onAdd(entry);
	};

	// Name-only add: input with check/cross like folder add.
	if (isNewFileNameOnly) {
		return (
			<div className={cn("flex flex-col gap-1.5 py-1", className)}>
				<div className="flex items-center gap-2">
					<Input
						ref={nameInputRef}
						autoFocus
						data-testid="skill-file-filename-input"
						value={filename}
						onChange={(e) => {
							setFilename(e.target.value);
							setError(null);
						}}
						onKeyDown={(e) => {
							if (e.key === "Enter") {
								e.preventDefault();
								handleSubmit();
							} else if (e.key === "Escape") {
								e.preventDefault();
								onCancel();
							}
						}}
						placeholder="filename.ext"
						className="h-7 max-w-xs font-mono text-xs"
						aria-label="Filename"
					/>
					<Button variant="ghost" size="sm" className="h-7 w-7 p-0" data-testid="skill-file-confirm-btn" onClick={handleSubmit}>
						<Check className="h-3 w-3" />
					</Button>
					<Button variant="ghost" size="sm" className="h-7 w-7 p-0" data-testid="skill-file-cancel-btn" onClick={onCancel}>
						<X className="h-3 w-3" />
					</Button>
				</div>
				{error && (
					<div className="text-destructive flex items-center gap-2 text-xs" role="alert">
						<AlertCircle className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
						{error}
					</div>
				)}
			</div>
		);
	}

	return (
		<div className={cn("w-full rounded-sm border border-dashed p-2 flex flex-col gap-3", className)}>
			{!isNewFileNameOnly && (
				<div className="flex items-center gap-3">
					<span className="border-border/60 text-muted-foreground inline-flex h-5 shrink-0 items-center rounded-full border bg-transparent px-2 text-xs leading-none font-medium">
						{sourceOption.label}
					</span>
					<span className="text-muted-foreground font-mono text-xs">Location: {locationLabel}</span>
				</div>
			)}

			<div>
				<Label className="text-muted-foreground text-xs">Filename</Label>
				<Input
					data-testid="skill-file-filename-input"
					value={filename}
					onChange={(e) => setFilename(e.target.value)}
					placeholder="review.py"
					className="mt-1 h-8 font-mono text-xs"
				/>
			</div>

			{sourceType === "url" && (
				<Input
					data-testid="skill-file-url-input"
					value={url}
					onChange={(e) => setUrl(e.target.value)}
					placeholder="https://example.com/file.py"
					className="h-8 font-mono text-xs"
					aria-label="Source URL"
				/>
			)}
			{sourceType === "text" && !isNewFileNameOnly && (
				<Textarea
					data-testid="skill-file-content-textarea"
					value={content}
					onChange={(e) => setContent(e.target.value)}
					placeholder="File content..."
					className="min-h-20 font-mono text-xs"
					rows={4}
					aria-label="File content"
				/>
			)}
			{sourceType === "dataurl" && (
				<Textarea
					data-testid="skill-file-dataurl-textarea"
					value={dataurl}
					onChange={(e) => setDataurl(e.target.value)}
					placeholder="data:text/plain;base64,..."
					className="font-mono text-xs"
					rows={2}
					aria-label="Data URL"
				/>
			)}
			{sourceType === "upload" && (
				<div className="flex flex-col gap-1.5">
					<Input
						data-testid="skill-file-upload-input"
						type="file"
						onChange={(e) => handleUploadFileChange(e.target.files?.[0] ?? null)}
						className="h-8 text-xs"
						aria-label="Choose file to upload"
					/>
					{selectedFile && (
						<div className="text-muted-foreground flex items-center gap-2 text-xs">
							<span className="truncate font-mono">{selectedFile.name}</span>
							<span>{formatFileSize(selectedFile.size)}</span>
							{selectedFile.type && <span>{selectedFile.type}</span>}
						</div>
					)}
				</div>
			)}

			{sourceType === "url" && (
				<div className="flex items-start gap-2 rounded-sm border border-blue-200 bg-blue-50 px-3 py-2 text-xs text-blue-900 dark:border-blue-900/50 dark:bg-blue-950/30 dark:text-blue-200">
					<Info className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
					<span>This source is saved as a live reference. Bifrost will read from this URL when the skill file is retrieved.</span>
				</div>
			)}

			{error && (
				<div className="text-destructive flex items-center gap-2 text-xs" role="alert">
					<AlertCircle className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
					{error}
				</div>
			)}

			<div className="flex justify-end gap-2 pt-1">
				<Button variant="ghost" size="sm" className="h-7 text-xs" data-testid="skill-file-cancel-btn" onClick={onCancel}>
					Cancel
				</Button>
				<Button size="sm" className="h-7 text-xs" data-testid="skill-file-save-btn" onClick={handleSubmit} disabled={isUploading}>
					{isUploading ? (
						<>
							<Loader2 className="h-3 w-3 animate-spin" />
							Uploading...
						</>
					) : (
						<>
							<Plus className="h-3 w-3" />
							{submitLabel ?? (sourceType === "upload" ? "Upload & add" : "Add")}
						</>
					)}
				</Button>
			</div>
		</div>
	);
}

function getMovedPath(path: string, fromFolder: string, toFolder: string) {
	const relativePath = path === fromFolder ? basename(path) : path.slice(fromFolder.length + 1);
	return joinPath(toFolder, relativePath);
}

interface TreeFolder {
	name: string;
	path: string;
	folders: Map<string, TreeFolder>;
	files: { entry: SkillFileEntry; index: number }[];
}

function makeFolder(name: string, path: string): TreeFolder {
	return { name, path, folders: new Map(), files: [] };
}

function buildTree(files: SkillFileEntry[]) {
	const root = makeFolder("", "");
	files.forEach((entry, index) => {
		const segments = entry.path.split("/").filter(Boolean);
		const fileName = segments.pop();
		if (!fileName) return;
		let folder = root;
		let currentPath = "";
		for (const segment of segments) {
			currentPath = joinPath(currentPath, segment);
			if (!folder.folders.has(segment)) folder.folders.set(segment, makeFolder(segment, currentPath));
			folder = folder.folders.get(segment)!;
		}
		folder.files.push({ entry, index });
	});
	return root;
}

interface FileTreeNodeData extends BaseNodeData {
	kind: "root" | "folder" | "file" | "skillmd" | "add-file" | "add-folder" | "empty-folder";
	path: string;
	folder?: TreeFolder;
	file?: SkillFileEntry;
	fileIndex?: number;
}

function folderToTreeNode(folder: TreeFolder): TreeNode<FileTreeNodeData> {
	const childFolders = [...folder.folders.values()].sort((a, b) => a.name.localeCompare(b.name));
	const childFiles = [...folder.files].sort((a, b) => basename(a.entry.path).localeCompare(basename(b.entry.path)));

	return {
		data: {
			id: folder.path || "root",
			name: folder.path ? folder.name : "root",
			kind: folder.path ? "folder" : "root",
			path: folder.path,
			folder,
		},
		children: [
			...childFolders.map((childFolder) => folderToTreeNode(childFolder)),
			...childFiles.map(({ entry, index }) => ({
				data: {
					id: `file:${index}:${entry.path}`,
					name: basename(entry.path),
					kind: "file" as const,
					path: entry.path,
					file: entry,
					fileIndex: index,
				},
			})),
		],
	};
}

function addChildNode(
	node: TreeNode<FileTreeNodeData>,
	parentPath: string,
	child: TreeNode<FileTreeNodeData>,
	placement: "start" | "end" = "end",
): boolean {
	if (node.data.path === parentPath) {
		node.children = placement === "start" ? [child, ...(node.children ?? [])] : [...(node.children ?? []), child];
		return true;
	}
	for (const nested of node.children ?? []) {
		if (addChildNode(nested, parentPath, child, placement)) return true;
	}
	return false;
}

export function FileManagerSection({
	files,
	onAddFile,
	onRemoveFile,
	onUpdateFile,
	readOnly,
	selectedIndex,
	onSelectFile,
	bodySelected,
	onSelectBody,
	hasBodyError,
}: {
	files: SkillFileEntry[];
	onAddFile: (entry: SkillFileEntry) => void;
	onRemoveFile: (index: number) => void;
	onUpdateFile: (index: number, updates: Partial<SkillFileEntry>) => void;
	readOnly: boolean;
	// When provided, clicking a file row selects it (for an external editor/preview pane).
	selectedIndex?: number | null;
	onSelectFile?: (index: number) => void;
	// When provided, a SKILL.md node is shown at the top of the tree and selecting it
	// calls onSelectBody (the body is edited in the external pane).
	bodySelected?: boolean;
	onSelectBody?: () => void;
	// When true, the SKILL.md label renders red to indicate missing body.
	hasBodyError?: boolean;
}) {
	const [uploadSkillFile] = useUploadSkillFileMutation();
	const folderUploadInputRef = useRef<HTMLInputElement | null>(null);
	const folderUploadTargetRef = useRef("");
	const fileUploadInputRef = useRef<HTMLInputElement | null>(null);
	const fileUploadTargetRef = useRef("");
	const [folderUploadState, setFolderUploadState] = useState<{
		folderPath: string;
		total: number;
		completed: number;
	} | null>(null);
	const [folderUploadError, setFolderUploadError] = useState<string | null>(null);
	const [editingFileIndex, setEditingFileIndex] = useState<number | null>(null);
	const [editingFileOriginal, setEditingFileOriginal] = useState<{
		path: string;
	} | null>(null);
	const [searchQuery, setSearchQuery] = useState("");
	const [addingFile, setAddingFile] = useState<{
		folderPath: string;
		sourceType: string;
	} | null>(null);
	const [newFolderParent, setNewFolderParent] = useState<string | null>(null);
	const [newFolderName, setNewFolderName] = useState("");
	const [newFolderError, setNewFolderError] = useState<string | null>(null);
	const [folders, setFolders] = useState<string[]>([]);
	const [folderToDelete, setFolderToDelete] = useState<string | null>(null);
	const [fileToRemove, setFileToRemove] = useState<{
		index: number;
		path: string;
		isLocal: boolean;
	} | null>(null);
	// External expansion state so we can programmatically expand folders
	const [expandedNodes, setExpandedNodes] = useState<Record<string, boolean>>({});
	const [activeDropdownNodeId, setActiveDropdownNodeId] = useState<string | null>(null);

	useEffect(() => {
		const foldersFromFiles = files.flatMap((file) => getFolderAncestors(file.path));
		if (foldersFromFiles.length === 0) return;
		setFolders((prev) => {
			const next = new Set(prev);
			foldersFromFiles.forEach((folder) => next.add(folder));
			return next.size === prev.length ? prev : [...next];
		});
	}, [files]);

	const expandFolder = (folderPath: string) => {
		const normalizedFolderPath = folderPath === "root" ? "" : folderPath;
		const ancestors = normalizedFolderPath.split("/").filter(Boolean);
		setExpandedNodes((prev) => {
			const next: Record<string, boolean> = { ...prev, root: true };
			let path = "";
			for (const part of ancestors) {
				path = path ? `${path}/${part}` : part;
				next[path] = true;
			}
			return next;
		});
	};

	const isFolderUploading = folderUploadState !== null;

	const filteredFiles = searchQuery.trim() ? files.filter((file) => file.path.toLowerCase().includes(searchQuery.toLowerCase())) : files;

	const treeData = useMemo(() => {
		const rootFolder = buildTree(filteredFiles);
		for (const folderPath of folders) {
			if (searchQuery.trim()) {
				const hasMatchingFile = filteredFiles.some((f) => f.path.startsWith(`${folderPath}/`));
				if (!hasMatchingFile) continue;
			}
			const segments = folderPath.split("/").filter(Boolean);
			let folder = rootFolder;
			let currentPath = "";
			for (const segment of segments) {
				currentPath = joinPath(currentPath, segment);
				if (!folder.folders.has(segment)) folder.folders.set(segment, makeFolder(segment, currentPath));
				folder = folder.folders.get(segment)!;
			}
		}

		const rootNode = folderToTreeNode(rootFolder);

		// SKILL.md sits at the top of the tree (under root), selecting the body pane.
		if (onSelectBody) {
			rootNode.children = [
				{
					data: { id: "skillmd", name: "SKILL.md", kind: "skillmd", path: "" },
				},
				...(rootNode.children ?? []),
			];
		}

		if (newFolderParent !== null) {
			addChildNode(
				rootNode,
				newFolderParent,
				{
					data: {
						id: `draft-folder:${newFolderParent}`,
						name: "New folder",
						kind: "add-folder",
						path: newFolderParent,
					},
				},
				"start",
			);
		}

		if (addingFile) {
			addChildNode(
				rootNode,
				addingFile.folderPath,
				{
					data: {
						id: `draft-file:${addingFile.folderPath}:${addingFile.sourceType}`,
						name: "New file",
						kind: "add-file",
						path: addingFile.folderPath,
					},
				},
				"start",
			);
		}

		const addEmptyFolderDrafts = (node: TreeNode<FileTreeNodeData>) => {
			for (const child of node.children ?? []) addEmptyFolderDrafts(child);
			const isEmptyDraftFolder = node.data.kind === "folder" && folders.includes(node.data.path) && (node.children?.length ?? 0) === 0;
			if (isEmptyDraftFolder) {
				node.children = [
					{
						data: {
							id: `empty:${node.data.path}`,
							name: "Empty folder",
							kind: "empty-folder",
							path: node.data.path,
						},
					},
				];
			}
		};
		addEmptyFolderDrafts(rootNode);

		return [rootNode];
	}, [addingFile, filteredFiles, files, folders, newFolderParent, onSelectBody, searchQuery]);

	const availableFolderPaths = (() => {
		const folderSet = new Set<string>([""]);
		folders.forEach((folder) => folderSet.add(folder));
		files.forEach((file) => {
			getFolderAncestors(file.path).forEach((folder) => folderSet.add(folder));
		});
		return [...folderSet].sort((a, b) => {
			if (a === "") return -1;
			if (b === "") return 1;
			return a.localeCompare(b);
		});
	})();

	const handleFolderUploadClick = (folderPath: string) => {
		if (isFolderUploading) return;
		folderUploadTargetRef.current = folderPath;
		setFolderUploadError(null);
		folderUploadInputRef.current?.click();
	};

	// "Add file" opens the OS picker directly and uploads the chosen file.
	const handleAddFileUploadClick = (folderPath: string) => {
		if (isFolderUploading) return;
		fileUploadTargetRef.current = folderPath;
		setAddingFile(null);
		setEditingFileIndex(null);
		setEditingFileOriginal(null);
		fileUploadInputRef.current?.click();
	};

	const handleSingleFileUploadChange = async (event: ChangeEvent<HTMLInputElement>) => {
		const file = event.target.files?.[0];
		event.target.value = "";
		if (!file) return;

		const targetFolderPath = fileUploadTargetRef.current;
		const fullPath = joinPath(targetFolderPath, file.name);
		if (files.some((f) => f.path === fullPath)) {
			toast.error("A file already exists at that path");
			return;
		}
		const pathErr = validateFilePath(fullPath);
		if (pathErr) {
			toast.error(pathErr);
			return;
		}
		const sizeErr = validateSkillFileSize(file.size);
		if (sizeErr) {
			toast.error(sizeErr);
			return;
		}

		setFolderUploadState({
			folderPath: targetFolderPath,
			total: 1,
			completed: 0,
		});
		try {
			const upload = await uploadSkillFile({ file }).unwrap();
			onAddFile({
				path: fullPath,
				source_type: "upload",
				upload_id: upload.upload_id,
				storage_key: upload.storage_key,
				blob_id: upload.blob_id,
				mime_type: upload.mime_type || file.type || "application/octet-stream",
				file_size_bytes: upload.file_size_bytes,
				__local: false,
			});
			getFolderAncestors(fullPath).forEach((folder) => expandFolder(folder));
			setFolders((prev) => {
				const next = new Set(prev);
				getFolderAncestors(fullPath).forEach((folder) => next.add(folder));
				return next.size === prev.length ? prev : [...next];
			});
			onSelectFile?.(files.length);
		} catch (err: unknown) {
			toast.error(getErrorMessage(err));
		} finally {
			setFolderUploadState(null);
		}
	};

	const handleFolderUploadChange = async (event: ChangeEvent<HTMLInputElement>) => {
		const selectedFiles = Array.from(event.target.files ?? []);
		event.target.value = "";
		if (selectedFiles.length === 0) return;

		const targetFolderPath = folderUploadTargetRef.current;
		setFolderUploadError(null);
		setAddingFile(null);
		setEditingFileIndex(null);
		setEditingFileOriginal(null);

		setFolderUploadState({
			folderPath: targetFolderPath,
			total: selectedFiles.length,
			completed: 0,
		});

		try {
			const importedFolders = new Set<string>();
			const entries: SkillFileEntry[] = [];

			for (const file of selectedFiles) {
				const relativePath = normalizeFolderPath(getRelativeUploadPath(file));
				if (!relativePath) continue;

				const fullPath = joinPath(targetFolderPath, relativePath);
				if (files.some((f) => f.path === fullPath) || entries.some((e) => e.path === fullPath))
					throw new Error(`${fullPath}: a file already exists at that path`);
				const pathErr = validateFilePath(fullPath);
				if (pathErr) throw new Error(`${fullPath}: ${pathErr}`);
				const sizeErr = validateSkillFileSize(file.size);
				if (sizeErr) throw new Error(`${fullPath}: ${sizeErr}`);

				const upload = await uploadSkillFile({ file }).unwrap();
				entries.push({
					path: fullPath,
					source_type: "upload",
					upload_id: upload.upload_id,
					storage_key: upload.storage_key,
					blob_id: upload.blob_id,
					mime_type: upload.mime_type || file.type || "application/octet-stream",
					file_size_bytes: upload.file_size_bytes,
					__local: false,
				});
				getFolderAncestors(fullPath).forEach((folder) => importedFolders.add(folder));
				setFolderUploadState((current) => (current ? { ...current, completed: current.completed + 1 } : current));
			}

			entries.forEach(onAddFile);
			setFolders((prev) => {
				const next = new Set(prev);
				importedFolders.forEach((folder) => next.add(folder));
				return next.size === prev.length ? prev : [...next];
			});
		} catch (err: unknown) {
			setFolderUploadError(getErrorMessage(err));
		} finally {
			setFolderUploadState(null);
		}
	};

	const moveFileToFolder = (index: number, folderPath: string) => {
		const file = files[index];
		if (!file) return;
		const nextPath = joinPath(folderPath, basename(file.path));
		if (nextPath === file.path) return;
		if (files.some((f, i) => i !== index && f.path === nextPath)) {
			toast.error("A file already exists at that path");
			return;
		}
		onUpdateFile(index, { path: nextPath });
	};

	const moveFolderToFolder = (folderPath: string, targetFolderPath: string) => {
		const nextFolderPath = joinPath(targetFolderPath, basename(folderPath));
		if (
			nextFolderPath === folderPath ||
			targetFolderPath === dirname(folderPath) ||
			targetFolderPath === folderPath ||
			targetFolderPath.startsWith(`${folderPath}/`)
		) {
			return;
		}

		const hasCollision = files.some((file) => {
			if (!file.path.startsWith(`${folderPath}/`)) return false;
			const movedPath = getMovedPath(file.path, folderPath, nextFolderPath);
			return files.some((f) => !f.path.startsWith(`${folderPath}/`) && f.path === movedPath);
		});
		if (hasCollision) {
			toast.error("A file already exists at that path");
			return;
		}

		files.forEach((file, index) => {
			if (file.path.startsWith(`${folderPath}/`)) {
				onUpdateFile(index, {
					path: getMovedPath(file.path, folderPath, nextFolderPath),
				});
			}
		});

		setFolders((prev) => {
			const next = new Set<string>();
			prev.forEach((folder) => {
				if (folder === folderPath || folder.startsWith(`${folderPath}/`)) {
					next.add(getMovedPath(folder, folderPath, nextFolderPath));
				} else {
					next.add(folder);
				}
			});
			next.add(nextFolderPath);
			getFolderAncestors(`${nextFolderPath}/placeholder.txt`).forEach((folder) => next.add(folder));
			return [...next];
		});

		if (addingFile?.folderPath.startsWith(`${folderPath}/`) || addingFile?.folderPath === folderPath) {
			setAddingFile({
				...addingFile,
				folderPath: getMovedPath(addingFile.folderPath, folderPath, nextFolderPath),
			});
		}
		if (newFolderParent?.startsWith(`${folderPath}/`) || newFolderParent === folderPath) {
			setNewFolderParent(getMovedPath(newFolderParent, folderPath, nextFolderPath));
		}
	};

	const removeFile = (index: number) => {
		onRemoveFile(index);
		if (editingFileIndex !== null) {
			if (editingFileIndex === index) {
				setEditingFileIndex(null);
				setEditingFileOriginal(null);
			} else if (index < editingFileIndex) {
				setEditingFileIndex(editingFileIndex - 1);
			}
		}
	};

	const folderDeleteImpact = (() => {
		if (!folderToDelete) return null;
		const nestedFiles = files
			.filter((file) => file.path.startsWith(`${folderToDelete}/`))
			.map((file) => file.path)
			.sort((a, b) => a.localeCompare(b));
		const nestedFolders = folders
			.filter((folder) => folder !== folderToDelete && folder.startsWith(`${folderToDelete}/`))
			.sort((a, b) => a.localeCompare(b));

		return { nestedFiles, nestedFolders };
	})();

	const removeFolder = (folderPath: string) => {
		setFolders((prev) => prev.filter((folder) => folder !== folderPath && !folder.startsWith(`${folderPath}/`)));
		const removedIndices = files
			.map((file, index) => ({ file, index }))
			.filter(({ file }) => file.path === folderPath || file.path.startsWith(`${folderPath}/`))
			.map(({ index }) => index)
			.sort((a, b) => b - a);

		removedIndices.forEach((index) => onRemoveFile(index));

		const adjustIndexAfterBatchRemove = (currentIndex: number | null) => {
			if (currentIndex === null) return currentIndex;
			if (removedIndices.includes(currentIndex)) return null;
			const removedBeforeCurrent = removedIndices.filter((index) => index < currentIndex).length;
			return currentIndex - removedBeforeCurrent;
		};

		setEditingFileIndex((currentIndex) => {
			const nextIndex = adjustIndexAfterBatchRemove(currentIndex);
			if (nextIndex === null && currentIndex !== null) {
				setEditingFileOriginal(null);
			}
			return nextIndex;
		});
		if (addingFile?.folderPath === folderPath || addingFile?.folderPath.startsWith(`${folderPath}/`)) setAddingFile(null);
		if (newFolderParent === folderPath || newFolderParent?.startsWith(`${folderPath}/`)) {
			setNewFolderParent(null);
			setNewFolderName("");
			setNewFolderError(null);
		}
	};

	const requestRemoveFolder = (folderPath: string) => {
		const hasNestedFiles = files.some((file) => file.path.startsWith(`${folderPath}/`));
		const hasNestedFolders = folders.some((folder) => folder !== folderPath && folder.startsWith(`${folderPath}/`));

		if (hasNestedFiles || hasNestedFolders) {
			setFolderToDelete(folderPath);
			return;
		}

		removeFolder(folderPath);
	};

	const handleAdd = (entry: SkillFileEntry) => {
		if (files.some((f) => f.path === entry.path)) {
			toast.error("A file already exists at that path");
			return;
		}
		onAddFile(entry);
		// Expand the folder containing the new file so the user can see it
		const folder = dirname(entry.path);
		expandFolder(folder || "root");
		setAddingFile(null);
		// Select the new file (appended at the end) so its editor opens immediately.
		onSelectFile?.(files.length);
	};

	const addFolder = (parentPath: string) => {
		setNewFolderError(null);
		const nameErr = validateFilename(newFolderName);
		if (nameErr) {
			setNewFolderError(nameErr);
			return;
		}
		const nextPath = joinPath(parentPath, newFolderName);
		if (folders.includes(nextPath) || files.some((file) => file.path === nextPath || file.path.startsWith(`${nextPath}/`))) {
			setNewFolderError("A folder with this name already exists here");
			return;
		}
		const pathErr = validateFilePath(`${nextPath}/placeholder.txt`);
		if (pathErr) {
			setNewFolderError(pathErr);
			return;
		}
		setFolders((prev) => (prev.includes(nextPath) ? prev : [...prev, nextPath]));
		setNewFolderName("");
		setNewFolderError(null);
		setNewFolderParent(null);
	};

	const renderItem = ({
		item,
		isExpanded,
		hasChildren,
		onToggle,
		onExpandAll,
		onCollapseAll,
		isAllExpanded,
		isAllCollapsed,
	}: {
		item: FileTreeNodeData;
		level: number;
		isExpanded: boolean;
		hasChildren: boolean;
		onToggle: () => void;
		onExpandAll: () => void;
		onCollapseAll: () => void;
		isAllExpanded: boolean;
		isAllCollapsed: boolean;
	}) => {
		if (item.kind === "skillmd") {
			const selected = !!bodySelected;
			const showError = !selected && !!hasBodyError;
			return (
				<div
					data-selected={selected || undefined}
					role="button"
					tabIndex={0}
					onClick={onSelectBody}
					onKeyDown={(e) => {
						if (e.key === "Enter" || e.key === " ") {
							e.preventDefault();
							onSelectBody?.();
						}
					}}
					className={cn(
						"flex h-7 min-w-0 cursor-pointer items-center gap-2 rounded-sm px-1.5 text-sm transition-colors hover:bg-muted/50",
						selected && "bg-primary/10 text-primary hover:bg-primary/10",
					)}
				>
					<BookOpen
						className={cn("h-3.5 w-3.5 shrink-0", selected ? "text-primary" : showError ? "text-destructive" : "text-muted-foreground")}
					/>
					<span className={cn("min-w-0 flex-1 truncate font-mono text-xs", showError && "text-destructive")} title="SKILL.md">
						SKILL.md
					</span>
				</div>
			);
		}

		if (item.kind === "add-folder") {
			return (
				<div className="ml-1 flex flex-col gap-1.5 py-1">
					<div className="flex items-center gap-2">
						<Input
							data-testid="skill-file-folder-name-input"
							value={newFolderName}
							onChange={(e) => {
								setNewFolderName(normalizeFolderPath(e.target.value));
								setNewFolderError(null);
							}}
							placeholder="folder-name"
							className="h-7 max-w-xs font-mono text-xs"
						/>
						<Button
							variant="ghost"
							size="sm"
							className="h-7 w-7 p-0"
							data-testid="skill-file-folder-confirm-btn"
							onClick={() => addFolder(item.path)}
						>
							<Check className="h-3 w-3" />
						</Button>
						<Button
							variant="ghost"
							size="sm"
							className="h-7 w-7 p-0"
							data-testid="skill-file-folder-cancel-btn"
							onClick={() => {
								setNewFolderParent(null);
								setNewFolderName("");
								setNewFolderError(null);
							}}
						>
							<X className="h-3 w-3" />
						</Button>
					</div>
					{newFolderError && (
						<div className="text-destructive flex items-center gap-2 text-xs" role="alert">
							<AlertCircle className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
							{newFolderError}
						</div>
					)}
				</div>
			);
		}

		if (item.kind === "add-file" && addingFile) {
			return (
				<FileAddForm
					className="ml-1"
					folderPath={addingFile.folderPath}
					initialSourceType={addingFile.sourceType}
					onAdd={handleAdd}
					onCancel={() => setAddingFile(null)}
				/>
			);
		}

		if (item.kind === "empty-folder") {
			return (
				<div className="text-muted-foreground ml-1 flex items-center gap-2 py-1 text-xs">
					<span>Empty folder</span>
					<span className="text-muted-foreground/60 text-xs">Not saved until it contains a file.</span>
				</div>
			);
		}

		if (item.kind === "file" && item.file && item.fileIndex != null) {
			const file = item.file;
			const index = item.fileIndex;
			const fileMoveTargets = availableFolderPaths.filter((folderPath) => folderPath !== dirname(file.path));
			const fileMeta = [file.source_type, file.mime_type, file.file_size_bytes ? formatFileSize(file.file_size_bytes) : null]
				.filter(Boolean)
				.join(" \u00b7 ");

			const fileDropdownActive = activeDropdownNodeId === item.id;
			const isSelected = onSelectFile != null && selectedIndex === index;
			const isRenaming = editingFileIndex === index;

			return (
				<div
					data-selected={isSelected || undefined}
					className={cn(
						"group flex min-w-0 items-center gap-2 rounded-sm px-1.5 py-1 text-sm transition-colors hover:bg-muted/50",
						fileDropdownActive && "bg-muted/50",
						isSelected && "bg-primary/10 hover:bg-primary/10",
					)}
				>
					<div
						className={cn("flex min-w-0 flex-1 items-center gap-2", onSelectFile && !isRenaming && "cursor-pointer")}
						onClick={!isRenaming && onSelectFile ? () => onSelectFile(index) : undefined}
						onKeyDown={
							!isRenaming && onSelectFile
								? (e) => {
										if (e.key === "Enter" || e.key === " ") {
											e.preventDefault();
											onSelectFile(index);
										}
									}
								: undefined
						}
						role={!isRenaming && onSelectFile ? "button" : undefined}
						tabIndex={!isRenaming && onSelectFile ? 0 : undefined}
					>
						<FileText className={cn("h-3.5 w-3.5 shrink-0", isSelected ? "text-primary" : "text-muted-foreground")} />
						{isRenaming ? (
							<Input
								autoFocus
								value={basename(file.path)}
								onChange={(e) => {
									const newPath = joinPath(dirname(file.path), e.target.value);
									if (files.some((f, i) => i !== index && f.path === newPath)) {
										toast.error("A file already exists at that path");
										return;
									}
									onUpdateFile(index, { path: newPath });
								}}
								onKeyDown={(e) => {
									if (e.key === "Enter") {
										e.preventDefault();
										const err = validateFilePath(file.path);
										const filenameErr = validateFilename(basename(file.path));
										if (!err && !filenameErr) {
											setEditingFileIndex(null);
											setEditingFileOriginal(null);
										}
									} else if (e.key === "Escape") {
										e.preventDefault();
										if (editingFileOriginal) onUpdateFile(index, { path: editingFileOriginal.path });
										setEditingFileIndex(null);
										setEditingFileOriginal(null);
									}
								}}
								onBlur={() => {
									const err = validateFilePath(file.path);
									const filenameErr = validateFilename(basename(file.path));
									if (err || filenameErr) {
										if (editingFileOriginal) onUpdateFile(index, { path: editingFileOriginal.path });
									}
									setEditingFileIndex(null);
									setEditingFileOriginal(null);
								}}
								placeholder="filename.ext"
								className="h-7 min-w-0 flex-1 font-mono text-xs"
								aria-label="Rename file"
							/>
						) : (
							<span className="min-w-0 flex-1 truncate font-mono text-xs" title={basename(file.path)}>
								{basename(file.path)}
							</span>
						)}
					</div>
					{!readOnly && (
						<div
							className={cn(
								"sticky right-1 z-10 ml-auto flex shrink-0 items-center justify-end gap-1 rounded-sm bg-muted px-0.5 opacity-100 transition-opacity md:opacity-0 md:group-hover:opacity-100 md:group-focus-within:opacity-100",
								fileDropdownActive && "md:opacity-100",
							)}
						>
							<DropdownMenu onOpenChange={(open) => setActiveDropdownNodeId(open ? item.id : null)}>
								<DropdownMenuTrigger asChild>
									<Button
										variant="ghost"
										size="icon"
										className="text-muted-foreground h-6 w-6"
										data-testid={`skill-file-actions-${basename(file.path)}`}
										aria-label={`Actions for ${file.path}`}
									>
										<MoreHorizontal className="h-3.5 w-3.5" />
									</Button>
								</DropdownMenuTrigger>
								<DropdownMenuContent align="end" className="w-48">
									{fileMeta && (
										<>
											<DropdownMenuLabel className="text-muted-foreground text-xs font-normal break-all">{fileMeta}</DropdownMenuLabel>
											<DropdownMenuSeparator />
										</>
									)}
									<DropdownMenuItem
										className="cursor-pointer"
										onSelect={() => {
											setEditingFileIndex(index);
											setEditingFileOriginal({ path: file.path });
										}}
									>
										Rename
									</DropdownMenuItem>
									{fileMoveTargets.length > 0 && (
										<DropdownMenuSub>
											<DropdownMenuSubTrigger>Move to…</DropdownMenuSubTrigger>
											<DropdownMenuSubContent>
												{fileMoveTargets.map((folderPath) => (
													<DropdownMenuItem
														key={folderPath || "root"}
														className="cursor-pointer"
														onSelect={() => {
															setEditingFileIndex(null);
															setEditingFileOriginal(null);
															moveFileToFolder(index, folderPath);
														}}
													>
														{folderPath || "root"}
													</DropdownMenuItem>
												))}
											</DropdownMenuSubContent>
										</DropdownMenuSub>
									)}
									<DropdownMenuSeparator />
									<DropdownMenuItem
										variant="destructive"
										className="cursor-pointer"
										onSelect={() =>
											setFileToRemove({
												index,
												path: file.path,
												isLocal: !!file.__local,
											})
										}
									>
										Delete
									</DropdownMenuItem>
								</DropdownMenuContent>
							</DropdownMenu>
						</div>
					)}
				</div>
			);
		}

		const isRoot = item.kind === "root";
		const folderDropdownActive = activeDropdownNodeId === item.id;
		const folderMoveTargets = isRoot
			? []
			: availableFolderPaths.filter(
					(folderPath) => folderPath !== item.path && !folderPath.startsWith(`${item.path}/`) && folderPath !== dirname(item.path),
				);

		return (
			<div
				className={cn(
					"group flex min-w-0 items-center gap-2 rounded-sm px-2 py-1.5 text-sm hover:bg-muted/40",
					hasChildren && "cursor-pointer",
					folderDropdownActive && "bg-muted/40",
				)}
				onClick={() => {
					if (hasChildren) onToggle();
				}}
				onKeyDown={(event) => {
					if (hasChildren && (event.key === "Enter" || event.key === " ")) {
						event.preventDefault();
						onToggle();
					}
				}}
				role={hasChildren ? "button" : undefined}
				tabIndex={hasChildren ? 0 : undefined}
				aria-label={hasChildren ? `${isExpanded ? "Collapse" : "Expand"} ${isRoot ? "root" : item.name}` : undefined}
			>
				{hasChildren ? (
					<span className="text-muted-foreground flex h-4 w-4 items-center justify-center" aria-hidden="true">
						{isExpanded ? <ChevronDown className="h-3.5 w-3.5 shrink-0" /> : <ChevronRight className="h-3.5 w-3.5 shrink-0" />}
					</span>
				) : (
					<span className="h-4 w-4" />
				)}
				{isRoot ? <BookOpen className="text-muted-foreground h-4 w-4" /> : <Folder className="text-muted-foreground h-4 w-4" />}
				<span className="min-w-0 flex-1 truncate font-mono text-xs font-medium" title={isRoot ? "root" : `${item.name}/`}>
					{isRoot ? "root" : `${item.name}/`}
				</span>
				{!readOnly && (
					<div
						className={cn(
							"sticky right-1 z-10 ml-auto flex shrink-0 items-center gap-1 rounded-sm px-0.5 opacity-100 transition-opacity",
							// Root keeps its actions always visible; folders reveal on hover/focus.
							!isRoot && "md:opacity-0 md:group-hover:opacity-100 md:group-focus-within:opacity-100",
							!isRoot && folderDropdownActive && "md:opacity-100",
						)}
						onClick={(event) => event.stopPropagation()}
						onKeyDown={(event) => event.stopPropagation()}
					>
						<DropdownMenu onOpenChange={(open) => setActiveDropdownNodeId(open ? item.id : null)}>
							<DropdownMenuTrigger asChild>
								<Button
									variant="ghost"
									size="icon"
									className="text-muted-foreground h-6 w-6"
									data-testid="skill-file-folder-actions"
									aria-label={`Actions for ${isRoot ? "root" : item.path}`}
								>
									<MoreHorizontal className="h-3.5 w-3.5" />
								</Button>
							</DropdownMenuTrigger>
							<DropdownMenuContent align="end" className="w-44">
								<DropdownMenuSub>
									<DropdownMenuSubTrigger>Add file</DropdownMenuSubTrigger>
									<DropdownMenuSubContent>
										<DropdownMenuItem
											className="cursor-pointer"
											disabled={isFolderUploading}
											onSelect={() => handleAddFileUploadClick(item.path)}
										>
											Upload
										</DropdownMenuItem>
										<DropdownMenuItem
											className="cursor-pointer"
											onSelect={() => {
												expandFolder(item.path || "root");
												setAddingFile({
													folderPath: item.path,
													sourceType: "text",
												});
												setEditingFileIndex(null);
												setEditingFileOriginal(null);
											}}
										>
											From text
										</DropdownMenuItem>
										<DropdownMenuItem
											className="cursor-pointer"
											onSelect={() => {
												expandFolder(item.path || "root");
												setAddingFile({
													folderPath: item.path,
													sourceType: "url",
												});
												setEditingFileIndex(null);
												setEditingFileOriginal(null);
											}}
										>
											From URL
										</DropdownMenuItem>
										<DropdownMenuItem
											className="cursor-pointer"
											onSelect={() => {
												expandFolder(item.path || "root");
												setAddingFile({
													folderPath: item.path,
													sourceType: "dataurl",
												});
												setEditingFileIndex(null);
												setEditingFileOriginal(null);
											}}
										>
											From data URL
										</DropdownMenuItem>
									</DropdownMenuSubContent>
								</DropdownMenuSub>
								<DropdownMenuItem
									className="cursor-pointer"
									onSelect={() => {
										setNewFolderParent(item.path);
										setNewFolderName("");
										setNewFolderError(null);
									}}
								>
									Add folder
								</DropdownMenuItem>
								<DropdownMenuItem
									className="cursor-pointer"
									disabled={isFolderUploading}
									onSelect={() => handleFolderUploadClick(item.path)}
								>
									Upload folder
								</DropdownMenuItem>
								{isRoot && (
									<>
										<DropdownMenuSeparator />
										<DropdownMenuItem className="cursor-pointer" disabled={isAllExpanded} onSelect={onExpandAll}>
											Expand all
										</DropdownMenuItem>
										<DropdownMenuItem className="cursor-pointer" disabled={isAllCollapsed} onSelect={onCollapseAll}>
											Collapse all
										</DropdownMenuItem>
									</>
								)}
								{!isRoot && (
									<>
										<DropdownMenuSeparator />
										{folderMoveTargets.length > 0 && (
											<DropdownMenuSub>
												<DropdownMenuSubTrigger>Move to…</DropdownMenuSubTrigger>
												<DropdownMenuSubContent>
													{folderMoveTargets.map((folderPath) => (
														<DropdownMenuItem
															key={folderPath || "root"}
															className="cursor-pointer"
															onSelect={() => {
																setEditingFileIndex(null);
																setEditingFileOriginal(null);
																moveFolderToFolder(item.path, folderPath);
															}}
														>
															{folderPath || "root"}
														</DropdownMenuItem>
													))}
												</DropdownMenuSubContent>
											</DropdownMenuSub>
										)}
										<DropdownMenuItem variant="destructive" className="cursor-pointer" onSelect={() => requestRemoveFolder(item.path)}>
											Delete folder
										</DropdownMenuItem>
									</>
								)}
							</DropdownMenuContent>
						</DropdownMenu>
					</div>
				)}
			</div>
		);
	};

	return (
		<>
			<input
				ref={folderUploadInputRef}
				type="file"
				multiple
				className="hidden"
				aria-hidden="true"
				tabIndex={-1}
				onChange={handleFolderUploadChange}
				{...{
					webkitdirectory: "",
					directory: "",
				}}
			/>
			<input
				ref={fileUploadInputRef}
				type="file"
				className="hidden"
				aria-hidden="true"
				tabIndex={-1}
				onChange={handleSingleFileUploadChange}
			/>
			{folderUploadState && (
				<div className="mb-2 flex items-center gap-2 rounded-sm border border-blue-200 bg-blue-50 px-3 py-2 text-xs text-blue-900 dark:border-blue-900/50 dark:bg-blue-950/30 dark:text-blue-200">
					<Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
					<span>
						Uploading folder files {folderUploadState.completed}/{folderUploadState.total}
						{folderUploadState.folderPath ? ` into ${folderUploadState.folderPath}/` : " into root"}
					</span>
				</div>
			)}
			{folderUploadError && (
				<div
					className="border-destructive/30 bg-destructive/10 text-destructive mb-2 flex items-center gap-2 rounded-sm border px-3 py-2 text-xs"
					role="alert"
				>
					<AlertCircle className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
					{folderUploadError}
				</div>
			)}
			<div className="mb-2">
				<Input
					value={searchQuery}
					onChange={(e) => {
						setSearchQuery(e.target.value);
						if (e.target.value.trim()) {
							// Expand all folders when searching so results are visible
							setExpandedNodes((prev) => {
								const next: Record<string, boolean> = { ...prev, root: true };
								folders.forEach((f) => {
									next[f] = true;
								});
								return next;
							});
						}
					}}
					placeholder="Search files..."
					className="h-7 font-mono text-xs"
					aria-label="Search files"
				/>
			</div>
			<div className="min-w-max pr-2">
				<Tree
					data={treeData}
					renderItem={renderItem}
					indentSize={22}
					levelsToExpandByDefault={1}
					states={{ expandedNodes, setExpandedNodes }}
				/>
			</div>
			<ScrollBar orientation="horizontal" />

			<AlertDialog
				open={folderToDelete !== null}
				onOpenChange={(open) => {
					if (!open) setFolderToDelete(null);
				}}
			>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Delete folder?</AlertDialogTitle>
						<AlertDialogDescription>
							{folderDeleteImpact?.nestedFiles.length ? (
								<>
									This will remove the folder <b>{folderToDelete}</b>, its nested folders, and all files inside it from this skill draft.
								</>
							) : folderDeleteImpact?.nestedFolders.length ? (
								<>
									This will remove the folder <b>{folderToDelete}</b> and its nested folders from this skill draft. There are no files in
									the hierarchy of this folder.
								</>
							) : (
								<>
									This will remove the empty folder <b>{folderToDelete}</b> from this skill draft.
								</>
							)}
						</AlertDialogDescription>
					</AlertDialogHeader>

					{folderDeleteImpact?.nestedFiles.length ? (
						<div className="bg-muted/20 flex flex-col gap-3 rounded-sm border p-3 text-xs">
							<div className="flex flex-col gap-1">
								<div className="text-muted-foreground text-xs font-medium tracking-wide uppercase">Files</div>
								<ul className="text-muted-foreground flex max-h-32 flex-col gap-1 overflow-auto font-mono">
									{folderDeleteImpact.nestedFiles.map((file) => (
										<li key={file}>{file}</li>
									))}
								</ul>
							</div>
						</div>
					) : null}

					<AlertDialogFooter>
						<AlertDialogCancel>Cancel</AlertDialogCancel>
						<AlertDialogAction
							onClick={() => {
								if (folderToDelete) removeFolder(folderToDelete);
								setFolderToDelete(null);
							}}
						>
							Delete folder
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>

			<AlertDialog
				open={fileToRemove !== null}
				onOpenChange={(open) => {
					if (!open) setFileToRemove(null);
				}}
			>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Remove file?</AlertDialogTitle>
						<AlertDialogDescription>
							{fileToRemove?.isLocal ? (
								<>
									This will remove <b>{fileToRemove.path}</b> from this skill draft.
								</>
							) : (
								<>
									This will remove <b>{fileToRemove?.path}</b> from this skill draft. The file stops being tracked only after you save these
									changes. If you need it back before saving, reload the page to discard this draft state; any other unsaved changes will be
									lost too.
								</>
							)}
						</AlertDialogDescription>
					</AlertDialogHeader>

					{!fileToRemove?.isLocal && (
						<div className="rounded-sm border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-900 dark:border-amber-900/50 dark:bg-amber-950/30 dark:text-amber-200">
							After saving, restoring this file requires re-adding or re-uploading it again.
						</div>
					)}

					<AlertDialogFooter>
						<AlertDialogCancel>Cancel</AlertDialogCancel>
						<AlertDialogAction
							onClick={() => {
								if (fileToRemove) removeFile(fileToRemove.index);
								setFileToRemove(null);
							}}
						>
							Remove file
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</>
	);
}