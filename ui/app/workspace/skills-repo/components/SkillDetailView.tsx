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
import FullPageLoader from "@/components/fullPageLoader";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
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
  useGetSkillQuery,
  useUpdateSkillMutation,
  useDeleteSkillMutation,
  useListSkillVersionsQuery,
} from "@/lib/store/apis/skillsApi";
import { getErrorMessage } from "@/lib/store/apis/baseApi";
import { SkillFile, SkillVersionSummary } from "@/lib/types/skills";
import { validateVersionBump } from "@/lib/validators/skills";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import {
  ArrowDown,
  ArrowLeft,
  ArrowUp,
  ArrowUpDown,
  ChevronLeft,
  ChevronRight,
  MoreHorizontal,
  Pencil,
  Loader2,
  Trash2,
} from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import {
  type SkillFormState,
  composeFrontmatter,
  formatDate,
  useSkillForm,
} from "./helpers";
import { SkillHeader, FormSection } from "./shared";
import { SkillFormFields } from "./SkillFormFields";
import { SkillEditView } from "./SkillEditView";
import { VersionDetailDialog } from "./VersionDetailDialog";

// ---------- SkillDetailView ----------

export function SkillDetailView({
  skillId,
  isEditing,
  setIsEditing,
  onBack,
}: {
  skillId: string;
  isEditing: boolean;
  setIsEditing: (editing: boolean) => void;
  onBack: () => void;
}) {
  const hasEditAccess = useRbac(
    RbacResource.SkillsRepository,
    RbacOperation.Update,
  );
  const hasDeleteAccess = useRbac(
    RbacResource.SkillsRepository,
    RbacOperation.Delete,
  );

  const { data: skillData, isLoading } = useGetSkillQuery(skillId);
  const [updateSkill, { isLoading: isUpdating }] = useUpdateSkillMutation();
  const [deleteSkill, { isLoading: isDeleting }] = useDeleteSkillMutation();

  const [selectedVersion, setSelectedVersion] = useState<SkillVersionSummary | null>(
    null,
  );
  const [versionSortOrder, setVersionSortOrder] = useState<
    "asc" | "desc" | null
  >(null);
  const [versionsPage, setVersionsPage] = useState(0);
  const [deleteDialogOpen, setDeleteDialogOpen] = useState(false);
  const versionsPageSize = 10;

  const form = useSkillForm();
  const skill = skillData?.skill;

  const { data: versionsData } = useListSkillVersionsQuery(
    {
      id: skillId,
      limit: versionsPageSize,
      offset: versionsPage * versionsPageSize,
      sort_by: versionSortOrder ? "version" : undefined,
      order: versionSortOrder || undefined,
    },
    { skip: !skill },
  );

  const highestVersion =
    skill?.highest_version || skill?.latest_version || "0.0.0";

  const getSkillFormState = useMemo(() => {
    if (!skill) return null;
    const state: SkillFormState = {
      name: skill.name,
      description: skill.description,
      license: skill.license || "",
      compatibility: skill.compatibility || "",
      allowedTools: skill.allowed_tools || "",
      extraFrontmatterJson:
        skill.extra_frontmatter && Object.keys(skill.extra_frontmatter).length > 0
          ? JSON.stringify(skill.extra_frontmatter, null, 2)
          : "",
      metadataJson:
        skill.metadata && Object.keys(skill.metadata).length > 0
          ? JSON.stringify(skill.metadata, null, 2)
          : "",
      skillMdBody: skill.skill_md_body,
      version: highestVersion || "1.0.0",
      files: skill.files
        ? skill.files.map((f: SkillFile) => ({
            path: f.path,
            source_type: f.source_type,
            source_url: f.source_url,
            storage_key: f.storage_key,
            blob_id: f.blob_id,
            mime_type: f.mime_type,
            file_size_bytes: f.file_size_bytes,
          }))
        : [],
    };
    return state;
  }, [highestVersion, skill]);

  // Populate form when skill loads
  useEffect(() => {
    if (getSkillFormState) form.reset(getSkillFormState);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [getSkillFormState]);

  const handleSave = async (serve: boolean) => {
    if (!form.runValidation()) return;
    const bumpErr = validateVersionBump(form.version, highestVersion);
    if (bumpErr) {
      toast.error("Invalid version", { description: bumpErr });
      return;
    }

    try {
      const { name: _name, ...payload } = form.getPayload();
      await updateSkill({
        id: skillId,
        data: { ...payload, serve },
      }).unwrap();
      toast.success(
        serve
          ? "Skill saved and now serving this version"
          : "Version saved successfully",
      );
      setIsEditing(false);
    } catch (err: unknown) {
      toast.error("Failed to update skill", {
        description: getErrorMessage(err),
      });
    }
  };

  const handleDelete = async () => {
    try {
      await deleteSkill(skillId).unwrap();
      toast.success("Skill deleted");
      onBack();
    } catch (err: unknown) {
      toast.error("Failed to delete skill", {
        description: getErrorMessage(err),
      });
    }
  };

  const handleCancelEdit = () => {
    if (getSkillFormState) form.reset(getSkillFormState);
    setIsEditing(false);
  };

  if (isLoading) {
    return <FullPageLoader />;
  }

  if (!skill) {
    return (
      <div className="w-full min-h-0 flex-1 flex flex-col items-center justify-center p-4">
        <p className="text-muted-foreground text-sm">Skill not found</p>
        <Button variant="outline" size="sm" className="mt-3" onClick={onBack}>
          <ArrowLeft className="h-3.5 w-3.5" />
          Back to list
        </Button>
      </div>
    );
  }

  return (
    <div className="w-full flex-1 flex flex-col relative">
      {/* Content */}
      {isEditing ? (
        <SkillEditView
          form={form}
          skillName={skill.name}
          previousVersion={highestVersion}
          onSave={handleSave}
          onCancel={handleCancelEdit}
          onBack={handleCancelEdit}
          isSaving={isUpdating}
        />
      ) : (
        <>
          <SkillHeader
            name={skill.name}
            version={skill.latest_version}
            description={skill.description}
            license={skill.license}
            compatibility={skill.compatibility}
            allowedTools={skill.allowed_tools}
            composedSkillMd={
              composeFrontmatter({
                name: skill.name,
                description: skill.description,
                license: skill.license || "",
                compatibility: skill.compatibility || "",
                allowed_tools: skill.allowed_tools || "",
                extra_frontmatter_json:
                  skill.extra_frontmatter &&
                  Object.keys(skill.extra_frontmatter).length > 0
                    ? JSON.stringify(skill.extra_frontmatter, null, 2)
                    : "",
                metadata_json:
                  skill.metadata && Object.keys(skill.metadata).length > 0
                    ? JSON.stringify(skill.metadata, null, 2)
                    : "",
              }) +
              "\n\n" +
              skill.skill_md_body
            }
            downloadSkillName={skill.name}
            onBack={onBack}
            actions={
              <>
                {(hasEditAccess || hasDeleteAccess) && (
                  <DropdownMenu>
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
                      {hasEditAccess && (
                        <DropdownMenuItem
                          className="cursor-pointer"
                          onSelect={() => setIsEditing(true)}
                        >
                          <Pencil className="h-4 w-4" />
                          Edit
                        </DropdownMenuItem>
                      )}
                      {hasDeleteAccess && (
                        <DropdownMenuItem
                          variant="destructive"
                          className="cursor-pointer"
                          disabled={isDeleting}
                          onSelect={(event) => {
                            event.preventDefault();
                            setDeleteDialogOpen(true);
                          }}
                        >
                          <Trash2 className="h-4 w-4" />
                          Delete
                        </DropdownMenuItem>
                      )}
                    </DropdownMenuContent>
                  </DropdownMenu>
                )}
                <AlertDialog
                  open={deleteDialogOpen}
                  onOpenChange={setDeleteDialogOpen}
                >
                  <AlertDialogContent>
                    <AlertDialogHeader>
                      <AlertDialogTitle>Delete {skill.name}?</AlertDialogTitle>
                      <AlertDialogDescription>
                        This action cannot be undone. The skill, its files, and
                        version history will be permanently deleted.
                      </AlertDialogDescription>
                    </AlertDialogHeader>
                    <AlertDialogFooter>
                      <AlertDialogCancel>Cancel</AlertDialogCancel>
                      <AlertDialogAction onClick={handleDelete} disabled={isDeleting}>
                        {isDeleting ? (
                          <><Loader2 className="h-3.5 w-3.5 animate-spin" /> Deleting...</>
                        ) : (
                          "Delete skill"
                        )}
                      </AlertDialogAction>
                    </AlertDialogFooter>
                  </AlertDialogContent>
                </AlertDialog>
              </>
            }
          />

          <div className="mt-6 flex">
            <div className="w-full min-w-0 flex flex-col gap-5">
              <SkillFormFields skill={skill} />

              {/* Version History Table */}
              {versionsData && versionsData.versions.length > 0 && (
                <FormSection title="Version History">
                  <div className="overflow-hidden rounded-sm border">
                    <Table>
                      <TableHeader className="bg-muted">
                        <TableRow className="hover:bg-transparent">
                          <TableHead className="w-[200px]">
                            <Button
                              variant="ghost"
                              aria-label="Sort by version"
                              onClick={() => {
                                setVersionsPage(0);
                                if (versionSortOrder === null)
                                  setVersionSortOrder("asc");
                                else if (versionSortOrder === "asc")
                                  setVersionSortOrder("desc");
                                else setVersionSortOrder(null);
                              }}
                              className="!px-0"
                            >
                              Version
                              {versionSortOrder === "asc" ? (
                                <ArrowUp className="h-4 w-4 text-foreground" />
                              ) : versionSortOrder === "desc" ? (
                                <ArrowDown className="h-4 w-4 text-foreground" />
                              ) : (
                                <ArrowUpDown className="h-4 w-4" />
                              )}
                            </Button>
                          </TableHead>
                          <TableHead>Created</TableHead>
                        </TableRow>
                      </TableHeader>
                      <TableBody>
                        {versionsData.versions.map((v) => {
                          const isServing = v.version === skill.latest_version;
                          return (
                            <TableRow
                              key={v.id}
                              className="group hover:bg-muted/50 cursor-pointer transition-colors"
                              tabIndex={0}
                              onClick={() => setSelectedVersion(v)}
                              onKeyDown={(e) => {
                                if (e.key === "Enter" || e.key === " ") {
                                  e.preventDefault();
                                  setSelectedVersion(v);
                                }
                              }}
                            >
                              <TableCell>
                                <div className="flex items-center gap-2">
                                  <span className="font-mono text-sm font-medium">
                                    {v.version}
                                  </span>
                                  {isServing && (
                                    <Badge
                                      variant="secondary"
                                      className="text-[10px] bg-emerald-100 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-400"
                                    >
                                      Serving
                                    </Badge>
                                  )}
                                </div>
                              </TableCell>
                              <TableCell className="text-muted-foreground text-sm">
                                {formatDate(v.created_at)}
                              </TableCell>
                            </TableRow>
                          );
                        })}
                      </TableBody>
                    </Table>
                  </div>
                  {/* Pagination */}
                  {versionsData.total > 0 && (
                    <div className="flex shrink-0 items-center justify-between pt-3 text-xs">
                      <div className="text-muted-foreground flex items-center gap-2">
                        {(versionsPage * versionsPageSize + 1).toLocaleString()}
                        –
                        {Math.min(
                          (versionsPage + 1) * versionsPageSize,
                          versionsData.total,
                        ).toLocaleString()}{" "}
                        of {versionsData.total.toLocaleString()} entries
                      </div>
                      <div className="flex items-center gap-2">
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => setVersionsPage((p) => p - 1)}
                          disabled={versionsPage === 0}
                          aria-label="Previous page"
                        >
                          <ChevronLeft className="size-3" />
                        </Button>
                        <div className="flex items-center gap-1">
                          <span>Page</span>
                          <span>{versionsPage + 1}</span>
                          <span>
                            of{" "}
                            {Math.ceil(versionsData.total / versionsPageSize)}
                          </span>
                        </div>
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => setVersionsPage((p) => p + 1)}
                          disabled={
                            (versionsPage + 1) * versionsPageSize >=
                            versionsData.total
                          }
                          aria-label="Next page"
                        >
                          <ChevronRight className="size-3" />
                        </Button>
                      </div>
                    </div>
                  )}
                </FormSection>
              )}
            </div>
          </div>
        </>
      )}

      {/* Version Detail Dialog */}
      {selectedVersion && skill && (
        <VersionDetailDialog
          skillId={skillId}
          version={selectedVersion.version}
          isServingVersion={selectedVersion.version === skill.latest_version}
          open={true}
          onOpenChange={(open) => {
            if (!open) setSelectedVersion(null);
          }}
        />
      )}
    </div>
  );
}
