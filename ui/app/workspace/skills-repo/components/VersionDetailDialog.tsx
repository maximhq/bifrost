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
import {
  useGetSkillQuery,
  useShiftSkillVersionMutation,
} from "@/lib/store/apis/skillsApi";
import { getErrorMessage } from "@/lib/store/apis/baseApi";
import { getApiBaseUrl } from "@/lib/utils/port";
import { SkillFile, SkillFileEntry } from "@/lib/types/skills";
import { Loader2, RefreshCw, Download, X } from "lucide-react";
import { useMemo } from "react";
import { toast } from "sonner";
import { composeFrontmatter } from "./helpers";
import { SkillHeader, SkillReadOnlyContent } from "./shared";

// ---------- VersionDetailDialog ----------

export function VersionDetailDialog({
  skillId,
  version,
  isServingVersion,
  open,
  onOpenChange,
}: {
  skillId: string;
  skillName: string;
  version: string;
  isServingVersion: boolean;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { data: versionData, isLoading } = useGetSkillQuery(
    { id: skillId, version },
    { skip: !open },
  );
  const [shiftVersion, { isLoading: isShifting }] =
    useShiftSkillVersionMutation();

  const skill = versionData?.skill;

  const extraFrontmatter = useMemo(() => {
    if (
      !skill?.extra_frontmatter ||
      Object.keys(skill.extra_frontmatter).length === 0
    )
      return null;
    return skill.extra_frontmatter;
  }, [skill]);

  const metadata = useMemo(() => {
    if (!skill?.metadata || Object.keys(skill.metadata).length === 0)
      return null;
    return skill.metadata as Record<string, unknown>;
  }, [skill]);

  const composedSkillMd = useMemo(() => {
    if (!skill) return "";
    return (
      composeFrontmatter({
        name: skill.name,
        description: skill.description,
        license: skill.license || "",
        compatibility: skill.compatibility || "",
        allowed_tools: skill.allowed_tools || "",
        extra_frontmatter_json: skill.extra_frontmatter
          ? JSON.stringify(skill.extra_frontmatter)
          : "",
        metadata_json: skill.metadata ? JSON.stringify(skill.metadata) : "",
      }) +
      "\n\n" +
      skill.skill_md_body
    );
  }, [skill]);

  const fileEntries: SkillFileEntry[] = useMemo(() => {
    if (!skill?.files) return [];
    return skill.files.map((f: SkillFile) => ({
      path: f.path,
      source_type: f.source_type,
      source_url: f.source_url,
      filepath: f.source_filepath,
      storage_key: f.storage_key,
      mime_type: f.mime_type,
      file_size_bytes: f.file_size_bytes,
    }));
  }, [skill]);

  const downloadUrl = skill
    ? `${getApiBaseUrl()}/skills/serve/${skill.name}/download.zip?version=${encodeURIComponent(version)}`
    : "";

  const handleShiftVersion = async () => {
    try {
      await shiftVersion({ id: skillId, version }).unwrap();
      toast.success(`Shifted to version ${version}`);
      onOpenChange(false);
    } catch (err: any) {
      toast.error("Failed to shift version", {
        description: getErrorMessage(err),
      });
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        showCloseButton={false}
        className="h-[90vh] max-h-[90vh] w-[75vw] min-w-[75vw] max-w-[75vw] overflow-hidden p-0 sm:max-w-[75vw] gap-0"
      >
        <div className="flex items-center justify-between border-b pl-6 pr-2">
          <DialogHeader className="p-0">
            <DialogTitle>Version {version}</DialogTitle>
          </DialogHeader>
          <div className="flex items-center gap-1.5">
            {downloadUrl && (
              <Button
                variant="ghost"
                size="icon"
                className="h-8 w-8"
                asChild
                aria-label="Download ZIP"
              >
                <a href={downloadUrl} download>
                  <Download className="h-4 w-4" />
                  <span className="sr-only">Download ZIP</span>
                </a>
              </Button>
            )}
            <DialogClose asChild>
              <Button
                variant="ghost"
                size="icon"
                className="h-8 w-8"
                aria-label="Close"
              >
                <X className="h-4 w-4" />
                <span className="sr-only">Close</span>
              </Button>
            </DialogClose>
          </div>
        </div>
        <ScrollArea className="h-[calc(90vh-57px)]">
          <div className="p-6 space-y-6">
            {isLoading ? (
              <div className="flex items-center justify-center py-20">
                <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
              </div>
            ) : skill ? (
              <>
                <SkillHeader
                  sticky={false}
                  name={skill.name}
                  version={version}
                  description={skill.description}
                  license={skill.license}
                  compatibility={skill.compatibility}
                  allowedTools={skill.allowed_tools}
                  composedSkillMd={composedSkillMd}
                  decorators={
                    <>
                      {isServingVersion && (
                        <Badge
                          variant="secondary"
                          className="text-xs bg-emerald-100 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-400"
                        >
                          Serving
                        </Badge>
                      )}
                    </>
                  }
                  actions={
                    !isServingVersion ? (
                      <Button
                        size="sm"
                        onClick={handleShiftVersion}
                        disabled={isShifting}
                      >
                        {isShifting ? (
                          <>
                            <Loader2 className="h-3.5 w-3.5 animate-spin" />
                            Shifting...
                          </>
                        ) : (
                          <>
                            <RefreshCw className="h-3.5 w-3.5" />
                            Shift to this version
                          </>
                        )}
                      </Button>
                    ) : undefined
                  }
                />

                <SkillReadOnlyContent
                  skillName={skill.name}
                  skillMdBody={skill.skill_md_body}
                  files={fileEntries}
                  extraFrontmatter={extraFrontmatter}
                  metadata={metadata}
                  composedSkillMd={composedSkillMd}
                />
              </>
            ) : (
              <p className="text-muted-foreground text-sm text-center py-12">
                Version data not found
              </p>
            )}
          </div>
        </ScrollArea>
      </DialogContent>
    </Dialog>
  );
}
