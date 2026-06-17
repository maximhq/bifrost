"use client";

import { PIN_SHADOW_RIGHT } from "@/components/table/columnPinning";
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
import FullPageLoader from "@/components/fullPageLoader";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdownMenu";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import {
  useListSkillsQuery,
  useDeleteSkillMutation,
  useGetAllSkillsVersionQuery,
  useBumpAllSkillsVersionMutation,
} from "@/lib/store/apis/skillsApi";
import { getErrorMessage } from "@/lib/store/apis/baseApi";
import { useGetCoreConfigQuery } from "@/lib/store";
import { getApiBaseUrl } from "@/lib/utils/port";
import { cn } from "@/lib/utils";
import { SkillListItem, AllSkillsVersionBump } from "@/lib/types/skills";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import {
  ArrowDown,
  ArrowUp,
  ArrowUpDown,
  Check,
  Clipboard,
  Info,
  Download,
  Loader2,
  ChevronLeft,
  ChevronRight,
  FileText,
  MoreHorizontal,
  Package,
  Plus,
  Search,
  Trash2,
  ChevronDown,
  ArrowUpRight,
  BookOpenText,
} from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import { PAGE_SIZE, formatDateShort, useDebouncedValue } from "./helpers";

const SKILLS_REPOSITORY_DOCS_URL =
  "https://docs.getbifrost.ai/features/skills-repository";

// ---------- MarketplacePopover ----------

function MarketplacePopover() {
  const [copiedKey, setCopiedKey] = useState<string | null>(null);
  const [open, setOpen] = useState(false);
  const marketplaceBaseUrl = getApiBaseUrl();

  const items = [
    {
      key: "claude",
      label: "Claude Code",
      command: `claude plugin marketplace add ${marketplaceBaseUrl}/skills/serve/claude-code/.claude-plugin/marketplace.json`,
    },
    {
      key: "codex",
      label: "Codex",
      command: `codex plugin marketplace add ${marketplaceBaseUrl}/skills/serve/codex`,
    },
  ];

  const handleCopy = (key: string, text: string) => {
    navigator.clipboard
      .writeText(text)
      .then(() => {
        setCopiedKey(key);
        setOpen(false);
        toast.success("Copied to clipboard");
        setTimeout(() => setCopiedKey(null), 2000);
      })
      .catch(() => {
        toast.error("Failed to copy to clipboard");
      });
  };

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button variant="outline" size="sm">
          <Package className="h-3.5 w-3.5" />
          Register as Marketplace
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-auto max-w-md p-0">
        <div className="px-3 py-2 border-b">
          <p className="text-xs font-medium text-muted-foreground">
            Copy CLI command to register this repository
          </p>
        </div>
        <div className="py-1">
          {items.map((item) => (
            <button
              key={item.key}
              data-testid={`skill-copy-marketplace-${item.key}`}
              className="w-full flex items-center gap-3 px-3 py-2 text-left hover:bg-muted/50 transition-colors cursor-pointer"
              aria-label={`Copy ${item.label} command`}
              onClick={() => handleCopy(item.key, item.command)}
            >
              <div className="min-w-0 flex-1">
                <p className="text-xs font-medium">{item.label}</p>
                <p className="text-xs font-mono text-muted-foreground truncate mt-0.5">
                  {item.command}
                </p>
              </div>
              {copiedKey === item.key ? (
                <Check className="h-3.5 w-3.5 shrink-0 text-green-500" />
              ) : (
                <Clipboard className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
              )}
            </button>
          ))}
        </div>
      </PopoverContent>
    </Popover>
  );
}

// ---------- SortableHeader ----------

type SortColumn = "name" | "updated_at";
type SortOrder = "asc" | "desc";

function SortableHeader({
  column,
  label,
  sortBy,
  order,
  onToggle,
}: {
  column: SortColumn;
  label: string;
  sortBy: SortColumn | null;
  order: SortOrder;
  onToggle: (column: SortColumn) => void;
}) {
  const isActive = sortBy === column;
  let Icon = ArrowUpDown;
  if (isActive && order === "desc") Icon = ArrowDown;
  else if (isActive) Icon = ArrowUp;
  return (
    <Button
      variant="ghost"
      onClick={() => onToggle(column)}
      className="!px-0"
      data-testid={`skill-sort-${column}`}
      aria-label={`Sort by ${label}`}
    >
      {label}
      <Icon className={cn("h-4 w-4", isActive && "text-foreground")} />
    </Button>
  );
}

// ---------- SkillActionsMenu ----------

function SkillActionsMenu({
  skill,
  hasEditAccess,
  hasDeleteAccess,
  isDeleting,
  onEdit,
  onDelete,
}: {
  skill: SkillListItem;
  hasEditAccess: boolean;
  hasDeleteAccess: boolean;
  isDeleting: boolean;
  onEdit: (id: string) => void;
  onDelete: (id: string) => Promise<void>;
}) {
  const [isOpen, setIsOpen] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [isDownloading, setIsDownloading] = useState(false);

  const handleDownload = async () => {
    setIsDownloading(true);
    let url: string | undefined;
    try {
      const res = await fetch(
        `${getApiBaseUrl()}/skills/serve/${encodeURIComponent(skill.name)}/download.zip`,
      );
      if (!res.ok) throw new Error("Download failed");
      const blob = await res.blob();
      url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = `${skill.name}.zip`;
      document.body.appendChild(link);
      link.click();
      document.body.removeChild(link);
    } catch {
      toast.error("Failed to download skill");
    } finally {
      if (url) URL.revokeObjectURL(url);
      setIsDownloading(false);
    }
  };

  return (
    <>
      <DropdownMenu open={isOpen} onOpenChange={setIsOpen}>
        <DropdownMenuTrigger asChild>
          <Button
            variant="ghost"
            size="icon"
            className="h-8 w-8"
            data-testid={`skill-actions-menu-${skill.name}`}
            aria-label={`Actions for ${skill.name}`}
          >
            <MoreHorizontal className="h-4 w-4" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem
            className="cursor-pointer"
            data-testid={`skill-download-btn-${skill.name}`}
            disabled={isDownloading}
            onSelect={(e) => {
              e.preventDefault();
              handleDownload();
              setIsOpen(false);
            }}
          >
            {isDownloading ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : (
              <Download className="h-4 w-4" />
            )}
            {isDownloading ? "Downloading..." : "Download ZIP"}
          </DropdownMenuItem>
          <DropdownMenuItem
            variant="destructive"
            className="cursor-pointer"
            data-testid={`skill-delete-btn-${skill.name}`}
            disabled={!hasDeleteAccess || isDeleting}
            onSelect={(e) => {
              e.preventDefault();
              setDeleteOpen(true);
              setIsOpen(false);
            }}
          >
            <Trash2 className="h-4 w-4" />
            Delete
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete {skill.name}?</AlertDialogTitle>
            <AlertDialogDescription>
              This action cannot be undone. The skill, its files, and version
              history will be permanently deleted.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              data-testid="skill-delete-confirm-btn"
              onClick={() => onDelete(skill.id)}
              disabled={isDeleting}
            >
              {isDeleting ? (
                <>
                  <Loader2 className="h-3.5 w-3.5 animate-spin" /> Deleting...
                </>
              ) : (
                "Delete skill"
              )}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

// ---------- SkillsListView ----------

export function SkillsListView({
  onSelectSkill,
  onCreateNew,
}: {
  onSelectSkill: (id: string, edit?: boolean) => void;
  onCreateNew: () => void;
}) {
  const hasCreateAccess = useRbac(
    RbacResource.SkillsRepository,
    RbacOperation.Create,
  );
  const hasEditAccess = useRbac(
    RbacResource.SkillsRepository,
    RbacOperation.Update,
  );
  const hasDeleteAccess = useRbac(
    RbacResource.SkillsRepository,
    RbacOperation.Delete,
  );
  const { data: bifrostConfig } = useGetCoreConfigQuery({});
  const isGitAvailable = bifrostConfig?.is_git_available ?? false;
  const [deleteSkill, { isLoading: isDeleting }] = useDeleteSkillMutation();
  const { data: allSkillsVersionData, refetch: refetchAllSkillsVersion } =
    useGetAllSkillsVersionQuery();
  const [bumpAllSkillsVersion, { isLoading: isBumpingAllSkillsVersion }] =
    useBumpAllSkillsVersionMutation();

  const [isDownloadingAll, setIsDownloadingAll] = useState(false);
  const [search, setSearch] = useState("");
  const debouncedSearch = useDebouncedValue(search, 300);
  const [offset, setOffset] = useState(0);
  const [sortBy, setSortBy] = useState<SortColumn | null>(null);
  const [sortOrder, setSortOrder] = useState<SortOrder>("asc");

  const { data, isLoading, isFetching, isError, refetch } = useListSkillsQuery({
    limit: PAGE_SIZE,
    offset,
    search: debouncedSearch || undefined,
    sort_by: sortBy || undefined,
    order: sortBy ? sortOrder : undefined,
  });

  const skills = data?.skills || [];
  const total = data?.total || 0;

  const toggleSort = (column: SortColumn) => {
    setOffset(0);
    if (sortBy === column) {
      if (sortOrder === "asc") {
        setSortOrder("desc");
      } else {
        setSortBy(null);
        setSortOrder("asc");
      }
    } else {
      setSortBy(column);
      setSortOrder("asc");
    }
  };

  const handleDeleteSkill = async (id: string) => {
    try {
      await deleteSkill(id).unwrap();
      toast.success("Skill deleted");
    } catch (err: unknown) {
      toast.error("Failed to delete skill", {
        description: getErrorMessage(err),
      });
    }
  };

  const handleBumpAllSkillsVersion = async (bump: AllSkillsVersionBump) => {
    try {
      const result = await bumpAllSkillsVersion({ bump }).unwrap();
      toast.success(`All-skills version bumped to ${result.version}`);
      refetchAllSkillsVersion();
    } catch (err: unknown) {
      toast.error("Failed to bump all-skills version", {
        description: getErrorMessage(err),
      });
    }
  };

  if (isLoading) {
    return <FullPageLoader />;
  }

  if (isError) {
    return (
      <div className="flex flex-col items-center justify-center gap-3 py-20">
        <p className="text-muted-foreground text-sm">Failed to load skills</p>
        <Button variant="outline" size="sm" onClick={refetch}>
          Retry
        </Button>
      </div>
    );
  }

  // True empty state: no skills at all (not just filtered to zero)
  if (total === 0 && !search && !isFetching) {
    return (
      <div
        className="flex h-full w-full flex-col items-center justify-center gap-4 py-16 text-center"
        data-testid="skills-repo-empty-state"
      >
        <div className="text-muted-foreground">
          <BookOpenText className="h-24 w-24" strokeWidth={1} />
        </div>
        <div className="flex flex-col gap-1">
          <h1 className="text-muted-foreground text-xl font-medium">
            Create, version, and share Agent Skills from Bifrost
          </h1>
          <div className="text-muted-foreground mx-auto mt-2 max-w-xl text-sm font-normal">
            Manage SKILL.md instructions and supporting files in one place,
            publish immutable versions, and expose them as installable plugins
            for Claude Code, Codex, and other skill-aware clients.
          </div>
          <div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
            <Button
              variant="outline"
              aria-label="Read more about skills (opens in new tab)"
              data-testid="skills-button-read-more"
              onClick={() => {
                window.open(
                  `${SKILLS_REPOSITORY_DOCS_URL}?utm_source=bfd`,
                  "_blank",
                  "noopener,noreferrer",
                );
              }}
            >
              Read more{" "}
              <ArrowUpRight className="text-muted-foreground h-3 w-3" />
            </Button>
            {hasCreateAccess && (
              <Button
                aria-label="Create your first skill"
                data-testid="skill-create-btn"
                onClick={onCreateNew}
              >
                Create Skill
              </Button>
            )}
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="w-full flex-1 flex flex-col">
      {/* Header */}
      <div className="flex shrink-0 items-center justify-between mb-4">
        <div>
          <div className="flex items-center gap-2">
            <h2 className="text-lg font-semibold">Skills Repository</h2>
            <Badge aria-label="Skills Repository is in beta">Beta</Badge>
          </div>
          <p className="text-muted-foreground text-sm">
            Manage Agent Skills for distribution to AI coding assistants
          </p>
        </div>
        <div className="flex items-center gap-2">
          {isGitAvailable ? (
            <MarketplacePopover />
          ) : (
            <Tooltip>
              <TooltipTrigger asChild>
                <span tabIndex={0}>
                  <Button variant="outline" size="sm" disabled>
                    <Package className="h-3.5 w-3.5" />
                    Register as Marketplace
                  </Button>
                </span>
              </TooltipTrigger>
              <TooltipContent side="bottom">
                <p className="max-w-xs text-xs">
                  Git is not available on the server. Install git and restart
                  Bifrost to enable marketplace registration for Claude Code and
                  Codex.
                </p>
              </TooltipContent>
            </Tooltip>
          )}
          <Button
            variant="outline"
            size="sm"
            data-testid="skill-download-all-btn"
            onClick={async () => {
              setIsDownloadingAll(true);
              try {
                const res = await fetch(
                  `${getApiBaseUrl()}/skills/serve/all/download.zip`,
                );
                if (!res.ok) throw new Error("Download failed");
                const blob = await res.blob();
                const url = URL.createObjectURL(blob);
                const link = document.createElement("a");
                link.href = url;
                link.download = "all-skills.zip";
                document.body.appendChild(link);
                link.click();
                document.body.removeChild(link);
                URL.revokeObjectURL(url);
              } catch {
                toast.error("Failed to download skills");
              } finally {
                setIsDownloadingAll(false);
              }
            }}
            disabled={!skills?.length || isDownloadingAll}
          >
            {isDownloadingAll ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : (
              <Download className="h-4 w-4" />
            )}
            {isDownloadingAll ? "Downloading..." : "Download All Skills"}
          </Button>
          {hasCreateAccess && (
            <Button
              data-testid="skill-create-btn"
              onClick={onCreateNew}
              size="sm"
            >
              <Plus className="h-4 w-4" />
              New Skill
            </Button>
          )}
        </div>
      </div>

      {/* Search + All-skills version */}
      <div className="flex items-center gap-3 mb-4">
        <div className="relative max-w-sm flex-1">
          <Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
          <Input
            data-testid="skill-search-input"
            aria-label="Search skills by name"
            placeholder="Search skills..."
            value={search}
            onChange={(e) => {
              setSearch(e.target.value);
              setOffset(0);
            }}
            className="pl-9"
          />
        </div>
        <div className="flex items-center gap-2 text-xs">
          <Tooltip>
            <TooltipTrigger asChild>
              <Info className="h-3.5 w-3.5 text-muted-foreground cursor-help" />
            </TooltipTrigger>
            <TooltipContent side="bottom" className="max-w-xs text-xs">
              When registered as a marketplace, an "all-skills" plugin is
              available that installs every skill in this repository at once.
              Its version bumps automatically on changes; use the dropdown to
              bump manually if needed.
            </TooltipContent>
          </Tooltip>
          <span className="text-muted-foreground whitespace-nowrap">
            all-skills version
          </span>
          {hasEditAccess ? (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <button
                  data-testid="skill-bump-version-btn"
                  disabled={isBumpingAllSkillsVersion}
                  className="cursor-pointer disabled:opacity-50 disabled:cursor-not-allowed"
                >
                  <Badge
                    variant="secondary"
                    className="font-mono text-xs hover:bg-muted transition-colors"
                  >
                    {isBumpingAllSkillsVersion ? (
                      <Loader2 className="h-3 w-3 animate-spin" />
                    ) : (
                      <>
                        {allSkillsVersionData?.version ?? "0.0.0"}
                        <ChevronDown className="h-3 w-3" />
                      </>
                    )}
                  </Badge>
                </button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                {(["patch", "minor", "major"] as AllSkillsVersionBump[]).map(
                  (bump) => (
                    <DropdownMenuItem
                      key={bump}
                      className="cursor-pointer capitalize"
                      disabled={isBumpingAllSkillsVersion}
                      onSelect={() => handleBumpAllSkillsVersion(bump)}
                    >
                      Bump {bump}
                    </DropdownMenuItem>
                  ),
                )}
              </DropdownMenuContent>
            </DropdownMenu>
          ) : (
            <Badge variant="secondary" className="font-mono text-xs">
              {allSkillsVersionData?.version ?? "0.0.0"}
            </Badge>
          )}
        </div>
      </div>

      {/* Table */}
      <div className="grow overflow-hidden rounded-sm border">
        <Table
          containerClassName="h-full overflow-auto"
          className="w-full table-fixed"
        >
          <TableHeader className="bg-muted sticky top-0 z-20">
            <TableRow className="hover:bg-transparent">
              <TableHead className="w-60">
                <SortableHeader
                  column="name"
                  label="Name"
                  sortBy={sortBy}
                  order={sortOrder}
                  onToggle={toggleSort}
                />
              </TableHead>
              <TableHead>Description</TableHead>
              <TableHead className="w-36">Version</TableHead>
              <TableHead className="w-36">Files</TableHead>
              <TableHead className="w-44">
                <SortableHeader
                  column="updated_at"
                  label="Updated"
                  sortBy={sortBy}
                  order={sortOrder}
                  onToggle={toggleSort}
                />
              </TableHead>
              <TableHead
                className={`bg-muted sticky right-0 z-30 w-14 text-right ${PIN_SHADOW_RIGHT}`}
              >
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {skills.length === 0 ? (
              <TableRow>
                <TableCell colSpan={6} className="text-center py-12">
                  <div className="flex flex-col items-center gap-2">
                    <FileText className="h-8 w-8 text-muted-foreground" />
                    <p className="text-muted-foreground text-sm">
                      {search
                        ? "No skills match your search"
                        : "No skills created yet"}
                    </p>
                    {!search && hasCreateAccess && (
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={onCreateNew}
                        className="mt-2"
                      >
                        <Plus className="h-3.5 w-3.5" />
                        Create your first skill
                      </Button>
                    )}
                  </div>
                </TableCell>
              </TableRow>
            ) : (
              skills.map((skill) => {
                const fileCount = skill.file_count ?? 0;

                return (
                  <TableRow
                    key={skill.id}
                    data-testid={`skill-row-${skill.name}`}
                    className="group hover:bg-muted/50 cursor-pointer transition-colors"
                    tabIndex={0}
                    onClick={() => onSelectSkill(skill.id)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter" || e.key === " ") {
                        e.preventDefault();
                        onSelectSkill(skill.id);
                      }
                    }}
                  >
                    <TableCell className="w-60 max-w-60 overflow-hidden font-medium font-mono text-sm">
                      <div className="max-w-full truncate" title={skill.name}>
                        {skill.name}
                      </div>
                    </TableCell>
                    <TableCell className="text-muted-foreground overflow-hidden text-sm">
                      <div className="truncate" title={skill.description}>
                        {skill.description}
                      </div>
                    </TableCell>
                    <TableCell>
                      <Badge
                        variant="secondary"
                        className="font-mono text-xs px-2.5 py-1"
                      >
                        {skill.latest_version}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <span className="text-muted-foreground text-xs">
                        <span className="font-mono text-foreground">
                          {fileCount}
                        </span>{" "}
                        files
                      </span>
                    </TableCell>
                    <TableCell className="text-muted-foreground text-sm">
                      {formatDateShort(skill.updated_at)}
                    </TableCell>
                    <TableCell
                      className={`group-hover:bg-muted dark:bg-card dark:group-hover:bg-muted sticky right-0 z-20 bg-white text-right ${PIN_SHADOW_RIGHT}`}
                      onClick={(e) => e.stopPropagation()}
                    >
                      <SkillActionsMenu
                        skill={skill}
                        hasEditAccess={hasEditAccess}
                        hasDeleteAccess={hasDeleteAccess}
                        isDeleting={isDeleting}
                        onEdit={(id) => onSelectSkill(id, true)}
                        onDelete={handleDeleteSkill}
                      />
                    </TableCell>
                  </TableRow>
                );
              })
            )}
          </TableBody>
        </Table>
      </div>

      {/* Pagination */}
      {total > 0 && (
        <div className="flex shrink-0 items-center justify-between text-xs">
          <div className="text-muted-foreground flex items-center gap-2">
            {(offset + 1).toLocaleString()}-
            {Math.min(offset + PAGE_SIZE, total).toLocaleString()} of{" "}
            {total.toLocaleString()} entries
          </div>
          <div className="flex items-center gap-2">
            <Button
              variant="ghost"
              size="sm"
              data-testid="skill-pagination-prev"
              onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
              disabled={offset === 0 || isFetching}
              aria-label="Previous page"
            >
              <ChevronLeft className="size-3" />
            </Button>
            <div className="flex items-center gap-1">
              <span>Page</span>
              <span>{Math.floor(offset / PAGE_SIZE) + 1}</span>
              <span>of {Math.ceil(total / PAGE_SIZE)}</span>
            </div>
            <Button
              variant="ghost"
              size="sm"
              data-testid="skill-pagination-next"
              onClick={() => setOffset(offset + PAGE_SIZE)}
              disabled={offset + PAGE_SIZE >= total || isFetching}
              aria-label="Next page"
            >
              <ChevronRight className="size-3" />
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
