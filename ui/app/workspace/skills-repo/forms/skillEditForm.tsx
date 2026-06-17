"use client";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ScrollArea, ScrollBar } from "@/components/ui/scrollArea";
import { Textarea } from "@/components/ui/textarea";
import { lazy, Suspense, type ComponentProps } from "react";
import { CodeEditor, type CompletionItem } from "@/components/ui/codeEditor";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { validateVersionBump } from "@/lib/validators/skills";
import { cn } from "@/lib/utils";
import type { SkillFileEntry } from "@/lib/types/skills";
import {
  AlertTriangle,
  ArrowLeft,
  Check,
  Copy,
  Eye,
  Loader2,
  Plus,
  Save,
  X,
} from "lucide-react";
import { useRef, useState } from "react";
import {
  type SkillFormReturn,
  composeFrontmatter,
} from "../components/helpers";
import { FormSection } from "../components/shared";
import { FilePreviewPane } from "../components/filePreview";
import { FileManagerSection } from "../components/fileManagerView";
import { MetadataTableEditor } from "../components/metadataEditorTableView";

const LazyMarkdown = lazy(() => import("@/components/ui/markdown").then((m) => ({ default: m.Markdown })));
const Markdown = (props: ComponentProps<typeof LazyMarkdown>) => <Suspense fallback={null}><LazyMarkdown {...props} /></Suspense>;

export function SkillEditView({
  form,
  skillName,
  previousVersion,
  onSave,
  onCancel,
  onBack,
  isSaving,
  mode = "edit",
}: {
  form: SkillFormReturn;
  skillName?: string;
  previousVersion?: string;
  onSave: (serve: boolean) => void;
  onCancel: () => void;
  onBack: () => void;
  isSaving: boolean;
  mode?: "edit" | "create";
}) {
  const isCreate = mode === "create";
  const [bodyTab, setBodyTab] = useState<"edit" | "preview">("edit");
  const [showPreviewDialog, setShowPreviewDialog] = useState(false);
  // Two-pane workspace selection: null = SKILL.md body, otherwise a file index.
  const [selectedFileIndex, setSelectedFileIndex] = useState<number | null>(
    null,
  );
  const [selectedDetailsPane, setSelectedDetailsPane] = useState<
    "details" | "metadata" | "frontmatter" | null
  >("details");
  const selectedFile =
    selectedFileIndex != null ? (form.files[selectedFileIndex] ?? null) : null;

  // The version is asked for in a dialog at save time (not in the form).
  const [versionDialog, setVersionDialog] = useState<{ serve: boolean } | null>(
    null,
  );

  // Lets the file editor commit any buffered ("Save file") content before a version save.
  const flushFileEditRef = useRef<(() => void) | null>(null);

  const openVersionDialog = (serve: boolean) => {
    flushFileEditRef.current?.();
    // Suggest a patch bump for edits when the version still equals the previous one.
    if (!isCreate && previousVersion) {
      const match = previousVersion.match(/^(\d+)\.(\d+)\.(\d+)$/);
      if (match && (form.version === previousVersion || !form.version.trim())) {
        const suggested = `${match[1]}.${match[2]}.${Number(match[3]) + 1}`;
        form.setVersion(suggested);
        form.validateField("version", suggested);
      }
    }
    setVersionDialog({ serve });
  };

  // Closing the dialog restores a valid version so a half-typed value can't
  // leave the footer Save buttons stuck disabled via form.hasErrors.
  const closeVersionDialog = () => {
    if (form.errors.version) {
      const restore = !isCreate && previousVersion ? previousVersion : "1.0.0";
      form.setVersion(restore);
      form.validateField("version", restore);
    }
    setVersionDialog(null);
  };
  const { copy: copyPreviewContent, copied: copiedPreviewContent } =
    useCopyToClipboard({
      successMessage: "Copied raw SKILL.md",
      errorMessage: "Failed to copy raw SKILL.md",
    });

  const previewContent =
    composeFrontmatter({
      name: form.name,
      description: form.description,
      license: form.license,
      compatibility: form.compatibility,
      allowed_tools: form.allowedTools,
      extra_frontmatter_json: form.extraFrontmatterJson,
      metadata_json: form.metadataJson,
    }) +
    "\n\n" +
    form.skillMdBody;

  const filePathCompletions = buildFilePathCompletions(form.files);

  const descriptionLength = form.description.length;
  let descriptionLimitColor = "text-muted-foreground";
  if (descriptionLength > 1024) {
    descriptionLimitColor = "text-destructive";
  } else if (descriptionLength > 900) {
    descriptionLimitColor = "text-yellow-600 dark:text-yellow-500";
  }

  return (
    <div className="animate-in fade-in flex h-full min-h-0 flex-col duration-200 overflow-hidden">
      <div className="shrink-0 overflow-y-auto px-4">
        {/* Top bar with back button integrated */}
        <div className="dark:bg-card flex items-center gap-3 bg-white py-4">
          <Button
            variant="ghost"
            size="sm"
            data-testid="skill-back-btn"
            onClick={onBack}
            aria-label="Go back"
          >
            <ArrowLeft className="h-4 w-4" />
          </Button>
          <div className="text-muted-foreground flex min-w-0 items-center gap-2 text-sm">
            <span>{isCreate ? "Creating" : "Editing"}</span>
            <span className="text-foreground truncate font-mono">
              {isCreate ? form.name || "<new-skill>" : skillName}
            </span>
          </div>
        </div>

        <div className="mb-6 flex gap-3 rounded-sm border border-amber-500/40 bg-amber-50/80 p-3 text-sm text-amber-900 dark:border-amber-400/30 dark:bg-amber-950/40 dark:text-amber-200">
          <AlertTriangle
            className="mt-0.5 h-4 w-4 shrink-0"
            aria-hidden="true"
          />
          <p>
            Files added to a skill can be downloaded from marketplace URLs
            without logging in. Anyone who can reach this Bifrost server can
            request them directly, so do not upload secrets, credentials,
            private code, or other sensitive files.
          </p>
        </div>

        {/* Edit sections */}
        <div className="flex flex-col gap-8">
          {isCreate && (
            <FormSection title="Name">
              <Input
                data-testid="skill-name-input"
                value={form.name}
                onChange={(e) => {
                  form.setName(e.target.value);
                  form.validateField("name", e.target.value);
                }}
                placeholder="my-skill-name"
                className={cn(
                  "font-mono",
                  form.errors.name && "border-destructive",
                )}
              />
              {form.errors.name && (
                <p className="text-destructive text-xs" role="alert">
                  {form.errors.name}
                </p>
              )}
              <p className="text-muted-foreground text-xs">
                Lowercase letters, numbers, and hyphens only.{" "}
                <span className="font-bold">
                  Cannot be changed after creation.
                </span>
              </p>
            </FormSection>
          )}

          {/* Details now render in the right pane, selected from above the file tree. */}
        </div>
      </div>

      {/* Files + SKILL.md two-pane workspace */}
      <div className="min-h-0 flex-1 px-4 pt-4 pb-2">
        <div className="flex h-full min-h-0 gap-3">
          {/* Left: files panel */}
          <div className="bg-card flex min-h-0 w-72 shrink-0 flex-col gap-2">
            <div className="grid gap-1.5">
              <div className="grid grid-cols-2 gap-1.5">
                {[
                  ["details", "Details"],
                  ["metadata", "Metadata"],
                ].map(([pane, label]) => (
                  <button
                    key={pane}
                    type="button"
                    data-testid={`skill-${pane}-pane-btn`}
                    onClick={() => {
                      setSelectedDetailsPane(pane as "details" | "metadata");
                      setSelectedFileIndex(null);
                    }}
                    className={cn(
                      "rounded-md border px-3 py-2 text-left text-xs font-medium transition-colors",
                      selectedDetailsPane === pane
                        ? "border-primary/20 bg-primary/10 text-primary hover:bg-primary/10"
                        : "bg-card hover:bg-muted",
                    )}
                  >
                    {label}
                  </button>
                ))}
              </div>
              <button
                type="button"
                data-testid="skill-frontmatter-pane-btn"
                onClick={() => {
                  setSelectedDetailsPane("frontmatter");
                  setSelectedFileIndex(null);
                }}
                className={cn(
                  "rounded-md border px-3 py-2 text-left text-xs font-medium transition-colors",
                  selectedDetailsPane === "frontmatter"
                    ? "border-primary/20 bg-primary/10 text-primary hover:bg-primary/10"
                    : "bg-card hover:bg-muted",
                )}
              >
                Extra Frontmatter
              </button>
            </div>
            <div className="bg-card flex min-h-0 flex-1 flex-col rounded-md border">
              <div className="flex py-2 items-center border-b px-3">
                <span className="text-sm font-semibold">Files</span>
              </div>
              <ScrollArea
                className="min-h-0 flex-1 p-1"
                viewportClassName="overflow-x-auto"
              >
                <FileManagerSection
                  files={form.files}
                  onAddFile={form.addFile}
                  onRemoveFile={form.removeFile}
                  onUpdateFile={form.updateFile}
                  readOnly={false}
                  selectedIndex={
                    selectedDetailsPane == null ? selectedFileIndex : null
                  }
                  onSelectFile={(index) => {
                    setSelectedDetailsPane(null);
                    setSelectedFileIndex(index);
                  }}
                  bodySelected={
                    selectedFile == null && selectedDetailsPane == null
                  }
                  hasBodyError={!form.skillMdBody.trim()}
                  onSelectBody={() => {
                    setSelectedDetailsPane(null);
                    setSelectedFileIndex(null);
                  }}
                />
                <ScrollBar orientation="horizontal" />
              </ScrollArea>
            </div>
          </div>

          {/* Right: editor for the selected item */}
          <div className="flex min-h-0 grow flex-col overflow-auto">
            {selectedDetailsPane === "details" ? (
              <DetailsEditorPane
                form={form}
                descriptionLength={descriptionLength}
                descriptionLimitColor={descriptionLimitColor}
              />
            ) : selectedDetailsPane === "metadata" ? (
              <MetadataEditorPane form={form} />
            ) : selectedDetailsPane === "frontmatter" ? (
              <ExtraFrontmatterEditorPane form={form} />
            ) : selectedFile ? (
              <FilePreviewPane
                key={selectedFile.path}
                file={selectedFile}
                skillName={skillName ?? form.name ?? ""}
                mode="edit"
                registerFlush={(flush) => {
                  flushFileEditRef.current = flush;
                }}
                onFileUpdate={(updates) => {
                  if (selectedFileIndex == null) return;
                  form.updateFile(selectedFileIndex, updates);
                }}
                onContentChange={(editedText) => {
                  if (selectedFileIndex == null) return;
                  if (selectedFile.source_type === "dataurl") {
                    // Re-encode edited text as a data URI; drop old storage refs
                    // so the backend creates a new blob for the new version.
                    const dataurl = `data:${selectedFile.mime_type || "text/plain"};base64,${btoa(unescape(encodeURIComponent(editedText)))}`;
                    form.updateFile(selectedFileIndex, {
                      dataurl,
                      blob_id: undefined,
                      storage_key: undefined,
                    });
                  } else {
                    // text source: submit raw content; drop old storage refs.
                    form.updateFile(selectedFileIndex, {
                      content: editedText,
                      blob_id: undefined,
                      storage_key: undefined,
                    });
                  }
                }}
              />
            ) : (
              <div className="flex h-full min-h-0 flex-col overflow-hidden rounded-sm border">
                <div
                  className="flex h-9 shrink-0 items-center gap-1 border-b px-2"
                  role="tablist"
                  aria-label="Body editor tabs"
                >
                  <button
                    type="button"
                    className={cn(
                      "px-3 py-1 text-xs rounded-sm transition-colors cursor-pointer",
                      bodyTab === "edit"
                        ? "bg-muted font-medium"
                        : "text-muted-foreground hover:text-foreground",
                    )}
                    data-testid="skill-body-tab-edit"
                    onClick={() => setBodyTab("edit")}
                    role="tab"
                    aria-selected={bodyTab === "edit"}
                  >
                    Edit
                  </button>
                  <button
                    type="button"
                    className={cn(
                      "px-3 py-1 text-xs rounded-sm transition-colors cursor-pointer",
                      bodyTab === "preview"
                        ? "bg-muted font-medium"
                        : "text-muted-foreground hover:text-foreground",
                    )}
                    data-testid="skill-body-tab-preview"
                    onClick={() => setBodyTab("preview")}
                    role="tab"
                    aria-selected={bodyTab === "preview"}
                  >
                    Preview
                  </button>
                  <span className="text-muted-foreground ml-auto pr-1 text-xs">
                    Use <code className="font-mono">@</code> to reference files
                  </span>
                </div>
                <div className="min-h-0 grow overflow-y-auto">
                  {bodyTab === "edit" ? (
                    <CodeEditor
                      className="z-0 w-full"
                      code={form.skillMdBody}
                      lang="markdown"
                      onChange={(value: string) => form.setSkillMdBody(value)}
                      height="100%"
                      wrap
                      customCompletions={filePathCompletions}
                      options={{
                        showVerticalScrollbar: true,
                        scrollBeyondLastLine: false,
                        lineNumbers: "on",
                        alwaysConsumeMouseWheel: false,
                        quickSuggestions: false,
                      }}
                    />
                  ) : (
                    <div className="p-4">
                      <Markdown
                        content={form.skillMdBody || ""}
                        className="text-sm"
                      />
                    </div>
                  )}
                </div>
                {(form.errors.skill_md_body || form.bodyWarning) && (
                  <div className="shrink-0 border-t px-3 py-1.5">
                    {form.errors.skill_md_body && (
                      <p className="text-destructive text-xs" role="alert">
                        {form.errors.skill_md_body}
                      </p>
                    )}
                    {form.bodyWarning && (
                      <p
                        className="text-xs text-yellow-600 dark:text-yellow-500"
                        role="status"
                      >
                        {form.bodyWarning}
                      </p>
                    )}
                  </div>
                )}
              </div>
            )}
          </div>
        </div>
      </div>

      <div className="dark:bg-card/95 sticky bottom-0 z-20 mt-4 flex items-center justify-end gap-2 border-t bg-white/95 px-1 py-3 backdrop-blur">
        <Button
          variant="ghost"
          size="sm"
          data-testid="skill-cancel-btn"
          onClick={onCancel}
          className="text-muted-foreground hover:bg-transparent hover:text-red-600 dark:hover:text-red-400"
        >
          Cancel
        </Button>
        <Button
          variant="outline"
          size="sm"
          data-testid="skill-preview-btn"
          onClick={() => setShowPreviewDialog(true)}
        >
          <Eye className="h-3.5 w-3.5" />
          Preview Raw SKILL.md
        </Button>
        {isCreate ? (
          <Button
            size="sm"
            data-testid="skill-create-save-btn"
            onClick={() => openVersionDialog(true)}
            disabled={isSaving || form.hasErrors}
          >
            {isSaving ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <Plus className="h-3.5 w-3.5" />
            )}
            {isSaving ? "Creating..." : "Create Skill"}
          </Button>
        ) : (
          <>
            <Button
              variant="outline"
              size="sm"
              data-testid="skill-save-btn"
              onClick={() => openVersionDialog(false)}
              disabled={isSaving || form.hasErrors}
            >
              {isSaving ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              ) : (
                <Save className="h-3.5 w-3.5" />
              )}
              {isSaving ? "Saving..." : "Save"}
            </Button>
            <Button
              size="sm"
              data-testid="skill-save-serve-btn"
              onClick={() => openVersionDialog(true)}
              disabled={isSaving || form.hasErrors}
            >
              {isSaving ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              ) : (
                <Save className="h-3.5 w-3.5" />
              )}
              {isSaving ? "Saving..." : "Save & Serve"}
            </Button>
          </>
        )}
      </div>

      {/* Preview SKILL.md Dialog */}
      <Dialog open={showPreviewDialog} onOpenChange={setShowPreviewDialog}>
        <DialogContent
          showCloseButton={false}
          className="border-0 p-0 shadow-none sm:w-4/5 sm:max-w-4xl md:w-1/2 md:max-w-3xl"
        >
          <DialogHeader className="sr-only">
            <DialogTitle>SKILL.md Preview</DialogTitle>
          </DialogHeader>
          <div className="bg-muted relative overflow-hidden rounded-sm border shadow-lg">
            <div className="absolute top-3 right-3 z-10 flex items-center gap-1">
              <Button
                variant="ghost"
                size="icon"
                data-testid="skill-preview-copy-btn"
                className="bg-background/70 text-muted-foreground hover:bg-background/90 hover:text-foreground h-8 w-8 rounded-sm"
                onClick={() => copyPreviewContent(previewContent)}
                aria-label={
                  copiedPreviewContent
                    ? "Raw SKILL.md copied"
                    : "Copy raw SKILL.md"
                }
              >
                {copiedPreviewContent ? (
                  <Check className="h-4 w-4" />
                ) : (
                  <Copy className="h-4 w-4" />
                )}
              </Button>
              <DialogClose className="text-muted-foreground hover:bg-background/80 hover:text-foreground cursor-pointer rounded-sm p-1.5 transition-colors">
                <X className="h-4 w-4" />
                <span className="sr-only">Close</span>
              </DialogClose>
            </div>
            <ScrollArea className="h-dvh" viewportClassName="bg-muted">
              <pre className="bg-muted min-h-96 p-5 pr-24 font-mono text-xs leading-5 whitespace-pre-wrap">
                {previewContent}
              </pre>
            </ScrollArea>
          </div>
        </DialogContent>
      </Dialog>

      {/* Version dialog — asked for at save time */}
      <Dialog
        open={versionDialog != null}
        onOpenChange={(open) => !open && closeVersionDialog()}
      >
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle>
              {isCreate ? "Create skill" : "Save new version"}
            </DialogTitle>
          </DialogHeader>
          <VersionDialogBody
            form={form}
            isCreate={isCreate}
            previousVersion={previousVersion}
            isSaving={isSaving}
            serve={versionDialog?.serve ?? false}
            onClose={closeVersionDialog}
            onSave={(serve) => {
              setVersionDialog(null);
              onSave(serve);
            }}
          />
        </DialogContent>
      </Dialog>
    </div>
  );
}

/** Build autocomplete items for referencing skill files with @[name](path) syntax. */
function buildFilePathCompletions(files: SkillFileEntry[]): CompletionItem[] {
  const completions: CompletionItem[] = [];
  const folderPaths = new Set<string>();

  for (const file of files) {
    if (!file.path) continue;
    const pathParts = file.path.split("/").filter(Boolean);
    const fileName = pathParts.at(-1) ?? file.path;
    const rootRelativePath = `./${file.path}`;

    for (let i = 0; i < pathParts.length - 1; i++) {
      folderPaths.add(pathParts.slice(0, i + 1).join("/"));
    }

    completions.push({
      label: fileName,
      insertText: `@[${fileName}](${rootRelativePath})`,
      type: "object" as const,
      description: rootRelativePath,
      documentation: `Full path: ${rootRelativePath}`,
    });
  }

  for (const folderPath of folderPaths) {
    const folderName =
      folderPath.split("/").filter(Boolean).pop() ?? folderPath;
    const rootRelativePath = `./${folderPath}/`;
    completions.push({
      label: folderName,
      insertText: `@[${folderName}](${rootRelativePath})`,
      type: "folder" as const,
      description: rootRelativePath,
      documentation: `Full path: ${rootRelativePath}`,
    });
  }

  return completions.sort(
    (a, b) => a.description?.localeCompare(b.description ?? "") ?? 0,
  );
}

function DetailsEditorPane({
  form,
  descriptionLength,
  descriptionLimitColor,
}: {
  form: SkillFormReturn;
  descriptionLength: number;
  descriptionLimitColor: string;
}) {
  return (
    <div className="flex h-full min-h-0 flex-col overflow-hidden">
      <div className="flex h-9 shrink-0 items-center gap-2 px-1">
        <span className="text-sm font-semibold">Details</span>
        <span className="text-muted-foreground text-xs">
          Edit the skill description and spec fields
        </span>
      </div>
      <ScrollArea className="min-h-0 flex-1 rounded-sm border">
        <div className="flex flex-col gap-8 p-4">
          <FormSection title="Description">
            <div className="flex flex-col gap-2">
              <Textarea
                data-testid="skill-description-input"
                value={form.description}
                onChange={(e) => {
                  form.setDescription(e.target.value);
                  form.validateField("description", e.target.value);
                }}
                placeholder="What does this skill do?"
                rows={3}
                className={
                  form.errors.description ? "border-destructive" : undefined
                }
              />
              <div className="flex justify-between">
                <span
                  className={`text-xs tabular-nums transition-colors ${descriptionLimitColor}`}
                >
                  {descriptionLength}/1024
                </span>
                {form.errors.description ? (
                  <p className="text-destructive text-xs" role="alert">
                    {form.errors.description}
                  </p>
                ) : (
                  <span />
                )}
              </div>
            </div>
          </FormSection>

          <FormSection title="Spec Fields">
            <div className="grid grid-cols-3 gap-4">
              <div className="flex flex-col gap-1.5">
                <Label className="text-muted-foreground text-xs">License</Label>
                <Input
                  data-testid="skill-license-input"
                  value={form.license}
                  onChange={(e) => form.setLicense(e.target.value)}
                  placeholder="MIT (optional)"
                  className="text-sm"
                />
              </div>
              <div className="flex flex-col gap-1.5">
                <Label className="text-muted-foreground text-xs">
                  Compatibility
                </Label>
                <Input
                  data-testid="skill-compatibility-input"
                  value={form.compatibility}
                  onChange={(e) => form.setCompatibility(e.target.value)}
                  placeholder="Claude Code, Codex (optional)"
                  className="text-sm"
                />
              </div>
              <div className="flex flex-col gap-1.5">
                <Label className="text-muted-foreground text-xs">
                  Allowed Tools
                </Label>
                <Input
                  data-testid="skill-allowed-tools-input"
                  value={form.allowedTools}
                  onChange={(e) => form.setAllowedTools(e.target.value)}
                  placeholder="Bash Read Grep (optional)"
                  className="text-sm"
                />
              </div>
            </div>
          </FormSection>
        </div>
      </ScrollArea>
    </div>
  );
}

function MetadataEditorPane({ form }: { form: SkillFormReturn }) {
  return (
    <div className="flex h-full min-h-0 flex-col overflow-hidden">
      <div className="flex h-9 shrink-0 items-center gap-2 px-1">
        <span className="text-sm font-semibold">Metadata</span>
        <span className="text-muted-foreground text-xs">
          Flat key-value pairs nested under{" "}
          <code className="font-mono">metadata:</code> in SKILL.md
        </span>
      </div>
      <ScrollArea className="min-h-0 flex-1 rounded-sm border">
        <div className="p-3">
          <MetadataTableEditor
            metadataJson={form.metadataJson}
            onChange={(json) => {
              form.setMetadataJson(json);
              form.validateField("metadata", json);
            }}
            error={form.errors.metadata}
          />
        </div>
      </ScrollArea>
    </div>
  );
}

function ExtraFrontmatterEditorPane({ form }: { form: SkillFormReturn }) {
  return (
    <div className="flex h-full min-h-0 flex-col overflow-hidden">
      <div className="flex h-9 shrink-0 items-center gap-2 px-1">
        <span className="text-sm font-semibold">Extra Frontmatter</span>
        <span className="text-muted-foreground text-xs">
          Valid JSON merged into the SKILL.md YAML frontmatter
        </span>
      </div>
      <div className="flex min-h-0 flex-1 flex-col gap-2 rounded-sm border p-3">
        <div className="min-h-0 flex-1 overflow-hidden">
          <CodeEditor
            className="z-0 w-full"
            code={form.extraFrontmatterJson}
            lang="json"
            onChange={(value: string) => {
              form.setExtraFrontmatterJson(value);
              form.validateField("extra_frontmatter", value);
            }}
            height="100%"
            wrap
            options={{
              showVerticalScrollbar: true,
              scrollBeyondLastLine: false,
              lineNumbers: "on",
              alwaysConsumeMouseWheel: false,
            }}
          />
        </div>
        {form.errors.extra_frontmatter && (
          <p className="text-destructive shrink-0 text-xs" role="alert">
            {form.errors.extra_frontmatter}
          </p>
        )}
      </div>
    </div>
  );
}

/** The version input + confirm/cancel inside the version dialog. */
function VersionDialogBody({
  form,
  isCreate,
  previousVersion,
  isSaving,
  serve,
  onClose,
  onSave,
}: {
  form: SkillFormReturn;
  isCreate: boolean;
  previousVersion?: string;
  isSaving: boolean;
  serve: boolean;
  onClose: () => void;
  onSave: (serve: boolean) => void;
}) {
  const bumpError =
    !isCreate && previousVersion
      ? validateVersionBump(form.version, previousVersion)
      : null;
  const versionError = form.errors.version || bumpError;
  const canSave = !!form.version.trim() && !versionError && !isSaving;

  const submit = () => {
    if (!canSave) return;
    onSave(serve);
  };

  return (
    <>
      <div className="flex flex-col gap-1.5">
        <Label className="text-muted-foreground text-xs">Version</Label>
        <div className="flex items-center gap-2">
          {!isCreate && previousVersion && (
            <>
              <span className="text-muted-foreground font-mono text-sm">
                {previousVersion}
              </span>
              <span className="text-muted-foreground/50">→</span>
            </>
          )}
          <Input
            autoFocus
            data-testid="skill-version-input"
            value={form.version}
            onChange={(e) => {
              form.setVersion(e.target.value);
              form.validateField("version", e.target.value);
            }}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                submit();
              }
            }}
            placeholder="1.0.0"
            className={cn(
              "max-w-44 font-mono text-sm",
              versionError && "border-destructive",
            )}
          />
        </div>
        {versionError ? (
          <p className="text-destructive text-xs" role="alert">
            {versionError}
          </p>
        ) : (
          !isCreate && (
            <p className="text-muted-foreground text-xs">
              Bump major (2.x.x), minor (1.1.x), or patch (1.0.1).
            </p>
          )
        )}
      </div>
      <div className="mt-4 flex justify-end gap-2">
        <Button variant="ghost" size="sm" onClick={onClose}>
          Cancel
        </Button>
        <Button
          size="sm"
          data-testid="skill-version-confirm-btn"
          disabled={!canSave}
          onClick={submit}
        >
          {isCreate ? "Create" : serve ? "Save & Serve" : "Save"}
        </Button>
      </div>
    </>
  );
}
