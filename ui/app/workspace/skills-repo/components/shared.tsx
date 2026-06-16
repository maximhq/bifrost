"use client";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { ScrollArea } from "@/components/ui/scrollArea";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { Markdown } from "@/components/ui/markdown";
import { Tree, type BaseNodeData, type TreeNode } from "@/components/ui/treeView";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { SkillFileEntry } from "@/lib/types/skills";
import { getApiBaseUrl } from "@/lib/utils/port";
import { cn } from "@/lib/utils";
import {
	Check,
	Bot,
	BookOpen,
	ChevronDown,
	ChevronRight,
	ChevronsDownUp,
	ChevronsUpDown,
	ArrowLeft,
	Copy,
	Download,
	ExternalLink,
	FileText,
	Folder,
	FolderOpen,
	Hammer,
	Maximize2,
	MoreHorizontal,
	PanelLeftClose,
	PanelLeftOpen,
	Scale,
	X,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { formatYamlRecord } from "./helpers";
import { FilePreviewPane, getFileServeUrl } from "./filePreview";

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
			<div className={cn("flex flex-col items-start gap-2 bg-white dark:bg-card w-full", sticky && "sticky top-0 z-30 py-4")}>
				<div className="flex w-full flex-row items-center gap-2">
					<div className="flex flex-row items-center gap-2 align-middle">
						{onBack && (
							<Button variant="ghost" size="sm" className="h-8 w-8 shrink-0 p-0" onClick={onBack} aria-label="Go back">
								<ArrowLeft className="h-4 w-4" />
							</Button>
						)}
						<h2 className="min-w-0 truncate text-xl font-semibold tracking-tight">{name}</h2>
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
					<div className="ml-auto flex flex-row items-center align-middle">
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
				<div className={cn("w-full", onBack && "ml-10")}>
					<p className="text-muted-foreground max-w-3xl text-xs">{description}</p>
					<TooltipProvider>
						<div className="mt-3 flex flex-wrap items-center gap-2 pb-2">
							<HeaderMetaItem label="License" value={license} missingText="No license defined" icon={Scale} />
							<HeaderMetaItem label="Compatibility" value={compatibility} missingText="No compatibility defined" icon={Bot} />
							<HeaderMetaItem label="Allowed tools" value={allowedTools} missingText="No allowed tools defined" icon={Hammer} />
						</div>
					</TooltipProvider>
				</div>
			</div>
			{composedSkillMd && (
				<Dialog open={showRawDialog} onOpenChange={setShowRawDialog}>
					<DialogContent
						showCloseButton={false}
						className="h-[90vh] max-h-[90vh] w-[95vw] max-w-[95vw] min-w-0 overflow-hidden border-0 bg-transparent p-0 shadow-none sm:w-[80vw] sm:max-w-[80vw] md:w-[50vw] md:max-w-[50vw]"
					>
						<DialogHeader className="sr-only">
							<DialogTitle>Raw SKILL.md</DialogTitle>
						</DialogHeader>
						<div className="bg-muted relative overflow-hidden rounded-sm border shadow-lg">
							<div className="absolute top-3 right-3 z-10 flex items-center gap-1">
								<Button
									variant="ghost"
									size="icon"
									className="bg-background/70 text-muted-foreground hover:bg-background/90 hover:text-foreground h-8 w-8 rounded-sm"
									onClick={() => copyRawSkillMd(composedSkillMd)}
									aria-label={copiedRawSkillMd ? "Raw SKILL.md copied" : "Copy raw SKILL.md"}
								>
									{copiedRawSkillMd ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
								</Button>
								<DialogClose className="text-muted-foreground hover:bg-background/80 hover:text-foreground cursor-pointer rounded-sm p-1.5 transition-colors">
									<X className="h-4 w-4" />
									<span className="sr-only">Close</span>
								</DialogClose>
							</div>
							<ScrollArea className="h-[90vh]" viewportClassName="bg-muted">
								<pre className="bg-muted min-h-[420px] p-5 pr-24 font-mono text-xs leading-5 whitespace-pre-wrap">{composedSkillMd}</pre>
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
		<section className={cn("space-y-3", className)}>
			<div className="flex items-baseline gap-2 pb-1">
				<h2 className="text-foreground text-base font-semibold tracking-tight">{title}</h2>
				{optional && <span className="text-muted-foreground text-xs">optional</span>}
				{helperText && <span className="text-muted-foreground text-xs">{helperText}</span>}
			</div>
			{children}
		</section>
	);
}

// ---------- ReadOnlyYamlBlock ----------
export function ReadOnlyYamlBlock({ title, value }: { title: string; value: Record<string, unknown> }) {
	const yaml = formatYamlRecord(value);

	return (
		<FormSection title={title}>
			<div>
				<Markdown content={`\`\`\`yaml\n${yaml}\n\`\`\``} />
			</div>
		</FormSection>
	);
}

// ---------- ReadOnlyMetadataTable ----------
export function ReadOnlyMetadataTable({ value }: { value: Record<string, unknown> }) {
	const entries = Object.entries(value);

	return (
		<FormSection title="Metadata">
			<div className="rounded-sm border">
				<div className="bg-muted/30 grid grid-cols-2 border-b px-3 py-2 text-sm font-medium">
					<span>Key</span>
					<span>Value</span>
				</div>
				<div className="text-muted-foreground divide-y">
					{entries.map(([key, item]) => (
						<div key={key} className="grid grid-cols-2 gap-3 px-3 py-2.5 text-sm">
							<p className="min-w-0 truncate font-mono text-xs">{key}</p>
							<p className="min-w-0 font-mono text-xs leading-5 break-words">{String(item)}</p>
						</div>
					))}
				</div>
			</div>
		</FormSection>
	);
}
// ---------- ReadOnlySkillBody ----------

export function ReadOnlySkillBody({ body }: { body: string }) {
	const [activeTab, setActiveTab] = useState("rendered");
	const [dialogTab, setDialogTab] = useState("rendered");
	const [expandOpen, setExpandOpen] = useState(false);
	const [externalLink, setExternalLink] = useState<{ href: string; label: string } | null>(null);

	const markdownComponents = useMemo(
		() => ({
			a: ({ href, children, ...props }: React.ComponentProps<"a">) => {
				const isExternal = Boolean(href && /^https?:\/\//i.test(href));
				const label = typeof children === "string" ? children : href || "external link";

				if (!isExternal || !href) {
					return (
						<a href={href} {...props}>
							{children}
						</a>
					);
				}

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
			},
		}),
		[],
	);

	const openExternalLink = () => {
		if (!externalLink) return;
		window.open(externalLink.href, "_blank", "noopener,noreferrer");
		setExternalLink(null);
	};

	return (
		<FormSection title="SKILL.md Body" className="flex min-h-0 flex-1 flex-col">
			<Tabs defaultValue="rendered" onValueChange={setActiveTab} className="flex min-h-0 w-full flex-1 flex-col">
				<div
					className={cn(
						"relative min-h-[320px] flex-1 overflow-hidden rounded-sm border",
						activeTab === "raw" ? "bg-muted" : "bg-background",
					)}
				>
					<div className="absolute top-3 right-3 z-10 flex items-center gap-1.5">
						<TabsList className="bg-card h-8 shadow-sm backdrop-blur">
							<TabsTrigger value="rendered" className="h-6 px-2.5 text-xs">
								Rendered
							</TabsTrigger>
							<TabsTrigger value="raw" className="h-6 px-2.5 text-xs">
								Raw
							</TabsTrigger>
						</TabsList>
						<button
							type="button"
							className="bg-card text-muted-foreground hover:text-foreground inline-flex h-8 w-8 cursor-pointer items-center justify-center rounded-sm shadow-sm backdrop-blur transition-colors"
							onClick={() => setExpandOpen(true)}
							aria-label="Expand SKILL.md body"
						>
							<Maximize2 className="h-3.5 w-3.5" />
						</button>
					</div>
					<TabsContent value="rendered" className="absolute inset-0 m-0 overflow-hidden">
						<ScrollArea className="h-full">
							<div className="min-w-0 p-4">
								<Markdown
									content={body || ""}
									className="max-w-full text-sm break-words [&_*]:max-w-full [&_*]:break-words [&_a]:break-all [&_code]:whitespace-pre-wrap [&_pre]:break-words [&_pre]:whitespace-pre-wrap [&_table]:table-fixed"
									components={markdownComponents}
								/>
							</div>
						</ScrollArea>
					</TabsContent>
					<TabsContent value="raw" className="bg-muted absolute inset-0 m-0 overflow-hidden">
						<ScrollArea className="h-full" viewportClassName="bg-muted">
							<pre className="bg-muted min-h-full p-4 font-mono text-xs leading-5 whitespace-pre-wrap">{body || "(empty)"}</pre>
						</ScrollArea>
					</TabsContent>
				</div>
			</Tabs>

			<Dialog
				open={expandOpen}
				onOpenChange={(open) => {
					setExpandOpen(open);
					if (open) setDialogTab(activeTab);
				}}
			>
				<DialogContent
					showCloseButton={false}
					className="h-[90vh] max-h-[90vh] w-[95vw] max-w-[95vw] min-w-0 overflow-hidden border-0 bg-transparent p-0 shadow-none sm:w-[80vw] sm:max-w-[80vw] md:w-[50vw] md:max-w-[50vw]"
				>
					<DialogHeader className="sr-only">
						<DialogTitle>SKILL.md Body</DialogTitle>
					</DialogHeader>
					<Tabs value={dialogTab} onValueChange={setDialogTab} className="h-full">
						<div
							className={cn(
								"relative h-full overflow-hidden rounded-sm border shadow-lg",
								dialogTab === "raw" ? "bg-muted" : "bg-background",
							)}
						>
							<div className="absolute top-3 right-3 z-10 flex items-center gap-1.5">
								<TabsList className="bg-card h-8 shadow-sm backdrop-blur">
									<TabsTrigger value="rendered" className="h-6 px-2.5 text-xs">
										Rendered
									</TabsTrigger>
									<TabsTrigger value="raw" className="h-6 px-2.5 text-xs">
										Raw
									</TabsTrigger>
								</TabsList>
								<DialogClose className="bg-card text-muted-foreground hover:text-foreground inline-flex h-8 w-8 cursor-pointer items-center justify-center rounded-sm shadow-sm backdrop-blur transition-colors">
									<X className="h-4 w-4" />
									<span className="sr-only">Close</span>
								</DialogClose>
							</div>
							<TabsContent value="rendered" className="absolute inset-0 m-0 overflow-hidden">
								<ScrollArea className="h-full">
									<div className="min-w-0 p-5">
										<Markdown
											content={body || ""}
											className="max-w-full text-sm break-words [&_*]:max-w-full [&_*]:break-words [&_a]:break-all [&_code]:whitespace-pre-wrap [&_pre]:break-words [&_pre]:whitespace-pre-wrap [&_table]:table-fixed"
											components={markdownComponents}
										/>
									</div>
								</ScrollArea>
							</TabsContent>
							<TabsContent value="raw" className="bg-muted absolute inset-0 m-0 overflow-hidden">
								<ScrollArea className="h-full" viewportClassName="bg-muted">
									<pre className="bg-muted min-h-full p-5 font-mono text-xs leading-5 whitespace-pre-wrap">{body || "(empty)"}</pre>
								</ScrollArea>
							</TabsContent>
						</div>
					</Tabs>
				</DialogContent>
			</Dialog>

			<Dialog open={externalLink != null} onOpenChange={(open) => !open && setExternalLink(null)}>
				<DialogContent>
					<DialogHeader>
						<DialogTitle>Open external link?</DialogTitle>
						<DialogDescription>This link opens outside Bifrost in a new browser tab.</DialogDescription>
					</DialogHeader>
					<div className="bg-muted/40 min-w-0 rounded-sm border px-3 py-2">
						<p className="truncate text-sm font-medium">{externalLink?.label}</p>
						<p className="text-muted-foreground truncate font-mono text-xs">{externalLink?.href}</p>
					</div>
					<DialogFooter>
						<Button variant="outline" onClick={() => setExternalLink(null)}>
							Cancel
						</Button>
						<Button onClick={openExternalLink}>
							<ExternalLink className="h-4 w-4" />
							Open link
						</Button>
					</DialogFooter>
				</DialogContent>
			</Dialog>
		</FormSection>
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

export function ReadOnlyFileTree({
	skillName,
	files,
	composedSkillMd,
	bare = false,
	selectedPath,
	onSelectPath,
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
}) {
	const treeData = useMemo((): TreeNode<FileTreeNodeData>[] => {
		interface FolderBucket {
			files: SkillFileEntry[];
			subfolders: Record<string, FolderBucket>;
		}
		const rootBucket: FolderBucket = { files: [], subfolders: {} };

		for (const file of files) {
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
				children: [{ data: { id: "skillmd", name: "SKILL.md", type: "skillmd" } }, ...bucketToNodes(rootBucket, "")],
			},
		];
	}, [skillName, files]);

	const downloadUrl = `${getApiBaseUrl()}/skills/serve/${encodeURIComponent(skillName)}/download.zip`;

	const tree = (
		<TooltipProvider>
			<Tree<FileTreeNodeData>
				data={treeData}
				levelsToExpandByDefault={1}
				indentSize={28}
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
								(hasChildren || isDownloadable) && "cursor-pointer hover:bg-muted/50",
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
							{hasChildren ? (
								isExpanded ? (
									<ChevronDown className="text-muted-foreground h-3.5 w-3.5 shrink-0" />
								) : (
									<ChevronRight className="text-muted-foreground h-3.5 w-3.5 shrink-0" />
								)
							) : (
								<span className="w-3.5 shrink-0" />
							)}

							{isDownloadable ? (
								isSkillMd ? (
									<BookOpen className="text-muted-foreground h-4 w-4 shrink-0" />
								) : (
									<FileText className="text-muted-foreground h-4 w-4 shrink-0" />
								)
							) : isFolder ? (
								isExpanded ? (
									<FolderOpen className="text-muted-foreground h-4 w-4 shrink-0" />
								) : (
									<Folder className="text-muted-foreground h-4 w-4 shrink-0" />
								)
							) : null}

							<span className={cn("min-w-0 flex-1 truncate font-mono text-xs", isFolder && "font-medium")} title={item.name}>
								{item.name}
							</span>

							{isFolder && !isExpanded && item.childCount != null && item.childCount > 0 && (
								<span className="text-muted-foreground text-[10px]">
									{item.childCount} item{item.childCount !== 1 ? "s" : ""}
								</span>
							)}

							{/* Download context menu (file + SKILL.md rows) */}
							{isDownloadable && (
								<div
									className={cn(
										"shrink-0 opacity-0 transition-opacity group-focus-within:opacity-100 group-hover:opacity-100",
										isSelected && "opacity-100",
									)}
									onClick={(e) => e.stopPropagation()}
									onKeyDown={(e) => e.stopPropagation()}
								>
									<DropdownMenu>
										<DropdownMenuTrigger asChild>
											<Button
												variant="ghost"
												size="icon"
												className="h-6 w-6"
												data-testid="skill-file-row-actions"
												aria-label={`Actions for ${item.name}`}
											>
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
								<div className="ml-auto" onClick={(e) => e.stopPropagation()} onKeyDown={(e) => e.stopPropagation()}>
									<DropdownMenu>
										<DropdownMenuTrigger asChild>
											<Button
												variant="ghost"
												size="icon"
												className="h-6 w-6"
												data-testid="skill-files-tree-actions"
												aria-label="File actions"
											>
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
}: {
	skillName: string;
	skillMdBody: string;
	files: SkillFileEntry[];
	extraFrontmatter: Record<string, unknown> | null;
	metadata: Record<string, unknown> | null;
	composedSkillMd: string;
}) {
	// Default selection is the SKILL.md body, preserving the prior default view.
	const [selected, setSelected] = useState<string>(SKILLMD_KEY);
	const selectedFile = selected === SKILLMD_KEY ? null : (files.find((f) => f.path === selected) ?? null);

	return (
		<div className="flex h-full min-h-0 w-full gap-3">
			{/* Left: collapsible files sidebar */}
			<SkillFilesSidebar
				skillName={skillName}
				files={files}
				composedSkillMd={composedSkillMd}
				selectedPath={selected}
				onSelectPath={setSelected}
			/>

			{/* Right: skill content — frontmatter/metadata, then the selected file or SKILL.md body */}
			<div className="flex min-h-0 min-w-0 flex-1 flex-col gap-6 pr-1 pb-1">
				{extraFrontmatter && <ReadOnlyYamlBlock title="Extra Frontmatter" value={extraFrontmatter} />}
				{metadata && <ReadOnlyMetadataTable value={metadata} />}
				{selectedFile ? (
					<FilePreviewPane file={selectedFile} skillName={skillName} mode="view" />
				) : (
					<ReadOnlySkillBody body={skillMdBody} />
				)}
			</div>
		</div>
	);
}

const FILES_COLLAPSE_STORAGE_KEY = "skill-files-sidebar-collapsed";

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
	const [collapsed, setCollapsed] = useState(false);

	// Load persisted collapsed state on mount
	useEffect(() => {
		if (typeof window === "undefined") return;
		const stored = window.localStorage.getItem(FILES_COLLAPSE_STORAGE_KEY);
		if (stored === "true") setCollapsed(true);
	}, []);

	const toggleCollapsed = useCallback(() => {
		setCollapsed((prev) => {
			const next = !prev;
			if (typeof window !== "undefined") {
				window.localStorage.setItem(FILES_COLLAPSE_STORAGE_KEY, String(next));
			}
			return next;
		});
	}, []);

	// Collapsed: thin rail with a vertical "Files" label — the whole rail expands on click
	if (collapsed) {
		return (
			<button
				type="button"
				data-testid="skill-files-sidebar-show-btn"
				onClick={toggleCollapsed}
				className="bg-card group flex h-full w-10 shrink-0 cursor-pointer flex-col items-center gap-3 rounded-md border py-4 text-sm font-medium"
				title="Show files"
				aria-label="Show files"
			>
				<PanelLeftOpen className="text-muted-foreground group-hover:text-foreground size-4 transition-colors" />
				<span className="rotate-180 select-none [writing-mode:vertical-rl]">Files</span>
			</button>
		);
	}

	return (
		<div className="bg-card flex h-full w-72 shrink-0 flex-col rounded-md border">
			{/* Header */}
			<div className="flex h-11 items-center justify-between border-b pr-2 pl-4">
				<span className="text-sm font-semibold">Files</span>
				<Button
					variant="ghost"
					size="icon"
					data-testid="skill-files-sidebar-hide-btn"
					className="size-7"
					onClick={toggleCollapsed}
					title="Hide files"
					aria-label="Hide files"
				>
					<PanelLeftClose className="size-4" />
				</Button>
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
					/>
				</div>
			</ScrollArea>
		</div>
	);
}