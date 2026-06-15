"use client";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { ScrollArea } from "@/components/ui/scrollArea";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { Markdown } from "@/components/ui/markdown";
import {
  Tree,
  type BaseNodeData,
  type TreeNode,
} from "@/components/ui/treeView";
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
  ChevronsDown,
  ChevronsUp,
  ArrowLeft,
  Copy,
  Download,
  FileText,
  Folder,
  FolderOpen,
  Hammer,
  Maximize2,
  Scale,
  X,
} from "lucide-react";
import { useMemo, useState } from "react";
import { formatFileSize, formatYamlRecord } from "./helpers";

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
      <span className={cn("truncate", hasValue && "font-mono")}>
        {hasValue ? value : missingText}
      </span>
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
      <div
        className={cn(
          "flex items-center gap-2 bg-white dark:bg-card",
          sticky && "sticky top-0 z-30 py-4",
        )}
      >
        {onBack && (
          <Button
            variant="ghost"
            size="sm"
            className="h-8 w-8 p-0 shrink-0"
            onClick={onBack}
            aria-label="Go back"
          >
            <ArrowLeft className="h-4 w-4" />
          </Button>
        )}
        <h2 className="min-w-0 truncate text-xl font-semibold tracking-tight">
          {name}
        </h2>
        <Badge
          variant="secondary"
          className="font-mono text-xs shrink-0"
          role="status"
        >
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
        {decorators}
        {(actions || downloadSkillName) && (
          <div className="ml-auto flex shrink-0 items-center gap-1.5">
            {downloadSkillName && (
              <Button
                variant="ghost"
                size="icon"
                className="h-8 w-8"
                asChild
                aria-label="Download ZIP"
              >
                <a
                  href={`${getApiBaseUrl()}/skills/serve/${encodeURIComponent(downloadSkillName)}/download.zip`}
                  download
                >
                  <Download className="h-4 w-4" />
                  <span className="sr-only">Download ZIP</span>
                </a>
              </Button>
            )}
            {actions}
          </div>
        )}
      </div>
      <p className="text-muted-foreground text-xs mt-0.5 max-w-3xl">
        {description}
      </p>
      <TooltipProvider>
        <div className="mt-3 flex flex-wrap items-center gap-2 pb-2">
          <HeaderMetaItem
            label="License"
            value={license}
            missingText="No license defined"
            icon={Scale}
          />
          <HeaderMetaItem
            label="Compatibility"
            value={compatibility}
            missingText="No compatibility defined"
            icon={Bot}
          />
          <HeaderMetaItem
            label="Allowed tools"
            value={allowedTools}
            missingText="No allowed tools defined"
            icon={Hammer}
          />
        </div>
      </TooltipProvider>
      {composedSkillMd && (
        <Dialog open={showRawDialog} onOpenChange={setShowRawDialog}>
          <DialogContent
            showCloseButton={false}
            className="h-[90vh] max-h-[90vh] w-[95vw] sm:w-[80vw] md:w-[50vw] min-w-0 max-w-[95vw] sm:max-w-[80vw] md:max-w-[50vw] overflow-hidden border-0 bg-transparent p-0 shadow-none"
          >
            <DialogHeader className="sr-only">
              <DialogTitle>Raw SKILL.md</DialogTitle>
            </DialogHeader>
            <div className="relative overflow-hidden rounded-sm border bg-muted shadow-lg">
              <div className="absolute right-3 top-3 z-10 flex items-center gap-1">
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-8 w-8 rounded-sm bg-background/70 text-muted-foreground hover:bg-background/90 hover:text-foreground"
                  onClick={() => copyRawSkillMd(composedSkillMd)}
                  aria-label={copiedRawSkillMd ? "Raw SKILL.md copied" : "Copy raw SKILL.md"}
                >
                  {copiedRawSkillMd ? (
                    <Check className="h-4 w-4" />
                  ) : (
                    <Copy className="h-4 w-4" />
                  )}
                </Button>
                <DialogClose className="cursor-pointer rounded-sm p-1.5 text-muted-foreground transition-colors hover:bg-background/80 hover:text-foreground">
                  <X className="h-4 w-4" />
                  <span className="sr-only">Close</span>
                </DialogClose>
              </div>
              <ScrollArea className="h-[90vh]" viewportClassName="bg-muted">
                <pre className="min-h-[420px] bg-muted p-5 pr-24 text-xs font-mono leading-5 whitespace-pre-wrap">
                  {composedSkillMd}
                </pre>
              </ScrollArea>
            </div>
          </DialogContent>
        </Dialog>
      )}
    </>
  );
}

// ---------- FormSection ----------

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
        <h2 className="text-base font-semibold tracking-tight text-foreground">
          {title}
        </h2>
        {optional && (
          <span className="text-xs text-muted-foreground">optional</span>
        )}
        {helperText && (
          <span className="text-xs text-muted-foreground">{helperText}</span>
        )}
      </div>
      {children}
    </section>
  );
}

// ---------- ReadOnlyYamlBlock ----------
export function ReadOnlyYamlBlock({
  title,
  value,
}: {
  title: string;
  value: Record<string, unknown>;
}) {
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
export function ReadOnlyMetadataTable({
  value,
}: {
  value: Record<string, unknown>;
}) {
  const entries = Object.entries(value);

  return (
    <FormSection title="Metadata">
      <div className="rounded-sm border">
        <div className="grid grid-cols-2 border-b bg-muted/30 px-3 py-2 text-sm font-medium">
          <span>Key</span>
          <span>Value</span>
        </div>
        <div className="divide-y text-muted-foreground">
          {entries.map(([key, item]) => (
            <div
              key={key}
              className="grid grid-cols-2 gap-3 px-3 py-2.5 text-sm"
            >
              <p className="min-w-0 truncate font-mono text-xs">{key}</p>
              <p className="min-w-0 break-words font-mono text-xs leading-5">
                {String(item)}
              </p>
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
  return (
    <FormSection title="SKILL.md Body">
      <Tabs
        defaultValue="rendered"
        onValueChange={setActiveTab}
        className="w-full"
      >
        <div
          className={cn(
            "relative h-[520px] overflow-hidden rounded-sm border",
            activeTab === "raw" ? "bg-muted" : "bg-background",
          )}
        >
          <div className="absolute right-3 top-3 z-10 flex items-center gap-1.5">
            <TabsList className="h-8 bg-card shadow-sm backdrop-blur">
              <TabsTrigger value="rendered" className="h-6 px-2.5 text-xs">
                Rendered
              </TabsTrigger>
              <TabsTrigger value="raw" className="h-6 px-2.5 text-xs">
                Raw
              </TabsTrigger>
            </TabsList>
            <button
              type="button"
              className="inline-flex h-8 w-8 cursor-pointer items-center justify-center rounded-sm bg-card text-muted-foreground shadow-sm backdrop-blur transition-colors hover:text-foreground"
              onClick={() => setExpandOpen(true)}
              aria-label="Expand SKILL.md body"
            >
              <Maximize2 className="h-3.5 w-3.5" />
            </button>
          </div>
          <TabsContent
            value="rendered"
            className="absolute inset-0 m-0 overflow-hidden"
          >
            <ScrollArea className="h-full">
              <div className="min-w-0 p-4">
                <Markdown
                  content={body || ""}
                  className="max-w-full text-sm break-words [overflow-wrap:anywhere] [&_*]:max-w-full [&_*]:break-words [&_*]:[overflow-wrap:anywhere] [&_a]:break-all [&_code]:whitespace-pre-wrap [&_pre]:whitespace-pre-wrap [&_pre]:break-words [&_table]:table-fixed"
                />
              </div>
            </ScrollArea>
          </TabsContent>
          <TabsContent
            value="raw"
            className="absolute inset-0 m-0 overflow-hidden bg-muted"
          >
            <ScrollArea className="h-full" viewportClassName="bg-muted">
              <pre className="min-h-full bg-muted p-4 text-xs font-mono leading-5 whitespace-pre-wrap">
                {body || "(empty)"}
              </pre>
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
          className="h-[90vh] max-h-[90vh] w-[95vw] sm:w-[80vw] md:w-[50vw] min-w-0 max-w-[95vw] sm:max-w-[80vw] md:max-w-[50vw] overflow-hidden border-0 bg-transparent p-0 shadow-none"
        >
          <DialogHeader className="sr-only">
            <DialogTitle>SKILL.md Body</DialogTitle>
          </DialogHeader>
          <Tabs
            value={dialogTab}
            onValueChange={setDialogTab}
            className="h-full"
          >
            <div
              className={cn(
                "relative h-full overflow-hidden rounded-sm border shadow-lg",
                dialogTab === "raw" ? "bg-muted" : "bg-background",
              )}
            >
              <div className="absolute right-3 top-3 z-10 flex items-center gap-1.5">
                <TabsList className="h-8 bg-card shadow-sm backdrop-blur">
                  <TabsTrigger value="rendered" className="h-6 px-2.5 text-xs">
                    Rendered
                  </TabsTrigger>
                  <TabsTrigger value="raw" className="h-6 px-2.5 text-xs">
                    Raw
                  </TabsTrigger>
                </TabsList>
                <DialogClose className="inline-flex h-8 w-8 cursor-pointer items-center justify-center rounded-sm bg-card text-muted-foreground shadow-sm backdrop-blur transition-colors hover:text-foreground">
                  <X className="h-4 w-4" />
                  <span className="sr-only">Close</span>
                </DialogClose>
              </div>
              <TabsContent
                value="rendered"
                className="absolute inset-0 m-0 overflow-hidden"
              >
                <ScrollArea className="h-full">
                  <div className="min-w-0 p-5">
                    <Markdown
                      content={body || ""}
                      className="max-w-full text-sm break-words [overflow-wrap:anywhere] [&_*]:max-w-full [&_*]:break-words [&_*]:[overflow-wrap:anywhere] [&_a]:break-all [&_code]:whitespace-pre-wrap [&_pre]:whitespace-pre-wrap [&_pre]:break-words [&_table]:table-fixed"
                    />
                  </div>
                </ScrollArea>
              </TabsContent>
              <TabsContent
                value="raw"
                className="absolute inset-0 m-0 overflow-hidden bg-muted"
              >
                <ScrollArea className="h-full" viewportClassName="bg-muted">
                  <pre className="min-h-full bg-muted p-5 text-xs font-mono leading-5 whitespace-pre-wrap">
                    {body || "(empty)"}
                  </pre>
                </ScrollArea>
              </TabsContent>
            </div>
          </Tabs>
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
}: {
  skillName: string;
  files: SkillFileEntry[];
  composedSkillMd: string;
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
        if (!bucket.subfolders[segment])
          bucket.subfolders[segment] = { files: [], subfolders: {} };
        bucket = bucket.subfolders[segment];
      }
      bucket.files.push(file);
    }

    const bucketToNodes = (
      bucket: FolderBucket,
      parentPath: string,
    ): TreeNode<FileTreeNodeData>[] => {
      const nodes: TreeNode<FileTreeNodeData>[] = [];
      for (const [folderName, sub] of Object.entries(bucket.subfolders).sort(
        ([a], [b]) => a.localeCompare(b),
      )) {
        const folderPath = parentPath
          ? `${parentPath}/${folderName}`
          : folderName;
        const children = bucketToNodes(sub, folderPath);
        const immediateCount =
          Object.keys(sub.subfolders).length + sub.files.length;
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
      for (const file of bucket.files.sort((a, b) =>
        fileNameFromPath(a.path).localeCompare(fileNameFromPath(b.path)),
      )) {
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
          childCount:
            Object.keys(rootBucket.subfolders).length + rootBucket.files.length,
        },
        children: [
          { data: { id: "skillmd", name: "SKILL.md", type: "skillmd" } },
          ...bucketToNodes(rootBucket, ""),
        ],
      },
    ];
  }, [skillName, files]);

  const downloadUrl = `${getApiBaseUrl()}/skills/serve/${encodeURIComponent(skillName)}/download.zip`;

  return (
    <FormSection title="Files">
      <TooltipProvider>
      <Tree<FileTreeNodeData>
        data={treeData}
        levelsToExpandByDefault={1}
        indentSize={28}
        renderItem={({
          item,
          isExpanded,
          hasChildren,
          onToggle,
          onExpandAll,
          onCollapseAll,
          isAllExpanded,
          isAllCollapsed,
        }) => {
          const isFolder = item.type === "root" || item.type === "folder";
          const isSkillMd = item.type === "skillmd";
          const isFile = item.type === "file";
          const isDownloadable = isSkillMd || isFile;

          const fileDownloadUrl =
            isFile && item.path
              ? `${getApiBaseUrl()}/skills/serve/${encodeURIComponent(skillName)}/files/${item.path.split("/").map(encodeURIComponent).join("/")}`
              : undefined;

          const handleClick = () => {
            if (hasChildren) onToggle();
            else if (isSkillMd) downloadTextAsFile(composedSkillMd, "SKILL.md");
            else if (isFile && fileDownloadUrl) {
              const a = document.createElement("a");
              a.href = fileDownloadUrl;
              a.download = item.name;
              document.body.appendChild(a);
              a.click();
              document.body.removeChild(a);
            }
          };

          return (
            <div
              className={cn(
                "group flex h-9 items-center gap-2 rounded-sm px-2 text-sm transition-colors",
                (hasChildren || isDownloadable) &&
                  "cursor-pointer hover:bg-muted/50",
              )}
              onClick={handleClick}
              onKeyDown={(e) => {
                if (
                  (e.key === "Enter" || e.key === " ") &&
                  (hasChildren || isDownloadable)
                ) {
                  e.preventDefault();
                  handleClick();
                }
              }}
              role={hasChildren || isDownloadable ? "button" : undefined}
              tabIndex={hasChildren || isDownloadable ? 0 : undefined}
              aria-label={
                isDownloadable
                  ? `Download ${item.name}`
                  : isFolder
                    ? `${isExpanded ? "Collapse" : "Expand"} ${item.name}`
                    : item.name
              }
            >
              {hasChildren ? (
                isExpanded ? (
                  <ChevronDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                ) : (
                  <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                )
              ) : (
                <span className="w-3.5 shrink-0" />
              )}

              {isDownloadable ? (
                isSkillMd ? (
                  <>
                    <BookOpen className="h-4 w-4 shrink-0 text-muted-foreground group-hover:hidden" />
                    <Download className="h-4 w-4 shrink-0 text-muted-foreground hidden group-hover:block" />
                  </>
                ) : (
                  <>
                    <FileText className="h-4 w-4 shrink-0 text-muted-foreground group-hover:hidden" />
                    <Download className="h-4 w-4 shrink-0 text-muted-foreground hidden group-hover:block" />
                  </>
                )
              ) : isFolder ? (
                isExpanded ? (
                  <FolderOpen className="h-4 w-4 shrink-0 text-muted-foreground" />
                ) : (
                  <Folder className="h-4 w-4 shrink-0 text-muted-foreground" />
                )
              ) : null}

              <span
                className={cn("font-mono text-xs", isFolder && "font-medium")}
              >
                {item.name}
              </span>

              {isFolder &&
                !isExpanded &&
                item.childCount != null &&
                item.childCount > 0 && (
                  <span className="text-[10px] text-muted-foreground">
                    {item.childCount} item{item.childCount !== 1 ? "s" : ""}
                  </span>
                )}

              {isFile && (
                <>
                  {item.source_type && (
                    <Tooltip>
                      <TooltipTrigger asChild>
                        <span className="inline-flex h-5 shrink-0 items-center rounded-full border border-border/60 bg-transparent px-2 text-[10px] font-medium leading-none text-muted-foreground">
                          {item.source_type}
                        </span>
                      </TooltipTrigger>
                      <TooltipContent className="px-2 py-1 text-xs">
                        Source
                      </TooltipContent>
                    </Tooltip>
                  )}
                  {item.mime_type && (
                    <span className="text-[10px] text-muted-foreground">
                      {item.mime_type}
                    </span>
                  )}
                  {item.file_size_bytes != null && item.file_size_bytes > 0 && (
                    <span className="text-[10px] text-muted-foreground">
                      {formatFileSize(item.file_size_bytes)}
                    </span>
                  )}
                </>
              )}

              {item.type === "root" && (
                <div
                  className="ml-auto flex items-center gap-1 opacity-0 transition-opacity group-hover:opacity-100"
                  onClick={(e) => e.stopPropagation()}
                  onKeyDown={(e) => e.stopPropagation()}
                >
                  <Tooltip>
                    <TooltipTrigger asChild>
                      <Button
                        variant="ghost"
                        size="sm"
                        className="h-6 w-6 p-0"
                        aria-label="Expand all folders"
                        onClick={onExpandAll}
                        disabled={isAllExpanded}
                      >
                        <ChevronsDown className="h-3 w-3" />
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
                        className="h-6 w-6 p-0"
                        aria-label="Collapse all folders"
                        onClick={onCollapseAll}
                        disabled={isAllCollapsed}
                      >
                        <ChevronsUp className="h-3 w-3" />
                      </Button>
                    </TooltipTrigger>
                    <TooltipContent className="px-2 py-1 text-xs">
                      Collapse all
                    </TooltipContent>
                  </Tooltip>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-6 px-2"
                    asChild
                  >
                    <a href={downloadUrl} download>
                      <Download className="h-3 w-3" />
                      <span className="text-[10px]">ZIP</span>
                    </a>
                  </Button>
                </div>
              )}
            </div>
          );
        }}
      />
      </TooltipProvider>
    </FormSection>
  );
}

// ---------- SkillReadOnlyContent ----------

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
  return (
    <div className="flex flex-col gap-6">
      {extraFrontmatter && (
        <ReadOnlyYamlBlock title="Extra Frontmatter" value={extraFrontmatter} />
      )}
      {metadata && <ReadOnlyMetadataTable value={metadata} />}
      <ReadOnlySkillBody body={skillMdBody} />
      <ReadOnlyFileTree
        skillName={skillName}
        files={files}
        composedSkillMd={composedSkillMd}
      />
    </div>
  );
}
