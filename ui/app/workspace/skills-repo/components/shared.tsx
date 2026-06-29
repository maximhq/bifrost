"use client";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ResizableHandle, ResizablePanel, ResizablePanelGroup } from "@/components/ui/resizable";
import { ScrollArea } from "@/components/ui/scrollArea";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { Tree, type BaseNodeData, type TreeNode } from "@/components/ui/treeView";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { SkillFileEntry } from "@/lib/types/skills";
import { cn } from "@/lib/utils";
import { getApiBaseUrl } from "@/lib/utils/port";
import {
	BookOpen,
	Bot,
	Check,
	ChevronDown,
	ChevronRight,
	ChevronsDownUp,
	ChevronsUpDown,
	Copy,
	Download,
	ExternalLink,
	FileText,
	Folder,
	FolderOpen,
	Hammer,
	Info,
	MoreHorizontal,
	Scale,
	Search,
	X,
} from "lucide-react";
import { lazy, Suspense, useEffect, useMemo, useRef, useState, type ComponentProps } from "react";
import { FilePreviewPane, getFileServeUrl } from "./filePreview";
import { formatYamlRecord } from "./helpers";

const LazyMarkdown = lazy(() => import("@/components/ui/markdown").then((m) => ({ default: m.Markdown })));
const Markdown = (props: ComponentProps<typeof LazyMarkdown>) => (
	<Suspense fallback={null}>
		<LazyMarkdown {...props} />
	</Suspense>
);

// Sentinel used as the "selected file" value for the SKILL.md body node.
export const SKILLMD_KEY = "__skillmd__";

// ---------- HeaderMetaItem ----------

export function HeaderMetaItem({
	label,
	value,
	missingText,
	icon: Icon,
}: {
	label: string;
	value?: string;
	missingText: string;
	icon: typeof Scale;
}) {
	const hasValue = Boolean(value?.trim());

	const pill = (
		<div
			className={cn(
				"inline-flex max-w-full items-center gap-1.5 rounded-sm border bg-muted/20 px-2.5 py-1 text-xs",
				!hasValue && "text-muted-foreground",
			)}
		>
			<Icon className="h-3.5 w-3.5 shrink-0" />
			<span className={cn("truncate", hasValue && "font-mono")}>{hasValue ? value : missingText}</span>
		</div>
	);

	if (!hasValue) return pill;

	return (
		<Tooltip>
			<TooltipTrigger asChild>{pill}</TooltipTrigger>
			<TooltipContent className="px-2 py-1 text-xs">{label}</TooltipContent>
		</Tooltip>
	);
}

// ---------- ClampedDescription ----------

/** Shows the description clamped to 3 lines with a Show more/less toggle when it overflows. */
function ClampedDescription({ description }: { description: string }) {
	const [expanded, setExpanded] = useState(false);
	const [isOverflowing, setIsOverflowing] = useState(false);
	const textRef = useRef<HTMLParagraphElement>(null);

	useEffect(() => {
		// Only measure while collapsed — once expanded the clamp is removed so the
		// element always fits, which would otherwise reset isOverflowing to false.
		if (expanded) return;
		const el = textRef.current;
		if (!el) return;
		const check = () => setIsOverflowing(el.scrollHeight > el.clientHeight + 1);
		check();
		const observer = new ResizeObserver(check);
		observer.observe(el);
		return () => observer.disconnect();
	}, [description, expanded]);

	if (!description) return null;

	return (
		<div className="relative max-w-3xl">
			<p ref={textRef} className={cn("text-muted-foreground text-xs", !expanded && "line-clamp-3")}>
				{description}
				{expanded && isOverflowing && (
					<button
						type="button"
						onClick={() => setExpanded(false)}
						className="ml-1 cursor-pointer text-xs font-medium text-blue-600 transition-colors hover:underline dark:text-blue-400"
					>
						Show less
					</button>
				)}
			</p>
			{!expanded && isOverflowing && (
				<button
					type="button"
					onClick={() => setExpanded(true)}
					className="bg-card absolute right-0 bottom-0 cursor-pointer pr-4 pl-8 text-xs font-medium text-blue-600 transition-colors hover:underline dark:text-blue-400"
				>
					<span className="from-card pointer-events-none absolute top-0 right-full h-full w-8 bg-gradient-to-l to-transparent" />
					Show more
				</button>
			)}
		</div>
	);
}

// ---------- SkillHeader ----------

export function SkillHeader({
	name,
	version,
	description,
	license,
	compatibility,
	allowedTools,
	composedSkillMd,
	downloadSkillName,
	decorators,
	actions,
	onBack,
	sticky = true,
}: {
	name: string;
	version: string;
	description: string;
	license?: string;
	compatibility?: string;
	allowedTools?: string;
	composedSkillMd?: string;
	downloadSkillName?: string;
	decorators?: React.ReactNode;
	actions?: React.ReactNode;
	onBack?: () => void;
	sticky?: boolean;
}) {
	const [showRawDialog, setShowRawDialog] = useState(false);
	const { copy: copyRawSkillMd, copied: copiedRawSkillMd } = useCopyToClipboard({
		successMessage: "Copied raw SKILL.md",
		errorMessage: "Failed to copy raw SKILL.md",
	});

	return (
		<>
			<div className={cn("flex w-full flex-row items-center gap-2 bg-card relative", sticky && "sticky top-0 z-30 py-4")}>
				<div className="flex h-5 flex-row items-center gap-2 align-middle">
					{onBack ? (
						<nav aria-label="Breadcrumb" className="min-w-0">
							<ol className="text-muted-foreground flex items-center gap-1.5 text-sm">
								<li>
									<button
										type="button"
										data-testid="skill-back-btn"
										onClick={onBack}
										className="hover:text-foreground cursor-pointer transition-colors"
									>
										Skills
									</button>
								</li>
								<li aria-hidden="true" className="text-muted-foreground/60">
									/
								</li>
								<li aria-current="page" className="text-foreground truncate font-medium">
									{name}
								</li>
							</ol>
						</nav>
					) : (
						<h2 className="truncate text-xl font-semibold">{name}</h2>
					)}
					<Badge variant="secondary" className="shrink-0 font-mono text-xs" role="status">
						v{version}
					</Badge>
					{composedSkillMd && (
						<Button
							variant="link"
							size="sm"
							className="h-auto shrink-0 px-1 py-0 text-xs text-blue-600 dark:text-blue-400"
							onClick={() => setShowRawDialog(true)}
						>
							View raw SKILL.md
						</Button>
					)}
				</div>
				<div className="absolute top-4 right-0 ml-auto flex flex-row items-center align-middle">
					{decorators}
					{(actions || downloadSkillName) && (
						<div className="ml-auto flex shrink-0 items-center gap-1.5">
							{downloadSkillName && (
								<Button variant="outline" size="sm" asChild>
									<a href={`${getApiBaseUrl()}/skills/serve/${encodeURIComponent(downloadSkillName)}/download.zip`} download>
										Download ZIP
									</a>
								</Button>
							)}
							{actions}
						</div>
					)}
				</div>
			</div>
			<div className="w-full">
				<ClampedDescription description={description} />
				<TooltipProvider>
					<div className="mt-4 flex flex-wrap items-center gap-2 pb-2">
						<HeaderMetaItem label="License" value={license} missingText="No license defined" icon={Scale} />
						<HeaderMetaItem label="Compatibility" value={compatibility} missingText="No compatibility defined" icon={Bot} />
						<HeaderMetaItem label="Allowed tools" value={allowedTools} missingText="No allowed tools defined" icon={Hammer} />
					</div>
				</TooltipProvider>
			</div>
			{composedSkillMd && (
				<Dialog open={showRawDialog} onOpenChange={setShowRawDialog}>
					<DialogContent
						showCloseButton={false}
						className="h-[90vh] w-full border-0 p-0 sm:w-[85vw] sm:max-w-[85vw] md:w-[75vw] md:max-w-[75vw]"
					>
						<DialogHeader className="sr-only">
							<DialogTitle>Raw SKILL.md</DialogTitle>
						</DialogHeader>
						<div className="bg-muted relative overflow-hidden rounded-sm border shadow-lg">
							<div className="absolute top-3 right-3 z-10 flex items-center gap-1">
								<Button
									variant="ghost"
									size="icon"
									className="bg-background/70 text-muted-foreground hover:bg-card hover:text-foreground h-8 w-8 rounded-sm"
									onClick={() => copyRawSkillMd(composedSkillMd)}
									aria-label={copiedRawSkillMd ? "Raw SKILL.md copied" : "Copy raw SKILL.md"}
								>
									{copiedRawSkillMd ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
								</Button>
								<DialogClose className="text-muted-foreground hover:bg-card hover:text-foreground flex h-8 w-8 cursor-pointer items-center justify-center rounded-sm transition-colors">
									<X className="h-4 w-4" />
									<span className="sr-only">Close</span>
								</DialogClose>
							</div>
							<ScrollArea className="h-full" viewportClassName="bg-muted">
								<pre className="bg-muted p-5 pr-24 font-mono text-xs leading-5 whitespace-pre-wrap">{composedSkillMd}</pre>
							</ScrollArea>
						</div>
					</DialogContent>
				</Dialog>
			)}
		</>
	);
}

export function FormSection({
	title,
	children,
	className,
	optional,
	helperText,
}: {
	title: string;
	children: React.ReactNode;
	className?: string;
	optional?: boolean;
	helperText?: React.ReactNode;
}) {
	return (
		<section className={cn("flex flex-col gap-2", className)}>
			<div className="flex items-center gap-1.5">
				<Label>{title}</Label>
				{optional && <span className="text-muted-foreground text-xs">optional</span>}
				{helperText && (
					<Tooltip>
						<TooltipTrigger asChild>
							<button
								type="button"
								className="text-muted-foreground hover:text-foreground inline-flex h-4 w-4 items-center justify-center"
								aria-label={`About ${title}`}
							>
								<Info className="h-3.5 w-3.5" aria-hidden="true" />
							</button>
						</TooltipTrigger>
						<TooltipContent className="max-w-xs text-xs">{helperText}</TooltipContent>
					</Tooltip>
				)}
			</div>
			{children}
		</section>
	);
}

// ---------- ReadOnlyYamlBlock ----------
export function ReadOnlyYamlBlock({ title, value, className }: { title: string; value: Record<string, unknown>; className?: string }) {
	const yaml = formatYamlRecord(value);

	return (
		<FormSection title={title} className={cn("flex flex-1 flex-col", className)}>
			<div className="bg-muted/10 flex-1 overflow-y-auto rounded-sm border p-3">
				<Markdown content={`\`\`\`yaml\n${yaml}\n\`\`\``} />
			</div>
		</FormSection>
	);
}

// ---------- ReadOnlyMetadataTable ----------
export function ReadOnlyMetadataTable({ value, className }: { value: Record<string, unknown>; className?: string }) {
	const entries = Object.entries(value);

	return (
		<FormSection title="Metadata" className={cn("flex flex-1 flex-col", className)}>
			<div className="flex flex-1 flex-col rounded-sm border">
				<div className="bg-muted/30 sticky top-0 z-10 grid grid-cols-2 border-b px-3 py-2 text-sm font-medium">
					<span>Key</span>
					<span>Value</span>
				</div>
				<div className="text-muted-foreground flex-1 divide-y overflow-y-auto">
					{entries.map(([key, item]) => (
						<div key={key} className="grid grid-cols-2 gap-3 px-3 py-2.5 text-sm">
							<p className="truncate font-mono text-xs">{key}</p>
							<p className="font-mono text-xs leading-5 break-words">{String(item)}</p>
						</div>
					))}
				</div>
			</div>
		</FormSection>
	);
}
// ---------- ReadOnlySkillBody ----------

// Resolve a relative markdown link (e.g. "./tui/activity.go", "../foo.md") to a
// known skill file path so it can be opened in the sidebar instead of navigated.
function resolveSkillFilePath(href: string, files: SkillFileEntry[]): string | null {
	if (!href) return null;
	// Ignore anchors and protocol-ish links (mailto:, //example.com, etc.).
	if (href.startsWith("#") || href.startsWith("//") || /^[a-z][a-z0-9+.-]*:/i.test(href)) return null;

	// Strip query/hash and any leading "./".
	const cleaned = href.split(/[?#]/)[0].replace(/^\.\//, "");
	if (!cleaned) return null;

	const normalize = (p: string) => p.replace(/^\.?\//, "").replace(/^\/+/, "");
	const target = normalize(cleaned);

	// Exact match on the full path first.
	const exact = files.find((f) => normalize(f.path) === target);
	if (exact) return exact.path;

	// Fall back to matching by trailing path segments (handles "../" prefixes).
	const targetTail = target.replace(/^(\.\.\/)+/, "");
	const byTail = files.find((f) => {
		const fp = normalize(f.path);
		return fp === targetTail || fp.endsWith(`/${targetTail}`);
	});
	if (byTail) return byTail.path;

	// Last resort: match on the bare filename if unambiguous.
	const fileName = target.split("/").pop();
	if (fileName) {
		const matches = files.filter((f) => normalize(f.path).split("/").pop() === fileName);
		if (matches.length === 1) return matches[0].path;
	}

	return null;
}

// Renders SKILL.md markdown with skill-aware links: relative links that resolve to
// a sibling file open it via onSelectFile, external links route through a confirm
// dialog instead of navigating away. Shared by the read-only view and the edit-form
// preview so both behave identically.
export function SkillMarkdown({
	content,
	files = [],
	onSelectFile,
	className,
}: {
	content: string;
	files?: SkillFileEntry[];
	onSelectFile?: (path: string) => void;
	className?: string;
}) {
	const [externalLink, setExternalLink] = useState<{
		href: string;
		label: string;
	} | null>(null);

	const markdownComponents = {
		a: ({ href, children, ...props }: React.ComponentProps<"a">) => {
			const isExternal = Boolean(href && (href.startsWith("//") || /^https?:\/\//i.test(href)));
			const label = typeof children === "string" ? children : href || "external link";

			if (isExternal && href) {
				return (
					<a
						href={href}
						{...props}
						onClick={(event) => {
							props.onClick?.(event);
							if (event.defaultPrevented) return;
							event.preventDefault();
							setExternalLink({ href, label });
						}}
					>
						{children}
					</a>
				);
			}

			// Internal/relative link: resolve to a sibling file and select it in the
			// sidebar instead of letting the browser navigate to a broken path.
			const matchedPath = href ? resolveSkillFilePath(href, files) : null;

			// Resolvable file link: render as a primary-styled button (matching the
			// markdown preview's link treatment) that selects the file in the sidebar.
			if (matchedPath && onSelectFile) {
				return (
					<button
						type="button"
						className="text-primary cursor-pointer appearance-none text-left font-medium wrap-anywhere underline"
						onClick={() => onSelectFile(matchedPath)}
					>
						{children}
					</button>
				);
			}

			return (
				<a href={href} {...props}>
					{children}
				</a>
			);
		},
	};

	return (
		<>
			<Markdown content={content || ""} className={className} components={markdownComponents} />

			<Dialog open={externalLink != null} onOpenChange={(open) => !open && setExternalLink(null)}>
				<DialogContent>
					<DialogHeader>
						<DialogTitle>Open external link?</DialogTitle>
						<DialogDescription>This link opens outside Bifrost in a new browser tab.</DialogDescription>
					</DialogHeader>
					<div className="bg-muted/40 rounded-sm border px-3 py-2">
						<p className="truncate text-sm font-medium">{externalLink?.label}</p>
						<p className="text-muted-foreground truncate font-mono text-xs">{externalLink?.href}</p>
					</div>
					<DialogFooter>
						<Button variant="outline" onClick={() => setExternalLink(null)}>
							Cancel
						</Button>
						<Button
							onClick={() => {
								if (!externalLink) return;
								window.open(externalLink.href, "_blank", "noopener,noreferrer");
								setExternalLink(null);
							}}
						>
							<ExternalLink className="h-4 w-4" />
							Open link
						</Button>
					</DialogFooter>
				</DialogContent>
			</Dialog>
		</>
	);
}

export function ReadOnlySkillBody({
	body,
	files = [],
	onSelectFile,
}: {
	body: string;
	files?: SkillFileEntry[];
	onSelectFile?: (path: string) => void;
}) {
	const [activeTab, setActiveTab] = useState("preview");

	return (
		<Tabs defaultValue="preview" onValueChange={setActiveTab} className="flex min-h-0 w-full flex-1 flex-col gap-2">
			<div className="flex items-center justify-between gap-2">
				<h2 className="text-foreground text-base leading-[normal] font-semibold">SKILL.md Body</h2>
				<TabsList className="bg-muted h-8">
					<TabsTrigger value="preview" className="h-6 px-2.5 text-xs">
						Preview
					</TabsTrigger>
					<TabsTrigger value="raw" className="h-6 px-2.5 text-xs">
						Raw
					</TabsTrigger>
				</TabsList>
			</div>
			<div className={cn("flex min-h-0 flex-1 flex-col overflow-hidden rounded-sm border")}>
				<TabsContent value="preview" className="m-0 flex-1 overflow-y-auto">
					<div className="p-4">
						<SkillMarkdown
							content={body}
							files={files}
							onSelectFile={onSelectFile}
							className="max-w-full text-sm break-words [&_*]:max-w-full [&_*]:break-words [&_a]:break-all [&_code]:whitespace-pre-wrap [&_pre]:break-words [&_pre]:whitespace-pre-wrap [&_table]:table-fixed"
						/>
					</div>
				</TabsContent>
				<TabsContent value="raw" className="m-0 flex-1 overflow-y-auto">
					<pre className="min-h-full p-4 font-mono text-xs leading-5 whitespace-pre-wrap">{body || "(empty)"}</pre>
				</TabsContent>
			</div>
		</Tabs>
	);
}

// ---------- ReadOnlyFileTree ----------

interface FileTreeNodeData extends BaseNodeData {
	type: "root" | "folder" | "file" | "skillmd";
	mime_type?: string;
	source_type?: string;
	file_size_bytes?: number;
	path?: string;
	childCount?: number;
}

function downloadTextAsFile(content: string, filename: string) {
	const blob = new Blob([content], { type: "text/markdown;charset=utf-8" });
	const url = URL.createObjectURL(blob);
	const a = document.createElement("a");
	a.href = url;
	a.download = filename;
	document.body.appendChild(a);
	a.click();
	document.body.removeChild(a);
	URL.revokeObjectURL(url);
}

function fileNameFromPath(filePath: string) {
	return filePath.split("/").filter(Boolean).pop() || filePath;
}

// Simple helper components to avoid nested ternaries in the file tree rows.

function TreeRowChevron({ hasChildren, isExpanded }: { hasChildren: boolean; isExpanded: boolean }) {
	if (!hasChildren) return <span className="w-3.5 shrink-0" />;
	if (isExpanded) return <ChevronDown className="text-muted-foreground h-3.5 w-3.5 shrink-0" />;
	return <ChevronRight className="text-muted-foreground h-3.5 w-3.5 shrink-0" />;
}

function TreeRowIcon({
	isSkillMd,
	isFile,
	isFolder,
	isExpanded,
}: {
	isSkillMd: boolean;
	isFile: boolean;
	isFolder: boolean;
	isExpanded: boolean;
}) {
	if (isSkillMd) return <BookOpen className="text-muted-foreground h-4 w-4 shrink-0" />;
	if (isFile) return <FileText className="text-muted-foreground h-4 w-4 shrink-0" />;
	if (isFolder && isExpanded) return <FolderOpen className="text-muted-foreground h-4 w-4 shrink-0" />;
	if (isFolder) return <Folder className="text-muted-foreground h-4 w-4 shrink-0" />;
	return null;
}

export function ReadOnlyFileTree({
	skillName,
	files,
	composedSkillMd,
	bare = false,
	selectedPath,
	onSelectPath,
	searchQuery = "",
}: {
	skillName: string;
	files: SkillFileEntry[];
	composedSkillMd: string;
	// When true, render only the tree (no "Files" FormSection heading) so it can
	// be embedded in a sidebar that supplies its own header.
	bare?: boolean;
	// When provided, clicking a file/SKILL.md row selects it (for an external
	// preview pane) instead of triggering a direct download. The SKILL.md node
	// is reported as SKILLMD_KEY.
	selectedPath?: string;
	onSelectPath?: (path: string) => void;
	// Filters the tree to files whose path matches (case-insensitive substring).
	searchQuery?: string;
}) {
	const query = searchQuery.trim().toLowerCase();
	const filteredFiles = useMemo(() => (query ? files.filter((f) => f.path.toLowerCase().includes(query)) : files), [files, query]);
	const treeData = useMemo((): TreeNode<FileTreeNodeData>[] => {
		interface FolderBucket {
			files: SkillFileEntry[];
			subfolders: Record<string, FolderBucket>;
		}
		const rootBucket: FolderBucket = { files: [], subfolders: {} };

		for (const file of filteredFiles) {
			const segments = file.path.split("/").filter(Boolean);
			if (segments.length === 0) continue;
			segments.pop();
			let bucket = rootBucket;
			for (const segment of segments) {
				if (!bucket.subfolders[segment]) bucket.subfolders[segment] = { files: [], subfolders: {} };
				bucket = bucket.subfolders[segment];
			}
			bucket.files.push(file);
		}

		const bucketToNodes = (bucket: FolderBucket, parentPath: string): TreeNode<FileTreeNodeData>[] => {
			const nodes: TreeNode<FileTreeNodeData>[] = [];
			for (const [folderName, sub] of Object.entries(bucket.subfolders).sort(([a], [b]) => a.localeCompare(b))) {
				const folderPath = parentPath ? `${parentPath}/${folderName}` : folderName;
				const children = bucketToNodes(sub, folderPath);
				const immediateCount = Object.keys(sub.subfolders).length + sub.files.length;
				nodes.push({
					data: {
						id: `folder-${folderPath}`,
						name: `${folderName}/`,
						type: "folder",
						childCount: immediateCount,
						path: folderPath,
					},
					children,
				});
			}
			for (const file of bucket.files.sort((a, b) => fileNameFromPath(a.path).localeCompare(fileNameFromPath(b.path)))) {
				nodes.push({
					data: {
						id: `file-${file.path}`,
						name: fileNameFromPath(file.path),
						type: "file",
						mime_type: file.mime_type,
						source_type: file.source_type,
						file_size_bytes: file.file_size_bytes,
						path: file.path,
					},
				});
			}
			return nodes;
		};

		return [
			{
				data: {
					id: "root",
					name: `${skillName || "skill"}/`,
					type: "root",
					childCount: Object.keys(rootBucket.subfolders).length + rootBucket.files.length,
				},
				children: [
					...(query ? [] : [{ data: { id: "skillmd", name: "SKILL.md", type: "skillmd" as const } }]),
					...bucketToNodes(rootBucket, ""),
				],
			},
		];
	}, [skillName, filteredFiles, query]);

	// Own the expansion state so we can programmatically reveal a file selected from
	// outside the tree (e.g. a markdown link), expanding its ancestor folders.
	const [expandedNodes, setExpandedNodes] = useState<Record<string, boolean>>({});

	useEffect(() => {
		if (!selectedPath || selectedPath === SKILLMD_KEY) return;
		const segments = selectedPath.split("/").filter(Boolean);
		segments.pop(); // drop the file name, keep folder ancestors
		if (segments.length === 0) return;
		setExpandedNodes((prev) => {
			const next: Record<string, boolean> = { ...prev, root: true };
			let path = "";
			for (const segment of segments) {
				path = path ? `${path}/${segment}` : segment;
				next[`folder-${path}`] = true;
			}
			// Skip the state update if every ancestor is already expanded.
			const changed = Object.keys(next).some((id) => next[id] !== prev[id]);
			return changed ? next : prev;
		});
	}, [selectedPath]);

	const downloadUrl = `${getApiBaseUrl()}/skills/serve/${encodeURIComponent(skillName)}/download.zip`;

	const tree = (
		<TooltipProvider>
			<Tree<FileTreeNodeData>
				data={treeData}
				levelsToExpandByDefault={query ? 99 : 1}
				states={{ expandedNodes, setExpandedNodes }}
				indentSize={28}
				fitContainer
				renderItem={({ item, isExpanded, hasChildren, onToggle, onExpandAll, onCollapseAll, isAllExpanded, isAllCollapsed }) => {
					const isFolder = item.type === "root" || item.type === "folder";
					const isSkillMd = item.type === "skillmd";
					const isFile = item.type === "file";
					const isDownloadable = isSkillMd || isFile;

					const fileDownloadUrl = isFile && item.path ? getFileServeUrl(skillName, item.path) : undefined;

					const selectKey = isSkillMd ? SKILLMD_KEY : item.path;
					const isSelected = !!onSelectPath && isDownloadable && selectedPath != null && selectedPath === selectKey;

					const downloadFile = () => {
						if (isSkillMd) {
							downloadTextAsFile(composedSkillMd, "SKILL.md");
						} else if (isFile && fileDownloadUrl) {
							const a = document.createElement("a");
							a.href = fileDownloadUrl;
							a.download = item.name;
							document.body.appendChild(a);
							a.click();
							document.body.removeChild(a);
						}
					};

					const handleClick = () => {
						if (hasChildren) {
							onToggle();
						} else if (isDownloadable) {
							// Selection mode previews the file; legacy mode downloads on click.
							if (onSelectPath && selectKey != null) onSelectPath(selectKey);
							else downloadFile();
						}
					};

					return (
						<div
							data-selected={isSelected || undefined}
							className={cn(
								"group flex h-7 min-w-0 items-center gap-1.5 rounded-sm px-1.5 text-sm transition-colors",
								(hasChildren || isDownloadable) && "cursor-pointer hover:bg-muted",
								isSelected && "bg-primary/10 text-primary hover:bg-primary/10",
							)}
							onClick={handleClick}
							onKeyDown={(e) => {
								if ((e.key === "Enter" || e.key === " ") && (hasChildren || isDownloadable)) {
									e.preventDefault();
									handleClick();
								}
							}}
							role={hasChildren || isDownloadable ? "button" : undefined}
							tabIndex={hasChildren || isDownloadable ? 0 : undefined}
							aria-label={isFolder ? `${isExpanded ? "Collapse" : "Expand"} ${item.name}` : item.name}
						>
							<TreeRowChevron hasChildren={hasChildren} isExpanded={isExpanded} />
							<TreeRowIcon isSkillMd={isSkillMd} isFile={isFile} isFolder={isFolder} isExpanded={isExpanded} />

							<span className={cn("min-w-0 flex-1 truncate font-mono text-xs", isFolder && "font-medium")} title={item.name}>
								{item.name}
							</span>

							{isFolder && !isExpanded && item.childCount != null && item.childCount > 0 && (
								<span className="text-muted-foreground text-xs">
									{item.childCount} item{item.childCount !== 1 ? "s" : ""}
								</span>
							)}

							{/* Download context menu (file + SKILL.md rows) */}
							{isDownloadable && (
								<div
									className={cn(
										"sticky right-1 z-10 ml-auto shrink-0 rounded-sm bg-muted px-0.5 opacity-0 transition-opacity group-focus-within:opacity-100 group-hover:opacity-100",
									)}
									onClick={(e) => e.stopPropagation()}
									onKeyDown={(e) => e.stopPropagation()}
								>
									<DropdownMenu>
										<DropdownMenuTrigger asChild>
											<Button variant="ghost" size="icon" className="h-6 w-6" aria-label={`Actions for ${item.name}`}>
												<MoreHorizontal className="h-3.5 w-3.5" />
											</Button>
										</DropdownMenuTrigger>
										<DropdownMenuContent align="end">
											<DropdownMenuItem className="cursor-pointer" onSelect={() => downloadFile()}>
												<Download className="h-3.5 w-3.5" />
												Download
											</DropdownMenuItem>
										</DropdownMenuContent>
									</DropdownMenu>
								</div>
							)}

							{item.type === "root" && (
								<div
									className="sticky right-1 z-10 ml-auto rounded-sm px-0.5"
									onClick={(e) => e.stopPropagation()}
									onKeyDown={(e) => e.stopPropagation()}
								>
									<DropdownMenu>
										<DropdownMenuTrigger asChild>
											<Button variant="ghost" size="icon" className="h-6 w-6" aria-label="File actions">
												<MoreHorizontal className="h-3.5 w-3.5" />
											</Button>
										</DropdownMenuTrigger>
										<DropdownMenuContent align="end">
											<DropdownMenuItem className="cursor-pointer" disabled={isAllExpanded} onSelect={() => onExpandAll()}>
												<ChevronsUpDown className="h-3.5 w-3.5" />
												Expand all
											</DropdownMenuItem>
											<DropdownMenuItem className="cursor-pointer" disabled={isAllCollapsed} onSelect={() => onCollapseAll()}>
												<ChevronsDownUp className="h-3.5 w-3.5" />
												Collapse all
											</DropdownMenuItem>
											<DropdownMenuItem className="cursor-pointer" asChild>
												<a href={downloadUrl} download>
													<Download className="h-3.5 w-3.5" />
													Download ZIP
												</a>
											</DropdownMenuItem>
										</DropdownMenuContent>
									</DropdownMenu>
								</div>
							)}
						</div>
					);
				}}
			/>
		</TooltipProvider>
	);

	if (bare) return tree;

	return <FormSection title="Files">{tree}</FormSection>;
}

export function SkillReadOnlyContent({
	skillName,
	skillMdBody,
	files,
	extraFrontmatter,
	metadata,
	composedSkillMd,
	className,
}: {
	skillName: string;
	skillMdBody: string;
	files: SkillFileEntry[];
	extraFrontmatter: Record<string, unknown> | null;
	metadata: Record<string, unknown> | null;
	composedSkillMd: string;
	className?: string;
}) {
	const METADATA_KEY = "__metadata__";
	const FRONTMATTER_KEY = "__extra_frontmatter__";

	// Default selection is the SKILL.md body, preserving the prior default view.
	const [selected, setSelected] = useState<string>(SKILLMD_KEY);
	const selectedFile =
		selected === SKILLMD_KEY || selected === METADATA_KEY || selected === FRONTMATTER_KEY
			? null
			: (files.find((f) => f.path === selected) ?? null);

	const hasMetadata = metadata && Object.keys(metadata).length > 0;
	const hasFrontmatter = extraFrontmatter && Object.keys(extraFrontmatter).length > 0;

	return (
		<ResizablePanelGroup direction="horizontal" className={cn("min-h-0 w-full", className)}>
			<ResizablePanel defaultSize="28%" minSize="18%" maxSize="50%" className="flex min-h-0 flex-col gap-2">
				{/* Metadata & Frontmatter buttons */}
				{(hasMetadata || hasFrontmatter) && (
					<div className="flex gap-1.5">
						{hasMetadata && (
							<button
								type="button"
								data-testid="skill-readonly-metadata-pane-btn"
								onClick={() => setSelected(METADATA_KEY)}
								className={cn(
									"flex-1 rounded-md border px-3 py-2 text-left text-xs font-medium transition-colors",
									selected === METADATA_KEY ? "border-primary/20 bg-primary/10 text-primary hover:bg-primary/10" : "bg-card hover:bg-muted",
								)}
							>
								Metadata
							</button>
						)}
						{hasFrontmatter && (
							<button
								type="button"
								data-testid="skill-readonly-frontmatter-pane-btn"
								onClick={() => setSelected(FRONTMATTER_KEY)}
								className={cn(
									"flex-1 rounded-md border px-3 py-2 text-left text-xs font-medium transition-colors",
									selected === FRONTMATTER_KEY
										? "border-primary/20 bg-primary/10 text-primary hover:bg-primary/10"
										: "bg-card hover:bg-muted",
								)}
							>
								Extra Frontmatter
							</button>
						)}
					</div>
				)}

				{/* Files sidebar */}
				<SkillFilesSidebar
					skillName={skillName}
					files={files}
					composedSkillMd={composedSkillMd}
					selectedPath={selected === METADATA_KEY || selected === FRONTMATTER_KEY ? undefined : selected}
					onSelectPath={setSelected}
				/>
			</ResizablePanel>

			<ResizableHandle className="mx-1.5 bg-transparent" />

			{/* Right: content pane */}
			<ResizablePanel defaultSize="72%" minSize="30%" className="flex min-h-0 flex-col overflow-auto">
				{selected === METADATA_KEY && metadata ? (
					<ReadOnlyMetadataTable value={metadata} />
				) : selected === FRONTMATTER_KEY && extraFrontmatter ? (
					<ReadOnlyYamlBlock title="Extra Frontmatter" value={extraFrontmatter} />
				) : selectedFile ? (
					<FilePreviewPane file={selectedFile} skillName={skillName} mode="view" />
				) : (
					<ReadOnlySkillBody body={skillMdBody} files={files} onSelectFile={setSelected} />
				)}
			</ResizablePanel>
		</ResizablePanelGroup>
	);
}

function SkillFilesSidebar({
	skillName,
	files,
	composedSkillMd,
	selectedPath,
	onSelectPath,
}: {
	skillName: string;
	files: SkillFileEntry[];
	composedSkillMd: string;
	selectedPath?: string;
	onSelectPath?: (path: string) => void;
}) {
	const [searchQuery, setSearchQuery] = useState("");

	return (
		<div className="bg-card flex h-full w-full min-w-0 flex-col rounded-md border">
			{/* Header: search */}
			<div className="flex h-9 items-center border-b">
				<div className="relative grow">
					<Search className="text-muted-foreground absolute top-1/2 left-2.5 h-4 w-4 -translate-y-1/2" />
					<Input
						placeholder="Search files..."
						value={searchQuery}
						onChange={(e) => setSearchQuery(e.target.value)}
						data-testid="sidebar-search"
						className="h-9 border-none pl-8 shadow-none focus-visible:ring-0"
					/>
				</div>
			</div>

			{/* Scrollable tree */}
			<ScrollArea className="min-h-0 flex-1" viewportClassName="[&>div]:!block">
				<div className="p-1">
					<ReadOnlyFileTree
						bare
						skillName={skillName}
						files={files}
						composedSkillMd={composedSkillMd}
						selectedPath={selectedPath}
						onSelectPath={onSelectPath}
						searchQuery={searchQuery}
					/>
				</div>
			</ScrollArea>
		</div>
	);
}