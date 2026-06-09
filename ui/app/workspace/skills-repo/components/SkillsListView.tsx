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
  useListSkillsQuery,
  useDeleteSkillMutation,
} from "@/lib/store/apis/skillsApi";
import { getErrorMessage } from "@/lib/store/apis/baseApi";
import { useGetCoreConfigQuery } from "@/lib/store";
import { getApiBaseUrl } from "@/lib/utils/port";
import { cn } from "@/lib/utils";
import { SkillListItem } from "@/lib/types/skills";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import {
  ArrowDown,
  ArrowUp,
  ArrowUpDown,
  Check,
  Clipboard,
  Download,
  Loader2,
  ChevronLeft,
  ChevronRight,
  FileText,
  MoreHorizontal,
  Package,
  Pencil,
  Plus,
  Search,
  Trash2,
} from "lucide-react";
import { useMemo, useState } from "react";
import { toast } from "sonner";
import { PAGE_SIZE, formatDateShort, useDebouncedValue } from "./helpers";

// ---------- MarketplacePopover ----------

function MarketplacePopover() {
  const [copiedKey, setCopiedKey] = useState<string | null>(null);
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
    navigator.clipboard.writeText(text);
    setCopiedKey(key);
    toast.success("Copied to clipboard");
    setTimeout(() => setCopiedKey(null), 2000);
  };

  return (
    <Popover>
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
              className="w-full flex items-center gap-3 px-3 py-2 text-left hover:bg-muted/50 transition-colors cursor-pointer"
              aria-label={`Copy ${item.label} command`}
              onClick={() => handleCopy(item.key, item.command)}
            >
              <div className="min-w-0 flex-1">
                <p className="text-xs font-medium">{item.label}</p>
                <p className="text-[11px] font-mono text-muted-foreground truncate mt-0.5">
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
  const Icon = isActive
    ? order === "desc"
      ? ArrowDown
      : ArrowUp
    : ArrowUpDown;
  return (
    <Button
      variant="ghost"
      onClick={() => onToggle(column)}
      className="!px-0"
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
    try {
      const res = await fetch(
        `${getApiBaseUrl()}/skills/serve/${skill.name}/download.zip`,
      );
      if (!res.ok) throw new Error("Download failed");
      const blob = await res.blob();
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = `${skill.name}.zip`;
      document.body.appendChild(link);
      link.click();
      document.body.removeChild(link);
      URL.revokeObjectURL(url);
    } catch {
      toast.error("Failed to download skill");
    } finally {
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
            aria-label={`Actions for ${skill.name}`}
          >
            <MoreHorizontal className="h-4 w-4" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem
            className="cursor-pointer"
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
            className="cursor-pointer"
            disabled={!hasEditAccess}
            onSelect={(e) => {
              e.preventDefault();
              onEdit(skill.id);
              setIsOpen(false);
            }}
          >
            <Pencil className="h-4 w-4" />
            Edit
          </DropdownMenuItem>
          <DropdownMenuItem
            variant="destructive"
            className="cursor-pointer"
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

  const [isDownloadingAll, setIsDownloadingAll] = useState(false);
  const [search, setSearch] = useState("");
  const debouncedSearch = useDebouncedValue(search, 300);
  const [offset, setOffset] = useState(0);
  const [sortBy, setSortBy] = useState<SortColumn | null>(null);
  const [sortOrder, setSortOrder] = useState<SortOrder>("asc");

  const { data, isLoading, isFetching } = useListSkillsQuery({
    limit: PAGE_SIZE,
    offset,
    search: debouncedSearch || undefined,
  });

  const skills = data?.skills || [];
  const total = data?.total || 0;

  const sortedSkills = useMemo(() => {
    if (!sortBy) return skills;
    return [...skills].sort((a, b) => {
      let cmp = 0;
      if (sortBy === "name") {
        cmp = a.name.localeCompare(b.name);
      } else if (sortBy === "updated_at") {
        cmp =
          new Date(a.updated_at).getTime() - new Date(b.updated_at).getTime();
      }
      return sortOrder === "desc" ? -cmp : cmp;
    });
  }, [skills, sortBy, sortOrder]);

  const toggleSort = (column: SortColumn) => {
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
    } catch (err: any) {
      toast.error("Failed to delete skill", {
        description: getErrorMessage(err),
      });
    }
  };

  if (isLoading) {
    return <FullPageLoader />;
  }

  return (
    <div className="w-full min-h-0 flex-1 flex flex-col overflow-hidden">
      {/* Header */}
      <div className="flex shrink-0 items-center justify-between mb-4">
        <div>
          <h2 className="text-lg font-semibold">Skills Repository</h2>
          <p className="text-muted-foreground text-sm">
            Manage Agent Skills for distribution to AI coding assistants
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
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
            {isDownloadingAll ? "Downloading..." : "Download All"}
          </Button>
          {isGitAvailable && <MarketplacePopover />}
          {hasCreateAccess && (
            <Button onClick={onCreateNew} size="sm">
              <Plus className="h-4 w-4" />
              New Skill
            </Button>
          )}
        </div>
      </div>

      {/* Search */}
      <div className="flex items-center gap-3 mb-4">
        <div className="relative max-w-sm flex-1">
          <Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
          <Input
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
      </div>

      {/* Table */}
      <div className="min-h-0 grow overflow-hidden rounded-sm border">
        <Table
          containerClassName="h-full overflow-auto"
          className="w-full table-fixed"
        >
          <TableHeader className="bg-muted sticky top-0 z-20">
            <TableRow className="hover:bg-transparent">
              <TableHead className="w-[240px]">
                <SortableHeader
                  column="name"
                  label="Name"
                  sortBy={sortBy}
                  order={sortOrder}
                  onToggle={toggleSort}
                />
              </TableHead>
              <TableHead>Description</TableHead>
              <TableHead className="w-[140px]">Version</TableHead>
              <TableHead className="w-[140px]">Files</TableHead>
              <TableHead className="w-[170px]">
                <SortableHeader
                  column="updated_at"
                  label="Updated"
                  sortBy={sortBy}
                  order={sortOrder}
                  onToggle={toggleSort}
                />
              </TableHead>
              <TableHead
                className={`bg-muted sticky right-0 z-30 w-[56px] text-right ${PIN_SHADOW_RIGHT}`}
              >
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {sortedSkills.length === 0 ? (
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
              sortedSkills.map((skill) => {
                const fileCount = skill.file_count ?? 0;

                return (
                  <TableRow
                    key={skill.id}
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
                    <TableCell className="font-medium font-mono text-sm">
                      {skill.name}
                    </TableCell>
                    <TableCell className="text-muted-foreground text-sm max-w-[300px] truncate">
                      {skill.description}
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
