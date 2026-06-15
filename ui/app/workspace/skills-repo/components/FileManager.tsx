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
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdownMenu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import {
  Tree,
  type BaseNodeData,
  type TreeNode,
} from "@/components/ui/treeView";
import { getErrorMessage } from "@/lib/store/apis/baseApi";
import { useUploadSkillFileMutation } from "@/lib/store/apis/skillsApi";
import { SkillFileEntry } from "@/lib/types/skills";
import {
  validateFilePath,
  validateFilename,
  validateSkillFileSize,
  validateSourceType,
} from "@/lib/validators/skills";
import {
  AlertCircle,
  BookOpen,
  Check,
  ChevronDown,
  ChevronRight,
  ChevronsDownUp,
  ChevronsUpDown,
  FileText,
  Folder,
  FolderPlus,
  Info,
  Loader2,
  MoveRight,
  Pencil,
  Plus,
  Search,
  Trash2,
  Upload,
  X,
} from "lucide-react";
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ChangeEvent,
} from "react";
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
  return (
    FILE_SOURCE_OPTIONS.find((option) => option.value === sourceType) ??
    FILE_SOURCE_OPTIONS[0]
  );
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
  return (
    (file as File & { webkitRelativePath?: string }).webkitRelativePath ||
    file.name
  );
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

function FileAddForm({
  folderPath,
  initialSourceType,
  initialEntry,
  submitLabel,
  onAdd,
  onCancel,
  className,
}: FileAddFormProps) {
  const [uploadSkillFile, { isLoading: isUploading }] =
    useUploadSkillFileMutation();
  const sourceType = initialSourceType;
  const sourceOption = getSourceOption(sourceType);
  const initialPath = initialEntry?.path ?? "";
  const [filename, setFilename] = useState(
    initialEntry ? basename(initialPath) : "",
  );
  const [url, setUrl] = useState(initialEntry?.source_url ?? "");
  const [content, setContent] = useState(initialEntry?.content ?? "");
  const [dataurl, setDataurl] = useState(initialEntry?.dataurl ?? "");
  const [selectedFile, setSelectedFile] = useState<File | null>(null);
  const [mimeType, setMimeType] = useState(
    initialEntry?.mime_type ?? "text/plain",
  );
  const [error, setError] = useState<string | null>(null);

  const locationLabel = folderPath ? `${folderPath}/` : "root";

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

    const srcErr = validateSourceType(sourceType, {
      url,
      dataurl,
      content,
      upload_id: selectedFile?.name,
    });
    if (srcErr) return setError(srcErr);

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

  return (
    <div
      className={cn(
        "w-full rounded-sm border border-dashed p-2 space-y-3",
        className,
      )}
    >
      <div className="flex items-center gap-3">
        <span className="inline-flex h-5 shrink-0 items-center rounded-full border border-border/60 bg-transparent px-2 text-[10px] font-medium leading-none text-muted-foreground">
          {sourceOption.label}
        </span>
        <span className="font-mono text-[10px] text-muted-foreground">
          Location: {locationLabel}
        </span>
      </div>

      <div>
        <Label className="text-xs text-muted-foreground">Filename</Label>
        <Input
          data-testid="skill-file-filename-input"
          value={filename}
          onChange={(e) => setFilename(e.target.value)}
          placeholder="review.py"
          className="mt-1 font-mono text-xs h-8"
        />
      </div>

      {sourceType === "url" && (
        <Input
          data-testid="skill-file-url-input"
          value={url}
          onChange={(e) => setUrl(e.target.value)}
          placeholder="https://example.com/file.py"
          className="font-mono text-xs h-8"
          aria-label="Source URL"
        />
      )}
      {sourceType === "text" && (
        <Textarea
          data-testid="skill-file-content-textarea"
          value={content}
          onChange={(e) => setContent(e.target.value)}
          placeholder="File content..."
          className="font-mono text-xs min-h-[80px]"
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
        <div className="space-y-1.5">
          <Input
            data-testid="skill-file-upload-input"
            type="file"
            onChange={(e) =>
              handleUploadFileChange(e.target.files?.[0] ?? null)
            }
            className="h-8 text-xs"
            aria-label="Choose file to upload"
          />
          {selectedFile && (
            <div className="flex items-center gap-2 text-[10px] text-muted-foreground">
              <span className="font-mono truncate">{selectedFile.name}</span>
              <span>{formatFileSize(selectedFile.size)}</span>
              {selectedFile.type && <span>{selectedFile.type}</span>}
            </div>
          )}
        </div>
      )}

      {sourceType === "url" && (
        <div className="flex items-start gap-2 rounded-sm border border-blue-200 bg-blue-50 px-3 py-2 text-xs text-blue-900 dark:border-blue-900/50 dark:bg-blue-950/30 dark:text-blue-200">
          <Info className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          <span>
            This source is saved as a live reference. Bifrost will read from
            this URL when the skill file is retrieved.
          </span>
        </div>
      )}

      {error && (
        <div
          className="flex items-center gap-2 text-destructive text-xs"
          role="alert"
        >
          <AlertCircle className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          {error}
        </div>
      )}

      <div className="flex gap-2 justify-end pt-1">
        <Button
          variant="ghost"
          size="sm"
          className="h-7 text-xs"
          data-testid="skill-file-cancel-btn"
          onClick={onCancel}
        >
          Cancel
        </Button>
        <Button
          size="sm"
          className="h-7 text-xs"
          data-testid="skill-file-save-btn"
          onClick={handleSubmit}
          disabled={isUploading}
        >
          {isUploading ? (
            <>
              <Loader2 className="h-3 w-3 animate-spin" />
              Uploading...
            </>
          ) : (
            <>
              <Plus className="h-3 w-3" />
              {submitLabel ??
                (sourceType === "upload" ? "Upload & add" : "Add")}
            </>
          )}
        </Button>
      </div>
    </div>
  );
}

function fuzzyMatches(value: string, query: string) {
  const normalizedValue = value.toLowerCase();
  const normalizedQuery = query.trim().toLowerCase();
  if (!normalizedQuery) return true;

  let queryIndex = 0;
  for (const char of normalizedValue) {
    if (char === normalizedQuery[queryIndex]) queryIndex += 1;
    if (queryIndex === normalizedQuery.length) return true;
  }
  return false;
}

function getMovedPath(path: string, fromFolder: string, toFolder: string) {
  const relativePath =
    path === fromFolder ? basename(path) : path.slice(fromFolder.length + 1);
  return joinPath(toFolder, relativePath);
}

interface MoveDestinationMenuProps {
  destinations: string[];
  currentFolder: string;
  triggerLabel: string;
  tooltip: string;
  onMove: (folderPath: string) => void;
  onOpenChange?: (open: boolean) => void;
  isDisabledDestination?: (folderPath: string) => boolean;
}

function MoveDestinationMenu({
  destinations,
  currentFolder,
  triggerLabel,
  tooltip,
  onMove,
  onOpenChange,
  isDisabledDestination,
}: MoveDestinationMenuProps) {
  const [query, setQuery] = useState("");
  const searchInputRef = useRef<HTMLInputElement | null>(null);
  const firstEnabledDestinationRef = useRef<HTMLDivElement | null>(null);
  const filteredDestinations = useMemo(
    () =>
      destinations.filter((folderPath) =>
        fuzzyMatches(folderPath || "root", query),
      ),
    [destinations, query],
  );
  const firstEnabledDestination = filteredDestinations.find((folderPath) => {
    const isCurrent = folderPath === currentFolder;
    return !isCurrent && !isDisabledDestination?.(folderPath);
  });

  return (
    <DropdownMenu
      onOpenChange={(open) => {
        onOpenChange?.(open);
        if (!open) {
          setQuery("");
          firstEnabledDestinationRef.current = null;
        }
      }}
    >
      <Tooltip>
        <DropdownMenuTrigger asChild>
          <TooltipTrigger asChild>
            <Button
              variant="ghost"
              size="sm"
              className="h-6 w-6 p-0 text-muted-foreground hover:text-foreground"
              data-testid="skill-file-move-btn"
              aria-label={triggerLabel}
            >
              <MoveRight className="h-3 w-3" />
            </Button>
          </TooltipTrigger>
        </DropdownMenuTrigger>
        <TooltipContent className="px-2 py-1 text-xs">{tooltip}</TooltipContent>
      </Tooltip>
      <DropdownMenuContent align="end" className="w-64 p-1">
        <div
          className="relative p-1"
          onKeyDown={(event) => {
            event.stopPropagation();
            if (event.key === "ArrowDown") {
              event.preventDefault();
              firstEnabledDestinationRef.current?.focus();
            }
          }}
        >
          <Search className="pointer-events-none absolute left-3 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            ref={searchInputRef}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Search folders..."
            className="h-8 pl-8 font-mono text-xs"
            autoFocus
          />
        </div>
        <div className="max-h-64 overflow-y-auto py-1">
          {filteredDestinations.length ? (
            filteredDestinations.map((folderPath) => {
              const isCurrent = folderPath === currentFolder;
              const isDisabled =
                isCurrent || isDisabledDestination?.(folderPath);
              return (
                <DropdownMenuItem
                  key={folderPath || "root"}
                  ref={(node) => {
                    if (folderPath === firstEnabledDestination) {
                      firstEnabledDestinationRef.current = node;
                    }
                  }}
                  className="cursor-pointer font-mono text-xs"
                  disabled={isDisabled}
                  onKeyDown={(event) => {
                    if (
                      event.key === "ArrowUp" &&
                      folderPath === firstEnabledDestination
                    ) {
                      event.preventDefault();
                      searchInputRef.current?.focus();
                    }
                  }}
                  onSelect={() => onMove(folderPath)}
                >
                  {folderPath || "root"}
                </DropdownMenuItem>
              );
            })
          ) : (
            <div className="px-2 py-3 text-center text-xs text-muted-foreground">
              No folders found
            </div>
          )}
        </div>
      </DropdownMenuContent>
    </DropdownMenu>
  );
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
      if (!folder.folders.has(segment))
        folder.folders.set(segment, makeFolder(segment, currentPath));
      folder = folder.folders.get(segment)!;
    }
    folder.files.push({ entry, index });
  });
  return root;
}

function AddMenu({
  onSelect,
  onOpenChange,
}: {
  onSelect: (sourceType: string) => void;
  onOpenChange?: (open: boolean) => void;
}) {
  return (
    <DropdownMenu onOpenChange={onOpenChange}>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="sm"
          className="h-7 text-xs text-muted-foreground"
          data-testid="skill-file-add-btn"
        >
          <Plus className="h-3 w-3" />
          Add file
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start">
        {FILE_SOURCE_OPTIONS.map((option) => (
          <DropdownMenuItem
            key={option.value}
            className="cursor-pointer"
            onSelect={() => onSelect(option.value)}
          >
            {option.label}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

interface FileTreeNodeData extends BaseNodeData {
  kind:
    | "root"
    | "folder"
    | "file"
    | "add-file"
    | "add-folder"
    | "edit-file"
    | "empty-folder";
  path: string;
  folder?: TreeFolder;
  file?: SkillFileEntry;
  fileIndex?: number;
}

function folderToTreeNode(
  folder: TreeFolder,
  editingFullFileIndex: number | null,
): TreeNode<FileTreeNodeData> {
  const childFolders = [...folder.folders.values()].sort((a, b) =>
    a.name.localeCompare(b.name),
  );
  const childFiles = [...folder.files].sort((a, b) =>
    basename(a.entry.path).localeCompare(basename(b.entry.path)),
  );

  return {
    data: {
      id: folder.path || "root",
      name: folder.path ? folder.name : "root",
      kind: folder.path ? "folder" : "root",
      path: folder.path,
      folder,
    },
    children: [
      ...childFolders.map((childFolder) =>
        folderToTreeNode(childFolder, editingFullFileIndex),
      ),
      ...childFiles.map(({ entry, index }) => ({
        data: {
          id:
            editingFullFileIndex === index
              ? `draft-edit:${index}:${entry.path}`
              : `file:${index}:${entry.path}`,
          name: basename(entry.path),
          kind:
            editingFullFileIndex === index
              ? ("edit-file" as const)
              : ("file" as const),
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
    node.children =
      placement === "start"
        ? [child, ...(node.children ?? [])]
        : [...(node.children ?? []), child];
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
}: {
  files: SkillFileEntry[];
  onAddFile: (entry: SkillFileEntry) => void;
  onRemoveFile: (index: number) => void;
  onUpdateFile: (index: number, updates: Partial<SkillFileEntry>) => void;
  readOnly: boolean;
}) {
  const [uploadSkillFile] = useUploadSkillFileMutation();
  const folderUploadInputRef = useRef<HTMLInputElement | null>(null);
  const folderUploadTargetRef = useRef("");
  const [folderUploadState, setFolderUploadState] = useState<{
    folderPath: string;
    total: number;
    completed: number;
  } | null>(null);
  const [folderUploadError, setFolderUploadError] = useState<string | null>(
    null,
  );
  const [editingFileIndex, setEditingFileIndex] = useState<number | null>(null);
  const [editingFileOriginal, setEditingFileOriginal] = useState<{
    path: string;
  } | null>(null);
  const [editingFullFileIndex, setEditingFullFileIndex] = useState<
    number | null
  >(null);
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
  const [expandedNodes, setExpandedNodes] = useState<Record<string, boolean>>(
    {},
  );
  const [activeDropdownNodeId, setActiveDropdownNodeId] = useState<
    string | null
  >(null);

  useEffect(() => {
    const foldersFromFiles = files.flatMap((file) =>
      getFolderAncestors(file.path),
    );
    if (foldersFromFiles.length === 0) return;
    setFolders((prev) => {
      const next = new Set(prev);
      foldersFromFiles.forEach((folder) => next.add(folder));
      return next.size === prev.length ? prev : [...next];
    });
  }, [files]);

  const treeStates = useMemo(
    () => ({ expandedNodes, setExpandedNodes }),
    [expandedNodes],
  );

  const expandFolder = useCallback((folderPath: string) => {
    // Expand root, the target folder, and all its ancestors
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
  }, []);

  const isFolderUploading = folderUploadState !== null;

  const treeData = useMemo(() => {
    const rootFolder = buildTree(files);
    for (const folderPath of folders) {
      const segments = folderPath.split("/").filter(Boolean);
      let folder = rootFolder;
      let currentPath = "";
      for (const segment of segments) {
        currentPath = joinPath(currentPath, segment);
        if (!folder.folders.has(segment))
          folder.folders.set(segment, makeFolder(segment, currentPath));
        folder = folder.folders.get(segment)!;
      }
    }

    const rootNode = folderToTreeNode(rootFolder, editingFullFileIndex);

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
      const isEmptyDraftFolder =
        node.data.kind === "folder" &&
        folders.includes(node.data.path) &&
        (node.children?.length ?? 0) === 0;
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
  }, [addingFile, editingFullFileIndex, files, folders, newFolderParent]);

  const availableFolderPaths = useMemo(() => {
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
  }, [files, folders]);

  const handleFolderUploadClick = (folderPath: string) => {
    if (isFolderUploading) return;
    folderUploadTargetRef.current = folderPath;
    setFolderUploadError(null);
    folderUploadInputRef.current?.click();
  };

  const handleFolderUploadChange = async (
    event: ChangeEvent<HTMLInputElement>,
  ) => {
    const selectedFiles = Array.from(event.target.files ?? []);
    event.target.value = "";
    if (selectedFiles.length === 0) return;

    const targetFolderPath = folderUploadTargetRef.current;
    setFolderUploadError(null);
    setAddingFile(null);
    setEditingFileIndex(null);
    setEditingFileOriginal(null);
    setEditingFullFileIndex(null);

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
        if (
          files.some((f) => f.path === fullPath) ||
          entries.some((e) => e.path === fullPath)
        )
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
          mime_type:
            upload.mime_type || file.type || "application/octet-stream",
          file_size_bytes: upload.file_size_bytes,
          __local: false,
        });
        getFolderAncestors(fullPath).forEach((folder) =>
          importedFolders.add(folder),
        );
        setFolderUploadState((current) =>
          current ? { ...current, completed: current.completed + 1 } : current,
        );
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
      return files.some(
        (f) => !f.path.startsWith(`${folderPath}/`) && f.path === movedPath,
      );
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
      getFolderAncestors(`${nextFolderPath}/placeholder.txt`).forEach(
        (folder) => next.add(folder),
      );
      return [...next];
    });

    if (
      addingFile?.folderPath.startsWith(`${folderPath}/`) ||
      addingFile?.folderPath === folderPath
    ) {
      setAddingFile({
        ...addingFile,
        folderPath: getMovedPath(
          addingFile.folderPath,
          folderPath,
          nextFolderPath,
        ),
      });
    }
    if (
      newFolderParent?.startsWith(`${folderPath}/`) ||
      newFolderParent === folderPath
    ) {
      setNewFolderParent(
        getMovedPath(newFolderParent, folderPath, nextFolderPath),
      );
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
    if (editingFullFileIndex !== null) {
      if (editingFullFileIndex === index) {
        setEditingFullFileIndex(null);
      } else if (index < editingFullFileIndex) {
        setEditingFullFileIndex(editingFullFileIndex - 1);
      }
    }
  };

  const folderDeleteImpact = useMemo(() => {
    if (!folderToDelete) return null;
    const nestedFiles = files
      .filter((file) => file.path.startsWith(`${folderToDelete}/`))
      .map((file) => file.path)
      .sort((a, b) => a.localeCompare(b));
    const nestedFolders = folders
      .filter(
        (folder) =>
          folder !== folderToDelete && folder.startsWith(`${folderToDelete}/`),
      )
      .sort((a, b) => a.localeCompare(b));

    return { nestedFiles, nestedFolders };
  }, [files, folderToDelete, folders]);

  const removeFolder = (folderPath: string) => {
    setFolders((prev) =>
      prev.filter(
        (folder) =>
          folder !== folderPath && !folder.startsWith(`${folderPath}/`),
      ),
    );
    const removedIndices = files
      .map((file, index) => ({ file, index }))
      .filter(
        ({ file }) =>
          file.path === folderPath || file.path.startsWith(`${folderPath}/`),
      )
      .map(({ index }) => index)
      .sort((a, b) => b - a);

    removedIndices.forEach((index) => onRemoveFile(index));

    const adjustIndexAfterBatchRemove = (currentIndex: number | null) => {
      if (currentIndex === null) return currentIndex;
      if (removedIndices.includes(currentIndex)) return null;
      const removedBeforeCurrent = removedIndices.filter(
        (index) => index < currentIndex,
      ).length;
      return currentIndex - removedBeforeCurrent;
    };

    setEditingFileIndex((currentIndex) => {
      const nextIndex = adjustIndexAfterBatchRemove(currentIndex);
      if (nextIndex === null && currentIndex !== null) {
        setEditingFileOriginal(null);
      }
      return nextIndex;
    });
    setEditingFullFileIndex(adjustIndexAfterBatchRemove);
    if (
      addingFile?.folderPath === folderPath ||
      addingFile?.folderPath.startsWith(`${folderPath}/`)
    )
      setAddingFile(null);
    if (
      newFolderParent === folderPath ||
      newFolderParent?.startsWith(`${folderPath}/`)
    ) {
      setNewFolderParent(null);
      setNewFolderName("");
      setNewFolderError(null);
    }
  };

  const requestRemoveFolder = (folderPath: string) => {
    const hasNestedFiles = files.some((file) =>
      file.path.startsWith(`${folderPath}/`),
    );
    const hasNestedFolders = folders.some(
      (folder) => folder !== folderPath && folder.startsWith(`${folderPath}/`),
    );

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
    setEditingFullFileIndex(null);
  };

  const addFolder = (parentPath: string) => {
    setNewFolderError(null);
    const nameErr = validateFilename(newFolderName);
    if (nameErr) {
      setNewFolderError(nameErr);
      return;
    }
    const nextPath = joinPath(parentPath, newFolderName);
    if (
      folders.includes(nextPath) ||
      files.some(
        (file) =>
          file.path === nextPath || file.path.startsWith(`${nextPath}/`),
      )
    ) {
      setNewFolderError("A folder with this name already exists here");
      return;
    }
    const pathErr = validateFilePath(`${nextPath}/placeholder.txt`);
    if (pathErr) {
      setNewFolderError(pathErr);
      return;
    }
    setFolders((prev) =>
      prev.includes(nextPath) ? prev : [...prev, nextPath],
    );
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
    if (item.kind === "add-folder") {
      return (
        <div className="space-y-1.5 py-1 ml-1">
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
            <div
              className="flex items-center gap-2 text-xs text-destructive"
              role="alert"
            >
              <AlertCircle
                className="h-3.5 w-3.5 shrink-0"
                aria-hidden="true"
              />
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

    if (item.kind === "edit-file" && item.file && item.fileIndex != null) {
      return (
        <FileAddForm
          className="ml-1"
          folderPath={dirname(item.file.path)}
          initialSourceType={item.file.source_type}
          initialEntry={item.file}
          submitLabel="Update"
          onAdd={(entry) => {
            if (
              files.some(
                (f, i) => i !== item.fileIndex && f.path === entry.path,
              )
            ) {
              toast.error("A file already exists at that path");
              return;
            }
            onUpdateFile(item.fileIndex!, entry);
            setEditingFullFileIndex(null);
          }}
          onCancel={() => setEditingFullFileIndex(null)}
        />
      );
    }

    if (item.kind === "empty-folder") {
      return (
        <div className="flex items-center gap-2 py-1 text-xs text-muted-foreground ml-1">
          <span>Empty folder</span>
          <span className="text-[10px] text-muted-foreground/60">
            Not saved until it contains a file.
          </span>
        </div>
      );
    }

    if (item.kind === "file" && item.file && item.fileIndex != null) {
      const file = item.file;
      const index = item.fileIndex;
      const canFullEdit = !!file.__local && file.source_type !== "upload";

      if (editingFileIndex === index) {
        return (
          <div className="space-y-2 p-1 border rounded-sm border-dashed ml-1">
            <div className="flex items-center gap-2">
              <FileText className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
              <Input
                value={basename(file.path)}
                onChange={(e) => {
                  const newPath = joinPath(dirname(file.path), e.target.value);
                  if (files.some((f, i) => i !== index && f.path === newPath)) {
                    toast.error("A file already exists at that path");
                    return;
                  }
                  onUpdateFile(index, { path: newPath });
                }}
                placeholder="filename.ext"
                className="h-7 font-mono text-xs"
                aria-label="File name"
              />
              <Button
                variant="ghost"
                size="sm"
                className="h-7 w-7 p-0 text-muted-foreground hover:text-destructive"
                onClick={() => {
                  if (editingFileOriginal)
                    onUpdateFile(index, { path: editingFileOriginal.path });
                  setEditingFileIndex(null);
                  setEditingFileOriginal(null);
                }}
              >
                <X className="h-3 w-3" />
              </Button>
              <Button
                variant="ghost"
                size="sm"
                className="h-7 w-7 p-0 text-muted-foreground"
                onClick={() => {
                  const err = validateFilePath(file.path);
                  const filenameErr = validateFilename(basename(file.path));
                  if (!err && !filenameErr) {
                    setEditingFileIndex(null);
                    setEditingFileOriginal(null);
                  }
                }}
              >
                <Check className="h-3 w-3" />
              </Button>
            </div>
            <div className="flex items-start gap-2 rounded-sm border border-blue-200 bg-blue-50 px-3 py-2 text-xs text-blue-900 dark:border-blue-900/50 dark:bg-blue-950/30 dark:text-blue-200">
              <Info
                className="mt-0.5 h-3.5 w-3.5 shrink-0"
                aria-hidden="true"
              />
              <span>
                Committed files keep their stored reference. Rename here; use
                Move to place the file in another folder.
              </span>
            </div>
          </div>
        );
      }

      const fileDropdownActive = activeDropdownNodeId === item.id;

      return (
        <div
          className={cn(
            "group flex items-center gap-3 rounded-sm px-2 py-1.5 text-sm transition-colors hover:bg-muted/50",
            fileDropdownActive && "bg-muted/50",
          )}
        >
          <div className="flex min-w-0 items-center gap-2">
            <FileText className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
            <span className="truncate font-mono text-xs">
              {basename(file.path)}
            </span>
          </div>
          <div className="flex shrink-0 items-center gap-2">
            <Tooltip>
              <TooltipTrigger asChild>
                <span className="inline-flex h-5 shrink-0 items-center rounded-full border border-border/60 bg-transparent px-2 text-[10px] font-medium leading-none text-muted-foreground">
                  {file.source_type}
                </span>
              </TooltipTrigger>
              <TooltipContent className="px-2 py-1 text-xs">
                Source
              </TooltipContent>
            </Tooltip>
            {file.mime_type && (
              <span className="shrink-0 text-[10px] text-muted-foreground/60">
                {file.mime_type}
              </span>
            )}
            {file.file_size_bytes != null && file.file_size_bytes > 0 && (
              <span className="shrink-0 text-[10px] text-muted-foreground/60">
                {formatFileSize(file.file_size_bytes)}
              </span>
            )}
          </div>
          {!readOnly && (
            <div
              className={cn(
                "flex-1 justify-end flex shrink-0 items-center gap-1 opacity-100 md:opacity-0 transition-opacity md:group-hover:opacity-100 md:group-focus-within:opacity-100",
                fileDropdownActive && "md:opacity-100",
              )}
            >
              <MoveDestinationMenu
                destinations={availableFolderPaths}
                currentFolder={dirname(file.path)}
                triggerLabel={`Move ${file.path}`}
                tooltip="Move file"
                onOpenChange={(open) =>
                  setActiveDropdownNodeId(open ? item.id : null)
                }
                onMove={(folderPath) => {
                  setEditingFileIndex(null);
                  setEditingFileOriginal(null);
                  setEditingFullFileIndex(null);
                  moveFileToFolder(index, folderPath);
                }}
              />
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-6 w-6 p-0 text-muted-foreground hover:text-foreground"
                    data-testid={`skill-file-edit-${basename(file.path)}`}
                    aria-label={`Edit ${file.path}`}
                    onClick={() => {
                      if (canFullEdit) {
                        setAddingFile(null);
                        setEditingFileIndex(null);
                        setEditingFileOriginal(null);
                        setEditingFullFileIndex(index);
                        return;
                      }
                      setEditingFullFileIndex(null);
                      setEditingFileIndex(index);
                      setEditingFileOriginal({ path: file.path });
                    }}
                  >
                    <Pencil className="h-3 w-3" />
                  </Button>
                </TooltipTrigger>
                <TooltipContent className="px-2 py-1 text-xs">
                  {canFullEdit ? "Edit file" : "Rename file"}
                </TooltipContent>
              </Tooltip>
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-6 w-6 p-0 text-muted-foreground hover:text-destructive"
                    data-testid={`skill-file-delete-${basename(file.path)}`}
                    aria-label={`Remove ${file.path}`}
                    onClick={() =>
                      setFileToRemove({
                        index,
                        path: file.path,
                        isLocal: !!file.__local,
                      })
                    }
                  >
                    <X className="h-3 w-3" />
                  </Button>
                </TooltipTrigger>
                <TooltipContent className="px-2 py-1 text-xs">
                  Remove file
                </TooltipContent>
              </Tooltip>
            </div>
          )}
        </div>
      );
    }

    const isRoot = item.kind === "root";
    const folderDropdownActive = activeDropdownNodeId === item.id;

    return (
      <div
        className={cn(
          "group flex items-center gap-2 rounded-sm px-2 py-1.5 text-sm hover:bg-muted/40",
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
        aria-label={
          hasChildren
            ? `${isExpanded ? "Collapse" : "Expand"} ${isRoot ? "root" : item.name}`
            : undefined
        }
      >
        {hasChildren ? (
          <span
            className="flex h-4 w-4 items-center justify-center text-muted-foreground"
            aria-hidden="true"
          >
            {isExpanded ? (
              <ChevronDown className="h-3.5 w-3.5 shrink-0" />
            ) : (
              <ChevronRight className="h-3.5 w-3.5 shrink-0" />
            )}
          </span>
        ) : (
          <span className="h-4 w-4" />
        )}
        {isRoot ? (
          <BookOpen className="h-4 w-4 text-muted-foreground" />
        ) : (
          <Folder className="h-4 w-4 text-muted-foreground" />
        )}
        <span className="font-mono text-xs font-medium">
          {isRoot ? "root" : `${item.name}/`}
        </span>
        {isRoot && (
          <Badge variant="secondary" className="text-[10px]">
            SKILL.md
          </Badge>
        )}
        {!readOnly && editingFullFileIndex == null && (
          <div
            className={cn(
              "ml-auto flex items-center gap-1 opacity-100 md:opacity-0 transition-opacity md:group-hover:opacity-100 md:group-focus-within:opacity-100",
              folderDropdownActive && "md:opacity-100",
            )}
            onClick={(event) => event.stopPropagation()}
            onKeyDown={(event) => event.stopPropagation()}
          >
            {isRoot && (
              <>
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-7 w-7 p-0 text-muted-foreground"
                      data-testid="skill-file-expand-all-btn"
                      aria-label="Expand all folders"
                      onClick={onExpandAll}
                      disabled={isAllExpanded}
                    >
                      <ChevronsUpDown className="h-3.5 w-3.5" />
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent className="px-2 py-1 text-xs">
                    Expand all
                  </TooltipContent>
                </Tooltip>
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-7 w-7 p-0 text-muted-foreground"
                      data-testid="skill-file-collapse-all-btn"
                      aria-label="Collapse all folders"
                      onClick={onCollapseAll}
                      disabled={isAllCollapsed}
                    >
                      <ChevronsDownUp className="h-3.5 w-3.5" />
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent className="px-2 py-1 text-xs">
                    Collapse all
                  </TooltipContent>
                </Tooltip>
              </>
            )}
            <AddMenu
              onOpenChange={(open) =>
                setActiveDropdownNodeId(open ? item.id : null)
              }
              onSelect={(sourceType) => {
                expandFolder(item.path || "root");
                setAddingFile({ folderPath: item.path, sourceType });
                setEditingFileIndex(null);
                setEditingFileOriginal(null);
              }}
            />
            <Button
              variant="ghost"
              size="sm"
              className="h-7 text-xs text-muted-foreground"
              data-testid="skill-file-folder-upload-btn"
              onClick={() => handleFolderUploadClick(item.path)}
              disabled={isFolderUploading}
            >
              {folderUploadState?.folderPath === item.path ? (
                <Loader2 className="h-3 w-3 animate-spin" />
              ) : (
                <Upload className="h-3 w-3" />
              )}
              Upload folder
            </Button>
            <Button
              variant="ghost"
              size="sm"
              className="h-7 text-xs text-muted-foreground"
              data-testid="skill-file-add-folder-btn"
              onClick={() => {
                setNewFolderParent(item.path);
                setNewFolderName("");
                setNewFolderError(null);
              }}
            >
              <FolderPlus className="h-3 w-3" />
              Folder
            </Button>
            {!isRoot && (
              <MoveDestinationMenu
                destinations={availableFolderPaths}
                currentFolder={dirname(item.path)}
                triggerLabel={`Move folder ${item.path}`}
                tooltip="Move folder"
                onOpenChange={(open) =>
                  setActiveDropdownNodeId(open ? item.id : null)
                }
                isDisabledDestination={(folderPath) =>
                  folderPath === item.path ||
                  folderPath.startsWith(`${item.path}/`)
                }
                onMove={(folderPath) => {
                  setEditingFileIndex(null);
                  setEditingFileOriginal(null);
                  setEditingFullFileIndex(null);
                  moveFolderToFolder(item.path, folderPath);
                }}
              />
            )}
            {!isRoot && (
              <Button
                variant="ghost"
                size="sm"
                className="h-7 w-7 p-0 text-muted-foreground hover:text-destructive"
                data-testid={`skill-file-delete-folder-${item.name}`}
                aria-label={`Delete folder ${item.path}`}
                onClick={() => requestRemoveFolder(item.path)}
              >
                <Trash2 className="h-3 w-3" />
              </Button>
            )}
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
      {folderUploadState && (
        <div className="mb-2 flex items-center gap-2 rounded-sm border border-blue-200 bg-blue-50 px-3 py-2 text-xs text-blue-900 dark:border-blue-900/50 dark:bg-blue-950/30 dark:text-blue-200">
          <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
          <span>
            Uploading folder files {folderUploadState.completed}/
            {folderUploadState.total}
            {folderUploadState.folderPath
              ? ` into ${folderUploadState.folderPath}/`
              : " into root"}
          </span>
        </div>
      )}
      {folderUploadError && (
        <div
          className="mb-2 flex items-center gap-2 rounded-sm border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
          role="alert"
        >
          <AlertCircle className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          {folderUploadError}
        </div>
      )}
      <Tree
        data={treeData}
        renderItem={renderItem}
        indentSize={22}
        levelsToExpandByDefault={1}
        states={treeStates}
      />

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
                  This will remove the folder <b>{folderToDelete}</b>, its
                  nested folders, and all files inside it from this skill draft.
                </>
              ) : folderDeleteImpact?.nestedFolders.length ? (
                <>
                  This will remove the folder <b>{folderToDelete}</b> and its
                  nested folders from this skill draft. There are no files in
                  the hierarchy of this folder.
                </>
              ) : (
                <>
                  This will remove the empty folder <b>{folderToDelete}</b> from
                  this skill draft.
                </>
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>

          {folderDeleteImpact?.nestedFiles.length ? (
            <div className="space-y-3 rounded-sm border bg-muted/20 p-3 text-xs">
              <div className="space-y-1">
                <div className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
                  Files
                </div>
                <ul className="max-h-32 space-y-1 overflow-auto font-mono text-muted-foreground">
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
                  This will remove <b>{fileToRemove.path}</b> from this skill
                  draft.
                </>
              ) : (
                <>
                  This will remove <b>{fileToRemove?.path}</b> from this skill
                  draft. The file stops being tracked only after you save these
                  changes. If you need it back before saving, reload the page to
                  discard this draft state; any other unsaved changes will be
                  lost too.
                </>
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>

          {!fileToRemove?.isLocal && (
            <div className="rounded-sm border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-900 dark:border-amber-900/50 dark:bg-amber-950/30 dark:text-amber-200">
              After saving, restoring this file requires re-adding or
              re-uploading it again.
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
